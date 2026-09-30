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
	fn := func(*relayCall) error { runs.Add(1); return nil }
	ctx := context.Background()
	if _, err := c.Do(ctx, "k", fn); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Do(ctx, "k", fn); err != nil {
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
	fn := func(*relayCall) error {
		runs.Add(1)
		<-release
		return nil
	}
	ctx := context.Background()
	errCh := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { _, err := c.Do(ctx, "k", fn); errCh <- err }()
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

// A follower of a running flow must be handed the leader's attempt id, not one
// of its own. The handle is what a Cancel uses, so handing back an unpublished
// id would produce a cancel that silently releases nothing - the exact failure
// mode that made abandoning a sign-in unrecoverable.
func TestRelayCoordinator_FollowerAdoptsLeaderAttemptID(t *testing.T) {
	c := newRelayCoordinator()
	release := make(chan struct{})
	started := make(chan struct{})
	leaderID := ""
	fn := func(call *relayCall) error {
		leaderID = newRelayID()
		call.register(func(error) {}, leaderID, "peer", "flow")
		close(started)
		<-release
		return nil
	}
	ctx := context.Background()
	leaderCh := make(chan string, 1)
	go func() { id, _ := c.Do(ctx, "k", fn); leaderCh <- id }()
	<-started

	followerCh := make(chan string, 1)
	go func() { id, _ := c.Do(ctx, "k", fn); followerCh <- id }()
	// Give the follower time to attach to the existing call.
	time.Sleep(50 * time.Millisecond)
	close(release)

	if got := <-leaderCh; got != leaderID {
		t.Fatalf("leader got id %q, want %q", got, leaderID)
	}
	if got := <-followerCh; got != leaderID {
		t.Fatalf("follower got id %q, want the leader's %q", got, leaderID)
	}
}

// A cancel that lands before the flow has installed its cancel handle must still
// be honoured. This is a real ordering: a dashboard Cancel can arrive in the
// window between the call entering the coordinator and the closure reaching its
// registration. Dropping it would report success while the relay ran on to its
// timeout.
func TestRelayCall_ReleaseBeforeRegisterIsHonoured(t *testing.T) {
	call := &relayCall{done: make(chan struct{})}
	if !call.release() {
		t.Fatal("release before register reported nothing released")
	}
	applied := make(chan struct{})
	call.register(func(cause error) {
		if cause != context.Canceled {
			t.Errorf("cancel cause = %v, want context.Canceled", cause)
		}
		close(applied)
	}, "rl-1", "peer", "flow")
	select {
	case <-applied:
	case <-time.After(time.Second):
		t.Fatal("a cancel issued before register was dropped instead of applied on arrival")
	}
}

// Releasing a finished flow must not resurrect it, and must report that there was
// nothing to release, so the UI can say the sign-in had already ended.
func TestRelayCall_ReleaseAfterClearReportsNothing(t *testing.T) {
	call := &relayCall{done: make(chan struct{})}
	invoked := false
	call.register(func(error) { invoked = true }, "rl-1", "peer", "flow")
	call.clear()
	// A late Cancel must not reach a cancel function that outlived its flow.
	_ = call.release()
	if invoked {
		t.Fatal("a cancel issued after the flow finished was still invoked")
	}
}

// Relay keys bind the exact request: identical retries share a flow while a
// new login (fresh per-attempt values) never joins another login's session.
func TestRelayRequestKey_DistinguishesAttempts(t *testing.T) {
	base := "https://auth.example.test/authorize?client_id=demo&state=one"
	// Determinism is the property that lets a retry join its own flow, so it
	// is asserted against a second, independently computed key. Comparing one
	// call to itself would pass no matter what the function returned.
	first := relayRequestKey("peer", base)
	if second := relayRequestKey("peer", base); first != second {
		t.Fatalf("identical requests must share a key: %q vs %q", first, second)
	}
	if first == "" {
		t.Fatal("a relay key must never be empty, or every request would collide")
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

// A cancel must reach a relay that has not yet published its attempt id. That
// window is real - the call enters the coordinator before the flow closure runs -
// and a cancel matching only on the attempt id would report a target it had
// found while releasing nothing. That is the old Cancel button's behaviour with
// extra steps: success reported, nothing cancelled.
func TestHub_CancelRelayReachesRelayBeforeItPublishesItsID(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	coordinator := newRelayCoordinator()
	h.mu.Lock()
	h.relayCoordinator = coordinator
	h.mu.Unlock()

	// Occupy the coordinator exactly as Do does, with no id published yet: this
	// is the state a relay is in during the window before its id exists.
	flowCtx, flowCancel := context.WithCancel(context.Background())
	defer flowCancel()
	exited := make(chan struct{})
	go func() { <-flowCtx.Done(); close(exited) }()
	call := &relayCall{done: make(chan struct{}), flowKey: "key-in-window", started: time.Now()}
	coordinator.mu.Lock()
	coordinator.active["key-in-window"] = call
	coordinator.mu.Unlock()

	// The listing must present it honestly: no id, and not implying it is
	// already in flight on the peer.
	active := coordinator.snapshot()
	if len(active) != 1 {
		t.Fatalf("expected 1 in-flight relay, got %d", len(active))
	}
	if active[0].OperationID != "" {
		t.Fatalf("the relay unexpectedly has an attempt id %q", active[0].OperationID)
	}
	if active[0].State != "starting" {
		t.Errorf("state = %q, want starting for a relay with no id", active[0].State)
	}

	// Now the flow installs its cancel, with an empty id, exactly as a cancel
	// arriving mid-dial would be handled.
	call.register(func(error) { flowCancel() }, "", "peerA", "flow-1")

	result, err := h.CancelRelay(context.Background(), "")
	if err != nil {
		t.Fatalf("CancelRelay: %v", err)
	}
	if !result.ReleasedLocal {
		t.Fatal("a cancel in the pre-id window released nothing while reporting a target")
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("the relay's flow was not released by the cancel")
	}
}

// A cancel with no identifier targets the oldest open relay, not every open
// relay. Cancelling the wrong sign-in would be worse than cancelling none, so the
// scope has to stay narrow even when the caller does not name a target.
func TestHub_CancelRelayWithoutIDTargetsTheOldestOnly(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	coordinator := newRelayCoordinator()
	h.mu.Lock()
	h.relayCoordinator = coordinator
	h.mu.Unlock()

	now := time.Now()
	released := map[string]chan struct{}{}
	for i, id := range []string{"rl-older", "rl-newer"} {
		done := make(chan struct{})
		released[id] = done
		// Age is computed against time.Now, so a start time in the future yields
		// a negative age and would sort as the newest. Keep both in the past.
		call := &relayCall{
			done:    make(chan struct{}),
			flowKey: id,
			started: now.Add(-(2 - time.Duration(i)) * time.Second),
		}
		call.register(func(error) { close(done) }, id, "peerA", "flow-"+id)
		coordinator.mu.Lock()
		coordinator.active[id] = call
		coordinator.mu.Unlock()
	}

	if got := coordinator.snapshot(); len(got) != 2 || got[0].OperationID != "rl-older" {
		t.Fatalf("snapshot ordered %+v, want rl-older first", got)
	}

	if _, err := h.CancelRelay(context.Background(), ""); err != nil {
		t.Fatalf("CancelRelay: %v", err)
	}

	select {
	case <-released["rl-older"]:
	default:
		t.Error("the oldest open relay was not the one cancelled")
	}
	select {
	case <-released["rl-newer"]:
		t.Error("a cancel with no target also cancelled a second sign-in")
	default:
	}
}

// Naming a target must cancel exactly that one, even when it is not the oldest.
func TestHub_CancelRelayTargetsTheNamedRelay(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	coordinator := newRelayCoordinator()
	h.mu.Lock()
	h.relayCoordinator = coordinator
	h.mu.Unlock()

	now := time.Now()
	released := map[string]chan struct{}{}
	for i, id := range []string{"rl-older", "rl-newer"} {
		done := make(chan struct{})
		released[id] = done
		call := &relayCall{
			done:    make(chan struct{}),
			flowKey: id,
			started: now.Add(-(2 - time.Duration(i)) * time.Second),
		}
		call.register(func(error) { close(done) }, id, "peerA", "flow-"+id)
		coordinator.mu.Lock()
		coordinator.active[id] = call
		coordinator.mu.Unlock()
	}

	if _, err := h.CancelRelay(context.Background(), "rl-newer"); err != nil {
		t.Fatalf("CancelRelay: %v", err)
	}
	select {
	case <-released["rl-newer"]:
	default:
		t.Error("the named relay was not cancelled")
	}
	select {
	case <-released["rl-older"]:
		t.Error("naming a relay also cancelled the other one")
	default:
	}
}

// A cancel must not silently degrade into a peer-wide teardown. Releasing every
// sign-in the caller owns because one identifier was missing would be a much
// larger action than the user asked for.
func TestHub_CancelRelayDoesNotWidenToOtherPeers(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	coordinator := newRelayCoordinator()
	h.mu.Lock()
	h.relayCoordinator = coordinator
	h.mu.Unlock()

	mine := make(chan struct{})
	theirs := make(chan struct{})
	for id, peer := range map[string]chan struct{}{"rl-mine": mine, "rl-theirs": theirs} {
		call := &relayCall{done: make(chan struct{}), flowKey: id, started: time.Now()}
		call.register(func(error) { close(peer) }, id, "peerA", "flow-"+id)
		coordinator.mu.Lock()
		coordinator.active[id] = call
		coordinator.mu.Unlock()
	}

	if _, err := h.CancelRelay(context.Background(), "rl-mine"); err != nil {
		t.Fatalf("CancelRelay: %v", err)
	}
	select {
	case <-mine:
	default:
		t.Error("the named relay was not cancelled")
	}
	select {
	case <-theirs:
		t.Error("cancelling one relay also cancelled an unrelated one")
	default:
	}
}

// The flow must survive the HTTP client going away.
//
// This is the reason the leader's context is the Hub's run context rather than
// the request's. A bookmarklet popup is a window the user may close or reload
// while the sign-in legitimately continues in the provider's tab; if that
// cancelled the flow, the peer would be left holding its callback port - the
// abandoned-session bug arriving through the other door.
func TestRelayCoordinator_LeaderSurvivesCallerDisconnect(t *testing.T) {
	c := newRelayCoordinator()
	ctx, cancel := context.WithCancel(context.Background())

	flowCtx, flowCancel := context.WithCancelCause(context.Background())
	defer flowCancel(context.Canceled)
	flowExited := make(chan struct{})
	go func() {
		defer close(flowExited)
		<-flowCtx.Done()
	}()

	// flowStarted must be closed by the leader's own callback, not by a
	// goroutine that outlives Do's entry. Do returns on a cancelled ctx
	// before it ever touches c.active, so signalling from a watcher that
	// starts first let cancel() race ahead of the insert: the snapshot then
	// saw zero open relays and the test failed outright under -race (6 of 25
	// local runs). Because Do inserts into c.active before invoking fn, a
	// signal inside fn is a guarantee that the relay is registered.
	flowStarted := make(chan struct{})
	done := make(chan struct{})
	var attemptID string
	go func() {
		defer close(done)
		_, _ = c.Do(ctx, "k", func(call *relayCall) error {
			attemptID = "rl-live"
			call.register(func(error) { flowCancel(context.Canceled) }, attemptID, "peerA", "flow-1")
			close(flowStarted)
			<-flowCtx.Done()
			return flowCtx.Err()
		})
	}()
	select {
	case <-flowStarted:
	case <-time.After(5 * time.Second):
		// Bounded: this wait used to be satisfied by a watcher goroutine, so
		// it could not hang. It now waits for the leader callback itself, and
		// an unbounded wait here would turn a regression in Do into a CI job
		// that times out rather than a test that fails.
		t.Fatal("the leader callback never started")
	}

	// The caller walks away: the browser tab is closed, the request is abandoned.
	cancel()

	// The flow must still be running, and still be listed as open.
	time.Sleep(100 * time.Millisecond)
	select {
	case <-flowExited:
		t.Fatal("cancelling the caller aborted the leader's flow; a user closing a popup would strand the peer's callback port")
	default:
	}
	if got := c.snapshot(); len(got) != 1 {
		t.Fatalf("after the caller disconnected there are %d open relays, want 1", len(got))
	}

	// An explicit release must still work, and must be what actually ends it.
	if !c.cancelAttempt("rl-live") {
		t.Fatal("an explicit cancel reported nothing released")
	}
	select {
	case <-flowExited:
	case <-time.After(2 * time.Second):
		t.Fatal("an explicit cancel did not release the flow")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Do did not return after the flow was released")
	}
	if got := c.snapshot(); len(got) != 0 {
		t.Fatalf("%d relays still open after the flow ended", len(got))
	}
}
