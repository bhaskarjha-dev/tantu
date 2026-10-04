package main

// Performance budgets from the plan's section 12.1 table ("User-perceived
// budgets"), enforced in CI as of batch Z31 — see docs/DEV-RECORD.md and
// KNOWN-LIMITATIONS 2.5.
//
//   - "CLI first status response — under 500 ms locally"
//   - "`tantu doctor` completion — under 2 seconds excluding network probes"
//
// Both budgets are gated here on the code those commands actually run, in
// process, against a populated store (identity plus peers), because the
// commands themselves call os.Exit on the degraded paths a test store can
// take. `runStatus` returns normally on success and is timed whole — flag
// parsing, hub probe, identity load, per-peer SAS derivation and output —
// which is the real "first status response". `runDoctor`'s shared prefix is
// buildHealthReport, and the plan's doctor budget explicitly excludes network
// probes, which is exactly what that function's local work is.
//
// Zero-valued samples are a known clock artifact on the dev box (identical
// time.Now() readings across sub-millisecond work); they are kept, never
// dropped, because a zero can only lower a percentile — any real regression
// spans a clock refresh and measures truthfully. See the longer note in
// internal/hub/perf_budget_test.go.

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

const (
	// Plan §12.1: "CLI first status response — under 500 ms locally".
	budgetCLIStatusResponse = 500 * time.Millisecond
	// Plan §12.1: "`tantu doctor` completion — under 2 seconds excluding
	// network probes". The local work shared by both commands is bounded by
	// the tighter 500 ms status budget below; this is asserted against the
	// doctor envelope so the gate states both plan numbers explicitly rather
	// than leaving one implied.
	budgetDoctorLocalWork = 2 * time.Second
)

// seedPopulatedStore writes the store a real user has: an ECDSA identity and
// several trusted peers, so identity parsing and per-peer SAS derivation are
// inside the measurement instead of skipped.
func seedPopulatedStore(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	store, err := pairing.NewPeerStore(dir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	id, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := store.SaveIdentity(id); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	for i := 0; i < n; i++ {
		fp := fmt.Sprintf("fp-perf%040d", i)
		if err := store.AddPeer(pairing.Peer{
			Fingerprint: fp,
			Name:        fmt.Sprintf("machine-%02d", i),
			Address:     fmt.Sprintf("192.168.0.%d:9877", 10+i),
			FirstSeen:   time.Now().Add(-time.Duration(i) * time.Hour),
			LastSeen:    time.Now(),
		}); err != nil {
			t.Fatalf("AddPeer %d: %v", i, err)
		}
	}
	return dir
}

// withStdoutDiscarded runs fn with os.Stdout pointed at a temp file, so the
// status command's real printing is exercised without writing the test
// output. A temp file rather than a pipe: a pipe whose reader is not draining
// can block the writer once the buffer fills.
func withStdoutDiscarded(t *testing.T, fn func()) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "cli-stdout-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	orig := os.Stdout
	os.Stdout = f
	defer func() {
		os.Stdout = orig
		_ = f.Close()
	}()
	fn()
}

func p95Of(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[(len(sorted)*95)/100]
}

// summarize returns the p95, the maximum, and the count of samples that read
// as exactly zero (the sub-clock-resolution artifact described above).
func summarize(samples []time.Duration) (p95, max time.Duration, zeros int) {
	p95 = p95Of(samples)
	for _, s := range samples {
		if s == 0 {
			zeros++
		}
		if s > max {
			max = s
		}
	}
	return p95, max, zeros
}

// TestPerfBudget_CLIStatusResponse measures the whole plain `tantu status`
// path against the plan's 500 ms local budget. No sample is discarded: the
// budget names the *first* status response, so one-time lazy initialization
// belongs to the measurement, not outside it.
func TestPerfBudget_CLIStatusResponse(t *testing.T) {
	dir := seedPopulatedStore(t, 8)

	samples := make([]time.Duration, 0, 8)
	for i := 0; i < 8; i++ {
		start := time.Now()
		withStdoutDiscarded(t, func() {
			runStatus([]string{"--store-dir", dir})
		})
		samples = append(samples, time.Since(start))
	}

	p95, max, zeros := summarize(samples)
	t.Logf("tantu status (local, populated store): p95=%s max=%s budget=%s (n=%d, %d below clock resolution)",
		p95, max, budgetCLIStatusResponse, len(samples), zeros)
	if p95 > budgetCLIStatusResponse {
		t.Errorf("tantu status p95 %s exceeds the %s budget (max %s): first status response must stay under 500 ms locally",
			p95, budgetCLIStatusResponse, max)
	}
}

// TestPerfBudget_CLIDoctorLocalWork measures buildHealthReport — runDoctor's
// shared prefix and everything the plan counts against the 2 s doctor budget
// once network probes are excluded.
func TestPerfBudget_CLIDoctorLocalWork(t *testing.T) {
	dir := seedPopulatedStore(t, 8)

	samples := make([]time.Duration, 0, 8)
	for i := 0; i < 8; i++ {
		start := time.Now()
		report := buildHealthReport(dir, "")
		samples = append(samples, time.Since(start))
		if report.StoreDir != dir {
			t.Fatalf("report built for %q, want %q", report.StoreDir, dir)
		}
	}

	p95, max, zeros := summarize(samples)
	t.Logf("buildHealthReport (doctor local work): p95=%s max=%s budget=%s (n=%d, %d below clock resolution)",
		p95, max, budgetDoctorLocalWork, len(samples), zeros)
	if p95 > budgetDoctorLocalWork {
		t.Errorf("buildHealthReport p95 %s exceeds the %s budget (max %s; doctor, excluding network probes)",
			p95, budgetDoctorLocalWork, max)
	}
}
