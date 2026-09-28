package bridge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeSession models one A-side session: a context that a cancel terminates, and
// a separate "listener closed" signal that the test controls.
//
// The two are deliberately independent. Cancellation is a request and release is
// an outcome, and the gap between them is the whole bug: a caller that is
// releasing a session in order to bind its callback port must not proceed until
// the port is actually free. Modelling them as one event would make the registry
// look correct while reproducing the real race.
type fakeSession struct {
	// cancelled is closed once the session's context is terminated.
	cancelled <-chan struct{}
	// free releases the session's callback port, standing in for the A-side's
	// deferred listener.Close().
	free func()
}

func (f fakeSession) waitCancelled(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-f.cancelled:
	case <-time.After(within):
		t.Fatal("the session's context was not cancelled")
	}
}

func (f fakeSession) assertStillLive(t *testing.T) {
	t.Helper()
	select {
	case <-f.cancelled:
		t.Fatal("the session was cancelled when it should not have been")
	default:
	}
}

// registerSession registers a session modelled by fakeSession.
func registerSession(t *testing.T, r *SessionRegistry, requestID string, port int, peer string) fakeSession {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(context.Canceled) })
	cancelled := make(chan struct{})
	released := make(chan struct{})
	var once sync.Once
	go func() {
		<-ctx.Done()
		close(cancelled)
		// The A-side closes its listener only after unwinding, so the release
		// signal deliberately trails the cancellation.
		once.Do(func() { close(released) })
	}()
	_, err := r.Register(ctx, cancel, released, SessionInfo{
		RequestID:       requestID,
		CallbackPort:    port,
		PeerFingerprint: peer,
	})
	if err != nil {
		t.Fatalf("register %s: %v", requestID, err)
	}
	return fakeSession{cancelled: cancelled, free: func() { once.Do(func() { close(released) }) }}
}

// The bug this whole mechanism exists for: a user walks away from a sign-in, and
// until the timeout expires the machine keeps a bound loopback callback port.
// Because an application's redirect port is normally fixed, that also blocks the
// retry - which is why restarting the process used to be the only recovery.
// Cancelling must release the session's context immediately.
func TestSessionRegistry_CancelReleasesSessionContext(t *testing.T) {
	r := NewSessionRegistry()
	session := registerSession(t, r, "rl-1", 45231, "peerA")

	if _, ok := r.Cancel("rl-1", "peerA"); !ok {
		t.Fatal("cancel reported no live session for a registered one")
	}
	session.waitCancelled(t, 2*time.Second)
	if r.Count() != 0 {
		t.Fatalf("registry still holds %d sessions after cancel, want 0", r.Count())
	}
}

// A session must carry the cancellation cause so the A-side can tell an
// abandoned sign-in from a timeout. Conflating them is what made a user
// cancellation look like a failed login.
func TestSessionRegistry_CancelUsesSessionCancelledCause(t *testing.T) {
	r := NewSessionRegistry()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	if _, err := r.Register(ctx, cancel, nil, SessionInfo{RequestID: "rl-1", PeerFingerprint: "peerA"}); err != nil {
		t.Fatal(err)
	}
	r.Cancel("rl-1", "peerA")
	if cause := context.Cause(ctx); !errors.Is(cause, ErrSessionCancelled) {
		t.Fatalf("cancellation cause = %v, want ErrSessionCancelled", cause)
	}
}

// CancelOnPort is what makes a retry work: a stale sign-in must give up the
// loopback port a fresh attempt needs, instead of the new attempt failing to
// bind and forcing a restart.
func TestSessionRegistry_CancelOnPortReleasesStaleSession(t *testing.T) {
	r := NewSessionRegistry()
	stale := registerSession(t, r, "rl-stale", 45231, "peerA")
	if _, err := r.Register(context.Background(), nil, nil, SessionInfo{RequestID: "rl-new", CallbackPort: 45231}); err != nil {
		t.Fatalf("register new: %v", err)
	}

	released := r.CancelOnPort(45231, "peerA", "rl-new")
	if len(released) != 1 || released[0].RequestID != "rl-stale" {
		t.Fatalf("CancelOnPort released %+v, want just the stale session rl-stale", released)
	}
	stale.waitCancelled(t, 2*time.Second)
	if r.Count() != 1 {
		t.Fatalf("registry holds %d sessions, want only the new one", r.Count())
	}
}

// The exclusion is load-bearing, not defensive tidiness. A-side registers the new
// session and *then* releases whoever else holds the port, so without an
// exclusion the call cancels the caller: its context closes the connection and
// the flow kills itself before it can even acknowledge the request.
func TestSessionRegistry_CancelOnPortNeverReleasesTheExcludedSession(t *testing.T) {
	r := NewSessionRegistry()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	if _, err := r.Register(ctx, cancel, nil, SessionInfo{RequestID: "rl-new", CallbackPort: 45231}); err != nil {
		t.Fatal(err)
	}
	if released := r.CancelOnPort(45231, "", "rl-new"); len(released) != 0 {
		t.Fatalf("CancelOnPort released the excluded session: %+v", released)
	}
	select {
	case <-ctx.Done():
		t.Fatal("the excluded session was released, so the new flow would cancel itself on arrival")
	default:
	}
	if _, ok := r.Get("rl-new"); !ok {
		t.Fatal("the excluded session was dropped from the registry")
	}
}

// A port that no stale session holds must be left alone, so a legitimate second
// login on the same port is not destroyed by its own arrival.
func TestSessionRegistry_CancelOnPortLeavesOtherPortsAlone(t *testing.T) {
	r := NewSessionRegistry()
	session := registerSession(t, r, "rl-1", 45231, "peerA")
	if released := r.CancelOnPort(9999, "peerA", ""); len(released) != 0 {
		t.Fatalf("CancelOnPort(9999) released %+v, want nothing", released)
	}
	session.assertStillLive(t)
	if r.Count() != 1 {
		t.Fatalf("registry holds %d sessions, want 1", r.Count())
	}
}

// One paired machine must not be able to cancel another machine's sign-in. It
// could log a user out of an authentication in progress on someone else's
// machine, and the cancel is a remote message.
func TestSessionRegistry_CancelRefusesForeignPeer(t *testing.T) {
	r := NewSessionRegistry()
	session := registerSession(t, r, "rl-1", 45231, "peerA")

	if _, ok := r.Cancel("rl-1", "peerB"); ok {
		t.Fatal("a different peer was allowed to cancel the session")
	}
	session.assertStillLive(t)
	// A refused cancel must not hide the session from its owner.
	if _, ok := r.Get("rl-1"); !ok {
		t.Fatal("the session vanished after a refused cancel")
	}
}

// Fingerprint comparison must be case-insensitive: hex fingerprints are
// routinely rendered in either case, and a case mismatch would silently deny the
// legitimate owner its own cancel.
func TestSessionRegistry_CancelIsCaseInsensitiveOnFingerprint(t *testing.T) {
	r := NewSessionRegistry()
	session := registerSession(t, r, "rl-1", 45231, "AABBCC")
	if _, ok := r.Cancel("rl-1", "aabbcc"); !ok {
		t.Fatal("cancel was denied for the same fingerprint in different case")
	}
	session.waitCancelled(t, 2*time.Second)
}

// The loopback case has no peer certificate on either side. That must keep
// working, but only when neither side claims an owner: an unidentified caller
// must not be able to cancel a session that does have a known owner.
func TestSessionRegistry_UnboundCancelOnlyMatchesUnownedSession(t *testing.T) {
	r := NewSessionRegistry()
	owned := registerSession(t, r, "rl-owned", 45231, "peerA")
	unowned := registerSession(t, r, "rl-unowned", 45232, "")

	if _, ok := r.Cancel("rl-owned", ""); ok {
		t.Fatal("an unidentified caller cancelled a session with a known owner")
	}
	owned.assertStillLive(t)

	if _, ok := r.Cancel("rl-unowned", ""); !ok {
		t.Fatal("the loopback case broke: an unbound session was not cancellable")
	}
	unowned.waitCancelled(t, 2*time.Second)
}

// Cancelling nothing is a normal outcome, not an error: the flow may have
// completed or timed out between the user deciding and the cancel arriving.
// Reporting it as a failure would be a lie about something already resolved.
func TestSessionRegistry_CancelUnknownIDIsNotAnError(t *testing.T) {
	r := NewSessionRegistry()
	if _, ok := r.Cancel("rl-nope", "peerA"); ok {
		t.Fatal("cancelling an unknown id reported a release")
	}
}

// The registry is reachable from a browser surface, so it must bound itself. A
// hostile or buggy peer minting distinct flows must not grow it without limit.
func TestSessionRegistry_RegisterEnforcesCap(t *testing.T) {
	r := NewSessionRegistry()
	r.max = 3
	for i := 0; i < 3; i++ {
		if _, err := r.Register(context.Background(), nil, nil, SessionInfo{RequestID: string(rune('a' + i))}); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	if _, err := r.Register(context.Background(), nil, nil, SessionInfo{RequestID: "overflow"}); err == nil {
		t.Fatal("the registry accepted a session past its cap")
	}
}

// Re-registering an existing id must be allowed: the A-side registers before it
// knows whether it owns the request, and a duplicate request re-enters. Failing
// here would turn a retried login into a hard error.
func TestSessionRegistry_ReRegisterSameIDIsAllowed(t *testing.T) {
	r := NewSessionRegistry()
	r.max = 1
	if _, err := r.Register(context.Background(), nil, nil, SessionInfo{RequestID: "rl-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(context.Background(), nil, nil, SessionInfo{RequestID: "rl-1"}); err != nil {
		t.Fatalf("re-registering an existing id was rejected at capacity: %v", err)
	}
}

// A nil registry must be inert, not panic. The A-side tolerates a nil registry so
// pre-cancellation callers keep working.
func TestSessionRegistry_NilReceiverIsSafe(t *testing.T) {
	var r *SessionRegistry
	if _, err := r.Register(context.Background(), nil, nil, SessionInfo{RequestID: "rl-1"}); err != nil {
		t.Fatalf("nil registry Register: %v", err)
	}
	if _, ok := r.Cancel("rl-1", "peerA"); ok {
		t.Fatal("nil registry reported a release")
	}
	if r.Count() != 0 || r.List() != nil {
		t.Fatal("nil registry reported live sessions")
	}
	r.SetState("rl-1", "x")
	if released := r.CancelAll("peerA"); released != nil {
		t.Fatal("nil registry released sessions")
	}
}

// A session must disappear once its handler returns, for any reason. The
// unregister func is deferred by the A-side, so a leak here means a permanent
// phantom in the UI.
func TestSessionRegistry_UnregisterRemovesEntry(t *testing.T) {
	r := NewSessionRegistry()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	unregister, err := r.Register(ctx, cancel, nil, SessionInfo{RequestID: "rl-1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Count() != 1 {
		t.Fatalf("count = %d, want 1", r.Count())
	}
	unregister()
	if r.Count() != 0 {
		t.Fatalf("count = %d after unregister, want 0", r.Count())
	}
	// Idempotent: a double unregister from a nested defer must not panic.
	unregister()
}

// Listing must not leak anything sensitive. The whole point of the earlier
// logging fix was that URLs, codes, and tokens stay out of what is recorded and
// displayed, and SessionInfo is now a browser-visible surface.
func TestSessionRegistry_ListCarriesNoSecrets(t *testing.T) {
	r := NewSessionRegistry()
	registerSession(t, r, "rl-1", 45231, "peerA")
	for _, info := range r.List() {
		if info.State == "" {
			t.Fatal("a listed session has no state to show the operator")
		}
		if info.Age < 0 {
			t.Fatalf("a listed session has a negative age: %v", info.Age)
		}
	}
}

// Age must be reported, since "how long has this been stuck" is the question a
// user actually has. Ordering must be stable so a polling UI does not reshuffle.
func TestSessionRegistry_ListOrdersOldestFirst(t *testing.T) {
	r := NewSessionRegistry()
	now := time.Now()
	clock := now
	r.now = func() time.Time { return clock }
	r.Register(context.Background(), nil, nil, SessionInfo{RequestID: "rl-new"})
	clock = now.Add(time.Minute)
	r.Register(context.Background(), nil, nil, SessionInfo{RequestID: "rl-old"})

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("listed %d sessions, want 2", len(list))
	}
	if list[0].RequestID != "rl-new" {
		t.Fatalf("first entry is %q, want the oldest (rl-new)", list[0].RequestID)
	}
	if list[0].AgeSeconds < 59 {
		t.Fatalf("oldest session age = %ds, want at least 59s", list[0].AgeSeconds)
	}
}

// SetState must not resurrect a finished session, and must be safe on an unknown
// id: the A-side calls it unconditionally as the flow advances.
func TestSessionRegistry_SetStateOnUnknownIDIsSafe(t *testing.T) {
	r := NewSessionRegistry()
	r.SetState("rl-nope", SessionStateWaitingCallback)
	if r.Count() != 0 {
		t.Fatalf("SetState created a session out of nothing (count %d)", r.Count())
	}
}

// A flow-scoped cancel releases every session for that flow, which is the right
// granularity for "I decided not to sign in" when a login was retried.
func TestSessionRegistry_CancelByFlowReleasesAllAttempts(t *testing.T) {
	r := NewSessionRegistry()
	// An empty flow id must match nothing rather than everything, or a caller
	// that forgot to supply one would release every sign-in on the machine.
	if released := r.CancelByFlow("", "peerA"); released != 0 {
		t.Fatalf("an empty flow id released %d sessions, want 0", released)
	}

	attempts := map[string]fakeSession{}
	for _, id := range []string{"rl-f1", "rl-f2"} {
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		cancelled := make(chan struct{})
		if _, err := r.Register(ctx, cancel, nil, SessionInfo{RequestID: id, FlowID: "flow-1", PeerFingerprint: "peerA"}); err != nil {
			t.Fatal(err)
		}
		go func(ch chan struct{}) { <-ctx.Done(); close(ch) }(cancelled)
		attempts[id] = fakeSession{cancelled: cancelled}
	}

	unrelated := registerSession(t, r, "rl-other", 45233, "peerA")
	if released := r.CancelByFlow("flow-1", "peerA"); released != 2 {
		t.Fatalf("CancelByFlow released %d, want 2", released)
	}
	for _, session := range attempts {
		session.waitCancelled(t, 2*time.Second)
	}
	unrelated.assertStillLive(t)
}

// CancelAll is peer-scoped for the same reason a single cancel is: it must not
// become a way for one machine to tear down every sign-in on another.
func TestSessionRegistry_CancelAllIsPeerScoped(t *testing.T) {
	r := NewSessionRegistry()
	mine := registerSession(t, r, "rl-mine", 45231, "peerA")
	theirs := registerSession(t, r, "rl-theirs", 45232, "peerB")

	if released := r.CancelAll("peerA"); len(released) != 1 || released[0].RequestID != "rl-mine" {
		t.Fatalf("CancelAll(peerA) released %+v, want only rl-mine", released)
	}
	mine.waitCancelled(t, 2*time.Second)
	theirs.assertStillLive(t)
}

// Concurrent register, list, and cancel from many goroutines must not race or
// corrupt the registry. A data race here would be a crash in a Hub that is meant
// to be long-lived.
func TestSessionRegistry_ConcurrentAccessIsSafe(t *testing.T) {
	r := NewSessionRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		id := string(rune('a'+i%26)) + string(rune('0'+i/26))
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			unregister, err := r.Register(ctx, cancel, nil, SessionInfo{
				RequestID: id, CallbackPort: 45000 + i, PeerFingerprint: "peerA",
			})
			if err != nil {
				return
			}
			defer unregister()
			r.SetState(id, SessionStateWaitingCallback)
			_ = r.List()
			_ = r.Count()
			r.CancelByFlow("", "peerA")
		}()
	}
	wg.Wait()
	if r.Count() != 0 {
		t.Fatalf("%d sessions leaked after every goroutine unregistered", r.Count())
	}
}

// The A-side must report an abandoned sign-in as abandoned, not as a failure.
// A user cancellation reported through the same channel as a timeout is how the
// two became indistinguishable.
func TestErrSessionCancelledIsDistinctFromContextCanceled(t *testing.T) {
	if errors.Is(ErrSessionCancelled, context.Canceled) {
		t.Fatal("ErrSessionCancelled aliases context.Canceled, so an abandoned sign-in is indistinguishable from a closed context")
	}
	if !errors.Is(ErrSessionCancelled, ErrSessionCancelled) {
		t.Fatal("ErrSessionCancelled does not match itself")
	}
}
