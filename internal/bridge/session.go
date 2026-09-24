package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// DefaultOAuthReplayWindow is how long a completed OAuth request remains
// replayable.  OAuth clients commonly retry the BROWSER command after a
// transient failure; a short replay window prevents a completed flow from
// opening the authorization page again.
const DefaultOAuthReplayWindow = 2 * time.Minute

const maxOAuthReplayEntries = 256

const (
	maxOAuthURLLength     = 64 * 1024
	maxOAuthFlowIDLength  = 256
	maxBridgeRequestIDLen = 128
)

func validBridgeRequestID(id string) bool {
	if id == "" || len(id) > maxBridgeRequestIDLen {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// OAuthSessionManager coordinates identical OAuth requests handled by one
// bridge listener.  A retry has a different transport connection and request
// ID, so request IDs alone cannot identify it.  The canonical authorization
// URL plus callback port and authenticated peer identity do identify the
// logical OAuth transaction.
//
// The manager deliberately does not retain callback contents.  It only keeps
// a success/failure bit and a channel used to wake duplicate callers.
type OAuthSessionManager struct {
	mu           sync.Mutex
	active       map[string]*oauthSession
	recent       map[string]oauthReplay
	replayWindow time.Duration
}

type oauthSession struct {
	done    chan struct{}
	success bool
	err     error
	closed  bool
}

type oauthReplay struct {
	expires time.Time
	err     error
}

type oauthSessionLease struct {
	manager   *OAuthSessionManager
	key       string
	session   *oauthSession
	owner     bool
	replay    bool
	replayErr error
}

// NewOAuthSessionManager creates an isolated OAuth session coordinator.
// A coordinator should normally be shared by all handlers owned by one
// listener (the Hub dispatcher does this automatically).
func NewOAuthSessionManager() *OAuthSessionManager {
	return &OAuthSessionManager{
		active:       make(map[string]*oauthSession),
		recent:       make(map[string]oauthReplay),
		replayWindow: DefaultOAuthReplayWindow,
	}
}

// NewOAuthSessionManagerWithWindow is primarily useful for deterministic
// tests and callers that need a shorter/longer retry coalescing window.
func NewOAuthSessionManagerWithWindow(window time.Duration) *OAuthSessionManager {
	m := NewOAuthSessionManager()
	if window > 0 {
		m.replayWindow = window
	}
	return m
}

func (m *OAuthSessionManager) pruneLocked(now time.Time) {
	for key, replay := range m.recent {
		if now.After(replay.expires) {
			delete(m.recent, key)
		}
	}
	for len(m.recent) > maxOAuthReplayEntries {
		oldestKey := ""
		oldestExpiry := time.Time{}
		for key, replay := range m.recent {
			if oldestKey == "" || replay.expires.Before(oldestExpiry) {
				oldestKey = key
				oldestExpiry = replay.expires
			}
		}
		if oldestKey == "" {
			break
		}
		delete(m.recent, oldestKey)
	}
}

func (m *OAuthSessionManager) acquire(key string) oauthSessionLease {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.pruneLocked(now)
	if replay, ok := m.recent[key]; ok {
		return oauthSessionLease{
			manager:   m,
			key:       key,
			replay:    true,
			replayErr: replay.err,
		}
	}
	if session, ok := m.active[key]; ok {
		return oauthSessionLease{manager: m, key: key, session: session}
	}

	session := &oauthSession{done: make(chan struct{})}
	m.active[key] = session
	return oauthSessionLease{manager: m, key: key, session: session, owner: true}
}

func (l oauthSessionLease) wait(ctx context.Context) error {
	if l.replay {
		return l.replayErr
	}
	if l.session == nil {
		return errors.New("OAuth session has no completion state")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.session.done:
		if l.session.success {
			return nil
		}
		if l.session.err != nil {
			return l.session.err
		}
		return errors.New("OAuth session failed")
	}
}

func (l oauthSessionLease) finish(err error) {
	if !l.owner || l.manager == nil || l.session == nil {
		return
	}

	l.manager.mu.Lock()
	// A handler must only finish its own lease.  This guard also makes a
	// future cancellation path harmless if a handler is accidentally invoked
	// twice.
	if current, ok := l.manager.active[l.key]; !ok || current != l.session || l.session.closed {
		l.manager.mu.Unlock()
		return
	}
	l.session.success = err == nil
	l.session.err = err
	l.session.closed = true
	delete(l.manager.active, l.key)
	if err == nil {
		l.manager.recent[l.key] = oauthReplay{
			expires: time.Now().Add(l.manager.replayWindow),
		}
		l.manager.pruneLocked(time.Now())
	}
	close(l.session.done)
	l.manager.mu.Unlock()
}

type envelopeResult struct {
	env *protocol.Envelope
	err error
}

// allowedCallbackRelayHeader reports whether an OAuth callback header may cross
// the bridge trust boundary in either direction (A-side capture or B-side
// delivery). Only headers commonly needed by local OAuth callback servers are
// permitted; Cookie, Authorization, Host, Content-Length, and all other
// browser or peer metadata must never be forwarded to the local application.
func allowedCallbackRelayHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "accept", "accept-language", "content-type", "x-requested-with":
		return true
	default:
		return false
	}
}

// filterCallbackRelayHeaders returns the allowlisted subset of a relayed
// header map with canonicalized keys. It is the B-side enforcement point: a
// compromised or buggy peer must not be able to smuggle Authorization, Cookie,
// or other sensitive headers into the localhost application request.
func filterCallbackRelayHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for name, value := range in {
		if value == "" || !allowedCallbackRelayHeader(name) {
			continue
		}
		out[http.CanonicalHeaderKey(name)] = value
	}
	return out
}

// redactBridgeMessage keeps authorization URLs out of errors that may cross a
// process/logging boundary. Query values can contain codes, state, and PKCE
// material, so the safe form intentionally removes the entire query string.
func redactBridgeMessage(message string) string {
	fields := strings.Fields(message)
	for i, field := range fields {
		leftTrimmed := strings.TrimLeft(field, "([<\"'")
		trimmed := strings.TrimRight(leftTrimmed, ",.;)]}")
		if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
			prefix := field[:len(field)-len(leftTrimmed)]
			suffix := leftTrimmed[len(trimmed):]
			fields[i] = prefix + browser.RedactURL(trimmed) + suffix
		}
	}
	return strings.Join(fields, " ")
}

func safeBridgeError(err error) string {
	if err == nil {
		return ""
	}
	return redactBridgeMessage(err.Error())
}

// RedactError returns a log-safe representation of a bridge error. It is
// exported for standalone CLI loggers that do not pass through the Hub's
// structured redactor.
func RedactError(err error) string {
	return safeBridgeError(err)
}

func closeConnOnContext(ctx context.Context, conn transport.Conn) func() {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	return func() { stop() }
}

// receiveEnvelope makes the context contract explicit for transports that do
// not implement deadlines (notably SSH and lightweight test doubles).  Real
// transports use their native deadline support; the goroutine fallback is
// bounded by the caller's connection close and prevents a context deadline
// from being silently ignored.
func receiveEnvelope(ctx context.Context, conn transport.Conn) (*protocol.Envelope, error) {
	dl, hasDeadliner := conn.(transport.Deadliner)
	if supported, ok := conn.(interface{ DeadlineSupported() bool }); ok && !supported.DeadlineSupported() {
		hasDeadliner = false
	}
	stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopConn()
	if hasDeadliner {
		if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
			_ = dl.SetReadDeadline(deadline)
			defer func() { _ = dl.SetReadDeadline(time.Time{}) }()
		}
		return conn.Receive()
	}

	resultCh := make(chan envelopeResult, 1)
	go func() {
		env, err := conn.Receive()
		resultCh <- envelopeResult{env: env, err: err}
	}()
	select {
	case <-ctx.Done():
		// AfterFunc normally closes the connection asynchronously. Close it
		// synchronously as well so a transport without deadlines cannot leave
		// its Receive goroutine blocked after this function returns.
		_ = conn.Close()
		return nil, ctx.Err()
	case result := <-resultCh:
		return result.env, result.err
	}
}

// DeriveOAuthFlowID returns a stable, non-secret identity for retries of the
// same logical authorization flow. OAuth state, PKCE challenges, nonces, and a
// loopback app's ephemeral callback port are intentionally excluded: clients
// commonly regenerate those values when a BROWSER command is retried. Other
// authorization parameters remain part of the identity so two intentionally
// different requests (for example, different audiences or resources) are not
// accidentally coalesced.
func DeriveOAuthFlowID(rawURL string) string {
	canonical := browser.SanitizeURL(strings.TrimSpace(rawURL))
	parsed, err := url.Parse(canonical)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	q := parsed.Query()
	redirectIdentity := ""
	if redirect := strings.TrimSpace(q.Get("redirect_uri")); redirect != "" {
		if redirectURL, parseErr := url.Parse(redirect); parseErr == nil && redirectURL.Hostname() != "" {
			scheme := strings.ToLower(redirectURL.Scheme)
			host := strings.ToLower(redirectURL.Hostname())
			port := redirectURL.Port()
			hostPart := host
			if port != "" {
				hostPart = net.JoinHostPort(host, port)
			}
			path := redirectURL.EscapedPath()
			if path == "" {
				path = "/"
			}
			// A local OAuth app commonly chooses a fresh port for each
			// process. Keep the callback path/query but omit only HTTP
			// loopback ports.
			if (host == "localhost" || host == "127.0.0.1" || host == "::1") && scheme == "http" {
				hostPart = host
			}
			redirectURL.RawQuery = redirectURL.Query().Encode()
			redirectURL.Fragment = ""
			redirectURL.RawFragment = ""
			redirectIdentity = scheme + "://" + hostPart + path
			if redirectURL.RawQuery != "" {
				redirectIdentity += "?" + redirectURL.RawQuery
			}
		}
	}
	if q.Get("client_id") == "" && redirectIdentity == "" {
		return ""
	}

	// Keep all meaningful query parameters, while removing values that are
	// intentionally regenerated by OAuth clients on retries. Query names are
	// handled case-insensitively for the volatile set, while their values are
	// preserved verbatim for meaningful parameters.
	stableQuery := make(url.Values, len(q))
	for key, values := range q {
		lowerKey := strings.ToLower(key)
		switch lowerKey {
		case "state", "code_challenge", "code_challenge_method", "nonce":
			continue
		case "redirect_uri":
			if redirectIdentity != "" {
				stableQuery.Set("redirect_uri", redirectIdentity)
			} else {
				stableQuery[key] = append([]string(nil), values...)
			}
		default:
			stableQuery[key] = append([]string(nil), values...)
		}
	}
	if scopes := strings.Fields(q.Get("scope")); len(scopes) > 0 {
		sort.Strings(scopes)
		stableQuery.Set("scope", strings.Join(scopes, " "))
	}
	if strings.TrimSpace(q.Get("response_type")) == "" {
		stableQuery.Set("response_type", "code")
	}

	parsed.Fragment = ""
	parsed.RawFragment = ""
	base := strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host) + parsed.EscapedPath()
	parts := []string{
		"oauth-flow-v2",
		base,
		stableQuery.Encode(),
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "oauth-" + hex.EncodeToString(digest[:])
}

// oauthSessionKey returns a non-sensitive, stable identity for a logical
// request.  Query parameters are sorted by net/url, so harmless parameter
// ordering changes do not create a second browser session.  The peer identity
// is included to prevent one peer from replaying another peer's completed
// request.
func oauthSessionKey(conn transport.Conn, req protocol.BridgeRequest) string {
	peerIdentity := transport.GetPeerFingerprint(conn)
	if peerIdentity == "" && conn.RemoteAddr() != nil {
		// Loopback and test transports do not carry a certificate.  Using the
		// remote host (not the ephemeral source port) still prevents unrelated
		// remote hosts from sharing a session while keeping retries on one host
		// coalesced.
		peerIdentity = conn.RemoteAddr().String()
		if host, _, err := net.SplitHostPort(peerIdentity); err == nil {
			peerIdentity = host
		}
	}

	flowID := strings.TrimSpace(req.FlowID)
	derivedFlowID := DeriveOAuthFlowID(req.URL)
	if flowID == "" {
		flowID = derivedFlowID
	}
	if flowID != "" {
		// Bind an application-supplied FlowID to the authorization request as
		// well. A peer must not be able to reuse one opaque ID to coalesce two
		// unrelated URLs; state/PKCE/loopback-port churn is already normalized
		// by derivedFlowID.
		flowBinding := derivedFlowID
		if flowBinding == "" {
			flowBinding = browser.SanitizeURL(req.URL)
		}
		raw := strings.ToLower(peerIdentity) + "\x00" + flowID + "\x00" + flowBinding
		digest := sha256.Sum256([]byte(raw))
		return hex.EncodeToString(digest[:])
	}

	canonicalURL := browser.SanitizeURL(req.URL)
	if parsed, err := url.Parse(canonicalURL); err == nil {
		parsed.RawQuery = parsed.Query().Encode()
		canonicalURL = parsed.String()
	}
	raw := fmt.Sprintf("%s\n%d\n%s", strings.ToLower(peerIdentity), req.CallbackPort, canonicalURL)
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}
