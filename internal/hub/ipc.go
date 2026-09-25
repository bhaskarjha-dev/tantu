package hub

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

const IPCTokenHeader = "X-Tantu-IPC-Token"

// RuntimeHubInfo holds runtime discovery metadata for a running Hub process.
type RuntimeHubInfo struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	WebAddr   string    `json:"web_addr"`
	WebPort   int       `json:"web_port"`
	P2PAddr   string    `json:"p2p_addr"`
	P2PPort   int       `json:"p2p_port"`
	Transport string    `json:"transport"`
	IPCToken  string    `json:"ipc_token,omitempty"`
}

var (
	overrideMu              sync.RWMutex
	defaultStoreDirOverride string
	defaultWebAddrOverride  string
	runtimeLockMu           sync.Mutex
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

const (
	maxRuntimeInfoSize = 64 * 1024
	runtimeLockName    = ".hub.json.lock"
	runtimeLockWait    = 2 * time.Second
	runtimeLockStale   = 30 * time.Second
)

// withRuntimeLock serializes the complete read/write/remove transaction across
// Hub processes sharing a store directory. The directory lock protects against
// a second process replacing hub.json between an ownership check and removal;
// runtimeLockMu separately serializes callers within this process.
func withRuntimeLock(storeDir string, fn func() error) error {
	// The on-disk lock coordinates independent processes. This mutex also
	// serializes goroutines in this process, which is important on Windows:
	// several concurrent Mkdir/RemoveAll calls for the same lock directory can
	// otherwise surface a transient Access is denied error while one caller is
	// releasing it.
	runtimeLockMu.Lock()
	defer runtimeLockMu.Unlock()

	dir := filepath.Dir(GetRuntimeFilePath(storeDir))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create store dir: %w", err)
	}
	lockPath := filepath.Join(dir, runtimeLockName)
	deadline := time.Now().Add(runtimeLockWait)
	for {
		err := os.Mkdir(lockPath, 0700)
		if err == nil {
			break
		}
		// Windows can report ERROR_ACCESS_DENIED rather than
		// ERROR_ALREADY_EXISTS while another process owns the lock directory.
		// Only treat that error as contention when the lock path is actually
		// present; a real permission failure must still be surfaced.
		lockExists := os.IsExist(err)
		if !lockExists && os.IsPermission(err) {
			_, statErr := os.Lstat(lockPath)
			lockExists = statErr == nil
		}
		if !lockExists {
			return fmt.Errorf("acquire runtime lock: %w", err)
		}
		if stat, statErr := os.Stat(lockPath); statErr == nil && time.Since(stat.ModTime()) > runtimeLockStale {
			// Runtime operations are short. Reclaim only an abandoned lock;
			// never remove a live lock merely because a caller is impatient.
			_ = os.RemoveAll(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("runtime metadata lock timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer func() { _ = os.RemoveAll(lockPath) }()
	return fn()
}

func validateRuntimeInfo(info RuntimeHubInfo) error {
	if info.PID < 0 {
		return fmt.Errorf("runtime PID is invalid")
	}
	if info.WebPort < 0 || info.WebPort > 65535 || info.P2PPort < 0 || info.P2PPort > 65535 {
		return fmt.Errorf("runtime port is invalid")
	}
	if info.WebAddr != "" && !validLoopbackWebAddr(info.WebAddr) {
		return fmt.Errorf("runtime web address is not loopback")
	}
	if len(info.IPCToken) > 256 {
		return fmt.Errorf("runtime IPC token is too large")
	}
	return nil
}

func readRuntimeInfoUnlocked(storeDir string) (*RuntimeHubInfo, error) {
	filePath := GetRuntimeFilePath(storeDir)
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRuntimeInfoSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRuntimeInfoSize {
		return nil, fmt.Errorf("runtime metadata exceeds %d bytes", maxRuntimeInfoSize)
	}
	var info RuntimeHubInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("decode runtime metadata: %w", err)
	}
	if err := validateRuntimeInfo(info); err != nil {
		return nil, err
	}
	return &info, nil
}

func removeRuntimeInfoUnlocked(storeDir string) error {
	if err := os.Remove(GetRuntimeFilePath(storeDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func replaceRuntimeFile(tempPath, destination string) error {
	if err := os.Rename(tempPath, destination); err == nil {
		return nil
	} else {
		firstErr := err
		if destinationInfo, statErr := os.Lstat(destination); statErr == nil && destinationInfo.IsDir() {
			return fmt.Errorf("replace runtime info: destination is a directory")
		}
		// Windows does not replace an existing destination with Rename. Move
		// the old file aside first, and restore it if publishing the new file
		// fails. This preserves the previous metadata on unexpected failures.
		backup, backupErr := os.CreateTemp(filepath.Dir(destination), ".hub.json.old-*")
		if backupErr != nil {
			return fmt.Errorf("replace runtime info: %w", firstErr)
		}
		backupPath := backup.Name()
		_ = backup.Close()
		_ = os.Remove(backupPath)
		if err := os.Rename(destination, backupPath); err != nil {
			return fmt.Errorf("replace runtime info: %w", firstErr)
		}
		if err := os.Rename(tempPath, destination); err != nil {
			if restoreErr := os.Rename(backupPath, destination); restoreErr != nil {
				return fmt.Errorf("replace runtime info: %w (restore failed: %v)", err, restoreErr)
			}
			return fmt.Errorf("rename runtime info: %w", err)
		}
		_ = os.Remove(backupPath)
		return nil
	}
}

// WriteRuntimeInfo writes RuntimeHubInfo to hub.json atomically via a unique
// temporary file with 0600 permissions. A cross-process lock covers the whole
// publication transaction, and a failed replacement preserves the prior file.
func WriteRuntimeInfo(storeDir string, info RuntimeHubInfo) error {
	if err := validateRuntimeInfo(info); err != nil {
		return err
	}
	return withRuntimeLock(storeDir, func() error {
		filePath := GetRuntimeFilePath(storeDir)
		dir := filepath.Dir(filePath)
		if existing, readErr := readRuntimeInfoUnlocked(storeDir); readErr == nil && existing != nil && existing.IPCToken != "" && existing.WebAddr != "" {
			if _, live := probeAddr(existing.WebAddr, existing.IPCToken, existing); live {
				return fmt.Errorf("runtime metadata is already owned by a live Hub")
			}
		}
		data, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal runtime info: %w", err)
		}
		tmp, err := os.CreateTemp(dir, "hub.json.tmp-*")
		if err != nil {
			return fmt.Errorf("create runtime temp file: %w", err)
		}
		tmpPath := tmp.Name()
		cleanup := func() {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
		if err := tmp.Chmod(0600); err != nil {
			cleanup()
			return fmt.Errorf("set runtime temp permissions: %w", err)
		}
		if _, err := tmp.Write(data); err != nil {
			cleanup()
			return fmt.Errorf("write runtime temp file: %w", err)
		}
		if err := tmp.Sync(); err != nil {
			cleanup()
			return fmt.Errorf("sync runtime temp file: %w", err)
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("close runtime temp file: %w", err)
		}
		if err := replaceRuntimeFile(tmpPath, filePath); err != nil {
			_ = os.Remove(tmpPath)
			return err
		}
		return nil
	})
}

// RuntimeIPCToken returns the capability token from the active runtime file.
// An empty result means no Hub runtime metadata is available.
func RuntimeIPCToken(storeDir string) string {
	info, err := ReadRuntimeInfo(storeDir)
	if err != nil || info == nil {
		return ""
	}
	return strings.TrimSpace(info.IPCToken)
}

// RuntimeIPCTokenForAddr returns the token only when the destination matches
// the runtime file's authenticated loopback endpoint. This prevents a caller
// from accidentally forwarding the capability to an unrelated local listener.
func RuntimeIPCTokenForAddr(storeDir, webAddr string) string {
	info, err := ReadRuntimeInfo(storeDir)
	if err != nil || info == nil || info.IPCToken == "" || info.WebAddr == "" {
		return ""
	}
	if !sameLoopbackEndpoint(info.WebAddr, webAddr) {
		return ""
	}
	return strings.TrimSpace(info.IPCToken)
}

// RemoveRuntimeInfoIfOwned removes runtime metadata only when it still belongs
// to the expected Hub instance. The check and removal are one locked
// transaction, so an older process cannot delete a replacement's metadata.
func RemoveRuntimeInfoIfOwned(storeDir string, expected RuntimeHubInfo) error {
	return withRuntimeLock(storeDir, func() error {
		info, err := readRuntimeInfoUnlocked(storeDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.PID != expected.PID || !info.StartedAt.Equal(expected.StartedAt) || info.IPCToken != expected.IPCToken {
			return nil
		}
		return removeRuntimeInfoUnlocked(storeDir)
	})
}

// RemoveRuntimeInfo removes the hub.json file.
func RemoveRuntimeInfo(storeDir string) error {
	return withRuntimeLock(storeDir, func() error { return removeRuntimeInfoUnlocked(storeDir) })
}

// ReadRuntimeInfo reads and validates hub.json.
func ReadRuntimeInfo(storeDir string) (*RuntimeHubInfo, error) {
	var info *RuntimeHubInfo
	err := withRuntimeLock(storeDir, func() error {
		var err error
		info, err = readRuntimeInfoUnlocked(storeDir)
		return err
	})
	if err != nil {
		return nil, err
	}
	return info, nil
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
	PID       int            `json:"pid,omitempty"`
	StartedAt time.Time      `json:"started_at,omitempty"`
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
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	return strings.TrimRight(addr, "/")
}

func validLoopbackWebAddr(addr string) bool {
	cleaned := cleanWebAddr(addr)
	u, err := url.Parse("http://" + cleaned)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func sameLoopbackEndpoint(a, b string) bool {
	if !validLoopbackWebAddr(a) || !validLoopbackWebAddr(b) {
		return false
	}
	ua, _ := url.Parse("http://" + cleanWebAddr(a))
	ub, _ := url.Parse("http://" + cleanWebAddr(b))
	hostA := strings.ToLower(ua.Hostname())
	hostB := strings.ToLower(ub.Hostname())
	if hostA == "localhost" {
		hostA = "127.0.0.1"
	}
	if hostB == "localhost" {
		hostB = "127.0.0.1"
	}
	return hostA == hostB && effectivePort(ua) == effectivePort(ub)
}

func effectivePort(u *url.URL) string {
	if u == nil {
		return ""
	}
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func probeAddr(webAddr, token string, expected *RuntimeHubInfo) (*HubStatus, bool) {
	webAddr = cleanWebAddr(webAddr)
	if !validLoopbackWebAddr(webAddr) || strings.TrimSpace(token) == "" {
		return nil, false
	}
	client := &http.Client{
		Timeout:   200 * time.Millisecond,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	endpoint := fmt.Sprintf("http://%s/api/probe", webAddr)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set(IPCTokenHeader, token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false
	}

	var status HubStatus
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 64*1024))
	if err := decoder.Decode(&status); err != nil {
		return nil, false
	}
	if status.Status != "online" || status.WebAddr == "" || !sameLoopbackEndpoint(webAddr, status.WebAddr) {
		return nil, false
	}
	if expected != nil {
		if expected.IPCToken != "" && token != expected.IPCToken {
			return nil, false
		}
		if expected.PID > 0 && status.PID != expected.PID {
			return nil, false
		}
		if !expected.StartedAt.IsZero() && !status.StartedAt.Equal(expected.StartedAt) {
			return nil, false
		}
	}
	return &status, true
}

// ProbeHub checks if a local tantu Hub is running using the default runtime
// metadata location. Callers that operate a Hub with a custom store directory
// should use ProbeHubWithStoreDir.
func ProbeHub(webAddr string) (*HubStatus, bool) {
	return probeHub(webAddr, "")
}

// ProbeHubWithStoreDir is ProbeHub with an explicit runtime metadata
// directory. This keeps CLI delegation paired with `tantu hub --store-dir ...`
// instead of silently reading a different process's hub.json.
func ProbeHubWithStoreDir(webAddr, storeDir string) (*HubStatus, bool) {
	return probeHub(webAddr, storeDir)
}

func probeHub(webAddr, storeDir string) (*HubStatus, bool) {
	fallbackAddr := DefaultWebAddr
	overrideMu.RLock()
	overrideWeb := defaultWebAddrOverride
	overrideMu.RUnlock()
	if overrideWeb != "" {
		fallbackAddr = overrideWeb
	}

	// Runtime metadata is the source of the capability and the process
	// identity. Never probe an unauthenticated status-shaped endpoint: a local
	// fake service could otherwise impersonate a Hub and receive delegated
	// OAuth URLs or file bytes.
	info, infoErr := ReadRuntimeInfo(storeDir)
	cleaned := cleanWebAddr(webAddr)
	if webAddr != "" && cleaned != DefaultWebAddr && cleaned != fallbackAddr {
		if infoErr != nil || info == nil || info.IPCToken == "" || info.WebAddr == "" || !sameLoopbackEndpoint(cleaned, info.WebAddr) {
			return nil, false
		}
		return probeAddr(cleaned, info.IPCToken, info)
	}

	if infoErr == nil && info != nil && info.WebAddr != "" && validLoopbackWebAddr(info.WebAddr) && info.IPCToken != "" {
		// The default address is a discovery sentinel, not a demand to probe
		// port 9876 literally; this preserves dynamic-port fallback. Any
		// non-default explicit address was checked against the descriptor above.
		if status, ok := probeAddr(info.WebAddr, info.IPCToken, info); ok {
			return status, true
		}
	}

	// A caller may have supplied an explicit endpoint while using the default
	// runtime store. Do not silently fall back to a different unauthenticated
	// endpoint when the runtime capability is unavailable.
	return nil, false
}

// DashboardURLFromRuntime asks a running local Hub to mint a fresh one-time
// dashboard bootstrap URL. It is the headless/CLI equivalent of pressing [o]
// in the interactive cockpit, without embedding a long-lived capability in a
// command-line URL or HTML page.
func DashboardURLFromRuntime(webAddr, storeDir string) (string, error) {
	info, err := ReadRuntimeInfo(storeDir)
	if err != nil {
		return "", fmt.Errorf("read hub runtime metadata: %w", err)
	}
	if info == nil || info.WebAddr == "" || info.IPCToken == "" {
		return "", errors.New("running Hub runtime metadata is unavailable")
	}
	if strings.TrimSpace(webAddr) == "" {
		webAddr = info.WebAddr
	}
	webAddr = cleanWebAddr(webAddr)
	if !validLoopbackWebAddr(webAddr) || !sameLoopbackEndpoint(webAddr, info.WebAddr) {
		return "", fmt.Errorf("Hub web address is not the authenticated runtime endpoint: %q", webAddr)
	}

	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodPost, "http://"+webAddr+"/api/dashboard-url", nil)
	if err != nil {
		return "", fmt.Errorf("create dashboard URL request: %w", err)
	}
	req.Header.Set(IPCTokenHeader, info.IPCToken)
	req.Header.Set("X-CLI-Version", HubVersion)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request dashboard URL: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Hub returned dashboard URL status %d", resp.StatusCode)
	}
	var payload struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16*1024)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode dashboard URL response: %w", err)
	}
	if payload.Status != "ok" || strings.TrimSpace(payload.URL) == "" {
		return "", errors.New("Hub returned an empty dashboard URL")
	}
	parsed, err := url.Parse(payload.URL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Opaque != "" || !sameLoopbackEndpoint(parsed.Host, webAddr) || parsed.Path != "/" || parsed.RawQuery != "" || parsed.ForceQuery {
		return "", errors.New("Hub returned an invalid dashboard URL")
	}
	const prefix = "tantu_bootstrap="
	tokenPart := strings.TrimPrefix(parsed.Fragment, prefix)
	if tokenPart == parsed.Fragment || len(tokenPart) != 64 {
		return "", errors.New("Hub returned a dashboard URL without a one-time bootstrap fragment")
	}
	if decoded, decodeErr := hex.DecodeString(tokenPart); decodeErr != nil || len(decoded) != 32 {
		return "", errors.New("Hub returned a dashboard URL with an invalid bootstrap fragment")
	}
	return payload.URL, nil
}

// DelegateSend transmits a file or text snippet via the local Hub's POST /api/drop/upload endpoint.
// If targetPeer is non-empty, it instructs the Hub to send to that specific peer.
func newLoopbackIPCClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 5*time.Minute + 30*time.Second
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func DelegateSend(webAddr string, filePath string, text string, targetPeer string) error {
	return DelegateSendFromStore("", webAddr, filePath, text, targetPeer)
}

// DelegateSendFromStore is DelegateSend with an explicit Hub runtime store
// directory used to retrieve the local IPC capability token.
func DelegateSendFromStore(storeDir, webAddr string, filePath string, text string, targetPeer string) error {
	return delegateSend(storeDir, webAddr, filePath, text, targetPeer)
}

func delegateSend(storeDir, webAddr string, filePath string, text string, targetPeer string) error {
	webAddr = cleanWebAddr(webAddr)
	if !validLoopbackWebAddr(webAddr) {
		return fmt.Errorf("hub web address must be loopback: %q", webAddr)
	}
	client := newLoopbackIPCClient(5*time.Minute + 30*time.Second)
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
		if token := RuntimeIPCTokenForAddr(storeDir, webAddr); token != "" {
			req.Header.Set(IPCTokenHeader, token)
		}

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("delegate upload request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
	if token := RuntimeIPCTokenForAddr(storeDir, webAddr); token != "" {
		req.Header.Set(IPCTokenHeader, token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("delegate text request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("hub returned error (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// RelayOpenResult carries the Hub's authorization record identity back to
// the CLI so `tantu open` can report the same operation ID and destination
// the dashboard and support bundle show.
type RelayOpenResult struct {
	OperationID string `json:"operation_id"`
	Destination string `json:"destination"`
}

// RelayAPIError carries the Hub's relay error contract across the local IPC
// boundary. Error() preserves the historical human format.
type RelayAPIError struct {
	StatusCode  int
	Raw         string
	Message     string
	OperationID string
	Destination string
	NextAction  string
}

func (e *RelayAPIError) Error() string {
	return fmt.Sprintf("hub returned relay error (status %d): %s", e.StatusCode, e.Raw)
}

func NewRelayAPIError(status int, body []byte) *RelayAPIError {
	raw := strings.TrimSpace(string(body))
	re := &RelayAPIError{StatusCode: status, Raw: raw, Message: raw}
	var parsed struct {
		Message     string `json:"message"`
		OperationID string `json:"operation_id"`
		Destination string `json:"destination"`
		NextAction  string `json:"next_action"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Message != "" {
			re.Message = parsed.Message
		}
		re.OperationID = parsed.OperationID
		re.Destination = parsed.Destination
		re.NextAction = parsed.NextAction
	}
	if re.Message == "" {
		re.Message = fmt.Sprintf("hub returned relay status %d", status)
	}
	return re
}

func DecodeRelayOpenResult(body []byte) *RelayOpenResult {
	res := &RelayOpenResult{}
	_ = json.Unmarshal(body, res)
	return res
}

// DelegateOpen forwards an OAuth authorization URL via the local Hub's POST /api/relay/open endpoint.
// If targetPeer is non-empty, it instructs the Hub to open with that specific peer.
func DelegateOpen(webAddr string, targetURL string, targetPeer string) (*RelayOpenResult, error) {
	return DelegateOpenFromStore("", webAddr, targetURL, targetPeer)
}

// DelegateOpenFromStore is DelegateOpen with an explicit Hub runtime store
// directory used to retrieve the local IPC capability token.
func DelegateOpenFromStore(storeDir, webAddr string, targetURL string, targetPeer string) (*RelayOpenResult, error) {
	webAddr = cleanWebAddr(webAddr)
	if !validLoopbackWebAddr(webAddr) {
		return nil, fmt.Errorf("hub web address must be loopback: %q", webAddr)
	}
	client := newLoopbackIPCClient(5*time.Minute + 30*time.Second)
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
		return nil, fmt.Errorf("create relay delegation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CLI-Version", HubVersion)
	if token := RuntimeIPCTokenForAddr(storeDir, webAddr); token != "" {
		req.Header.Set(IPCTokenHeader, token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("delegate relay request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, NewRelayAPIError(resp.StatusCode, body)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return DecodeRelayOpenResult(body), nil
}
