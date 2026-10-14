package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
)

// A Hub whose startup failed must be presented as nothing at all.
//
// This gate exists because of a defect it caught. Ready() means "Start has
// returned", not "the Hub is up" -- the Hub deliberately wakes waiters on a
// failed startup so that nobody blocks forever, and Err() is how the two are
// told apart, a contract pinned by hub.TestHub_ReadyWakesOnStartupFailure. The
// CLI waited on Ready() and announced the Hub anyway, so a startup failure
// raced the announcement: the user was shown a bootstrap URL assembled from an
// empty WebAddr, or got a cockpit driving a Hub that had already exited, while
// the actual reason arrived a moment later on stderr.
//
// The failure is produced, not mocked: an unwritable store directory makes Start
// fail the same way it would on a machine with a broken profile path, and the
// assertion is on what the user is actually shown.
func TestPresentStartedHubSaysNothingWhenStartupFailed(t *testing.T) {
	// The store directory is made uncreatable by putting a *regular file* where
	// its parent would have to be. That fails on all three platforms for the
	// same reason.
	//
	// The first version used `Z:\tantu-does-not-exist\store`, which is a
	// perfectly good directory name on Linux and macOS -- backslash and colon
	// are legal in a path component -- so the Hub started successfully and the
	// gate failed on both of them with "Err() does not report the startup
	// failure". A fixture that only fails on the platform it was written on is
	// a fixture that measures the platform.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("this is a file, not a directory\n"), 0o644); err != nil {
		t.Fatalf("write the blocking file: %v", err)
	}

	h, err := hub.NewHub(hub.HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      filepath.Join(blocker, "store"),
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	t.Cleanup(func() { _ = h.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.Start(ctx) }()

	select {
	case <-h.Ready():
	case err := <-done:
		if err == nil {
			t.Fatal("expected a startup error")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Start never returned")
	}
	if h.Err() == nil {
		t.Fatal("Err() does not report the startup failure, so nothing can tell failure from success")
	}

	// Both headless and server are set, so the only branch that could announce is
	// the announcement branch. Were the cockpit branch reachable here the gate
	// would need a terminal, and a gate that needs one is not a gate.
	out := captureStdout(t, func() {
		presentStartedHub(ctx, h, hub.HubConfig{Headless: true, Server: true}, cancel, false)
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("a Hub that failed to start was announced to the user:\n%s\nThe user is told "+
			"about a dashboard that does not exist, and the real reason arrives later on a "+
			"different stream.", out)
	}
}

// The other half, so the gate above cannot be satisfied by simply printing
// nothing ever: a Hub that really did start is announced, and the announcement
// names the addresses it is actually serving on rather than the ones configured.
//
// The wire port is deliberately a fallback port -- occupied first, so the Hub
// comes up somewhere other than where it was configured -- because printing the
// configured number rather than the real one is precisely the mistake a new
// fallback makes possible.
func TestPresentStartedHubAnnouncesThePortActuallyInUse(t *testing.T) {
	h, configuredWire := hubOnOccupiedPort(t)

	out := captureStdout(t, func() {
		presentStartedHub(context.Background(), h, hub.HubConfig{Headless: true, Server: true}, func() {}, false)
	})
	if !strings.Contains(out, "tantu hub active") {
		t.Fatalf("a Hub that started was not announced; the gate above would be satisfied "+
			"by never printing anything:\n%s", out)
	}
	wire := h.P2PAddr()
	if wire == nil {
		t.Fatal("the Hub has no wire address, so there is nothing to announce")
	}
	if portOf(t, wire.String()) == configuredWire {
		t.Skip("the wire port was not actually taken; nothing moved, so there is nothing to check")
	}
	if !strings.Contains(out, wire.String()) {
		t.Errorf("the announcement does not name the wire address actually in use %s, which is "+
			"not the configured port %d:\n%s", wire, configuredWire, out)
	}
	if web := h.WebAddr(); web == "" || !strings.Contains(out, web) {
		t.Errorf("the announcement does not name the web address actually in use %q:\n%s", web, out)
	}
}

// hubOnOccupiedPort starts a real Hub whose configured wire port is already
// taken, so it comes up on a fallback port the user never configured. It returns
// the Hub and the port that was configured and taken.
func hubOnOccupiedPort(t *testing.T) (*hub.Hub, int) {
	t.Helper()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe for a free port: %v", err)
	}
	taken := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	blocker, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", taken))
	if err != nil {
		t.Skipf("could not occupy port %d, so this gate would test nothing: %v", taken, err)
	}
	t.Cleanup(func() { _ = blocker.Close() })

	dir := t.TempDir()
	h, err := hub.NewHub(hub.HubConfig{
		TransportType: "lan",
		ListenAddr:    fmt.Sprintf("127.0.0.1:%d", taken),
		WebAddr:       "127.0.0.1:0",
		StoreDir:      dir,
		OutputDir:     filepath.Join(dir, "drops"),
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(20 * time.Second):
		t.Fatal("Start never returned")
	}
	if err := h.Err(); err != nil {
		t.Fatalf("Hub failed to start: %v", err)
	}
	t.Cleanup(func() { _ = h.Stop() })
	return h, taken
}

func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatalf("port in %q is not a number: %v", addr, err)
	}
	return port
}
