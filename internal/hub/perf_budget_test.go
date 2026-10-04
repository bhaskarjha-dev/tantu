package hub

// Performance budgets from the plan's section 12.1 table ("User-perceived
// budgets", temp/tantu-ultimate-ux-upgrade-plan.md), enforced in CI as of
// batch Z31 — see docs/DEV-RECORD.md and KNOWN-LIMITATIONS 2.5.
//
// The numbers are the plan's, not measured-and-relaxed: measured reality on a
// loopback Hub is single-digit milliseconds, so every ceiling below has two
// orders of magnitude of headroom on a healthy path. A failure therefore means
// something structural changed — a synchronous disk or network call added to a
// request, a lock held across I/O, an unbounded scan — not that a shared CI
// runner was momentarily busy. Every test logs its measured p50/p95 so drift
// shows up in a green run long before it can become a red one.
//
// Clock caveat (measured on the dev box, Windows): time.Now() readings can be
// identical across a real sub-millisecond round trip, so some samples come out
// exactly 0. The reading does advance at refresh boundaries, and that was
// verified in both directions: a 112 µs busy loop often reads 0, while any
// span of a millisecond or more (a slow handler, an SSE stall) measures real
// elapsed time every time. The asymmetry is what makes the raw percentiles
// safe to assert on without filtering: a zero can only *lower* a percentile —
// it can never hide a slow path, because a genuinely slow path spans refreshes
// and is measured truthfully. A frozen reading certifies "faster than the
// clock can see", which is orders of magnitude inside every budget here. Both
// the zero count and the maximum are logged with each budget so the evidence
// reads honestly.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

const (
	// "Local action acknowledgement — under 250 ms": a POST that changes local
	// state (peer alias → peer-store write) acknowledged end to end.
	budgetLocalActionAck = 250 * time.Millisecond

	// "Peer-state refresh — under 2 seconds when online": a peer-store change
	// becomes observable through GET /api/status, the dashboard's own poll,
	// within the budget.
	budgetPeerStateRefresh = 2 * time.Second

	// "Dashboard first useful render — under 1 second": the server's share of
	// that budget is serving the document; the browser half is measured in
	// tools/uxtest. 250 ms of the second leaves 750 ms for parse, script and
	// first paint.
	budgetDashboardDocument = 250 * time.Millisecond

	// "Active upload progress — at least every 250 ms" / live-update cadence:
	// upload progress itself is XHR-local in the browser, but every
	// server-originated live update (the SSE stream carrying log, DROP and
	// transfer announcements) must arrive inside the same cadence number or a
	// live surface is slower than the plan permits even when the client is not.
	budgetLiveEventDelivery = 250 * time.Millisecond

	// "Event/history memory — bounded": the plan asks for a bound, not a
	// number. The RecentDrops history carries a byte budget (50 MiB default)
	// on top of its item capacity; this exercises the byte budget, which the
	// capacity-only tests cannot reach.
	recentDropsByteBudgetForTest = 4096
)

// p50AndP95 returns the median and 95th percentile of samples.
func p50AndP95(samples []time.Duration) (time.Duration, time.Duration) {
	if len(samples) == 0 {
		return 0, 0
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p50 := sorted[len(sorted)/2]
	p95 := sorted[(len(sorted)*95)/100]
	return p50, p95
}

// sampleEndpoint warms the path, then measures n round trips of method url.
// The response must be 200 on every sample: a budget measured against error
// paths is not measuring the user's path.
func sampleEndpoint(t *testing.T, token, method, url string, body []byte, n int) []time.Duration {
	t.Helper()
	one := func() time.Duration {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, url, rd)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set(IPCTokenHeader, token)
		}
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		elapsed := time.Since(start)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d, want 200 (a budget measured on an error path measures nothing)", method, url, resp.StatusCode)
		}
		return elapsed
	}
	for i := 0; i < 3; i++ {
		one()
	}
	samples := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		samples = append(samples, one())
	}
	return samples
}

func reportBudget(t *testing.T, what string, samples []time.Duration, budget time.Duration) {
	t.Helper()
	p50, p95 := p50AndP95(samples)
	var max time.Duration
	zeros := 0
	for _, s := range samples {
		if s == 0 {
			zeros++
		}
		if s > max {
			max = s
		}
	}
	t.Logf("%s: p50=%s p95=%s max=%s budget=%s (n=%d, %d below clock resolution)",
		what, p50, p95, max, budget, len(samples), zeros)
	if p95 > budget {
		t.Errorf("%s p95 %s exceeds the %s budget (max %s, p50 %s): the path itself regressed, not the machine",
			what, p95, budget, max, p50)
	}
}

// TestPerfBudget_DashboardDocumentServesWithinFirstRenderBudget pins the
// server's share of "dashboard first useful render under 1 second": the
// document itself (nonce generation, CSP, version substitution over the full
// page) must leave most of the second to the browser.
func TestPerfBudget_DashboardDocumentServesWithinFirstRenderBudget(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	samples := sampleEndpoint(t, h.ipcToken, http.MethodGet, "http://"+h.WebAddr()+"/", nil, 30)
	reportBudget(t, "GET / (dashboard document)", samples, budgetDashboardDocument)
}

// TestPerfBudget_LocalActionAcknowledgement pins "local action acknowledgement
// under 250 ms" on a real mutating action: POST /api/peers/alias writes the
// peer store to disk on every call.
func TestPerfBudget_LocalActionAcknowledgement(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	if err := h.Store().AddPeer(pairing.Peer{
		Fingerprint: "fp-perfbudget-00000001",
		Address:     "192.168.0.10:9877",
		Name:        "Laptop",
	}); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}

	body, err := json.Marshal(map[string]string{
		"fingerprint": "fp-perfbudget-00000001",
		"alias":       "Perf Budget",
	})
	if err != nil {
		t.Fatal(err)
	}
	samples := sampleEndpoint(t, h.ipcToken, http.MethodPost,
		"http://"+h.WebAddr()+"/api/peers/alias", body, 30)
	reportBudget(t, "POST /api/peers/alias (store write)", samples, budgetLocalActionAck)
}

// TestPerfBudget_PeerStateRefresh pins "peer-state refresh under 2 seconds
// when online": after the peer store changes, the dashboard's own poll
// (GET /api/status) must surface the new state inside the budget. The poll is
// what "refresh" means for this dashboard — there is no push channel for peer
// state — so this measures the real path a user watches.
func TestPerfBudget_PeerStateRefresh(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	if err := h.Store().AddPeer(pairing.Peer{
		Fingerprint: "fp-perfbudget-00000002",
		Address:     "192.168.0.11:9877",
		Name:        "Desktop",
		Alias:       "before",
	}); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}

	type statusBody struct {
		Peers []struct {
			Fingerprint string `json:"fingerprint"`
			Alias       string `json:"alias"`
		} `json:"peers"`
	}

	// Change state, then poll exactly as the dashboard does until the change
	// is visible.
	if err := h.Store().SetAlias("fp-perfbudget-00000002", "after"); err != nil {
		t.Fatalf("SetAlias failed: %v", err)
	}
	refreshStart := time.Now()
	var seen string
	deadline := refreshStart.Add(budgetPeerStateRefresh)
	for time.Now().Before(deadline) {
		resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/status")
		if err != nil {
			t.Fatalf("GET /api/status: %v", err)
		}
		var sb statusBody
		decodeErr := json.NewDecoder(resp.Body).Decode(&sb)
		resp.Body.Close()
		if decodeErr != nil {
			t.Fatalf("decode status: %v", decodeErr)
		}
		for _, p := range sb.Peers {
			if p.Fingerprint == "fp-perfbudget-00000002" {
				seen = p.Alias
			}
		}
		if seen == "after" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	elapsed := time.Since(refreshStart)
	t.Logf("peer-state refresh: store change -> visible in /api/status after %s (budget %s)",
		elapsed, budgetPeerStateRefresh)
	if seen != "after" {
		t.Fatalf("peer-state refresh: alias change never appeared through /api/status within %s (last seen %q)",
			budgetPeerStateRefresh, seen)
	}
	if elapsed > budgetPeerStateRefresh {
		t.Errorf("peer-state refresh took %s, over the %s budget", elapsed, budgetPeerStateRefresh)
	}
}

// sseEvent is what the reader goroutine hands back: the parsed event and the
// moment its data line was seen on the wire.
type sseEvent struct {
	ev   LogEvent
	seen time.Time
}

// openSSE subscribes to /api/events and returns a channel of incoming events.
// The preamble line is written after the subscription is admitted, so an event
// emitted once the preamble has been read cannot be lost to "not subscribed
// yet".
func openSSE(t *testing.T, h *Hub) (<-chan sseEvent, func()) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		resp.Body.Close()
		t.Fatalf("read SSE preamble: %v", err)
	}

	events := make(chan sseEvent, 64)
	done := make(chan struct{})
	go func() {
		defer close(events)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev LogEvent
			if json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimRight(line, "\r\n"), "data: ")), &ev) != nil {
				continue
			}
			select {
			case events <- sseEvent{ev: ev, seen: time.Now()}:
			case <-done:
				return
			}
		}
	}()
	return events, func() {
		close(done)
		_ = resp.Body.Close()
	}
}

// TestPerfBudget_LiveEventDelivery pins the hub's half of the plan's live
// update cadence: an event emitted by the hub must reach an SSE subscriber
// within 250 ms. This is the channel the dashboard uses for every
// server-originated live update (log lines, DROP announcements, transfer
// status), so a slower hub-side pipeline makes every live surface late no
// matter how fast the browser is.
func TestPerfBudget_LiveEventDelivery(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	events, closeSSE := openSSE(t, h)
	defer closeSSE()

	const n = 10
	var latencies []time.Duration
	for i := 0; i < n; i++ {
		marker := fmt.Sprintf("perf-budget-event-%d", i)
		emitted := h.EventLogger().Info(DomainSys, marker)
		select {
		case got := <-events:
			if got.ev.Message != marker {
				// Another hub event interleaved; keep reading for ours.
				i--
				continue
			}
			lat := got.seen.Sub(emitted.Timestamp)
			latencies = append(latencies, lat)
		case <-time.After(3 * time.Second):
			t.Fatalf("event %q did not arrive within 3s; the live-update path is stalled", marker)
		}
	}
	p50, p95 := p50AndP95(latencies)
	var max time.Duration
	for _, d := range latencies {
		if d > max {
			max = d
		}
	}
	t.Logf("SSE event delivery: p50=%s p95=%s max=%s budget=%s (n=%d)", p50, p95, max, budgetLiveEventDelivery, len(latencies))
	if p95 > budgetLiveEventDelivery {
		t.Errorf("SSE event delivery p95 %s exceeds the %s budget (max %s): every live surface on the dashboard is now late",
			p95, budgetLiveEventDelivery, max)
	}
}

// TestPerfBudget_RecentDropsHistoryMemoryBounded pins "event/history memory —
// bounded" on the byte budget itself: item capacity alone cannot catch a
// history whose items are each large, which is exactly how a bounded list
// becomes an unbounded RAM sink.
func TestPerfBudget_RecentDropsHistoryMemoryBounded(t *testing.T) {
	buf := NewRecentDropsBuffer(50)
	buf.maxBytes = recentDropsByteBudgetForTest

	// One pathological item, larger than the whole budget...
	buf.Add(ReceivedDropItem{
		ID:        "huge",
		Timestamp: time.Now(),
		Kind:      "text",
		Content:   strings.Repeat("x", recentDropsByteBudgetForTest*3),
		Size:      recentDropsByteBudgetForTest * 3,
	})
	// ...then many ordinary ones, whose combined size would dwarf it.
	for i := 0; i < 30; i++ {
		buf.Add(ReceivedDropItem{
			ID:        fmt.Sprintf("drop-%02d", i),
			Timestamp: time.Now(),
			Kind:      "text",
			Content:   strings.Repeat("y", 1024),
			Size:      1024,
			Name:      "note.txt",
			FromPeer:  "peer",
		})
	}

	if buf.bytes > buf.maxBytes {
		t.Errorf("history holds %d bytes, over the %d byte budget", buf.bytes, buf.maxBytes)
	}
	for _, item := range buf.GetAll() {
		if size := recentDropBytes(item); size > buf.maxBytes {
			t.Errorf("history item %q is %d bytes, over the %d byte budget: %q", item.ID, size, buf.maxBytes, item.Content[:min(40, len(item.Content))])
		}
	}
	t.Logf("history: %d items, %d bytes (budget %d)", buf.Count(), buf.bytes, buf.maxBytes)
}
