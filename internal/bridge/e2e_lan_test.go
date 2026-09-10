package bridge_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/testutil/oauthclient"
	"github.com/bhaskarjha-dev/tantu/internal/testutil/oauthserver"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// lanProtocolTracerConn wraps transport.Conn to:
// 1. Rewrite CallbackPort to 0 on loopback to avoid port collision with the client app.
// 2. Notify when BridgeAck is sent with the allocated listening port.
// 3. Record all sent and received message types to verify the protocol sequence.
type lanProtocolTracerConn struct {
	transport.Conn
	onAckSent func(port int)
	traceMu   sync.Mutex
	trace     []string
}

func (c *lanProtocolTracerConn) Receive() (*protocol.Envelope, error) {
	env, err := c.Conn.Receive()
	if err != nil {
		return nil, err
	}
	c.traceMu.Lock()
	c.trace = append(c.trace, "recv:"+env.Type)
	c.traceMu.Unlock()

	if env.Type == protocol.TypeBridgeRequest {
		var req protocol.BridgeRequest
		if err := env.DecodePayload(&req); err == nil {
			req.CallbackPort = 0
			return protocol.NewEnvelope(protocol.TypeBridgeRequest, req)
		}
	}
	return env, nil
}

func (c *lanProtocolTracerConn) Send(msgType string, payload any) error {
	c.traceMu.Lock()
	c.trace = append(c.trace, "send:"+msgType)
	c.traceMu.Unlock()

	if msgType == protocol.TypeBridgeAck {
		if ack, ok := payload.(protocol.BridgeAck); ok {
			if c.onAckSent != nil {
				c.onAckSent(ack.ListeningPort)
			}
		}
	}
	return c.Conn.Send(msgType, payload)
}

func (c *lanProtocolTracerConn) getTrace() []string {
	c.traceMu.Lock()
	defer c.traceMu.Unlock()
	res := make([]string, len(c.trace))
	copy(res, c.trace)
	return res
}

func createTestLANTransport(t *testing.T, id *pairing.Identity, trustedFPs []string) *transport.LANTransport {
	t.Helper()
	tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair failed: %v", err)
	}
	tr, err := transport.NewLANTransport(transport.LANTransportConfig{
		Cert:                tlsCert,
		TrustedFingerprints: trustedFPs,
	})
	if err != nil {
		t.Fatalf("NewLANTransport failed: %v", err)
	}
	return tr
}

// TestE2E_LANBridge_TransparentOAuthFlow tests the complete OAuth flow
// over mutual-TLS LANTransport with verified protocol sequence.
func TestE2E_LANBridge_TransparentOAuthFlow(t *testing.T) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// 1. Generate identities for A-side and B-side
	idA, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity A: %v", err)
	}
	idB, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity B: %v", err)
	}

	// 2. Setup PeerStores and establish mutual trust
	storeA, err := pairing.NewPeerStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerStore A: %v", err)
	}
	if err := storeA.SaveIdentity(idA); err != nil {
		t.Fatalf("storeA.SaveIdentity: %v", err)
	}
	if err := storeA.AddPeer(pairing.Peer{
		Fingerprint: idB.Fingerprint,
		Name:        "machine-b",
		Address:     "127.0.0.1",
		CertPEM:     idB.CertPEM,
	}); err != nil {
		t.Fatalf("storeA.AddPeer: %v", err)
	}

	storeB, err := pairing.NewPeerStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerStore B: %v", err)
	}
	if err := storeB.SaveIdentity(idB); err != nil {
		t.Fatalf("storeB.SaveIdentity: %v", err)
	}
	if err := storeB.AddPeer(pairing.Peer{
		Fingerprint: idA.Fingerprint,
		Name:        "machine-a",
		Address:     "127.0.0.1",
		CertPEM:     idA.CertPEM,
	}); err != nil {
		t.Fatalf("storeB.AddPeer: %v", err)
	}

	// 3. Start synthetic OAuth provider
	srv := oauthserver.New()
	defer srv.Close()

	// 4. Start synthetic OAuth client app
	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-lan-app",
		RedirectPath:  "/callback",
		Timeout:       6 * time.Second,
	})
	defer client.Close()

	authURL, waitAndExchange, err := client.DoFlow()
	if err != nil {
		t.Fatalf("client.DoFlow failed: %v", err)
	}

	// 5. Configure LAN transports using identities and trusted peer fingerprints
	trA := createTestLANTransport(t, idA, []string{idB.Fingerprint})
	trB := createTestLANTransport(t, idB, []string{idA.Fingerprint})

	// 6. A-side listens on dynamic port
	bridgeListener, err := trA.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("trA.Listen failed: %v", err)
	}
	defer bridgeListener.Close()

	asidePortCh := make(chan int, 1)
	asideErrCh := make(chan error, 1)
	bsideErrCh := make(chan error, 1)
	browserErrCh := make(chan error, 1)

	var aConnTracer *lanProtocolTracerConn

	// 7. Goroutine: A-side bridge handler
	go func() {
		rawConn, err := bridgeListener.Accept()
		if err != nil {
			asideErrCh <- fmt.Errorf("aside accept failed: %w", err)
			return
		}
		defer rawConn.Close()

		aConnTracer = &lanProtocolTracerConn{
			Conn: rawConn,
			onAckSent: func(port int) {
				asidePortCh <- port
			},
		}

		asideErrCh <- bridge.HandleASide(ctx, aConnTracer, bridge.ASideConfig{Timeout: 5 * time.Second})
	}()

	// 8. Goroutine: B-side bridge handler
	go func() {
		bConn, err := trB.Dial(bridgeListener.Addr().String())
		if err != nil {
			bsideErrCh <- fmt.Errorf("bside dial failed: %w", err)
			return
		}
		defer bConn.Close()

		bsideErrCh <- bridge.HandleBSide(ctx, bConn, authURL, bridge.BSideConfig{Timeout: 5 * time.Second})
	}()

	// 9. Goroutine: Browser simulation
	go func() {
		select {
		case asidePort := <-asidePortCh:
			browserClient := &http.Client{
				Timeout: 5 * time.Second,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if req.URL.Hostname() == "127.0.0.1" || req.URL.Hostname() == "localhost" {
						req.URL.Host = fmt.Sprintf("127.0.0.1:%d", asidePort)
					}
					return nil
				},
			}

			resp, err := browserClient.Get(authURL)
			if err != nil {
				browserErrCh <- fmt.Errorf("browser visit failed: %w", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				browserErrCh <- fmt.Errorf("browser expected 200 OK from A-side callback, got %d: %s", resp.StatusCode, string(body))
				return
			}
			browserErrCh <- nil
		case <-ctx.Done():
			browserErrCh <- ctx.Err()
		}
	}()

	// 10. Wait for client app callback and code exchange
	result, err := waitAndExchange()
	if err != nil {
		t.Fatalf("waitAndExchange failed: %v", err)
	}

	// 11. Verify all components completed successfully
	if browserErr := <-browserErrCh; browserErr != nil {
		t.Fatalf("browser error: %v", browserErr)
	}
	if asideErr := <-asideErrCh; asideErr != nil {
		t.Fatalf("A-side error: %v", asideErr)
	}
	if bsideErr := <-bsideErrCh; bsideErr != nil {
		t.Fatalf("B-side error: %v", bsideErr)
	}

	// 12. Verify token details
	if result.AccessToken == "" {
		t.Errorf("expected non-empty AccessToken")
	}
	if result.TokenType != "Bearer" {
		t.Errorf("expected TokenType 'Bearer', got %q", result.TokenType)
	}
	if result.ExpiresIn != 3600 {
		t.Errorf("expected ExpiresIn 3600, got %d", result.ExpiresIn)
	}

	// 13. Verify full protocol sequence: BridgeRequest -> BridgeAck -> CallbackRelay -> BridgeComplete
	if aConnTracer != nil {
		trace := aConnTracer.getTrace()
		expectedSeq := []string{
			"recv:" + protocol.TypeBridgeRequest,
			"send:" + protocol.TypeBridgeAck,
			"send:" + protocol.TypeCallbackRelay,
			"recv:" + protocol.TypeBridgeComplete,
		}
		if !reflect.DeepEqual(trace, expectedSeq) {
			t.Errorf("unexpected protocol trace on A-side:\n  got:  %v\n  want: %v", trace, expectedSeq)
		}
	} else {
		t.Error("aConnTracer was nil, could not verify protocol trace")
	}

	elapsed := time.Since(start)
	t.Logf("E2E LAN bridge flow completed in %v", elapsed)
	if elapsed > 10*time.Second {
		t.Errorf("expected test to complete in under 10 seconds, took %v", elapsed)
	}
}

// TestE2E_LANBridge_ProgrammaticPairingAndOAuthFlow tests the complete lifecycle:
// 1. Programmatic pairing between two fresh stores (auto-confirm)
// 2. Establishing mTLS LANTransport from the paired stores
// 3. Full OAuth flow across the paired peers
func TestE2E_LANBridge_ProgrammaticPairingAndOAuthFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// 1. Initialize two fresh PeerStores in isolated directories
	storeA, err := pairing.NewPeerStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerStore A: %v", err)
	}
	storeB, err := pairing.NewPeerStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerStore B: %v", err)
	}

	// 2. Perform pairing handshake using dynamic TCP listener
	pairListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pair net.Listen: %v", err)
	}
	defer pairListener.Close()
	pairAddr := pairListener.Addr().String()

	var (
		initResult *pairing.PairResult
		respResult *pairing.PairResult
		initErr    error
		respErr    error
		pairWg     sync.WaitGroup
	)

	pairWg.Add(2)
	go func() {
		defer pairWg.Done()
		initResult, initErr = pairing.PairInitiatorWithListener(storeA, pairListener, func(peerSAS, localSAS string) bool {
			return true // auto-confirm
		})
	}()

	go func() {
		defer pairWg.Done()
		respResult, respErr = pairing.PairResponder(storeB, pairAddr, func(peerSAS, localSAS string) bool {
			return true // auto-confirm
		})
	}()

	pairWg.Wait()

	if initErr != nil {
		t.Fatalf("pairing initiator error: %v", initErr)
	}
	if respErr != nil {
		t.Fatalf("pairing responder error: %v", respErr)
	}
	if !initResult.Accepted || !respResult.Accepted {
		t.Fatalf("pairing was not mutually accepted: init=%v, resp=%v", initResult.Accepted, respResult.Accepted)
	}

	// 3. Load identities and peers saved during pairing
	idA, err := storeA.LoadIdentity()
	if err != nil || idA == nil {
		t.Fatalf("storeA missing identity: %v", err)
	}
	idB, err := storeB.LoadIdentity()
	if err != nil || idB == nil {
		t.Fatalf("storeB missing identity: %v", err)
	}

	if !storeA.IsTrusted(idB.Fingerprint) {
		t.Fatalf("storeA does not trust peer B fingerprint: %s", idB.Fingerprint)
	}
	if !storeB.IsTrusted(idA.Fingerprint) {
		t.Fatalf("storeB does not trust peer A fingerprint: %s", idA.Fingerprint)
	}

	// 4. Start synthetic OAuth provider
	srv := oauthserver.New()
	defer srv.Close()

	// 5. Start synthetic OAuth client app
	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-paired-lan-app",
		RedirectPath:  "/callback",
		Timeout:       6 * time.Second,
	})
	defer client.Close()

	authURL, waitAndExchange, err := client.DoFlow()
	if err != nil {
		t.Fatalf("client.DoFlow failed: %v", err)
	}

	// 6. Build LANTransports from trusted stores
	var trustedA []string
	for _, p := range storeA.ListPeers() {
		trustedA = append(trustedA, p.Fingerprint)
	}
	var trustedB []string
	for _, p := range storeB.ListPeers() {
		trustedB = append(trustedB, p.Fingerprint)
	}

	trA := createTestLANTransport(t, idA, trustedA)
	trB := createTestLANTransport(t, idB, trustedB)

	// 7. A-side bridge listener
	bridgeListener, err := trA.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("trA.Listen failed: %v", err)
	}
	defer bridgeListener.Close()

	asidePortCh := make(chan int, 1)
	asideErrCh := make(chan error, 1)
	bsideErrCh := make(chan error, 1)
	browserErrCh := make(chan error, 1)

	go func() {
		rawConn, err := bridgeListener.Accept()
		if err != nil {
			asideErrCh <- fmt.Errorf("aside accept failed: %w", err)
			return
		}
		defer rawConn.Close()

		aConn := &lanProtocolTracerConn{
			Conn: rawConn,
			onAckSent: func(port int) {
				asidePortCh <- port
			},
		}

		asideErrCh <- bridge.HandleASide(ctx, aConn, bridge.ASideConfig{Timeout: 5 * time.Second})
	}()

	go func() {
		bConn, err := trB.Dial(bridgeListener.Addr().String())
		if err != nil {
			bsideErrCh <- fmt.Errorf("bside dial failed: %w", err)
			return
		}
		defer bConn.Close()

		bsideErrCh <- bridge.HandleBSide(ctx, bConn, authURL, bridge.BSideConfig{Timeout: 5 * time.Second})
	}()

	go func() {
		select {
		case asidePort := <-asidePortCh:
			browserClient := &http.Client{
				Timeout: 5 * time.Second,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if req.URL.Hostname() == "127.0.0.1" || req.URL.Hostname() == "localhost" {
						req.URL.Host = fmt.Sprintf("127.0.0.1:%d", asidePort)
					}
					return nil
				},
			}

			resp, err := browserClient.Get(authURL)
			if err != nil {
				browserErrCh <- fmt.Errorf("browser visit failed: %w", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				browserErrCh <- fmt.Errorf("browser expected 200 OK, got %d: %s", resp.StatusCode, string(body))
				return
			}
			browserErrCh <- nil
		case <-ctx.Done():
			browserErrCh <- ctx.Err()
		}
	}()

	result, err := waitAndExchange()
	if err != nil {
		t.Fatalf("waitAndExchange failed: %v", err)
	}

	if browserErr := <-browserErrCh; browserErr != nil {
		t.Fatalf("browser error: %v", browserErr)
	}
	if asideErr := <-asideErrCh; asideErr != nil {
		t.Fatalf("A-side error: %v", asideErr)
	}
	if bsideErr := <-bsideErrCh; bsideErr != nil {
		t.Fatalf("B-side error: %v", bsideErr)
	}

	if result.AccessToken == "" {
		t.Errorf("expected non-empty AccessToken")
	}
}

// TestE2E_LANBridge_UntrustedPeerRejected verifies that an untrusted peer (identity C)
// is rejected by A-side's mTLS listener when C's certificate is not pinned in A's peer store.
func TestE2E_LANBridge_UntrustedPeerRejected(t *testing.T) {
	idA, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity A: %v", err)
	}
	idB, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity B: %v", err)
	}
	idC, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity C: %v", err)
	}

	// A only trusts B; C is an untrusted outsider
	trA := createTestLANTransport(t, idA, []string{idB.Fingerprint})
	// C tries to connect to A, presenting C's cert
	trC := createTestLANTransport(t, idC, []string{idA.Fingerprint})

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
			acceptErrChan <- errors.New("expected accept to fail with untrusted peer C")
		} else {
			acceptErrChan <- nil // failed as expected
		}
	}()

	conn, dialErr := trC.Dial(l.Addr().String())
	if dialErr == nil {
		// If dial didn't immediately catch the handshake error, sending/receiving must fail
		_ = conn.Send(protocol.TypeBridgeRequest, protocol.BridgeRequest{})
		_, err := conn.Receive()
		if err == nil {
			t.Fatal("expected untrusted peer C to be rejected during receive")
		}
		_ = conn.Close()
	}

	select {
	case err := <-acceptErrChan:
		if err != nil {
			t.Fatalf("accept error check: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for accept failure")
	}
}
