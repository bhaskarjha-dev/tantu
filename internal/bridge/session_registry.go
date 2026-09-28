package bridge

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrSessionCancelled reports that a sign-in was released because the user
// abandoned it, rather than because it completed or timed out. It is distinct
// from context.Canceled so the log and the caller can say what happened.
var ErrSessionCancelled = errors.New("sign-in cancelled by user")

// SessionInfo describes one in-flight A-side sign-in. It deliberately carries
// no URL, no authorization code, and no token: a live session's whole value to
// the operator is "this is still open, started then, for that peer", and any
// more would mean re-introducing the logging the project already had to fix.
type SessionInfo struct {
	// RequestID is the per-attempt identifier, unique per session.
	RequestID string `json:"request_id"`
	// FlowID is the caller's flow identity when it supplied one.
	FlowID string `json:"flow_id,omitempty"`
	// CallbackPort is the loopback port this session is holding. It is the
	// reason a retry can fail with "port in use", so it must be visible.
	CallbackPort int `json:"callback_port"`
	// PeerFingerprint identifies the paired machine that asked for the sign-in.
	PeerFingerprint string `json:"peer_fingerprint,omitempty"`
	// State is the human-readable lifecycle label.
	State string `json:"state"`
	// StartedAt is when the session began waiting.
	StartedAt time.Time `json:"started_at"`
	// Age is how long it has been open. It is unexported over JSON because a
	// duration is awkward for a browser to render; AgeSeconds carries the same
	// value for a JSON surface.
	Age time.Duration `json:"-"`
	// AgeSeconds is Age in whole seconds.
	AgeSeconds int `json:"age_seconds"`
}

// SessionState values for SessionInfo.State.
const (
	SessionStateWaitingBrowser  = "waiting_for_browser"
	SessionStateWaitingCallback = "waiting_for_callback"
	SessionStateRelaying        = "relaying_callback"
	SessionStateCompleting      = "completing"
)

// activeSession is the registry's private record for one in-flight session.
type activeSession struct {
	info   SessionInfo
	cancel context.CancelCauseFunc
	// released is closed by the session after its listener is closed. See
	// Register for why a canceller has to wait on it.
	released <-chan struct{}
	started  time.Time
}

// waitForRelease blocks until the session's listener is closed, or until the
// bound expires. The bound matters: a session that never signals (a bug, or a
// handler wedged in teardown) must not turn a cancel into a hang, because the
// caller is usually a user waiting to retry a login.
func (s *activeSession) waitForRelease(within time.Duration) bool {
	if s.released == nil {
		return false
	}
	timer := time.NewTimer(within)
	defer timer.Stop()
	select {
	case <-s.released:
		return true
	case <-timer.C:
		return false
	}
}

// SessionRegistry tracks the A-side sign-ins a bridge listener currently holds,
// so they can be listed and released on demand.
//
// This exists because an OAuth sign-in has two independent owners - the machine
// with the browser that opens the page, and the machine running the application
// that is waiting for the callback - and nothing in the original protocol let
// either of them tell the other that the user had walked away. The abandoned
// side then held a bound loopback port (usually the application's fixed redirect
// port), an open browser tab, and a live session until its timeout, which is
// exactly the situation where a retry cannot start and the only recovery is
// restarting the process.
//
// Every entry is scoped to the peer that created it, so a paired machine can
// only cancel its own sign-ins.
type SessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*activeSession
	// max bounds the registry. A listener already caps concurrent OAuth
	// sessions; this is a second, independent ceiling so a registry cannot grow
	// without bound if that cap is ever relaxed.
	max int
	// now is injectable so the age computation is testable.
	now func() time.Time
}

// DefaultMaxTrackedSessions bounds tracked in-flight sign-ins per listener.
const DefaultMaxTrackedSessions = 128

// releaseWaitBound caps how long a cancel waits for the session's callback
// listener to actually close. Generous enough for a normal teardown, short
// enough that a wedged session cannot hang a user who is trying to sign in
// again.
const releaseWaitBound = 3 * time.Second

// NewSessionRegistry creates an empty registry.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{
		sessions: make(map[string]*activeSession),
		max:      DefaultMaxTrackedSessions,
		now:      time.Now,
	}
}

func (r *SessionRegistry) clock() time.Time {
	if r == nil || r.now == nil {
		return time.Now()
	}
	return r.now()
}

// Register records a new in-flight session and returns a function that removes
// it again. The returned unregister must always be deferred, so a session that
// ends for any reason disappears from the listing.
//
// ctx must be the session's own cancellable context: cancelling it is what
// unblocks the session's wait.
//
// released is closed by the session once its loopback listener is actually
// closed, not merely once cancellation was requested. Cancellation is
// asynchronous, and a caller that is releasing a session to take over its
// callback port needs the port to be free before it binds. Without this signal
// the retry races the teardown and fails with "port in use" - the same dead end
// the release exists to remove. A nil channel is accepted and means the caller
// does not care to wait.
func (r *SessionRegistry) Register(ctx context.Context, cancel context.CancelCauseFunc, released <-chan struct{}, info SessionInfo) (unregister func(), err error) {
	if r == nil {
		return func() {}, nil
	}
	id := strings.TrimSpace(info.RequestID)
	if id == "" {
		return nil, errors.New("session registry requires a request id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = make(map[string]*activeSession)
	}
	if _, exists := r.sessions[id]; !exists && len(r.sessions) >= r.max {
		return nil, errors.New("too many in-flight sign-ins are already open")
	}
	now := r.clock()
	info.RequestID = id
	info.StartedAt = now
	if info.State == "" {
		info.State = SessionStateWaitingBrowser
	}
	r.sessions[id] = &activeSession{info: info, cancel: cancel, started: now, released: released}

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.sessions, id)
			r.mu.Unlock()
		})
	}, nil
}

// SetState updates the visible lifecycle label of a tracked session.
func (r *SessionRegistry) SetState(requestID, state string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[strings.TrimSpace(requestID)]; ok {
		s.info.State = state
		s.info.StartedAt = r.clock()
	}
}

// Cancel releases the session identified by requestID, if it is still live and
// belongs to peerFingerprint.
//
// The peer check is what stops any paired machine from cancelling another
// machine's sign-in. An empty peerFingerprint matches only sessions that were
// created without peer identity, which is the loopback case where the caller has
// no certificate to compare.
func (r *SessionRegistry) Cancel(requestID, peerFingerprint string) (SessionInfo, bool) {
	if r == nil {
		return SessionInfo{}, false
	}
	id := strings.TrimSpace(requestID)
	r.mu.Lock()
	s, ok := r.sessions[id]
	if ok {
		delete(r.sessions, id)
	}
	r.mu.Unlock()
	if !ok {
		return SessionInfo{}, false
	}
	if !sessionBelongsToPeer(s.info, peerFingerprint) {
		// Put it back: the cancellation was not authorized, and removing the
		// entry would have hidden a live session from its own owner.
		r.mu.Lock()
		r.sessions[id] = s
		r.mu.Unlock()
		return SessionInfo{}, false
	}
	if s.cancel != nil {
		s.cancel(ErrSessionCancelled)
	}
	// Cancellation is a request, not an outcome. Waiting here is what makes the
	// release usable: a caller that is about to bind the callback port this
	// session is holding would otherwise race the teardown and be told the port
	// is in use, which is precisely the state this whole path exists to clear.
	s.waitForRelease(releaseWaitBound)
	return s.info, true
}

// CancelByFlow releases every live session with the given flow identity, and
// reports how many were released.
func (r *SessionRegistry) CancelByFlow(flowID, peerFingerprint string) int {
	if r == nil {
		return 0
	}
	flow := strings.TrimSpace(flowID)
	if flow == "" {
		return 0
	}
	var ids []string
	r.mu.Lock()
	for id, s := range r.sessions {
		if s.info.FlowID == flow {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()

	released := 0
	for _, id := range ids {
		if _, ok := r.Cancel(id, peerFingerprint); ok {
			released++
		}
	}
	return released
}

// CancelOnPort releases any live session already holding callbackPort, except
// excludeRequestID.
//
// This is what makes a retry work. The application's redirect port is usually
// fixed, so an abandoned session keeps it bound and the next attempt fails with
// "port in use" - a dead end the user can only escape by restarting. When a new
// sign-in legitimately wants a port that a stale session is holding, the stale
// one is the wrong owner of it and is released.
//
// The exclusion is not optional. A-side calls this after registering the new
// session, so the new session is itself holding the port; without the exclusion
// the call cancels the caller, its context closes the connection, and the very
// next send fails with "connection is closed" - the flow kills itself on arrival.
func (r *SessionRegistry) CancelOnPort(callbackPort int, peerFingerprint, excludeRequestID string) []SessionInfo {
	if r == nil || callbackPort <= 0 {
		return nil
	}
	exclude := strings.TrimSpace(excludeRequestID)
	var ids []string
	r.mu.Lock()
	for id, s := range r.sessions {
		if s.info.CallbackPort == callbackPort && id != exclude {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()

	var released []SessionInfo
	for _, id := range ids {
		if info, ok := r.Cancel(id, peerFingerprint); ok {
			released = append(released, info)
		}
	}
	return released
}

// CancelAll releases every live session, optionally restricted to one peer.
func (r *SessionRegistry) CancelAll(peerFingerprint string) []SessionInfo {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	ids := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		ids = append(ids, id)
	}
	r.mu.Unlock()

	var released []SessionInfo
	for _, id := range ids {
		if info, ok := r.Cancel(id, peerFingerprint); ok {
			released = append(released, info)
		}
	}
	return released
}

// Get returns a copy of one tracked session.
func (r *SessionRegistry) Get(requestID string) (SessionInfo, bool) {
	if r == nil {
		return SessionInfo{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[strings.TrimSpace(requestID)]
	if !ok {
		return SessionInfo{}, false
	}
	info := s.info
	info.Age = r.clock().Sub(s.started)
	info.AgeSeconds = int(info.Age / time.Second)
	return info, true
}

// List returns every tracked session, oldest first, with ages filled in.
func (r *SessionRegistry) List() []SessionInfo {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	out := make([]SessionInfo, 0, len(r.sessions))
	now := r.clock()
	for _, s := range r.sessions {
		info := s.info
		info.Age = now.Sub(s.started)
		info.AgeSeconds = int(info.Age / time.Second)
		out = append(out, info)
	}
	r.mu.Unlock()
	// Stable order keeps the UI from reshuffling rows on every poll.
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].RequestID < out[j].RequestID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// Count returns how many sign-ins are currently in flight.
func (r *SessionRegistry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// sessionBelongsToPeer reports whether a session may be released by the given
// peer fingerprint.
func sessionBelongsToPeer(info SessionInfo, peerFingerprint string) bool {
	owner := strings.TrimSpace(info.PeerFingerprint)
	requester := strings.TrimSpace(peerFingerprint)
	if owner == "" || requester == "" {
		// A session with no recorded owner, or a cancel with no verifiable
		// identity, is only allowed when neither side claims one. This keeps a
		// loopback caller (no certificate) working without letting an
		// unidentified caller cancel a session that has a known owner.
		return owner == "" && requester == ""
	}
	return strings.EqualFold(owner, requester)
}
