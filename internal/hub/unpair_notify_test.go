package hub

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// postPeersRemove removes a peer through the same API the dashboard's Unpair
// button calls, so the notice path under test is the shipped path.
func postPeersRemove(t *testing.T, h *Hub, fingerprint string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"fingerprint": fingerprint})
	req, _ := http.NewRequest(http.MethodPost, "http://"+h.WebAddr()+"/api/peers/remove", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+h.WebAddr())
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/peers/remove failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/peers/remove = %d, want 200", resp.StatusCode)
	}
}

// Unpairing is unilateral by design - the removing side must never need the
// other machine's consent - but the other machine must not be left believing
// the pair is still live. Before this existed, the removed peer discovered its
// status only by being rejected on its next send, with a message that blamed a
// generic "receiver refused" instead of naming the cause. The remover tells
// the removed peer directly, over the authenticated wire connection: both
// stores converge while both machines are up, and the removal is announced
// rather than inferred.
func TestHub_UnpairNotifiesRemovedPeer(t *testing.T) {
	hA, err := NewHub(HubConfig{
		TransportType:     "lan",
		ListenAddr:        "127.0.0.1:0",
		WebAddr:           "127.0.0.1:0",
		StoreDir:          t.TempDir(),
		Headless:          true,
		AutoAcceptPairing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hB, err := NewHub(HubConfig{
		TransportType:     "lan",
		ListenAddr:        "127.0.0.1:0",
		WebAddr:           "127.0.0.1:0",
		StoreDir:          t.TempDir(),
		Headless:          true,
		AutoAcceptPairing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = hA.Start(ctx) }()
	go func() { _ = hB.Start(ctx) }()
	select {
	case <-hA.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Hub A")
	}
	select {
	case <-hB.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Hub B")
	}
	defer func() {
		_ = hA.Stop()
		_ = hB.Stop()
		cancel()
	}()

	// Hub A initiates so its store records Hub B's exact wire address - the
	// address the notice will be dialled at.
	pairBody, _ := json.Marshal(map[string]string{"peer_addr": hB.P2PAddr().String()})
	pairReq, _ := http.NewRequest(http.MethodPost, "http://"+hA.WebAddr()+"/api/pair/initiate", bytes.NewReader(pairBody))
	pairReq.Header.Set("Content-Type", "application/json")
	pairReq.Header.Set("Origin", "http://"+hA.WebAddr())
	pairReq.Header.Set(IPCTokenHeader, hA.ipcToken)
	pairResp, err := (&http.Client{Timeout: 15 * time.Second}).Do(pairReq)
	if err != nil {
		t.Fatalf("initiate failed: %v", err)
	}
	pairResp.Body.Close()
	if pairResp.StatusCode != http.StatusOK {
		t.Fatalf("initiate = %d, want 200", pairResp.StatusCode)
	}

	peersA := hA.Store().ListPeers()
	if len(peersA) != 1 {
		t.Fatalf("expected 1 peer on A after pairing, got %d", len(peersA))
	}
	if len(hB.Store().ListPeers()) != 1 {
		t.Fatalf("expected 1 peer on B after pairing, got %d", len(hB.Store().ListPeers()))
	}
	removedFingerprint := peersA[0].Fingerprint

	postPeersRemove(t, hA, removedFingerprint)

	if got := len(hA.Store().ListPeers()); got != 0 {
		t.Fatalf("remover still lists %d peers after unpair", got)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(hB.Store().ListPeers()) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("removed peer still lists %d trusted peer(s) after 5s - the unpair notice never converged the stores", len(hB.Store().ListPeers()))
}

// sendUnpairNotice dials addr with clientCert and pushes one unpair notice at
// it, exactly as a hub would - including from a hub that is not trusted.
func sendUnpairNotice(t *testing.T, addr, serverFingerprint string, clientCert tls.Certificate, byFingerprint string) {
	t.Helper()
	tr, err := transport.NewLANTransport(transport.LANTransportConfig{
		Cert:                clientCert,
		TrustedFingerprints: []string{serverFingerprint},
	})
	if err != nil {
		t.Fatalf("client transport: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := transport.DialPinnedContext(ctx, tr, addr, serverFingerprint)
	if err != nil {
		t.Fatalf("dial pinned: %v", err)
	}
	defer conn.Close()
	if err := conn.Send(protocol.TypePeerUnpaired, protocol.PeerUnpaired{ByFingerprint: byFingerprint}); err != nil {
		t.Fatalf("send unpair notice: %v", err)
	}
}

// A notice may only ever remove the machine that cryptographically sent it.
// The gate walks both spoof shapes a stranger on the LAN could try: claiming
// to be a trusted peer (payload says the trusted fingerprint, certificate says
// the attacker), and announcing its own removal (not in the store, nothing to
// converge). Neither may touch the store - otherwise any host that can reach
// the wire port could silently unpair other machines from each other with one
// packet. This is a regression gate for the notice path: it passes vacuously
// on a build that ignores the type entirely, and fails the moment a handler
// trusts the payload over the certificate.
func TestHub_UnpairNoticeFromUntrustedSenderIsIgnored(t *testing.T) {
	h, err := NewHub(HubConfig{
		TransportType: "lan",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      t.TempDir(),
		Headless:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("hub not ready")
	}
	defer h.Stop()
	serverAddr := h.P2PAddr().String()
	serverFP := h.Identity().Fingerprint

	// A trusted peer that must survive both spoofed notices.
	trusted, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := h.Store().AddPeer(pairing.Peer{
		Fingerprint: trusted.Fingerprint,
		Name:        "trusted-peer",
		Address:     "127.0.0.1:9877",
		CertPEM:     trusted.CertPEM,
	}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	attacker, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	attackerCert, err := tls.X509KeyPair(attacker.CertPEM, attacker.KeyPEM)
	if err != nil {
		t.Fatalf("attacker keypair: %v", err)
	}

	// Shape 1: masquerade - the payload claims to be the trusted peer.
	sendUnpairNotice(t, serverAddr, serverFP, attackerCert, trusted.Fingerprint)
	// Shape 2: self-announcement from a peer the store never listed.
	sendUnpairNotice(t, serverAddr, serverFP, attackerCert, attacker.Fingerprint)

	// Watch for the whole grace period: an instant check could miss a removal
	// that raced the second notice.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		peers := h.Store().ListPeers()
		if len(peers) != 1 {
			t.Fatalf("store changed after spoofed notices: %d peer(s), want the trusted one intact", len(peers))
		}
		if peers[0].Fingerprint != trusted.Fingerprint {
			t.Fatalf("spoofed notice replaced the trusted peer with %s", peers[0].Fingerprint)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
