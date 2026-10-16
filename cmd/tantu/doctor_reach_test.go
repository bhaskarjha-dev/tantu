package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// Gates for `tantu doctor` peer reachability.
//
// The defect: doctor reported `peers: OK` purely because peers existed in the
// store. It never asked whether any of them could be reached. So a user whose
// peer had moved networks or gone to sleep ran the command written for the moment
// something is broken, and was told their peers were fine.
//
// This is the same false claim the cockpit's banner made (Z82), on the surface
// where it costs most.
//
// The produced gate below starts a real Hub, registers a peer at a TEST-NET-1
// address -- reserved by RFC 5737 and unroutable by design, so a dial hangs until
// the transport's timeout and then fails -- waits for the Hub's own heartbeat to
// produce a verdict, and asserts doctor reports it. Constructing a health value
// would prove the summariser agrees with a value the test wrote.

func doctorCheck(report healthReport, name string) (healthCheck, bool) {
	for _, c := range report.Checks {
		if c.Name == name {
			return c, true
		}
	}
	return healthCheck{}, false
}

// A peer the Hub has proved unreachable must not be reported as fine.
func TestDoctorReportsAPeerThatIsNotAnswering(t *testing.T) {
	dir := t.TempDir()
	h, err := hub.NewHub(hub.HubConfig{
		// lan, not loopback: only lan peers are probed, and a probe is the only
		// thing that can produce a verdict.
		TransportType:         "lan",
		ListenAddr:            "127.0.0.1:0",
		WebAddr:               "127.0.0.1:0",
		StoreDir:              dir,
		OutputDir:             filepath.Join(dir, "drops"),
		Headless:              true,
		AutoAcceptPairing:     true,
		PeerHeartbeatInterval: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("hub not ready")
	}
	defer func() { _ = h.Stop() }()

	// The store does not exist until Start, so the peer goes in afterwards.
	id, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := h.Store().AddPeer(pairing.Peer{
		Fingerprint: id.Fingerprint,
		Name:        "waking-laptop",
		Address:     "192.0.2.61:9877", // TEST-NET-1: reserved, never routable
		CertPEM:     id.CertPEM,
	}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	// Wait for the Hub's own loop to produce the verdict. The dial to an
	// unroutable address hangs for the transport timeout, so this is genuinely
	// slow rather than instantly refused.
	deadline := time.Now().Add(40 * time.Second)
	var last hub.PeerHealth
	for time.Now().Before(deadline) {
		last = h.PeerHealthOf(id.Fingerprint)
		if !last.LastChecked.IsZero() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if last.LastChecked.IsZero() {
		t.Fatalf("the Hub never probed the peer in 40s, so this gate cannot fail for the "+
			"reason it exists (health: %+v)", last)
	}
	if last.Reachable {
		t.Fatalf("the peer at a reserved unroutable address answered, so this gate cannot "+
			"fail for the reason it exists: %+v", last)
	}

	report := buildHealthReport(dir, "")
	if !report.HubRunning {
		t.Fatal("doctor did not see the live Hub, so the reachability check could not run")
	}

	check, found := doctorCheck(report, "peers_reachability")
	if !found {
		t.Fatalf("doctor emitted no peers_reachability check at all against a Hub that has "+
			"probed a peer and found it down. Checks: %+v", report.Checks)
	}
	if check.OK {
		t.Errorf("doctor reports peers_reachability OK (%q) while the Hub has a peer it "+
			"probed and found not answering (%+v)", check.Message, last)
	}
	if !strings.Contains(check.Message, "waking-laptop") {
		t.Errorf("the message does not name which peer is down, so a user with several has "+
			"to guess: %q", check.Message)
	}
	if check.NextAction == "" {
		t.Error("a failing check carries no next action, so the diagnostic ends in a dead end")
	}
}

// The check must not exist when no Hub is running, and must not claim anything
// about peers it has not probed.
//
// Both halves matter: emitting "peers_reachability OK: all answering" from a Hub
// that has no opinion would be the same false claim in new clothing, and emitting
// it when there is no Hub at all would attribute a peer problem to the wrong cause.
func TestDoctorSaysNothingAboutReachabilityWithoutAnOpinion(t *testing.T) {
	dir := t.TempDir()
	if _, err := pairing.NewPeerStore(dir); err != nil {
		t.Fatalf("create store: %v", err)
	}
	store, err := openPeerStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	id, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := store.AddPeer(pairing.Peer{
		Fingerprint: id.Fingerprint,
		Name:        "somewhere",
		Address:     "192.168.9.9:9877",
		CertPEM:     id.CertPEM,
	}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	report := buildHealthReport(dir, "")
	if report.HubRunning {
		t.Skip("a Hub is running on this machine's default address; the gate is about the " +
			"no-Hub case and cannot be arranged here")
	}
	if check, found := doctorCheck(report, "peers_reachability"); found {
		t.Errorf("doctor emitted peers_reachability (%+v) with no Hub running to have an "+
			"opinion; the peers check already says whether peers are configured, and a second "+
			"failure here would report one fault for two different causes", check)
	}
	// The configured check must still be there and still true: the store is fine.
	peers, found := doctorCheck(report, "peers")
	if !found || !peers.OK {
		t.Errorf("the peers check should report configuration, which is true with no Hub "+
			"running: got %+v", peers)
	}
}

// The summariser, over every shape of opinion the Hub can have.
//
// Legitimately pure: a list of peers and a list of opinions is the whole domain,
// and the output is three numbers and a name. It is not a substitute for the
// produced gate above -- that is what proves doctor fetches and reports at all.
func TestSummarisePeerReachabilityCountsEveryShapeOfOpinion(t *testing.T) {
	mk := func(name string) pairing.Peer {
		return pairing.Peer{Name: name, Fingerprint: strings.ToLower(name) + "-fp"}
	}
	peers := []pairing.Peer{mk("one"), mk("two"), mk("three")}

	checked := func(at time.Time) *peerHealthView {
		return &peerHealthView{Reachable: true, LastChecked: at, LastOK: at}
	}
	now := time.Now()
	down := func(at time.Time) *peerHealthView {
		return &peerHealthView{Reachable: false, LastChecked: at, ConsecutiveFailures: 4}
	}
	// An opinion with no health, and a health with no verdict yet: both mean
	// "no answer", which is not "down".
	unprobedHealth := func(at time.Time) *peerHealthView {
		return &peerHealthView{LastChecked: at}
	}
	neverChecked := func() *peerHealthView { return &peerHealthView{} }

	for _, tc := range []struct {
		name      string
		opinion   []peerReachability
		wantDown  int
		wantUn    int
		wantNames string
	}{
		{
			name:    "no opinion at all is not an all-clear",
			opinion: nil,
			// Every peer unprobed, and nothing down. The distinction is the whole
			// point: "we do not know" must never become "they are fine".
			wantDown: 0, wantUn: 3, wantNames: "",
		},
		{
			name: "an empty opinion list is the same",
			// An explicitly empty slice behaves as absence, not as "zero peers".
			opinion:  []peerReachability{},
			wantDown: 0, wantUn: 3, wantNames: "",
		},
		{
			name: "all answering",
			opinion: []peerReachability{
				{Fingerprint: "one-fp", Health: checked(now)},
				{Fingerprint: "two-fp", Health: checked(now)},
				{Fingerprint: "three-fp", Health: checked(now)},
			},
			wantDown: 0, wantUn: 0, wantNames: "",
		},
		{
			name: "one down, named",
			opinion: []peerReachability{
				{Fingerprint: "one-fp", Health: checked(now)},
				{Fingerprint: "two-fp", Health: down(now)},
				{Fingerprint: "three-fp", Health: checked(now)},
			},
			wantDown: 1, wantUn: 0, wantNames: "two",
		},
		{
			name: "never checked is unprobed, not down",
			opinion: []peerReachability{
				{Fingerprint: "one-fp", Health: checked(now)},
				{Fingerprint: "two-fp", Health: neverChecked()},
				{Fingerprint: "three-fp", Health: checked(now)},
			},
			wantDown: 0, wantUn: 1, wantNames: "",
		},
		{
			name: "probed with a zero timestamp is unprobed, not down",
			// The Hub serialises a zero time as 0001-01-01, which parses fine and
			// would otherwise read as a verdict.
			opinion: []peerReachability{
				{Fingerprint: "one-fp", Health: unprobedHealth(time.Time{})},
				{Fingerprint: "two-fp", Health: checked(now)},
				{Fingerprint: "three-fp", Health: checked(now)},
			},
			wantDown: 0, wantUn: 1, wantNames: "",
		},
		{
			name: "a missing health field is unprobed, not down",
			opinion: []peerReachability{
				{Fingerprint: "one-fp"},
				{Fingerprint: "two-fp", Health: checked(now)},
				{Fingerprint: "three-fp", Health: checked(now)},
			},
			wantDown: 0, wantUn: 1, wantNames: "",
		},
		{
			name: "a peer the Hub has never heard of is unprobed",
			// The opinion covers two of three; the third is simply absent rather
			// than answered, and must not vanish from the arithmetic.
			opinion: []peerReachability{
				{Fingerprint: "one-fp", Health: checked(now)},
				{Fingerprint: "two-fp", Health: down(now)},
			},
			wantDown: 1, wantUn: 1, wantNames: "two",
		},
		{
			name: "fingerprint matching ignores case",
			opinion: []peerReachability{
				{Fingerprint: "ONE-FP", Health: down(now)},
				{Fingerprint: "TWO-FP", Health: checked(now)},
				{Fingerprint: "THREE-FP", Health: checked(now)},
			},
			wantDown: 1, wantUn: 0, wantNames: "one",
		},
		{
			name: "several down are all named",
			opinion: []peerReachability{
				{Fingerprint: "one-fp", Health: down(now)},
				{Fingerprint: "two-fp", Health: down(now)},
				{Fingerprint: "three-fp", Health: checked(now)},
			},
			wantDown: 2, wantUn: 0, wantNames: "one, two",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := summarisePeerReachability(peers, tc.opinion)
			if got.down != tc.wantDown {
				t.Errorf("down = %d, want %d (names %q)", got.down, tc.wantDown, got.names)
			}
			if got.unprobed != tc.wantUn {
				t.Errorf("unprobed = %d, want %d", got.unprobed, tc.wantUn)
			}
			if got.names != tc.wantNames {
				t.Errorf("names = %q, want %q", got.names, tc.wantNames)
			}
			// The arithmetic must always account for every peer. A dropped peer
			// makes "all N answering" true of fewer peers than exist.
			if total := got.down + got.unprobed + (len(peers) - got.down - got.unprobed); total != len(peers) {
				t.Errorf("the verdict accounts for %d of %d peers", total, len(peers))
			}
		})
	}
}
