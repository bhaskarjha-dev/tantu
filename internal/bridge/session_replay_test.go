package bridge

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// A new login attempt (fresh state/PKCE) must open a new browser flow even
// when an earlier login for the same app completed recently. Only
// byte-identical retries may coalesce or replay. Regression test for the
// logout-then-login-again report: the second login was suppressed as a
// duplicate and falsely reported success with no callback delivered.
func TestOAuthSessionManager_DistinctLoginsAreNotCoalesced(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var opened atomic.Int32
	manager := NewOAuthSessionManagerWithWindow(2 * time.Minute)
	cfg := ASideConfig{
		Timeout:  5 * time.Second,
		Sessions: manager,
		OpenBrowser: func(string) error {
			opened.Add(1)
			return nil
		},
	}

	// acceptN wires n sequential connections to independent handlers.
	accepted := make(chan transport.Conn, 4)
	serveErr := make(chan error, 1)
	go func() {
		for i := 0; i < 4; i++ {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				serveErr <- acceptErr
				return
			}
			accepted <- conn
		}
	}()
	dialServe := func(t *testing.T) transport.Conn {
		t.Helper()
		client, err := tr.Dial(listener.Addr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		var server transport.Conn
		select {
		case server = <-accepted:
		case err := <-serveErr:
			t.Fatalf("accept: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("connection was not accepted")
		}
		go func() {
			defer server.Close()
			_ = HandleASide(ctx, server, cfg)
		}()
		return client
	}
	sendReq := func(t *testing.T, client transport.Conn, id, state string) protocol.BridgeAck {
		t.Helper()
		req := protocol.BridgeRequest{
			URL:          "https://auth.example.test/authorize?client_id=demo&state=" + state,
			CallbackPort: 0,
			RequestID:    id,
		}
		if err := client.Send(protocol.TypeBridgeRequest, req); err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
		env, err := client.Receive()
		if err != nil {
			t.Fatalf("receive %s ack: %v", id, err)
		}
		var ack protocol.BridgeAck
		if err := env.DecodePayload(&ack); err != nil {
			t.Fatalf("decode %s ack: %v", id, err)
		}
		return ack
	}
	completeFlow := func(t *testing.T, client transport.Conn, reqID string, ack protocol.BridgeAck, state string) {
		t.Helper()
		if ack.Replay || ack.ListeningPort == 0 {
			t.Fatalf("%s: expected owner ack, got %+v", reqID, ack)
		}
		callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback?code=code-%s&state=%s", ack.ListeningPort, reqID, state)
		go func() {
			resp, postErr := http.Get(callbackURL)
			if postErr == nil {
				_ = resp.Body.Close()
			}
		}()
		for {
			env, err := client.Receive()
			if err != nil {
				t.Fatalf("%s: receive relay: %v", reqID, err)
			}
			if env.Type == protocol.TypeCallbackRelay {
				break
			}
		}
		if err := client.Send(protocol.TypeBridgeComplete, protocol.BridgeComplete{RequestID: reqID, Success: true}); err != nil {
			t.Fatalf("%s: send completion: %v", reqID, err)
		}
	}

	// First login completes normally.
	first := dialServe(t)
	defer first.Close()
	firstAck := sendReq(t, first, "login-one", "state-one")
	completeFlow(t, first, "login-one", firstAck, "state-one")
	if got := opened.Load(); got != 1 {
		t.Fatalf("expected one browser launch, got %d", got)
	}

	// Second login after logout: same app, fresh state. Must own a new
	// session and open the browser again — never replay the old success.
	second := dialServe(t)
	defer second.Close()
	secondAck := sendReq(t, second, "login-two", "state-two")
	if secondAck.Replay {
		t.Fatal("distinct login was suppressed as a duplicate of the previous session")
	}
	completeFlow(t, second, "login-two", secondAck, "state-two")
	if got := opened.Load(); got != 2 {
		t.Fatalf("expected two browser launches, got %d", got)
	}

	// Concurrent distinct logins must also stay independent.
	third := dialServe(t)
	defer third.Close()
	thirdAck := sendReq(t, third, "login-three", "state-three")
	if thirdAck.Replay || thirdAck.ListeningPort == 0 {
		t.Fatalf("concurrent distinct login must own its session, got %+v", thirdAck)
	}
	fourth := dialServe(t)
	defer fourth.Close()
	fourthAck := sendReq(t, fourth, "login-four", "state-four")
	if fourthAck.Replay || fourthAck.ListeningPort == 0 {
		t.Fatalf("concurrent distinct login must own its session, got %+v", fourthAck)
	}
	completeFlow(t, third, "login-three", thirdAck, "state-three")
	completeFlow(t, fourth, "login-four", fourthAck, "state-four")
	if got := opened.Load(); got != 4 {
		t.Fatalf("expected four browser launches, got %d", got)
	}
}
