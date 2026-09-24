package transport

import (
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

func createTestTransport(t *testing.T, id *pairing.Identity, trustedFPs []string) *LANTransport {
	t.Helper()
	tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair: %v", err)
	}
	tr, err := NewLANTransport(LANTransportConfig{
		Cert:                 tlsCert,
		TrustedFingerprints:  trustedFPs,
		ReturnHandshakeError: true,
	})
	if err != nil {
		t.Fatalf("NewLANTransport: %v", err)
	}
	return tr
}

type fatalAcceptListener struct {
	err       error
	closeCh   chan struct{}
	closeOnce sync.Once
}

func (l *fatalAcceptListener) Accept() (net.Conn, error) { return nil, l.err }
func (l *fatalAcceptListener) Close() error {
	l.closeOnce.Do(func() { close(l.closeCh) })
	return nil
}
func (l *fatalAcceptListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}
}

func TestLANListener_FatalAcceptErrorIsReturned(t *testing.T) {
	underlying := &fatalAcceptListener{err: errors.New("synthetic accept failure"), closeCh: make(chan struct{})}
	l := &lanListener{
		listener:         underlying,
		handshakeTimeout: time.Second,
		closed:           make(chan struct{}),
		serveDone:        make(chan struct{}),
		results:          make(chan lanAcceptResult, 1),
		slots:            make(chan struct{}, 1),
		handshaking:      make(map[*tls.Conn]struct{}),
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.serve()
	}()
	_, err := l.Accept()
	if err == nil {
		t.Fatal("expected fatal listener error")
	}
	if !errors.Is(err, underlying.err) {
		t.Fatalf("expected wrapped accept error, got %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close after fatal accept: %v", err)
	}
}

func TestLANListener_CloseDrainsQueuedConnections(t *testing.T) {
	underlying := &fatalAcceptListener{err: errors.New("unused"), closeCh: make(chan struct{})}
	l := &lanListener{
		listener:         underlying,
		handshakeTimeout: time.Second,
		closed:           make(chan struct{}),
		serveDone:        make(chan struct{}),
		results:          make(chan lanAcceptResult, 1),
		slots:            make(chan struct{}, 1),
		handshaking:      make(map[*tls.Conn]struct{}),
	}
	local, remote := net.Pipe()
	queued := newTCPConn(local)
	l.results <- lanAcceptResult{conn: queued}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close = %v, want net.ErrClosed", err)
	}
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := remote.Read(make([]byte, 1)); err == nil {
		t.Fatal("queued connection was not closed during listener shutdown")
	}
	_ = remote.Close()
}

func TestLANTransport_MutualTLS_Success(t *testing.T) {
	idA, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity A: %v", err)
	}
	idB, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity B: %v", err)
	}

	trA := createTestTransport(t, idA, []string{idB.Fingerprint})
	trB := createTestTransport(t, idB, []string{idA.Fingerprint})

	l, err := trA.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("trA Listen: %v", err)
	}
	defer l.Close()

	serverDone := make(chan struct{})
	var serverErr error

	go func() {
		defer close(serverDone)
		conn, err := l.Accept()
		if err != nil {
			serverErr = err
			return
		}
		defer conn.Close()

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

		ack := protocol.BridgeAck{
			RequestID:     req.RequestID,
			ListeningPort: 9877,
		}
		if err := conn.Send(protocol.TypeBridgeAck, ack); err != nil {
			serverErr = err
			return
		}
	}()

	clientConn, err := trB.Dial(l.Addr().String())
	if err != nil {
		t.Fatalf("trB Dial: %v", err)
	}
	defer clientConn.Close()

	req := protocol.BridgeRequest{
		RequestID:    "req-mtls-1",
		URL:          "https://accounts.example.com/oauth",
		CallbackPort: 9877,
	}
	if err := clientConn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("client Send: %v", err)
	}

	env, err := clientConn.Receive()
	if err != nil {
		t.Fatalf("client Receive: %v", err)
	}
	if env.Type != protocol.TypeBridgeAck {
		t.Fatalf("expected bridge_ack, got %s", env.Type)
	}

	var ack protocol.BridgeAck
	if err := env.DecodePayload(&ack); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if ack.RequestID != "req-mtls-1" || ack.ListeningPort != 9877 {
		t.Errorf("unexpected ack: %+v", ack)
	}

	select {
	case <-serverDone:
		if serverErr != nil {
			t.Fatalf("server error: %v", serverErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server timed out")
	}
}

func TestLANTransport_PinnedDialRejectsSubstitutedPeer(t *testing.T) {
	local, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	substitute, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	localCert, err := tls.X509KeyPair(local.CertPEM, local.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	substituteCert, err := tls.X509KeyPair(substitute.CertPEM, substitute.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}

	server, err := NewLANTransport(LANTransportConfig{
		Cert:         substituteCert,
		AllowPairing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := server.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	client, err := NewLANTransport(LANTransportConfig{
		Cert:         localCert,
		AllowPairing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if conn, err := client.DialPinned(listener.Addr().String(), local.Fingerprint); err == nil {
		_ = conn.Close()
		t.Fatal("pinned dial accepted a substituted peer certificate")
	}
}

func TestLANTransport_UntrustedPeer_Rejected(t *testing.T) {
	idA, _ := pairing.GenerateIdentity()
	idB, _ := pairing.GenerateIdentity()
	idC, _ := pairing.GenerateIdentity()

	// A only trusts B
	trA := createTestTransport(t, idA, []string{idB.Fingerprint})
	// C tries to connect, trusting A
	trC := createTestTransport(t, idC, []string{idA.Fingerprint})

	l, err := trA.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("trA Listen: %v", err)
	}
	defer l.Close()

	acceptErrChan := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err == nil {
			conn.Close()
			acceptErrChan <- errors.New("expected accept to fail with untrusted peer")
		} else {
			acceptErrChan <- nil // failed as expected
		}
	}()

	conn, dialErr := trC.Dial(l.Addr().String())
	if dialErr == nil {
		// If dial succeeded initially, sending/receiving should trigger handshake failure
		_ = conn.Send(protocol.TypeBridgeRequest, protocol.BridgeRequest{})
		_, err := conn.Receive()
		if err == nil {
			t.Fatal("expected untrusted connection to fail")
		}
		_ = conn.Close()
	}

	select {
	case err := <-acceptErrChan:
		if err != nil {
			t.Fatalf("accept error check: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for accept failure")
	}
}

func TestLANTransport_EmptyTrustedFingerprints_RejectsAll(t *testing.T) {
	idA, _ := pairing.GenerateIdentity()
	idB, _ := pairing.GenerateIdentity()

	// A has empty trusted fingerprints
	trA := createTestTransport(t, idA, nil)
	trB := createTestTransport(t, idB, []string{idA.Fingerprint})

	l, err := trA.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("trA Listen: %v", err)
	}
	defer l.Close()

	acceptErrChan := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err == nil {
			conn.Close()
			acceptErrChan <- errors.New("expected accept to reject when trusted list is empty")
		} else {
			acceptErrChan <- nil // failed as expected
		}
	}()

	conn, dialErr := trB.Dial(l.Addr().String())
	if dialErr == nil {
		_, _ = conn.Receive()
		_ = conn.Close()
	}

	select {
	case err := <-acceptErrChan:
		if err != nil {
			t.Fatalf("empty trusted check: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for empty trusted reject")
	}
}

func TestLANTransport_BidirectionalProtocol(t *testing.T) {
	idA, _ := pairing.GenerateIdentity()
	idB, _ := pairing.GenerateIdentity()

	trA := createTestTransport(t, idA, []string{idB.Fingerprint})
	trB := createTestTransport(t, idB, []string{idA.Fingerprint})

	l, err := trA.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		conn, err := l.Accept()
		if err != nil {
			t.Errorf("Accept: %v", err)
			return
		}
		defer conn.Close()

		// Receive message 1 from client
		env1, err := conn.Receive()
		if err != nil || env1.Type != protocol.TypeBridgeRequest {
			t.Errorf("Receive 1: %v", err)
			return
		}

		// Send reply 1
		if err := conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{RequestID: "req-1"}); err != nil {
			t.Errorf("Send 1: %v", err)
			return
		}

		// Send message 2 from server to client
		relayMsg := protocol.CallbackRelay{
			RequestID: "req-1",
			Method:    "GET",
			Path:      "/callback?code=test_code_123&state=state_abc",
		}
		if err := conn.Send(protocol.TypeCallbackRelay, relayMsg); err != nil {
			t.Errorf("Send 2: %v", err)
			return
		}

		// Receive reply 2 from client
		env2, err := conn.Receive()
		if err != nil || env2.Type != protocol.TypeBridgeComplete {
			t.Errorf("Receive 2: %v", err)
			return
		}
	}()

	client, err := trB.Dial(l.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	// Client sends msg 1
	if err := client.Send(protocol.TypeBridgeRequest, protocol.BridgeRequest{RequestID: "req-1", URL: "https://example.com"}); err != nil {
		t.Fatalf("client send 1: %v", err)
	}

	// Client receives reply 1
	env1, err := client.Receive()
	if err != nil || env1.Type != protocol.TypeBridgeAck {
		t.Fatalf("client receive 1: %v", err)
	}

	// Client receives msg 2
	env2, err := client.Receive()
	if err != nil || env2.Type != protocol.TypeCallbackRelay {
		t.Fatalf("client receive 2: %v", err)
	}

	// Client sends reply 2
	resp := protocol.BridgeComplete{
		RequestID: "req-1",
		Success:   true,
	}
	if err := client.Send(protocol.TypeBridgeComplete, resp); err != nil {
		t.Fatalf("client send 2: %v", err)
	}

	wg.Wait()
}

func TestLANTransport_ListenerClose(t *testing.T) {
	id, _ := pairing.GenerateIdentity()
	tr := createTestTransport(t, id, []string{id.Fingerprint})

	l, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	if err := l.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from Accept after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not unblock after Close")
	}
}

func TestLANTransport_ConnClose(t *testing.T) {
	idA, _ := pairing.GenerateIdentity()
	idB, _ := pairing.GenerateIdentity()

	trA := createTestTransport(t, idA, []string{idB.Fingerprint})
	trB := createTestTransport(t, idB, []string{idA.Fingerprint})

	l, err := trA.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	go func() {
		conn, err := l.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = conn.Receive()
		}
	}()

	client, err := trB.Dial(l.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	if client.LocalAddr() == nil || client.RemoteAddr() == nil {
		t.Errorf("expected valid local and remote addrs")
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Subsequent Send/Receive should return error
	if err := client.Send(protocol.TypeBridgeRequest, protocol.BridgeRequest{}); err == nil {
		t.Error("expected error sending on closed conn")
	}
	if _, err := client.Receive(); err == nil {
		t.Error("expected error receiving on closed conn")
	}
}

func TestLANTransport_MultipleSequentialConnections(t *testing.T) {
	idA, _ := pairing.GenerateIdentity()
	idB, _ := pairing.GenerateIdentity()

	trA := createTestTransport(t, idA, []string{idB.Fingerprint})
	trB := createTestTransport(t, idB, []string{idA.Fingerprint})

	l, err := trA.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	for i := 0; i < 3; i++ {
		go func() {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{RequestID: "seq"})
		}()

		client, err := trB.Dial(l.Addr().String())
		if err != nil {
			t.Fatalf("Dial connection %d: %v", i, err)
		}
		env, err := client.Receive()
		if err != nil || env.Type != protocol.TypeBridgeAck {
			t.Fatalf("Receive connection %d: %v", i, err)
		}
		client.Close()
	}
}

func TestLANTransport_TLS13Minimum(t *testing.T) {
	id, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewLANTransport(LANTransportConfig{Cert: cert, TrustedFingerprints: []string{id.Fingerprint}})
	if err != nil {
		t.Fatal(err)
	}
	if tr.serverTLSConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("server minimum TLS version = %x, want TLS 1.3 (%x)", tr.serverTLSConfig.MinVersion, tls.VersionTLS13)
	}
	if tr.clientTLSConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("client minimum TLS version = %x, want TLS 1.3 (%x)", tr.clientTLSConfig.MinVersion, tls.VersionTLS13)
	}
}

func TestLANTransport_TrustSnapshotIgnoresCallerMutation(t *testing.T) {
	idA, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	idB, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	idC, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	certA, err := tls.X509KeyPair(idA.CertPEM, idA.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}

	trusted := []string{idB.Fingerprint}
	trA, err := NewLANTransport(LANTransportConfig{
		Cert:                 certA,
		TrustedFingerprints:  trusted,
		ReturnHandshakeError: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the caller's slice after construction must not revoke B or trust C.
	trusted[0] = idC.Fingerprint

	trB := createTestTransport(t, idB, []string{idA.Fingerprint})
	l, err := trA.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	accepted := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err == nil {
			_ = conn.Close()
		}
		accepted <- err
	}()

	client, err := trB.Dial(l.Addr().String())
	if err != nil {
		t.Fatalf("trusted B dial failed after caller slice mutation: %v", err)
	}
	defer client.Close()

	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("trusted B was rejected after caller slice mutation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for trusted connection")
	}
}

func TestLANTransport_HandshakeFailureDoesNotStopListener(t *testing.T) {
	idA, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	idB, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	certA, err := tls.X509KeyPair(idA.CertPEM, idA.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	handshakeErrors := make(chan error, 1)
	trA, err := NewLANTransport(LANTransportConfig{
		Cert:                certA,
		TrustedFingerprints: []string{idB.Fingerprint},
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
	l, err := trA.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	accepted := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err == nil {
			_ = conn.Close()
		}
		accepted <- err
	}()

	bad, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bad.Write([]byte("not a TLS ClientHello\r\n"))
	_ = bad.Close()

	select {
	case err := <-handshakeErrors:
		if !IsHandshakeError(err) || !errors.Is(err, ErrHandshake) {
			t.Fatalf("expected classified handshake error, got %v", err)
		}
		var handshakeErr *HandshakeError
		if !errors.As(err, &handshakeErr) || handshakeErr.Transport != "lan" {
			t.Fatalf("expected LAN HandshakeError, got %#v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for rejected TLS handshake")
	}

	trB := createTestTransport(t, idB, []string{idA.Fingerprint})
	client, err := trB.Dial(l.Addr().String())
	if err != nil {
		t.Fatalf("valid connection after bad handshake failed: %v", err)
	}
	defer client.Close()
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("valid connection was rejected after bad handshake: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("listener stopped after a rejected handshake")
	}
}

func TestLANTransport_HandshakeDeadline(t *testing.T) {
	id, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	handshakeErrors := make(chan error, 1)
	tr, err := NewLANTransport(LANTransportConfig{
		Cert:             cert,
		HandshakeTimeout: 100 * time.Millisecond,
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
	l, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() { _, _ = l.Accept() }()

	raw, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_, _ = raw.Write([]byte{0x16, 0x03, 0x03, 0x00})

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
		t.Fatal("partial TLS handshake did not reach its deadline")
	}
}

func TestLANTransport_CloseInterruptsHandshake(t *testing.T) {
	id, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	handshakeErrors := make(chan error, 1)
	tr, err := NewLANTransport(LANTransportConfig{
		Cert:             cert,
		HandshakeTimeout: 30 * time.Second,
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
	l, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	acceptDone := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		acceptDone <- err
	}()

	raw, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_, _ = raw.Write([]byte{0x16, 0x03, 0x03, 0x00}) // deliberately incomplete ClientHello
	time.Sleep(50 * time.Millisecond)

	if err := l.Close(); err != nil {
		t.Fatalf("listener close failed: %v", err)
	}
	select {
	case err := <-handshakeErrors:
		if !IsHandshakeError(err) {
			t.Fatalf("expected classified handshake error after close, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing listener did not interrupt in-flight TLS handshake")
	}
	select {
	case err := <-acceptDone:
		if err == nil {
			t.Fatal("Accept unexpectedly succeeded during listener close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not return after listener close")
	}
}

func TestLANTransport_PeerFingerprint(t *testing.T) {
	idA, _ := pairing.GenerateIdentity()
	idB, _ := pairing.GenerateIdentity()

	trA := createTestTransport(t, idA, []string{idB.Fingerprint})
	trB := createTestTransport(t, idB, []string{idA.Fingerprint})

	l, err := trA.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	serverDone := make(chan struct{})
	var serverSeenClientFP string

	go func() {
		defer close(serverDone)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		serverSeenClientFP = GetPeerFingerprint(conn)
		_ = conn.Send(protocol.TypeHeartbeat, protocol.Heartbeat{})
	}()

	client, err := trB.Dial(l.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	clientSeenServerFP := GetPeerFingerprint(client)
	_, _ = client.Receive()

	<-serverDone

	if serverSeenClientFP != idB.Fingerprint {
		t.Errorf("server saw client FP %s; want %s", serverSeenClientFP, idB.Fingerprint)
	}
	if clientSeenServerFP != idA.Fingerprint {
		t.Errorf("client saw server FP %s; want %s", clientSeenServerFP, idA.Fingerprint)
	}
}
