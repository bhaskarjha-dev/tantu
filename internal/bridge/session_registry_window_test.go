package bridge

// The window in SessionRegistry.Cancel.
//
// Cancel used to remove the session from the map, RELEASE the lock, decide
// whether the cancellation was authorized, and put the session back. The
// window between the removal and the re-insertion hid a live session from
// its own owner -- which is the very thing the removal was written to avoid
// -- and a concurrent Register with the same request id was silently
// overwritten by the re-insertion.
//
// The gate produces the window rather than hoping to catch it: the ownership
// check compares two fingerprints, and a comparison of two long strings that
// agree everywhere but the last byte takes long enough for the test to act
// inside it. That is what makes the observation deterministic rather than
// lucky.
//
// See docs/KNOWN-LIMITATIONS.md row 3.33.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// longFingerprint builds a fingerprint that agrees with other everywhere but
// in its LAST byte.
//
// The ownership check is a case-insensitive comparison of two strings, so a
// pair like this makes the check slow -- and the check is what the old code
// did BETWEEN removing the session and putting it back.
//
// The two must agree for the whole shared run: a comparison that differs in
// its first byte returns at once, and the window would be too short to
// observe.
func longFingerprint(prefix string, fill byte, n int, last byte) string {
	return prefix + strings.Repeat(string(fill), n) + string(last)
}

// TestCancelDoesNotClobberAConcurrentRegistration is the gate.
//
// A registration that arrives while an UNAUTHORIZED cancellation is being
// decided must survive it. The old code removed the session, decided outside
// the lock, and put the old session back -- over the new one.
func TestCancelDoesNotClobberAConcurrentRegistration(t *testing.T) {
	r := NewSessionRegistry()

	const requestID = "rl-clobber"
	owner := longFingerprint("owner-", 'A', 64<<20, 'X')
	requester := longFingerprint("owner-", 'A', 64<<20, 'Y')
	// The requester's fingerprint differs from the owner's only in the last
	// byte, so the comparison scans the whole string.

	// The session that must survive the cancellation.
	ctxA, cancelA := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancelA(context.Canceled) })
	releasedA := make(chan struct{})
	if _, err := r.Register(ctxA, cancelA, releasedA, SessionInfo{
		RequestID:       requestID,
		PeerFingerprint: owner,
	}); err != nil {
		t.Fatalf("register the original session: %v", err)
	}

	// An unauthorized cancellation of it, which must not remove it.
	cancelDone := make(chan struct{})
	go func() {
		defer close(cancelDone)
		if _, ok := r.Cancel(requestID, requester); ok {
			t.Error("an unauthorized cancel reported success")
		}
	}()

	// Let the cancellation reach its ownership check. The sleep is much shorter
	// than the comparison it is waiting for, so the registration below lands
	// INSIDE the window -- which is the whole point of making the fingerprints
	// long.
	time.Sleep(5 * time.Millisecond)

	// A new registration for the same request id, arriving while the
	// cancellation is still deciding.
	ctxB, cancelB := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancelB(context.Canceled) })
	releasedB := make(chan struct{})
	if _, err := r.Register(ctxB, cancelB, releasedB, SessionInfo{
		RequestID:       requestID,
		PeerFingerprint: "the-new-session",
	}); err != nil {
		t.Fatalf("register the replacement session: %v", err)
	}

	<-cancelDone

	// The replacement must be the one in the registry. The old code put the
	// ORIGINAL session back, over the top of it.
	got, ok := r.Get(requestID)
	if !ok {
		t.Fatal("the session vanished from the registry")
	}
	if got.PeerFingerprint != "the-new-session" {
		// The fingerprints are megabytes long, so the message names their
		// lengths rather than printing them.
		t.Fatalf("the registry holds the %d-byte original session, want the replacement: the "+
			"cancellation put the original session back over the registration that arrived "+
			"while it was deciding", len(got.PeerFingerprint))
	}
}

// TestCancelKeepsTheSessionVisibleToItsOwner is the other half.
//
// While an unauthorized cancellation is deciding, the session must be
// visible: to its owner, to List, and to Count. The old code removed it
// first and put it back afterwards, so for the length of the window it was
// gone from all three.
func TestCancelKeepsTheSessionVisibleToItsOwner(t *testing.T) {
	r := NewSessionRegistry()

	const requestID = "rl-visible"
	owner := longFingerprint("owner-", 'A', 64<<20, 'X')
	requester := longFingerprint("owner-", 'A', 64<<20, 'Y')

	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(context.Canceled) })
	released := make(chan struct{})
	if _, err := r.Register(ctx, cancel, released, SessionInfo{
		RequestID:       requestID,
		PeerFingerprint: owner,
	}); err != nil {
		t.Fatalf("register the session: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = r.Cancel(requestID, requester)
	}()

	// While the cancellation decides, the session must still be there.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := r.Get(requestID); !ok {
			wg.Wait()
			t.Fatal("the session was removed from the registry by an unauthorized cancellation: " +
				"it was deleted before the ownership check and put back afterwards, so for the " +
				"length of that window its own owner could not see it")
		}
		if n := r.Count(); n != 1 {
			wg.Wait()
			t.Fatalf("the registry holds %d sessions during an unauthorized cancellation, want 1", n)
		}
		time.Sleep(time.Millisecond)
	}
	wg.Wait()
}
