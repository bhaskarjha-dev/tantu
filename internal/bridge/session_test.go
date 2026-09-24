package bridge

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func TestRedactErrorRemovesAuthorizationQueryValues(t *testing.T) {
	err := fmt.Errorf("provider callback failed: https://auth.example.test/callback?code=one-time&state=secret")
	redacted := RedactError(err)
	if strings.Contains(redacted, "one-time") || strings.Contains(redacted, "secret") {
		t.Fatalf("bridge error retained OAuth secrets: %q", redacted)
	}
	if !strings.Contains(redacted, "auth.example.test/callback") {
		t.Fatalf("redacted error lost useful host/path: %q", redacted)
	}
}

func TestDeriveOAuthFlowID_ExcludesRetryValuesButKeepsMeaningfulParameters(t *testing.T) {
	first := "https://auth.example.test/authorize?client_id=app&scope=openid+profile&resource=api%3A%2F%2Fa&state=one&nonce=n1&code_challenge=challenge-1&redirect_uri=http%3A%2F%2F127.0.0.1%3A41001%2Fcallback"
	second := "https://auth.example.test/authorize?resource=api%3A%2F%2Fa&code_challenge=challenge-2&redirect_uri=http%3A%2F%2F127.0.0.1%3A51999%2Fcallback&scope=profile+openid&nonce=n2&state=two&client_id=app"
	if got, want := DeriveOAuthFlowID(first), DeriveOAuthFlowID(second); got != want {
		t.Fatalf("retry variants should share a flow ID: got %q, want %q", got, want)
	}

	differentResource := "https://auth.example.test/authorize?client_id=app&scope=openid+profile&resource=api%3A%2F%2Fb&state=three&redirect_uri=http%3A%2F%2F127.0.0.1%3A41001%2Fcallback"
	if DeriveOAuthFlowID(first) == DeriveOAuthFlowID(differentResource) {
		t.Fatal("different authorization resources must not share a flow ID")
	}
}

func TestOAuthSessionManager_CoalescesDuplicateBrowserRequests(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

	accepted := make(chan transport.Conn, 3)
	serveErr := make(chan error, 1)
	go func() {
		for i := 0; i < 3; i++ {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				serveErr <- acceptErr
				return
			}
			accepted <- conn
		}
	}()

	// First request owns the logical session.
	first, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("dial first: %v", err)
	}
	defer first.Close()
	var firstServer transport.Conn
	select {
	case firstServer = <-accepted:
	case err := <-serveErr:
		t.Fatalf("accept first: %v", err)
	case <-time.After(time.Second):
		t.Fatal("first connection was not accepted")
	}
	go func() {
		defer firstServer.Close()
		_ = HandleASide(ctx, firstServer, cfg)
	}()

	req := protocol.BridgeRequest{
		URL:          "https://auth.example.test/authorize?client_id=demo&state=repeat-test",
		CallbackPort: 0,
		RequestID:    "first-request",
	}
	if err := first.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("send first request: %v", err)
	}
	firstAckEnv, err := first.Receive()
	if err != nil {
		t.Fatalf("receive first ack: %v", err)
	}
	var firstAck protocol.BridgeAck
	if err := firstAckEnv.DecodePayload(&firstAck); err != nil {
		t.Fatalf("decode first ack: %v", err)
	}
	if firstAck.Replay || firstAck.ListeningPort == 0 {
		t.Fatalf("unexpected first ack: %+v", firstAck)
	}

	// A retry gets a separate transport connection but must not open a second
	// browser page or bind a second callback listener.
	second, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("dial duplicate: %v", err)
	}
	defer second.Close()
	var secondServer transport.Conn
	select {
	case secondServer = <-accepted:
	case err := <-serveErr:
		t.Fatalf("accept duplicate: %v", err)
	case <-time.After(time.Second):
		t.Fatal("duplicate connection was not accepted")
	}
	go func() {
		defer secondServer.Close()
		_ = HandleASide(ctx, secondServer, cfg)
	}()
	secondReq := req
	secondReq.RequestID = "retry-request"
	if err := second.Send(protocol.TypeBridgeRequest, secondReq); err != nil {
		t.Fatalf("send duplicate request: %v", err)
	}

	// Complete the original browser callback.
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback?code=one-time-code&state=repeat-test", firstAck.ListeningPort)
	go func() {
		resp, postErr := http.Get(callbackURL)
		if postErr == nil {
			_ = resp.Body.Close()
		}
	}()
	relayEnv, err := first.Receive()
	if err != nil {
		t.Fatalf("receive callback relay: %v", err)
	}
	if relayEnv.Type != protocol.TypeCallbackRelay {
		t.Fatalf("expected callback relay, got %q", relayEnv.Type)
	}
	if err := first.Send(protocol.TypeBridgeComplete, protocol.BridgeComplete{
		RequestID: req.RequestID,
		Success:   true,
	}); err != nil {
		t.Fatalf("send completion: %v", err)
	}

	// The duplicate receives a replay acknowledgement after the original
	// session completes, rather than a second browser launch.
	duplicateAckEnv, err := second.Receive()
	if err != nil {
		t.Fatalf("receive replay ack: %v", err)
	}
	var duplicateAck protocol.BridgeAck
	if err := duplicateAckEnv.DecodePayload(&duplicateAck); err != nil {
		t.Fatalf("decode replay ack: %v", err)
	}
	if !duplicateAck.Replay {
		t.Fatalf("expected replay acknowledgement, got %+v", duplicateAck)
	}
	if got := opened.Load(); got != 1 {
		t.Fatalf("expected exactly one browser launch, got %d", got)
	}

	// A later retry inside the replay window is also a no-op.
	third, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("dial recent retry: %v", err)
	}
	defer third.Close()
	var thirdServer transport.Conn
	select {
	case thirdServer = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("recent retry was not accepted")
	}
	go func() {
		defer thirdServer.Close()
		_ = HandleASide(ctx, thirdServer, cfg)
	}()
	thirdReq := req
	thirdReq.RequestID = "recent-retry"
	if err := third.Send(protocol.TypeBridgeRequest, thirdReq); err != nil {
		t.Fatalf("send recent retry: %v", err)
	}
	recentEnv, err := third.Receive()
	if err != nil {
		t.Fatalf("receive recent replay ack: %v", err)
	}
	var recentAck protocol.BridgeAck
	if err := recentEnv.DecodePayload(&recentAck); err != nil {
		t.Fatalf("decode recent replay ack: %v", err)
	}
	if !recentAck.Replay {
		t.Fatalf("expected recent replay acknowledgement, got %+v", recentAck)
	}
	if got := opened.Load(); got != 1 {
		t.Fatalf("browser opened again after completed retry: %d", got)
	}
}
