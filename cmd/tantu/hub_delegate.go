package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
)

// delegatedSendResult carries the Hub's sender-side truth back to the CLI
// so `tantu send` can report the same operation ID and destination the
// dashboard and `tantu transfers` show.
type delegatedSendResult struct {
	OperationID string `json:"operation_id"`
	Destination string `json:"destination"`
	Verified    bool   `json:"verified"`
}

// hubError carries the Hub's structured error contract across the local IPC
// boundary so CLI output can report retry safety instead of a bare string.
// Error() preserves the historical human format.
type hubError struct {
	StatusCode    int
	Raw           string
	Message       string
	Code          string
	PlainMessage  string
	RetrySafe     bool
	DuplicateRisk bool
	DataSafe      bool
	NextAction    string
	OperationID   string
	Destination   string
}

func (e *hubError) Error() string {
	return fmt.Sprintf("hub returned error (status %d): %s", e.StatusCode, e.Raw)
}

func newHubError(status int, body []byte) *hubError {
	raw := strings.TrimSpace(string(body))
	he := &hubError{StatusCode: status, Raw: raw, Message: raw}
	var parsed struct {
		Message       string `json:"message"`
		Code          string `json:"code"`
		PlainMessage  string `json:"plain_message"`
		RetrySafe     bool   `json:"retry_safe"`
		DuplicateRisk bool   `json:"duplicate_risk"`
		DataSafe      bool   `json:"data_safe"`
		NextAction    string `json:"next_action"`
		OperationID   string `json:"operation_id"`
		Destination   string `json:"destination"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Message != "" {
			he.Message = parsed.Message
		}
		he.Code = parsed.Code
		he.PlainMessage = parsed.PlainMessage
		he.RetrySafe = parsed.RetrySafe
		he.DuplicateRisk = parsed.DuplicateRisk
		he.DataSafe = parsed.DataSafe
		he.NextAction = parsed.NextAction
		he.OperationID = parsed.OperationID
		he.Destination = parsed.Destination
	}
	if he.Message == "" {
		he.Message = fmt.Sprintf("hub returned status %d", status)
	}
	return he
}

func decodeDelegatedResult(body []byte) *delegatedSendResult {
	res := &delegatedSendResult{}
	_ = json.Unmarshal(body, res)
	return res
}

// delegateSendWithName is the CLI-side equivalent of hub.DelegateSend, but it
// carries the caller's --name through the local Hub. The Hub IPC API accepts a
// name for JSON text drops and derives file names from the multipart filename,
// so using this helper avoids losing an explicit label in delegation mode.
func delegateSendWithName(webAddr, filePath, text, name, targetPeer string, timeout time.Duration) (*delegatedSendResult, error) {
	return delegateSendWithNameFromStore("", webAddr, filePath, text, name, targetPeer, timeout)
}

func delegateSendWithNameFromStore(storeDir, webAddr, filePath, text, name, targetPeer string, timeout time.Duration) (*delegatedSendResult, error) {
	if webAddr == "" {
		webAddr = hub.DefaultWebAddr
	}
	webAddr = normalizeLocalWebAddr(webAddr)
	if !validLocalWebAddr(webAddr) {
		return nil, fmt.Errorf("hub web address must be loopback: %q", webAddr)
	}
	client := &http.Client{
		Timeout: hubRequestTimeout(timeout),
		// Delegation is intended for a local Hub only. Do not follow a
		// redirect to an unrelated host, and do not route loopback traffic
		// through an environment-configured HTTP proxy.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{Proxy: nil},
	}
	defer client.CloseIdleConnections()

	if filePath != "" {
		return delegateFileWithName(client, storeDir, webAddr, filePath, name, targetPeer)
	}

	bodyMap := map[string]string{"text": text}
	if name != "" {
		bodyMap["name"] = name
	}
	if targetPeer != "" {
		bodyMap["peer"] = targetPeer
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("marshal text delegation body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://"+webAddr+"/api/drop/upload", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create text delegation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CLI-Version", hub.HubVersion)
	addLocalIPCTokenForStore(req, storeDir)
	return doDelegationRequest(client, req)
}

func delegateFileWithName(client *http.Client, storeDir, webAddr, filePath, name, targetPeer string) (*delegatedSendResult, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file for delegation: %w", err)
	}
	defer file.Close()

	if name == "" {
		name = filepath.Base(filePath)
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()
	writeErr := make(chan error, 1)
	go func() {
		var pipeErr error
		defer func() {
			if pipeErr != nil {
				_ = pw.CloseWithError(pipeErr)
			} else {
				_ = pw.Close()
			}
			writeErr <- pipeErr
		}()

		if targetPeer != "" {
			if err := writer.WriteField("peer", targetPeer); err != nil {
				pipeErr = err
				return
			}
		}
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			pipeErr = err
			return
		}
		if _, err := io.Copy(part, file); err != nil {
			pipeErr = err
			return
		}
		pipeErr = writer.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+webAddr+"/api/drop/upload", pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		<-writeErr
		return nil, fmt.Errorf("create file delegation request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-CLI-Version", hub.HubVersion)
	addLocalIPCTokenForStore(req, storeDir)

	resp, err := client.Do(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		<-writeErr
		return nil, fmt.Errorf("delegate upload request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Stop a producer that is still streaming when the Hub rejects the
		// request early, then drain its completion before returning.
		_ = pr.CloseWithError(fmt.Errorf("hub returned status %d", resp.StatusCode))
		<-writeErr
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, newHubError(resp.StatusCode, body)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if pipeErr := <-writeErr; pipeErr != nil {
		return nil, fmt.Errorf("prepare delegation upload: %w", pipeErr)
	}
	return decodeDelegatedResult(body), nil
}

func doDelegationRequest(client *http.Client, req *http.Request) (*delegatedSendResult, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("delegate text request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, newHubError(resp.StatusCode, body)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return decodeDelegatedResult(body), nil
}

func normalizeLocalWebAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	return strings.TrimRight(addr, "/")
}

func validLocalWebAddr(addr string) bool {
	u, err := url.Parse("http://" + normalizeLocalWebAddr(addr))
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return isLoopbackHostname(u.Hostname())
}

func addLocalIPCToken(req *http.Request) {
	addLocalIPCTokenForStore(req, "")
}

func addLocalIPCTokenForStore(req *http.Request, storeDir string) {
	if req == nil {
		return
	}
	if token := hub.RuntimeIPCTokenForAddr(storeDir, req.URL.Host); token != "" {
		req.Header.Set(hub.IPCTokenHeader, token)
	}
}

func hubRequestTimeout(timeout time.Duration) time.Duration {
	const grace = 30 * time.Second
	if timeout <= 0 {
		return 5*time.Minute + grace
	}
	return timeout + grace
}
