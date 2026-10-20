package main

// The dashboard-acceptance job's engine matrix, driven for real.
//
// The job used to run all seven browser sessions -- three engines, two
// topologies, and the two-hub pass -- on one runner, in one job. The session
// that failed on 2026-10-20 was the sixth: the webkit peers run, which
// reported "WebKit encountered an internal error" against every endpoint it
// touched, including the page's own URL. The job re-ran that case with a
// FRESH BROWSER on the SAME runner and it failed again, and that is the
// measurement that matters: a fresh browser not helping means the degradation
// is in the runner, not in the browser process, so the remedy is a fresh
// runner rather than a fresh browser.
//
// So the evidence now runs once per engine, on a runner that has never run a
// browser. This gate is what keeps that true. It takes the job's own shell
// out of ci.yml and runs it, one matrix leg at a time, with the harness
// replaced by a stub that records what it was asked to run.
//
// It also guards the second thing the split could have broken: the NAME of the
// check the branch requires. A matrix appends its value to a job's check
// name, so a matrix on the required job would have renamed it from
// "Dashboard acceptance (browser)" to "Dashboard acceptance (browser engine)
// (chromium)" and the branch's binding -- which names the old one -- would
// never be satisfied again. The aggregate job carries the name instead, and
// this gate executes that aggregate's shell to prove it fails when a leg
// fails.
//
// The gate is written to be able to fail. The negative cases run the checker
// against the shapes it must reject: the OLD shape -- one leg running all
// three engines -- a matrix with NO two-hub pass, and an aggregate that
// reports success whatever the legs said. A checker that accepted them would
// let the seventh session back onto the first runner without a word, or let
// the branch go green over a browser that failed.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	engineJobID   = "dashboard-acceptance-engines"
	aggregateJob  = "dashboard-acceptance"
	protectedName = "Dashboard acceptance (browser)"
	harnessStep   = "Run acceptance harness across engines"
	aggregateStep = "The browser evidence"
)

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

// extractJob returns the text of one job in the workflow.
func extractJob(t *testing.T, src, id string) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	start := -1
	for i, l := range lines {
		if l == "  "+id+":" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("ci.yml has no job %q, so the shape under test cannot be found and this test "+
			"would pass having checked nothing", id)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		// The next job key: exactly two spaces, then a name and a colon.
		if len(l) > 2 && l[:2] == "  " && l[2] != ' ' && strings.HasSuffix(strings.TrimSpace(l), ":") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// extractRunBlock returns the shell of the named step of a job.
//
// The step is found BY NAME. Picking the job's first `run: |` instead would
// have taken the Playwright install -- and the first version of this gate did
// exactly that, which is how the test came to run `npm ci` and install a
// browser in a temporary directory. A gate that reaches for the wrong block
// is worse than no gate, because it still passes.
func extractRunBlock(t *testing.T, job, stepName string) string {
	t.Helper()
	lines := strings.Split(job, "\n")
	step := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "- name: "+stepName {
			step = i
			break
		}
	}
	if step < 0 {
		t.Fatalf("the job has no step named %q, so the shell under test cannot be found and this "+
			"test would pass having checked nothing", stepName)
	}
	start := -1
	for i := step; i < len(lines); i++ {
		if lines[i] == "        run: |" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("the step %q has no `run: |` block, so the shell under test cannot be found and "+
			"this test would pass having checked nothing", stepName)
	}
	// The step's content is at ten spaces; the next step is at six.
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "      - name:") {
			end = i
			break
		}
	}
	return dedentBlock(strings.Join(lines[start+1:end], "\n"))
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

// extractEngineMatrix returns the engines the job's matrix names.
func extractEngineMatrix(job string) []string {
	i := strings.Index(job, "matrix:")
	if i < 0 {
		return nil
	}
	rest := job[i:]
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

// jobName returns the name a job's check run carries.
func jobName(job string) string {
	for _, l := range strings.Split(job, "\n") {
		if strings.HasPrefix(l, "    name: ") {
			return strings.TrimSpace(strings.TrimPrefix(l, "    name: "))
		}
	}
	return ""
}

// jobNeeds returns the ids a job needs.
func jobNeeds(job string) string {
	for _, l := range strings.Split(job, "\n") {
		if strings.HasPrefix(l, "    needs: ") {
			return strings.TrimSpace(strings.TrimPrefix(l, "    needs: "))
		}
	}
	return ""
}

// runBrowserLeg runs the engine job's shell as one matrix leg and reports the
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

// runAggregate runs the aggregate job's shell with the legs' result substituted
// and reports the status it would have ended with.
func runAggregate(t *testing.T, block, legsResult string) (int, string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is not on PATH, so the workflow's shell cannot be run: %v", err)
	}
	dir := t.TempDir()
	script := strings.ReplaceAll(block,
		"${{ needs.dashboard-acceptance-engines.result }}", legsResult) + "\n"
	cmd := exec.Command(bash, "-s")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
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
// cases below can drive it with the OLD shape and require it to be reported.
func matrixViolations(matrix []string, legs map[string][]string) []string {
	var v []string

	if len(matrix) == 0 {
		return []string{"the job declares no matrix, so every browser session runs on one runner"}
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
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	job := extractJob(t, string(workflow), engineJobID)
	matrix := extractEngineMatrix(job)
	if len(matrix) == 0 {
		t.Fatalf("the browser job declares no matrix, so every browser session runs on one " +
			"runner -- which is the shape that failed on 2026-10-20")
	}
	block := extractRunBlock(t, job, harnessStep)

	legs := map[string][]string{}
	for _, engine := range matrix {
		invocations, status, out := runBrowserLeg(t, engine, block)
		if status != 0 {
			t.Errorf("the %s leg of the dashboard-acceptance job ended with status %d, want 0:\n%s",
				engine, status, out)
		}
		legs[engine] = invocations
	}

	for _, v := range matrixViolations(matrix, legs) {
		t.Errorf("%s", v)
	}
}

// TestTheRequiredCheckNameSurvivesTheMatrix is the second claim, and it is
// the one a matrix could have broken silently.
//
// A matrix appends its value to a job's check name. The branch's required
// check names "Dashboard acceptance (browser)", and the DEV-RECORD records
// that the job was named that way in Batch Z28 for precisely this reason. If
// the matrix had been put on THAT job, the required check would have become
// "Dashboard acceptance (browser engine) (chromium)" and the branch would
// have been unsatisfiable -- a red that no push can clear.
func TestTheRequiredCheckNameSurvivesTheMatrix(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	src := string(workflow)

	aggregate := extractJob(t, src, aggregateJob)
	if got := jobName(aggregate); got != protectedName {
		t.Errorf("the aggregate job is named %q, want %q -- that is the name the branch requires, "+
			"and a matrix appends its value to a job's check name, so this name has to be carried "+
			"by a job that has no matrix", got, protectedName)
	}
	if needs := jobNeeds(aggregate); !strings.Contains(needs, engineJobID) {
		t.Errorf("the aggregate job needs %q, want it to need %q -- an aggregate that depends on "+
			"nothing aggregates nothing", needs, engineJobID)
	}
	if !strings.Contains(aggregate, "if: always()") {
		t.Errorf("the aggregate job does not run with `if: always()`, so a failed leg SKIPS it -- " +
			"and a skipped required check is not a red one, which is the failure this job exists " +
			"to prevent")
	}

	engine := extractJob(t, src, engineJobID)
	if got := jobName(engine); got == protectedName {
		t.Errorf("the job that carries the matrix is also named %q, so its check run would be "+
			"%q and the required check would be renamed out from under the branch's binding",
			got, protectedName+" (chromium)")
	}
}

// TestDashboardAcceptanceAggregateFailsWhenALegFails executes the aggregate's
// own shell. The aggregate is the check the branch requires, so what it does
// with the legs' verdict is the whole point of it.
func TestDashboardAcceptanceAggregateFailsWhenALegFails(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	job := extractJob(t, string(workflow), aggregateJob)
	block := extractRunBlock(t, job, aggregateStep)

	if status, out := runAggregate(t, block, "success"); status != 0 {
		t.Errorf("the aggregate ended with status %d when every leg passed, want 0:\n%s", status, out)
	}
	for _, result := range []string{"failure", "cancelled", "skipped"} {
		status, out := runAggregate(t, block, result)
		if status == 0 {
			t.Errorf("the aggregate ended with status 0 when a leg reported %q, so the branch "+
				"would go green over a browser that did not pass:\n%s", result, out)
		}
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
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	matrix := extractEngineMatrix(extractJob(t, string(workflow), engineJobID))
	got := append([]string(nil), matrix...)
	sort.Strings(got)
	want := []string{"chromium", "firefox", "webkit"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the matrix names %v, want %v -- a missing engine is an engine whose browser "+
			"evidence is never produced", got, want)
	}
}
