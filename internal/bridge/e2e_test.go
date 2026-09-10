package bridge_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/testutil/oauthclient"
	"github.com/bhaskarjha-dev/tantu/internal/testutil/oauthserver"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// portRewritingConn wraps transport.Conn to adapt for single-machine loopback testing.
// On loopback, Computer A and Computer B share the same localhost network namespace.
// To avoid port collision with the client app's already-running listener,
// this shim tells A-side to bind port 0 (dynamic), and captures A-side's allocated port.
type portRewritingConn struct {
	transport.Conn
	onAckSent func(port int)
}

func (c *portRewritingConn) Receive() (*protocol.Envelope, error) {
	env, err := c.Conn.Receive()
	if err != nil {
		return nil, err
	}
	if env.Type == protocol.TypeBridgeRequest {
		var req protocol.BridgeRequest
		if err := env.DecodePayload(&req); err == nil {
			// Rewrite callback port to 0 so A-side binds a dynamic free port on loopback
			req.CallbackPort = 0
			return protocol.NewEnvelope(protocol.TypeBridgeRequest, req)
		}
	}
	return env, nil
}

func (c *portRewritingConn) Send(msgType string, payload any) error {
	if msgType == protocol.TypeBridgeAck {
		if ack, ok := payload.(protocol.BridgeAck); ok {
			if c.onAckSent != nil {
				c.onAckSent(ack.ListeningPort)
			}
		}
	}
	return c.Conn.Send(msgType, payload)
}

func TestE2E_LoopbackBridge_TransparentOAuthFlow(t *testing.T) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// 1. Start synthetic OAuth provider on Computer A/Cloud
	srv := oauthserver.New()
	defer srv.Close()

	// 2. Start synthetic OAuth client app on Computer B
	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-app",
		RedirectPath:  "/callback",
		Timeout:       5 * time.Second,
	})
	defer client.Close()

	authURL, waitAndExchange, err := client.DoFlow()
	if err != nil {
		t.Fatalf("client.DoFlow failed: %v", err)
	}

	// 3. Start loopback transport listener (representing A-side bridge endpoint)
	tr := transport.NewLoopbackTransport()
	bridgeListener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("tr.Listen failed: %v", err)
	}
	defer bridgeListener.Close()

	asidePortCh := make(chan int, 1)
	asideErrCh := make(chan error, 1)
	bsideErrCh := make(chan error, 1)
	browserErrCh := make(chan error, 1)

	// 4. Goroutine: A-side bridge handler on Computer A
	go func() {
		rawConn, err := bridgeListener.Accept()
		if err != nil {
			asideErrCh <- fmt.Errorf("aside accept failed: %w", err)
			return
		}
		defer rawConn.Close()

		aConn := &portRewritingConn{
			Conn: rawConn,
			onAckSent: func(port int) {
				asidePortCh <- port
			},
		}

		asideErrCh <- bridge.HandleASide(ctx, aConn, bridge.ASideConfig{Timeout: 5 * time.Second})
	}()

	// 5. Goroutine: B-side bridge handler on Computer B
	go func() {
		bConn, err := tr.Dial(bridgeListener.Addr().String())
		if err != nil {
			bsideErrCh <- fmt.Errorf("bside dial failed: %w", err)
			return
		}
		defer bConn.Close()

		bsideErrCh <- bridge.HandleBSide(ctx, bConn, authURL, bridge.BSideConfig{Timeout: 5 * time.Second})
	}()

	// 6. Goroutine: Browser simulation on Computer A
	go func() {
		select {
		case asidePort := <-asidePortCh:
			// Browser client intercepts the redirect from oauthserver and follows it to A-side's port
			browserClient := &http.Client{
				Timeout: 4 * time.Second,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if req.URL.Hostname() == "127.0.0.1" || req.URL.Hostname() == "localhost" {
						req.URL.Host = fmt.Sprintf("127.0.0.1:%d", asidePort)
					}
					return nil
				},
			}

			resp, err := browserClient.Get(authURL)
			if err != nil {
				browserErrCh <- fmt.Errorf("browser visit failed: %w", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				browserErrCh <- fmt.Errorf("browser expected 200 OK from A-side callback, got %d: %s", resp.StatusCode, string(body))
				return
			}
			browserErrCh <- nil

		case <-ctx.Done():
			browserErrCh <- ctx.Err()
		}
	}()

	// 7. Wait for client app to receive callback and exchange code for token
	result, err := waitAndExchange()
	if err != nil {
		t.Fatalf("waitAndExchange failed: %v", err)
	}

	// 8. Verify all goroutines completed cleanly
	if browserErr := <-browserErrCh; browserErr != nil {
		t.Fatalf("browser error: %v", browserErr)
	}
	if asideErr := <-asideErrCh; asideErr != nil {
		t.Fatalf("A-side error: %v", asideErr)
	}
	if bsideErr := <-bsideErrCh; bsideErr != nil {
		t.Fatalf("B-side error: %v", bsideErr)
	}

	// 9. Verify token transparency
	if result.AccessToken == "" {
		t.Errorf("expected non-empty AccessToken")
	}
	if result.TokenType != "Bearer" {
		t.Errorf("expected TokenType 'Bearer', got %q", result.TokenType)
	}
	if result.ExpiresIn != 3600 {
		t.Errorf("expected ExpiresIn 3600, got %d", result.ExpiresIn)
	}

	elapsed := time.Since(start)
	t.Logf("E2E OAuth flow completed successfully in %v", elapsed)
	if elapsed > 10*time.Second {
		t.Errorf("expected test to complete in under 10 seconds, took %v", elapsed)
	}
}

// portCapturingConn wraps transport.Conn to capture BridgeAck.ListeningPort
// without modifying any request or response messages.
type portCapturingConn struct {
	transport.Conn
	onAckSent func(port int)
}

func (c *portCapturingConn) Send(msgType string, payload any) error {
	if msgType == protocol.TypeBridgeAck {
		if ack, ok := payload.(protocol.BridgeAck); ok {
			if c.onAckSent != nil {
				c.onAckSent(ack.ListeningPort)
			}
		}
	}
	return c.Conn.Send(msgType, payload)
}

func TestE2E_LoopbackBridge_BrowserOpen(t *testing.T) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// 1. Start synthetic OAuth provider on Computer A/Cloud
	srv := oauthserver.New()
	defer srv.Close()

	// 2. Start synthetic OAuth client app on Computer B
	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-app-p3",
		RedirectPath:  "/callback",
		Timeout:       5 * time.Second,
	})
	defer client.Close()

	authURL, waitAndExchange, err := client.DoFlow()
	if err != nil {
		t.Fatalf("client.DoFlow failed: %v", err)
	}

	appPort, err := bridge.ExtractCallbackPort(authURL)
	if err != nil {
		t.Fatalf("ExtractCallbackPort failed: %v", err)
	}

	// 3. Start loopback transport listener (representing A-side bridge endpoint)
	tr := transport.NewLoopbackTransport()
	bridgeListener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("tr.Listen failed: %v", err)
	}
	defer bridgeListener.Close()

	asidePortCh := make(chan int, 1)
	asideErrCh := make(chan error, 1)
	bsideErrCh := make(chan error, 1)
	browserErrCh := make(chan error, 1)
	openedURLCh := make(chan string, 1)

	// Mock browser opener
	openBrowserMock := func(url string) error {
		openedURLCh <- url
		return nil
	}

	// 4. Goroutine: A-side bridge handler on Computer A
	// Note: portRewritingConn rewrites CallbackPort to 0 for single-machine loopback execution.
	go func() {
		rawConn, err := bridgeListener.Accept()
		if err != nil {
			asideErrCh <- fmt.Errorf("aside accept failed: %w", err)
			return
		}
		defer rawConn.Close()

		aConn := &portRewritingConn{
			Conn: rawConn,
			onAckSent: func(port int) {
				asidePortCh <- port
			},
		}

		cfg := bridge.ASideConfig{
			Timeout:     5 * time.Second,
			OpenBrowser: openBrowserMock,
		}
		asideErrCh <- bridge.HandleASide(ctx, aConn, cfg)
	}()

	// 5. Goroutine: B-side bridge handler on Computer B
	go func() {
		bConn, err := tr.Dial(bridgeListener.Addr().String())
		if err != nil {
			bsideErrCh <- fmt.Errorf("bside dial failed: %w", err)
			return
		}
		defer bConn.Close()

		bsideErrCh <- bridge.HandleBSide(ctx, bConn, authURL, bridge.BSideConfig{Timeout: 5 * time.Second})
	}()

	// 6. Goroutine: Browser simulation on Computer A using fallback port
	go func() {
		select {
		case asidePort := <-asidePortCh:
			if asidePort == 0 {
				browserErrCh <- fmt.Errorf("expected non-zero fallback port")
				return
			}
			if asidePort == appPort {
				browserErrCh <- fmt.Errorf("expected fallback port to differ from occupied app port %d, got %d", appPort, asidePort)
				return
			}

			// Browser client intercepts the redirect from oauthserver and follows it to A-side's fallback port
			browserClient := &http.Client{
				Timeout: 4 * time.Second,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if req.URL.Hostname() == "127.0.0.1" || req.URL.Hostname() == "localhost" {
						req.URL.Host = fmt.Sprintf("127.0.0.1:%d", asidePort)
					}
					return nil
				},
			}

			resp, err := browserClient.Get(authURL)
			if err != nil {
				browserErrCh <- fmt.Errorf("browser visit failed: %w", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				browserErrCh <- fmt.Errorf("browser expected 200 OK from A-side callback, got %d: %s", resp.StatusCode, string(body))
				return
			}
			browserErrCh <- nil

		case <-ctx.Done():
			browserErrCh <- ctx.Err()
		}
	}()

	// 7. Verify OpenBrowser was invoked with the exact authURL
	select {
	case openedURL := <-openedURLCh:
		if openedURL != authURL {
			t.Errorf("OpenBrowser received %q, want %q", openedURL, authURL)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for OpenBrowser invocation")
	}

	// 8. Wait for client app to receive callback and exchange code for token
	result, err := waitAndExchange()
	if err != nil {
		t.Fatalf("waitAndExchange failed: %v", err)
	}

	// 9. Verify all goroutines completed cleanly
	if browserErr := <-browserErrCh; browserErr != nil {
		t.Fatalf("browser error: %v", browserErr)
	}
	if asideErr := <-asideErrCh; asideErr != nil {
		t.Fatalf("A-side error: %v", asideErr)
	}
	if bsideErr := <-bsideErrCh; bsideErr != nil {
		t.Fatalf("B-side error: %v", bsideErr)
	}

	// 10. Verify token
	if result.AccessToken == "" {
		t.Errorf("expected non-empty AccessToken")
	}
	if result.TokenType != "Bearer" {
		t.Errorf("expected TokenType 'Bearer', got %q", result.TokenType)
	}

	elapsed := time.Since(start)
	t.Logf("Phase 3 E2E test completed successfully in %v", elapsed)
}

func TestE2E_URLValidationRejectsInvalidScheme(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	tr := transport.NewLoopbackTransport()
	bridgeListener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("tr.Listen failed: %v", err)
	}
	defer bridgeListener.Close()

	browserOpened := false
	openBrowserMock := func(url string) error {
		browserOpened = true
		return nil
	}

	asideErrCh := make(chan error, 1)
	go func() {
		rawConn, err := bridgeListener.Accept()
		if err != nil {
			asideErrCh <- err
			return
		}
		defer rawConn.Close()

		asideErrCh <- bridge.HandleASide(ctx, rawConn, bridge.ASideConfig{
			Timeout:     2 * time.Second,
			OpenBrowser: openBrowserMock,
		})
	}()

	bConn, err := tr.Dial(bridgeListener.Addr().String())
	if err != nil {
		t.Fatalf("tr.Dial failed: %v", err)
	}
	defer bConn.Close()

	// Send BridgeRequest with dangerous file:// scheme
	req := protocol.BridgeRequest{
		URL:          "file:///etc/passwd",
		CallbackPort: 8080,
		RequestID:    "req-dangerous-scheme",
	}
	if err := bConn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("send bridge_request failed: %v", err)
	}

	// A-side should return BridgeAck with error
	env, err := bConn.Receive()
	if err != nil {
		t.Fatalf("receive ack failed: %v", err)
	}
	if env.Type != protocol.TypeBridgeAck {
		t.Fatalf("expected bridge_ack, got %s", env.Type)
	}

	var ack protocol.BridgeAck
	if err := env.DecodePayload(&ack); err != nil {
		t.Fatalf("decode ack failed: %v", err)
	}

	if ack.Error == "" {
		t.Fatal("expected BridgeAck to contain error for file:// scheme, got empty")
	}
	if !strings.Contains(ack.Error, "url scheme must be http or https") && !strings.Contains(ack.Error, "invalid url") {
		t.Errorf("expected error to mention scheme or invalid url, got: %s", ack.Error)
	}

	if browserOpened {
		t.Errorf("browser opener must NOT be invoked when URL validation fails")
	}

	asideErr := <-asideErrCh
	if asideErr == nil {
		t.Error("expected HandleASide to return error for invalid URL")
	}
}

