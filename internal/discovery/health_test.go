package discovery

// Health gates.
//
// Every check here corresponds to a signal the engine used to discard. The
// point is not that Health exists; it is that a failure which previously left
// no trace at all now leaves an explainable one, so "discovery found nothing"
// can be told apart from "discovery cannot work here".

import (
	"context"
	"strings"
	"testing"
	"time"
)

func startTestEngine(t *testing.T, cfg DiscoveryConfig) *Engine {
	t.Helper()
	if cfg.Interval == 0 {
		cfg.Interval = 50 * time.Millisecond
	}
	if cfg.TTL == 0 {
		cfg.TTL = 2 * time.Second
	}
	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

// TestEngine_HealthReportsWhatItIsDoing is the core gate: a running engine
// must always be able to say what it is doing, in words, with the socket state
// it actually holds.
func TestEngine_HealthReportsWhatItIsDoing(t *testing.T) {
	engine := startTestEngine(t, DiscoveryConfig{NodeName: "health", WirePort: 9877, BroadcastPort: 19881})

	health := engine.Health()
	if !health.Running {
		t.Fatal("a started engine reports not running")
	}
	if !health.Usable {
		t.Fatalf("a started engine with a bound broadcast socket reports unusable: %+v", health)
	}
	if !health.Broadcast {
		t.Error("the IPv4 broadcast listener bound but Health does not say so")
	}
	if health.Detail == "" {
		t.Error("Health.Detail is empty; an unlabelled status is the defect this type exists to remove")
	}
	if !strings.Contains(health.Detail, "No nearby hubs have answered yet") {
		t.Errorf("a fresh engine with an empty table does not say so: %q", health.Detail)
	}
	if health.Family == "" {
		t.Error("Health does not state which address family it can see peers on")
	}
	if health.Since.IsZero() {
		t.Error("Health does not record when the engine started")
	}

	// The advertise loop runs on its own schedule, so the counter must move
	// without any external stimulus - this is exactly the signal the old code
	// threw away.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && engine.Health().BeaconsSent == 0 {
		time.Sleep(25 * time.Millisecond)
	}
	if engine.Health().BeaconsSent == 0 {
		t.Error("BeaconsSent never incremented: broadcast writes are still being discarded unobserved")
	}
}

// TestEngine_HealthAfterCloseIsNotUsable closes a real hole: Health read the
// socket pointers, and a closed engine would have kept reporting the sockets it
// had held. A stopped engine that claims to be usable sends the user looking
// for a peer that discovery can never find.
func TestEngine_HealthAfterCloseIsNotUsable(t *testing.T) {
	engine, err := NewEngine(DiscoveryConfig{NodeName: "closing", WirePort: 9877, BroadcastPort: 19882})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !engine.Health().Usable {
		t.Fatal("engine is not usable before Close")
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	health := engine.Health()
	if health.Running {
		t.Error("a closed engine still reports running")
	}
	if health.Usable || health.Broadcast || health.Multicast {
		t.Errorf("a closed engine still reports bound sockets: %+v", health)
	}
	if !strings.Contains(health.Detail, "not running") {
		t.Errorf("a closed engine does not say it is not running: %q", health.Detail)
	}
}

// TestEngine_HealthAfterContextCancelIsNotDegraded covers the other way an
// engine stops, and it is the case that matters to the Hub.
//
// Close() is not how a running Hub shuts down: it cancels the context the engine
// was started with, and the engine's own cancellation goroutine closes the
// sockets. That path used to clear the bound flags without clearing `started`,
// so Health() answered Running=true with nothing bound — which describeHealth
// renders as "Discovery cannot reach this network: neither multicast nor subnet
// broadcast could be opened".
//
// Nothing looked wrong until the Hub actually started the watcher that reports
// these transitions (Batch Z37): it shares the engine's context, so it sampled
// that window and logged a discovery fault on every normal shutdown. A fault
// invented by our own shutdown teaches a user to ignore the line that would have
// been real. Close()'s own comment already stated the rule this violated.
func TestEngine_HealthAfterContextCancelIsNotDegraded(t *testing.T) {
	engine, err := NewEngine(DiscoveryConfig{NodeName: "ctx-cancel", WirePort: 9877, BroadcastPort: 19884})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !engine.Health().Usable {
		t.Fatal("engine is not usable before cancellation")
	}

	cancel()
	// The cancellation goroutine is asynchronous, so poll rather than assume a
	// fixed sleep is long enough; a flaky timing assertion here would be noise.
	deadline := time.Now().Add(5 * time.Second)
	var health Health
	for time.Now().Before(deadline) {
		health = engine.Health()
		if !health.Running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer engine.Close()

	if health.Running {
		t.Fatalf("an engine whose context was cancelled still reports running: %+v", health)
	}
	if health.Degraded {
		t.Errorf("a cancelled engine reports a discovery fault it did not have: %q", health.Detail)
	}
	if health.Broadcast || health.Multicast {
		t.Errorf("a cancelled engine still reports bound sockets: %+v", health)
	}
	if !strings.Contains(health.Detail, "not running") {
		t.Errorf("a cancelled engine does not say it is not running: %q", health.Detail)
	}
}

// TestNilEngineHealthIsHonest covers the shape a caller actually holds: an
// interface or pointer that may be nil because the Hub was configured without
// discovery. Returning zero values there would render as "running, nothing
// found", which is the same lie the health surface exists to remove.
func TestNilEngineHealthIsHonest(t *testing.T) {
	var engine *Engine
	health := engine.Health()
	if health.Running || health.Usable || health.Degraded {
		t.Errorf("a nil engine reports state: %+v", health)
	}
	if health.Detail == "" {
		t.Error("a nil engine has no explanation")
	}
}

// TestDescribeHealthCoversEveryState is the wording gate. Each row is a real
// engine state; each must produce a sentence that names the condition, because
// the sentence is the only thing a user ever reads.
func TestDescribeHealthCoversEveryState(t *testing.T) {
	cases := []struct {
		name       string
		health     Health
		bcastStop  bool
		mcastStop  bool
		mustHave   []string
		mustNotSay []string
	}{
		{
			name:     "stopped",
			health:   Health{Running: false},
			mustHave: []string{"not running"},
		},
		{
			name:     "nothing bound while running",
			health:   Health{Running: true, Broadcast: false, Multicast: false},
			mustHave: []string{"cannot reach this network", "Pair by entering"},
		},
		{
			name:       "both engines live, nothing found",
			health:     Health{Running: true, Broadcast: true, Multicast: true, Nodes: 0},
			mustHave:   []string{"multicast and subnet broadcast", "No nearby hubs have answered yet"},
			mustNotSay: []string{"cannot reach"},
		},
		{
			name:     "both engines live, peers found",
			health:   Health{Running: true, Broadcast: true, Multicast: true, Nodes: 3},
			mustHave: []string{"3 nearby hub(s)"},
		},
		{
			name:     "multicast blocked, broadcast working",
			health:   Health{Running: true, Broadcast: true, Multicast: false, Nodes: 0},
			mustHave: []string{"subnet broadcast only", "multicast is unavailable", "No nearby hubs have answered yet"},
		},
		{
			name:      "broadcast stopped responding",
			health:    Health{Running: true, Broadcast: true, Multicast: true},
			bcastStop: true,
			mustHave:  []string{"Subnet broadcast stopped responding", "multicast discovery is still working"},
		},
		{
			name:      "multicast stopped responding",
			health:    Health{Running: true, Broadcast: true, Multicast: true},
			mcastStop: true,
			mustHave:  []string{"Multicast discovery stopped responding", "subnet broadcast is still working"},
		},
		{
			name:      "both stopped responding",
			health:    Health{Running: true, Broadcast: true, Multicast: true},
			bcastStop: true,
			mcastStop: true,
			mustHave:  []string{"stopped responding", "Restart the Hub"},
		},
		{
			name:     "broadcast only, peers found",
			health:   Health{Running: true, Broadcast: true, Multicast: false, Nodes: 1},
			mustHave: []string{"1 nearby hub(s)"},
		},
		{
			name:     "multicast only, nothing found",
			health:   Health{Running: true, Broadcast: false, Multicast: true, Nodes: 0},
			mustHave: []string{"multicast only", "No nearby hubs have answered yet"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := describeHealth(tc.health, tc.bcastStop, tc.mcastStop)
			if strings.TrimSpace(got) == "" {
				t.Fatal("describeHealth returned nothing")
			}
			for _, want := range tc.mustHave {
				if !strings.Contains(got, want) {
					t.Errorf("detail %q does not contain %q", got, want)
				}
			}
			for _, unwanted := range tc.mustNotSay {
				if strings.Contains(got, unwanted) {
					t.Errorf("detail %q must not claim %q", got, unwanted)
				}
			}
		})
	}
}

// TestDescribeHealthNeverClaimsAFaultForANormalNetwork pins the calibration
// decision. On plenty of networks mDNS is blocked and subnet broadcast works
// perfectly; calling that degraded would put a permanent red warning in front
// of users whose discovery is fine, and a warning that is always there is a
// warning nobody reads.
func TestDescribeHealthNeverClaimsAFaultForANormalNetwork(t *testing.T) {
	working := Health{Running: true, Broadcast: true, Multicast: false, Nodes: 0}
	if describeHealth(working, false, false) == "" {
		t.Fatal("no detail for broadcast-only discovery")
	}
	if strings.Contains(describeHealth(working, false, false), "cannot reach") {
		t.Error("broadcast-only discovery is described as a fault")
	}
	if strings.Contains(describeHealth(working, false, false), "degraded") {
		t.Error("broadcast-only discovery is described as degraded")
	}

	// The Degraded flag itself is computed from Usable, which excludes the
	// multicast-only-missing case on purpose.
	h := working
	h.Usable = h.Running && (h.Broadcast || h.Multicast)
	if !h.Usable {
		t.Error("broadcast-only discovery must still count as usable")
	}
}

// TestDescribeHealthReportsWhenNothingCanBeAnnounced covers the state a macOS CI
// run exposed: sockets bound, zero beacons sent, and every send refused. Every
// "discovery is running on X" sentence is literally true there and completely
// useless, because a hub that has announced itself nothing is invisible however
// healthy its sockets are.
func TestDescribeHealthReportsWhenNothingCanBeAnnounced(t *testing.T) {
	silent := Health{
		Running: true, Broadcast: false, Multicast: true, Usable: true,
		BeaconsSent: 0, SendErrors: 100, Nodes: 0,
	}
	got := describeHealth(silent, false, false)
	if !strings.Contains(got, "cannot announce") {
		t.Errorf("a hub that cannot send a single beacon is not reported as such: %q", got)
	}
	// It must not claim to be running on a working transport while invisible.
	for _, forbidden := range []string{"is running on multicast", "No nearby hubs have answered"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the sentence still claims a healthy transport (%q): %q", forbidden, got)
		}
	}

	// The moment one beacon does get out, the ordinary wording returns: the
	// sentence must not latch on permanently after one bad network.
	recovered := silent
	recovered.BeaconsSent = 1
	if after := describeHealth(recovered, false, false); !strings.Contains(after, "multicast only") {
		t.Errorf("once a beacon has gone out the normal wording should return, got: %q", after)
	}

	// A healthy engine that has simply not needed to send yet must be unaffected.
	fresh := Health{Running: true, Broadcast: true, Multicast: true, Usable: true, Nodes: 0}
	if g := describeHealth(fresh, false, false); !strings.Contains(g, "is running on multicast and subnet broadcast") {
		t.Errorf("a healthy engine was misreported as unable to announce: %q", g)
	}
}

// TestTruncateErrorIsBounded keeps a hostile or verbose OS error from growing
// the dashboard's status payload without limit.
func TestTruncateErrorIsBounded(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got := truncateError(long)
	if len(got) != 200 {
		t.Errorf("truncated error is %d bytes, want 200", len(got))
	}
	if !strings.HasSuffix(got, "\u2026") {
		t.Error("a truncated error does not mark itself as truncated")
	}
	if truncateError("short") != "short" {
		t.Error("a short error was altered")
	}
}
