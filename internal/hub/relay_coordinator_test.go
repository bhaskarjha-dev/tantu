package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Sequential duplicates must each run the flow: replaying a past success
// would falsely report delivery for a login whose own callback never arrived
// (request keys normalize away per-attempt state/PKCE values).
func TestRelayCoordinator_NoPostSuccessReplay(t *testing.T) {
	c := newRelayCoordinator()
	var runs atomic.Int32
	fn := func() error { runs.Add(1); return nil }
	ctx := context.Background()
	if err := c.Do(ctx, "k", fn); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "k", fn); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 2 {
		t.Fatalf("sequential calls ran fn %d times, want 2 (no replay)", runs.Load())
	}
}

// Concurrent duplicates still coalesce onto the leader.
func TestRelayCoordinator_ConcurrentCoalesce(t *testing.T) {
	c := newRelayCoordinator()
	var runs atomic.Int32
	release := make(chan struct{})
	fn := func() error {
		runs.Add(1)
		<-release
		return nil
	}
	ctx := context.Background()
	errCh := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { errCh <- c.Do(ctx, "k", fn) }()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	for i := 0; i < 3; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if runs.Load() != 1 {
		t.Fatalf("concurrent calls ran fn %d times, want 1", runs.Load())
	}
}
