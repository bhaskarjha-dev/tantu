package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// BSideConfig configures the B-side handler.
type BSideConfig struct {
	Timeout time.Duration // Max time to wait for callback relay (default: 5 min)
	Logger  *log.Logger   // Optional logger for session-scoped output
	FlowID  string        // Stable application flow/idempotency identity across retries
}

// BSide represents a B-side bridge session handler on Computer B.
type BSide struct {
	conn transport.Conn
	cfg  BSideConfig
}

func (b *BSide) logf(format string, v ...any) {
	if b.cfg.Logger != nil {
		b.cfg.Logger.Printf(format, v...)
	}
}

// NewBSide creates a new BSide handler.
func NewBSide(conn transport.Conn, cfg BSideConfig) *BSide {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	return &BSide{conn: conn, cfg: cfg}
}

// HandleBSide processes a single bridge session on the B-side.
// oauthURL is the full OAuth authorization URL.
// It extracts the callback port, sends a BridgeRequest, waits for the callback relay,
// delivers it to the app's localhost listener, and sends BridgeComplete.
func HandleBSide(ctx context.Context, conn transport.Conn, oauthURL string, cfg BSideConfig) error {
	return NewBSide(conn, cfg).Run(ctx, oauthURL)
}

// ExtractCallbackPort parses the OAuth authorization URL, locates the redirect_uri query parameter,
// and extracts the port number. If redirect_uri points to an external trampoline service (e.g. https://vscode.dev/redirect),
// it inspects the state query parameter for an embedded loopback callback URL.
func ExtractCallbackPort(oauthURL string) (int, error) {
	oauthURL = browser.SanitizeURL(oauthURL)
	parsed, err := url.Parse(oauthURL)
	if err != nil {
		return 0, errors.New("invalid oauth url")
	}

	q := parsed.Query()
	extractPortFromURLStr := func(raw string) (int, bool) {
		if raw == "" {
			return 0, false
		}
		u, err := url.Parse(raw)
		if err != nil {
			if unescaped, uerr := url.QueryUnescape(raw); uerr == nil {
				u, err = url.Parse(unescaped)
			}
		}
		if err != nil || u == nil {
			return 0, false
		}
		pStr := u.Port()
		if pStr == "" {
			return 0, false
		}
		p, err := strconv.Atoi(pStr)
		return p, err == nil && p > 0 && p <= 65535
	}

	redirectURIStr := q.Get("redirect_uri")
	stateStr := q.Get("state")
	if redirectURIStr == "" {
		return 0, errors.New("missing redirect_uri parameter in oauth url")
	}
	redirectURI, err := url.Parse(redirectURIStr)
	if err != nil {
		// Avoid echoing redirect_uri, which can contain provider-specific
		// state or other sensitive material.
		return 0, errors.New("invalid redirect_uri parameter in oauth url")
	}
	redirectHost := strings.ToLower(redirectURI.Hostname())
	if redirectHost != "localhost" && redirectHost != "127.0.0.1" && redirectHost != "::1" {
		stateURL, stateErr := url.Parse(stateStr)
		if stateErr == nil && (strings.ToLower(stateURL.Hostname()) == "localhost" || strings.ToLower(stateURL.Hostname()) == "127.0.0.1" || strings.ToLower(stateURL.Hostname()) == "::1") && strings.EqualFold(stateURL.Scheme, "http") {
			if statePort, ok := extractPortFromURLStr(stateStr); ok {
				return statePort, nil
			}
		}
		return 0, fmt.Errorf("redirect_uri host %q has no supported loopback callback", redirectURI.Hostname())
	}
	portStr := redirectURI.Port()
	if portStr == "" {
		if redirectURI.Scheme == "https" {
			return 443, nil
		}
		return 80, nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q in redirect_uri", portStr)
	}
	return port, nil
}

func generateRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func redirectCallbackHostIsLoopback(oauthURL string) error {
	parsed, err := url.Parse(oauthURL)
	if err != nil {
		return errors.New("parse oauth URL failed")
	}
	redirect := parsed.Query().Get("redirect_uri")
	if redirect == "" {
		return errors.New("missing redirect_uri parameter in oauth url")
	}
	redirectURL, err := url.Parse(redirect)
	if err != nil {
		return errors.New("parse redirect_uri failed")
	}
	redirectHost := strings.ToLower(redirectURL.Hostname())
	if redirectHost == "localhost" || redirectHost == "127.0.0.1" || redirectHost == "::1" {
		if !strings.EqualFold(redirectURL.Scheme, "http") {
			return errors.New("loopback redirect_uri must use http")
		}
		return nil
	}
	// A few providers use an external trampoline and carry the actual
	// loopback redirect in state.  Only accept that narrow, explicit form.
	state := parsed.Query().Get("state")
	stateURL, stateErr := url.Parse(state)
	if stateErr == nil && strings.EqualFold(stateURL.Scheme, "http") {
		stateHost := strings.ToLower(stateURL.Hostname())
		if stateHost == "localhost" || stateHost == "127.0.0.1" || stateHost == "::1" {
			return nil
		}
	}
	return fmt.Errorf("redirect_uri host %q is not loopback", redirectURL.Hostname())
}

func callbackRequestURL(port int, relayPath string) (*url.URL, error) {
	if relayPath == "" {
		relayPath = "/"
	}
	parsed, err := url.ParseRequestURI(relayPath)
	if err != nil {
		// Do not include the peer-supplied URI in the error: it may contain an
		// authorization code or other one-time callback material.
		return nil, errors.New("invalid callback request URI")
	}
	if parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return nil, errors.New("callback request URI must be an absolute path")
	}
	return &url.URL{
		Scheme:   "http",
		Host:     net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Path:     parsed.Path,
		RawPath:  parsed.RawPath,
		RawQuery: parsed.RawQuery,
	}, nil
}

func validateCallbackRelay(expected callbackExpectationSpec, relay protocol.CallbackRelay) error {
	if len(relay.Body) > 1<<20 {
		return errors.New("callback relay body is too large")
	}
	if relay.Method != "" && relay.Method != http.MethodGet && relay.Method != http.MethodPost {
		return errors.New("unsupported callback method")
	}
	parsed, err := url.ParseRequestURI(relay.Path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return errors.New("callback relay path must be an absolute path")
	}
	if !expected.constrained {
		return nil
	}
	if expected.path != "" && parsed.Path != expected.path {
		return errors.New("callback relay path does not match authorization request")
	}
	state := parsed.Query().Get("state")
	if relay.Method == http.MethodPost && len(relay.Body) > 0 && len(relay.Body) <= 1<<20 {
		contentType := relay.Headers["Content-Type"]
		if contentType == "" {
			for key, value := range relay.Headers {
				if strings.EqualFold(key, "Content-Type") {
					contentType = value
					break
				}
			}
		}
		if strings.HasPrefix(strings.ToLower(contentType), "application/x-www-form-urlencoded") {
			if form, parseErr := url.ParseQuery(string(relay.Body)); parseErr == nil {
				formState := form.Get("state")
				if formState != "" {
					if state != "" && state != formState {
						return errors.New("callback relay state values disagree")
					}
					state = formState
				}
			}
		}
	}
	if expected.state != "" && state != expected.state {
		return errors.New("callback relay state does not match authorization request")
	}
	return nil
}

func sendBridgeComplete(conn transport.Conn, requestID string, success bool, cause error) error {
	comp := protocol.BridgeComplete{RequestID: requestID, Success: success}
	if cause != nil {
		comp.Error = safeBridgeError(cause)
	}
	return conn.Send(protocol.TypeBridgeComplete, comp)
}

// Run executes the B-side session lifecycle.
func (b *BSide) Run(parent context.Context, oauthURL string) error {
	oauthURL = browser.SanitizeURL(oauthURL)
	if len(oauthURL) > maxOAuthURLLength {
		return errors.New("OAuth authorization URL exceeds the maximum supported length")
	}
	if len(strings.TrimSpace(b.cfg.FlowID)) > maxOAuthFlowIDLength {
		return errors.New("OAuth flow ID exceeds the maximum supported length")
	}
	if err := browser.ValidateURL(oauthURL); err != nil {
		return fmt.Errorf("validate OAuth URL: %w", err)
	}
	if err := redirectCallbackHostIsLoopback(oauthURL); err != nil {
		return fmt.Errorf("validate OAuth redirect: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, b.cfg.Timeout)
	defer cancel()
	stopContextClose := closeConnOnContext(ctx, b.conn)
	defer stopContextClose()

	port, err := ExtractCallbackPort(oauthURL)
	if err != nil {
		return fmt.Errorf("extract callback port: %w", err)
	}

	reqID := generateRequestID()
	flowID := strings.TrimSpace(b.cfg.FlowID)
	if flowID == "" {
		flowID = DeriveOAuthFlowID(oauthURL)
	}
	req := protocol.BridgeRequest{URL: oauthURL, CallbackPort: port, RequestID: reqID, FlowID: flowID}
	if err := b.conn.Send(protocol.TypeBridgeRequest, req); err != nil {
		b.logf("❌ Session error: send bridge_request: %v", err)
		return fmt.Errorf("send bridge_request: %w", err)
	}
	b.logf("🔗 Session started: sending request %s for port %d", reqID, port)

	// 1. Receive BridgeAck.
	for {
		env, err := receiveEnvelope(ctx, b.conn)
		if err != nil {
			return fmt.Errorf("receive bridge_ack: %w", err)
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			if err := env.DecodePayload(&hb); err != nil {
				return fmt.Errorf("decode heartbeat: %w", err)
			}
			if err := b.conn.Send(protocol.TypeHeartbeat, hb); err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
			continue
		}
		if env.Type != protocol.TypeBridgeAck {
			return fmt.Errorf("expected %q, got %q", protocol.TypeBridgeAck, env.Type)
		}
		var ack protocol.BridgeAck
		if err := env.DecodePayload(&ack); err != nil {
			return fmt.Errorf("decode bridge_ack: %w", err)
		}
		if ack.RequestID != reqID {
			return fmt.Errorf("bridge_ack request ID mismatch: got %q, want %q", ack.RequestID, reqID)
		}
		if ack.Error != "" {
			return fmt.Errorf("bridge ack error: %s", redactBridgeMessage(ack.Error))
		}
		if ack.Replay {
			b.logf("✅ Replayed completed OAuth session %s", reqID)
			return nil
		}
		if ack.ListeningPort <= 0 || ack.ListeningPort > 65535 {
			return fmt.Errorf("invalid bridge_ack listening port %d", ack.ListeningPort)
		}
		break
	}

	// 2. Receive the correlated callback relay.
	var relay protocol.CallbackRelay
	for {
		env, err := receiveEnvelope(ctx, b.conn)
		if err != nil {
			return fmt.Errorf("receive callback_relay: %w", err)
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			if err := env.DecodePayload(&hb); err != nil {
				return fmt.Errorf("decode heartbeat: %w", err)
			}
			if err := b.conn.Send(protocol.TypeHeartbeat, hb); err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
			continue
		}
		if env.Type != protocol.TypeCallbackRelay {
			return fmt.Errorf("expected %q, got %q", protocol.TypeCallbackRelay, env.Type)
		}
		if err := env.DecodePayload(&relay); err != nil {
			b.logf("❌ Session error: decode callback_relay: %v", err)
			return fmt.Errorf("decode callback_relay: %w", err)
		}
		break
	}
	if relay.RequestID != reqID {
		return fmt.Errorf("callback_relay request ID mismatch: got %q, want %q", relay.RequestID, reqID)
	}

	method := relay.Method
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodPost {
		return errors.New("unsupported callback method")
	}
	if err := validateCallbackRelay(callbackExpectation(oauthURL), relay); err != nil {
		return err
	}
	targetURL, err := callbackRequestURL(port, relay.Path)
	if err != nil {
		return err
	}
	b.logf("📦 Callback received from bridge, delivering to localhost:%d", port)

	// 3. Deliver exactly to loopback. Redirects are intentionally disabled:
	// following a callback redirect could disclose the authorization code to
	// an arbitrary host.  A 3xx response is considered a successful handoff;
	// local OAuth applications commonly use one for their success page.
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	var deliveryErr error
	// OAuth authorization callbacks are state-changing even when the provider
	// uses GET: the code/query can be consumed by the local app before the
	// response reaches us. Never redeliver a callback automatically; a failed
	// handoff must be retried as a new user-visible flow.
	maxAttempts := 1
	for attempt := 0; attempt < maxAttempts; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, method, targetURL.String(), bytes.NewReader(relay.Body))
		if err != nil {
			deliveryErr = fmt.Errorf("create delivery request: %w", err)
			break
		}
		for k, v := range filterCallbackRelayHeaders(relay.Headers) {
			httpReq.Header.Set(k, v)
		}
		if host := relay.Headers["Host"]; host != "" && requestHostIsLoopback(host) {
			httpReq.Host = host
		}
		resp, err := client.Do(httpReq)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<20))
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				deliveryErr = nil
				break
			}
			deliveryErr = fmt.Errorf("local callback returned HTTP %d", resp.StatusCode)
			// A response was received; retrying a state-changing callback
			// could consume the authorization code twice.
			break
		}
		deliveryErr = err
		if ctxErr := ctx.Err(); ctxErr != nil {
			deliveryErr = ctxErr
		}
	}
	if deliveryErr != nil {
		wrapped := fmt.Errorf("deliver callback to localhost:%d failed: %w", port, deliveryErr)
		_ = sendBridgeComplete(b.conn, reqID, false, wrapped)
		return wrapped
	}

	if err := sendBridgeComplete(b.conn, reqID, true, nil); err != nil {
		b.logf("❌ Session error: send bridge_complete: %v", err)
		return fmt.Errorf("send bridge_complete: %w", err)
	}
	b.logf("✅ Session complete")
	return nil
}
