package bridge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func TestDispatcher_BridgeRequest(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	asideDoneCh := make(chan error, 1)

	cfg := DispatcherConfig{
		ASideConfig: ASideConfig{
			Timeout: 5 * time.Second,
		},
		OnASideDone: func(err error) {
			asideDoneCh <- err
		},
	}

	dispatcher := NewDispatcher(listener, cfg)
	go func() {
		_ = dispatcher.Serve(ctx)
	}()

	// B-side OAuth client
	bConn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer bConn.Close()

	// 1. Send BridgeRequest
	req := protocol.BridgeRequest{
		URL:          "http://auth.example.com/oauth/authorize?client_id=app",
		CallbackPort: 0,
		RequestID:    "req-multiplex-1",
	}
	if err := bConn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("Send bridge_request failed: %v", err)
	}

	// 2. Receive BridgeAck
	env, err := bConn.Receive()
	if err != nil {
		t.Fatalf("Receive ack failed: %v", err)
	}
	if env.Type != protocol.TypeBridgeAck {
		t.Fatalf("Expected bridge_ack, got %s", env.Type)
	}
	var ack protocol.BridgeAck
	if err := env.DecodePayload(&ack); err != nil {
		t.Fatalf("Decode ack failed: %v", err)
	}
	if ack.Error != "" {
		t.Fatalf("Ack returned error: %s", ack.Error)
	}

	// 3. Post to local callback listener
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback?code=auth-code-multiplex&state=state-1", ack.ListeningPort)
	go func() {
		resp, postErr := http.Post(callbackURL, "text/plain", strings.NewReader("callback ok"))
		if postErr == nil && resp != nil {
			_ = resp.Body.Close()
		}
	}()

	// 4. Receive CallbackRelay
	env, err = bConn.Receive()
	if err != nil {
		t.Fatalf("Receive callback relay failed: %v", err)
	}
	if env.Type != protocol.TypeCallbackRelay {
		t.Fatalf("Expected callback_relay, got %s", env.Type)
	}

	// 5. Send BridgeComplete
	if err := bConn.Send(protocol.TypeBridgeComplete, protocol.BridgeComplete{RequestID: req.RequestID, Success: true}); err != nil {
		t.Fatalf("Send bridge_complete failed: %v", err)
	}

	select {
	case err := <-asideDoneCh:
		if err != nil {
			t.Fatalf("OnASideDone returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for OnASideDone")
	}
}

func TestDispatcher_DropSend(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var buf bytes.Buffer
	dropReceivedCh := make(chan *drop.ReceiveDropResult, 1)

	cfg := DispatcherConfig{
		DropConfig: drop.ReceiveDropConfig{
			Timeout: 5 * time.Second,
		},
		DropWriter: &buf,
		OnDropReceived: func(res *drop.ReceiveDropResult) {
			dropReceivedCh <- res
		},
	}

	dispatcher := NewDispatcher(listener, cfg)
	go func() {
		_ = dispatcher.Serve(ctx)
	}()

	// Client sends text drop
	clientConn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	text := "Hello via multiplexed port 9877!"
	meta := drop.DropSend{
		DropID: "drop-multi-1",
		Kind:   drop.DropKindText,
		Name:   "snippet.txt",
		Size:   int64(len(text)),
	}

	if err := drop.SendDrop(ctx, clientConn, meta, strings.NewReader(text), drop.SendDropConfig{}); err != nil {
		t.Fatalf("SendDrop failed: %v", err)
	}

	select {
	case res := <-dropReceivedCh:
		if res == nil {
			t.Fatal("Expected non-nil drop result")
		}
		if res.Meta.DropID != meta.DropID {
			t.Errorf("Expected DropID=%s, got %s", meta.DropID, res.Meta.DropID)
		}
		if buf.String() != text {
			t.Errorf("Expected buffer %q, got %q", text, buf.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for OnDropReceived")
	}
}

func TestDispatcher_ConcurrentMultiplex(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var mu sync.Mutex
	receivedDrops := make(map[string]string)

	cfg := DispatcherConfig{
		ASideConfig: ASideConfig{
			Timeout: 5 * time.Second,
		},
		DropConfig: drop.ReceiveDropConfig{
			Timeout: 5 * time.Second,
			OnMeta: func(meta drop.DropSend) (io.Writer, error) {
				var b bytes.Buffer
				return &b, nil
			},
		},
		OnDropReceived: func(res *drop.ReceiveDropResult) {
			mu.Lock()
			defer mu.Unlock()
			receivedDrops[res.Meta.DropID] = res.Meta.Name
		},
	}

	dispatcher := NewDispatcher(listener, cfg)
	go func() {
		_ = dispatcher.Serve(ctx)
	}()

	addr := listener.Addr().String()
	var wg sync.WaitGroup

	// Run 2 concurrent QuickDrop transfers and 1 OAuth session
	wg.Add(3)

	// QuickDrop 1
	go func() {
		defer wg.Done()
		conn, dialErr := tr.Dial(addr)
		if dialErr != nil {
			t.Errorf("Dial Drop 1 failed: %v", dialErr)
			return
		}
		defer conn.Close()

		msg := "Payload One"
		meta := drop.DropSend{
			DropID: "drop-concurrent-1",
			Kind:   drop.DropKindText,
			Name:   "file1.txt",
			Size:   int64(len(msg)),
		}
		if err := drop.SendDrop(ctx, conn, meta, strings.NewReader(msg), drop.SendDropConfig{}); err != nil {
			t.Errorf("SendDrop 1 failed: %v", err)
		}
	}()

	// QuickDrop 2
	go func() {
		defer wg.Done()
		conn, dialErr := tr.Dial(addr)
		if dialErr != nil {
			t.Errorf("Dial Drop 2 failed: %v", dialErr)
			return
		}
		defer conn.Close()

		msg := "Payload Two"
		meta := drop.DropSend{
			DropID: "drop-concurrent-2",
			Kind:   drop.DropKindText,
			Name:   "file2.txt",
			Size:   int64(len(msg)),
		}
		if err := drop.SendDrop(ctx, conn, meta, strings.NewReader(msg), drop.SendDropConfig{}); err != nil {
			t.Errorf("SendDrop 2 failed: %v", err)
		}
	}()

	// OAuth session
	go func() {
		defer wg.Done()
		conn, dialErr := tr.Dial(addr)
		if dialErr != nil {
			t.Errorf("Dial OAuth failed: %v", dialErr)
			return
		}
		defer conn.Close()

		req := protocol.BridgeRequest{
			URL:          "http://auth.example.com/oauth/authorize?client_id=app2",
			CallbackPort: 0,
			RequestID:    "req-concurrent-oauth",
		}
		if err := conn.Send(protocol.TypeBridgeRequest, req); err != nil {
			t.Errorf("Send bridge_request failed: %v", err)
			return
		}

		env, err := conn.Receive()
		if err != nil || env.Type != protocol.TypeBridgeAck {
			t.Errorf("Receive ack failed: %v, env=%+v", err, env)
			return
		}
		var ack protocol.BridgeAck
		_ = env.DecodePayload(&ack)

		callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback?code=concurrent-code", ack.ListeningPort)
		resp, postErr := http.Post(callbackURL, "text/plain", strings.NewReader("ok"))
		if postErr == nil && resp != nil {
			_ = resp.Body.Close()
		}

		env, err = conn.Receive()
		if err != nil || env.Type != protocol.TypeCallbackRelay {
			t.Errorf("Receive relay failed: %v, env=%+v", err, env)
			return
		}

		_ = conn.Send(protocol.TypeBridgeComplete, protocol.BridgeComplete{RequestID: req.RequestID, Success: true})
	}()

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if _, ok := receivedDrops["drop-concurrent-1"]; !ok {
		t.Error("Missing drop-concurrent-1")
	}
	if _, ok := receivedDrops["drop-concurrent-2"]; !ok {
		t.Error("Missing drop-concurrent-2")
	}
}

func TestDispatcher_HeartbeatHandling(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var buf bytes.Buffer
	dropDoneCh := make(chan struct{}, 1)

	cfg := DispatcherConfig{
		DropWriter: &buf,
		OnDropReceived: func(res *drop.ReceiveDropResult) {
			dropDoneCh <- struct{}{}
		},
	}

	dispatcher := NewDispatcher(listener, cfg)
	go func() {
		_ = dispatcher.Serve(ctx)
	}()

	conn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// 1. Send heartbeat first
	if err := conn.Send(protocol.TypeHeartbeat, protocol.Heartbeat{RequestID: "hb-test"}); err != nil {
		t.Fatalf("Send heartbeat failed: %v", err)
	}

	// 2. Receive heartbeat echo from dispatcher
	env, err := conn.Receive()
	if err != nil {
		t.Fatalf("Receive heartbeat echo failed: %v", err)
	}
	if env.Type != protocol.TypeHeartbeat {
		t.Fatalf("Expected heartbeat echo, got %s", env.Type)
	}

	// 3. Now send DropSend
	text := "Post-heartbeat payload"
	meta := drop.DropSend{
		DropID: "drop-hb-1",
		Kind:   drop.DropKindText,
		Name:   "post_hb.txt",
		Size:   int64(len(text)),
	}
	if err := drop.SendDrop(ctx, conn, meta, strings.NewReader(text), drop.SendDropConfig{}); err != nil {
		t.Fatalf("SendDrop failed: %v", err)
	}

	select {
	case <-dropDoneCh:
		if buf.String() != text {
			t.Errorf("Expected %q, got %q", text, buf.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for drop completion")
	}
}

func TestDispatcher_ShutdownWithActiveIdleConnection(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())

	cfg := DispatcherConfig{
		HandshakeTimeout: 1 * time.Second,
	}
	dispatcher := NewDispatcher(listener, cfg)

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- dispatcher.Serve(ctx)
	}()

	// Connect an idle client that never sends anything
	conn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// Wait briefly so dispatcher accepts connection and enters handleConn
	time.Sleep(50 * time.Millisecond)

	// Now cancel context to trigger shutdown
	cancel()

	select {
	case err := <-serveDone:
		if err != nil {
			t.Errorf("expected clean serve shutdown, got error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve deadlocked on shutdown with active idle connection!")
	}
}

func TestDispatcher_UntrustedPeer_RejectsBridgeRequest(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	asideCalled := false
	cfg := DispatcherConfig{
		IsPeerTrusted: func(fp string) bool {
			return false // Reject all peers
		},
		ASideConfig: ASideConfig{
			OpenBrowser: func(string) error {
				asideCalled = true
				return nil
			},
		},
	}

	dispatcher := NewDispatcher(listener, cfg)
	go func() {
		_ = dispatcher.Serve(ctx)
	}()

	conn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	req := protocol.BridgeRequest{
		URL:       "http://attacker.example.com",
		RequestID: "req-unauth-1",
	}
	if err := conn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("Send bridge_request failed: %v", err)
	}

	resp, err := conn.Receive()
	if err != nil {
		t.Fatalf("Receive rejection ack failed: %v", err)
	}

	if resp.Type != protocol.TypeBridgeAck {
		t.Fatalf("expected TypeBridgeAck, got %q", resp.Type)
	}

	var ack protocol.BridgeAck
	if err := resp.DecodePayload(&ack); err != nil {
		t.Fatalf("decode bridge_ack failed: %v", err)
	}

	if !strings.Contains(ack.Error, "unauthorized") {
		t.Errorf("expected unauthorized error, got %q", ack.Error)
	}

	if asideCalled {
		t.Fatal("CRITICAL: HandleASide was executed for an untrusted peer!")
	}
}

func TestDispatcher_UntrustedPeer_RejectsDropSend(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := DispatcherConfig{
		IsPeerTrusted: func(fp string) bool {
			return false // Reject all peers
		},
	}

	dispatcher := NewDispatcher(listener, cfg)
	go func() {
		_ = dispatcher.Serve(ctx)
	}()

	conn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	meta := drop.DropSend{
		DropID: "drop-unauth-1",
		Kind:   drop.DropKindText,
		Name:   "malicious.txt",
		Size:   100,
	}
	if err := conn.Send(drop.TypeDropSend, meta); err != nil {
		t.Fatalf("Send drop_send failed: %v", err)
	}

	resp, err := conn.Receive()
	if err != nil {
		t.Fatalf("Receive drop_ack failed: %v", err)
	}

	if resp.Type != drop.TypeDropAck {
		t.Fatalf("expected TypeDropAck, got %q", resp.Type)
	}

	var ack drop.DropAck
	if err := resp.DecodePayload(&ack); err != nil {
		t.Fatalf("decode drop_ack failed: %v", err)
	}

	if ack.Accepted {
		t.Fatal("CRITICAL: Drop was accepted for untrusted peer!")
	}
	if !strings.Contains(ack.Error, "unauthorized") {
		t.Errorf("expected unauthorized error, got %q", ack.Error)
	}
}
