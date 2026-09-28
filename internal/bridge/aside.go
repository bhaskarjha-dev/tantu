// Package bridge implements the A-side and B-side handlers for tantu.
package bridge

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// ASideConfig configures the A-side handler.
type ASideConfig struct {
	Timeout     time.Duration        // Max time to wait for callback (default: 5 min)
	OpenBrowser func(string) error   // If nil, browser opening is skipped
	Logger      *log.Logger          // Optional logger for session-scoped output
	Sessions    *OAuthSessionManager // Shared retry/replay coordinator
	// Registry tracks in-flight sign-ins so the operator can list and release
	// them. Optional: a nil registry simply makes sessions unlistable, which is
	// the pre-cancellation behaviour.
	Registry *SessionRegistry
}

type completionResult struct {
	success bool
}

// bindListenErrorText distinguishes "port in use" from "OS forbids binding"
// (notably Windows Hyper-V-excluded ranges surface as WSAEACCES, not
// EADDRINUSE). Conflating them sends users hunting a process that holds
// nothing. Split out for unit testing.
func bindListenErrorText(port int, err error) string {
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return fmt.Sprintf("callback port %d cannot be bound on this machine (OS policy forbids it; on Windows see `netsh interface ipv4 show excludedportrange`). The login cannot proceed for an app pinned to this port", port)
	}
	return fmt.Sprintf("callback port %d is in use on this machine", port)
}

// ASide represents an A-side bridge session handler on Computer A.
type ASide struct {
	conn transport.Conn
	cfg  ASideConfig
}

func (a *ASide) logf(format string, v ...any) {
	if a.cfg.Logger != nil {
		a.cfg.Logger.Printf(format, v...)
	} else {
		log.Printf(format, v...)
	}
}

// NewASide creates a new ASide handler.
func NewASide(conn transport.Conn, cfg ASideConfig) *ASide {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.Sessions == nil {
		cfg.Sessions = NewOAuthSessionManager()
	}
	return &ASide{
		conn: conn,
		cfg:  cfg,
	}
}

// HandleASide processes a single bridge session on the A-side.
// It reads a BridgeRequest, opens a callback listener, captures the callback,
// sends it back as CallbackRelay, and waits for BridgeComplete.
func HandleASide(ctx context.Context, conn transport.Conn, cfg ASideConfig) error {
	return NewASide(conn, cfg).Run(ctx)
}

type callbackExpectationSpec struct {
	path        string
	state       string
	constrained bool
	// authOrigin is the origin of the authorization server, taken from the URL
	// the A-side was asked to open. It is what a legitimate callback's Origin
	// header must match, and it is empty for a synthetic caller that supplied
	// no parseable URL.
	authOrigin string
	// unbound is true when the authorization URL carried neither a loopback
	// redirect_uri nor a loopback state. Such a flow has no redirect binding of
	// its own, so it leans entirely on the navigation and Origin checks below
	// and must be reported as weaker to the user.
	unbound bool
}

// callbackExpectation extracts the callback path/state from a normal OAuth
// redirect_uri.  Older callers in the repository used synthetic URLs without
// a redirect_uri; those remain supported for compatibility, but real OAuth
// URLs are constrained whenever a redirect URI is present.
func callbackExpectation(rawURL string) callbackExpectationSpec {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		// Nothing can be derived from an unparseable URL, so the flow has no
		// binding at all. Report it as unbound so the browser checks apply and
		// the caller can see the flow is weaker.
		return callbackExpectationSpec{unbound: true}
	}
	q := parsed.Query()
	state := q.Get("state")
	redirect := q.Get("redirect_uri")
	path := ""
	constrained := false

	authOrigin := ""
	if parsed.Scheme != "" && parsed.Host != "" {
		authOrigin = strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
	}

	if redirect != "" {
		if redirectURL, parseErr := url.Parse(redirect); parseErr == nil && redirectURL.Hostname() != "" {
			host := strings.ToLower(redirectURL.Hostname())
			if isLoopbackHost(host) && strings.EqualFold(redirectURL.Scheme, "http") {
				path = redirectURL.Path
				constrained = true
			}
		}
	}

	// VS Code-style trampolines put the actual loopback callback URL in
	// state. Inspect that form even when redirect_uri points at an external
	// HTTPS service; otherwise the callback listener would be unconstrained.
	if stateURL, stateErr := url.Parse(state); stateErr == nil && stateURL.Hostname() != "" &&
		isLoopbackHost(stateURL.Hostname()) && strings.EqualFold(stateURL.Scheme, "http") {
		path = stateURL.Path
		constrained = true
	}
	if !constrained {
		// No loopback redirect binding: the application supplied neither a
		// redirect_uri nor a loopback state. Nothing about such a flow ties a
		// callback to the authorization request except the browser checks in
		// validateCallbackRequestState, so it is reported as unbound.
		return callbackExpectationSpec{state: state, authOrigin: authOrigin, unbound: true}
	}
	if path == "" {
		path = "/"
	}
	return callbackExpectationSpec{path: path, state: state, constrained: true, authOrigin: authOrigin}
}

func isLoopbackHost(host string) bool {
	// The full 127/8 range is loopback (RFC 1122), matching the standalone
	// server checks. OAuth delivery still targets 127.0.0.1 exactly; this
	// predicate only answers "is this a loopback interface".
	if strings.EqualFold(host, "localhost") {
		return true
	}
	trimmed := strings.Trim(host, "[]")
	if ip := net.ParseIP(trimmed); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func requestHostIsLoopback(hostHeader string) bool {
	host := strings.TrimSpace(hostHeader)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else {
		host = strings.Trim(host, "[]")
	}
	return isLoopbackHost(host)
}

func validateCallbackRequest(r *http.Request, expected callbackExpectationSpec) error {
	return validateCallbackRequestState(r, expected, "")
}

func validateCallbackRequestState(r *http.Request, expected callbackExpectationSpec, formState string) error {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return fmt.Errorf("unsupported callback method %q", r.Method)
	}
	if !requestHostIsLoopback(r.Host) {
		return errors.New("callback host is not loopback")
	}
	// A loopback Host only defeats DNS rebinding, where the attacker controls
	// the hostname. A page that simply addresses 127.0.0.1 with an IP literal
	// sends a perfectly valid loopback Host, so these two browser-controlled
	// headers are the actual browser-CSRF defence: a real authorization
	// response always arrives as a top-level navigation from the provider's
	// page, and a page-injected fetch() cannot present itself as one.
	if err := validateCallbackFetchMetadata(r, expected); err != nil {
		return err
	}
	if expected.constrained {
		if expected.path != "" && r.URL.Path != expected.path {
			return fmt.Errorf("callback path %q does not match redirect URI", r.URL.Path)
		}
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = formState
	}
	if expected.state != "" {
		// state is the application's own CSRF token when it supplies one.
		// Compare in constant time; it is a secret until the flow completes.
		if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(expected.state)) != 1 {
			return errors.New("callback state does not match authorization request")
		}
	}
	return nil
}

// validateCallbackFetchMetadata rejects a callback that a web page injected
// rather than one the user's browser navigated to.
//
// A genuine OAuth response is a top-level navigation: the provider redirects
// the tab, so Sec-Fetch-Mode is "navigate" and either no Origin is sent (a GET
// redirect) or the Origin is the provider's own (a form_post response). An
// attacker's page issues fetch(), XHR, or a subresource request, and the browser
// labels it "cors", "no-cors", or "same-origin" and stamps the attacker's Origin
// ÔÇö neither of which can be forged from a page, which is the whole point.
//
// Both headers are enforced when present and tolerated when absent, so a client
// that strips them falls back to the state check rather than being locked out.
func validateCallbackFetchMetadata(r *http.Request, expected callbackExpectationSpec) error {
	if mode := strings.TrimSpace(r.Header.Get("Sec-Fetch-Mode")); mode != "" {
		if !strings.EqualFold(mode, "navigate") && !strings.EqualFold(mode, "websocket") {
			return fmt.Errorf("callback was not a top-level navigation (Sec-Fetch-Mode: %s)", mode)
		}
	}
	// A subresource destination (image, script, style, ...) is never an OAuth
	// response. A document, iframe, or frame is a legitimate navigation target.
	switch dest := strings.TrimSpace(r.Header.Get("Sec-Fetch-Dest")); dest {
	case "", "document", "iframe", "frame", "object", "embed", "empty":
	default:
		return fmt.Errorf("callback destination %q is a subresource, not a navigation", dest)
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" && !strings.EqualFold(origin, "null") {
		if expected.authOrigin == "" {
			// No authorization origin is known, so a browser-supplied Origin
			// cannot be checked against anything. Refuse rather than guess.
			return fmt.Errorf("callback carried Origin %s but the authorization origin is unknown", origin)
		}
		if !strings.EqualFold(strings.TrimRight(origin, "/"), expected.authOrigin) {
			return fmt.Errorf("callback Origin %s does not match the authorization origin", origin)
		}
	}
	return nil
}

func callbackHeaders(h http.Header) map[string]string {
	// Do not relay cookies, authorization headers, or arbitrary browser
	// metadata to the application.  These are the headers commonly needed by
	// local OAuth callback servers. The same allowlist is enforced on the
	// B-side at delivery time (filterCallbackRelayHeaders) so a malicious or
	// buggy peer cannot inject headers the A-side would never capture.
	headers := make(map[string]string, 4)
	for name, values := range h {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		if allowedCallbackRelayHeader(name) || inspectableCallbackRelayHeader(name) {
			headers[http.CanonicalHeaderKey(name)] = values[0]
		}
	}
	return headers
}

// Run executes the A-side session lifecycle.
func (a *ASide) Run(parent context.Context) (runErr error) {
	// A cancellable context, not a bare timeout, so the session can be released
	// on user intent. Releasing it is what closes the callback listener below and
	// frees the loopback port, which is the resource an abandoned sign-in
	// otherwise holds until the timeout expires.
	ctx, cancel := context.WithCancelCause(parent)
	timeoutCancel := context.AfterFunc(ctx, func() {})
	defer func() {
		timeoutCancel()
		cancel(nil)
	}()
	// Apply the session timeout as a distinct cause so a timeout and a user
	// cancellation are never reported as the same event.
	timer := time.AfterFunc(a.cfg.Timeout, func() { cancel(context.DeadlineExceeded) })
	defer timer.Stop()
	stopContextClose := closeConnOnContext(ctx, a.conn)
	defer stopContextClose()

	// 1. Read BridgeRequest from connection.
	var req protocol.BridgeRequest
	for {
		env, err := receiveEnvelope(ctx, a.conn)
		if err != nil {
			return fmt.Errorf("receive bridge_request: %w", err)
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			if err := env.DecodePayload(&hb); err != nil {
				return fmt.Errorf("decode heartbeat: %w", err)
			}
			if err := a.conn.Send(protocol.TypeHeartbeat, hb); err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
			continue
		}
		if env.Type != protocol.TypeBridgeRequest {
			return fmt.Errorf("expected %q, got %q", protocol.TypeBridgeRequest, env.Type)
		}
		if err := env.DecodePayload(&req); err != nil {
			return fmt.Errorf("decode bridge_request: %w", err)
		}
		break
	}

	if !validBridgeRequestID(req.RequestID) {
		ackErr := "invalid or missing bridge request ID"
		_ = a.conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{RequestID: req.RequestID, Error: ackErr})
		return errors.New(ackErr)
	}
	if len(req.URL) > maxOAuthURLLength {
		ackErr := "OAuth authorization URL exceeds the maximum supported length"
		_ = a.conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{RequestID: req.RequestID, Error: ackErr})
		return errors.New(ackErr)
	}
	if len(req.FlowID) > maxOAuthFlowIDLength {
		ackErr := "OAuth flow ID exceeds the maximum supported length"
		_ = a.conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{RequestID: req.RequestID, Error: ackErr})
		return errors.New(ackErr)
	}

	a.logf("­ƒöù Session started: handling request %s (callback port %d)", req.RequestID, req.CallbackPort)

	// Track the session before binding anything, so an abandoned sign-in is
	// always listable and always releasable from the moment it exists. The
	// registry records the session's own cancel function, so releasing an entry
	// unblocks the wait below and closes the listener via its defer.
	sessionPeer := transport.GetPeerFingerprint(a.conn)
	// listenerClosed is signalled after the loopback listener is closed, so a
	// cancel can wait for the port to be genuinely free rather than merely
	// requested to be released. Without it a retry races the teardown and fails
	// with "port in use".
	listenerClosed := make(chan struct{})
	var signalListenerClosed sync.Once
	defer signalListenerClosed.Do(func() { close(listenerClosed) })

	unregister, regErr := a.cfg.Registry.Register(ctx, cancel, listenerClosed, SessionInfo{
		RequestID:       req.RequestID,
		FlowID:          strings.TrimSpace(req.FlowID),
		CallbackPort:    req.CallbackPort,
		PeerFingerprint: sessionPeer,
		State:           SessionStateWaitingBrowser,
	})
	if regErr != nil {
		a.logf("❌ Session error: %v", regErr)
		ackErr := "too many sign-ins are already open on this machine; close one and retry"
		_ = a.conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{RequestID: req.RequestID, Error: ackErr})
		return regErr
	}
	defer unregister()

	// 2. Validate URL before allocating a listener or touching the browser.
	req.URL = browser.SanitizeURL(req.URL)
	if err := browser.ValidateURL(req.URL); err != nil {
		ackErr := fmt.Sprintf("invalid url: %v", err)
		a.logf("ÔØî Session error: %s", ackErr)
		_ = a.conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{
			RequestID:     req.RequestID,
			ListeningPort: 0,
			Error:         ackErr,
		})
		return fmt.Errorf("validate url: %w", err)
	}
	if req.CallbackPort < 0 || req.CallbackPort > 65535 {
		ackErr := fmt.Sprintf("invalid callback port %d", req.CallbackPort)
		_ = a.conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{
			RequestID: req.RequestID,
			Error:     ackErr,
		})
		return errors.New(ackErr)
	}

	// Coalesce retries of the same logical OAuth transaction.  A retry gets a
	// new transport connection and request ID, so connection-local state alone
	// cannot prevent the browser/page storm seen in production. Only
	// byte-identical requests share a session: a new login mints fresh
	// per-attempt values and always opens its own browser flow.
	lease := a.cfg.Sessions.acquire(oauthSessionKey(a.conn, req))
	if lease.err != nil {
		ackErr := "too many active OAuth sessions"
		_ = a.conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{RequestID: req.RequestID, Error: ackErr})
		return errors.New(ackErr)
	}
	if !lease.owner {
		a.logf("ÔÖ╗´©Å Duplicate OAuth request suppressed for callback port %d", req.CallbackPort)
		waitCtx, waitCancel := context.WithTimeout(ctx, a.cfg.Timeout)
		waitErr := lease.wait(waitCtx)
		waitCancel()
		if waitErr != nil {
			ackErr := fmt.Sprintf("duplicate OAuth request did not complete: %v", waitErr)
			_ = a.conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{
				RequestID: req.RequestID,
				Error:     ackErr,
			})
			return fmt.Errorf("duplicate OAuth request: %w", waitErr)
		}
		if err := a.conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{
			RequestID: req.RequestID,
			Replay:    true,
		}); err != nil {
			return fmt.Errorf("send duplicate bridge_ack: %w", err)
		}
		return nil
	}
	defer func() { lease.finish(runErr) }()

	// A sign-in that wants a port an older in-flight session is still holding is
	// the retry-after-abandon case. The application's redirect port is normally
	// fixed, so the stale session owns a resource the user has already given up
	// on, and without this the new attempt would fail to bind with "port is in
	// use" and the only recovery would be restarting the process. Releasing the
	// stale session here is scoped to the same peer and excludes this session.
	for _, stale := range a.cfg.Registry.CancelOnPort(req.CallbackPort, sessionPeer, req.RequestID) {
		a.logf("Releasing an earlier abandoned sign-in on callback port %d (request %s) so this attempt can proceed", req.CallbackPort, stale.RequestID)
	}

	// 3. Bind HTTP listener on 127.0.0.1:req.CallbackPort (exact port, no fallback).
	bindAddr := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", req.CallbackPort))
	listener, err := net.Listen("tcp", bindAddr)
	if err != nil {
		ackErr := bindListenErrorText(req.CallbackPort, err)
		a.logf("ÔØî Session error: %s", ackErr)
		_ = a.conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{
			RequestID:     req.RequestID,
			ListeningPort: 0,
			Error:         ackErr,
		})
		return fmt.Errorf("bind callback listener: %w", err)
	}
	// Two defers, and their order is the point. Defers run last-in-first-out, so
	// listener.Close() runs before listenerClosed is signalled. A canceller
	// waiting on that channel is about to bind this same port, and signalling
	// first would let it proceed while the port was still held - which is the
	// "port is in use" dead end this path exists to remove. The sync.Once covers
	// the outer defer, for sessions that end before any listener exists.
	defer signalListenerClosed.Do(func() { close(listenerClosed) })
	defer listener.Close()

	actualPort := 0
	if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
		actualPort = tcpAddr.Port
	}
	if actualPort == 0 {
		return errors.New("callback listener did not return a TCP port")
	}

	// Send BridgeAck to B-side.
	ack := protocol.BridgeAck{
		RequestID:     req.RequestID,
		ListeningPort: actualPort,
	}
	if err := a.conn.Send(protocol.TypeBridgeAck, ack); err != nil {
		return fmt.Errorf("send bridge_ack: %w", err)
	}

	a.logf("Callback listener active on 127.0.0.1:%d", actualPort)

	// Open browser if configured.  Never put the query string in logs: it can
	// contain state, PKCE challenges, and (in callback requests) auth codes.
	if a.cfg.OpenBrowser != nil {
		a.cfg.Registry.SetState(req.RequestID, SessionStateWaitingCallback)
		a.logf("­ƒîÉ Opening browser for URL: %s", browser.RedactURL(req.URL))
		if err := a.cfg.OpenBrowser(req.URL); err != nil {
			a.logf("ÔØî failed to open browser: %s", safeBridgeError(err))
		}
	}

	// 4. Set up HTTP callback server.
	callbackCh := make(chan *protocol.CallbackRelay, 1)
	callbackErrCh := make(chan error, 1)
	completionCh := make(chan completionResult, 1)
	var captureMu sync.Mutex
	captured := false
	expected := callbackExpectation(req.URL)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if err := validateCallbackRequest(r, callbackExpectationSpec{}); err != nil {
			a.logf("ÔÜá´©Å Rejected callback request: %v", err)
			http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
			return
		}

		body, readErr := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if readErr != nil {
			a.logf("ÔÜá´©Å Failed to read callback body: %v", readErr)
			callbackErrCh <- fmt.Errorf("read callback body: %w", readErr)
			http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
			return
		}
		if len(body) > 1<<20 {
			err := errors.New("callback body exceeds 1 MiB limit")
			a.logf("ÔÜá´©Å %v", err)
			callbackErrCh <- err
			http.Error(w, "invalid OAuth callback", http.StatusRequestEntityTooLarge)
			return
		}
		formState := ""
		if r.Method == http.MethodPost && strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
			if form, parseErr := url.ParseQuery(string(body)); parseErr == nil {
				formState = form.Get("state")
			}
		}
		if err := validateCallbackRequestState(r, expected, formState); err != nil {
			a.logf("ÔÜá´©Å Rejected callback request: %v", err)
			http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
			return
		}
		captureMu.Lock()
		alreadyCaptured := captured
		if !alreadyCaptured {
			captured = true
		}
		captureMu.Unlock()
		a.logf("­ƒôÑ Callback received: %s %s", r.Method, r.URL.Path)
		if alreadyCaptured {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte("<html><body>OAuth callback was already captured.</body></html>"))
			return
		}
		relay := &protocol.CallbackRelay{
			RequestID:  req.RequestID,
			Method:     r.Method,
			Path:       r.URL.RequestURI(),
			Headers:    callbackHeaders(r.Header),
			Body:       body,
			StatusCode: http.StatusOK,
		}
		callbackCh <- relay

		// Hold the first HTTP response until BridgeComplete confirms delivery
		// or a bounded timeout.  This prevents providers from retrying while
		// the B-side is still processing the callback.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		select {
		case result := <-completionCh:
			if result.success {
				_, _ = w.Write([]byte(`<html><head><meta charset="utf-8"><title>tantu</title>` +
					`<style>body{font-family:system-ui,sans-serif;display:flex;align-items:center;` +
					`justify-content:center;min-height:100vh;margin:0;background:#111;color:#eee}` +
					`.card{text-align:center;padding:40px}</style></head>` +
					`<body><div class="card"><h1>Ô£à Authentication complete</h1>` +
					`<p>You can close this tab.</p></div></body></html>`))
			} else {
				_, _ = w.Write([]byte(`<html><head><meta charset="utf-8"><title>tantu</title>` +
					`<style>body{font-family:system-ui,sans-serif;display:flex;align-items:center;` +
					`justify-content:center;min-height:100vh;margin:0;background:#111;color:#eee}` +
					`.card{text-align:center;padding:40px}</style></head>` +
					`<body><div class="card"><h1>ÔÜá´©Å Callback captured</h1>` +
					`<p>Delivery to the application may have failed.</p></div></body></html>`))
			}
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
			_, _ = w.Write([]byte(`<html><head><meta charset="utf-8"><title>tantu</title>` +
				`<style>body{font-family:system-ui,sans-serif;display:flex;align-items:center;` +
				`justify-content:center;min-height:100vh;margin:0;background:#111;color:#eee}` +
				`.card{text-align:center;padding:40px}</style></head>` +
				`<body><div class="card"><h1>ÔÜá´©Å Callback captured</h1>` +
				`<p>Delivery status unknown. You can close this tab.</p></div></body></html>`))
		}
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// The callback response intentionally waits for BridgeComplete;
		// bound it by the session lifetime rather than expiring while a
		// one-time code is still being delivered.
		WriteTimeout:   a.cfg.Timeout + 15*time.Second,
		IdleTimeout:    30 * time.Second,
		MaxHeaderBytes: 16 * 1024,
	}
	serveErrCh := make(chan error, 1)
	go func() {
		if serveErr := srv.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
			serveErrCh <- serveErr
		}
	}()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(shutdownCtx)
		shutdownCancel()
		// Shutdown waits for handlers but does not force-close a handler that
		// ignores its context. Close as a final bound so a canceled session
		// cannot leave a callback socket alive until the browser timeout.
		_ = srv.Close()
	}()

	// 5. Wait for HTTP callback, session cancellation, or listener failure.
	var relay *protocol.CallbackRelay
	select {
	case <-ctx.Done():
		signalCompletion(completionCh, false)
		// A user-cancelled session is not a failure and must not read like one:
		// the operator chose to walk away, and the resources are being released
		// right now rather than at a timeout.
		if cause := context.Cause(ctx); errors.Is(cause, ErrSessionCancelled) {
			a.logf("🚪 Sign-in released: you cancelled it. The callback port is free again.")
			return ErrSessionCancelled
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			a.logf("ÔØî Session error: timeout waiting for authentication callback")
			return errors.New("timeout waiting for authentication callback")
		}
		a.logf("ÔØî Session error: %v", ctx.Err())
		return ctx.Err()
	case serveErr := <-serveErrCh:
		signalCompletion(completionCh, false)
		return fmt.Errorf("callback server: %w", serveErr)
	case callbackErr := <-callbackErrCh:
		signalCompletion(completionCh, false)
		return callbackErr
	case relay = <-callbackCh:
	}

	// 6. Send CallbackRelay over bridge connection.
	if err := a.conn.Send(protocol.TypeCallbackRelay, relay); err != nil {
		signalCompletion(completionCh, false)
		a.logf("ÔØî Session error: %v", err)
		return fmt.Errorf("send callback_relay: %w", err)
	}
	a.cfg.Registry.SetState(req.RequestID, SessionStateCompleting)
	a.logf("­ƒôª Callback relayed to B-side, awaiting completion")

	// 7. Wait for the correlated BridgeComplete from B-side.
	for {
		env, err := receiveEnvelope(ctx, a.conn)
		if err != nil {
			a.logf("ÔØî Session error: %v", err)
			signalCompletion(completionCh, false)
			return err
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			if err := env.DecodePayload(&hb); err != nil {
				signalCompletion(completionCh, false)
				return fmt.Errorf("decode heartbeat: %w", err)
			}
			if err := a.conn.Send(protocol.TypeHeartbeat, hb); err != nil {
				signalCompletion(completionCh, false)
				return err
			}
			continue
		}
		if env.Type != protocol.TypeBridgeComplete {
			signalCompletion(completionCh, false)
			return fmt.Errorf("unexpected message type: %s", env.Type)
		}
		var comp protocol.BridgeComplete
		if err := env.DecodePayload(&comp); err != nil {
			signalCompletion(completionCh, false)
			return fmt.Errorf("decode bridge_complete: %w", err)
		}
		if comp.RequestID != req.RequestID {
			signalCompletion(completionCh, false)
			return fmt.Errorf("bridge_complete request ID mismatch: got %q, want %q", comp.RequestID, req.RequestID)
		}
		if !comp.Success {
			signalCompletion(completionCh, false)
			if comp.Error != "" {
				return errors.New(redactBridgeMessage(comp.Error))
			}
			return errors.New("bridge completion reported failure")
		}
		signalCompletion(completionCh, true)
		a.logf("Ô£à Session completed successfully")
		return nil
	}
}

func signalCompletion(ch chan<- completionResult, success bool) {
	select {
	case ch <- completionResult{success: success}:
	default:
	}
}
