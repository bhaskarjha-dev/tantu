package bridge

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func TestASide_SuccessFlow(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	asideErrCh := make(chan error, 1)

	// A-side server goroutine
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			asideErrCh <- err
			return
		}
		defer conn.Close()

		asideErrCh <- HandleASide(ctx, conn, ASideConfig{Timeout: 3 * time.Second})
	}()

	// B-side client connection
	bConn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer bConn.Close()

	// 1. Send BridgeRequest (port 0 for dynamic allocation)
	req := protocol.BridgeRequest{
		URL:          "http://auth.example.com/oauth/authorize?client_id=app",
		CallbackPort: 0,
		RequestID:    "req-aside-1",
	}
	if err := bConn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("send bridge_request failed: %v", err)
	}

	// 2. Receive BridgeAck
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
	if ack.Error != "" {
		t.Fatalf("ack contained unexpected error: %s", ack.Error)
	}
	if ack.ListeningPort == 0 {
		t.Fatalf("expected non-zero listening port")
	}

	// 3. Simulate browser hitting callback listener on A-side in a separate goroutine
	// (HandleASide holds the HTTP response until BridgeComplete is received)
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback?code=auth-code-123&state=state-abc", ack.ListeningPort)
	respCh := make(chan *http.Response, 1)
	respErrCh := make(chan error, 1)
	go func() {
		resp, err := http.Post(callbackURL, "text/plain", strings.NewReader("sample payload"))
		if err != nil {
			respErrCh <- err
			return
		}
		respCh <- resp
	}()

	// 4. Receive CallbackRelay on B-side
	env, err = bConn.Receive()
	if err != nil {
		t.Fatalf("receive callback relay failed: %v", err)
	}
	if env.Type != protocol.TypeCallbackRelay {
		t.Fatalf("expected callback_relay, got %s", env.Type)
	}
	var relay protocol.CallbackRelay
	if err := env.DecodePayload(&relay); err != nil {
		t.Fatalf("decode callback relay failed: %v", err)
	}
	if relay.RequestID != "req-aside-1" {
		t.Errorf("expected request_id req-aside-1, got %s", relay.RequestID)
	}
	if relay.Method != "POST" {
		t.Errorf("expected POST method, got %s", relay.Method)
	}
	if !strings.Contains(relay.Path, "code=auth-code-123") {
		t.Errorf("path missing expected query params: %s", relay.Path)
	}
	if string(relay.Body) != "sample payload" {
		t.Errorf("expected body 'sample payload', got %q", string(relay.Body))
	}

	// 5. Send BridgeComplete to A-side
	comp := protocol.BridgeComplete{
		RequestID: "req-aside-1",
		Success:   true,
	}
	if err := bConn.Send(protocol.TypeBridgeComplete, comp); err != nil {
		t.Fatalf("send bridge_complete failed: %v", err)
	}

	// 6. Verify HandleASide completed cleanly
	select {
	case err := <-asideErrCh:
		if err != nil {
			t.Fatalf("HandleASide returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HandleASide did not return in time")
	}

	// 7. Verify browser received HTTP confirmation response
	select {
	case err := <-respErrCh:
		t.Fatalf("http post to callback failed: %v", err)
	case resp := <-respCh:
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK from callback handler, got %d: %s", resp.StatusCode, string(respBody))
		}
		if !strings.Contains(string(respBody), "Authentication complete") {
			t.Errorf("expected response to contain 'Authentication complete', got: %s", string(respBody))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for HTTP response")
	}
}

func TestASide_PortOccupied_ReturnsError(t *testing.T) {
	// Occupy a port first
	busyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("busy listen failed: %v", err)
	}
	defer busyListener.Close()

	occupiedPort := busyListener.Addr().(*net.TCPAddr).Port

	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	asideErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			asideErrCh <- err
			return
		}
		defer conn.Close()
		asideErrCh <- HandleASide(ctx, conn, ASideConfig{Timeout: 1 * time.Second})
	}()

	bConn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer bConn.Close()

	// Send BridgeRequest asking for occupied port
	req := protocol.BridgeRequest{
		URL:          "http://auth.example.com",
		CallbackPort: occupiedPort,
		RequestID:    "req-occupied",
	}
	if err := bConn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	// Expect BridgeAck with error and ListeningPort == 0
	env, err := bConn.Receive()
	if err != nil {
		t.Fatalf("receive ack failed: %v", err)
	}
	var ack protocol.BridgeAck
	if err := env.DecodePayload(&ack); err != nil {
		t.Fatalf("decode ack failed: %v", err)
	}
	if !strings.Contains(ack.Error, "is in use on this machine") {
		t.Fatalf("expected error containing 'is in use on this machine', got: %s", ack.Error)
	}
	if ack.ListeningPort != 0 {
		t.Fatalf("expected listening port 0 on collision, got %d", ack.ListeningPort)
	}

	// Verify HandleASide returns error
	select {
	case err := <-asideErrCh:
		if err == nil {
			t.Fatal("expected HandleASide to return error on occupied port, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HandleASide did not finish in time")
	}
}

func TestASide_Timeout(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	asideErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			asideErrCh <- err
			return
		}
		defer conn.Close()
		asideErrCh <- HandleASide(ctx, conn, ASideConfig{Timeout: 50 * time.Millisecond})
	}()

	bConn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer bConn.Close()

	req := protocol.BridgeRequest{
		URL:          "http://auth.example.com",
		CallbackPort: 0,
		RequestID:    "req-timeout",
	}
	if err := bConn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	// Read ack
	_, err = bConn.Receive()
	if err != nil {
		t.Fatalf("receive ack failed: %v", err)
	}

	// Do NOT send any HTTP callback
	err = <-asideErrCh
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error from HandleASide, got: %v", err)
	}
}

func TestASide_OpenBrowser_Success(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	openedURLCh := make(chan string, 1)
	openBrowserMock := func(targetURL string) error {
		openedURLCh <- targetURL
		return nil
	}

	asideErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			asideErrCh <- err
			return
		}
		defer conn.Close()

		asideErrCh <- HandleASide(ctx, conn, ASideConfig{
			Timeout:     3 * time.Second,
			OpenBrowser: openBrowserMock,
		})
	}()

	bConn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer bConn.Close()

	targetURL := "https://auth.example.com/oauth/authorize?client_id=my-app"
	req := protocol.BridgeRequest{
		URL:          targetURL,
		CallbackPort: 0,
		RequestID:    "req-browser-1",
	}
	if err := bConn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("send bridge_request failed: %v", err)
	}

	// Receive BridgeAck
	env, err := bConn.Receive()
	if err != nil {
		t.Fatalf("receive ack failed: %v", err)
	}
	var ack protocol.BridgeAck
	if err := env.DecodePayload(&ack); err != nil {
		t.Fatalf("decode ack failed: %v", err)
	}
	if ack.Error != "" {
		t.Fatalf("ack contained unexpected error: %s", ack.Error)
	}

	// Verify OpenBrowser was called with correct URL
	select {
	case opened := <-openedURLCh:
		if opened != targetURL {
			t.Errorf("OpenBrowser called with %q, want %q", opened, targetURL)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OpenBrowser was not called within timeout")
	}

	// Hit callback listener in goroutine so session completes without deadlock
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback?code=ok", ack.ListeningPort)
	go func() {
		resp, err := http.Get(callbackURL)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	// Read relay
	_, err = bConn.Receive()
	if err != nil {
		t.Fatalf("receive callback relay failed: %v", err)
	}

	// Send complete
	comp := protocol.BridgeComplete{
		RequestID: "req-browser-1",
		Success:   true,
	}
	if err := bConn.Send(protocol.TypeBridgeComplete, comp); err != nil {
		t.Fatalf("send bridge_complete failed: %v", err)
	}

	// Verify clean exit
	select {
	case err := <-asideErrCh:
		if err != nil {
			t.Fatalf("HandleASide returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HandleASide did not finish in time")
	}
}

func TestASide_InvalidURL_Rejected(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	browserCalled := false
	openBrowserMock := func(targetURL string) error {
		browserCalled = true
		return nil
	}

	asideErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			asideErrCh <- err
			return
		}
		defer conn.Close()

		asideErrCh <- HandleASide(ctx, conn, ASideConfig{
			Timeout:     1 * time.Second,
			OpenBrowser: openBrowserMock,
		})
	}()

	bConn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer bConn.Close()

	// Send invalid URL (javascript scheme)
	req := protocol.BridgeRequest{
		URL:          "javascript:alert(1)",
		CallbackPort: 0,
		RequestID:    "req-invalid-url",
	}
	if err := bConn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("send bridge_request failed: %v", err)
	}

	// Receive BridgeAck with error
	env, err := bConn.Receive()
	if err != nil {
		t.Fatalf("receive ack failed: %v", err)
	}
	var ack protocol.BridgeAck
	if err := env.DecodePayload(&ack); err != nil {
		t.Fatalf("decode ack failed: %v", err)
	}
	if ack.Error == "" {
		t.Errorf("expected error in ack for invalid url, got empty")
	}
	if !strings.Contains(ack.Error, "invalid url") {
		t.Errorf("expected ack.Error to contain 'invalid url', got %q", ack.Error)
	}

	if browserCalled {
		t.Errorf("OpenBrowser should NOT have been called for invalid URL")
	}

	err = <-asideErrCh
	if err == nil {
		t.Errorf("expected HandleASide to return error for invalid URL")
	}
}

