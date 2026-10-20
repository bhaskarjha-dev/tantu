package main

// The dashboard-acceptance job's engine matrix, driven for real.
//
// The job used to run all seven browser sessions -- three engines, two
// topologies, and the two-hub pass -- on one runner, in one job. The session
// that failed on 2026-10-20 was the sixth: the webkit peers run, which
// reported "WebKit encountered an internal error" against every endpoint it
// touched, including the page's own URL. The job re-ran that case with a
// FRESH BROWSER on the SAME runner and it failed again, and that is the
// measurement that matters: a fresh browser not helping means the
// degradation is in the runner, not in the browser process, so the remedy is
// a fresh runner rather than a fresh browser.
//
// So the job now runs once per engine, on a runner that has never run a
// browser. This gate is what keeps that true. It takes the step's own shell
// out of ci.yml and runs it, one matrix leg at a time, with the harness
// replaced by a stub that records what it was asked to run.
//
// The gate is written to be able to fail. The negative case at the end runs
// the checker against the OLD shape -- one leg running all three engines --
// and requires the checker to report it. A checker that accepts the old
// shape would let the seventh session back onto the first runner without
// saying anything.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const browserStepName = "      - name: Run acceptance harness across engines"

// browserStub is the harness, replaced. It records the arguments it was
// invoked with and exits 0.
//
// It replaces the FILE tools/uxtest/run.mjs rather than `node` on PATH for
// the reason tools/wfcheck/dashboard_case_test.go gives: `bash` here resolves
// to the WSL launcher on a machine that has both, a Windows directory on PATH
// is read as escapes, and the real node runs instead of the stub. Every path
// here is relative so none of them crosses the process boundary.
const browserStub = `import fs from 'fs';
fs.appendFileSync('stub-invocations', process.argv.slice(2).join(' ') + '\n');
process.exit(0);
`

// extractBrowserStep returns the step's own text and the shell under it.
func extractBrowserStep(t *testing.T) (step, block string) {
	t.Helper()
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	src := string(workflow)
	i := strings.Index(src, browserStepName)
	if i < 0 {
		t.Fatalf("ci.yml has no step named %q, so the matrix under test cannot be found "+
			"and this test would pass having checked nothing", strings.TrimSpace(browserStepName))
	}
	runMark := "\n        run: |\n"
	j := strings.Index(src[i:], runMark)
	if j < 0 {
		t.Fatalf("the dashboard-acceptance step has no `run: |` block, so the shell under test " +
			"cannot be found and this test would pass having checked nothing")
	}
	bodyStart := i + j + len(runMark)
	k := strings.Index(src[bodyStart:], "\n      - name:")
	end := len(src)
	if k >= 0 {
		end = bodyStart + k
	}
	step = src[i:end]
	block = dedentBlock(src[bodyStart:end])
	return step, block
}

// dedentBlock removes the common leading indentation from a YAML literal block.
func dedentBlock(src string) string {
	lines := strings.Split(src, "\n")
	indent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	if indent <= 0 {
		return src
	}
	for i, l := range lines {
		if len(l) >= indent {
			lines[i] = l[indent:]
		} else {
			lines[i] = strings.TrimLeft(l, " ")
		}
	}
	return strings.Join(lines, "\n")
}

// extractEngineMatrix returns the engines the step's matrix names.
func extractEngineMatrix(step string) []string {
	i := strings.Index(step, "matrix:")
	if i < 0 {
		return nil
	}
	rest := step[i:]
	j := strings.Index(rest, "engine:")
	if j < 0 {
		return nil
	}
	rest = rest[j+len("engine:"):]
	if k := strings.Index(rest, "\n"); k >= 0 {
		rest = rest[:k]
	}
	rest = strings.NewReplacer("[", "", "]", "").Replace(strings.TrimSpace(rest))
	var out []string
	for _, e := range strings.Split(rest, ",") {
		e = strings.TrimSpace(e)
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// runBrowserLeg runs the step's shell as one matrix leg and reports the
// harness invocations it made.
func runBrowserLeg(t *testing.T, engine, block string) (invocations []string, status int, output string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is not on PATH, so the workflow's shell cannot be run: %v", err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tools", "uxtest"), 0o755); err != nil {
		t.Fatalf("mkdir tools/uxtest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tools", "uxtest", "run.mjs"), []byte(browserStub), 0o644); err != nil {
		t.Fatalf("write the stub harness: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stub-invocations"), []byte(""), 0o644); err != nil {
		t.Fatalf("write the invocation log: %v", err)
	}

	// The shell is shell from a workflow, so it is delivered the way the
	// workflow's own step is: on stdin, with the working directory set to
	// where the log files are written. RUNNER_TEMP is exported here rather
	// than passed in, because the environment does not survive the crossing
	// intact and an unset RUNNER_TEMP would leave the screenshot directory
	// empty -- the block would still run, and would still prove nothing.
	script := "set -o pipefail\n" +
		"RUNNER_TEMP=rt\nexport RUNNER_TEMP\n" +
		"mkdir -p \"$RUNNER_TEMP\"\n" +
		strings.ReplaceAll(block, "${{ matrix.engine }}", engine) + "\n"

	cmd := exec.Command(bash, "-s")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	out, _ := cmd.CombinedOutput()
	output = string(out)
	status = cmd.ProcessState.ExitCode()

	raw, err := os.ReadFile(filepath.Join(dir, "stub-invocations"))
	if err != nil {
		t.Fatalf("read the invocation log: %v", err)
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(l) != "" {
			invocations = append(invocations, l)
		}
	}
	return invocations, status, output
}

// browserOf returns the engine an invocation names, and whether it was the
// two-hub pass.
func browserOf(invocation string) (engine string, twoHub bool) {
	for _, f := range strings.Fields(invocation) {
		if strings.HasPrefix(f, "--browser=") {
			engine = strings.TrimPrefix(f, "--browser=")
		}
	}
	return engine, strings.Contains(invocation, "--two-hub")
}

// matrixViolations reports everything wrong with a set of legs.
//
// It takes the legs as data rather than reading the workflow, so the negative
// case below can drive it with the OLD shape and require it to be reported.
func matrixViolations(matrix []string, legs map[string][]string) []string {
	var v []string

	if len(matrix) == 0 {
		return []string{"the step declares no matrix, so every browser session runs on one runner"}
	}
	for _, want := range []string{"chromium", "firefox", "webkit"} {
		found := false
		for _, e := range matrix {
			if e == want {
				found = true
			}
		}
		if !found {
			v = append(v, "the matrix does not name "+want+", so that engine's evidence is never produced")
		}
	}

	twoHubLegs := 0
	for _, leg := range matrix {
		invocations, ok := legs[leg]
		if !ok {
			v = append(v, "the matrix names "+leg+" but no leg ran for it")
			continue
		}
		if len(invocations) == 0 {
			v = append(v, "the "+leg+" leg ran no browser session at all")
		}
		for _, inv := range invocations {
			engine, twoHub := browserOf(inv)
			if twoHub {
				twoHubLegs++
			}
			if engine != leg {
				v = append(v, "the "+leg+" leg also ran "+engine+
					" -- that is the old shape, where one runner queues every engine's sessions "+
					"behind each other")
			}
		}
	}
	if twoHubLegs != 1 {
		v = append(v, fmt.Sprintf("the two-hub pass ran %d times across the matrix; it stands up two "+
			"extra Hubs and a real pairing, and it is meant to run once", twoHubLegs))
	}
	return v
}

// TestDashboardAcceptanceRunsOneEnginePerRunner is the claim: each engine's
// browser evidence is produced on a runner that has not already run another
// engine's browsers, and the two-hub pass still runs exactly once.
func TestDashboardAcceptanceRunsOneEnginePerRunner(t *testing.T) {
	step, block := extractBrowserStep(t)
	matrix := extractEngineMatrix(step)
	if len(matrix) == 0 {
		t.Fatalf("the dashboard-acceptance step declares no matrix, so every browser session " +
			"runs on one runner -- which is the shape that failed on 2026-10-20")
	}

	legs := map[string][]string{}
	for _, engine := range matrix {
		invocations, status, out := runBrowserLeg(t, engine, block)
		if status != 0 {
			t.Errorf("the %s leg of the dashboard-acceptance step ended with status %d, want 0:\n%s",
				engine, status, out)
		}
		legs[engine] = invocations
	}

	for _, v := range matrixViolations(matrix, legs) {
		t.Errorf("%s", v)
	}
}

// TestBrowserMatrixCheckerRejectsTheOldShape is the gate about the gate: the
// checker must report the pre-split shape, or it is not checking anything.
//
// The old shape was one leg running all three engines, twice each, plus the
// two-hub pass -- seven browser sessions on one runner, with the webkit peers
// run sixth in the queue. A checker that accepts it would let that come back
// without a word.
func TestBrowserMatrixCheckerRejectsTheOldShape(t *testing.T) {
	matrix := []string{"chromium"}
	oldShape := map[string][]string{
		"chromium": {
			"--browser=chromium",
			"--browser=firefox",
			"--browser=webkit",
			"--browser=chromium --peers",
			"--browser=firefox --peers",
			"--browser=webkit --peers",
			"--browser=chromium --peers --two-hub",
		},
	}
	violations := matrixViolations(matrix, oldShape)
	if len(violations) == 0 {
		t.Fatalf("the checker accepted the old shape -- one leg running every engine, so seven " +
			"browser sessions on one runner -- so it would not notice the split being undone")
	}
	found := false
	for _, v := range violations {
		if strings.Contains(v, "old shape") {
			found = true
		}
	}
	if !found {
		t.Errorf("the checker reported %v, but not the fact that a leg ran another engine's "+
			"sessions -- which is the specific thing being guarded against", violations)
	}
}

// TestBrowserMatrixCheckerRejectsAMissingTwoHubPass is the second negative:
// the two-hub pass is the only place the browser ever drives a peer that
// exists, so a split that lost it would leave the product's peer path
// unevidenced while every gate stayed green.
func TestBrowserMatrixCheckerRejectsAMissingTwoHubPass(t *testing.T) {
	matrix := []string{"chromium", "firefox", "webkit"}
	noTwoHub := map[string][]string{
		"chromium": {"--browser=chromium", "--browser=chromium --peers"},
		"firefox":  {"--browser=firefox", "--browser=firefox --peers"},
		"webkit":   {"--browser=webkit", "--browser=webkit --peers"},
	}
	violations := matrixViolations(matrix, noTwoHub)
	if len(violations) == 0 {
		t.Fatalf("the checker accepted a matrix in which the two-hub pass never ran, so the " +
			"product's only browser-driven real-peer path would go unevidenced")
	}
}

// TestBrowserMatrixNamesEveryEngineSorted is a small guard on the extraction
// itself: the matrix list must be read, not guessed, and a missing name must
// be reported rather than silently producing an empty matrix.
func TestBrowserMatrixNamesEveryEngineSorted(t *testing.T) {
	step, _ := extractBrowserStep(t)
	matrix := extractEngineMatrix(step)
	got := append([]string(nil), matrix...)
	sort.Strings(got)
	want := []string{"chromium", "firefox", "webkit"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the matrix names %v, want %v -- a missing engine is an engine whose browser "+
			"evidence is never produced", got, want)
	}
}
