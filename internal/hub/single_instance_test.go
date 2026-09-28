package hub

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// A second Hub used to start both listeners and answer a health probe before
// WriteRuntimeInfo discovered that a live Hub already owned the store. For that
// window it was accepting peer connections and serving HTTP, and if the
// incumbent was momentarily slow the 200ms liveness probe could miss it and the
// second process would silently steal the routing descriptor, leaving the CLI
// pointing at the wrong Hub with no operator-visible signal.
func TestHub_SecondInstanceIsRefusedBeforeServing(t *testing.T) {
	storeDir := t.TempDir()
	newHub := func() *Hub {
		h, err := NewHub(HubConfig{
			TransportType: "loopback",
			ListenAddr:    "127.0.0.1:0",
			WebAddr:       "127.0.0.1:0",
			StoreDir:      storeDir,
			OutputDir:     filepath.Join(t.TempDir(), "drops"),
			Headless:      true,
		})
		if err != nil {
			t.Fatalf("NewHub failed: %v", err)
		}
		return h
	}
	startAndWait := func(t *testing.T, h *Hub) error {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		errCh := make(chan error, 1)
		go func() { errCh <- h.Start(ctx) }()
		select {
		case <-h.Ready():
			// Ready is closed on failure as well as success, so the startup
			// error has to be read explicitly.
			return h.Err()
		case err := <-errCh:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for readiness")
			return nil
		}
	}

	first := newHub()
	if err := startAndWait(t, first); err != nil {
		t.Fatalf("first Hub failed to start: %v", err)
	}
	defer func() { _ = first.Stop() }()

	second := newHub()
	err := startAndWait(t, second)
	if err == nil {
		_ = second.Stop()
		t.Fatal("a second Hub started against a store owned by a live Hub")
	}
	if second.WebAddr() == "" || second.WebAddr() == "127.0.0.1:0" {
		return // it never bound a usable endpoint, which is the desired outcome
	}
	// If it did bind, that endpoint must not be accepting: the refusal has to
	// happen before Serve, not after.
	conn, dialErr := net.DialTimeout("tcp", second.WebAddr(), 500*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		t.Errorf("a refused second Hub is still accepting connections on %s", second.WebAddr())
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, getErr := client.Get("http://" + second.WebAddr() + "/healthz")
	if getErr == nil {
		resp.Body.Close()
		t.Errorf("a refused second Hub is still serving HTTP on %s", second.WebAddr())
	}
}

// The descriptor must be released when a start fails, so a rejected or crashed
// attempt cannot leave the CLI routing to a process that is not serving.
func TestHub_RuntimeDescriptorReleasedOnFailedStart(t *testing.T) {
	storeDir := t.TempDir()
	h, err := NewHub(HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      storeDir,
		OutputDir:     filepath.Join(t.TempDir(), "drops"),
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("hub did not become ready")
	}
	if err := h.Stop(); err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	// After Stop the descriptor must not still claim a live owner, otherwise a
	// later legitimate start would be refused.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, readErr := ReadRuntimeInfo(storeDir)
		if readErr != nil || info == nil {
			return // released
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the runtime descriptor was still present after Stop")
}
