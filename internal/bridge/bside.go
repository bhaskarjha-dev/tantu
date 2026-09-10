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
	return &BSide{
		conn: conn,
		cfg:  cfg,
	}
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
		return 0, fmt.Errorf("invalid oauth url: %w", err)
	}

	q := parsed.Query()

	// Helper to extract a valid port from a URL string
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
		if err == nil && u != nil {
			pStr := u.Port()
			if pStr != "" {
				if p, err := strconv.Atoi(pStr); err == nil && p > 0 && p <= 65535 {
					return p, true
				}
			}
		}
		return 0, false
	}

	redirectURIStr := q.Get("redirect_uri")

	// Check if state contains a loopback callback URL (VS Code / Copilot trampoline pattern)
	stateStr := q.Get("state")
	if statePort, ok := extractPortFromURLStr(stateStr); ok {
		// If redirect_uri has an explicit custom port, redirect_uri takes precedence.
		// Otherwise (e.g. vscode.dev/redirect without custom port), the state loopback port wins.
		if redirectPort, hasPort := extractPortFromURLStr(redirectURIStr); hasPort {
			return redirectPort, nil
		}
		return statePort, nil
	}

	if redirectURIStr == "" {
		return 0, errors.New("missing redirect_uri parameter in oauth url")
	}

	redirectURI, err := url.Parse(redirectURIStr)
	if err != nil {
		return 0, fmt.Errorf("invalid redirect_uri %q: %w", redirectURIStr, err)
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

// Run executes the B-side session lifecycle.
func (b *BSide) Run(ctx context.Context, oauthURL string) error {
	oauthURL = browser.SanitizeURL(oauthURL)
	ctx, cancel := context.WithTimeout(ctx, b.cfg.Timeout)
	defer cancel()

	// 1. Extract callback port
	port, err := ExtractCallbackPort(oauthURL)
	if err != nil {
		return fmt.Errorf("extract callback port: %w", err)
	}

	// 2. Generate request ID and send BridgeRequest
	reqID := generateRequestID()
	req := protocol.BridgeRequest{
		URL:          oauthURL,
		CallbackPort: port,
		RequestID:    reqID,
	}

	if err := b.conn.Send(protocol.TypeBridgeRequest, req); err != nil {
		b.logf("❌ Session error: send bridge_request: %v", err)
		return fmt.Errorf("send bridge_request: %w", err)
	}
	b.logf("🔗 Session started: sending request %s for port %d", reqID, port)

	// 3. Receive BridgeAck
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		env, err := b.conn.Receive()
		if err != nil {
			return fmt.Errorf("receive bridge_ack: %w", err)
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			_ = env.DecodePayload(&hb)
			_ = b.conn.Send(protocol.TypeHeartbeat, hb)
			continue
		}
		if env.Type != protocol.TypeBridgeAck {
			return fmt.Errorf("expected %q, got %q", protocol.TypeBridgeAck, env.Type)
		}
		var ack protocol.BridgeAck
		if err := env.DecodePayload(&ack); err != nil {
			return fmt.Errorf("decode bridge_ack: %w", err)
		}
		if ack.Error != "" {
			return fmt.Errorf("bridge ack error: %s", ack.Error)
		}
		break
	}

	// 4. Receive CallbackRelay
	var relay protocol.CallbackRelay
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		env, err := b.conn.Receive()
		if err != nil {
			return fmt.Errorf("receive callback_relay: %w", err)
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			_ = env.DecodePayload(&hb)
			_ = b.conn.Send(protocol.TypeHeartbeat, hb)
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

	b.logf("📦 Callback received from bridge, delivering to localhost:%d", port)

	// 5. Deliver callback to application on 127.0.0.1:port
	method := relay.Method
	if method == "" {
		method = http.MethodGet
	}
	path := relay.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	targetURL := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	client := &http.Client{Timeout: 10 * time.Second}

	var lastErr error
	retryDelay := 100 * time.Millisecond
	for attempt := 0; attempt < 5; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(relay.Body))
		if err != nil {
			return fmt.Errorf("create delivery request: %w", err)
		}

		for k, v := range relay.Headers {
			httpReq.Header.Set(k, v)
		}
		if host, ok := relay.Headers["Host"]; ok && host != "" {
			httpReq.Host = host
		}

		resp, err := client.Do(httpReq)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			lastErr = nil
			break
		}

		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelay):
		}
		retryDelay *= 2 // exponential backoff: 100ms, 200ms, 400ms, 800ms, 1600ms
	}

	if lastErr != nil {
		b.logf("❌ Session error: deliver callback failed: %v", lastErr)
		return fmt.Errorf("deliver callback to localhost:%d failed after retries: %w", port, lastErr)
	}

	// 6. Send BridgeComplete
	comp := protocol.BridgeComplete{
		RequestID: reqID,
		Success:   true,
	}
	if err := b.conn.Send(protocol.TypeBridgeComplete, comp); err != nil {
		b.logf("❌ Session error: send bridge_complete: %v", err)
		return fmt.Errorf("send bridge_complete: %w", err)
	}

	b.logf("✅ Session complete")
	return nil
}
