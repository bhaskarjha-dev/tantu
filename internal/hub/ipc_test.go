package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

)

func TestProbeHub_Success(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	status, ok := ProbeHub(h.WebAddr())
	if !ok {
		t.Fatalf("expected ProbeHub to succeed on active hub")
	}
	if status.Status != "online" {
		t.Errorf("expected status 'online', got %s", status.Status)
	}
	if status.Version != HubVersion {
		t.Errorf("expected version %s, got %s", HubVersion, status.Version)
	}
	if status.WebAddr != h.WebAddr() {
		t.Errorf("expected web addr %s, got %s", h.WebAddr(), status.WebAddr)
	}
}

func TestProbeHub_Unreachable(t *testing.T) {
	start := time.Now()
	status, ok := ProbeHub("127.0.0.1:59998")
	elapsed := time.Since(start)

	if ok || status != nil {
		t.Errorf("expected ProbeHub to fail on unreachable address, got ok=%v, status=%+v", ok, status)
	}
	if elapsed > 400*time.Millisecond {
		t.Errorf("ProbeHub took too long to timeout: %v", elapsed)
	}
}

func TestDelegateSend_Text(t *testing.T) {
	var receivedText string
	var receivedVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/drop/upload" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		receivedVersion = r.Header.Get("X-CLI-Version")

		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		receivedText = body.Text
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	defer server.Close()

	webAddr := strings.TrimPrefix(server.URL, "http://")
	err := DelegateSend(webAddr, "", "Sample IPC message", "")
	if err != nil {
		t.Fatalf("DelegateSend failed: %v", err)
	}
	if receivedText != "Sample IPC message" {
		t.Errorf("expected 'Sample IPC message', got %q", receivedText)
	}
	if receivedVersion != HubVersion {
		t.Errorf("expected version header %q, got %q", HubVersion, receivedVersion)
	}
}

func TestDelegateSend_File(t *testing.T) {
	var receivedFilename string
	var receivedContent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/drop/upload" {
			http.NotFound(w, r)
			return
		}
		err := r.ParseMultipartForm(10 * 1024 * 1024)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer file.Close()

		receivedFilename = header.Filename
		content, _ := io.ReadAll(file)
		receivedContent = string(content)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	defer server.Close()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test-doc.txt")
	if err := os.WriteFile(filePath, []byte("Hello File Drop!"), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	webAddr := strings.TrimPrefix(server.URL, "http://")
	err := DelegateSend(webAddr, filePath, "", "")
	if err != nil {
		t.Fatalf("DelegateSend file failed: %v", err)
	}
	if receivedFilename != "test-doc.txt" {
		t.Errorf("expected filename 'test-doc.txt', got %q", receivedFilename)
	}
	if receivedContent != "Hello File Drop!" {
		t.Errorf("expected file content 'Hello File Drop!', got %q", receivedContent)
	}
}

func TestDelegateSend_HubError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`peer offline`))
	}))
	defer server.Close()

	webAddr := strings.TrimPrefix(server.URL, "http://")
	err := DelegateSend(webAddr, "", "failing message", "")
	if err == nil {
		t.Fatalf("expected error from failed delegation, got nil")
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "peer offline") {
		t.Errorf("expected 502 and 'peer offline' in error, got %v", err)
	}
}

func TestDelegateSend_WithPeer(t *testing.T) {
	var receivedTextPeer string
	var receivedFilePeer string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/drop/upload" {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(10 * 1024 * 1024); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			receivedFilePeer = r.FormValue("peer")
		} else {
			var body struct {
				Peer string `json:"peer"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			receivedTextPeer = body.Peer
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	defer server.Close()

	webAddr := strings.TrimPrefix(server.URL, "http://")

	// 1. Text delegation with targetPeer
	if err := DelegateSend(webAddr, "", "Sample text", "macbook-pro"); err != nil {
		t.Fatalf("DelegateSend text with peer failed: %v", err)
	}
	if receivedTextPeer != "macbook-pro" {
		t.Errorf("expected receivedTextPeer 'macbook-pro', got %q", receivedTextPeer)
	}

	// 2. File delegation with targetPeer
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "sample.txt")
	if err := os.WriteFile(filePath, []byte("peer file data"), 0600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if err := DelegateSend(webAddr, filePath, "", "ubuntu-desktop"); err != nil {
		t.Fatalf("DelegateSend file with peer failed: %v", err)
	}
	if receivedFilePeer != "ubuntu-desktop" {
		t.Errorf("expected receivedFilePeer 'ubuntu-desktop', got %q", receivedFilePeer)
	}
}

func TestDelegateOpen_Success(t *testing.T) {
	var receivedURL string
	var receivedVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/relay/open" {
			http.NotFound(w, r)
			return
		}
		receivedVersion = r.Header.Get("X-CLI-Version")
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		receivedURL = body.URL
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	defer server.Close()

	targetOAuth := "https://accounts.google.com/o/oauth2/v2/auth?client_id=123"
	webAddr := strings.TrimPrefix(server.URL, "http://")
	err := DelegateOpen(webAddr, targetOAuth, "")
	if err != nil {
		t.Fatalf("DelegateOpen failed: %v", err)
	}
	if receivedURL != targetOAuth {
		t.Errorf("expected %q, got %q", targetOAuth, receivedURL)
	}
	if receivedVersion != HubVersion {
		t.Errorf("expected version header %q, got %q", HubVersion, receivedVersion)
	}
}

func TestDelegateOpen_WithPeer(t *testing.T) {
	var receivedPeer string
	var receivedURL string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/relay/open" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			URL  string `json:"url"`
			Peer string `json:"peer"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		receivedURL = body.URL
		receivedPeer = body.Peer
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	defer server.Close()

	webAddr := strings.TrimPrefix(server.URL, "http://")
	targetOAuth := "https://accounts.google.com/o/oauth2/v2/auth?client_id=123"
	if err := DelegateOpen(webAddr, targetOAuth, "linux-box"); err != nil {
		t.Fatalf("DelegateOpen with peer failed: %v", err)
	}
	if receivedURL != targetOAuth {
		t.Errorf("expected URL %q, got %q", targetOAuth, receivedURL)
	}
	if receivedPeer != "linux-box" {
		t.Errorf("expected receivedPeer 'linux-box', got %q", receivedPeer)
	}
}

func TestDelegateOpen_HubError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`bridge failure`))
	}))
	defer server.Close()

	webAddr := strings.TrimPrefix(server.URL, "http://")
	err := DelegateOpen(webAddr, "https://example.com/oauth", "")
	if err == nil {
		t.Fatalf("expected error from failed relay, got nil")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "bridge failure") {
		t.Errorf("expected error containing 'bridge failure', got %v", err)
	}
}

func TestRuntimeHubInfo_FileOperations(t *testing.T) {
	tempDir := t.TempDir()
	info := RuntimeHubInfo{
		PID:       12345,
		StartedAt: time.Now().Truncate(time.Second),
		WebAddr:   "127.0.0.1:9875",
		WebPort:   9875,
		P2PAddr:   "127.0.0.1:9877",
		P2PPort:   9877,
		Transport: "lan",
	}

	// 1. Write
	err := WriteRuntimeInfo(tempDir, info)
	if err != nil {
		t.Fatalf("WriteRuntimeInfo failed: %v", err)
	}

	// Verify file exists
	path := GetRuntimeFilePath(tempDir)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected hub.json to exist at %s: %v", path, err)
	}

	// 2. Read
	readInfo, err := ReadRuntimeInfo(tempDir)
	if err != nil {
		t.Fatalf("ReadRuntimeInfo failed: %v", err)
	}
	if readInfo.PID != 12345 || readInfo.WebPort != 9875 || readInfo.Transport != "lan" {
		t.Errorf("readInfo mismatch: %+v", readInfo)
	}

	// 3. Remove
	err = RemoveRuntimeInfo(tempDir)
	if err != nil {
		t.Fatalf("RemoveRuntimeInfo failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected hub.json to be deleted, got err: %v", err)
	}
}

func TestProbeHub_DiscoversViaHubJSON(t *testing.T) {
	// Start an active hub on a dynamic port
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// h.storeDir has hub.json written by h.Start!
	storeDir := h.cfg.StoreDir
	hubInfo, err := ReadRuntimeInfo(storeDir)
	if err != nil {
		t.Fatalf("failed to read hub.json written by Hub: %v", err)
	}
	if hubInfo.WebAddr != h.WebAddr() {
		t.Fatalf("expected WebAddr %s, got %s", h.WebAddr(), hubInfo.WebAddr)
	}

	// Use an isolated temporary store directory to test ProbeHub("")
	testDir := t.TempDir()
	SetDefaultStoreDirOverride(testDir)
	defer SetDefaultStoreDirOverride("")

	_ = WriteRuntimeInfo(testDir, *hubInfo)

	// ProbeHub with empty address should discover the active Hub via hub.json
	status, ok := ProbeHub("")
	if !ok || status == nil {
		t.Fatalf("expected ProbeHub(\"\") to discover running hub via hub.json, got ok=%v", ok)
	}
	if status.WebAddr != h.WebAddr() {
		t.Errorf("expected status.WebAddr %s, got %s", h.WebAddr(), status.WebAddr)
	}
}

func TestProbeHub_StaleHubJSONRecovery(t *testing.T) {
	testDir := t.TempDir()
	SetDefaultStoreDirOverride(testDir)
	defer SetDefaultStoreDirOverride("")

	// Override fallback default web addr to an unreachable test port so host state does not interfere
	SetDefaultWebAddrOverride("127.0.0.1:59998")
	defer SetDefaultWebAddrOverride("")

	// Write a stale hub.json pointing to an unreachable dead port
	staleInfo := RuntimeHubInfo{
		PID:       999999,
		WebAddr:   "127.0.0.1:59997",
		WebPort:   59997,
		Transport: "loopback",
	}
	_ = WriteRuntimeInfo(testDir, staleInfo)

	// ProbeHub should fail gracefully without panic or hanging
	start := time.Now()
	status, ok := ProbeHub("")
	elapsed := time.Since(start)

	if ok || status != nil {
		t.Errorf("expected ProbeHub to fail when hub.json is stale and default is down, got ok=%v", ok)
	}
	if elapsed > 1*time.Second {
		t.Errorf("ProbeHub took too long to recover from stale hub.json: %v", elapsed)
	}
}
