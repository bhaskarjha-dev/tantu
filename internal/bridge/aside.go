// Package bridge implements the A-side and B-side handlers for tantu.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
}

type completionResult struct {
	success bool
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
}

// callbackExpectation extracts the callback path/state from a normal OAuth
// redirect_uri.  Older callers in the repository used synthetic URLs without
// a redirect_uri; those remain supported for compatibility, but real OAuth
// URLs are constrained whenever a redirect URI is present.
func callbackExpectation(rawURL string) callbackExpectationSpec {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return callbackExpectationSpec{}
	}
	q := parsed.Query()
	state := q.Get("state")
	redirect := q.Get("redirect_uri")
	path := ""
	constrained := false

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
		return callbackExpectationSpec{state: state}
	}
	if path == "" {
		path = "/"
	}
	return callbackExpectationSpec{path: path, state: state, constrained: true}
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
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
	if expected.constrained {
		if expected.path != "" && r.URL.Path != expected.path {
			return fmt.Errorf("callback path %q does not match redirect URI", r.URL.Path)
		}
		state := r.URL.Query().Get("state")
		if state == "" {
			state = formState
		}
		if expected.state != "" && state != expected.state {
			return errors.New("callback state does not match authorization request")
		}
	}
	return nil
}

func callbackHeaders(h http.Header) map[string]string {
	// Do not relay cookies, authorization headers, or arbitrary browser
	// metadata to the application.  These are the headers commonly needed by
	// local OAuth callback servers.
	allowed := []string{
		"Accept",
		"Accept-Language",
		"Content-Type",
		"X-Requested-With",
	}
	headers := make(map[string]string, len(allowed))
	for _, name := range allowed {
		if value := h.Get(name); value != "" {
			headers[name] = value
		}
	}
	return headers
}

// Run executes the A-side session lifecycle.
func (a *ASide) Run(parent context.Context) (runErr error) {
	ctx, cancel := context.WithTimeout(parent, a.cfg.Timeout)
	defer cancel()
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

	a.logf("🔗 Session started: handling request %s (callback port %d)", req.RequestID, req.CallbackPort)

	// 2. Validate URL before allocating a listener or touching the browser.
	req.URL = browser.SanitizeURL(req.URL)
	if err := browser.ValidateURL(req.URL); err != nil {
		ackErr := fmt.Sprintf("invalid url: %v", err)
		a.logf("❌ Session error: %s", ackErr)
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
	// cannot prevent the browser/page storm seen in production.
	lease := a.cfg.Sessions.acquire(oauthSessionKey(a.conn, req))
	if !lease.owner {
		a.logf("♻️ Duplicate OAuth request suppressed for callback port %d", req.CallbackPort)
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

	// 3. Bind HTTP listener on 127.0.0.1:req.CallbackPort (exact port, no fallback).
	bindAddr := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", req.CallbackPort))
	listener, err := net.Listen("tcp", bindAddr)
	if err != nil {
		ackErr := fmt.Sprintf("callback port %d is in use on this machine", req.CallbackPort)
		a.logf("❌ Session error: %s", ackErr)
		_ = a.conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{
			RequestID:     req.RequestID,
			ListeningPort: 0,
			Error:         ackErr,
		})
		return fmt.Errorf("bind callback listener: %w", err)
	}
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
		a.logf("🌐 Opening browser for URL: %s", browser.RedactURL(req.URL))
		if err := a.cfg.OpenBrowser(req.URL); err != nil {
			a.logf("❌ failed to open browser: %s", safeBridgeError(err))
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
			a.logf("⚠️ Rejected callback request: %v", err)
			http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
			return
		}

		body, readErr := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if readErr != nil {
			a.logf("⚠️ Failed to read callback body: %v", readErr)
			callbackErrCh <- fmt.Errorf("read callback body: %w", readErr)
			http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
			return
		}
		if len(body) > 1<<20 {
			err := errors.New("callback body exceeds 1 MiB limit")
			a.logf("⚠️ %v", err)
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
			a.logf("⚠️ Rejected callback request: %v", err)
			http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
			return
		}
		captureMu.Lock()
		alreadyCaptured := captured
		if !alreadyCaptured {
			captured = true
		}
		captureMu.Unlock()
		a.logf("📥 Callback received: %s %s", r.Method, r.URL.Path)
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
					`<body><div class="card"><h1>✅ Authentication complete</h1>` +
					`<p>You can close this tab.</p></div></body></html>`))
			} else {
				_, _ = w.Write([]byte(`<html><head><meta charset="utf-8"><title>tantu</title>` +
					`<style>body{font-family:system-ui,sans-serif;display:flex;align-items:center;` +
					`justify-content:center;min-height:100vh;margin:0;background:#111;color:#eee}` +
					`.card{text-align:center;padding:40px}</style></head>` +
					`<body><div class="card"><h1>⚠️ Callback captured</h1>` +
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
				`<body><div class="card"><h1>✅ Callback captured</h1>` +
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
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			a.logf("❌ Session error: timeout waiting for authentication callback")
			return errors.New("timeout waiting for authentication callback")
		}
		a.logf("❌ Session error: %v", ctx.Err())
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
		a.logf("❌ Session error: %v", err)
		return fmt.Errorf("send callback_relay: %w", err)
	}
	a.logf("📦 Callback relayed to B-side, awaiting completion")

	// 7. Wait for the correlated BridgeComplete from B-side.
	for {
		env, err := receiveEnvelope(ctx, a.conn)
		if err != nil {
			a.logf("❌ Session error: %v", err)
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
		a.logf("✅ Session completed successfully")
		return nil
	}
}

func signalCompletion(ch chan<- completionResult, success bool) {
	select {
	case ch <- completionResult{success: success}:
	default:
	}
}
