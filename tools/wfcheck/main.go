// Command wfcheck parses every shell block in the repository's GitHub Actions
// workflows and checks each one with `bash -n`.
//
// Why it exists: release.yml has never executed. It runs for the first time when
// a tag is pushed, and nothing in CI inspects it, so a shell syntax error in its
// signing step would be found at the moment of release -- the worst available
// time. The CI workflow itself grew five shell steps recently, and each of them
// was found broken by a real runner rather than by anything checking it first.
//
// `bash -n` parses without executing, which is the only safe thing to do here:
// these steps install binaries, push tags and sign artifacts.
//
// A missing bash is reported and causes a non-zero exit rather than a silent
// pass. This gate exists because gates that quietly stop running are worse than
// gates that are absent.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// runBlock matches a `run: |` key and captures its indentation.
var runBlock = regexp.MustCompile(`^(\s*)run: \|\s*$`)

// leadingSpaces returns the count of leading space characters, treating tabs as
// one column each. Workflows in this repository are space-indented; a tab would
// be a line-ending problem, which encgate already refuses to let in.
func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' && r != '\t' {
			break
		}
		n++
	}
	return n
}

// blocks extracts every `run: |` block, returning the source and the line the
// key was on. A block ends at the first line indented no further than its key.
func blocks(src string) []struct {
	line int
	body string
} {
	var out []struct {
		line int
		body string
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		m := runBlock.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		indent := len(m[1])
		var body []string
		for j := i + 1; j < len(lines); j++ {
			ln := lines[j]
			if strings.TrimSpace(ln) == "" {
				body = append(body, "")
				continue
			}
			if leadingSpaces(ln) <= indent {
				break
			}
			// Drop the block's own indentation plus one level of YAML's
			// mandatory extra indent, so `bash -n` sees the script as written.
			body = append(body, ln[min(indent+2, len(ln)):])
		}
		out = append(out, struct {
			line int
			body string
		}{line: i + 1, body: strings.Join(body, "\n")})
	}
	return out
}

// jobKeys are the keys a job may carry. Anything else sitting directly under a
// job name is a mis-indented job, not a field of one.
var jobKeys = map[string]bool{
	"name": true, "runs-on": true, "steps": true, "needs": true, "if": true,
	"permissions": true, "strategy": true, "services": true, "env": true,
	"outputs": true, "defaults": true, "timeout-minutes": true,
	"continue-on-error": true, "container": true, "uses": true, "with": true,
	"secrets": true, "concurrency": true,
}

// mapKey returns the key of a 'key:' or 'key: value' line.
func mapKey(ln string) (string, bool) {
	s := strings.TrimSpace(ln)
	if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "- ") {
		return "", false
	}
	i := strings.Index(s, ":")
	if i <= 0 {
		return "", false
	}
	k := strings.TrimSpace(s[:i])
	if k == "" || strings.ContainsAny(k, " \t") {
		return "", false
	}
	return k, true
}

// checkJobShape reports keys that sit directly under a job but are not job
// keys.
//
// Why this exists: on 2026-10-13 a job was added at four spaces when every
// other job sits at two. That is not cosmetic. The new key stopped being a job
// and became a field inside the preceding one, so the workflow failed to load
// entirely and GitHub recorded a failed run with zero jobs.
//
// Nothing local caught it, and both gates that plausibly should have were
// reasoning about something else. bash -n only ever sees shell blocks, and
// every one of those was valid. A YAML parser would not have helped either: a
// nested key under a job is well-formed YAML, it is simply not a workflow.
//
// So the check is semantic rather than syntactic -- a job's children have to
// look like a job's children -- which is the only form that separates this from
// a file that is merely valid.
func checkJobShape(src string) []string {
	var problems []string
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")

	jobsAt := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "jobs:") {
			jobsAt = i
			break
		}
	}
	if jobsAt < 0 {
		return []string{"no top-level jobs: key"}
	}

	// Job names are the keys at jobs+2. Fix the indent from the first one.
	jobIndent := -1
	for i := jobsAt + 1; i < len(lines); i++ {
		ln := lines[i]
		if strings.TrimSpace(ln) == "" || strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		ind := leadingSpaces(ln)
		if ind == 0 {
			break
		}
		if jobIndent < 0 {
			jobIndent = ind
			continue
		}
		// Job names sit at jobIndent and are skipped by construction. Their
		// children sit at jobIndent+2, and that is exactly where a mis-indented
		// job lands -- the ambiguity the key-name test below exists to resolve.
		if ind == jobIndent+2 {
			k, ok := mapKey(ln)
			if ok && !jobKeys[k] {
				problems = append(problems, fmt.Sprintf(
					"line %d: %q sits directly under a job at %d spaces but is not a job key "+
						"(allowed: name, runs-on, steps, needs, if, permissions, strategy, services, env, "+
						"outputs, defaults, timeout-minutes, continue-on-error, container, uses, with, secrets, "+
						"concurrency). A job written at the wrong indentation parses as YAML and runs nothing",
					i+1, k, ind))
			}
		}
	}
	return problems
}

// teePipeline matches a pipeline that captures a command's output with tee, which
// in a CI workflow is always done so the output can be read afterwards.
var teePipeline = regexp.MustCompile(`\|\s*tee\b`)

// setPipefail matches a line that actually turns pipefail on, rather than one
// that merely mentions it.
//
// The first version of this check asked whether the block's text contained the
// word "pipefail". It passed a block whose pipefail line had been deleted,
// because the block's own comment explained why pipefail was needed and that
// explanation contains the word. A gate satisfied by a comment about a safety
// measure, with nothing checking the measure, is the failure this repository has
// now hit repeatedly -- in a watcher nobody called and in a document nobody
// verified. The red proof caught it, which is the only reason it is not shipped.
var setPipefail = regexp.MustCompile(`(?m)^[ \t]*set[ \t]+(-[A-Za-z]+[ \t]+)*pipefail\b`)

// checkTeePipelinesPreserveExitStatus reports any block that captures output
// with tee without enabling pipefail.
//
// The reason this gate exists is not hypothetical. In a bash pipeline the exit
// status is that of the *last* command, so:
//
//	node tools/uxtest/run.mjs ... | tee harness.log || run_status=$?
//
// captures tee's status, which is 0 whether the harness passed, found a product
// regression, or died with its own "not a product result" code 4. Every case
// statement downstream of that capture is unreachable, and the step reports
// success.
//
// It was reached in practice rather than merely reasoned about: the dashboard
// acceptance job stayed green through a period in which the harness threw a
// ReferenceError on every single run and exited 1. The job that carries the
// product's only browser evidence could not fail.
//
// The rule is deliberately blunt -- any tee pipeline needs pipefail -- because
// the alternative is a rule that reasons about which variable is read where, and
// such a rule is one edit away from being wrong in the permissive direction.
func checkTeePipelinesPreserveExitStatus(src string) []string {
	var problems []string
	for _, b := range blocks(src) {
		if !teePipeline.MatchString(b.body) {
			continue
		}
		if setPipefail.MatchString(b.body) {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"line %d: this block pipes to `tee` but never runs `set -o pipefail`. A pipeline's "+
				"exit status is that of its last command, so anything reading `$?` after one reads "+
				"tee's status, which is always 0. Every gate result derived from it is a false green. "+
				"A comment explaining why pipefail matters does not turn it on.",
			b.line))
	}
	return problems
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	bash, err := exec.LookPath("bash")
	if err != nil {
		// Refusing to report success when the tool that does the work is absent.
		fmt.Fprintln(os.Stderr, "wfcheck: bash is not on PATH, so no workflow block was checked. "+
			"This gate must not pass without running.")
		os.Exit(2)
	}

	files, err := filepath.Glob(filepath.Join(".github", "workflows", "*.yml"))
	if err != nil || len(files) == 0 {
		fmt.Fprintln(os.Stderr, "wfcheck: no workflows found under .github/workflows")
		os.Exit(1)
	}
	sort.Strings(files)

	tmp, err := os.MkdirTemp("", "wfcheck")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wfcheck: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	total, failed := 0, 0
	for _, wf := range files {
		raw, err := os.ReadFile(wf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wfcheck: read %s: %v\n", wf, err)
			failed++
			continue
		}
		src := string(raw)
		if problems := checkTeePipelinesPreserveExitStatus(src); len(problems) > 0 {
			for _, p := range problems {
				fmt.Printf("FAIL %s: %s\n", wf, p)
			}
			failed++
			continue
		}
		if problems := checkJobShape(src); len(problems) > 0 {
			for _, p := range problems {
				fmt.Printf("FAIL %s: %s\n", wf, p)
			}
			failed++
			continue
		}
		bs := blocks(src)
		// Cross-check the parse against the file. Without it, a regex change
		// turns this into a no-op that reports success having inspected nothing,
		// which is exactly how an earlier version of a similar check failed.
		want := strings.Count(src, "run: |")
		if len(bs) != want {
			fmt.Fprintf(os.Stderr, "wfcheck: %s has %d `run: |` keys but %d were parsed; "+
				"the checker is broken, not the workflow\n", wf, want, len(bs))
			failed++
			continue
		}
		for _, b := range bs {
			total++
			f, err := os.CreateTemp(tmp, "block-*.sh")
			if err != nil {
				fmt.Fprintf(os.Stderr, "wfcheck: %v\n", err)
				os.Exit(1)
			}
			_, _ = f.WriteString(b.body)
			_ = f.Close()
			cmd := exec.Command(bash, "-n", f.Name())
			out, err := cmd.CombinedOutput()
			if err != nil {
				failed++
				fmt.Printf("FAIL %s:%d does not parse as bash\n%s\n", wf, b.line, out)
				continue
			}
		}
		fmt.Printf("wfcheck: %s -- %d shell block(s) parse\n", wf, len(bs))
	}

	if failed > 0 {
		fmt.Fprintf(os.Stderr, "wfcheck: %d block(s) failed\n", failed)
		os.Exit(1)
	}
	fmt.Printf("wfcheck: %d shell block(s) across %d workflow(s) parse cleanly\n", total, len(files))
}
