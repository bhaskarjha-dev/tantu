package main

import (
	"strconv"
	"strings"
	"testing"
)

// The cockpit banner is the first thing a user sees when they start Tantu, and
// for its whole life the box was a fixed 66 columns containing six hardcoded
// minimum field widths. A minimum width pads short content and does nothing
// about long content, so the two values that are genuinely unbounded broke it in
// opposite directions. Measured against the same border, on one build:
//
//	title   7 columns SHORT   (dev version)
//	title   9 columns OVER    (release-shaped version)
//	peer   22 columns OVER    (two peers)
//
// Both were invisible until the binary was run and its output measured, which is
// why these tests exist rather than a comment. They assert the property directly:
// every line of the box occupies the same number of terminal columns.

// plainWidth strips the colour escapes so a coloured row can be measured the
// same way a plain one is.
func plainWidth(s string) int {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if r == 0x1b {
			inEscape = true
			continue
		}
		b.WriteRune(r)
	}
	return cockpitWidth(b.String())
}

func assertUniformBox(t *testing.T, rows []cockpitRow) {
	t.Helper()
	lines := cockpitBox(rows)
	if len(lines) < 3 {
		t.Fatalf("expected a top border, rows and a bottom border, got %d lines", len(lines))
	}
	want := plainWidth(lines[0])
	for i, l := range lines {
		if got := plainWidth(l); got != want {
			t.Errorf("line %d is %d columns, border is %d:\n  %q\n  %q",
				i, got, want, l, lines[0])
		}
	}
}

func TestCockpitBoxRowsAreAllTheSameWidth(t *testing.T) {
	assertUniformBox(t, []cockpitRow{
		{text: "🚀 tantu v1.0.0-dev+4ebc9cc.dirty (Unified Symmetric Hub)"},
		{text: ""},
		{text: "➜ Dashboard:  http://127.0.0.1:9876/               (press o)"},
		{text: "➜ Network:    127.0.0.1:9877 (lan mTLS multiplexer)"},
		{text: "➜ Peer:       Peers (2): Devbox (workstation)* [Online 🟢] • Unreachable (blackhole)"},
	})
}

// The two shapes that broke it: a long version string and a long peer list.
// Both are built by the product, not by hand, so both are reachable.
func TestCockpitBoxSurvivesALongVersionString(t *testing.T) {
	assertUniformBox(t, []cockpitRow{
		{text: "🚀 tantu v1.0.0-rc.1+build.20261013abcdef-dirty (Unified Symmetric Hub)"},
		{text: ""},
		{text: "➜ Dashboard:  http://127.0.0.1:9876/               (press o)"},
	})
}

func TestCockpitBoxSurvivesManyLongPeers(t *testing.T) {
	peers := append([]string(nil),
		"build-box-with-a-deliberately-long-hostname-that-should-truncate-cleanly",
		"Unreachable (blackhole)",
		"tiny-fp",
		"a-really-long-alias-somebody-chose-in-the-dashboard",
	)
	assertUniformBox(t, []cockpitRow{
		{text: "➜ Peer:       Peers (" + strconv.Itoa(len(peers)) + "): " + strings.Join(peers, " • ")},
	})
}

// A coloured row must measure the same as a plain one. The colour is applied
// around the text, so a width computed from the raw row would count escape
// sequences as visible columns.
func TestCockpitBoxColourDoesNotAffectWidth(t *testing.T) {
	assertUniformBox(t, []cockpitRow{
		{text: "⚠️  2 sign-in(s) in progress — press [l] to list or release", color: "33"},
		{text: "⚡ Nearby: devbox (10.0.12.7:9877, not yet paired)", color: "32"},
		{text: "plain row for comparison"},
	})
}

// Truncation must be honest: never exceed the budget, always leave the ellipsis,
// and never split a rune.
func TestCockpitTruncate(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want int // expected column width
	}{
		{"short", 20, 5},
		{"exactly-ten", 11, 11},
		{"this is much longer than twenty columns", 20, 20},
		{"🚀🚀🚀🚀", 5, 5},
		{"", 10, 0},
	} {
		got := cockpitTruncate(tc.in, tc.max)
		if w := cockpitWidth(got); w > tc.max {
			t.Errorf("cockpitTruncate(%q, %d) is %d columns wide, over budget: %q",
				tc.in, tc.max, w, got)
		}
		if tc.in != "" && tc.max > 0 && cockpitWidth(tc.in) > tc.max && !strings.HasSuffix(got, "…") {
			t.Errorf("cockpitTruncate(%q, %d) truncated without an ellipsis: %q", tc.in, tc.max, got)
		}
		if w := cockpitWidth(got); w != tc.want {
			t.Errorf("cockpitTruncate(%q, %d) width = %d, want %d", tc.in, tc.max, w, tc.want)
		}
	}
}

// The emoji in the banner are the reason width cannot be counted in runes.
// Counting runes is what made the old box look right in a diff and wrong on
// screen, so the widths are pinned here rather than trusted.
func TestCockpitRuneWidthsForTheGlyphsTheBannerUses(t *testing.T) {
	for _, tc := range []struct {
		r    rune
		want int
		why  string
	}{
		{'🚀', 2, "the wordmark rocket"},
		{'➜', 2, "the row arrow"},
		{'🟢', 2, "the online status dot"},
		{'⚠', 2, "the warning sign"},
		{'•', 1, "U+2022 is East Asian Width Ambiguous: one column in a Western " +
			"terminal and two in a CJK one. Tantu targets the former, so it counts " +
			"as one. Pinned here because the first version of this test guessed two, " +
			"and 'fixing' the code to match the guess would have made every banner " +
			"line one column too narrow in the terminals people actually use"},
		{'┌', 1, "a box corner"},
		{'─', 1, "a box rule"},
		{'│', 1, "a box wall"},
		{'…', 1, "the truncation ellipsis"},
		{'a', 1, "ordinary text"},
		{' ', 1, "space"},
	} {
		if got := cockpitRuneWidth(tc.r); got != tc.want {
			t.Errorf("cockpitRuneWidth(%q) = %d, want %d (%s)", tc.r, got, tc.want, tc.why)
		}
	}
}
