package hub

// Gates for the reconnection engine.
//
// The claim is not "discovery can propose an address" -- that was already true,
// and it was already unsafe to act on. The claim is that a peer which has become
// unreachable **because its address changed** is found again automatically, and
// that nothing weaker than a completed mTLS handshake may write the new address.
//
// Both halves are produced, not constructed. Every gate below starts real Hubs
// with real certificates, corrupts a real store entry to make the condition
// genuine, and then exercises the real code path. Nothing here feeds a
// hand-written value into a decision function and calls it coverage, because
// that is the kind of gate that passes against a product which does not do the
// thing at all.

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// staleAddress writes a wrong-but-valid address for A's only peer, producing the
// condition the feature exists to repair: the store names a peer at an endpoint
// where it is not.
//
// This is the one constructed input in this file, and it is a legitimate one --
// it stands in for history (a peer that used to be there) rather than for
// behaviour (a peer moving, which is what the engine is for and cannot be
// conjured without a second machine on a second network).
func staleAddress(t *testing.T, a *Hub, addr string) {
	t.Helper()
	staleFor(t, a, a.Store().ListPeers()[0].Fingerprint, addr)
}

// staleFor writes a wrong-but-valid address for one specific peer, producing the
// condition the feature exists to repair: the store names a peer at an endpoint
// where it is not.
//
// This is the one constructed input in this file, and it is a legitimate one --
// it stands in for history (a peer that used to be there) rather than for
// behaviour (a peer moving, which is what the engine is for and cannot be
// conjured without a second machine on a second network).
func staleFor(t *testing.T, a *Hub, fingerprint, addr string) {
	t.Helper()
	if err := a.Store().UpdatePeerAddress(fingerprint, addr); err != nil {
		t.Fatalf("seed the stale address %q: %v", addr, err)
	}
}

func mustPeerByFingerprint(t *testing.T, a *Hub, fingerprint string) pairing.Peer {
	t.Helper()
	p, ok := a.Store().GetPeer(fingerprint)
	if !ok {
		t.Fatalf("peer %s is not in A's store", fingerprint)
	}
	return p
}

// waitForAddress polls A's store until the single peer is recorded at want.
// Reconciliation runs in a goroutine -- it must, because a discovery callback
// cannot block on dials -- so convergence has to be polled, not assumed. The
// precedent is waitForPeerCount in the pairing helper: an earlier version of
// this kind of gate asserted convergence immediately and passed on Windows,
// then failed on Linux CI.
func waitForAddress(t *testing.T, a *Hub, want string, within time.Duration) pairing.Peer {
	t.Helper()
	deadline := time.Now().Add(within)
	var p pairing.Peer
	for time.Now().Before(deadline) {
		list := a.Store().ListPeers()
		if len(list) == 1 && list[0].Address == want {
			return list[0]
		}
		if len(list) == 1 {
			p = list[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("A still records the peer at %q after %s; the reconnection engine did not "+
		"find it at %q. A peer that moved network is a permanent silent partition, and "+
		"this is the state that proves it", p.Address, within, want)
	return p
}

// A peer that moved **ports** -- the firewall rule changed, the service was
// rebound, the other machine picked a different number -- is found again.
//
// This is the produced case for the whole feature. Before it, the address was
// never moved by anything except an inbound connection, and the inbound path
// deliberately kept the stored port, so a moved port was not merely unrepaired:
// it was undetectable.
func TestReconnectionFindsAPeerThatMovedPort(t *testing.T) {
	a, b := startPairedHubs(t, t.TempDir())
	realAddr := b.P2PAddr().String()

	// The port in the store is wrong; everything else about the peer is right.
	staleAddress(t, a, "127.0.0.1:1")

	a.reconcileDiscoveredAddress(realAddr, "B")

	got := waitForAddress(t, a, realAddr, 15*time.Second)
	if got.LastAddressConfirmed.IsZero() {
		t.Error("the peer was re-found but its address was never confirmed; a rewrite " +
			"that does not stamp the confirmation leaves no record that it was proven")
	}
}

// A peer that moved **IP** while keeping its port -- broadband to a phone
// hotspot, a DHCP lease, a different subnet -- is found again.
//
// Produced without a second machine by binding B to 127.0.0.2 and leaving A on
// 127.0.0.1, then making A believe the peer is on A's own address at B's port.
// That is precisely the broadband-to-hotspot shape: same port, different subnet,
// so the old inbound host-heal would have had to notice and nothing else would.
func TestReconnectionFindsAPeerThatMovedIP(t *testing.T) {
	a, b := startPairedHubsListening(t, "127.0.0.1:0", "127.0.0.2:0", t.TempDir())
	realAddr := b.P2PAddr().String()

	_, port, err := net.SplitHostPort(realAddr)
	if err != nil {
		t.Fatalf("split B's address %q: %v", realAddr, err)
	}
	if host, _, _ := net.SplitHostPort(realAddr); host != "127.0.0.2" {
		t.Fatalf("B bound to %q, not 127.0.0.2; this gate cannot produce an IP change "+
			"without that and would silently degrade into the port-change case", host)
	}
	// Same port, wrong host: the peer is now one subnet over.
	staleAddress(t, a, net.JoinHostPort("127.0.0.1", port))

	a.reconcileDiscoveredAddress(realAddr, "B")

	waitForAddress(t, a, realAddr, 15*time.Second)
}

// The threat model, produced. Something is genuinely listening at the discovered
// address, it speaks Tantu, it completes the TLS handshake -- and it is not our
// peer. No stored address may move.
//
// This is the gate that says the engine *verifies* rather than *believes*. The
// first version of this gate used a listener that hung up on accept, and it was
// not red: removing the certificate check entirely still passed it, because a
// hung-up port is rejected for transport reasons and says nothing about identity.
// The impostor has to be a working peer of the same product, one that a Hub will
// happily complete a handshake with, for the gate to isolate what it claims to.
//
// Produced with a third real Hub, started on the same transport, never paired
// with A. Deleting the pinned fingerprint from the dial turns this red.
func TestReconciliationRefusesAnAddressThatIsNotOurPeer(t *testing.T) {
	a, _ := startPairedHubs(t, t.TempDir())
	impostor := startUnpairedHub(t)
	impostorAddr := impostor.P2PAddr().String()

	staleAddress(t, a, "127.0.0.1:1")
	before := a.Store().ListPeers()[0]

	a.reconcileDiscoveredAddress(impostorAddr, "somebody-else")
	waitForDialAttempt(t, impostorAddr, 10*time.Second)

	// Give the engine a chance to write something wrong if it is going to.
	time.Sleep(750 * time.Millisecond)

	after := a.Store().ListPeers()
	if len(after) != 1 {
		t.Fatalf("A has %d peers after a beacon from a stranger, want 1", len(after))
	}
	if after[0].Address != before.Address {
		t.Errorf("a Tantu Hub that is not our peer, at %s, moved the stored peer from %q "+
			"to %q; discovery may propose an address, only a handshake proving *which* peer "+
			"may dispose of one", impostorAddr, before.Address, after[0].Address)
	}
	if after[0].Fingerprint != before.Fingerprint {
		t.Errorf("the stored peer changed identity: %q -> %q", before.Fingerprint, after[0].Fingerprint)
	}
}

// waitForDialAttempt blocks until the engine has demonstrably dialled addr, so
// the gates cannot pass by the engine never trying.
//
// The probe is deliberately cheap and passive: a TCP connect to the address the
// engine was told about, which the listening Hub accepts and drops. It proves the
// address is live and reachable at assertion time, without asserting anything
// about the engine -- the assertion is what follows.
func waitForDialAttempt(t *testing.T, addr string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("nothing is listening at %s, so this gate cannot produce an impostor and "+
		"would pass against an engine that never dials", addr)
}

// The threat model, produced, and it is the one that actually bites.
//
// Tantu's own unpinned dial already refuses an unknown certificate -- "untrusted
// peer certificate fingerprint" -- so a beacon from a stranger on the LAN cannot
// move anything. That gate exists and passes. But it is defended by the wrong
// question. An unpinned dial asks "is this certificate someone I know?" and a
// Tantu Hub is asked against **every** peer in the store, so it accepts any of
// them. It cannot answer "is this certificate the peer I was looking for?"
//
// That difference is only observable when the impostor is itself a trusted peer,
// and it is severe when it is: dialling peer C's address in the hope of finding
// peer B, and succeeding, means concluding that **B is at C's address**. A then
// sends to C believing it is B -- and mTLS happily delivers it, because C is
// genuinely trusted. Nothing downstream notices. The address is the identity
// that routing uses, so this does not merely record a wrong hint, it redirects
// traffic to the wrong machine.
//
// Produced with three real Hubs: A paired with B and with C, B's stored address
// made stale, and C's real address announced. Swapping the pinned dial for the
// unpinned one turns this red.
func TestReconciliationWillNotAttributeOnePairedPeersAddressToAnother(t *testing.T) {
	a, b := startPairedHubs(t, t.TempDir())
	c := startHub(t)
	cAddr := c.P2PAddr().String()

	pairAWith(t, a, cAddr)
	waitForPeerCount(t, a, 2, 15*time.Second)

	// B's address goes stale. B is not at C's address; nothing will ever prove
	// that, because B is somewhere else entirely.
	bFingerprint := mustPeerAt(t, a, b.P2PAddr().String()).Fingerprint
	staleFor(t, a, bFingerprint, "127.0.0.1:1")

	bBefore := mustPeerByFingerprint(t, a, bFingerprint)
	cBefore := mustPeerAt(t, a, cAddr)

	a.reconcileDiscoveredAddress(cAddr, "C")
	waitForDialAttempt(t, cAddr, 10*time.Second)
	time.Sleep(750 * time.Millisecond)

	bAfter := mustPeerByFingerprint(t, a, bFingerprint)
	if bAfter.Address != bBefore.Address {
		t.Errorf("peer B was moved to %q, the address of peer C; C is a trusted peer, so "+
			"an unpinned dial succeeds against it and the engine concluded B lives there. "+
			"A would now send B's traffic to C, and C would accept it.",
			bAfter.Address)
	}
	cAfter := mustPeerAt(t, a, cAddr)
	if cAfter.Address != cBefore.Address {
		t.Errorf("peer C moved from %q to %q; a peer already recorded at the discovered "+
			"address must not be rewritten", cBefore.Address, cAfter.Address)
	}
}

// startHub starts a real Hub on the lan transport, paired with nobody: a
// neighbour's machine running the same product.
//
// AutoAcceptPairing is on, as it is on every other Hub in these gates. The first
// version left it off and the mis-attribution gate then failed at *setup* --
// "pair initiate: context deadline exceeded" -- whenever the engine's dial was
// changed to something less careful, because the neighbour's acceptor was busy
// and its pairing response never came back. A gate that can be broken by an
// unrelated dial interfering with setup cannot tell you whether the property it
// names still holds, so the neighbour is made well-behaved and the gate is left
// to isolate exactly one claim.
func startHub(t *testing.T) *Hub {
	t.Helper()
	h, err := NewHub(HubConfig{
		TransportType:     "lan",
		ListenAddr:        "127.0.0.1:0",
		WebAddr:           "127.0.0.1:0",
		StoreDir:          t.TempDir(),
		OutputDir:         t.TempDir(),
		Headless:          true,
		AutoAcceptPairing: true,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for Hub")
	}
	t.Cleanup(func() { _ = h.Stop() })
	return h
}

// startUnpairedHub is startHub under the name the stranger-impostor gate reads
// better with. Both return the same thing: a real Hub nobody here has paired.
func startUnpairedHub(t *testing.T) *Hub { return startHub(t) }

// pairAWith pairs `a` with the Hub at addr, using the product's own pairing
// endpoint, so the resulting store entry is a real pairing and not a fixture.
func pairAWith(t *testing.T, a *Hub, addr string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"peer_addr": addr})
	req, _ := http.NewRequest(http.MethodPost, "http://"+a.WebAddr()+"/api/pair/initiate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+a.WebAddr())
	req.Header.Set(IPCTokenHeader, a.ipcToken)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("pair initiate %s: %v", addr, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair initiate %s = %d, want 200", addr, resp.StatusCode)
	}
}

// mustPeerAt finds A's peer recorded at exactly addr. Pairing records the exact
// wire address, so this identifies a peer without depending on how Name or Alias
// happen to be derived -- a gate that keys on a display name is a gate that can
// fail for a rename.
func mustPeerAt(t *testing.T, a *Hub, addr string) pairing.Peer {
	t.Helper()
	for _, p := range a.Store().ListPeers() {
		if p.Address == addr {
			return p
		}
	}
	t.Fatalf("A has no peer recorded at %s; peers are %v", addr, addressesOf(a))
	return pairing.Peer{}
}

func addressesOf(a *Hub) []string {
	var out []string
	for _, p := range a.Store().ListPeers() {
		out = append(out, p.Address)
	}
	return out
}

// The bound. A beacon that keeps arriving -- which is what a beacon does -- must
// not turn into an outbound dial per broadcast, forever, at nothing.
//
// Measured by counting real TCP accepts on the far end, so the assertion is on
// produced traffic rather than on internal bookkeeping. Four further calls are
// made immediately; without the cooldown each would dial again and the count
// would reach five.
func TestRepeatedBeaconsDoNotProduceRepeatedDials(t *testing.T) {
	a, _ := startPairedHubs(t, t.TempDir())

	impostor, acceptCount := startImpostorListener(t)
	staleAddress(t, a, "127.0.0.1:1")

	for i := 0; i < 5; i++ {
		a.reconcileDiscoveredAddress(impostor, "noisy-neighbour")
	}
	waitForConnects(t, acceptCount, 1, 10*time.Second)

	// Five calls, one beacon burst. One dial.
	time.Sleep(1500 * time.Millisecond)
	if got := acceptCount.get(); got != 1 {
		t.Errorf("five beacons from one address produced %d dials, want 1; the cooldown "+
			"is what stops a chatty LAN from becoming unbounded outbound traffic", got)
	}
}

// startImpostorListener opens a real TCP port that accepts connections and hangs
// up immediately -- something genuinely listening, which is not a Tantu Hub --
// and counts how many connections it received.
//
// The count is the instrument: it measures traffic the engine actually generated,
// so the cooldown gate cannot pass by bookkeeping a map correctly while still
// dialling every time.
func startImpostorListener(t *testing.T) (addr string, count *acceptCounter) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	count = &acceptCounter{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			count.add(1)
			// Hang up at once. A real TLS handshake then fails immediately
			// rather than waiting out the dial timeout, which keeps the gate
			// fast without weakening what it proves.
			_ = c.Close()
		}
	}()
	return ln.Addr().String(), count
}

type acceptCounter struct{ v int32 }

func (c *acceptCounter) add(n int32) { atomic.AddInt32(&c.v, n) }
func (c *acceptCounter) get() int32  { return atomic.LoadInt32(&c.v) }

func waitForConnects(t *testing.T, c *acceptCounter, want int32, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if c.get() >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the listener saw %d connections, want at least %d, within %s; the engine "+
		"never dialled, so the gates above would be passing against code that does nothing",
		c.get(), want, within)
}
