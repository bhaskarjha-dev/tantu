package transport

import (
	"crypto/tls"
	"errors"
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
		Cert:                tlsCert,
		TrustedFingerprints: trustedFPs,
	})
	if err != nil {
		t.Fatalf("NewLANTransport: %v", err)
	}
	return tr
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
