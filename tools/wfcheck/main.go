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
