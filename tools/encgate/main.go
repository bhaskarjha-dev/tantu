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
//     mis-decode produces, plus the OEM console pages (CP850, CP437): a
//     PowerShell console round trip decodes as the OEM page, not CP1252, and
//     four such arrows shipped in internal/protocol/protocol.go before this
//     reverse existed.
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
	return fmt.Sprintf("%s: %s - encoding damage", name, strings.Join(parts, " and "))
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
	for b, r := range cp850Table {
		cp850Byte[r] = b
	}
	for b, r := range cp437Table {
		cp437Byte[r] = b
	}
}

// cp850Table and cp437Table are the OEM console code pages - the pages a
// Windows console or a PowerShell Get-Content/Set-Content round trip applies.
// They are stated as byte -> rune, like the CP1252 table, and inverted into
// the reverse maps at init. Both pages are bijective over 0x80-0xFF (no
// character appears twice), so the inversion is exact; a duplicate would make
// a reverse table quietly wrong for one of its two bytes.
//
// Like the CP1252 table above, these are rune literals with the gate's own
// damage shapes in them, and the gate scans this file like any other. What
// keeps it clean is structural: every glyph sits between ASCII characters
// (quote, colon, comma), so no run of high-Latin characters forms here and
// reversing a lone glyph plus its ASCII surroundings can never produce valid
// UTF-8.
var cp850Table = map[byte]rune{
	0x80: 'Ç', 0x81: 'ü', 0x82: 'é', 0x83: 'â', 0x84: 'ä', 0x85: 'à',
	0x86: 'å', 0x87: 'ç', 0x88: 'ê', 0x89: 'ë', 0x8A: 'è', 0x8B: 'ï',
	0x8C: 'î', 0x8D: 'ì', 0x8E: 'Ä', 0x8F: 'Å', 0x90: 'É', 0x91: 'æ',
	0x92: 'Æ', 0x93: 'ô', 0x94: 'ö', 0x95: 'ò', 0x96: 'û', 0x97: 'ù',
	0x98: 'ÿ', 0x99: 'Ö', 0x9A: 'Ü', 0x9B: 'ø', 0x9C: '£', 0x9D: 'Ø',
	0x9E: '×', 0x9F: 'ƒ', 0xA0: 'á', 0xA1: 'í', 0xA2: 'ó', 0xA3: 'ú',
	0xA4: 'ñ', 0xA5: 'Ñ', 0xA6: 'ª', 0xA7: 'º', 0xA8: '¿', 0xA9: '®',
	0xAA: '¬', 0xAB: '½', 0xAC: '¼', 0xAD: '¡', 0xAE: '«', 0xAF: '»',
	0xB0: '░', 0xB1: '▒', 0xB2: '▓', 0xB3: '│', 0xB4: '┤', 0xB5: 'Á',
	0xB6: 'Â', 0xB7: 'À', 0xB8: '©', 0xB9: '╣', 0xBA: '║', 0xBB: '╗',
	0xBC: '╝', 0xBD: '¢', 0xBE: '¥', 0xBF: '┐', 0xC0: '└', 0xC1: '┴',
	0xC2: '┬', 0xC3: '├', 0xC4: '─', 0xC5: '┼', 0xC6: 'ã', 0xC7: 'Ã',
	0xC8: '╚', 0xC9: '╔', 0xCA: '╩', 0xCB: '╦', 0xCC: '╠', 0xCD: '═',
	0xCE: '╬', 0xCF: '¤', 0xD0: 'ð', 0xD1: 'Ð', 0xD2: 'Ê', 0xD3: 'Ë',
	0xD4: 'È', 0xD5: 'ı', 0xD6: 'Í', 0xD7: 'Î', 0xD8: 'Ï', 0xD9: '┘',
	0xDA: '┌', 0xDB: '█', 0xDC: '▄', 0xDD: '¦', 0xDE: 'Ì', 0xDF: '▀',
	0xE0: 'Ó', 0xE1: 'ß', 0xE2: 'Ô', 0xE3: 'Ò', 0xE4: 'õ', 0xE5: 'Õ',
	0xE6: 'µ', 0xE7: 'þ', 0xE8: 'Þ', 0xE9: 'Ú', 0xEA: 'Û', 0xEB: 'Ù',
	0xEC: 'ý', 0xED: 'Ý', 0xEE: '¯', 0xEF: '´', 0xF0: '\u00AD', 0xF1: '±',
	0xF2: '‗', 0xF3: '¾', 0xF4: '¶', 0xF5: '§', 0xF6: '÷', 0xF7: '¸',
	0xF8: '°', 0xF9: '¨', 0xFA: '·', 0xFB: '¹', 0xFC: '³', 0xFD: '²',
	0xFE: '■', 0xFF: '\u00A0',
}

var cp437Table = map[byte]rune{
	0x80: 'Ç', 0x81: 'ü', 0x82: 'é', 0x83: 'â', 0x84: 'ä', 0x85: 'à',
	0x86: 'å', 0x87: 'ç', 0x88: 'ê', 0x89: 'ë', 0x8A: 'è', 0x8B: 'ï',
	0x8C: 'î', 0x8D: 'ì', 0x8E: 'Ä', 0x8F: 'Å', 0x90: 'É', 0x91: 'æ',
	0x92: 'Æ', 0x93: 'ô', 0x94: 'ö', 0x95: 'ò', 0x96: 'û', 0x97: 'ù',
	0x98: 'ÿ', 0x99: 'Ö', 0x9A: 'Ü', 0x9B: '¢', 0x9C: '£', 0x9D: '¥',
	0x9E: '₧', 0x9F: 'ƒ', 0xA0: 'á', 0xA1: 'í', 0xA2: 'ó', 0xA3: 'ú',
	0xA4: 'ñ', 0xA5: 'Ñ', 0xA6: 'ª', 0xA7: 'º', 0xA8: '¿', 0xA9: '⌐',
	0xAA: '¬', 0xAB: '½', 0xAC: '¼', 0xAD: '¡', 0xAE: '«', 0xAF: '»',
	0xB0: '░', 0xB1: '▒', 0xB2: '▓', 0xB3: '│', 0xB4: '┤', 0xB5: '╡',
	0xB6: '╢', 0xB7: '╖', 0xB8: '╕', 0xB9: '╣', 0xBA: '║', 0xBB: '╗',
	0xBC: '╝', 0xBD: '╜', 0xBE: '╛', 0xBF: '┐', 0xC0: '└', 0xC1: '┴',
	0xC2: '┬', 0xC3: '├', 0xC4: '─', 0xC5: '┼', 0xC6: '╞', 0xC7: '╟',
	0xC8: '╚', 0xC9: '╔', 0xCA: '╩', 0xCB: '╦', 0xCC: '╠', 0xCD: '═',
	0xCE: '╬', 0xCF: '╧', 0xD0: '╨', 0xD1: '╤', 0xD2: '╥', 0xD3: '╙',
	0xD4: '╘', 0xD5: '╒', 0xD6: '╓', 0xD7: '╫', 0xD8: '╪', 0xD9: '┘',
	0xDA: '┌', 0xDB: '█', 0xDC: '▄', 0xDD: '▌', 0xDE: '▐', 0xDF: '▀',
	0xE0: 'α', 0xE1: 'ß', 0xE2: 'Γ', 0xE3: 'π', 0xE4: 'Σ', 0xE5: 'σ',
	0xE6: 'µ', 0xE7: 'τ', 0xE8: 'Φ', 0xE9: 'Θ', 0xEA: 'Ω', 0xEB: 'δ',
	0xEC: '∞', 0xED: 'φ', 0xEE: 'ε', 0xEF: '∩', 0xF0: '≡', 0xF1: '±',
	0xF2: '≥', 0xF3: '≤', 0xF4: '⌠', 0xF5: '⌡', 0xF6: '÷', 0xF7: '≈',
	0xF8: '°', 0xF9: '∙', 0xFA: '·', 0xFB: '√', 0xFC: 'ⁿ', 0xFD: '²',
	0xFE: '■', 0xFF: '\u00A0',
}

// cp850Byte and cp437Byte are the inverted OEM tables: character -> the byte
// a mis-decode consumed. Built at init alongside the CP1252 maps so every
// direction comes from one statement of each code page.
var (
	cp850Byte = map[rune]byte{}
	cp437Byte = map[rune]byte{}
)

// isLatinLead reports whether r is one of the U+00C0..U+00FF characters that a
// UTF-8 mis-decode leaves behind. × and ÷ are excluded: they are real
// mathematics, and the Windows-1252 code page does not remap their bytes.
func isLatinLead(r rune) bool {
	return r >= 0xC0 && r <= 0xFF && r != '×' && r != '÷'
}

// misdecodes reports whether a run of text is the result of decoding UTF-8
// bytes as one of the code pages this gate knows, and does so by construction
// rather than by guessing.
//
// The question a shape heuristic keeps getting wrong is "could this be real
// text?". A letter with an acute accent followed by an em dash is a perfectly
// good sentence, and no character pair distinguishes it from the damage those
// same three characters produce when they arrive from a mis-decode. So the
// test does not guess: it reverses the operation, under each code page in
// turn. A run counts as damage only if some page recodes it to bytes that are
// valid UTF-8 *and* decode to something different from what is actually in
// the file. Legitimate text either fails to recode or recodes to itself, so
// it is never counted; real damage always recodes to a shorter, different,
// valid UTF-8 string, because that is literally what happened to it.
//
// Trying only CP1252 misses damage done by the OEM console pages: CP850
// turns an em-dash arrow into three Latin-1 letters whose CP1252 reverse is
// not valid UTF-8. That shape shipped in internal/protocol/protocol.go, so
// every page the project's toolchains can apply is reversed here.
func misdecodes(run string) bool {
	for _, page := range pages {
		recoded, ok := page.recode(run)
		if !ok {
			continue
		}
		if !utf8.Valid(recoded) {
			continue
		}
		if string(recoded) != run {
			return true
		}
	}
	return false
}

// page is one code page the gate can reverse: its name for diagnostics and
// the recode function that maps characters back to the bytes a mis-decode
// consumed.
type page struct {
	name   string
	recode func(string) ([]byte, bool)
}

// pages are the code pages reversed by the gate, in the order they are
// tried. CP1252 first because it caused the original damage; the OEM console
// pages follow because a console round trip applies them instead.
var pages = []page{
	{"CP1252", recodeCP1252},
	{"CP850", recodeOEM(cp850Byte)},
	{"CP437", recodeOEM(cp437Byte)},
}

// recodeOEM builds a recoder for an OEM console page. Bytes 0x00-0x7F are
// ASCII in every page; everything above must be present in the reverse map.
func recodeOEM(byteOf map[rune]byte) func(string) ([]byte, bool) {
	return func(run string) ([]byte, bool) {
		out := make([]byte, 0, len(run))
		for _, r := range run {
			if r < 0x80 {
				out = append(out, byte(r))
				continue
			}
			b, ok := byteOf[r]
			if !ok {
				return nil, false
			}
			out = append(out, b)
		}
		return out, true
	}
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
// characters that all recode to bytes under some known page and start with a
// Latin lead. Splitting on lead characters keeps the comparison local, so one
// damaged region in a long line cannot be masked by legitimate text elsewhere
// in it. Growth accepts any page; the misdecode test then has to reverse the
// whole run under a single page, which is what keeps permissive growth from
// inventing damage.
func countMojibake(s string) int {
	rs := []rune(s)
	n := 0
	for i := 0; i < len(rs); i++ {
		if !isLatinLead(rs[i]) {
			continue
		}
		// Grow the run: the lead plus every following character that some
		// page can map back to a byte. A real sequence is 2-3 characters wide.
		j := i
		for j < len(rs) {
			if !recodableRune(rs[j]) {
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

// recodableRune reports whether some known page maps r to a byte: ASCII, the
// CP1252-representable set, or either OEM reverse table.
func recodableRune(r rune) bool {
	if r < 0x80 {
		return true
	}
	if _, ok := recodeCP1252(string(r)); ok {
		return true
	}
	if _, ok := cp850Byte[r]; ok {
		return true
	}
	if _, ok := cp437Byte[r]; ok {
		return true
	}
	return false
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
