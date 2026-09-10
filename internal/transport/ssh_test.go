package transport

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/testutil"
)

func TestSSHTransport_BasicRoundTrip(t *testing.T) {
	server, err := testutil.NewMockSSHServer()
	if err != nil {
		t.Fatalf("NewMockSSHServer failed: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Close()

	serverDone := make(chan struct{})
	var serverErr error

	go func() {
		defer close(serverDone)
		ch, err := server.AcceptChannel()
		if err != nil {
			serverErr = fmt.Errorf("accept channel: %w", err)
			return
		}
		conn := newSSHConn(ch, nil, nil, nil)
		defer conn.Close()

		env, err := conn.Receive()
		if err != nil {
			serverErr = fmt.Errorf("server receive: %w", err)
			return
		}
		if env.Type != protocol.TypeBridgeRequest {
			serverErr = fmt.Errorf("expected bridge_request, got %s", env.Type)
			return
		}

		var req protocol.BridgeRequest
		if err := env.DecodePayload(&req); err != nil {
			serverErr = fmt.Errorf("decode request: %w", err)
			return
		}

		ack := protocol.BridgeAck{
			RequestID:     req.RequestID,
			ListeningPort: 9999,
		}
		if err := conn.Send(protocol.TypeBridgeAck, ack); err != nil {
			serverErr = fmt.Errorf("server send ack: %w", err)
			return
		}
	}()

	tr, err := NewSSHTransport(SSHTransportConfig{
		User:       "test-user",
		PrivateKey: server.ClientKey,
	})
	if err != nil {
		t.Fatalf("NewSSHTransport failed: %v", err)
	}

	clientConn, err := tr.Dial(server.Addr)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	if clientConn.LocalAddr() == nil || clientConn.RemoteAddr() == nil {
		t.Errorf("expected non-nil addresses on client conn")
	}

	req := protocol.BridgeRequest{
		URL:          "http://127.0.0.1:8080/auth",
		CallbackPort: 8080,
		RequestID:    "ssh-test-req-1",
	}
	if err := clientConn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("client send failed: %v", err)
	}

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
	if ack.RequestID != "ssh-test-req-1" || ack.ListeningPort != 9999 {
		t.Errorf("unexpected ack payload: %+v", ack)
	}

	<-serverDone
	if serverErr != nil {
		t.Fatalf("server error: %v", serverErr)
	}
}

func TestSSHTransport_ListenDialRoundTrip(t *testing.T) {
	keyPEM, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatalf("GeneratePrivateKeyPEM: %v", err)
	}

	serverTr, err := NewSSHTransport(SSHTransportConfig{
		HostKey: keyPEM,
	})
	if err != nil {
		t.Fatalf("NewSSHTransport server: %v", err)
	}

	listener, err := serverTr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	serverDone := make(chan struct{})
	var serverErr error

	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			serverErr = fmt.Errorf("accept: %w", err)
			return
		}
		defer conn.Close()

		env, err := conn.Receive()
		if err != nil {
			serverErr = fmt.Errorf("server receive: %w", err)
			return
		}
		if env.Type != protocol.TypeHeartbeat {
			serverErr = fmt.Errorf("expected heartbeat, got %s", env.Type)
			return
		}

		var hb protocol.Heartbeat
		if err := env.DecodePayload(&hb); err != nil {
			serverErr = err
			return
		}

		if err := conn.Send(protocol.TypeHeartbeat, hb); err != nil {
			serverErr = err
			return
		}
	}()

	clientTr, err := NewSSHTransport(SSHTransportConfig{
		PrivateKey: keyPEM,
	})
	if err != nil {
		t.Fatalf("NewSSHTransport client: %v", err)
	}

	clientConn, err := clientTr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer clientConn.Close()

	testHB := protocol.Heartbeat{RequestID: "req-listen-dial"}
	if err := clientConn.Send(protocol.TypeHeartbeat, testHB); err != nil {
		t.Fatalf("Send: %v", err)
	}

	env, err := clientConn.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	var respHB protocol.Heartbeat
	if err := env.DecodePayload(&respHB); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if respHB.RequestID != "req-listen-dial" {
		t.Errorf("expected request_id 'req-listen-dial', got %q", respHB.RequestID)
	}

	<-serverDone
	if serverErr != nil {
		t.Fatalf("server error: %v", serverErr)
	}
}

func TestSSHTransport_AuthFailure(t *testing.T) {
	server, err := testutil.NewMockSSHServer()
	if err != nil {
		t.Fatalf("NewMockSSHServer failed: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Close()

	badKeyPEM, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatalf("GeneratePrivateKeyPEM failed: %v", err)
	}

	tr, err := NewSSHTransport(SSHTransportConfig{
		User:       "unauthorized-user",
		PrivateKey: badKeyPEM,
	})
	if err != nil {
		t.Fatalf("NewSSHTransport failed: %v", err)
	}

	_, err = tr.Dial(server.Addr)
	if err == nil {
		t.Fatalf("expected dial error with unauthorized key, got nil")
	}
}

func TestSSHTransport_ConnectionClose(t *testing.T) {
	server, err := testutil.NewMockSSHServer()
	if err != nil {
		t.Fatalf("NewMockSSHServer failed: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Close()

	go func() {
		ch, err := server.AcceptChannel()
		if err != nil {
			return
		}
		defer ch.Close()
		_ = ch
	}()

	tr, err := NewSSHTransport(SSHTransportConfig{
		User:       "test-user",
		PrivateKey: server.ClientKey,
	})
	if err != nil {
		t.Fatalf("NewSSHTransport failed: %v", err)
	}

	conn, err := tr.Dial(server.Addr)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	if err := conn.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}

	// Idempotent close
	if err := conn.Close(); err != nil {
		t.Errorf("Second close returned error: %v", err)
	}

	// Send after close fails
	err = conn.Send(protocol.TypeHeartbeat, protocol.Heartbeat{})
	if err == nil {
		t.Errorf("expected Send on closed connection to fail, got nil")
	}

	// Receive after close fails
	_, err = conn.Receive()
	if err == nil {
		t.Errorf("expected Receive on closed connection to fail, got nil")
	}
}

func TestSSHTransport_ConcurrentConnections(t *testing.T) {
	server, err := testutil.NewMockSSHServer()
	if err != nil {
		t.Fatalf("NewMockSSHServer failed: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Close()

	const connCount = 5
	var wg sync.WaitGroup
	wg.Add(connCount)

	for i := 0; i < connCount; i++ {
		go func() {
			ch, err := server.AcceptChannel()
			if err != nil {
				return
			}
			conn := newSSHConn(ch, nil, nil, nil)
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

	tr, err := NewSSHTransport(SSHTransportConfig{
		User:       "test-user",
		PrivateKey: server.ClientKey,
	})
	if err != nil {
		t.Fatalf("NewSSHTransport: %v", err)
	}

	for i := 0; i < connCount; i++ {
		go func(idx int) {
			defer wg.Done()
			conn, err := tr.Dial(server.Addr)
			if err != nil {
				t.Errorf("dial %d failed: %v", idx, err)
				return
			}
			defer conn.Close()

			reqID := fmt.Sprintf("req-concurrent-%d-%d", idx, time.Now().UnixNano())
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
				t.Errorf("client %d mismatch: got %+v, want %s", idx, hb, reqID)
			}
		}(i)
	}

	wg.Wait()
}

func TestSSHTransport_ConfigValidation(t *testing.T) {
	// Missing keys
	_, err := NewSSHTransport(SSHTransportConfig{})
	if err == nil {
		t.Errorf("expected error with empty config, got nil")
	}

	// Invalid private key PEM
	_, err = NewSSHTransport(SSHTransportConfig{
		PrivateKey: []byte("invalid pem content"),
	})
	if err == nil {
		t.Errorf("expected error with invalid PrivateKey PEM, got nil")
	}

	// Invalid host key PEM
	_, err = NewSSHTransport(SSHTransportConfig{
		HostKey: []byte("invalid pem content"),
	})
	if err == nil {
		t.Errorf("expected error with invalid HostKey PEM, got nil")
	}
}
