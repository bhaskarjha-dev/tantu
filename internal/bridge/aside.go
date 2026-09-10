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
	"sync"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// ASideConfig configures the A-side handler.
type ASideConfig struct {
	Timeout     time.Duration      // Max time to wait for callback (default: 5 min)
	OpenBrowser func(string) error // If nil, browser opening is skipped
	Logger      *log.Logger        // Optional logger for session-scoped output (default: log.Default())
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

// Run executes the A-side session lifecycle.
func (a *ASide) Run(ctx context.Context) error {
	// 1. Read BridgeRequest from connection
	var req protocol.BridgeRequest
	for {
		env, err := a.conn.Receive()
		if err != nil {
			return fmt.Errorf("receive bridge_request: %w", err)
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			_ = env.DecodePayload(&hb)
			_ = a.conn.Send(protocol.TypeHeartbeat, hb)
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

	a.logf("🔗 Session started: handling request %s (callback port %d)", req.RequestID, req.CallbackPort)

	// 2. Validate URL
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

	// 3. Bind HTTP listener on 127.0.0.1:req.CallbackPort (exact port, no fallback)
	bindAddr := fmt.Sprintf("127.0.0.1:%d", req.CallbackPort)
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

	actualPort := listener.Addr().(*net.TCPAddr).Port

	// Send BridgeAck to B-side
	ack := protocol.BridgeAck{
		RequestID:     req.RequestID,
		ListeningPort: actualPort,
	}
	if err := a.conn.Send(protocol.TypeBridgeAck, ack); err != nil {
		return fmt.Errorf("send bridge_ack: %w", err)
	}

	a.logf("Callback listener active on 127.0.0.1:%d", actualPort)

	// Open browser if configured
	if a.cfg.OpenBrowser != nil {
		a.logf("🌐 Opening browser for URL: %s", req.URL)
		if err := a.cfg.OpenBrowser(req.URL); err != nil {
			a.logf("❌ failed to open browser: %v", err)
		}
	}

	// 4. Set up HTTP callback server
	callbackCh := make(chan *protocol.CallbackRelay, 1)
	type completionResult struct {
		success bool
	}
	completionCh := make(chan completionResult, 1)
	var once sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.logf("📥 Callback received: %s %s", r.Method, r.URL.RequestURI())
		once.Do(func() {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
			headers := make(map[string]string)
			for k, v := range r.Header {
				if len(v) > 0 {
					headers[k] = v[0]
				}
			}
			callbackCh <- &protocol.CallbackRelay{
				RequestID:  req.RequestID,
				Method:     r.Method,
				Path:       r.URL.RequestURI(),
				Headers:    headers,
				Body:       body,
				StatusCode: http.StatusOK,
			}
		})
		// Hold HTTP response until BridgeComplete confirms delivery or timeout
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
		Handler: mux,
	}

	go func() {
		_ = srv.Serve(listener)
	}()
	defer func() {
		ctxShutdown, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctxShutdown)
	}()

	// 4. Wait for HTTP callback or timeout
	timer := time.NewTimer(a.cfg.Timeout)
	defer timer.Stop()

	var relay *protocol.CallbackRelay
	select {
	case <-ctx.Done():
		a.logf("❌ Session error: %v", ctx.Err())
		return ctx.Err()
	case <-timer.C:
		a.logf("❌ Session error: timeout waiting for authentication callback")
		return errors.New("timeout waiting for authentication callback")
	case relay = <-callbackCh:
	}

	// 5. Send CallbackRelay over bridge connection
	if err := a.conn.Send(protocol.TypeCallbackRelay, relay); err != nil {
		a.logf("❌ Session error: %v", err)
		return fmt.Errorf("send callback_relay: %w", err)
	}

	a.logf("📦 Callback relayed to B-side, awaiting completion")

	// 6. Wait for BridgeComplete from B-side
	completeCh := make(chan error, 1)
	go func() {
		for {
			env, err := a.conn.Receive()
			if err != nil {
				completeCh <- err
				return
			}
			if env.Type == protocol.TypeHeartbeat {
				var hb protocol.Heartbeat
				_ = env.DecodePayload(&hb)
				_ = a.conn.Send(protocol.TypeHeartbeat, hb)
				continue
			}
			if env.Type == protocol.TypeBridgeComplete {
				var comp protocol.BridgeComplete
				if err := env.DecodePayload(&comp); err != nil {
					completeCh <- fmt.Errorf("decode bridge_complete: %w", err)
					return
				}
				completeCh <- nil
				return
			}
			completeCh <- fmt.Errorf("unexpected message type: %s", env.Type)
			return
		}
	}()

	select {
	case <-ctx.Done():
		a.logf("❌ Session error: %v", ctx.Err())
		select {
		case completionCh <- completionResult{success: false}:
		default:
		}
		return ctx.Err()
	case err := <-completeCh:
		if err != nil {
			a.logf("❌ Session error: %v", err)
			select {
			case completionCh <- completionResult{success: false}:
			default:
			}
			return err
		}
		a.logf("✅ Session completed successfully")
		select {
		case completionCh <- completionResult{success: true}:
		default:
		}
		return nil
	}
}
