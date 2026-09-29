package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/browser"
)

const (
	maxRelayActiveEntries = 128
)

// relayCoordinator coalesces duplicate OAuth requests before they reach the
// wire.  Browser launchers and CLI clients often retry the same authorization
// URL; without single-flight behavior every retry opens another browser
// session and competes for the same localhost callback port.
type relayCoordinator struct {
	mu     sync.Mutex
	active map[string]*relayCall
}

// relayCall is one in-flight relay.
//
// Everything the leader publishes for a canceller to see - its cancel function,
// its attempt id, its peer - lives under cancelMu, not under the coordinator
// mutex. The leader installs these from inside the flow closure, which runs
// concurrently with a Cancel that arrives at any point, so they are a separate
// lock with their own consistent view. release and describe take it together.
type relayCall struct {
	done chan struct{}
	err  error
	// started is set once by the leader under the coordinator mutex, and read
	// afterwards, so it needs no additional synchronization.
	started time.Time
	// flowKey is the coordinator key, retained so cancellation can find the
	// call by request key as well as by attempt id.
	flowKey string

	cancelMu sync.Mutex
	// cancel releases the leader's wait. Nil until the flow installs it, and
	// nil again once the flow has finished.
	cancel context.CancelCauseFunc
	// released records that a cancellation arrived before (or while) the flow
	// was installing its cancel, so the install can honour it immediately
	// rather than dropping it on the floor.
	released   bool
	requestID  string
	peer       string
	flowID     string
	registered bool
}

// register publishes the flow's cancel handle, attempt id, and peer.
//
// If a cancellation already arrived - which is a real ordering, not a
// hypothetical: a dashboard Cancel can land in the window between the call
// being added to the coordinator map and the closure reaching this point - the
// cancel is applied here, immediately, rather than being lost. Without this the
// user gets "cancelled" reported back while the relay runs to its timeout, which
// is the same lie the old Cancel button told.
func (c *relayCall) register(cancel context.CancelCauseFunc, requestID, peer, flowID string) {
	c.cancelMu.Lock()
	c.cancel = cancel
	c.requestID = requestID
	c.peer = peer
	c.flowID = flowID
	c.registered = true
	pending := c.released
	c.cancelMu.Unlock()
	if pending {
		cancel(context.Canceled)
	}
}

// clear marks the flow finished so a late cancel finds nothing to release.
func (c *relayCall) clear() {
	c.cancelMu.Lock()
	c.cancel = nil
	c.registered = false
	c.cancelMu.Unlock()
}

// release ends the flow if it is still live, reporting whether it was. The cause
// marks the flow as abandoned by the user rather than failed, so the recorded
// state and the operator's log say what actually happened.
func (c *relayCall) release() bool {
	c.cancelMu.Lock()
	if c.cancel == nil {
		// The flow has not installed its cancel yet, so it cannot have finished
		// either - clear() would have run. Record the intent so register applies
		// it on arrival, and report success: the release is secured, just not
		// delivered yet.
		c.released = true
		c.cancelMu.Unlock()
		return true
	}
	cancel := c.cancel
	c.released = true
	c.cancelMu.Unlock()
	cancel(context.Canceled)
	return true
}

// describe returns a consistent view of what a canceller needs to see.
func (c *relayCall) describe() (requestID, peer, flowID string, registered bool) {
	c.cancelMu.Lock()
	defer c.cancelMu.Unlock()
	return c.requestID, c.peer, c.flowID, c.registered
}

// cancelFlow releases the call registered under key, if it is still in flight.
// It reports whether a live call was released.
func (c *relayCoordinator) cancelFlow(key string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	call, ok := c.active[key]
	c.mu.Unlock()
	if !ok {
		return false
	}
	return call.release()
}

// cancelAttempt releases the call whose recorded attempt id matches. This is
// the handle a Cancel button uses, because the attempt id is what the operator
// and the dashboard both see.
func (c *relayCoordinator) cancelAttempt(operationID string) bool {
	if c == nil || strings.TrimSpace(operationID) == "" {
		return false
	}
	// Collect under c.mu, then release every match outside it. Holding the
	// coordinator lock across release() would risk a lock-order inversion
	// against the leader, which takes cancelMu and then re-enters the flow.
	var found []*relayCall
	c.mu.Lock()
	for _, call := range c.active {
		if id, _, _, _ := call.describe(); id == operationID {
			found = append(found, call)
		}
	}
	c.mu.Unlock()
	if len(found) == 0 {
		return false
	}
	for _, call := range found {
		call.release()
	}
	return true
}

func newRelayCoordinator() *relayCoordinator {
	return &relayCoordinator{
		active: make(map[string]*relayCall),
	}
}

// Do runs fn once for key. Concurrent callers wait for the leader's result.
// Deliberately there is no post-success replay: the request key normalizes
// away per-attempt OAuth values (state, PKCE, loopback port), so replaying a
// past success would report "completed" for a login whose own callback was
// never delivered. A duplicate after completion runs the flow again, which is
// slower but correct. Failures are not cached either, so a transient
// dial/browser error is immediately retryable.
//
// Only the leader's fn runs. A follower gets the leader's error and its already
// published attempt id, so every caller of a coalesced relay refers to the same
// attempt and a cancel from any of them releases the same thing. That is why the
// id is returned rather than kept as a local: a follower's own id was never
// published anywhere, so reporting it would hand out a handle that cancels
// nothing.
func (c *relayCoordinator) Do(ctx context.Context, key string, fn func(call *relayCall) error) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c == nil {
		call := &relayCall{done: make(chan struct{})}
		return "", fn(call)
	}
	c.mu.Lock()
	if call, ok := c.active[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-call.done:
			attemptID, _, _, _ := call.describe()
			return attemptID, call.err
		}
	}
	if len(c.active) >= maxRelayActiveEntries {
		c.mu.Unlock()
		return "", errors.New("too many active OAuth relay requests")
	}

	call := &relayCall{done: make(chan struct{}), flowKey: key, started: time.Now()}
	c.active[key] = call
	c.mu.Unlock()

	// No context.AfterFunc on ctx here. The leader's flow runs on a context the
	// closure chooses - in practice the Hub's run context, not the HTTP request's
	// - precisely so a user closing the bookmarklet popup or a browser tab does
	// not abort a sign-in that is legitimately continuing in the provider's tab.
	// Coupling the two would reintroduce the abandoned-session bug through the
	// other door: the peer would be left holding its callback port because the
	// user dismissed a window. Only an explicit release ends a leader's flow.
	err := fn(call)

	// Read the id before the call is torn down, so it is still the one the peer
	// saw even if the closure minted it late.
	attemptID, _, _, _ := call.describe()

	c.mu.Lock()
	call.err = err
	delete(c.active, key)
	close(call.done)
	c.mu.Unlock()
	return attemptID, err
}

// relayRequestKey builds a bounded, non-secret key.  It intentionally hashes
// the canonical URL rather than retaining OAuth query values in memory. The
// normalized flow identity is paired with the exact-request identity, so
// identical retries coalesce while a new login (fresh per-attempt values)
// always runs its own flow.
func relayRequestKey(peer, rawURL string) string {
	canonical := browser.SanitizeURL(strings.TrimSpace(rawURL))
	flowID := bridge.DeriveOAuthFlowID(canonical)
	if flowID != "" {
		raw := strings.TrimSpace(peer) + "\x00" + flowID + "\x00" + bridge.RequestURLIdentity(canonical)
		digest := sha256.Sum256([]byte(raw))
		return hex.EncodeToString(digest[:])
	}
	if parsed, err := url.Parse(canonical); err == nil {
		parsed.RawQuery = parsed.Query().Encode()
		canonical = parsed.String()
	}
	raw := peer + "\x00" + canonical
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

// relayOAuth performs one wire flow, with duplicate requests coalesced at the
// local Hub boundary. The leader uses a detached timeout context so a browser
// client disconnecting does not strand the peer connection or leave a callback
// listener behind. It returns the recorded attempt ID ("" when the request
// was coalesced onto another caller's in-flight flow) so API responses can
// point at the durable authorization record.
func (h *Hub) relayOAuth(ctx context.Context, peer, rawURL string) (string, error) {
	oauthURL := browser.SanitizeURL(strings.TrimSpace(rawURL))
	if len(oauthURL) > 64*1024 {
		return h.recordRelayFailure(peer, "", "invalid_input", "Fix the URL and retry."), errors.New("OAuth authorization URL exceeds the maximum supported length")
	}
	if err := browser.ValidateURL(oauthURL); err != nil {
		return h.recordRelayFailure(peer, safeRelayOrigin(oauthURL), "invalid_input", "Fix the URL and retry."), err
	}
	h.mu.Lock()
	coordinator := h.relayCoordinator
	if coordinator == nil {
		coordinator = newRelayCoordinator()
		h.relayCoordinator = coordinator
	}
	h.mu.Unlock()
	keyPeer := strings.TrimSpace(peer)
	dialPeer := keyPeer
	h.mu.RLock()
	transportName := h.cfg.TransportType
	store := h.store
	h.mu.RUnlock()
	activePeer := h.GetActivePeer()
	peerKey := strings.TrimSpace(peer)
	if peerKey == "" {
		peerKey = strings.TrimSpace(activePeer)
		dialPeer = peerKey
	}
	if store != nil {
		resolved, resolveErr := store.ResolvePeer(peerKey)
		if resolveErr == nil {
			// Resolve aliases/defaults before keying replay state. Otherwise a
			// completed flow for peer A could be replayed to peer B after the
			// active/default selection changes.
			keyPeer = resolved.Fingerprint
			dialPeer = resolved.Fingerprint
		} else if transportName == "lan" {
			return h.recordRelayFailure(peer, safeRelayOrigin(oauthURL), "destination_unreachable", "Check the peer is online and paired, then retry."), resolveErr
		}
	}
	key := relayRequestKey(keyPeer, oauthURL)
	// Do returns the id the flow actually published, so a follower of an
	// already-running relay is handed the leader's id rather than one of its own.
	// That is what makes a cancel from either popup release the same flow.
	attemptID, err := coordinator.Do(ctx, key, func(call *relayCall) error {
		attemptID := newRelayID()
		timeout := h.cfg.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		baseCtx := ctx
		h.mu.RLock()
		if h.runContext != nil {
			baseCtx = h.runContext
		}
		h.mu.RUnlock()
		// Bound the flow as a timeout, but keep the cause distinct from a user
		// cancel so the recorded state and the operator's log can tell the two
		// apart. A sign-in the user walked away from is not a failed login.
		// baseCtx is the Hub's run context, not the HTTP request's, so a browser
		// client going away does not abort a flow that is still legitimately in
		// progress elsewhere. The request context is watched separately below,
		// and only to end this caller's own wait.
		relayCtx, cancel := context.WithCancelCause(baseCtx)
		timer := time.AfterFunc(timeout, func() { cancel(context.DeadlineExceeded) })
		defer timer.Stop()
		defer cancel(nil)
		// Publish the cancel handle, attempt id, and flow identity before the
		// wire call, so a Cancel that arrives during the dial still finds this
		// attempt and can already address the peer.
		call.register(cancel, attemptID, dialPeer, bridge.DeriveOAuthFlowID(oauthURL))
		defer call.clear()
		destName, destFP, destAddr := h.resolveOperationDestination(dialPeer)
		origin := safeRelayOrigin(oauthURL)
		now := time.Now()
		h.recordRelayAttempt(RelayAttempt{
			ID: attemptID, TargetPeer: destName, TargetFP: destFP,
			TargetAddress: destAddr, SafeOrigin: origin,
			State: RelayStateSubmitted, CreatedAt: now, UpdatedAt: now,
			DiagnosticID: attemptID,
		})
		h.updateRelayAttempt(attemptID, RelayStateWaiting, "", "")

		conn, err := h.DialPeer(dialPeer)
		if err != nil {
			h.updateRelayAttempt(attemptID, RelayStateFailed, "destination_unreachable", "Check the peer is online and paired, then retry.")
			h.logger.Warn(DomainOAuth, fmt.Sprintf("Sign-in to %s failed: the destination could not be reached", destName))
			return err
		}
		defer conn.Close()
		if err := bridge.HandleBSide(relayCtx, conn, oauthURL, bridge.BSideConfig{Timeout: timeout}); err != nil {
			state, code, next := RelayStateFailed, "relay_failed", "Check the peer is online, then retry the login."
			if ctx.Err() != nil || relayCtx.Err() != nil {
				state, code, next = RelayStateCancelled, "cancelled", "Retry the login flow."
			} else if msg := strings.ToLower(err.Error()); strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
				code, next = "relay_timeout", "Retry; the authorization callback may have expired."
			}
			h.updateRelayAttempt(attemptID, state, code, next)
			// Only the origin host is logged, never the URL: the callback it
			// carries is a one-time credential.
			h.logger.Warn(DomainOAuth, fmt.Sprintf("Sign-in to %s via %s failed (%s)", destName, origin, code))
			return err
		}
		h.updateRelayAttempt(attemptID, RelayStateComplete, "", "")
		// The primary journey is a sign-in completing. Logging only the
		// failures left the successful path invisible in Live Logs, which is
		// where a user goes to confirm what the Hub did.
		h.logger.Action(DomainOAuth, fmt.Sprintf("Sign-in to %s via %s completed", destName, origin))
		return nil
	})
	return attemptID, err
}

// ActiveRelay is a snapshot of one in-flight relay, safe to copy because it
// holds no lock. It carries no URL, no code, and no token: the operator needs to
// know which sign-in is open, for which peer, for how long, and how to stop it.
type ActiveRelay struct {
	// OperationID is the recorded attempt id, and the handle a Cancel uses.
	OperationID string `json:"operation_id,omitempty"`
	// Peer is the resolved destination the relay is going to.
	Peer string `json:"peer,omitempty"`
	// Age is how long the relay has been in flight.
	Age time.Duration `json:"-"`
	// AgeSeconds is Age in whole seconds, for a JSON surface.
	AgeSeconds int `json:"age_seconds"`
	// State is the human-readable label to show.
	State string `json:"state"`

	// flowID is unexported: it is an implementation detail of how a cancel
	// addresses the peer, not something a browser surface should receive.
	flowID string
	// flowKey is the coordinator's own key for this call. It is the only handle
	// available for a relay that has not yet published an attempt id, so a
	// cancel issued in that window still finds its target.
	flowKey string
}

// activeRelays returns a value snapshot of the calls in flight, oldest first.
func (c *relayCoordinator) snapshot() []ActiveRelay {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	out := make([]ActiveRelay, 0, len(c.active))
	for _, call := range c.active {
		operationID, peer, flowID, registered := call.describe()
		relay := ActiveRelay{Peer: peer, Age: now.Sub(call.started), State: "relaying"}
		relay.OperationID = operationID
		relay.AgeSeconds = int(relay.Age / time.Second)
		// An attempt with no id has not reached the wire yet; it is still
		// cancellable, but the UI must not imply it is already in flight on the
		// peer.
		if !registered {
			relay.State = "starting"
		}
		// The flow identity is the only identifier the peer can resolve, since it
		// never saw this Hub's attempt id. It is a derived, non-secret value.
		relay.flowID = flowID
		relay.flowKey = call.flowKey
		out = append(out, relay)
	}
	// Oldest first, so a listing without a selection defaults to the relay that
	// has been stuck longest.
	sort.Slice(out, func(i, j int) bool { return out[i].Age > out[j].Age })
	return out
}

// ActiveRelays returns the relays this Hub is currently waiting on, oldest
// first.
//
// This is the answer to "why is my sign-in stuck", which previously had no
// answer: a pending relay was invisible on every surface, so the only available
// recovery was restarting the process.
func (h *Hub) ActiveRelays() []ActiveRelay {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	coordinator := h.relayCoordinator
	h.mu.RUnlock()
	if coordinator == nil {
		return nil
	}
	return coordinator.snapshot()
}

// CancelRelayResult reports what a cancellation actually achieved.
type CancelRelayResult struct {
	OperationID string `json:"operation_id,omitempty"`
	// ReleasedLocal is true when a local in-flight relay was ended.
	ReleasedLocal bool `json:"released_local"`
	// ReleasedRemote is true when the peer's sign-in was released, so the
	// loopback callback port it was holding is free again.
	ReleasedRemote bool `json:"released_remote"`
	// Message is user-facing and says what happened and what to do next.
	Message string `json:"message"`
}

// CancelRelay abandons an in-flight relay: it ends this Hub's wait, asks the
// peer to release the sign-in it is holding, and records the attempt as
// cancelled rather than failed.
//
// The remote half is the part that matters to the user. Until it existed,
// walking away from a sign-in left the browser machine holding a bound loopback
// callback port, an open browser tab, and a live session for the full timeout -
// and because an application's redirect port is normally fixed, the abandoned
// session blocked the very retry that follows. That is why restarting the Hub
// used to be the only way out.
func (h *Hub) CancelRelay(ctx context.Context, operationID string) (CancelRelayResult, error) {
	result := CancelRelayResult{OperationID: operationID}
	if h == nil {
		return result, errors.New("no hub")
	}
	const reason = "cancelled from the dashboard"

	h.mu.RLock()
	coordinator := h.relayCoordinator
	h.mu.RUnlock()

	active := coordinator.snapshot()
	if len(active) == 0 {
		result.Message = "No sign-in is in progress. It may have already finished or timed out."
		return result, nil
	}

	// Target the named attempt when one was supplied, otherwise the oldest open
	// relay. Cancelling everything is never inferred: cancelling the wrong
	// sign-in would be worse than doing nothing.
	var target *ActiveRelay
	if strings.TrimSpace(operationID) != "" {
		for i := range active {
			if active[i].OperationID == operationID {
				target = &active[i]
				break
			}
		}
		if target == nil {
			result.Message = "That sign-in has already finished or timed out."
			return result, nil
		}
	} else {
		target = &active[0]
	}

	// Address the relay by attempt id when it has one, and by coordinator key
	// otherwise. A relay caught between entering the coordinator and publishing
	// its id has no attempt id, and a cancel that matched only on the id would
	// silently release nothing while reporting a target it had found.
	if target.OperationID != "" {
		result.ReleasedLocal = coordinator.cancelAttempt(target.OperationID)
	}
	if !result.ReleasedLocal && target.flowKey != "" {
		result.ReleasedLocal = coordinator.cancelFlow(target.flowKey)
	}
	if released, err := h.cancelRemoteRelay(ctx, target, reason); err == nil && released {
		result.ReleasedRemote = true
	}
	h.updateRelayAttempt(target.OperationID, RelayStateCancelled, "cancelled",
		"The sign-in was released. Start it again when you are ready.")

	switch {
	case result.ReleasedLocal && result.ReleasedRemote:
		result.Message = "Sign-in cancelled. Both machines released it; you can start again."
	case result.ReleasedLocal:
		result.Message = "Sign-in cancelled here. The other machine did not confirm, so it may release its side when it times out."
	case result.ReleasedRemote:
		result.Message = "The other machine released its sign-in. This machine had already stopped waiting."
	default:
		result.Message = "Nothing was left to release; the sign-in had already ended."
	}
	return result, nil
}

// cancelRemoteRelay asks a peer to release the sign-in it is holding.
//
// The browser side never saw this Hub's attempt id, so the flow identity is the
// only identifier it can resolve. When even that is unknown - the attempt was
// still starting when the snapshot was taken - the cancel is peer-scoped, and
// that stays safe because the peer's registry only lets a peer release sessions
// it owns.
func (h *Hub) cancelRemoteRelay(ctx context.Context, target *ActiveRelay, reason string) (bool, error) {
	dialPeer := strings.TrimSpace(target.Peer)
	if dialPeer == "" {
		return false, errors.New("no peer recorded for the sign-in")
	}
	conn, err := h.DialPeer(dialPeer)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	return bridge.CancelRemoteSession(ctx, conn, "", strings.TrimSpace(target.flowID), reason)
}

// recordRelayFailure records a terminal failed attempt that never reached
// the wire (validation or resolution) and returns its ID. Only the safe
// origin host is kept, never the raw URL, codes, or tokens.
func (h *Hub) recordRelayFailure(peer, safeOrigin, code, next string) string {
	destName, destFP, destAddr := h.resolveOperationDestination(peer)
	now := time.Now()
	id := newRelayID()
	h.recordRelayAttempt(RelayAttempt{
		ID: id, TargetPeer: destName, TargetFP: destFP, TargetAddress: destAddr,
		SafeOrigin: safeOrigin, State: RelayStateFailed,
		CreatedAt: now, UpdatedAt: now,
		ErrorCode: code, NextAction: next, DiagnosticID: id,
	})
	return id
}

// updateRelayAttempt sets a follow-up state on a recorded attempt and
// persists the ledger best-effort.
func (h *Hub) updateRelayAttempt(id, state, code, next string) {
	if h == nil {
		return
	}
	h.mu.RLock()
	ledger := h.relayHistory
	h.mu.RUnlock()
	if ledger == nil {
		return
	}
	ledger.Update(id, func(op *RelayAttempt) {
		op.State = state
		op.ErrorCode = code
		op.NextAction = next
	})
	if dir := h.hubStoreDir(); dir != "" {
		if err := persistRelayHistory(dir, ledger.GetAll()); err != nil && h.logger != nil {
			h.logger.Warn(DomainOAuth, fmt.Sprintf("Could not save authorization history: %v", err))
		}
	}
}

// relayAttemptNextAction returns the recorded recovery guidance for an
// attempt, letting API responses echo exactly what the ledger holds.
func (h *Hub) relayAttemptNextAction(id string) string {
	if h == nil || id == "" {
		return ""
	}
	h.mu.RLock()
	ledger := h.relayHistory
	h.mu.RUnlock()
	if ledger == nil {
		return ""
	}
	for _, op := range ledger.GetAll() {
		if op.ID == id {
			return op.NextAction
		}
	}
	return ""
}
