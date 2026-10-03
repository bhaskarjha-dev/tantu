package pairing

import (
	"bytes"
	"crypto/tls"
	"net"
	"testing"
	"time"
)

// The pairing flow's core security claim: a claimed certificate only binds
// to a connection whose TLS handshake actually authenticated that
// certificate, and a claimed transport fingerprint only binds when it
// matches the authenticated transport. An attacker who substitutes their
// own certificate inside pair_hello must be rejected here — the human SAS
// comparison is the second line of defence, not the first.

func newTLSFixture(t *testing.T) (conn *tls.Conn, serverID, otherID *Identity) {
	t.Helper()
	serverID, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("generate server identity: %v", err)
	}
	otherID, err = GenerateIdentity()
	if err != nil {
		t.Fatalf("generate other identity: %v", err)
	}
	serverCert, err := tls.X509KeyPair(serverID.CertPEM, serverID.KeyPEM)
	if err != nil {
		t.Fatalf("server key pair: %v", err)
	}
	otherCert, err := tls.X509KeyPair(otherID.CertPEM, otherID.KeyPEM)
	if err != nil {
		t.Fatalf("other key pair: %v", err)
	}

	rawClient, rawServer := net.Pipe()
	deadline := time.Now().Add(10 * time.Second)
	_ = rawClient.SetDeadline(deadline)
	_ = rawServer.SetDeadline(deadline)

	serverTLS := tls.Server(rawServer, &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequestClientCert,
		MinVersion:   tls.VersionTLS13,
	})
	clientTLS := tls.Client(rawClient, &tls.Config{
		Certificates:       []tls.Certificate{otherCert},
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- serverTLS.Handshake() }()
	if err := clientTLS.Handshake(); err != nil {
		t.Fatalf("client TLS handshake: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server TLS handshake: %v", err)
	}
	// Release the raw pipes directly: tls.Conn.Close writes close_notify
	// into an unbuffered net.Pipe whose peer is closing too, which blocks
	// until the stdlib's 5-second close timeout.
	t.Cleanup(func() {
		_ = rawClient.Close()
		_ = rawServer.Close()
	})
	return clientTLS, serverID, otherID
}

func TestVerifyTLSClaimBindsClaimToAuthenticatedLeaf(t *testing.T) {
	conn, serverID, otherID := newTLSFixture(t)

	// Honest claim: the certificate presented by the authenticated peer.
	if err := verifyTLSClaim(conn, serverID.CertPEM); err != nil {
		t.Fatalf("honest certificate claim rejected: %v", err)
	}

	// Substituted claim: a different identity's certificate must not bind to
	// this connection — this is the MITM case the SAS is meant to catch.
	if err := verifyTLSClaim(conn, otherID.CertPEM); err == nil {
		t.Fatal("substituted certificate claim accepted, want rejection")
	}

	// Garbage claims are rejected the same way.
	if err := verifyTLSClaim(conn, []byte("not a certificate")); err == nil {
		t.Fatal("malformed certificate claim accepted, want rejection")
	}
}

type stubPairConn struct {
	Conn
	fingerprint string
}

func (c *stubPairConn) PeerFingerprint() string { return c.fingerprint }

type stubPlainConn struct {
	Conn
}

func TestVerifyPairTransportIdentityBindsClaimToTransport(t *testing.T) {
	const transportFP = "AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899"

	// Matching claim (case-insensitive, as fingerprints are hex digests).
	if err := verifyPairTransportIdentity(&stubPairConn{fingerprint: transportFP}, "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"); err != nil {
		t.Fatalf("matching transport fingerprint rejected: %v", err)
	}

	// Substituted claim: the transport authenticated a different peer.
	if err := verifyPairTransportIdentity(&stubPairConn{fingerprint: transportFP}, "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00"); err == nil {
		t.Fatal("substituted transport fingerprint accepted, want rejection")
	}

	// An empty transport fingerprint means the transport carries no TLS
	// identity (loopback). The binding is intentionally conditional on the
	// transport providing one; the loopback transport is 127.0.0.1-only and
	// authenticated by capability token instead. See handshake.go options.
	if err := verifyPairTransportIdentity(&stubPairConn{fingerprint: ""}, transportFP); err != nil {
		t.Fatalf("empty transport fingerprint should skip binding, got %v", err)
	}

	// A connection that exposes no peer identity at all takes the same path.
	if err := verifyPairTransportIdentity(&stubPlainConn{}, transportFP); err != nil {
		t.Fatalf("identity-less connection should skip binding, got %v", err)
	}
}

func TestInvokeConfirmWithoutPromptFailsClosed(t *testing.T) {
	// No confirm function means no human said yes — never a stored peer.
	if invokeConfirm(nil, "peer-sas", "local-sas") {
		t.Fatal("nil confirm function reported acceptance")
	}

	// The prompt receives both codes and its verdict is returned unchanged.
	var gotPeer, gotLocal string
	accepted := invokeConfirm(func(peerSAS, localSAS string) bool {
		gotPeer, gotLocal = peerSAS, localSAS
		return true
	}, "peer-sas", "local-sas")
	if !accepted || gotPeer != "peer-sas" || gotLocal != "local-sas" {
		t.Fatalf("confirm verdict = %v (peer=%q local=%q)", accepted, gotPeer, gotLocal)
	}
}

func TestPairNoncesAreFreshPerAttempt(t *testing.T) {
	first, err := newPairNonce()
	if err != nil {
		t.Fatalf("first nonce: %v", err)
	}
	second, err := newPairNonce()
	if err != nil {
		t.Fatalf("second nonce: %v", err)
	}
	if len(first) != pairNonceBytes || len(second) != pairNonceBytes {
		t.Fatalf("nonce lengths = %d, %d; want %d", len(first), len(second), pairNonceBytes)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two pairing attempts produced identical transcript nonces")
	}
}
