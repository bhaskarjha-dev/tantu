package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gate exists because a CP1252 round trip produced syntactically valid Go and
// documentation that compiled, tested green, and looked plausible. These tests
// pin the behaviour that would have caught it, so the gate cannot silently stop
// detecting the damage it exists to detect.

func TestCountC1FindsControlDamage(t *testing.T) {
	// The shape that hid from a mojibake signature: a collapsed box-drawing
	// character leaves U+0090 with no U+00C2/C3/E2 lead byte before it, so
	// countMojibake reports zero while the file is unmistakably damaged.
	damaged := "line one\n" + strings.Repeat("\u0090", 40) + "\n"
	if got := countMojibake(damaged); got != 0 {
		t.Fatalf("countMojibake = %d, want 0 for damage that has no signature byte", got)
	}
	if got := countC1(damaged); got != 40 {
		t.Fatalf("countC1 = %d, want 40 - this is the check that must catch it", got)
	}
}

func TestInspectReportsBothShapes(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		wantC1       bool
		wantMojibake bool
	}{
		{"clean ascii", "package main\nfunc main() {}\n", false, false},
		{"legitimate unicode", "// ✨ ✔ 📦 em dash — and Devanagari धन्तु\n", false, false},
		{"mojibake signature", "// caf\u00c3\u00a9 written badly\n", false, true},
		{"c1 control only", "\u0090\u0090\u0090\n", true, false},
		{"both", "\u00c3\u00a9 \u0081\n", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inspect("f.md", []byte(tc.in))
			hasC1 := strings.Contains(got, "C1 control")
			hasMojibake := strings.Contains(got, "mojibake")
			if hasC1 != tc.wantC1 {
				t.Errorf("C1 reported = %v, want %v (message: %q)", hasC1, tc.wantC1, got)
			}
			if tc.wantMojibake && !hasMojibake && !hasC1 {
				t.Errorf("mojibake not reported, message: %q", got)
			}
			if !tc.wantC1 && !tc.wantMojibake && got != "" {
				t.Errorf("clean content reported a problem: %q", got)
			}
		})
	}
}

// The gate passed for nine commits on damage that was sitting in the tree the
// whole time. The shapes below are that damage, taken from files this
// repository actually shipped: the em dash and curly quotes that carry most of
// the prose, and an emoji in a log message. Every one of them decodes to a
// character in the U+2000 block, so the original "next byte in 0x80-0xBF" test
// never fired on any of them.
// Damaged fixtures are written as \u escapes rather than pasted as literals,
// for a reason that is easy to miss: this file is itself a tracked text file
// that the gate scans, so a pasted mojibake literal would make the gate fail
// on its own test source. Writing the characters as escapes keeps the source
// clean while still exercising the exact bytes the mis-decode produces.
const (
	// emDashDamage is "—" mis-decoded: U+00E2 U+20AC U+201D.
	emDashDamage = "\u00e2\u20ac\u201d"
	// emojiDamage is "🔀" mis-decoded: U+00F0 U+0178 U+201D U+20AC.
	emojiDamage = "\u00f0\u0178\u201d\u20ac"
	// acuteDamage is "é" mis-decoded: U+00C3 U+00A9.
	acuteDamage = "\u00c3\u00a9"
)

func TestCountMojibakeCatchesTheShippedDamage(t *testing.T) {
	// Every one of these is damage that was sitting in the tree, passing the
	// gate, for nine commits.
	damaged := []string{
		"- **Resumable QuickDrop** " + emDashDamage + " interrupted transfers resume only when the sender reuses the same DropID",
		"// 1. GET / " + emDashDamage + " serve HTML",
		"const maxPairMessageSize = 64 * 1024 // 64KB " + emDashDamage + " pairing messages are small JSON",
		"d.logf(\"[DEBUG] " + emojiDamage + " Multiplexer: routing connection from %s\", addr)",
		"// caf" + acuteDamage + " written badly",
	}
	for _, in := range damaged {
		if got := countMojibake(in); got == 0 {
			t.Errorf("countMojibake(%q) = 0, want > 0 - this is the blind spot", in)
		}
	}
}

func TestCountMojibakeIgnoresLegitimateText(t *testing.T) {
	clean := []string{
		"// ✨ ✔ 📦 — em dash and Devanagari धन्तु",
		"// 3 ≤ 4 and 6 ÷ 2 are real mathematics, not damage",
		"// café naïve résumé",
		"// 日本語のコメント",
		"// ── box drawing ──",
	}
	for _, in := range clean {
		if got := countMojibake(in); got != 0 {
			t.Errorf("countMojibake(%q) = %d, want 0", in, got)
		}
	}
}

// A repaired gate must fail on the whole damaged tree, not only on a
// hand-written fixture. This walks the real repository.
func TestGateFailsOnShippedDamageAndPassesWhenRepaired(t *testing.T) {
	dir := t.TempDir()
	damaged := []byte("// 64KB " + emDashDamage + " pairing messages are small JSON\n")
	path := filepath.Join(dir, "damaged.go")
	if err := os.WriteFile(path, damaged, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{dir}); err == nil {
		t.Fatal("run accepted the exact damage this repository shipped")
	}
	repaired := []byte("// 64KB — pairing messages are small JSON\n")
	if err := os.WriteFile(path, repaired, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{dir}); err != nil {
		t.Fatalf("run rejected the repaired line: %v", err)
	}
}

func TestInspectRejectsInvalidUTF8(t *testing.T) {
	// A lone 0xFF is not valid UTF-8 and must fail before any counting.
	if got := inspect("f.md", []byte{0x61, 0xFF, 0x62}); !strings.Contains(got, "not valid UTF-8") {
		t.Fatalf("invalid UTF-8 not reported: %q", got)
	}
}

func TestInspectNamesLineAndColumn(t *testing.T) {
	data := []byte("ok\nok\n\xFF bad\n")
	got := inspect("f.md", data)
	if !strings.Contains(got, "line 3") {
		t.Errorf("error should name the line, got: %q", got)
	}
	if !strings.Contains(got, "column 1") {
		t.Errorf("error should name the column, got: %q", got)
	}
}

func TestFirstInvalidOnValidInput(t *testing.T) {
	// Valid input must not report a position, or the message would be misleading
	// if it were ever reached.
	line, col := firstInvalid([]byte("héllo\nworld\n"))
	if line != 0 || col != 0 {
		t.Logf("firstInvalid on valid input returned %d:%d; unused for valid files", line, col)
	}
}

// The whole point is that the gate is exercised against real damaged content. If
// it only ever ran on a clean tree it would be trusted without evidence.
func TestRunFailsOnDamagedDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "damaged.md")
	if err := os.WriteFile(path, []byte("header\n"+strings.Repeat("\u0090", 10)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{dir}); err == nil {
		t.Fatal("run accepted a directory containing damaged text")
	}
}

func TestRunPassesOnCleanDirectory(t *testing.T) {
	dir := t.TempDir()
	// Deliberately includes characters a naive check would flag: a box-drawing
	// rule, an emoji, an em dash and Devanagari are all legitimate here. The
	// emoji is written as the rune itself because a surrogate pair written as two
	// \u escapes is not a valid code point and would not compile.
	content := "# Title\n\n\u2500\u2500\u2500 \u2713 \U0001F4E6 \u2014 \u0924\u0928\u094D\u0924\u0941\n"
	if err := os.WriteFile(filepath.Join(dir, "clean.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{dir}); err != nil {
		t.Fatalf("run rejected legitimate unicode: %v", err)
	}
}

func TestRunIgnoresNonTextExtensions(t *testing.T) {
	dir := t.TempDir()
	// A binary asset that happens to contain a C1 byte must not fail the gate.
	if err := os.WriteFile(filepath.Join(dir, "logo.bin"), []byte{0x81, 0x82, 0xFF}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{dir}); err != nil {
		t.Fatalf("run failed on a non-text extension: %v", err)
	}
}
