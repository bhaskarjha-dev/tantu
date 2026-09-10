package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// RuntimeHubInfo holds runtime discovery metadata for a running Hub process.
type RuntimeHubInfo struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	WebAddr   string    `json:"web_addr"`
	WebPort   int       `json:"web_port"`
	P2PAddr   string    `json:"p2p_addr"`
	P2PPort   int       `json:"p2p_port"`
	Transport string    `json:"transport"`
}

var (
	overrideMu              sync.RWMutex
	defaultStoreDirOverride string
	defaultWebAddrOverride  string
)

// SetDefaultStoreDirOverride sets a package-level store directory override for testing.
func SetDefaultStoreDirOverride(dir string) {
	overrideMu.Lock()
	defaultStoreDirOverride = dir
	overrideMu.Unlock()
}

// SetDefaultWebAddrOverride sets a package-level fallback web address override for testing.
func SetDefaultWebAddrOverride(addr string) {
	overrideMu.Lock()
	defaultWebAddrOverride = addr
	overrideMu.Unlock()
}

// GetRuntimeFilePath returns the absolute path to hub.json in storeDir.
// If storeDir is empty, defaultStoreDirOverride or pairing.DefaultStoreDir() is used.
func GetRuntimeFilePath(storeDir string) string {
	if storeDir == "" {
		overrideMu.RLock()
		override := defaultStoreDirOverride
		overrideMu.RUnlock()
		if override != "" {
			storeDir = override
		} else {
			d, err := pairing.DefaultStoreDir()
			if err == nil {
				storeDir = d
			}
		}
	}
	return filepath.Join(storeDir, "hub.json")
}

// WriteRuntimeInfo writes RuntimeHubInfo to hub.json atomically via a temporary file with 0600 permissions.
func WriteRuntimeInfo(storeDir string, info RuntimeHubInfo) error {
	filePath := GetRuntimeFilePath(storeDir)
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create store dir: %w", err)
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime info: %w", err)
	}

	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write tmp runtime info: %w", err)
	}

	// Remove target first on Windows to avoid access denied error on rename
	_ = os.Remove(filePath)
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename runtime info: %w", err)
	}
	return nil
}

// RemoveRuntimeInfo removes the hub.json file.
func RemoveRuntimeInfo(storeDir string) error {
	filePath := GetRuntimeFilePath(storeDir)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadRuntimeInfo reads and unmarshals hub.json.
func ReadRuntimeInfo(storeDir string) (*RuntimeHubInfo, error) {
	filePath := GetRuntimeFilePath(storeDir)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var info RuntimeHubInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// HubStatus represents the status response from GET /api/status.
type HubStatus struct {
	Status    string         `json:"status"`
	Version   string         `json:"version"`
	Identity  IdentityStatus `json:"identity"`
	Transport string         `json:"transport"`
	P2PAddr   string         `json:"p2p_addr"`
	WebAddr   string         `json:"web_addr"`
	Peers     []PeerStatus   `json:"peers"`
}

// IdentityStatus holds local node identity metadata.
type IdentityStatus struct {
	Fingerprint string `json:"fingerprint"`
	SAS         string `json:"sas"`
}

// PeerStatus holds metadata of a paired peer.
type PeerStatus struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
	SAS         string `json:"sas"`
}

func cleanWebAddr(addr string) string {
	if addr == "" {
		return DefaultWebAddr
	}
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	return addr
}

func probeAddr(webAddr string) (*HubStatus, bool) {
	webAddr = cleanWebAddr(webAddr)
	client := &http.Client{
		Timeout: 200 * time.Millisecond,
	}
	url := fmt.Sprintf("http://%s/api/status", webAddr)
	resp, err := client.Get(url)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false
	}

	var status HubStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, false
	}

	return &status, true
}

// ProbeHub checks if a local tantu Hub is running.
// If webAddr is explicitly passed (and not equal to DefaultWebAddr), it probes that address.
// Otherwise, it checks hub.json for dynamic port discovery before falling back to DefaultWebAddr.
func ProbeHub(webAddr string) (*HubStatus, bool) {
	fallbackAddr := DefaultWebAddr
	overrideMu.RLock()
	overrideWeb := defaultWebAddrOverride
	overrideMu.RUnlock()
	if overrideWeb != "" {
		fallbackAddr = overrideWeb
	}

	cleaned := cleanWebAddr(webAddr)
	if webAddr != "" && cleaned != DefaultWebAddr && cleaned != fallbackAddr {
		return probeAddr(cleaned)
	}

	// 1. Attempt daemon discovery via hub.json
	if info, err := ReadRuntimeInfo(""); err == nil && info != nil && info.WebAddr != "" {
		if status, ok := probeAddr(info.WebAddr); ok {
			return status, true
		}
	}

	// 2. Fall back to default web addr
	return probeAddr(fallbackAddr)
}

// DelegateSend transmits a file or text snippet via the local Hub's POST /api/drop/upload endpoint.
// If targetPeer is non-empty, it instructs the Hub to send to that specific peer.
func DelegateSend(webAddr string, filePath string, text string, targetPeer string) error {
	webAddr = cleanWebAddr(webAddr)
	targetURL := fmt.Sprintf("http://%s/api/drop/upload", webAddr)

	if filePath != "" {
		f, err := os.Open(filePath)
		if err != nil {
			return fmt.Errorf("open file for delegation: %w", err)
		}
		defer f.Close()

		pr, pw := io.Pipe()
		writer := multipart.NewWriter(pw)

		go func() {
			var pipeErr error
			defer func() {
				if pipeErr != nil {
					_ = pw.CloseWithError(pipeErr)
				} else {
					_ = pw.Close()
				}
			}()

			if targetPeer != "" {
				if err := writer.WriteField("peer", targetPeer); err != nil {
					pipeErr = err
					return
				}
			}

			part, err := writer.CreateFormFile("file", filepath.Base(filePath))
			if err != nil {
				pipeErr = err
				return
			}
			if _, err := io.Copy(part, f); err != nil {
				pipeErr = err
				return
			}
			pipeErr = writer.Close()
		}()

		req, err := http.NewRequest(http.MethodPost, targetURL, pr)
		if err != nil {
			return fmt.Errorf("create delegation request: %w", err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("X-CLI-Version", HubVersion)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("delegate upload request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("hub returned error (status %d): %s", resp.StatusCode, string(body))
		}
		return nil
	}

	// Text drop
	bodyMap := map[string]string{
		"text": text,
	}
	if targetPeer != "" {
		bodyMap["peer"] = targetPeer
	}
	reqBody, _ := json.Marshal(bodyMap)
	req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create text delegation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CLI-Version", HubVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("delegate text request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("hub returned error (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// DelegateOpen forwards an OAuth authorization URL via the local Hub's POST /api/relay/open endpoint.
// If targetPeer is non-empty, it instructs the Hub to open with that specific peer.
func DelegateOpen(webAddr string, targetURL string, targetPeer string) error {
	webAddr = cleanWebAddr(webAddr)
	endpoint := fmt.Sprintf("http://%s/api/relay/open", webAddr)

	bodyMap := map[string]string{
		"url": targetURL,
	}
	if targetPeer != "" {
		bodyMap["peer"] = targetPeer
	}
	reqBody, _ := json.Marshal(bodyMap)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create relay delegation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CLI-Version", HubVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("delegate relay request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("hub returned relay error (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}
