// Command encgate fails when a tracked text file carries UTF-8 double-encoding
// damage.
//
// The damage this catches came from an editing session that round-tripped files
// through CP1252, re-encoding UTF-8 bytes as Latin-1 on every write. The compiler
// never noticed: the result is still syntactically valid Go, because the damage
// landed in log-message emoji, comments and documentation. Every build and every
// test passed, for nine commits, until someone read the output.
//
// Two checks, because one pattern is not sufficient:
//
//   - C1 control characters. Nothing in this project's source legitimately
//     contains U+0080-U+009F, so a single occurrence is proof of damage. This is
//     the check that generalises: it caught damage whose mangled characters had no
//     U+00C2/C3/E2 lead byte and therefore matched no mojibake signature.
//   - Mojibake signatures. The lead-byte-plus-high-byte shapes a CP1252
//     mis-decode produces.
//
// Invalid UTF-8 fails on its own, before either count is meaningful.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// textExtensions are the tracked file types that carry source or documentation.
// Anything else is skipped by extension, so no binary is ever read as text.
var textExtensions = map[string]bool{
	".go": true, ".md": true, ".yml": true, ".yaml": true,
	".html": true, ".css": true, ".js": true, ".json": true, ".txt": true,
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// listFiles returns the files to check: every tracked text file of the current
// repository, or every matching file beneath the directories given as arguments.
func listFiles(args []string) ([]string, error) {
	if len(args) == 0 {
		out, err := exec.Command("git", "ls-files").Output()
		if err != nil {
			return nil, fmt.Errorf("git ls-files: %w", err)
		}
		var files []string
		for _, f := range strings.Split(string(out), "\n") {
			if f = strings.TrimSpace(f); f != "" {
				files = append(files, f)
			}
		}
		return files, nil
	}
	var files []string
	for _, root := range args {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func run(args []string) error {
	// With an argument, check the files under that directory. Without one, ask git
	// for the tracked files of the current repository, so untracked scratch files
	// and build output cannot fail the gate. The argument exists so the gate can
	// be tested against known-damaged content rather than only trusted because it
	// has never been seen to fail.
	files, err := listFiles(args)
	if err != nil {
		return err
	}
	var bad []string
	for _, f := range files {
		if !textExtensions[strings.ToLower(filepath.Ext(f))] {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		if problem := inspect(f, data); problem != "" {
			bad = append(bad, problem)
		}
	}
	if len(bad) == 0 {
		fmt.Println("encoding gate: all tracked text files are clean UTF-8 with no encoding damage")
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("UTF-8 encoding damage found.\n"+
		"Do NOT fix this with 'gofmt -w', a PowerShell Get-Content/Set-Content\n"+
		"round trip, or any editor with an ambiguous default encoding - running one of\n"+
		"those is what caused it. Use an editor that preserves UTF-8, or a tool that\n"+
		"reads and writes bytes.\n\n%s", strings.Join(bad, "\n"))
}

func inspect(name string, data []byte) string {
	if !utf8.Valid(data) {
		line, col := firstInvalid(data)
		return fmt.Sprintf("%s: not valid UTF-8 (first bad byte at line %d, column %d)", name, line, col)
	}
	s := string(data)
	c1, moji := countC1(s), countMojibake(s)
	switch {
	case c1 > 0 && moji > 0:
		return fmt.Sprintf("%s: %d C1 control characters (U+0080-U+009F) and %d mojibake signatures - CP1252 round-trip damage", name, c1, moji)
	case c1 > 0:
		return fmt.Sprintf("%s: %d C1 control characters (U+0080-U+009F) - CP1252 round-trip damage", name, c1)
	case moji > 0:
		return fmt.Sprintf("%s: %d mojibake signatures - CP1252 round-trip damage", name, moji)
	}
	return ""
}

func firstInvalid(data []byte) (line, col int) {
	line, col = 1, 1
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size <= 1 {
			return line, col
		}
		if r == '\n' {
			line++
			col = 1
		} else {
			col++
		}
		i += size
	}
	return line, col
}

// countC1 counts C1 control characters. No clean source file in this project
// contains one, so any occurrence is damage rather than a style choice.
func countC1(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x80 && r <= 0x9F {
			n++
		}
	}
	return n
}

// countMojibake counts the classic CP1252 mis-decode shapes: a UTF-8 lead byte
// immediately followed by a byte in the range only CP1252 uses.
func countMojibake(s string) int {
	rs := []rune(s)
	n := 0
	for i := 0; i+1 < len(rs); i++ {
		switch rs[i] {
		case 0xC2, 0xC3, 0xE2:
			if rs[i+1] >= 0x80 && rs[i+1] <= 0xBF {
				n++
			}
		}
	}
	return n
}
