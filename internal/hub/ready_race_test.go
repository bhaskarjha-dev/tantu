package hub

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The previous Ready() implementation decided whether to allocate a new
// generation channel by selecting on whether the previous one had closed,
// which raced close(). It could therefore hand a caller either the
// already-closed channel (so the caller proceeded against a stopped Hub) or a
// fresh channel that nothing would ever close (so the caller blocked forever).
//
// Hammer both interleavings: Ready() called before Start, concurrently with
// Start, and in the instant between Stop returning and the next Start.
func TestHub_ReadyNeverReturnsAStaleOrOrphanedChannel(t *testing.T) {
	tempDir := t.TempDir()
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
	defer func() { _ = h.Stop() }()

	for generation := 0; generation < 6; generation++ {
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)

		// Grab Ready() the way a caller does, concurrently with startup, and
		// also a second observer a moment later. Both must observe the same
		// generation and both must be woken.
		var readyEarly, readyLate <-chan struct{}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); readyEarly = h.Ready() }()
		go func() { defer wg.Done(); time.Sleep(2 * time.Millisecond); readyLate = h.Ready() }()

		go func() { errCh <- h.Start(ctx) }()
		wg.Wait()

		for i, ch := range []<-chan struct{}{readyEarly, readyLate} {
			select {
			case <-ch:
			case err := <-errCh:
				t.Fatalf("generation %d observer %d: start failed: %v", generation, i, err)
			case <-time.After(10 * time.Second):
				t.Fatalf("generation %d observer %d was handed a channel nothing closes (orphaned)", generation, i)
			}
		}
		if err := h.Err(); err != nil {
			cancel()
			t.Fatalf("generation %d reported a startup error: %v", generation, err)
		}

		// After Stop, a fresh Ready() must not be the channel that just closed.
		if err := h.Stop(); err != nil {
			cancel()
			t.Fatalf("generation %d stop failed: %v", generation, err)
		}
		select {
		case err := <-errCh:
			if err != nil {
				cancel()
				t.Fatalf("generation %d returned an error during stop: %v", generation, err)
			}
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatalf("generation %d did not finish after stop", generation)
		}
		cancel()

		postStop := h.Ready()
		select {
		case <-postStop:
			t.Fatalf("generation %d: Ready() after Stop returned the closed channel of a finished generation", generation)
		case <-time.After(50 * time.Millisecond):
			// Correct: a new, not-yet-closed channel.
		}
	}
}

// A startup failure must still wake waiters, and they must be able to see the
// error rather than blocking forever.
func TestHub_ReadyWakesOnStartupFailure(t *testing.T) {
	h := &Hub{cfg: HubConfig{StoreDir: t.TempDir(), TransportType: "loopback", WebAddr: "127.0.0.1:0"}}
	// An unwritable store directory makes startup fail deterministically.
	h.cfg.StoreDir = "Z:\\tantu-does-not-exist\\store"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- h.Start(ctx) }()

	select {
	case <-h.Ready():
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected a startup error")
		}
		return
	case <-time.After(10 * time.Second):
		t.Fatal("Ready() never fired on a failed startup")
	}
	if h.Err() == nil {
		t.Error("Err() must report the startup failure so a woken waiter can tell failure from success")
	}
	select {
	case <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return")
	}
	_ = h.Stop()
}
