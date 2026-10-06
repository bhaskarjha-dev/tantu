package hub

// Hub-side gates for discovery health (limitation 3.13).
//
// The defect these close is not a crash; it is silence. Before this, every
// discovery failure signal was discarded inside the engine, so the Hub had
// nothing to log and the dashboard had nothing to show, and "discovery found
// nothing" was indistinguishable from "discovery cannot work here". A gate that
// only checked Health() would still pass if the Hub never surfaced it, so the
// checks below go all the way to the wire and the log.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/discovery"
)

// TestWebDashboard_StatusExposesDiscoveryHealth proves the explanation reaches
// the product. A health value computed and then dropped is the same silence in
// a new place.
func TestWebDashboard_StatusExposesDiscoveryHealth(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// The loopback rig starts no discovery engine, which is the configuration
	// the "not running is not a fault" rule exists for. A second case injects a
	// real started engine so the running path is exercised too - otherwise this
	// gate could pass with a handler that only ever reports a stopped engine.
	engine, err := discovery.NewEngine(discovery.DiscoveryConfig{NodeName: "status-health", WirePort: 9877, BroadcastPort: 19883})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("Start discovery: %v", err)
	}
	defer engine.Close()
	h.SetDiscoveryEngine(engine)

	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	raw, ok := body["discovery"]
	if !ok {
		t.Fatalf("/api/status carries no discovery block: %v", body)
	}
	health, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("discovery block is not an object: %T", raw)
	}
	// The fields a caller needs in order to say something useful, all present
	// even when discovery was never started (this rig runs loopback-only).
	for _, field := range []string{"running", "usable", "degraded", "detail", "family"} {
		if _, present := health[field]; !present {
			t.Errorf("discovery block has no %q field: %v", field, health)
		}
	}
	detail, _ := health["detail"].(string)
	if strings.TrimSpace(detail) == "" {
		t.Error("discovery detail is empty; the page would render a status line with nothing in it")
	}

	// An engine that cannot see anything must not be reported as healthy, and
	// the running state must survive to the wire.
	if running, _ := health["running"].(bool); !running {
		t.Errorf("a started engine reports running=false through /api/status: %v", health)
	}
	if usable, _ := health["usable"].(bool); !usable {
		t.Errorf("a started engine with a bound broadcast socket reports usable=false: %v", health)
	}
	if degraded, _ := health["degraded"].(bool); degraded {
		t.Errorf("a working engine reports degraded: %v", health)
	}

	// The JSON must not leak an internal path or a peer's identity: discovery
	// health is metadata only, like every other status field.
	for _, banned := range []string{"cert", "fingerprint", "sas", "peers.json"} {
		if _, present := health[banned]; present {
			t.Errorf("discovery block carries %q, which is not a health field", banned)
		}
	}
}

// TestDiscoveryHealthEventNamesEveryTransition is the wording gate. Each case
// is a real transition a running Hub can hit; each must produce exactly one
// actionable line, and the steady states must produce none at all.
func TestDiscoveryHealthEventNamesEveryTransition(t *testing.T) {
	healthy := discovery.Health{Running: true, Broadcast: true, Multicast: true, Usable: true, Nodes: 1, Detail: "running"}
	degraded := discovery.Health{Running: true, Broadcast: false, Multicast: false, Degraded: true, Detail: "Discovery cannot reach this network: neither multicast nor subnet broadcast could be opened."}
	blocked := discovery.Health{Running: true, Broadcast: true, Multicast: true, Nodes: 0, SendErrors: 12, LastError: "beacon to 255.255.255.255:9879: permission denied", Detail: "running"}
	stopped := discovery.Health{Running: false, Detail: "Discovery is not running on this Hub."}

	cases := []struct {
		name     string
		prev     discoveryHealthState
		cur      discovery.Health
		wantLog  bool
		contains []string
		absent   []string
	}{
		{
			name: "first healthy sample is silent", prev: discoveryHealthState{}, cur: healthy,
			wantLog: false,
		},
		{
			name: "first degraded sample warns with the fallback", prev: discoveryHealthState{}, cur: degraded,
			wantLog: true, contains: []string{"Discovery degraded", "tantu pair --peer"},
		},
		{
			name: "healthy then degraded warns", prev: discoveryHealthState{degraded: false, observed: true}, cur: degraded,
			wantLog: true, contains: []string{"Discovery degraded", "tantu pair --peer"},
			// This is the case the feature exists for. The Hub starts healthy,
			// then loses discovery while it runs: a firewall rule changes, a
			// laptop joins a different network, a VPN starts eating broadcast.
			// Requiring !prev.observed meant only breakage present before the
			// very first ten-second sample was ever reported, so the transition
			// that actually happens on a live Hub said nothing.
		},
		{
			name: "degraded twice logs once", prev: discoveryHealthState{degraded: true, observed: true}, cur: degraded,
			wantLog: false,
			// The rationale: a warning repeated every ten seconds is a log the
			// user learns to scroll past.
		},
		{
			name: "recovery is announced", prev: discoveryHealthState{degraded: true, observed: true}, cur: healthy,
			wantLog: true, contains: []string{"Discovery recovered"},
		},
		{
			name: "healthy afterwards is silent", prev: discoveryHealthState{degraded: false, observed: true}, cur: healthy,
			wantLog: false,
		},
		{
			name: "first sample with failing beacons warns", prev: discoveryHealthState{}, cur: blocked,
			wantLog: true, contains: []string{"beacons are not reaching this network", "12 beacon send(s) failed", "permission denied"},
		},
		{
			name: "repeated send failures do not repeat the warning",
			prev: discoveryHealthState{degraded: false, observed: true}, cur: blocked,
			wantLog: false,
		},
		{
			name: "a stopped engine is not a fault", prev: discoveryHealthState{}, cur: stopped,
			wantLog: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, next := discoveryHealthEvent(tc.prev, tc.cur)
			if !tc.wantLog {
				if event != nil {
					t.Fatalf("expected no log line, got %q", event.Message)
				}
				return
			}
			if event == nil {
				t.Fatal("expected a log line, got none")
			}
			for _, want := range tc.contains {
				if !strings.Contains(event.Message, want) {
					t.Errorf("line %q does not contain %q", event.Message, want)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(event.Message, unwanted) {
					t.Errorf("line %q must not contain %q", event.Message, unwanted)
				}
			}
			if next.observed != true {
				t.Error("the returned state does not record that a sample was observed")
			}
			if next.degraded != tc.cur.Degraded {
				t.Errorf("the returned state records degraded=%v for a sample with degraded=%v", next.degraded, tc.cur.Degraded)
			}
		})
	}
}

// fakeHealthSource is a discoveryHealthSource whose reported state the test
// controls. It exists because a real Engine can only be driven into the healthy
// states from a test: every fault the watcher cares about needs a socket that
// failed to bind in a specific way, which is not something a unit test can ask
// the operating system for.
type fakeHealthSource struct {
	mu sync.Mutex
	h  discovery.Health
}

func (f *fakeHealthSource) Health() discovery.Health {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.h
}

func (f *fakeHealthSource) set(h discovery.Health) {
	f.mu.Lock()
	f.h = h
	f.mu.Unlock()
}

// waitForLogLines polls the real activity log until n lines contain substr, and
// fails the test if that never happens. Polling rather than sleeping a fixed
// time keeps the gate fast on a healthy machine without becoming flaky on a
// loaded one.
func waitForLogLines(t *testing.T, h *Hub, substr string, n int) []LogEvent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var got []LogEvent
	for time.Now().Before(deadline) {
		got = messagesMatching(readActivityLog(t, h), substr)
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waited 10s for %d log line(s) containing %q, saw %d: %s",
		n, substr, len(got), summarizeMessages(got))
	return nil
}

// TestWatchDiscoveryHealthLogsTransitionsOverTime drives the watcher loop
// itself, rather than the pure decision function it calls.
//
// The reason this gate exists: watchDiscoveryHealth was never started by
// anything, and every gate on this file still passed, because each of them
// tested discoveryHealthEvent - a function that is perfectly correct whether or
// not any goroutine ever calls it. Testing the decision and testing the thing
// that runs it are different claims, and only the second one caught nothing
// here. This one exercises the ticker, the state threading between samples, the
// log call and the cancellation path, and reads the result out of the same
// /api/logs the dashboard reads.
func TestWatchDiscoveryHealthLogsTransitionsOverTime(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	healthy := discovery.Health{Running: true, Broadcast: true, Multicast: true, Usable: true, Nodes: 1, Detail: "running"}
	degraded := discovery.Health{Running: true, Broadcast: false, Multicast: false, Degraded: true, Detail: "Discovery cannot reach this network: neither multicast nor subnet broadcast could be opened."}

	// Starts healthy, as a real Hub does, so the transition this asserts is the
	// one that happens on a live machine rather than one present before the
	// first tick.
	src := &fakeHealthSource{h: healthy}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const interval = 5 * time.Millisecond
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		h.watchDiscoveryHealthEvery(ctx, src, interval)
	}()

	// A healthy engine is not news: the first sample must say nothing, or every
	// Hub start would warn about nothing.
	time.Sleep(20 * interval)
	if got := messagesMatching(readActivityLog(t, h), "Discovery"); len(got) != 0 {
		t.Fatalf("a healthy discovery engine produced %d log line(s): %s", len(got), summarizeMessages(got))
	}

	// The transition into the fault is announced, with the fallback named.
	src.set(degraded)
	warns := waitForLogLines(t, h, "Discovery degraded", 1)
	if warns[0].Level != LevelWarn {
		t.Errorf("degraded transition logged at %v, want %v", warns[0].Level, LevelWarn)
	}
	if !strings.Contains(warns[0].Message, "tantu pair --peer") {
		t.Errorf("the degraded line does not name the pairing fallback: %q", warns[0].Message)
	}

	// And it is announced once. Twenty further samples of a persistent fault
	// must not produce twenty more lines.
	time.Sleep(20 * interval)
	if got := messagesMatching(readActivityLog(t, h), "Discovery degraded"); len(got) != 1 {
		t.Errorf("a persistent fault logged %d times, want 1: %s", len(got), summarizeMessages(got))
	}

	// Recovery is announced too, so a user who gave up on discovery by address
	// learns that it works again.
	src.set(healthy)
	recovered := waitForLogLines(t, h, "Discovery recovered", 1)
	if recovered[0].Level != LevelInfo {
		t.Errorf("recovery logged at %v, want %v", recovered[0].Level, LevelInfo)
	}

	// A watcher that outlived the Hub would keep sampling and could announce a
	// recovery that never happened, so cancellation has to actually stop it.
	cancel()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not return after its context was cancelled")
	}
}

// TestWatchDiscoveryHealthEveryRejectsANonPositiveInterval stops a test helper
// from becoming a busy loop that starves the rest of the suite.
func TestWatchDiscoveryHealthEveryRejectsANonPositiveInterval(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	src := &fakeHealthSource{h: discovery.Health{Running: false}}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		h.watchDiscoveryHealthEvery(ctx, src, 0)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("a non-positive interval fell back to the production interval instead of being ignored")
	}
}

// TestDescribeDiscoverySendFailureInventsNothing pins the honesty rule. UDP
// gives no delivery guarantee, so the product may say that writes errored and
// may quote the OS error; it may not say why.
func TestDescribeDiscoverySendFailureInventsNothing(t *testing.T) {
	plain := describeDiscoverySendFailure(discovery.Health{SendErrors: 3})
	if !strings.Contains(plain, "3 beacon send(s) failed") {
		t.Errorf("count is missing from %q", plain)
	}
	// The error suffix is the only " (" in the message; "send(s)" has no
	// leading space, so this distinguishes the two without guessing.
	if strings.Contains(plain, " (") {
		t.Errorf("a message with no recorded error quoted one: %q", plain)
	}

	withErr := describeDiscoverySendFailure(discovery.Health{SendErrors: 3, LastError: "network is unreachable"})
	if !strings.Contains(withErr, "network is unreachable") {
		t.Errorf("the recorded error is missing from %q", withErr)
	}
	for _, forbidden := range []string{"firewall", "VPN", "proxy"} {
		if strings.Contains(plain, forbidden) || strings.Contains(withErr, forbidden) {
			t.Errorf("the explanation guessed a cause (%q) it cannot know", forbidden)
		}
	}
}

// TestWebDashboard_StatusReportsDiscoveryAbsentWhenUnconfigured keeps the
// omitempty contract honest. A Hub that was never given a discovery engine must
// omit the block rather than publish a zeroed one that reads as "running,
// nothing found".
func TestWebDashboard_StatusReportsDiscoveryAbsentWhenUnconfigured(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if block, present := body["discovery"]; present {
		t.Errorf("/api/status invents a discovery block with no engine configured: %v", block)
	}
}

// TestWebDashboard_RendersDiscoveryHealth is the page-side gate: the status has
// to be rendered, or the server-side work reaches no user.
func TestWebDashboard_RendersDiscoveryHealth(t *testing.T) {
	for _, want := range []string{
		`id="discoveryHealth"`,
		"function renderDiscoveryHealth(",
		"renderDiscoveryHealth(data.discovery)",
		"Discovery unavailable",
		"Discovery blocked",
		"Discovery listening",
		"Recheck",
		// Not running is not a fault and must not paint a warning.
		"card.style.display = 'none';",
	} {
		if !strings.Contains(dashboardHTML, want) {
			t.Errorf("dashboard does not render discovery health: missing %q", want)
		}
	}
	// The status text is peer-independent, but the engine's last error is an
	// OS string; it must be assigned through a property or textContent, never
	// interpolated into markup.
	if strings.Contains(dashboardHTML, "text.innerHTML") {
		t.Error("discovery health is rendered with innerHTML")
	}
}
