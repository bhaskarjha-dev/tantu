package main

// The dashboard-acceptance job's exit-code handling, driven for real.
//
// The workflow decides what a run means by the exit code the harness returned:
// 0 green, 1 a product failure, 4 the Hub under test died, 5 the browser engine
// failed. The shell that turns those codes into a job outcome is the part that
// decides whether a red build means "the product broke" -- and it is shell, in
// a workflow file, which nothing executed until the first tag was pushed.
//
// So it is executed here. The block is taken out of ci.yml by its markers and
// run with the harness replaced by a stub that exits with whatever the test
// chooses. Nothing is simulated except the harness, and the block under test is
// the one that ships.
//
// The case that matters most is the last one: an engine fault on the first
// attempt and a PRODUCT failure on the retry must still fail the job. If the
// retry could turn that green, the retry would be a way to explain away a
// defect, which is the opposite of what it is for.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	caseBeginMarker = "# --- BEGIN dashboard-acceptance case ---"
	caseEndMarker   = "# --- END dashboard-acceptance case ---"
)

// extractCaseBlock returns the dashboard-acceptance exit-code block from a
// workflow, or an empty string if it is not marked.
func extractCaseBlock(src string) string {
	i := strings.Index(src, caseBeginMarker)
	j := strings.Index(src, caseEndMarker)
	if i < 0 || j < 0 || j < i {
		return ""
	}
	return src[i+len(caseBeginMarker) : j]
}

// stubHarness is the harness, replaced. It is written to the relative path the
// workflow's own block already invokes -- tools/uxtest/run.mjs -- and it exits
// with the nth code in the file "stub-codes", so a test can say "the first
// attempt reports 5 and the retry reports 0".
//
// It is a JavaScript file rather than a shell script because the block invokes
// it as `node tools/uxtest/run.mjs`, and replacing the *file* rather than the
// `node` on PATH is what keeps the test correct under the WSL launcher, which
// is what `bash` resolves to first on a machine that has both: a Windows
// directory on PATH is read as escapes, so a stub placed there is never found
// and the real node runs instead. The repository has already been bitten by
// that twice -- see pipefail_test.go.
//
// The .mjs extension means the stub is an ES module, so it uses `import`
// rather than `require`. The first version used `require` and every case
// failed with "require is not defined in ES module scope" -- which is at least
// a failure that said what was wrong.
//
// The codes come from a file rather than an environment variable for the same
// reason: the environment does not survive the crossing intact, and a stub
// that silently read nothing would exit 1 and make every case look like a
// product failure.
//
// Every path the stub and the block use is relative, so none of them crosses
// the process boundary.
const stubHarness = `import fs from 'fs';
let n = 0;
if (fs.existsSync('stub-count')) n = parseInt(fs.readFileSync('stub-count', 'utf8'), 10) || 0;
n += 1;
fs.writeFileSync('stub-count', String(n));
const codes = fs.existsSync('stub-codes') ? fs.readFileSync('stub-codes', 'utf8').trim().split(',') : [];
const code = codes[n - 1];
process.exit(code === undefined || code === '' ? 1 : parseInt(code, 10));
`

// runDashboardCase runs the workflow's own block with a stubbed harness and
// reports the job status it would have ended with.
func runDashboardCase(t *testing.T, codes string) (status int, infra int, output string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is not on PATH, so the workflow's shell cannot be run: %v", err)
	}
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	block := extractCaseBlock(string(workflow))
	if block == "" {
		t.Fatalf("ci.yml does not carry the %q markers, so the block under test cannot be "+
			"found and this test would pass having checked nothing", caseBeginMarker)
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tools", "uxtest"), 0o755); err != nil {
		t.Fatalf("mkdir tools/uxtest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tools", "uxtest", "run.mjs"), []byte(stubHarness), 0o644); err != nil {
		t.Fatalf("write the stub harness: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stub-codes"), []byte(codes), 0o644); err != nil {
		t.Fatalf("write the stub codes: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "shots"), 0o755); err != nil {
		t.Fatalf("mkdir shots: %v", err)
	}

	// The block is shell from a workflow, so it is delivered the way the
	// workflow's own step is: on stdin, with the working directory set to
	// where the log files are written.
	script := "set -o pipefail\n" +
		"status=0\ninfra=0\n" +
		"SHOTDIR=shots\nengine=chromium\nmode=\"\"\nlabel=chromium\nslug=chromium\n" +
		block + "\n" +
		"printf 'FINAL_STATUS=%s\\nFINAL_INFRA=%s\\n' \"$status\" \"$infra\"\n"

	cmd := exec.Command(bash, "-s")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	out, runErr := cmd.CombinedOutput()
	output = string(out)
	if runErr != nil {
		t.Fatalf("the workflow's block did not run: %v\n%s", runErr, output)
	}
	status, infra = -1, -1
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "FINAL_STATUS="):
			fmt.Sscanf(line, "FINAL_STATUS=%d", &status)
		case strings.HasPrefix(line, "FINAL_INFRA="):
			fmt.Sscanf(line, "FINAL_INFRA=%d", &infra)
		}
	}
	if status < 0 {
		t.Fatalf("the block did not report a final status\n%s", output)
	}
	return status, infra, output
}

// TestDashboardAcceptanceCaseDecidesTheJobOutcome drives the workflow's own
// shell with every exit code the harness can return.
func TestDashboardAcceptanceCaseDecidesTheJobOutcome(t *testing.T) {
	for _, tc := range []struct {
		name       string
		codes      string
		wantStatus int
		wantInfra  int
		why        string
	}{
		{
			name: "a green run", codes: "0", wantStatus: 0, wantInfra: 0,
			why: "a passing harness must leave the job green",
		},
		{
			name: "a product failure", codes: "1", wantStatus: 1, wantInfra: 0,
			why: "a product defect must fail the job",
		},
		{
			name: "the Hub died", codes: "4", wantStatus: 4, wantInfra: 1,
			why: "the Hub exiting is not a product result, and it must be marked as one",
		},
		{
			name: "an engine fault, then a green retry", codes: "5,0", wantStatus: 0, wantInfra: 0,
			why: "an engine fault does not reproduce, and the retry is what distinguishes it " +
				"from a defect -- so the job must be able to come back green",
		},
		{
			name: "an engine fault, then the same engine fault", codes: "5,5", wantStatus: 5, wantInfra: 0,
			why: "an engine fault that reproduces is not an intermittent browser fault, so it fails",
		},
		{
			name: "an engine fault, then a product failure", codes: "5,1", wantStatus: 1, wantInfra: 0,
			why: "THE SAFETY PROPERTY: the retry must not become a way to explain away a " +
				"product defect. A defect that reproduces on the retry must fail the job",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, infra, out := runDashboardCase(t, tc.codes)
			if status != tc.wantStatus {
				t.Errorf("the job would have ended with status %d (infra %d), want %d: %s\n%s",
					status, infra, tc.wantStatus, tc.why, out)
			}
			if infra != tc.wantInfra {
				t.Errorf("the job marked the run as infrastructure=%d, want %d: %s\n%s",
					infra, tc.wantInfra, tc.why, out)
			}
		})
	}
}

// TestDashboardAcceptanceCaseRetriesOnlyOnAnEngineFault is the narrower
// claim: the retry must not fire for any other exit code, because a retry
// that fires for a product failure would be a way to explain it away.
func TestDashboardAcceptanceCaseRetriesOnlyOnAnEngineFault(t *testing.T) {
	for _, codes := range []string{"0", "1", "4"} {
		_, _, out := runDashboardCase(t, codes)
		if strings.Contains(out, "Re-running this case once") {
			t.Errorf("the job re-ran a case whose harness reported %q, which is not an engine "+
				"fault; the retry exists for the browser and for nothing else\n%s", codes, out)
		}
	}
	_, _, out := runDashboardCase(t, "5,0")
	if !strings.Contains(out, "Re-running this case once") {
		t.Errorf("an engine fault did not re-run the case, so an intermittent browser fault "+
			"still costs a red build\n%s", out)
	}
}
