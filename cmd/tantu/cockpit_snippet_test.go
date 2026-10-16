package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
)

// Gates for the snippet card.
//
// Rendered and read, after seven batches of adding gates that found nothing. The
// card had two defects and no test had ever seen either:
//
//   - Its top and bottom borders disagreed. The top was sized by the label, the
//     peer and the time; the bottom was a fixed run of dashes. A URL card
//     rendered 74 columns against a 78-column base, and a card from a long-named
//     peer rendered 90 against 78. They agreed only when the content happened to
//     fit, which is the definition of a defect that survives review.
//
//   - It truncated with `content[:4096]`, slicing a string by *bytes*. A drop
//     whose 4096th byte landed inside a three-byte character emitted a
//     replacement character into the user's terminal -- in the same file where
//     cockpitTruncate exists specifically to cut on rune boundaries.
//
// And one it hid: the guard was a byte count, which bounds volume but not rows.
// 4096 bytes with no newlines is a single 4096-column line, which is fifty-one
// visual rows on an 80-column terminal carrying one "│". A peer could still
// obliterate the cockpit without exceeding any byte cap.

// snippetCard renders a card and returns its lines with ANSI escapes stripped, so
// a width assertion is about what a terminal draws rather than about escape bytes.
func snippetCard(t *testing.T, item hub.ReceivedDropItem) []string {
	t.Helper()
	raw := captureStdout(t, func() { RenderSnippetCard(item) })
	return strings.Split(strings.TrimRight(stripANSI(raw), "\n"), "\n")
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Every row of a card draws the same width, for every shape of content.
//
// This is the whole of the border defect, stated as the property a reader sees.
func TestSnippetCardRowsAreAllTheSameWidth(t *testing.T) {
	long := strings.Repeat("x", 3000)
	manyLines := strings.Repeat("a line\n", 200)
	cases := []struct {
		name string
		item hub.ReceivedDropItem
	}{
		{"empty", hub.ReceivedDropItem{}},
		{"short text", hub.ReceivedDropItem{Content: "hello"}},
		{"a url", hub.ReceivedDropItem{IsURL: true, Content: "https://example.com/a/very/long/path?with=query&more"}},
		{"anonymous peer", hub.ReceivedDropItem{Content: "hi"}},
		{"a long peer name", hub.ReceivedDropItem{
			FromPeer: "a-really-long-machine-name-that-keeps-going-and-going-and-going",
			Content:  "short note",
		}},
		{"a long peer name and a long url", hub.ReceivedDropItem{
			IsURL:    true,
			FromPeer: "a-really-long-machine-name-that-keeps-going-and-going-and-going",
			Content:  "https://example.com/an/extremely/long/url/that/keeps/going/for/quite/a/while",
		}},
		{"one enormous line", hub.ReceivedDropItem{Content: long}},
		{"many lines", hub.ReceivedDropItem{Content: manyLines}},
		{"wide runes throughout", hub.ReceivedDropItem{Content: strings.Repeat("✓", 400)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := snippetCard(t, tc.item)
			var width int
			for i, line := range lines {
				if line == "" {
					continue // the blank line RenderSnippetCard prints between cards
				}
				w := cockpitWidth(line)
				if width == 0 {
					width = w
					continue
				}
				if w != width {
					t.Errorf("row %d is %d columns and row 0 is %d; a card whose borders "+
						"disagree is ragged in a terminal: %q", i, w, width, line)
				}
			}
			if width == 0 {
				t.Fatal("the card rendered no rows")
			}
		})
	}
}

// A drop whose byte cap lands inside a multi-byte character must not put a
// replacement character on the user's screen.
//
// Produced by arranging the cap to fall inside a three-byte rune, which is the
// only arrangement under which the old byte slice was wrong.
func TestSnippetCardNeverSplitsACharacterAtTheByteCap(t *testing.T) {
	// Place the check mark so the cut falls between its second and third byte.
	// "✓" is three bytes; the cap lands inside whichever one straddles it.
	for offset := 0; offset < 3; offset++ {
		var b strings.Builder
		for b.Len() < snippetMaxBytes-3+offset {
			b.WriteString("a")
		}
		b.WriteString("✓✓✓TAIL")

		out := stripANSI(captureStdout(t, func() {
			RenderSnippetCard(hub.ReceivedDropItem{Timestamp: time.Now(), Content: b.String()})
		}))
		if strings.Contains(out, "�") {
			t.Errorf("with the cap landing %d byte(s) into the character, the card emitted a "+
				"replacement character, which is what a split rune looks like in a terminal", offset)
		}
		if !utf8.ValidString(out) {
			t.Errorf("with the cap landing %d byte(s) into the character, the card emitted "+
				"invalid UTF-8", offset)
		}
	}
}

// A drop cannot obliterate the cockpit by having no newlines in it.
//
// The old guard bounded bytes. Bytes do not bound rows: 4096 of them with no
// newline is one 4096-column line.
func TestSnippetCardBoundsRowsNotJustBytes(t *testing.T) {
	out := snippetCard(t, hub.ReceivedDropItem{
		Timestamp: time.Now(),
		Content:   strings.Repeat("x", 20000), // ~2.7 screenfuls on one line
	})
	// The card is: two borders, a title, a blank, at most snippetMaxLines of
	// content, and a truncation note. Plus the blank lines the card prints
	// around itself.
	if budget := snippetMaxLines + 8; len(out) > budget {
		t.Errorf("a single 20000-character line produced %d rows, over the budget of %d; the "+
			"card must wrap and cap rows, not merely cap bytes, or one peer can still "+
			"scroll-bomb the cockpit", len(out), budget)
	}
	// And it must say so rather than looking complete.
	if !strings.Contains(strings.Join(out, "\n"), "truncated") {
		t.Errorf("a card that dropped content did not say it was truncated:\n%s",
			strings.Join(out, "\n"))
	}
}

// The byte cut lands on a character boundary.
//
// Directly, rather than only through the rendered card, so a failure names the
// cut rather than a replacement character three layers away.
func TestSnippetContentBoundsCutsOnARuneBoundary(t *testing.T) {
	for _, tail := range []string{"✓", "€", "𝄞", "é"} {
		// Every padding here leaves the body longer than the cap, so the cut
		// always happens. An earlier fixture padded to exactly the cap for the
		// widest tail, so that case was not cut at all and the gate failed for
		// its own reasons.
		for pad := 0; pad < len(tail)+3; pad++ {
			body := strings.Repeat("a", snippetMaxBytes-len(tail)+1+pad) + tail
			got, cut := snippetContentBounds(body)
			if !cut {
				t.Fatalf("with %q and padding %d the body was %d bytes and was not cut; "+
					"the fixture does not reach the cap", tail, pad, len(body))
			}
			if !utf8.ValidString(got) {
				t.Errorf("with %q and padding %d the cut produced invalid UTF-8", tail, pad)
			}
			if r, size := utf8.DecodeLastRuneInString(got); r == utf8.RuneError && size <= 1 {
				t.Errorf("with %q and padding %d the last rune decoded as an error", tail, pad)
			}
		}
	}
}

// Wrapping keeps to the width and to rune boundaries.
func TestCockpitWrapRespectsWidthAndRunes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		width int
	}{
		{"short", "hello", 10},
		{"exact", "0123456789", 10},
		{"long", strings.Repeat("a", 35), 10},
		{"wide runes", strings.Repeat("✓", 10), 10},
		{"mixed", strings.Repeat("a✓", 12), 10},
		{"existing newlines", "one\ntwo\nthree", 10},
		{"empty line in the middle", "one\n\ntwo", 10},
		{"zero width", "anything", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := cockpitWrap(tc.in, tc.width)
			if len(got) == 0 {
				t.Fatal("wrapping produced no lines at all")
			}
			if tc.width > 0 {
				for i, line := range got {
					if w := cockpitWidth(line); w > tc.width {
						t.Errorf("wrapped line %d is %d columns, over the %d it was asked for: %q",
							i, w, tc.width, line)
					}
				}
			}
			for _, line := range got {
				if !utf8.ValidString(line) {
					t.Errorf("wrapping produced an invalid UTF-8 line: %q", line)
				}
			}
			// Wrapping must not lose or invent content.
			if tc.width > 0 {
				joined := strings.Join(got, "")
				flat := strings.ReplaceAll(tc.in, "\n", "")
				if strings.ReplaceAll(joined, "", "") != flat {
					t.Errorf("wrapping changed the content: %q became %q", flat,
						strings.ReplaceAll(joined, "", ""))
				}
			}
		})
	}
}

var _ = fmt.Sprint
