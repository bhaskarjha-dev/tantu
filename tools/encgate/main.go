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
	"unicode"
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
	c1, moji, fmtChars := countC1(s), countMojibake(s), countFormatChars(s)
	var parts []string
	if c1 > 0 {
		parts = append(parts, fmt.Sprintf("%d C1 control characters (U+0080-U+009F)", c1))
	}
	if moji > 0 {
		parts = append(parts, fmt.Sprintf("%d mojibake signatures", moji))
	}
	if fmtChars > 0 {
		parts = append(parts, fmt.Sprintf("%d invisible format characters", fmtChars))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("%s: %s - CP1252 round-trip damage", name, strings.Join(parts, " and "))
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

// invisibleFormatChars are the Unicode format and invisible characters that a
// source file has no legitimate use for. They are listed explicitly rather
// than derived from the general Cf category so that adding a legitimate one
// later is a deliberate, reviewable edit instead of a silent gate change.
var invisibleFormatChars = map[rune]string{
	0x00AD: "soft hyphen",
	0x200B: "zero-width space",
	0x200C: "zero-width non-joiner",
	0x200D: "zero-width joiner",
	0x200E: "left-to-right mark",
	0x200F: "right-to-left mark",
	0x2060: "word joiner",
	0xFEFF: "byte-order mark",
}

// countFormatChars counts invisible formatting characters. Their presence in
// source is never intentional, and they are a reliable marker: the soft hyphen
// in particular is the signature of a lossy transcode, and it is invisible in
// an editor, in a diff, and in a terminal - which is exactly why the damage it
// marks went unnoticed from the very first commit of this repository.
func countFormatChars(s string) int {
	n := 0
	for _, r := range s {
		if _, bad := invisibleFormatChars[r]; bad {
			n++
		}
	}
	return n
}

// cp1252High is every character a Windows-1252 mis-decode produces for the
// byte range 0x80-0x9F. This set is the whole reason the original check missed
// damage: after a lead character such as U+00E2 came a code point from the
// 0x2000 block, not a low one, so a "next byte in 0x80-0xBF" test never fired.
var cp1252High = map[rune]bool{}

// cp1252Byte is the reverse mapping: character -> the byte a mis-decode
// consumed. Built alongside cp1252High so both directions come from one
// statement of the code page.
var cp1252Byte = map[rune]byte{}

// buildCP1252High is a table rather than an inline literal so the mapping is
// stated once and stays reviewable against the Windows-1252 code page.
func init() {
	// Written as escapes on purpose. These are the exact characters the gate
	// exists to detect, so a literal here would be both fragile in review and
	// a self-inflicted instance of the damage being guarded against.
	for b, r := range map[byte]rune{
		0x80: '€', 0x82: '‚', 0x83: 'ƒ', 0x84: '„',
		0x85: '…', 0x86: '†', 0x87: '‡', 0x88: 'ˆ',
		0x89: '‰', 0x8A: 'Š', 0x8B: '‹', 0x8C: 'Œ',
		0x8E: 'Ž', 0x91: '‘', 0x92: '’', 0x93: '“',
		0x94: '”', 0x95: '•', 0x96: '–', 0x97: '—',
		0x98: '˜', 0x99: '™', 0x9A: 'š', 0x9B: '›',
		0x9C: 'œ', 0x9E: 'ž', 0x9F: 'Ÿ',
	} {
		cp1252High[r] = true
		cp1252Byte[r] = b
	}
}

// isLatinLead reports whether r is one of the U+00C0..U+00FF characters that a
// UTF-8 mis-decode leaves behind. × and ÷ are excluded: they are real
// mathematics, and the Windows-1252 code page does not remap their bytes.
func isLatinLead(r rune) bool {
	return r >= 0xC0 && r <= 0xFF && r != '×' && r != '÷'
}

// misdecodes reports whether a run of text is the result of decoding UTF-8
// bytes as CP1252, and does so by construction rather than by guessing.
//
// The question a shape heuristic keeps getting wrong is "could this be real
// text?". A letter with an acute accent followed by an em dash is a perfectly
// good sentence, and no character pair distinguishes it from the damage those
// same three characters produce when they arrive from a CP1252 mis-decode. So
// the test does not guess: it reverses the operation. Each run is recoded back to the
// bytes a CP1252 mis-decode would have consumed, and the run counts as damage
// only if those bytes are valid UTF-8 *and* decode to something different from
// what is actually in the file. Legitimate text either fails to recode or
// recodes to itself, so it is never counted; real damage always recodes to a
// shorter, different, valid UTF-8 string, because that is literally what
// happened to it.
//
// This is why the em dash, curly quotes, and emoji the shape heuristic missed
// are now caught: their reverse round trip is exact, and no clean text can
// produce one.
func misdecodes(run string) bool {
	if len(run) < 2 {
		return false
	}
	recoded, ok := recodeCP1252(run)
	if !ok {
		return false
	}
	if !utf8.Valid(recoded) {
		return false
	}
	return string(recoded) != run
}

// recodeCP1252 maps each character back to the single CP1252 byte a mis-decode
// would have produced. It fails if any character is not CP1252-representable,
// which is exactly the property that keeps real text out: an intact em dash
// (U+2014) is a CP1252 byte 0x97, but an intact Devanagari vowel or an emoji
// has no CP1252 byte at all and aborts the attempt.
func recodeCP1252(run string) ([]byte, bool) {
	out := make([]byte, 0, len(run))
	for _, r := range run {
		switch {
		case r < 0x80:
			out = append(out, byte(r))
		case r >= 0xA0 && r <= 0xFF:
			// Not remapped by the CP1252 code page; identity.
			out = append(out, byte(r))
		case cp1252High[r]:
			out = append(out, cp1252Byte[r])
		default:
			// U+0080-U+009F and anything else with no CP1252 byte.
			return nil, false
		}
	}
	return out, true
}

// countMojibake counts mis-decoded runs, where a run is a maximal span of
// characters that all recode to CP1252 bytes and start with a Latin lead.
// Splitting on lead characters keeps the comparison local, so one damaged
// region in a long line cannot be masked by legitimate text elsewhere in it.
func countMojibake(s string) int {
	rs := []rune(s)
	n := 0
	for i := 0; i < len(rs); i++ {
		if !isLatinLead(rs[i]) {
			continue
		}
		// Grow the run: the lead plus every following character that still
		// recodes to a CP1252 byte. A real sequence is 2-3 characters wide.
		j := i
		for j < len(rs) {
			if _, ok := recodeCP1252(string(rs[j : j+1])); !ok {
				break
			}
			j++
		}
		if j-i >= 2 && misdecodes(string(rs[i:j])) {
			n++
			i = j - 1
		}
	}
	return n + countMojibakeRuns(s)
}

// isHighLatin reports whether r is one of the U+00A0-U+00FF characters a
// mis-decode leaves behind. The range is wider than isLatinLead because the
// lead byte of the damage is not always above U+00C0.
//
// Non-letters are excluded deliberately. Real punctuation in this range is
// common and legitimate - the multiplication and division signs, the section
// and paragraph marks, the middle dot - and two of them sitting side by side
// must not read as a word-shaped accident.
func isHighLatin(r rune) bool {
	return r >= 0xA0 && r <= 0xFF && unicode.IsLetter(r)
}

// countMojibakeRuns catches the one damage shape that leaves no reversible
// trace, and it is the shape this repository actually shipped from its very
// first commit.
//
// Some mis-decoded sequences cannot be reversed: recoding them to bytes yields
// something that is not valid UTF-8, so no amount of reasoning can recover the
// original. A bare cluster of high-Latin characters in front of a log message
// is exactly that. It was invisible in review, invisible in a terminal, and
// invisible to every other check here.
//
// What makes it detectable without guessing is that it cannot be a word. Every
// legitimate accented character in this project's prose is a letter inside a
// word, bounded by ASCII letters or punctuation. Mojibake runs instead appear
// as a bare cluster: two or more high-Latin characters that are not part of a
// word, i.e. not immediately preceded by a letter and not immediately followed
// by one. The allowlist is therefore the whole Latin-1 range minus a
// self-validating test, not a hand-maintained list of characters someone
// remembered to include.
func countMojibakeRuns(s string) int {
	rs := []rune(s)
	n := 0
	for i := 0; i < len(rs); i++ {
		if !isHighLatin(rs[i]) {
			continue
		}
		// Grow the maximal run of high-Latin characters.
		j := i
		for j < len(rs) && isHighLatin(rs[j]) {
			j++
		}
		runLen := j - i
		if runLen < 2 {
			i = j - 1
			continue
		}
		// Part of a word on either side means it is real text.
		beforeIsLetter := i > 0 && isWordRune(rs[i-1])
		afterIsLetter := j < len(rs) && isWordRune(rs[j])
		if !beforeIsLetter && !afterIsLetter {
			n++
		}
		i = j - 1
	}
	return n
}

// isWordRune reports whether r continues a word, which is what distinguishes
// "café" (real) from a bare cluster of Latin-1 characters (damage).
func isWordRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r > 0x7F && (unicode.IsLetter(r) || unicode.IsDigit(r)):
		// An accented letter continues a word, so "naïve" is real text.
		return true
	default:
		return false
	}
}
