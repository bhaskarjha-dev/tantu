package bridge

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func TestExtractCallbackPort(t *testing.T) {
	tests := []struct {
		name        string
		oauthURL    string
		wantPort    int
		expectError bool
	}{
		{
			name:        "Standard explicit port",
			oauthURL:    "http://auth.example.com/oauth/authorize?client_id=myclient&redirect_uri=http%3A%2F%2F127.0.0.1%3A8085%2Fcallback",
			wantPort:    8085,
			expectError: false,
		},
		{
			name:        "Unencoded redirect URI",
			oauthURL:    "http://auth.example.com/oauth/authorize?client_id=myclient&redirect_uri=http://localhost:9000/cb",
			wantPort:    9000,
			expectError: false,
		},
		{
			name:        "Missing redirect_uri param",
			oauthURL:    "http://auth.example.com/oauth/authorize?client_id=myclient",
			wantPort:    0,
			expectError: true,
		},
		{
			name:        "Default http port",
			oauthURL:    "http://auth.example.com/oauth/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%2Fcallback",
			wantPort:    80,
			expectError: false,
		},
		{
			name:        "Default https port",
			oauthURL:    "http://auth.example.com/oauth/authorize?redirect_uri=https%3A%2F%2F127.0.0.1%2Fcallback",
			wantPort:    443,
			expectError: false,
		},
		{
			name:        "Invalid port string",
			oauthURL:    "http://auth.example.com/oauth/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3Ainvalid%2Fcallback",
			wantPort:    0,
			expectError: true,
		},
		{
			name:        "VS Code Copilot trampoline redirect with loopback port in state",
			oauthURL:    "https://github.com/login/oauth/select_account?client_id=01ab8ac9400c4e429b23&redirect_uri=https%3A%2F%2Fvscode.dev%2Fredirect&state=http%3A%2F%2F127.0.0.1%3A64210%2Fcallback%3Fnonce%3DttzgBtztwl7F0bKCv%252FWuPA%253D%253D",
			wantPort:    64210,
			expectError: false,
		},
		{
			name:        "Trampoline with localhost port in state",
			oauthURL:    "https://auth.example.com/oauth/authorize?redirect_uri=https%3A%2F%2Fexample.com%2Fredirect&state=http%3A%2F%2Flocalhost%3A54321%2Fcallback",
			wantPort:    54321,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port, err := ExtractCallbackPort(tt.oauthURL)
			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error, got port %d", port)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if port != tt.wantPort {
				t.Fatalf("expected port %d, got %d", tt.wantPort, port)
			}
		})
	}
}

func TestValidateCallbackRelay_CorrelatesPathAndState(t *testing.T) {
	oauthURL := "https://auth.example.test/authorize?client_id=app&state=expected-state&redirect_uri=http%3A%2F%2F127.0.0.1%3A8080%2Fcallback%3Ftenant%3Dacme"
	expected := callbackExpectation(oauthURL)
	if !expected.constrained || expected.path != "/callback" {
		t.Fatalf("unexpected callback expectation: %+v", expected)
	}

	valid := protocol.CallbackRelay{
		Method: http.MethodGet,
		Path:   "/callback?tenant=acme&code=one-time&state=expected-state",
	}
	if err := validateCallbackRelay(expected, valid); err != nil {
		t.Fatalf("valid relay rejected: %v", err)
	}
	wrongPath := valid
	wrongPath.Path = "/other?state=expected-state"
	if err := validateCallbackRelay(expected, wrongPath); err == nil {
		t.Fatal("relay with wrong callback path was accepted")
	}
	wrongState := valid
	wrongState.Path = "/callback?tenant=acme&code=one-time&state=other"
	if err := validateCallbackRelay(expected, wrongState); err == nil {
		t.Fatal("relay with wrong callback state was accepted")
	}

	formPost := protocol.CallbackRelay{
		Method:  http.MethodPost,
		Path:    "/callback",
		Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		Body:    []byte("code=one-time&state=expected-state"),
	}
	if err := validateCallbackRelay(expected, formPost); err != nil {
		t.Fatalf("valid form_post relay rejected: %v", err)
	}

	trampoline := "https://provider.example/authorize?client_id=app&redirect_uri=https%3A%2F%2Fprovider.example%2Fredirect&state=http%3A%2F%2F127.0.0.1%3A8080%2Fcallback%3Fnonce%3Dabc"
	trampolineExpected := callbackExpectation(trampoline)
	if !trampolineExpected.constrained || trampolineExpected.path != "/callback" {
		t.Fatalf("external trampoline expectation was not constrained: %+v", trampolineExpected)
	}
}

func TestBSide_DoesNotReplayGETCallbackAfterLostResponse(t *testing.T) {
	appListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer appListener.Close()
	appPort := appListener.Addr().(*net.TCPAddr).Port
	var attempts atomic.Int32
	appServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if hijacker, ok := w.(http.Hijacker); ok {
			conn, _, hijackErr := hijacker.Hijack()
			if hijackErr == nil {
				_ = conn.Close()
				return
			}
		}
		// A normal response is not expected, but keep the test server from
		// leaving a request hanging if hijacking is unavailable.
		w.WriteHeader(http.StatusInternalServerError)
	})}
	go func() { _ = appServer.Serve(appListener) }()
	defer appServer.Close()

	tr := transport.NewLoopbackTransport()
	bridgeListener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer bridgeListener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mockDone := make(chan error, 1)
	go func() {
		conn, acceptErr := bridgeListener.Accept()
		if acceptErr != nil {
			mockDone <- acceptErr
			return
		}
		defer conn.Close()
		env, receiveErr := conn.Receive()
		if receiveErr != nil {
			mockDone <- receiveErr
			return
		}
		var req protocol.BridgeRequest
		if decodeErr := env.DecodePayload(&req); decodeErr != nil {
			mockDone <- decodeErr
			return
		}
		if sendErr := conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{RequestID: req.RequestID, ListeningPort: appPort}); sendErr != nil {
			mockDone <- sendErr
			return
		}
		if sendErr := conn.Send(protocol.TypeCallbackRelay, protocol.CallbackRelay{
			RequestID: req.RequestID,
			Method:    http.MethodGet,
			Path:      "/callback?code=one-time&state=expected",
		}); sendErr != nil {
			mockDone <- sendErr
			return
		}
		completeEnv, completeErr := conn.Receive()
		if completeErr != nil {
			mockDone <- completeErr
			return
		}
		var complete protocol.BridgeComplete
		if decodeErr := completeEnv.DecodePayload(&complete); decodeErr != nil {
			mockDone <- decodeErr
			return
		}
		if complete.Success {
			mockDone <- fmt.Errorf("expected failed callback completion")
			return
		}
		mockDone <- nil
	}()

	bConn, err := tr.Dial(bridgeListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer bConn.Close()
	oauthURL := fmt.Sprintf("https://provider.test/authorize?client_id=app&state=expected&redirect_uri=http%%3A%%2F%%2F127.0.0.1%%3A%d%%2Fcallback", appPort)
	bsideErr := make(chan error, 1)
	go func() { bsideErr <- HandleBSide(ctx, bConn, oauthURL, BSideConfig{Timeout: 3 * time.Second}) }()

	select {
	case err := <-mockDone:
		if err != nil {
			t.Fatalf("mock A-side failed: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("mock A-side did not observe failed completion")
	}
	select {
	case err := <-bsideErr:
		if err == nil {
			t.Fatal("expected B-side delivery error")
		}
	case <-time.After(time.Second):
		t.Fatal("B-side did not return after failed delivery")
	}
	// Allow any accidental automatic retry to become observable before the
	// assertion; the implementation should have made only one request.
	time.Sleep(250 * time.Millisecond)
	if got := attempts.Load(); got != 1 {
		t.Fatalf("GET callback was replayed %d times after a lost response", got)
	}
}

func TestBSide_SuccessFlow(t *testing.T) {
	// 1. Start a local app HTTP listener on Computer B
	appListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on app port: %v", err)
	}
	defer appListener.Close()

	appPort := appListener.Addr().(*net.TCPAddr).Port

	appReceivedCh := make(chan struct {
		method  string
		path    string
		headers http.Header
		body    string
	}, 1)

	appMux := http.NewServeMux()
	appMux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		appReceivedCh <- struct {
			method  string
			path    string
			headers http.Header
			body    string
		}{
			method:  r.Method,
			path:    r.URL.RequestURI(),
			headers: r.Header,
			body:    string(body),
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	appServer := &http.Server{Handler: appMux}
	go func() {
		_ = appServer.Serve(appListener)
	}()
	defer func() {
		_ = appServer.Close()
	}()

	// 2. Start Mock A-side transport listener
	tr := transport.NewLoopbackTransport()
	bridgeListener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on bridge transport: %v", err)
	}
	defer bridgeListener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mockAsideErrCh := make(chan error, 1)

	// Mock A-side goroutine
	go func() {
		conn, err := bridgeListener.Accept()
		if err != nil {
			mockAsideErrCh <- err
			return
		}
		defer conn.Close()

		// Read BridgeRequest
		env, err := conn.Receive()
		if err != nil {
			mockAsideErrCh <- err
			return
		}
		if env.Type != protocol.TypeBridgeRequest {
			mockAsideErrCh <- fmt.Errorf("expected bridge_request, got %s", env.Type)
			return
		}
		var req protocol.BridgeRequest
		if err := env.DecodePayload(&req); err != nil {
			mockAsideErrCh <- err
			return
		}

		// Send BridgeAck
		ack := protocol.BridgeAck{
			RequestID:     req.RequestID,
			ListeningPort: 9000,
		}
		if err := conn.Send(protocol.TypeBridgeAck, ack); err != nil {
			mockAsideErrCh <- err
			return
		}

		// Simulate captured callback and send CallbackRelay. A compromised or
		// buggy peer may smuggle arbitrary headers here; only the allowlisted
		// callback headers may reach the local application.
		relay := protocol.CallbackRelay{
			RequestID: req.RequestID,
			Method:    "GET",
			Path:      "/callback?code=mock-auth-code&state=mock-state",
			Headers: map[string]string{
				"Accept":          "text/html",
				"X-Custom-Header": "custom-val",
				"Cookie":          "session=stealme",
				"Authorization":   "Bearer stealme",
			},
			Body:       []byte("callback-body"),
			StatusCode: 200,
		}
		if err := conn.Send(protocol.TypeCallbackRelay, relay); err != nil {
			mockAsideErrCh <- err
			return
		}

		// Read BridgeComplete
		env, err = conn.Receive()
		if err != nil {
			mockAsideErrCh <- err
			return
		}
		if env.Type != protocol.TypeBridgeComplete {
			mockAsideErrCh <- fmt.Errorf("expected bridge_complete, got %s", env.Type)
			return
		}
		var comp protocol.BridgeComplete
		if err := env.DecodePayload(&comp); err != nil {
			mockAsideErrCh <- err
			return
		}
		if !comp.Success {
			mockAsideErrCh <- fmt.Errorf("expected success true in bridge_complete")
			return
		}

		mockAsideErrCh <- nil
	}()

	// 3. Connect B-side to mock A-side
	bConn, err := tr.Dial(bridgeListener.Addr().String())
	if err != nil {
		t.Fatalf("dial bridge failed: %v", err)
	}
	defer bConn.Close()

	oauthURL := fmt.Sprintf("http://provider.test/oauth/authorize?client_id=app&redirect_uri=http%%3A%%2F%%2F127.0.0.1%%3A%d%%2Fcallback", appPort)

	// 4. Run HandleBSide
	if err := HandleBSide(ctx, bConn, oauthURL, BSideConfig{Timeout: 3 * time.Second}); err != nil {
		t.Fatalf("HandleBSide failed: %v", err)
	}

	// 5. Verify local app received the callback
	select {
	case received := <-appReceivedCh:
		if received.method != "GET" {
			t.Errorf("expected GET, got %s", received.method)
		}
		if !strings.Contains(received.path, "code=mock-auth-code") {
			t.Errorf("path missing query: %s", received.path)
		}
		if received.headers.Get("Accept") != "text/html" {
			t.Errorf("allowlisted header not delivered: %+v", received.headers)
		}
		for _, blocked := range []string{"X-Custom-Header", "Cookie", "Authorization"} {
			if received.headers.Get(blocked) != "" {
				t.Errorf("non-allowlisted header %q reached the local app: %+v", blocked, received.headers)
			}
		}
		if received.body != "callback-body" {
			t.Errorf("body mismatch: %s", received.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for app to receive callback")
	}

	// 6. Verify mock A-side completed cleanly
	if err := <-mockAsideErrCh; err != nil {
		t.Fatalf("mock A-side failed: %v", err)
	}
}

func TestBSide_BridgeAckError(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	bridgeListener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer bridgeListener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		conn, err := bridgeListener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		env, _ := conn.Receive()
		var req protocol.BridgeRequest
		_ = env.DecodePayload(&req)

		// Respond with error ack
		_ = conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{
			RequestID: req.RequestID,
			Error:     "port collision on Computer A",
		})
	}()

	bConn, err := tr.Dial(bridgeListener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer bConn.Close()

	oauthURL := "http://auth.test/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A8080%2Fcallback"
	err = HandleBSide(ctx, bConn, oauthURL, BSideConfig{Timeout: 2 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "port collision on Computer A") {
		t.Fatalf("expected error containing 'port collision on Computer A', got: %v", err)
	}
}

func TestBSide_PortMismatch_DeliversToOriginalAppPort(t *testing.T) {
	// 1. Local app HTTP listener on Computer B
	appListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on app port: %v", err)
	}
	defer appListener.Close()

	appPort := appListener.Addr().(*net.TCPAddr).Port

	appReceivedCh := make(chan string, 1)
	appMux := http.NewServeMux()
	appMux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		appReceivedCh <- r.URL.Query().Get("code")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	appServer := &http.Server{Handler: appMux}
	go func() {
		_ = appServer.Serve(appListener)
	}()
	defer func() {
		_ = appServer.Close()
	}()

	// 2. Mock A-side transport listener
	tr := transport.NewLoopbackTransport()
	bridgeListener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on bridge transport: %v", err)
	}
	defer bridgeListener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mockAsideErrCh := make(chan error, 1)

	// Mock A-side reporting a DIFFERENT listening port (e.g. 54321 due to fallback on A)
	go func() {
		conn, err := bridgeListener.Accept()
		if err != nil {
			mockAsideErrCh <- err
			return
		}
		defer conn.Close()

		env, err := conn.Receive()
		if err != nil {
			mockAsideErrCh <- err
			return
		}
		var req protocol.BridgeRequest
		if err := env.DecodePayload(&req); err != nil {
			mockAsideErrCh <- err
			return
		}

		// A-side reports fallback port 54321, different from req.CallbackPort (appPort)
		fallbackPortOnA := 54321
		if fallbackPortOnA == req.CallbackPort {
			fallbackPortOnA = 54322
		}
		ack := protocol.BridgeAck{
			RequestID:     req.RequestID,
			ListeningPort: fallbackPortOnA,
		}
		if err := conn.Send(protocol.TypeBridgeAck, ack); err != nil {
			mockAsideErrCh <- err
			return
		}

		// Send CallbackRelay
		relay := protocol.CallbackRelay{
			RequestID:  req.RequestID,
			Method:     "GET",
			Path:       "/callback?code=port-mismatch-success",
			Headers:    map[string]string{},
			Body:       nil,
			StatusCode: 200,
		}
		if err := conn.Send(protocol.TypeCallbackRelay, relay); err != nil {
			mockAsideErrCh <- err
			return
		}

		// Read BridgeComplete
		env, err = conn.Receive()
		if err != nil {
			mockAsideErrCh <- err
			return
		}
		if env.Type != protocol.TypeBridgeComplete {
			mockAsideErrCh <- fmt.Errorf("expected bridge_complete, got %s", env.Type)
			return
		}

		mockAsideErrCh <- nil
	}()

	// 3. Connect B-side to mock A-side
	bConn, err := tr.Dial(bridgeListener.Addr().String())
	if err != nil {
		t.Fatalf("dial bridge failed: %v", err)
	}
	defer bConn.Close()

	oauthURL := fmt.Sprintf("http://provider.test/oauth/authorize?redirect_uri=http%%3A%%2F%%2F127.0.0.1%%3A%d%%2Fcallback", appPort)

	// 4. Run HandleBSide
	if err := HandleBSide(ctx, bConn, oauthURL, BSideConfig{Timeout: 3 * time.Second}); err != nil {
		t.Fatalf("HandleBSide failed: %v", err)
	}

	// 5. Verify local app received callback on original appPort
	select {
	case code := <-appReceivedCh:
		if code != "port-mismatch-success" {
			t.Errorf("expected code 'port-mismatch-success', got %q", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for local app to receive callback on appPort")
	}

	if err := <-mockAsideErrCh; err != nil {
		t.Fatalf("mock A-side failed: %v", err)
	}
}
