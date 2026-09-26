package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Sequential duplicates must each run the flow: replaying a past success
// would falsely report delivery for a login whose own callback never arrived.
// Session keys bind the exact request URL, so only byte-identical retries
// share a flow.
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

// Relay keys bind the exact request: identical retries share a flow while a
// new login (fresh per-attempt values) never joins another login's session.
func TestRelayRequestKey_DistinguishesAttempts(t *testing.T) {
	base := "https://auth.example.test/authorize?client_id=demo&state=one"
	if relayRequestKey("peer", base) != relayRequestKey("peer", base) {
		t.Fatal("identical requests must share a key")
	}
	other := "https://auth.example.test/authorize?client_id=demo&state=two"
	if relayRequestKey("peer", base) == relayRequestKey("peer", other) {
		t.Fatal("distinct logins must not share a key")
	}
	reordered := "https://auth.example.test/authorize?state=one&client_id=demo"
	if relayRequestKey("peer", base) != relayRequestKey("peer", reordered) {
		t.Fatal("harmless parameter reordering must not split a flow")
	}
}
