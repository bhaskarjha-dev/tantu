package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Self-tests for the "a gate must be able to fail" check.
//
// This one is different from the others in this file and from the rest of the
// repository's gates. The others assert on the checker's own decision about some
// text. This one asserts on **bash's** behaviour, because the defect it guards is
// bash's behaviour: the checker cannot be shown to understand `pipefail` by
// reading a rule about `pipefail`. It has to be shown that a block it accepts
// really does surface a failure.
//
// The defect it guards was reached in practice. The dashboard acceptance job and
// two test steps all read `$?` after a `| tee` pipeline. That reads tee's status,
// which is 0. Every downstream gate result was therefore a false green, and the
// dashboard job stayed green through a period in which the harness was exiting 1
// on every single run.

// workflowWithHarnessStep renders the shape a real workflow uses: the step has a
// name, and `run: |` is on its own line. The `- run: |` one-liner looks
// equivalent and is not: blocks() matches `run: |` only at the start of a line,
// and a fixture that does not exercise the real shape tests nothing.
func workflowWithHarnessStep(body string) string {
	return `jobs:
  acceptance:
    steps:
      - name: Harness
        run: |
` + body
}

func TestTeePipelineCheckAcceptsABlockThatSetsPipefail(t *testing.T) {
	src := workflowWithHarnessStep(`          set -o pipefail
          node run.mjs 2>&1 | tee log.txt || status=$?
          exit "$status"
`)
	if problems := checkTeePipelinesPreserveExitStatus(src); len(problems) > 0 {
		t.Fatalf("a block that sets pipefail was reported: %v", problems)
	}
}

// A comment that explains why pipefail matters must not satisfy the check. The
// first version of this rule asked whether the block mentioned "pipefail" and
// therefore passed a block whose `set -o pipefail` line had been deleted -- the
// comment alone kept it green. That is a comment standing in for the thing it
// describes, which is the failure this repository keeps meeting.
func TestTeePipelineCheckIsNotSatisfiedByACommentAboutPipefail(t *testing.T) {
	src := workflowWithHarnessStep(`          # pipefail matters here: a pipeline's status is its last
          # command's, so without it this block would report success for a
          # red run.
          status=0
          node run.mjs 2>&1 | tee log.txt || status=$?
          exit "$status"
`)
	problems := checkTeePipelinesPreserveExitStatus(src)
	if len(problems) == 0 {
		t.Fatal("a block that only *mentions* pipefail in a comment was accepted; the comment " +
			"describes the setting rather than turning it on")
	}
	if !strings.Contains(problems[0], "does not turn it on") {
		t.Errorf("the problem does not explain the comment trap: %q", problems[0])
	}
}

// A combined form must be accepted too: `set -euo pipefail` turns it on just as
// well as `set -o pipefail`, and a rule that only matched one spelling would
// push people toward the shorter form for no reason.
func TestTeePipelineCheckAcceptsACombinedSetForm(t *testing.T) {
	src := workflowWithHarnessStep(`          set -euo pipefail
          node run.mjs 2>&1 | tee log.txt || status=$?
          exit "$status"
`)
	if problems := checkTeePipelinesPreserveExitStatus(src); len(problems) > 0 {
		t.Fatalf("`set -euo pipefail` was not recognised: %v", problems)
	}
}

func TestTeePipelineCheckRejectsABlockThatDoesNot(t *testing.T) {
	src := workflowWithHarnessStep(`          status=0
          node run.mjs 2>&1 | tee log.txt || status=$?
          exit "$status"
`)
	problems := checkTeePipelinesPreserveExitStatus(src)
	if len(problems) == 0 {
		t.Fatal("a block that pipes to tee without pipefail was accepted; this is the exact " +
			"shape that made a failing harness report as success")
	}
	if !strings.Contains(problems[0], "pipefail") {
		t.Errorf("the problem does not say what to do about it: %q", problems[0])
	}
}

func TestTeePipelineCheckIgnoresBlocksWithNoTee(t *testing.T) {
	src := `jobs:
  build:
    steps:
      - name: build
        run: |
          go build ./...
      - name: test
        run: |
          go test ./... 2>&1 | tail -40
`
	if problems := checkTeePipelinesPreserveExitStatus(src); len(problems) > 0 {
		t.Errorf("blocks without a tee pipeline were reported: %v", problems)
	}
}

// The load-bearing one: run bash and confirm the rule matches reality in both
// directions. A text rule can agree with a wrong model forever; this cannot.
func TestTeePipelineRuleMatchesWhatBashActuallyDoes(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is not on PATH, so bash's own behaviour cannot be checked: %v", err)
	}
	dir := t.TempDir()
	child := filepath.Join(dir, childName)
	if err := os.WriteFile(child, []byte("exit \"${1:-0}\"\n"), 0o644); err != nil {
		t.Fatalf("write child: %v", err)
	}

	for _, tc := range []struct {
		name      string
		pipefail  bool
		wantAbove int
		wantBelow int
	}{
		{name: "without pipefail", pipefail: false, wantAbove: 0, wantBelow: 0},
		{name: "with pipefail", pipefail: true, wantAbove: 1, wantBelow: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := runProbe(t, bash, dir, child, 1, tc.pipefail)
			if status > tc.wantAbove {
				t.Errorf("a harness exiting 1 was reported as status %d; the check's premise is "+
					"that a pipe without pipefail reports %d", status, tc.wantAbove)
			}
			pass := runProbe(t, bash, dir, child, 0, tc.pipefail)
			if pass != tc.wantBelow {
				t.Errorf("a passing harness reported status %d, want %d", pass, tc.wantBelow)
			}
		})
	}
}

// runProbe reproduces the ci.yml construct exactly -- pre-set the status, pipe to
// tee, capture on failure -- and reports what the script saw.
//
// The script is delivered on stdin and the child is named relatively, with the
// working directory set to where it was written. Nothing crosses the process
// boundary as an absolute path.
//
// That is not tidiness; it is the third version of this function, and each of
// the first two was wrong in a way that produced a *wrong* result rather than an
// error.
//
//   - The first passed a native path into bash. Under Git Bash that works. Under
//     the WSL launcher in C:\Windows\System32\bash.exe -- which is what `bash`
//     resolves to first on a machine that has both -- the backslashes are read
//     as escapes, the child is never found, and the probe measures 127 instead
//     of the child's exit code. 127 is swallowed by tee exactly as a real
//     failure is, so the "without pipefail" case agreed with the right answer
//     for entirely the wrong reason, and the "with pipefail" case failed on a
//     green product.
//   - The second fixed the child's path and left the script's own path native,
//     so the same substitution simply moved up one argument.
//
// Relative names plus a working directory are the one form that is correct
// under every bash a developer might have installed. The child is also invoked
// with `bash` rather than executed, so the probe does not depend on the
// executable bit surviving the filesystem.
func runProbe(t *testing.T, bash, dir, child string, code int, pipefail bool) int {
	t.Helper()
	script := "status=0\n"
	if pipefail {
		script += "set -o pipefail\n"
	}
	script += "bash " + childName + " " + itoa(code) +
		" | tee /dev/null || status=$?\necho \"$status\"\n"

	cmd := exec.Command(bash, "-s")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run probe with bash=%s: %v\n%s", bash, err, out)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		t.Fatalf("probe produced no output:\n%s", out)
	}
	// The status is the last whitespace-separated token, and it must be a
	// number. A token like "C:Users...probe.sh" or a bash error means the probe
	// never reached its echo; reporting that as a status would be the first
	// version's failure again, so it is refused rather than returned.
	last := fields[len(fields)-1]
	status, ok := parseStatus(last)
	if !ok {
		t.Fatalf("probe's last line was %q, not a status, so it never reached `echo \"$status\"`:\n%s",
			last, out)
	}
	return status
}

// childName is the probe child's file name, referenced relatively so that no
// absolute path is ever passed to bash.
const childName = "exitwith.sh"

// parseStatus reads the exit status the probe echoed. It reports false rather
// than 0 for anything that is not a bare non-negative integer, so a probe that
// failed for an unrelated reason can never be mistaken for a passing one.
func parseStatus(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
