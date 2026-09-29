package main

import "testing"

// The two shapes below were in the tree from its very first commit and were
// invisible to every check the gate had. The fixtures are written with \u
// escapes because this file is itself scanned by the gate: a pasted mojibake
// literal would make the gate fail on its own test source. The soft hyphen is
// additionally assembled from its code point rather than written literally,
// because writing it literally is exactly the invisible-character problem
// this test exists to describe - and it would trip the very check it covers.
var (
	// softHyphenDamage is a log-message emoji mis-decoded to
	// U+00AD U+0192 U+00F6 U+00F9. The soft hyphen is invisible in an editor,
	// a diff and a terminal, which is why this damage went unnoticed.
	softHyphenDamage = string([]rune{0x00AD, 0x0192, 0x00F6, 0x00F9})
	// latinRunDamage is an emoji mis-decoded to a cluster of Latin-1 letters
	// whose bytes are not valid UTF-8. It cannot be reversed, so no round
	// trip can detect it - only its shape can.
	latinRunDamage = string([]rune{0x00D4, 0x00D8, 0x00EE})
)

// countMojibakeRuns catches the one damage shape that leaves no reversible
// trace, and it must stay silent on every intact version of the same lines.
func TestCountMojibakeRunsCatchesUnreversibleDamage(t *testing.T) {
	// The soft-hyphen shape starts below U+00A0, so the run rule does not see
	// it; countFormatChars is what covers it, and the combined gate must
	// therefore report it. Asserting on the combined result is the honest
	// statement of the guarantee, since that is what a run of the gate does.
	damaged := []string{
		`a.logf("` + latinRunDamage + ` Session error: %s", err)`,
		`a.logf("` + softHyphenDamage + ` Opening browser for URL: %s", u)`,
		`// ` + latinRunDamage + ` neither of which can be forged`,
	}
	for _, in := range damaged {
		viaRuns := countMojibakeRuns(in)
		viaFormat := countFormatChars(in)
		viaRoundTrip := countMojibake(in) - viaRuns
		if viaRuns+viaFormat+viaRoundTrip == 0 {
			t.Errorf("no rule detected the damage in %q", in)
		}
	}
	// The run rule specifically covers the cluster that begins above U+00A0.
	for _, in := range []string{latinRunDamage, `// ` + latinRunDamage + ` text`} {
		if got := countMojibakeRuns(in); got == 0 {
			t.Errorf("countMojibakeRuns(%q) = 0, want > 0", in)
		}
	}
	clean := []string{
		`a.logf("⚠️ Connection rejected from %s", addr)`,
		`a.logf("❌ OAuth ASide error from %s: %v", addr, err)`,
		`a.logf("🔀 Multiplexer: routing connection from %s", addr)`,
		`// 3 ≤ 4 and 6 ÷ 2 are real mathematics`,
		`// a · b and § 2 are real separators`,
		"// café and naïve are real words",
		"// 日本語のコメント",
	}
	for _, in := range clean {
		if got := countMojibakeRuns(in); got != 0 {
			t.Errorf("countMojibakeRuns(%q) = %d, want 0", in, got)
		}
	}
}

// Invisible format characters have no legitimate use in source, and the soft
// hyphen is the signature of the lossy transcode that caused the damage.
func TestCountFormatCharsCatchesInvisibleDamage(t *testing.T) {
	if got := countFormatChars("log line " + softHyphenDamage + " more"); got == 0 {
		t.Error("a soft hyphen must be reported as damage")
	}
	if got := countFormatChars("clean ✅ text with an em dash — and ❌"); got != 0 {
		t.Errorf("countFormatChars = %d, want 0 on clean text", got)
	}
}
