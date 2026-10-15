package hub

// Gates for the proactive heartbeat.
//
// The claim is not "a heartbeat is sent". It is that a Hub left running with an
// idle peer reports that peer as reachable, and keeps the peer's address proven
// current -- both without a single transfer. Before this, an idle peer was
// indistinguishable from a dead one, which is why `last_address_confirmed`
// existed and nothing read it.
//
// Every gate below produces its condition with real Hubs, real certificates and
// a real mTLS handshake, and then waits for the product's own loop to do the
// thing. Nothing here constructs a PeerHealth and asserts it round-trips.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// startHeartbeatHubs brings up two paired Hubs whose heartbeat runs fast enough
// for a test to watch, and which never exchange a transfer unless a gate asks.
func startHeartbeatHubs(t *testing.T) (a *Hub, b *Hub) {
	t.Helper()
	newHub := func(out string) *Hub {
		h, err := NewHub(HubConfig{
			TransportType:         "lan",
			ListenAddr:            "127.0.0.1:0",
			WebAddr:               "127.0.0.1:0",
			StoreDir:              t.TempDir(),
			OutputDir:             out,
			Headless:              true,
			AutoAcceptPairing:     true,
			PeerHeartbeatInterval: 200 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("NewHub: %v", err)
		}
		return h
	}
	a = newHub(t.TempDir())
	b = newHub(t.TempDir())

	ctx, _ := contextWithCancelForTest(t)
	go func() { _ = a.Start(ctx) }()
	go func() { _ = b.Start(ctx) }()
	for name, h := range map[string]*Hub{"A": a, "B": b} {
		select {
		case <-h.Ready():
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for Hub %s", name)
		}
	}
	t.Cleanup(func() {
		_ = a.Stop()
		_ = b.Stop()
	})

	// A initiates, so A is the side that holds the address worth confirming.
	pairBody, _ := json.Marshal(map[string]string{"peer_addr": b.P2PAddr().String()})
	code, resp := postJSON(t, "http://"+a.WebAddr()+"/api/pair/initiate", a.ipcToken, "http://"+a.WebAddr(), pairBody)
	if code != 200 {
		t.Fatalf("pair initiate = %d: %s", code, resp)
	}
	waitForPeerCount(t, b, 1, 15*time.Second)
	return a, b
}

// waitForHealth polls until the peer is reported reachable, or gives up saying
// what it saw. The heartbeat runs on its own goroutine, so convergence has to be
// waited for rather than assumed -- the precedent is waitForPeerCount.
func waitForHealth(t *testing.T, h *Hub, fingerprint string, want bool, within time.Duration) PeerHealth {
	t.Helper()
	deadline := time.Now().Add(within)
	var last PeerHealth
	for time.Now().Before(deadline) {
		last = h.PeerHealthOf(fingerprint)
		if last.Reachable == want && !last.LastChecked.IsZero() {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("after %s the peer's reachability was %v (lastChecked=%s, failures=%d), want %v",
		within, last.Reachable, last.LastChecked, last.ConsecutiveFailures, want)
	return last
}

// The headline claim: an idle peer is reported reachable, and its address is
// proven current, with no transfer of any kind having happened.
//
// The store's last_address_confirmed is the part that is easy to fake. It is
// stamped by pairing too, so this gate captures it *after* pairing and requires
// it to advance afterwards -- the same correction the address-learning gates
// needed when they asserted a field started at zero and so failed a
// precondition instead of the claim.
func TestHeartbeatConfirmsAPeerWithoutAnyTransfer(t *testing.T) {
	a, _ := startHeartbeatHubs(t)
	peer := solePeer(t, a)

	// Captured after pairing, so "it moved on its own" is what is asserted.
	afterPairing := mustPeerByFingerprint(t, a, peer.Fingerprint)

	health := waitForHealth(t, a, peer.Fingerprint, true, 20*time.Second)
	if health.LastOK.IsZero() {
		t.Error("the peer is reported reachable but LastOK was never set, so a UI cannot say " +
			"when it last answered")
	}

	// No transfer happened in this test. The address confirmation must still
	// have advanced past what pairing alone could have set, or the field still
	// means nothing.
	afterHeartbeat := mustPeerByFingerprint(t, a, peer.Fingerprint)
	if !afterHeartbeat.LastAddressConfirmed.After(afterPairing.LastAddressConfirmed) {
		t.Errorf("last_address_confirmed did not advance past %s with no transfer happening; "+
			"an idle peer's address is still never proven after pairing",
			afterPairing.LastAddressConfirmed)
	}
}

// A peer that goes away must be reported gone, and the fact that it was working
// must survive.
//
// The second half is the one that matters for a UI. "Answered 4 minutes ago,
// stopped answering since" and "never answered once" are different bugs: the
// first is a sleeping laptop, the second is a stale address or an unpaired
// machine. Collapsing them would lose the only clue a user has.
func TestHeartbeatReportsAPeerThatWentAway(t *testing.T) {
	a, b := startHeartbeatHubs(t)
	peer := solePeer(t, a)

	up := waitForHealth(t, a, peer.Fingerprint, true, 20*time.Second)

	// Stop the peer for real. The next probe dials a closed port.
	if err := b.Stop(); err != nil {
		t.Fatalf("stop B: %v", err)
	}

	down := waitForHealth(t, a, peer.Fingerprint, false, 30*time.Second)

	// waitForHealth returns at the *first* unanswered probe, which is the right
	// moment for "the peer went away" and far too early for "the counter is
	// climbing". Those are separate claims and conflating them produced a gate
	// that failed on the first run while asserting something two minutes later.
	down = waitForFailureCount(t, a, peer.Fingerprint, 2, 30*time.Second)

	if down.LastOK.Before(up.LastOK) {
		t.Errorf("LastOK moved backwards, from %s to %s; a peer that stopped answering must "+
			"keep the record of when it last did", up.LastOK, down.LastOK)
	}
	if down.ConsecutiveFailures < 2 {
		t.Errorf("ConsecutiveFailures is %d after the peer went away; it must count up, since "+
			"that is what distinguishes a peer that is asleep from one that is gone",
			down.ConsecutiveFailures)
	}
	if down.LastChecked.Before(up.LastChecked) {
		t.Errorf("LastChecked did not advance past %s after the failure", up.LastChecked)
	}
}

// waitForFailureCount polls until the peer has been probed unsuccessfully at
// least n times.
func waitForFailureCount(t *testing.T, h *Hub, fingerprint string, n int, within time.Duration) PeerHealth {
	t.Helper()
	deadline := time.Now().Add(within)
	var last PeerHealth
	for time.Now().Before(deadline) {
		last = h.PeerHealthOf(fingerprint)
		if last.ConsecutiveFailures >= n {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("after %s the peer had %d consecutive failures, want at least %d",
		within, last.ConsecutiveFailures, n)
	return last
}

// The backoff ladder, as a decision function.
//
// This one is legitimately pure. The input is the whole domain -- a sequence of
// outcomes -- and the output is when to try next, so feeding outcomes and
// asserting the schedule tests the decision rather than a stand-in for it. It is
// not a substitute for the produced gates above, which are what prove the loop
// actually consults this.
func TestProbeBackoffGrowsOnFailureAndResetsOnSuccess(t *testing.T) {
	base := 60 * time.Second
	probe := &peerProbe{interval: base}
	now := time.Now()

	// 16x base would be 16 minutes, so the ladder reaches the absolute 15-minute
	// ceiling on the fifth rung and stays there. The first rung is the base
	// itself, not twice it: a peer that was answering a second ago is the one
	// most likely to come straight back, and making it wait two minutes for the
	// retry is backwards.
	want := []time.Duration{
		base, 2 * base, 4 * base, 8 * base, peerHeartbeatMaxBackoff, peerHeartbeatMaxBackoff,
		peerHeartbeatMaxBackoff,
	}
	for i, w := range want {
		scheduleProbeNext(probe, false, now, base)
		if probe.interval != w {
			t.Fatalf("after failure %d the interval is %s, want %s", i+1, probe.interval, w)
		}
		if got := probe.next.Sub(now); got != w {
			t.Errorf("after failure %d the next probe is in %s, want %s", i+1, got, w)
		}
	}

	scheduleProbeNext(probe, true, now, base)
	if probe.interval != base {
		t.Errorf("after a success the interval is %s, want the base %s; a peer that comes "+
			"back must not stay on the slow ladder", probe.interval, base)
	}
}

// A long configured interval must scale the whole ladder, but the ceiling stays
// absolute. A ceiling relative to the base would mean a generous interval also
// buys a generous ceiling, and the ceiling is the thing that stops an absent
// peer being dialled all afternoon.
func TestProbeBackoffCeilingDoesNotScaleWithTheInterval(t *testing.T) {
	base := 10 * time.Minute
	probe := &peerProbe{interval: base}
	now := time.Now()
	for i := 0; i < 10; i++ {
		scheduleProbeNext(probe, false, now, base)
	}
	if probe.interval != peerHeartbeatMaxBackoff {
		t.Errorf("the ceiling is %s, want the absolute %s; scaling it with the base interval "+
			"would let a long interval mean an unreachable peer is dialled all day",
			probe.interval, peerHeartbeatMaxBackoff)
	}
}

// The receiving side must not consider a heartbeat a failed connection.
//
// This is the claim that is only true by reading the code: the dispatcher
// handles a heartbeat-first connection, and treats the resulting EOF as silence
// rather than an error. Reading it is exactly what this repository keeps being
// wrong about. So the peer that is being pinged is asked what it saw.
func TestHeartbeatLeavesNoErrorOnTheReceivingPeer(t *testing.T) {
	_, b := startHeartbeatHubs(t)

	// Long enough for several probes at the test interval.
	time.Sleep(3 * time.Second)

	for _, e := range b.EventLogger().GetRecent() {
		msg := strings.ToLower(e.Message)
		if e.Level != LevelError {
			continue
		}
		if strings.Contains(msg, "heartbeat") || strings.Contains(msg, "initial envelope") ||
			strings.Contains(msg, "unexpected eof") || strings.Contains(msg, "connection reset") {
			t.Errorf("the peer being pinged logged an error for a heartbeat: %q\n"+
				"a keepalive that shows up as a failure in the other machine's log is worse "+
				"than no keepalive", e.Message)
		}
	}
}

// Loopback is the same machine by definition, so probing it would report
// "reachable" about a peer whose reachability was never in question and would
// spend a handshake per interval for nothing.
func TestHeartbeatDoesNotProbeOnLoopback(t *testing.T) {
	h, err := NewHub(HubConfig{
		TransportType:         "loopback",
		ListenAddr:            "127.0.0.1:0",
		WebAddr:               "127.0.0.1:0",
		StoreDir:              t.TempDir(),
		OutputDir:             t.TempDir(),
		Headless:              true,
		PeerHeartbeatInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, _ := contextWithCancelForTest(t)
	t.Cleanup(func() { _ = h.Stop() })
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("Hub never became ready")
	}

	// With no peers at all there is nothing to probe, so this asserts the loop
	// runs harmlessly rather than that it skips. What it must never do is start
	// probing once a peer exists, so a peer is added and the map is still empty.
	peer := seedLoopbackPeer(t, h)
	time.Sleep(1500 * time.Millisecond)

	if len(h.AllPeerHealth()) != 0 {
		t.Errorf("the heartbeat probed a loopback peer; loopback is the same machine, so this "+
			"spends a handshake per interval to learn nothing. Reported: %v", h.AllPeerHealth())
	}
	_ = peer
}

// The API must carry what the heartbeat learned.
//
// The Go gates above prove the Hub records reachability; this proves it reaches
// the dashboard, which is a separate path with its own shape and its own way of
// dropping a field. A `Health *PeerHealth` that was never serialised would leave
// every earlier gate green and the Peers tab permanently blank.
//
// It also pins the *absent* case, which is the one that matters for trust: a peer
// the heartbeat has not probed must serialise as no health at all rather than as
// a zero value reading "unreachable". The distinction is a pointer in peerStatus
// and nothing else preserves it.
func TestPeersAPIReportsReachability(t *testing.T) {
	a, _ := startHeartbeatHubs(t)
	peer := solePeer(t, a)

	waitForHealth(t, a, peer.Fingerprint, true, 20*time.Second)

	var body struct {
		Peers []struct {
			Name                 string      `json:"name"`
			Fingerprint          string      `json:"fingerprint"`
			Health               *PeerHealth `json:"health"`
			LastAddressConfirmed time.Time   `json:"last_address_confirmed"`
		} `json:"peers"`
	}
	getJSON(t, "http://"+a.WebAddr()+"/api/status", a.ipcToken, &body)

	if len(body.Peers) != 1 {
		t.Fatalf("/api/status returned %d peers, want 1", len(body.Peers))
	}
	got := body.Peers[0]
	if got.Health == nil {
		t.Fatal("the API reports no health for a peer the heartbeat has answered for; " +
			"the Peers tab has nothing to render and every earlier gate stays green")
	}
	if !got.Health.Reachable {
		t.Errorf("the API reports the peer as not reachable when it answered a heartbeat: %+v", got.Health)
	}
	if got.Health.LastOK.IsZero() {
		t.Error("the API carries health with no LastOK, so a UI cannot say when it last answered")
	}
	if got.LastAddressConfirmed.IsZero() {
		t.Error("the API carries no last_address_confirmed")
	}
}

// A peer nobody has probed must be absent from the payload rather than present
// and false.
func TestPeersAPIReportsNoHealthBeforeTheFirstProbe(t *testing.T) {
	h, err := NewHub(HubConfig{
		TransportType: "lan",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      t.TempDir(),
		OutputDir:     t.TempDir(),
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, _ := contextWithCancelForTest(t)
	t.Cleanup(func() { _ = h.Stop() })
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("Hub never became ready")
	}
	seedLoopbackPeer(t, h)

	var body struct {
		Peers []struct {
			Health *PeerHealth `json:"health"`
		} `json:"peers"`
	}
	getJSON(t, "http://"+h.WebAddr()+"/api/status", h.ipcToken, &body)

	if len(body.Peers) != 1 {
		t.Fatalf("/api/status returned %d peers, want 1", len(body.Peers))
	}
	if body.Peers[0].Health != nil {
		t.Errorf("an unprobed peer carries health %+v; it must be absent, so the dashboard can "+
			"distinguish 'not probed yet' from 'unreachable'", body.Peers[0].Health)
	}
}

// getJSON fetches an endpoint with the Hub's IPC token and decodes it.
//
// Built on the package's existing testGet rather than a second copy of the same
// request, for the usual reason: two helpers that differ only in what they do
// with a failure response are one of them wrong.
func getJSON(t *testing.T, url, token string, out any) {
	t.Helper()
	resp, err := testGet(token, url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

// helpers

func solePeer(t *testing.T, h *Hub) pairing.Peer {
	t.Helper()
	peers := h.Store().ListPeers()
	if len(peers) != 1 {
		t.Fatalf("expected exactly 1 peer, found %d", len(peers))
	}
	return peers[0]
}

func seedLoopbackPeer(t *testing.T, h *Hub) pairing.Peer {
	t.Helper()
	id, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	p := pairing.Peer{
		Fingerprint: id.Fingerprint,
		Name:        "loopback-peer",
		Address:     net.JoinHostPort("127.0.0.1", "9877"),
		CertPEM:     id.CertPEM,
	}
	if err := h.Store().AddPeer(p); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	return p
}

// contextWithCancelForTest gives a test a context cancelled at cleanup, so the
// heartbeat goroutines a test starts do not outlive it.
func contextWithCancelForTest(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx, cancel
}
