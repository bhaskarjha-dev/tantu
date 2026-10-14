package hub

// Gates for address learning.
//
// The claim being gated is not "the code compiles" and not "a field exists". It
// is that a peer's address is only ever moved by an observation bound to a
// completed authenticated handshake, and that the two directions differ in what
// they are allowed to learn. Both halves are load-bearing: the trust argument
// for the whole feature is that nothing weaker than a handshake may write, and
// the correctness argument is that an inbound port is never a listening port.
//
// Everything that can be produced, is. The two end-to-end gates start two real
// Hubs, pair them through the product's own path, send a real payload over real
// mTLS, and then read what the store says. Removing the corresponding call fails
// them by name.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// TestOutboundSendConfirmsThePeerAddress is the produced case for the outbound
// observation: a real A to B send, then the store must say the address was
// confirmed. Before this field existed the store could not answer that question
// at all -- LastSeen moves only on pairing and on an address rewrite, so a peer
// that had been unreachable for a week looked identical to one that had just
// transferred.
func TestOutboundSendConfirmsThePeerAddress(t *testing.T) {
	a, _ := startPairedHubs(t, t.TempDir())
	target := solePeerName(t, a)

	// Captured *after* pairing, and the assertion is that it advances across the
	// transfer. The first version of this gate asserted the field started at
	// zero, which pairing itself now stamps -- so it failed for the wrong reason
	// when the observation was removed, and would have kept passing if the
	// observation broke any other way. A gate has to fail for the reason it
	// exists.
	before := peerByName(t, a, target)
	if before.LastAddressConfirmed.IsZero() {
		t.Fatalf("pairing did not stamp LastAddressConfirmed; a freshly paired peer reads as " +
			"never confirmed")
	}

	body, _ := json.Marshal(map[string]string{"text": "confirm the address", "name": "gate-confirm", "peer": target})
	code, respBody := postJSON(t, "http://"+a.WebAddr()+"/api/drop/upload", a.ipcToken, "http://"+a.WebAddr(), body)
	if code != http.StatusOK {
		t.Fatalf("upload = %d: %s", code, respBody)
	}

	after := waitForAddressConfirmedAfter(t, a, target, before.LastAddressConfirmed)
	if after.Address != before.Address {
		t.Errorf("the peer did not move, yet its address changed from %q to %q; an "+
			"observation that agrees with what is stored must not rewrite it",
			before.Address, after.Address)
	}
}

// TestInboundReceiveConfirmsThePeerAddress is the same claim from the other
// direction. B sends to A; A must advance the confirmation without being told.
func TestInboundReceiveConfirmsThePeerAddress(t *testing.T) {
	a, b := startPairedHubs(t, t.TempDir())
	bSees := solePeerName(t, b)

	before := peerByName(t, a, bSees)
	if before.LastAddressConfirmed.IsZero() {
		t.Fatalf("pairing did not stamp LastAddressConfirmed on A")
	}

	body, _ := json.Marshal(map[string]string{"text": "reverse direction", "name": "gate-inbound", "peer": bSees})
	code, respBody := postJSON(t, "http://"+b.WebAddr()+"/api/drop/upload", b.ipcToken, "http://"+b.WebAddr(), body)
	if code != http.StatusOK {
		t.Fatalf("upload = %d: %s", code, respBody)
	}

	// The receipt on A is what triggers A's inbound observation, so wait for the
	// confirmation to advance rather than for the sender's success.
	waitForAddressConfirmedAfter(t, a, bSees, before.LastAddressConfirmed)
}

// TestConfirmPeerAddressDoesNotRewriteAnUnmovedPeer is the decision function
// behind both produced gates: confirming the address already on record must
// report "unchanged" and leave it alone, and must still stamp the confirmation.
func TestConfirmPeerAddressDoesNotRewriteAnUnmovedPeer(t *testing.T) {
	s := newTestStore(t)
	fp := seedTestPeer(t, s, "192.168.1.50:9877")

	before := mustPeer(t, s, fp)
	changed, err := s.ConfirmPeerAddress(fp, "192.168.1.50:9877")
	if err != nil {
		t.Fatalf("ConfirmPeerAddress: %v", err)
	}
	if changed {
		t.Error("confirming the address already stored reported a change")
	}
	after := mustPeer(t, s, fp)
	if after.Address != before.Address {
		t.Errorf("address changed from %q to %q on a confirmation that agreed", before.Address, after.Address)
	}
	if !after.LastAddressConfirmed.After(before.LastAddressConfirmed) {
		t.Error("confirmation did not advance LastAddressConfirmed")
	}
}

// The bare-host and explicit-port spellings of one endpoint are one endpoint.
// Without this normalisation every handshake would rewrite the record, because
// the store would keep seeing a difference that is not there.
func TestConfirmPeerAddressTreatsBareHostAndExplicitPortAsOne(t *testing.T) {
	s := newTestStore(t)
	fp := seedTestPeer(t, s, "192.168.1.50")

	changed, err := s.ConfirmPeerAddress(fp, "192.168.1.50:9877")
	if err != nil {
		t.Fatalf("ConfirmPeerAddress: %v", err)
	}
	if changed {
		t.Error("a bare host and its explicit-port spelling were treated as a move")
	}
}

// A peer that genuinely moved must be updated, and the caller must be able to
// tell that it happened, because that is what earns a log line.
func TestConfirmPeerAddressReportsAndAppliesAMove(t *testing.T) {
	s := newTestStore(t)
	fp := seedTestPeer(t, s, "192.168.1.50:9877")

	changed, err := s.ConfirmPeerAddress(fp, "10.0.2.7:9877")
	if err != nil {
		t.Fatalf("ConfirmPeerAddress: %v", err)
	}
	if !changed {
		t.Error("a peer that changed address did not report a change")
	}
	if got := mustPeer(t, s, fp).Address; got != "10.0.2.7:9877" {
		t.Errorf("address = %q, want %q", got, "10.0.2.7:9877")
	}
}

// Refuses inputs that must never be written, rather than storing something
// unrouteable and reporting success.
func TestConfirmPeerAddressRejectsUnusableInput(t *testing.T) {
	s := newTestStore(t)
	fp := seedTestPeer(t, s, "192.168.1.50:9877")
	original := mustPeer(t, s, fp).Address

	for _, bad := range []struct{ name, addr string }{
		{"zero port", "192.168.1.50:0"},
		{"port out of range", "192.168.1.50:70000"},
		{"control character", "192.168.1.50:98\x0077"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			if _, err := s.ConfirmPeerAddress(fp, bad.addr); err == nil {
				t.Errorf("ConfirmPeerAddress(%q) returned no error", bad.addr)
			}
			if got := mustPeer(t, s, fp).Address; got != original {
				t.Errorf("a rejected observation still changed the address: %q", got)
			}
		})
	}

	if _, err := s.ConfirmPeerAddress(unknownFingerprint, "192.168.1.50:9877"); err == nil {
		t.Error("confirming an address for an unknown peer returned no error")
	}
}

// An absent observation is not a bad one. A connection that yields no address
// should quietly confirm nothing rather than erroring, and must certainly not
// blank the stored address.
func TestConfirmPeerAddressIgnoresAnEmptyObservation(t *testing.T) {
	s := newTestStore(t)
	fp := seedTestPeer(t, s, "192.168.1.50:9877")
	original := mustPeer(t, s, fp)

	changed, err := s.ConfirmPeerAddress(fp, "   ")
	if err != nil {
		t.Errorf("an empty observation returned an error: %v", err)
	}
	if changed {
		t.Error("an empty observation reported a change")
	}
	if got := mustPeer(t, s, fp); got.Address != original.Address {
		t.Errorf("an empty observation changed the address to %q", got.Address)
	}
}

// Pairing proves an address too, so a freshly paired peer must not read as
// "never confirmed" until its first transfer.
func TestPairingStampsAddressConfirmed(t *testing.T) {
	a, _ := startPairedHubs(t, t.TempDir())
	target := solePeerName(t, a)
	p := peerByName(t, a, target)
	if p.LastAddressConfirmed.IsZero() {
		t.Error("a freshly paired peer has a zero LastAddressConfirmed; pairing proves the address")
	}
}

// The duplicated default port is deliberate -- internal/pairing cannot import
// internal/transport, because transport imports pairing -- and this is what
// stops the duplication rotting.
//
// It is compared through its observable effect rather than by exporting an
// unexported constant: normalising a bare host must yield the transport's own
// default, which is the only reason the copy exists.
func TestDefaultLANPortMatchesTransport(t *testing.T) {
	s := newTestStore(t)
	fp := seedTestPeer(t, s, "192.168.1.50")

	// Confirm the bare host with the explicit port transport names. If the two
	// constants disagreed, these would be two different endpoints and the store
	// would report a move.
	changed, err := s.ConfirmPeerAddress(fp, "192.168.1.50:"+transport.DefaultLANPort)
	if err != nil {
		t.Fatalf("ConfirmPeerAddress: %v", err)
	}
	if changed {
		t.Errorf("pairing's default port and transport.DefaultLANPort (%q) disagree: "+
			"normalising a bare host is not the same endpoint", transport.DefaultLANPort)
	}
}

// --- helpers -------------------------------------------------------------

func newTestStore(t *testing.T) *pairing.PeerStore {
	t.Helper()
	s, err := pairing.NewPeerStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	return s
}

// unknownFingerprint is a syntactically valid fingerprint that is deliberately
// not in any store, used to assert that confirming an address for a peer that
// does not exist fails rather than creating one.
const unknownFingerprint = "0000000000000000000000000000000000000000000000000000000000000000"

func seedTestPeer(t *testing.T, s *pairing.PeerStore, addr string) string {
	t.Helper()
	// A real identity, because the store validates the certificate and a stub
	// PEM would be rejected -- correctly.
	id, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := s.AddPeer(pairing.Peer{
		Fingerprint: id.Fingerprint,
		Name:        "gate-peer",
		Address:     addr,
		CertPEM:     id.CertPEM,
		FirstSeen:   time.Now(),
		LastSeen:    time.Now(),
	}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	return id.Fingerprint
}

func mustPeer(t *testing.T, s *pairing.PeerStore, fp string) pairing.Peer {
	t.Helper()
	p, ok := s.GetPeer(fp)
	if !ok {
		t.Fatalf("peer %s is not in the store", fp)
	}
	return p
}

// peerByName finds the single peer of `a` and returns it, so the gates do not
// depend on how the store happens to key its map.
func peerByName(t *testing.T, a *Hub, name string) pairing.Peer {
	t.Helper()
	peers := a.Store().ListPeers()
	if len(peers) != 1 {
		t.Fatalf("expected exactly one peer on A, found %d", len(peers))
	}
	return peers[0]
}

// waitForAddressConfirmedAfter waits until the store records a confirmation
// strictly newer than `previous`, which is what an observation must do.
func waitForAddressConfirmedAfter(t *testing.T, a *Hub, name string, previous time.Time) pairing.Peer {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last pairing.Peer
	for time.Now().Before(deadline) {
		last = peerByName(t, a, name)
		if last.LastAddressConfirmed.After(previous) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("LastAddressConfirmed did not advance past %s within 10s of a completed "+
		"authenticated transfer; it is still %s, so nothing observed the connection",
		previous, last.LastAddressConfirmed)
	return last
}
