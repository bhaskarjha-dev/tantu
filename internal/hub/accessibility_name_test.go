package hub

// Gate for WCAG 2.5.3, Label in Name.
//
// Found by a Lighthouse accessibility audit against the running dashboard: the
// score was 1.0 overall but axe reported one failure,
// `label-content-name-mismatch`, on #dropZone. Its accessible name was
// "Choose a file to send to your peer" while the visible text read "Drag & drop
// files here, or click to browse".
//
// That matters because voice-control users say what they see. A control labelled
// "Drag and drop files here" whose accessible name is something else entirely
// cannot be activated by saying its label, and a screen-reader user hears a
// different sentence from the one printed on screen. 2.5.3 requires the
// accessible name to contain the visible text; this asserts exactly that, for
// every control on the page that has both.
//
// This is a machine check, and it is not a substitute for the screen-reader
// session limitation 2.2 still records. It catches the mechanical mismatch; it
// cannot tell you the journey is navigable.

import (
	"regexp"
	"strings"
	"testing"
)

// visibleLabelPattern matches the visible text of the drop zone and similar
// controls: plain words, ampersands and dashes, emoji stripped.
var emojiOrSpace = regexp.MustCompile(`[^a-z0-9&' ]+`)

func TestAccessibleNamesContainTheirVisibleLabel(t *testing.T) {
	code := stripDashboardComments(dashboardHTML)

	type control struct {
		id    string
		label string
	}
	// Controls whose accessible name must contain what is printed on them.
	for _, c := range []control{
		{id: "dropZone", label: "Drag & drop files here, or click to browse"},
	} {
		idx := strings.Index(code, `id="`+c.id+`"`)
		if idx < 0 {
			t.Errorf("control %s is gone; if it was renamed, point this gate at its replacement", c.id)
			continue
		}
		// The tag runs to its closing '>'.
		start := strings.LastIndex(code[:idx], "<")
		tag := code[start : strings.Index(code[idx:], ">")+idx+1]

		m := regexp.MustCompile(`aria-label="([^"]*)"`).FindStringSubmatch(tag)
		if m == nil {
			t.Errorf("%s has no aria-label, so its accessible name is whatever its text content happens to be", c.id)
			continue
		}
		name := m[1]

		// Normalise both sides: lowercase, drop punctuation and emoji, collapse
		// spaces. "Drag & drop" and "Drag and drop" must compare equal, because
		// a screen reader pronounces the ampersand and a person does not.
		norm := func(s string) string {
			s = strings.ToLower(s)
			s = strings.ReplaceAll(s, "&", " and ")
			s = emojiOrSpace.ReplaceAllString(s, " ")
			return strings.Join(strings.Fields(s), " ")
		}
		want := norm(c.label)
		got := norm(name)
		if !strings.Contains(got, want) {
			t.Errorf("%s announces %q but displays %q. WCAG 2.5.3 requires the accessible name to contain "+
				"the visible text, or a voice-control user cannot activate it by saying its label:\n  tag: %s",
				c.id, name, c.label, tag)
		}
	}
}

// TestNoControlHidesItsLabelEntirely is the other half of the same rule: an
// interactive element with an aria-label and no visible text at all is fine
// (an icon button), but one with visible text whose name omits all of it is the
// defect above. Asserting the broad case keeps a future control from
// reintroducing it with different wording.
func TestNoControlHidesItsLabelEntirely(t *testing.T) {
	code := stripDashboardComments(dashboardHTML)
	// Every role="button" must either have no visible text, or an aria-label that
	// shares a word with that text.
	re := regexp.MustCompile(`<[^>]*role="button"[^>]*>`)
	for _, tag := range re.FindAllString(code, -1) {
		name := regexp.MustCompile(`aria-label="([^"]*)"`).FindStringSubmatch(tag)
		if name == nil {
			continue // no aria-label: the text content is the name
		}
		visible := regexp.MustCompile(`>\s*([^<]{3,})\s*<`).FindStringSubmatch(tag)
		if visible == nil {
			continue // no visible text in the tag itself
		}
		text := strings.TrimSpace(visible[1])
		if text == "" {
			continue
		}
		first := strings.ToLower(strings.Fields(text)[0])
		if !strings.Contains(strings.ToLower(name[1]), first) {
			t.Errorf("a role=button displays %q but announces %q, sharing no word with its label:\n  %s",
				text, name[1], tag)
		}
	}
}
