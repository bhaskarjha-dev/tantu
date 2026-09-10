package transport

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

func TestLoopbackTransport_BasicRoundTrip(t *testing.T) {
	tr := NewLoopbackTransport()

	// Listen on dynamic port
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().String()

	serverDone := make(chan struct{})
	var serverErr error

	// Server goroutine
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			serverErr = err
			return
		}
		defer conn.Close()

		// Receive BridgeRequest
		env, err := conn.Receive()
		if err != nil {
			serverErr = err
			return
		}
		if env.Type != protocol.TypeBridgeRequest {
			serverErr = errors.New("expected bridge_request")
			return
		}

		var req protocol.BridgeRequest
		if err := env.DecodePayload(&req); err != nil {
			serverErr = err
			return
		}

		// Send BridgeAck
		ack := protocol.BridgeAck{
			RequestID:     req.RequestID,
			ListeningPort: 9999,
		}
		if err := conn.Send(protocol.TypeBridgeAck, ack); err != nil {
			serverErr = err
			return
		}
	}()

	// Client dial
	clientConn, err := tr.Dial(addr)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	// Verify addrs
	if clientConn.LocalAddr() == nil || clientConn.RemoteAddr() == nil {
		t.Errorf("expected non-nil addresses on client conn")
	}

	// Send BridgeRequest
	req := protocol.BridgeRequest{
		URL:          "http://127.0.0.1:8080/auth",
		CallbackPort: 8080,
		RequestID:    "test-req-1",
	}
	if err := clientConn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("client send failed: %v", err)
	}

	// Receive BridgeAck
	env, err := clientConn.Receive()
	if err != nil {
		t.Fatalf("client receive failed: %v", err)
	}
	if env.Type != protocol.TypeBridgeAck {
		t.Fatalf("expected bridge_ack, got %q", env.Type)
	}

	var ack protocol.BridgeAck
	if err := env.DecodePayload(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.RequestID != "test-req-1" || ack.ListeningPort != 9999 {
		t.Errorf("unexpected ack payload: %+v", ack)
	}

	<-serverDone
	if serverErr != nil {
		t.Fatalf("server error: %v", serverErr)
	}
}

func TestLoopbackTransport_SecurityRejectsNonLoopback(t *testing.T) {
	tr := NewLoopbackTransport()

	// Attempt to bind to 0.0.0.0
	_, err := tr.Listen("0.0.0.0:0")
	if err == nil {
		t.Errorf("expected error binding to 0.0.0.0, got nil")
	}

	// Attempt to bind to external IP
	_, err = tr.Listen("192.168.1.100:8080")
	if err == nil {
		t.Errorf("expected error binding to 192.168.1.100, got nil")
	}

	// Attempt to dial non-loopback
	_, err = tr.Dial("192.168.1.100:8080")
	if err == nil {
		t.Errorf("expected error dialing non-loopback, got nil")
	}
}

func TestLoopbackTransport_ConnectionRefused(t *testing.T) {
	tr := NewLoopbackTransport()

	// Dial port that has no listener
	_, err := tr.Dial("127.0.0.1:59999")
	if err == nil {
		t.Errorf("expected dial error on unused port, got nil")
	}
}

func TestLoopbackTransport_ListenerClose(t *testing.T) {
	tr := NewLoopbackTransport()

	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		errCh <- err
	}()

	time.Sleep(20 * time.Millisecond)
	if err := listener.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	acceptErr := <-errCh
	if acceptErr == nil {
		t.Errorf("expected error after listener closed, got nil")
	}
}

func TestLoopbackTransport_ConcurrentConnections(t *testing.T) {
	tr := NewLoopbackTransport()

	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().String()

	const connCount = 5
	var wg sync.WaitGroup
	wg.Add(connCount)

	// Server accepts 5 connections concurrently
	for i := 0; i < connCount; i++ {
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()

			env, err := conn.Receive()
			if err != nil {
				return
			}
			var hb protocol.Heartbeat
			_ = env.DecodePayload(&hb)
			_ = conn.Send(protocol.TypeHeartbeat, hb)
		}()
	}

	// Clients dial concurrently
	for i := 0; i < connCount; i++ {
		go func(idx int) {
			defer wg.Done()
			conn, err := tr.Dial(addr)
			if err != nil {
				t.Errorf("dial %d failed: %v", idx, err)
				return
			}
			defer conn.Close()

			reqID := time.Now().Format(time.RFC3339Nano)
			if err := conn.Send(protocol.TypeHeartbeat, protocol.Heartbeat{RequestID: reqID}); err != nil {
				t.Errorf("send %d failed: %v", idx, err)
				return
			}

			env, err := conn.Receive()
			if err != nil {
				t.Errorf("receive %d failed: %v", idx, err)
				return
			}
			var hb protocol.Heartbeat
			if err := env.DecodePayload(&hb); err != nil || hb.RequestID != reqID {
				t.Errorf("client %d got mismatch heartbeat: %+v", idx, hb)
			}
		}(i)
	}

	wg.Wait()
}
