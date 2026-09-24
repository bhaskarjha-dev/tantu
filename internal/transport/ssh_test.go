package transport

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func TestSSHTransport_HandshakeTimeoutIncludesBannerExchange(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	key, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewSSHTransport(SSHTransportConfig{
		PrivateKey:           key,
		AllowInsecureHostKey: true,
		HandshakeTimeout:     100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = tr.Dial(listener.Addr().String())
	if err == nil {
		t.Fatal("expected stalled SSH handshake to fail")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("SSH handshake timeout was not enforced: %v", elapsed)
	}
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(time.Second):
		t.Fatal("stall server did not accept connection")
	}
}

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
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		User:                   "test-user",
		PrivateKey:             server.ClientKey,
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
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		HostKey:                keyPEM,
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
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		PrivateKey:             keyPEM,
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
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		User:                   "unauthorized-user",
		PrivateKey:             badKeyPEM,
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
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		User:                   "test-user",
		PrivateKey:             server.ClientKey,
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
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		User:                   "test-user",
		PrivateKey:             server.ClientKey,
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

func TestSSHTransport_ListenRejectsUnauthorizedKey(t *testing.T) {
	serverKey, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	unauthorizedKey, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}

	handshakeErrors := make(chan error, 1)
	serverTr, err := NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		HostKey:                serverKey,
		HandshakeTimeout:       2 * time.Second,
		OnHandshakeError: func(err error) {
			select {
			case handshakeErrors <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := serverTr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	clientTr, err := NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		PrivateKey:             unauthorizedKey,
		HandshakeTimeout:       2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := clientTr.Dial(listener.Addr().String())
	if err == nil {
		_ = conn.Close()
		t.Fatal("SSH listener accepted an unauthorized private key")
	}

	select {
	case err := <-handshakeErrors:
		if !IsHandshakeError(err) || !errors.Is(err, ErrHandshake) {
			t.Fatalf("expected classified SSH handshake error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for rejected SSH authentication")
	}
}

func TestSSHTransport_HandshakeFailureDoesNotStopListener(t *testing.T) {
	keyPEM, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	handshakeErrors := make(chan error, 1)
	serverTr, err := NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		HostKey:                keyPEM,
		HandshakeTimeout:       2 * time.Second,
		OnHandshakeError: func(err error) {
			select {
			case handshakeErrors <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := serverTr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	bad, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bad.Write([]byte("SSH-2.0-invalid-client\r\n"))
	_ = bad.Close()

	select {
	case err := <-handshakeErrors:
		if !IsHandshakeError(err) {
			t.Fatalf("expected classified SSH handshake error, got %v", err)
		}
		var handshakeErr *HandshakeError
		if !errors.As(err, &handshakeErr) || handshakeErr.Transport != "ssh" {
			t.Fatalf("expected SSH HandshakeError, got %#v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for rejected SSH handshake")
	}

	clientTr, err := NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		PrivateKey:             keyPEM,
		HandshakeTimeout:       2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
		accepted <- err
	}()
	client, err := clientTr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("valid connection after bad SSH handshake failed: %v", err)
	}
	defer client.Close()
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("valid SSH connection was rejected after bad handshake: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSH listener stopped after a rejected handshake")
	}
}

func TestSSHTransport_StrictHandshakeErrorBehavior(t *testing.T) {
	keyPEM, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	serverTr, err := NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		HostKey:                keyPEM,
		HandshakeTimeout:       2 * time.Second,
		ReturnHandshakeError:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := serverTr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	bad, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bad.Write([]byte("not-an-ssh-banner\r\n"))
	_ = bad.Close()

	conn, err := listener.Accept()
	if err == nil {
		_ = conn.Close()
		t.Fatal("strict listener unexpectedly accepted invalid SSH handshake")
	}
	if !IsHandshakeError(err) {
		t.Fatalf("strict listener error = %v, want HandshakeError", err)
	}
}

func TestSSHTransport_HandshakeDeadline(t *testing.T) {
	keyPEM, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	handshakeErrors := make(chan error, 1)
	serverTr, err := NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		HostKey:                keyPEM,
		HandshakeTimeout:       100 * time.Millisecond,
		OnHandshakeError: func(err error) {
			select {
			case handshakeErrors <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := serverTr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	raw, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_, _ = raw.Write([]byte("SSH-2.0-partial"))

	select {
	case err := <-handshakeErrors:
		if !IsHandshakeError(err) {
			t.Fatalf("expected classified handshake error, got %v", err)
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("handshake error = %v, want timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("partial SSH handshake did not reach its deadline")
	}
}

func TestSSHTransport_MaxConcurrentHandshakes(t *testing.T) {
	keyPEM, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	handshakeErrors := make(chan error, 2)
	serverTr, err := NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:    true,
		AllowLegacyHostKeyAuth:  true,
		HostKey:                 keyPEM,
		HandshakeTimeout:        2 * time.Second,
		MaxConcurrentHandshakes: 1,
		OnHandshakeError: func(err error) {
			select {
			case handshakeErrors <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := serverTr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	first, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, _ = first.Write([]byte("SSH-2.0-first-partial"))
	time.Sleep(50 * time.Millisecond)

	second, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = second.Write([]byte("SSH-2.0-second-partial"))
	defer second.Close()

	select {
	case err := <-handshakeErrors:
		if !IsHandshakeError(err) || !strings.Contains(err.Error(), "maximum concurrent SSH handshakes") {
			t.Fatalf("expected handshake-cap error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSH listener did not enforce concurrent handshake limit")
	}
}

func TestSSHTransport_CloseInterruptsPendingHandshake(t *testing.T) {
	keyPEM, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	handshakeErrors := make(chan error, 1)
	serverTr, err := NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		HostKey:                keyPEM,
		HandshakeTimeout:       30 * time.Second,
		OnHandshakeError: func(err error) {
			select {
			case handshakeErrors <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := serverTr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	raw, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	// A partial banner keeps the SSH handshake pending.
	_, _ = raw.Write([]byte("SSH-2.0-partial"))
	time.Sleep(50 * time.Millisecond)

	if err := listener.Close(); err != nil {
		t.Fatalf("listener close failed: %v", err)
	}
	select {
	case err := <-handshakeErrors:
		if !IsHandshakeError(err) {
			t.Fatalf("expected classified handshake error after close, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing listener did not interrupt pending SSH authentication")
	}
}

func TestSSHTransport_ReadDeadline(t *testing.T) {
	keyPEM, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	serverTr, err := NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		HostKey:                keyPEM,
		HandshakeTimeout:       2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := serverTr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	clientTr, err := NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		PrivateKey:             keyPEM,
		HandshakeTimeout:       2 * time.Second,
		ReadTimeout:            75 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := clientTr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	serverConn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer serverConn.Close()

	_, err = client.Receive()
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Receive error = %v, want deadline exceeded", err)
	}
}

func TestSSHTransport_ConfigValidation(t *testing.T) {
	// Missing keys
	_, err := NewSSHTransport(SSHTransportConfig{})
	if err == nil {
		t.Errorf("expected error with empty config, got nil")
	}

	// Invalid private key PEM
	_, err = NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		PrivateKey:             []byte("invalid pem content"),
	})
	if err == nil {
		t.Errorf("expected error with invalid PrivateKey PEM, got nil")
	}

	// Invalid host key PEM
	_, err = NewSSHTransport(SSHTransportConfig{
		AllowInsecureHostKey:   true,
		AllowLegacyHostKeyAuth: true,
		HostKey:                []byte("invalid pem content"),
	})
	if err == nil {
		t.Errorf("expected error with invalid HostKey PEM, got nil")
	}
}

func TestSSHTransport_DefaultHostVerificationFailsClosed(t *testing.T) {
	server, err := testutil.NewMockSSHServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	tr, err := NewSSHTransport(SSHTransportConfig{
		PrivateKey:       server.ClientKey,
		KnownHostsFile:   filepath.Join(t.TempDir(), "missing-known-hosts"),
		HandshakeTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if conn, err := tr.Dial(server.Addr); err == nil {
		_ = conn.Close()
		t.Fatal("dial unexpectedly succeeded without a trusted host key")
	}
}

func TestSSHTransport_HostKeyFingerprintPin(t *testing.T) {
	server, err := testutil.NewMockSSHServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	host, portStr, err := net.SplitHostPort(server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(server.HostKey)
	if err != nil {
		t.Fatal(err)
	}
	pinned := ssh.FingerprintSHA256(signer.PublicKey())

	newClient := func(fp string) *SSHTransport {
		tr, err := NewSSHTransport(SSHTransportConfig{
			User:               "tantu",
			Host:               host,
			Port:               port,
			PrivateKey:         server.ClientKey,
			HostKeyFingerprint: fp,
			HandshakeTimeout:   5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}

	conn, err := newClient(pinned).Dial(server.Addr)
	if err != nil {
		t.Fatalf("dial with correct pin failed: %v", err)
	}
	_ = conn.Close()

	if _, err := newClient("SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA").Dial(server.Addr); err == nil {
		t.Fatal("dial with wrong pin succeeded, want failure")
	}
	if _, err := NewSSHTransport(SSHTransportConfig{
		PrivateKey:         server.ClientKey,
		HostKeyFingerprint: "md5:00:11:22",
	}); err == nil {
		t.Fatal("non-SHA256 fingerprint accepted, want error")
	}
}
