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

func medianOf(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
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

// summarize returns the median, the p95, the maximum, and the count of samples
// that read as exactly zero (the sub-clock-resolution artifact described above).
//
// The median is here so that a budget failure can be read rather than acted on:
// a regression moves the median, one stalled request moves p95 alone. See the
// longer note on reportBudget in internal/hub/perf_budget_test.go.
func summarize(samples []time.Duration) (p50, p95, max time.Duration, zeros int) {
	p50 = medianOf(samples)
	p95 = p95Of(samples)
	for _, s := range samples {
		if s == 0 {
			zeros++
		}
		if s > max {
			max = s
		}
	}
	return p50, p95, max, zeros
}

// budgetSampleCount is how many samples the CLI budget gates take.
//
// It is thirty, and the reason is arithmetic rather than taste. p95 at n=8 is
// sorted[7] -- the MAXIMUM -- so a single stalled request on a shared runner
// failed the gate, and the CI run of 2026-10-20 failed it exactly that way:
// p50 107ms, p95 884ms, max 884ms, n=8. The median says the path was fine;
// one request had stalled.
//
// At n=30 p95 is sorted[28] -- the twenty-ninth of thirty -- so one stall is
// tolerated and a tail regression (two or more slow samples) is still caught.
// The twin gate in internal/hub uses thirty for the same reason, and Batch Z83
// of the DEV-RECORD is the record of that decision.
//
// The number is a named constant rather than a literal in two places because
// the failure it causes is invisible until a runner is loaded, and a constant
// can be checked.
const budgetSampleCount = 30

// budgetSamplesMinimum is the smallest count at which p95 is not the maximum.
//
// It is derived from the same arithmetic: p95 is sorted[(n*95)/100], which is
// the maximum for every n below 20.
const budgetSamplesMinimum = 20

// TestTheBudgetSampleCountMakesP95Meaningful is a gate about a gate.
//
// It exists because the CLI budget gates failed on a shared runner in 2026-10
// and the cause was their own sample count, not the product: with eight
// samples the "95th percentile" WAS the worst sample, so one stalled request
// failed the gate. Nothing about the product changed and nothing about the
// gate announced that it was one request away from a false red.
//
// A gate whose failure means "the machine" rather than "the product" is a
// gate that trains everyone to ignore it, and the fix is to make the
// arithmetic say what it means. See Batch Z83 and Batch H of the DEV-RECORD.
func TestTheBudgetSampleCountMakesP95Meaningful(t *testing.T) {
	if budgetSampleCount < budgetSamplesMinimum {
		t.Errorf("the budget gates take %d samples, and p95 over %d samples is the %d-th "+
			"smallest -- which for fewer than %d samples is the MAXIMUM, so one stalled "+
			"request fails the gate",
			budgetSampleCount, budgetSampleCount, (budgetSampleCount*95)/100, budgetSamplesMinimum)
	}
	// And the arithmetic is the gate's own: p95 must not be the maximum.
	samples := make([]time.Duration, budgetSampleCount)
	for i := range samples {
		samples[i] = time.Duration(i) * time.Millisecond
	}
	samples[budgetSampleCount-1] = 10 * time.Second // one stall
	if p95Of(samples) == maxOf(samples) {
		t.Errorf("p95 over %d samples is the maximum, so a single stall fails the budget",
			budgetSampleCount)
	}
}

// maxOf returns the largest sample, for the gate above.
func maxOf(samples []time.Duration) time.Duration {
	var max time.Duration
	for _, s := range samples {
		if s > max {
			max = s
		}
	}
	return max
}

// TestPerfBudget_CLIStatusResponse measures the whole plain `tantu status`
// path against the plan's 500 ms local budget. No sample is discarded: the
// budget names the *first* status response, so one-time lazy initialization
// belongs to the measurement, not outside it.
//
// The sample count is budgetSampleCount, and the reason it is not eight is
// written down at that constant: at n=8 the p95 IS the maximum, so one
// stalled request on a shared runner failed this gate.
func TestPerfBudget_CLIStatusResponse(t *testing.T) {
	dir := seedPopulatedStore(t, 8)

	samples := make([]time.Duration, 0, budgetSampleCount)
	for i := 0; i < budgetSampleCount; i++ {
		start := time.Now()
		withStdoutDiscarded(t, func() {
			runStatus([]string{"--store-dir", dir})
		})
		samples = append(samples, time.Since(start))
	}

	// p50 is reported alongside p95 for the same reason as in
	// internal/hub/perf_budget_test.go, which explains it in full: a regression
	// moves the median and one stalled request moves p95 alone, and a failure
	// message carrying the median lets the reader tell those apart without the
	// gate guessing which it saw.
	p50, p95, max, zeros := summarize(samples)
	t.Logf("tantu status (local, populated store): p50=%s p95=%s max=%s budget=%s (n=%d, %d below clock resolution)",
		p50, p95, max, budgetCLIStatusResponse, len(samples), zeros)
	if p95 > budgetCLIStatusResponse {
		t.Errorf("tantu status p95 %s exceeds the %s budget (p50 %s, max %s): the first "+
			"status response must stay under 500 ms locally. A regression moves the median; "+
			"one stalled sample moves p95 alone",
			p95, budgetCLIStatusResponse, p50, max)
	}
}

// TestPerfBudget_CLIDoctorLocalWork measures buildHealthReport — runDoctor's
// shared prefix and everything the plan counts against the 2 s doctor budget
// once network probes are excluded.
func TestPerfBudget_CLIDoctorLocalWork(t *testing.T) {
	dir := seedPopulatedStore(t, 8)

	// The same count as the status gate, for the same reason: at n=8 the p95
	// is the maximum and one stalled request fails the budget.
	samples := make([]time.Duration, 0, budgetSampleCount)
	for i := 0; i < budgetSampleCount; i++ {
		start := time.Now()
		report := buildHealthReport(dir, "")
		samples = append(samples, time.Since(start))
		if report.StoreDir != dir {
			t.Fatalf("report built for %q, want %q", report.StoreDir, dir)
		}
	}

	p50, p95, max, zeros := summarize(samples)
	t.Logf("buildHealthReport (doctor local work): p50=%s p95=%s max=%s budget=%s (n=%d, %d below clock resolution)",
		p50, p95, max, budgetDoctorLocalWork, len(samples), zeros)
	if p95 > budgetDoctorLocalWork {
		t.Errorf("buildHealthReport p95 %s exceeds the %s budget (p50 %s, max %s; doctor, "+
			"excluding network probes)",
			p95, budgetDoctorLocalWork, p50, max)
	}
}
