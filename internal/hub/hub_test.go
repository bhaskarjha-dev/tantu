package hub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func TestEnsureIdentity_SelfHealing(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hub-id-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := pairing.NewPeerStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create peer store: %v", err)
	}
	if store.HasIdentity() {
		t.Fatalf("expected store to not have identity initially")
	}

	h, err := NewHub(HubConfig{
		StoreDir: tempDir,
		Headless: true,
	})
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	h.store = store

	start := time.Now()
	id, err := h.EnsureIdentity()
	duration := time.Since(start)
	if err != nil {
		t.Fatalf("EnsureIdentity failed: %v", err)
	}
	if id == nil {
		t.Fatalf("expected non-nil identity")
	}
	if len(id.CertPEM) == 0 || len(id.KeyPEM) == 0 || id.Fingerprint == "" {
		t.Fatalf("invalid generated identity fields: %+v", id)
	}
	t.Logf("Generated identity in %v (fingerprint: %s, SAS: %s)", duration, id.Fingerprint, pairing.SASCode(id.Fingerprint))

	// Verify certificate is valid ECDSA P-256
	block, _ := pem.Decode(id.CertPEM)
	if block == nil {
		t.Fatalf("failed to decode certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("failed to parse certificate: %v", err)
	}
	if cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Errorf("expected ECDSA algorithm, got %v", cert.PublicKeyAlgorithm)
	}

	// Second call should return existing identity without regeneration
	id2, err := h.EnsureIdentity()
	if err != nil {
		t.Fatalf("second EnsureIdentity failed: %v", err)
	}
	if id2.Fingerprint != id.Fingerprint {
		t.Errorf("expected matching fingerprints, got %s vs %s", id.Fingerprint, id2.Fingerprint)
	}
}

func TestDefaultTransport(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hub-transport-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := pairing.NewPeerStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create peer store: %v", err)
	}

	// Default transport is always "lan" to enable discovery and in-band pairing
	if tr := DefaultTransport(store); tr != "lan" {
		t.Errorf("expected lan by default, got %s", tr)
	}

	// 1 peer -> lan
	err = store.AddPeer(pairing.Peer{
		Name:        "remote-pc",
		Address:     "192.168.1.100:9877",
		Fingerprint: "abcd1234ef567890",
	})
	if err != nil {
		t.Fatalf("failed to add peer: %v", err)
	}

	if tr := DefaultTransport(store); tr != "lan" {
		t.Errorf("expected lan with 1 peer, got %s", tr)
	}
}

func TestEnsurePeerLANPort(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"192.168.1.50", "192.168.1.50:9877"},
		{"192.168.1.50:56299", "192.168.1.50:56299"},
		{"192.168.1.50:9877", "192.168.1.50:9877"},
		{"my-host", "my-host:9877"},
		{"my-host:12345", "my-host:12345"},
	}

	for _, c := range cases {
		got := EnsurePeerLANPort(c.input)
		if got != c.expected {
			t.Errorf("EnsurePeerLANPort(%q) = %q; want %q", c.input, got, c.expected)
		}
	}
}

func TestEnsurePeerLANPort_PreservesCustomPort(t *testing.T) {
	custom := "192.168.1.50:9999"
	got := EnsurePeerLANPort(custom)
	if got != custom {
		t.Errorf("expected custom port preserved: %q, got %q", custom, got)
	}

	missing := "192.168.1.50"
	gotMissing := EnsurePeerLANPort(missing)
	if gotMissing != "192.168.1.50:9877" {
		t.Errorf("expected default port appended: 192.168.1.50:9877, got %q", gotMissing)
	}
}

func TestNormalizePeers(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hub-norm-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := pairing.NewPeerStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create peer store: %v", err)
	}

	// Peer without port should get :9877
	_ = store.AddPeer(pairing.Peer{
		Name:        "peer-no-port",
		Address:     "10.0.0.5",
		Fingerprint: "fp1234567890",
	})
	// Peer with custom port should preserve it
	_ = store.AddPeer(pairing.Peer{
		Name:        "peer-custom-port",
		Address:     "10.0.0.6:9999",
		Fingerprint: "fp0987654321",
	})

	h, _ := NewHub(HubConfig{StoreDir: tempDir})
	h.store = store
	h.NormalizePeers()

	p1, ok := store.GetPeer("fp1234567890")
	if !ok {
		t.Fatalf("peer 1 not found after normalize")
	}
	if p1.Address != "10.0.0.5:9877" {
		t.Errorf("expected normalized address 10.0.0.5:9877, got %s", p1.Address)
	}

	p2, ok := store.GetPeer("fp0987654321")
	if !ok {
		t.Fatalf("peer 2 not found after normalize")
	}
	if p2.Address != "10.0.0.6:9999" {
		t.Errorf("expected custom port preserved 10.0.0.6:9999, got %s", p2.Address)
	}
}

func TestHub_StartStopRestart(t *testing.T) {
	storeDir := t.TempDir()
	h, err := NewHub(HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      storeDir,
		OutputDir:     filepath.Join(storeDir, "drops"),
		Headless:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	startAndWait := func() {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- h.Start(ctx) }()
		select {
		case <-h.Ready():
		case err := <-errCh:
			cancel()
			t.Fatalf("hub start failed: %v", err)
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("timed out waiting for hub readiness")
		}
		if err := h.Stop(); err != nil {
			cancel()
			t.Fatalf("hub stop failed: %v", err)
		}
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("hub returned error during stop: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("hub did not finish after stop")
		}
	}

	startAndWait()
	startAndWait()
}

func TestHub_StopClosesActiveSSE(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()
	req, err := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read SSE preamble: %v", err)
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- h.Stop() }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not finish while SSE was active")
	}
	_ = resp.Body.Close()
}

func TestHub_DialPeerRejectsArbitraryNonLANTarget(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()
	conn, err := h.DialPeer("127.0.0.1:1")
	if err == nil {
		if conn != nil {
			conn.Close()
		}
		t.Fatal("loopback Hub accepted an arbitrary caller-supplied target")
	}
}

func TestHub_WebPortFallback(t *testing.T) {
	// Occupy port 9876 with a dummy listener
	dummyLn, err := net.Listen("tcp", "127.0.0.1:9876")
	if err != nil {
		t.Skipf("cannot bind dummy listener on 127.0.0.1:9876 (already in use or permission): %v", err)
	}
	defer dummyLn.Close()

	tempDir, err := os.MkdirTemp("", "hub-fallback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	h, err := NewHub(HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:9876",
		StoreDir:      tempDir,
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Start(ctx)
	}()

	select {
	case <-h.Ready():
		// Hub is running and bound
	case err := <-errCh:
		t.Fatalf("Hub failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for Hub ready")
	}
	defer h.Stop()

	boundWeb := h.WebAddr()
	if boundWeb == "127.0.0.1:9876" {
		t.Errorf("expected fallback port, but Hub bound occupied port 9876")
	}
	if !strings.HasPrefix(boundWeb, "127.0.0.1:") {
		t.Errorf("unexpected WebAddr format: %s", boundWeb)
	}

	// Verify the fallback port responds
	resp, err := http.Get("http://" + boundWeb + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed on fallback port %s: %v", boundWeb, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHub_Startup_Loopback(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hub-startup-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	var browserOpened int32

	h, err := NewHub(HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      tempDir,
		OutputDir:     filepath.Join(tempDir, "drops"),
		Headless:      true,
		OpenBrowser: func(url string) error {
			atomic.AddInt32(&browserOpened, 1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Start(ctx)
	}()

	select {
	case <-h.Ready():
		// Hub is running and bound
	case err := <-errCh:
		t.Fatalf("Hub failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for Hub ready")
	}

	// Verify WebAddr and P2PAddr are assigned
	webAddr := h.WebAddr()
	if webAddr == "" {
		t.Fatalf("expected non-empty WebAddr")
	}
	p2pAddr := h.P2PAddr()
	if p2pAddr == nil {
		t.Fatalf("expected non-nil P2PAddr")
	}

	// 1. Verify /healthz
	resp, err := http.Get("http://" + webAddr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for /healthz, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Verify /api/status
	reqStatus, err := http.NewRequest(http.MethodGet, "http://"+webAddr+"/api/status", nil)
	if err != nil {
		t.Fatalf("failed to create status request: %v", err)
	}
	reqStatus.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err = http.DefaultClient.Do(reqStatus)
	if err != nil {
		t.Fatalf("GET /api/status failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for /api/status, got %d", resp.StatusCode)
	}
	var status statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("failed to decode /api/status JSON: %v", err)
	}
	resp.Body.Close()

	if status.Status != "online" {
		t.Errorf("expected status online, got %s", status.Status)
	}
	if status.Transport != "loopback" {
		t.Errorf("expected transport loopback, got %s", status.Transport)
	}
	if status.Identity.Fingerprint == "" {
		t.Errorf("expected non-empty fingerprint in status response")
	}

	// Verify browser was NOT opened in headless mode
	if atomic.LoadInt32(&browserOpened) != 0 {
		t.Errorf("browser was opened despite Headless: true")
	}

	// Cancel context and wait for clean exit
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("hub returned unexpected error on shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Errorf("hub took too long to shutdown")
	}
}

func TestCanonicalDownloadsDefault(t *testing.T) {
	h, err := NewHub(HubConfig{
		Headless: true,
	})
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}

	expectedSuffix := filepath.Join("Downloads", "tantu")
	if !strings.HasSuffix(h.OutputDir(), expectedSuffix) && h.OutputDir() != "downloads" {
		t.Errorf("expected OutputDir to end with %q or be 'downloads', got %q", expectedSuffix, h.OutputDir())
	}

	h.SetOutputDir("/custom/downloads")
	if h.OutputDir() != "/custom/downloads" {
		t.Errorf("expected OutputDir to be updated to '/custom/downloads', got %q", h.OutputDir())
	}
}

func TestRecentDropsBuffer(t *testing.T) {
	buf := NewRecentDropsBuffer(5)

	// Add 7 items to test capacity eviction
	for i := 1; i <= 7; i++ {
		buf.Add(ReceivedDropItem{
			ID:        string(rune('A' + i - 1)),
			Timestamp: time.Now(),
			Kind:      "text",
			Content:   "snippet",
			Size:      7,
		})
	}

	if count := buf.Count(); count != 5 {
		t.Errorf("expected count 5, got %d", count)
	}

	items := buf.GetAll()
	if len(items) != 5 {
		t.Fatalf("expected 5 items, got %d", len(items))
	}

	// Oldest items (A and B) should have been evicted; first should be C
	if items[0].ID != "C" {
		t.Errorf("expected first item ID to be 'C', got %q", items[0].ID)
	}
	if items[4].ID != "G" {
		t.Errorf("expected last item ID to be 'G', got %q", items[4].ID)
	}
}

func TestBoundedBuffer_SafetyLimit(t *testing.T) {
	bb := &boundedBuffer{limit: 10}
	n, err := bb.Write([]byte("12345"))
	if err != nil || n != 5 {
		t.Fatalf("unexpected write failure: %v", err)
	}

	n, err = bb.Write([]byte("67890"))
	if err != nil || n != 5 {
		t.Fatalf("unexpected write failure: %v", err)
	}

	// Next byte should exceed 10 bytes limit
	_, err = bb.Write([]byte("!"))
	if err == nil {
		t.Fatalf("expected error exceeding limit, got nil")
	}
	if bb.String() != "1234567890" {
		t.Errorf("expected buffer content '1234567890', got %q", bb.String())
	}
}

func TestHub_DropResumption(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hub-drop-resume-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	outDir := filepath.Join(tempDir, "drops")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatalf("failed to create drops outDir: %v", err)
	}

	// Payload is 100KB with chunk size 32KB
	chunkSize := 32 * 1024
	payload := bytes.Repeat([]byte("HubDropResumptionTestPayload123!"), 3200) // 102,400 bytes (exactly 3.125 chunks)
	fileName := "resumable-file.bin"

	// Pre-seed the private staging file with the first two chunks.
	dropID := "resume-drop"
	preSeededBytes := int64(2 * chunkSize)
	stagingDir := filepath.Join(outDir, ".tantu-staging")
	if err := os.MkdirAll(stagingDir, 0700); err != nil {
		t.Fatalf("failed to create staging directory: %v", err)
	}
	partPath := filepath.Join(stagingDir, dropID+".part")
	if err := os.WriteFile(partPath, payload[:preSeededBytes], 0600); err != nil {
		t.Fatalf("failed to pre-seed part file: %v", err)
	}

	h, err := NewHub(HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      tempDir,
		OutputDir:     outDir,
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Start(ctx)
	}()

	select {
	case <-h.Ready():
	case err := <-errCh:
		t.Fatalf("Hub failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for Hub ready")
	}
	defer h.Stop()

	// Dial Hub's P2P address using loopback transport
	tr := transport.NewLoopbackTransport()
	conn, err := tr.Dial(h.P2PAddr().String())
	if err != nil {
		t.Fatalf("failed to dial hub P2P addr: %v", err)
	}
	defer conn.Close()

	head := sha256.Sum256(payload[:64*1024])
	meta := drop.DropSend{
		DropID:    dropID,
		Kind:      drop.DropKindFile,
		Name:      fileName,
		Size:      int64(len(payload)),
		ChunkSize: chunkSize,
		HeadHash:  hex.EncodeToString(head[:]),
	}
	if err := drop.WriteStagingManifest(partPath, meta); err != nil {
		t.Fatalf("write staging manifest: %v", err)
	}

	var acceptedOffset int64
	sendErr := drop.SendDrop(ctx, conn, meta, bytes.NewReader(payload), drop.SendDropConfig{
		OnAck: func(ack drop.DropAck) { acceptedOffset = ack.ReceivedBytes },
	})
	if sendErr != nil {
		t.Fatalf("SendDrop failed: %v", sendErr)
	}
	if acceptedOffset != preSeededBytes {
		t.Fatalf("resume ACK offset = %d, want %d", acceptedOffset, preSeededBytes)
	}

	// Give hub a moment to process OnDropReceived
	destPath := filepath.Join(outDir, fileName)
	var content []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(destPath); err == nil {
			content, _ = os.ReadFile(destPath)
			if len(content) == len(payload) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify partPath was removed
	if _, err := os.Stat(partPath); !os.IsNotExist(err) {
		t.Errorf("expected part file to be removed after completion, but it exists")
	}

	// Verify destPath has full payload
	if !bytes.Equal(content, payload) {
		t.Fatalf("file content mismatch: received %d bytes, expected %d bytes", len(content), len(payload))
	}

	// Verify recent drops recorded
	drops := h.RecentDrops().GetAll()
	if len(drops) == 0 {
		t.Errorf("expected RecentDrops to contain item, got 0")
	} else if drops[0].Name != fileName {
		t.Errorf("expected drop name %s, got %s", fileName, drops[0].Name)
	}
}

func TestHub_DuplicateDropIDDoesNotCancelActiveTransfer(t *testing.T) {
	h, cancel, cleanup := startTestHub(t)
	defer cleanup()
	defer cancel()

	payload := bytes.Repeat([]byte("duplicate-drop-id-ownership-"), 16*1024)
	chunkSize := 32 * 1024
	meta := drop.DropSend{
		DropID:    "duplicate-active-drop",
		Kind:      drop.DropKindFile,
		Name:      "duplicate.bin",
		Size:      int64(len(payload)),
		ChunkSize: chunkSize,
	}
	head := sha256.Sum256(payload[:64*1024])
	meta.HeadHash = hex.EncodeToString(head[:])

	tr := transport.NewLoopbackTransport()
	firstConn, err := tr.Dial(h.P2PAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer firstConn.Close()
	reader, writer := io.Pipe()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- drop.SendDrop(context.Background(), firstConn, meta, reader, drop.SendDropConfig{Timeout: 10 * time.Second})
	}()

	partPath := filepath.Join(h.OutputDir(), drop.StagingDirName, meta.DropID+".part")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(partPath); statErr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, statErr := os.Stat(partPath); statErr != nil {
		t.Fatalf("first transfer did not acquire staging ownership: %v", statErr)
	}

	secondConn, err := tr.Dial(h.P2PAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	secondErr := drop.SendDrop(context.Background(), secondConn, meta, bytes.NewReader(payload), drop.SendDropConfig{Timeout: 3 * time.Second})
	_ = secondConn.Close()
	if secondErr == nil || !strings.Contains(secondErr.Error(), "duplicate drop_id") {
		t.Fatalf("duplicate attempt error = %v, want duplicate drop_id rejection", secondErr)
	}

	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write first payload: %v", err)
	}
	_ = writer.Close()
	if err := <-firstDone; err != nil {
		t.Fatalf("active transfer was affected by duplicate attempt: %v", err)
	}

	finalPath := filepath.Join(h.OutputDir(), meta.Name)
	deadline = time.Now().Add(3 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(finalPath)
		if bytes.Equal(got, payload) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("published payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestSanitizeDropFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal.txt", "normal.txt"},
		{"../../evil.txt", "evil.txt"},
		{"..\\..\\evil_win.txt", "evil_win.txt"},
		{"/etc/passwd", "passwd"},
		{"C:\\Windows\\System32\\cmd.exe", "cmd.exe"},
		{"stream.txt:hidden", "stream.txt"},
		{"windows<bad>\"name|.txt", "windows_bad__name_.txt"},
		{"wild?*name.txt", "wild__name.txt"},
		{":hidden_only", "drop.bin"},
		{"CON", "drop_CON"},
		{"con.txt", "drop_con.txt"},
		{"PRN.dat", "drop_PRN.dat"},
		{"aux.log", "drop_aux.log"},
		{"nul", "drop_nul"},
		{"com1.bin", "drop_com1.bin"},
		{"lpt9.tar.gz", "drop_lpt9.tar.gz"},
		{"   spaced.txt   ", "spaced.txt"},
		{"trailing_dots...", "trailing_dots"},
		{"bad\x00\x01\x1f.bin", "bad.bin"},
		{"", "drop.bin"},
		{".", "drop.bin"},
		{"..", "drop.bin"},
		{"...", "drop.bin"},
	}

	for _, tc := range tests {
		got := SanitizeDropFilename(tc.input)
		if got != tc.expected {
			t.Errorf("SanitizeDropFilename(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestOnDropReceived_UnauthenticatedNotAttributedToPeer(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hub-provenance-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := pairing.NewPeerStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	// Add 1 trusted peer
	_ = store.AddPeer(pairing.Peer{
		Fingerprint: "deadbeefcafebabe0123456789abcdef0123456789abcdef0123456789abcdef",
		Name:        "Trusted Laptop",
		Address:     "192.168.1.50:9877",
	})

	h, err := NewHub(HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      tempDir,
		OutputDir:     filepath.Join(tempDir, "drops"),
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	h.store = store

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Start(ctx) }()

	select {
	case <-h.Ready():
	case <-time.After(5 * time.Second):
		t.Fatalf("Hub startup timed out")
	}
	defer h.Stop()

	// Send an unauthenticated drop over loopback (res.PeerFingerprint will be empty)
	tr := transport.NewLoopbackTransport()
	conn, err := tr.Dial(h.P2PAddr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	textPayload := []byte("secret text from unauthenticated loopback")
	meta := drop.DropSend{
		DropID: "test-unauth-drop",
		Kind:   drop.DropKindText,
		Name:   "snippet.txt",
		Size:   int64(len(textPayload)),
	}
	err = drop.SendDrop(ctx, conn, meta, bytes.NewReader(textPayload), drop.SendDropConfig{})
	if err != nil {
		t.Fatalf("SendDrop failed: %v", err)
	}

	// Wait for item to appear in RecentDrops
	deadline := time.Now().Add(3 * time.Second)
	var drops []ReceivedDropItem
	for time.Now().Before(deadline) {
		drops = h.RecentDrops().GetAll()
		if len(drops) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(drops) == 0 {
		t.Fatalf("expected RecentDrops to contain item")
	}

	// AUTH-01 check: FromPeer must NOT be "Trusted Laptop"
	if drops[0].FromPeer == "Trusted Laptop" {
		t.Fatalf("SECURITY FLAW: unauthenticated drop was falsely attributed to %q", drops[0].FromPeer)
	}
	if drops[0].FromPeer != "Unauthenticated Peer" {
		t.Errorf("expected FromPeer 'Unauthenticated Peer', got %q", drops[0].FromPeer)
	}
}

func TestHub_InBandPairing_DynamicPortsRoundTrip(t *testing.T) {
	newDynamicHub := func(t *testing.T) (*Hub, context.CancelFunc) {
		t.Helper()
		h, err := NewHub(HubConfig{
			TransportType:     "lan",
			ListenAddr:        "127.0.0.1:0",
			WebAddr:           "127.0.0.1:0",
			StoreDir:          t.TempDir(),
			Headless:          true,
			AutoAcceptPairing: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- h.Start(ctx) }()
		select {
		case <-h.Ready():
		case err := <-errCh:
			cancel()
			t.Fatalf("Hub failed to start: %v", err)
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("Hub start timed out")
		}
		return h, func() {
			_ = h.Stop()
			cancel()
		}
	}

	hA, stopA := newDynamicHub(t)
	defer stopA()
	hB, stopB := newDynamicHub(t)
	defer stopB()

	_, portText, err := net.SplitHostPort(hB.P2PAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	localPort, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	pairCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := pairing.DialInBandPairingWithOptions(pairCtx, hA.P2PAddr().String(), hB.Store(), func(string, string) bool { return true }, pairing.PairingOptions{LocalListenPort: localPort})
	if err != nil {
		t.Fatalf("dynamic-port pairing failed: %v", err)
	}
	if !res.Accepted {
		t.Fatal("dynamic-port pairing was not accepted")
	}

	deadline := time.Now().Add(3 * time.Second)
	var peerA, peerB pairing.Peer
	for time.Now().Before(deadline) {
		peersA := hA.Store().ListPeers()
		peersB := hB.Store().ListPeers()
		if len(peersA) == 1 && len(peersB) == 1 {
			peerA, peerB = peersA[0], peersB[0]
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if peerA.Address != hB.P2PAddr().String() {
		t.Fatalf("Hub A stored peer at %q, want %q", peerA.Address, hB.P2PAddr().String())
	}
	if peerB.Address != hA.P2PAddr().String() {
		t.Fatalf("Hub B stored peer at %q, want %q", peerB.Address, hA.P2PAddr().String())
	}
}

func TestHub_InBandPairing_OnPort9877(t *testing.T) {
	tempDirA, err := os.MkdirTemp("", "hub-pair-test-a-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDirA)

	tempDirB, err := os.MkdirTemp("", "hub-pair-test-b-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDirB)

	storeB, err := pairing.NewPeerStore(tempDirB)
	if err != nil {
		t.Fatal(err)
	}

	h, err := NewHub(HubConfig{
		TransportType:     "lan",
		ListenAddr:        "127.0.0.1:0",
		WebAddr:           "127.0.0.1:0",
		StoreDir:          tempDirA,
		Headless:          true,
		AutoAcceptPairing: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Start(ctx)
	}()

	select {
	case <-h.Ready():
	case err := <-errCh:
		t.Fatalf("hub failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("hub start timed out")
	}
	defer func() {
		_ = h.Stop()
		cancel()
	}()

	// Machine B connects to Machine A's P2P listener for in-band pairing
	p2pAddr := h.P2PAddr().String()
	res, err := pairing.DialInBandPairing(p2pAddr, storeB, func(peerSAS, localSAS string) bool {
		return true
	})
	if err != nil {
		t.Fatalf("DialInBandPairing to Hub failed: %v", err)
	}

	if !res.Accepted {
		t.Fatal("expected pairing to be accepted")
	}

	// Verify both stores have the peer
	peersB := storeB.ListPeers()
	if len(peersB) != 1 {
		t.Fatalf("expected 1 peer in storeB, got %d", len(peersB))
	}

	// Give hub a moment to finish saving peer in background
	deadline := time.Now().Add(2 * time.Second)
	var peersA []pairing.Peer
	for time.Now().Before(deadline) {
		peersA = h.Store().ListPeers()
		if len(peersA) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(peersA) != 1 {
		t.Fatalf("expected 1 peer in hub storeA, got %d", len(peersA))
	}
}

func dialDropAck(t *testing.T, addr, serverFP string, clientCert tls.Certificate, meta drop.DropSend) drop.DropAck {
	t.Helper()
	tr, err := transport.NewLANTransport(transport.LANTransportConfig{
		Cert:                clientCert,
		TrustedFingerprints: []string{serverFP},
	})
	if err != nil {
		t.Fatalf("client transport: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := transport.DialPinnedContext(ctx, tr, addr, serverFP)
	if err != nil {
		t.Fatalf("dial pinned: %v", err)
	}
	defer conn.Close()
	if dl, ok := conn.(transport.Deadliner); ok {
		_ = dl.SetReadDeadline(time.Now().Add(10 * time.Second))
	}
	if err := conn.Send(drop.TypeDropSend, meta); err != nil {
		t.Fatalf("send drop_send: %v", err)
	}
	env, err := conn.Receive()
	if err != nil {
		t.Fatalf("receive drop_ack: %v", err)
	}
	if env.Type != drop.TypeDropAck {
		t.Fatalf("expected %q, got %q", drop.TypeDropAck, env.Type)
	}
	var ack drop.DropAck
	if err := env.DecodePayload(&ack); err != nil {
		t.Fatalf("decode drop_ack: %v", err)
	}
	return ack
}

// Unpairing a peer must revoke its data access end to end: after RemovePeer a
// drop_send from that peer is rejected even though the TLS handshake still
// completes (AllowPairing keeps the listener open for new inbound pairings;
// the dispatcher enforces live trust).
func TestHub_UnpairRevokesDropAccess(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hub-revoke-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	h, err := NewHub(HubConfig{
		TransportType: "lan",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      tempDir,
		OutputDir:     filepath.Join(tempDir, "drops"),
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("hub not ready")
	}
	defer h.Stop()
	if h.P2PAddr() == nil {
		t.Fatal("no P2P addr")
	}
	serverAddr := h.P2PAddr().String()
	serverFP := h.Identity().Fingerprint

	clientID, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	clientTLS, err := tls.X509KeyPair(clientID.CertPEM, clientID.KeyPEM)
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}
	if err := h.Store().AddPeer(pairing.Peer{
		Fingerprint: clientID.Fingerprint,
		Name:        "revoke-client",
		Address:     "127.0.0.1:9877",
		CertPEM:     clientID.CertPEM,
	}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	ack := dialDropAck(t, serverAddr, serverFP, clientTLS, drop.DropSend{
		DropID: "revoke-test-1", Kind: drop.DropKindText, Name: "t", Size: 4, ChunkSize: 1 << 20,
	})
	if !ack.Accepted {
		t.Fatalf("trusted peer drop rejected: %q", ack.Error)
	}

	if err := h.Store().RemovePeer(clientID.Fingerprint); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}
	ack = dialDropAck(t, serverAddr, serverFP, clientTLS, drop.DropSend{
		DropID: "revoke-test-2", Kind: drop.DropKindText, Name: "t", Size: 4, ChunkSize: 1 << 20,
	})
	if ack.Accepted {
		t.Fatal("unpaired peer drop accepted, want rejection")
	}
}

func TestHub_ErrReportsStartupFailure(t *testing.T) {
	// A regular file where the store directory must be forces peer-store
	// init to fail. Ready must still close (waiters never hang) and Err
	// must report the failure instead of looking like success.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := NewHub(HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      filepath.Join(blocker, "store"),
		OutputDir:     filepath.Join(blocker, "drops"),
		Headless:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("Ready never closed on startup failure")
	}
	if err := h.Err(); err == nil {
		t.Fatal("Err() = nil after failed start; Ready alone is ambiguous")
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Start returned nil despite store failure")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start never returned after failure")
	}
	// The failed generation must not wedge the Hub: Stop is a no-op success
	// and a fresh Hub with a valid dir still starts.
	if err := h.Stop(); err != nil {
		t.Fatalf("Stop after failed start: %v", err)
	}
}

func TestHub_ErrNilOnSuccess(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()
	select {
	case <-h.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("hub never became ready")
	}
	if err := h.Err(); err != nil {
		t.Fatalf("Err() = %v after successful start", err)
	}
}

func TestHub_TimeoutDefaultAndConfigured(t *testing.T) {
	h, err := NewHub(HubConfig{TransportType: "loopback", Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Timeout(); got != 5*time.Minute {
		t.Fatalf("default Timeout() = %v, want 5m", got)
	}
	h2, err := NewHub(HubConfig{TransportType: "loopback", Headless: true, Timeout: 42 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got := h2.Timeout(); got != 42*time.Second {
		t.Fatalf("configured Timeout() = %v, want 42s", got)
	}
}

func TestRecentDropsBuffer_Find(t *testing.T) {
	buf := NewRecentDropsBuffer(4)
	buf.Add(ReceivedDropItem{ID: "a", Kind: "text", Content: "first"})
	buf.Add(ReceivedDropItem{ID: "b", Kind: "text", Content: "second"})
	buf.Add(ReceivedDropItem{ID: "a", Kind: "text", Content: "newest-a"})
	got, ok := buf.Find("a")
	if !ok || got.Content != "newest-a" {
		t.Fatalf("Find(a) = %+v, %v; want newest-a, true", got, ok)
	}
	if _, ok := buf.Find("missing"); ok {
		t.Fatal("Find(missing) succeeded, want false")
	}
}

func TestHub_StartRejectsInvalidOutputDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		dir  string
		want string
	}{
		{"file-as-dir", blocker, "create output directory"},
		{"uncreatable-nested", filepath.Join(blocker, "sub"), "create output directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			h, err := NewHub(HubConfig{
				TransportType: "loopback",
				ListenAddr:    "127.0.0.1:0",
				WebAddr:       "127.0.0.1:0",
				StoreDir:      tempDir,
				OutputDir:     tc.dir,
				Headless:      true,
			})
			if err != nil {
				t.Fatalf("NewHub: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			err = h.Start(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Start = %v, want error containing %q", err, tc.want)
			}
			if h.Err() == nil {
				t.Fatal("Hub.Err() is nil after startup failure")
			}
		})
	}
}

func TestHub_StartSweepsStalePartials(t *testing.T) {
	tempDir := t.TempDir()
	outDir := filepath.Join(tempDir, "drops")
	staging := filepath.Join(outDir, drop.StagingDirName)
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	oldPart := filepath.Join(staging, "old.part")
	if err := os.WriteFile(oldPart, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldPart, aged, aged); err != nil {
		t.Fatal(err)
	}
	freshPart := filepath.Join(staging, "fresh.part")
	if err := os.WriteFile(freshPart, []byte("live"), 0600); err != nil {
		t.Fatal(err)
	}

	h, err := NewHub(HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      tempDir,
		OutputDir:     outDir,
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Start(ctx) }()
	// Wait for the listener goroutine to finish before testing.TempDir removes
	// the store/output tree; cancellation alone can leave shutdown cleanup
	// racing the test cleanup on Windows.
	defer h.Stop()
	select {
	case <-h.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("hub not ready")
	}
	// The startup sweep runs before readiness is signaled.
	if _, err := os.Stat(oldPart); !os.IsNotExist(err) {
		t.Fatal("stale partial survived Hub startup sweep")
	}
	if _, err := os.Stat(freshPart); err != nil {
		t.Fatal("fresh partial wrongly swept at startup")
	}
}
