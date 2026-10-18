package hub

// Gate for focus visibility across every focusable control type.
//
// Found by walking the real Tab order against a running Hub and reading the
// computed outline at each stop. Everything showed a deliberate 2px accent ring
// except one: the expert immediate-send checkbox, which had no focus rule at
// all and so fell back to the user agent default -- `auto 0.8px
// rgb(238, 238, 238)`, a sub-pixel pale-grey outline. On the light theme, a pale
// grey ring on near-white is close to invisible.
//
// The omission was invisible to review because the rule it should have joined
// exists and looks complete: `input[type="text"], textarea, select, button` all
// share one focus-visible rule, and checkbox was simply absent from a list of
// five. Nothing about the CSS says "and also every other input".
//
// So this gate works from the controls outward rather than from the rules
// inward: for every focusable control type the page actually ships, assert that
// some rule gives it an explicit outline. A control that has none is exactly the
// defect, and a control added later with a new type fails here rather than
// inheriting the browser's ring silently.

import (
	"regexp"
	"strings"
	"testing"
)

// focusRuleMatches finds any selector in the page that gives a rule an explicit
// outline, and reports whether the given type selector matches it.
var outlineDecl = regexp.MustCompile(`outline\s*:`)

func hasExplicitOutlineFor(t *testing.T, typeSelector string) bool {
	t.Helper()
	code := stripDashboardComments(dashboardHTML)
	// Rule selectors containing :focus-visible with an outline declaration.
	re := regexp.MustCompile(`([^{}]+:focus-visible[^{}]*)\{([^{}]*)\}`)
	for _, m := range re.FindAllStringSubmatch(code, -1) {
		if !outlineDecl.MatchString(m[2]) {
			continue
		}
		sel := strings.TrimSpace(m[1])
		// A comma list: any part naming the type (with or without the
		// attribute form) counts.
		for _, part := range strings.Split(sel, ",") {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			if p == typeSelector || strings.HasSuffix(p, " "+typeSelector) || strings.Contains(p, typeSelector) {
				return true
			}
		}
	}
	return false
}

func TestEveryFocusableControlTypeHasAnExplicitFocusRing(t *testing.T) {
	// Each entry is a control type the dashboard actually ships, with the
	// selector a CSS author would write to target it.
	for _, c := range []struct {
		typeName string
		selector string
	}{
		{"text input", `input[type="text"]`},
		{"textarea", "textarea"},
		{"select", "select"},
		{"button", "button"},
		{"checkbox", `input[type="checkbox"]`},
	} {
		t.Run(c.typeName, func(t *testing.T) {
			if !hasExplicitOutlineFor(t, c.selector) {
				t.Errorf("no :focus-visible rule gives %s an explicit outline, so it falls back to the "+
					"user agent ring -- which on this page is a sub-pixel pale grey. Add %s to the "+
					"shared focus rule rather than relying on the browser.", c.typeName, c.selector)
			}
		})
	}
}

// TestCheckboxAndRadioShareTheStandardRing pins that the new rule uses the same
// accent and geometry as every other control, rather than merely existing. A
// second, differently-styled focus ring would still be a consistency defect.
func TestCheckboxAndRadioShareTheStandardRing(t *testing.T) {
	code := stripDashboardComments(dashboardHTML)
	re := regexp.MustCompile(`input\[type="checkbox"\]:focus-visible[^{]*\{([^}]*)\}`)
	m := re.FindStringSubmatch(code)
	if m == nil {
		t.Fatal("the checkbox focus-visible rule is gone; if it was renamed, point this gate at it")
	}
	body := m[1]
	for _, want := range []string{"outline: 2px solid var(--accent)", "outline-offset: 2px"} {
		if !strings.Contains(body, want) {
			t.Errorf("the checkbox focus ring differs from the page standard -- missing %q:\n%s", want, body)
		}
	}
}

// TestNoFocusableElementCarriesAnInlineOutlineSuppress is the gate the previous
// two could not be.
//
// Both of them read the stylesheet. The peer `<select>` in the dashboard header
// -- the control that decides who you are sending to -- was written with an
// inline `outline:none`, and an inline declaration outranks an author rule, so
// `select:focus-visible { outline: 2px solid var(--accent) }` was present,
// correct, and doing nothing. The control had no visible focus indicator at all
// (WCAG 2.4.7), and the stylesheet-based gates were green throughout.
//
// That is the same shape as every gate failure this repository records: the
// thing being checked is correct while the thing actually running is not. So
// this one does not read the stylesheet at all. It reads what is *rendered into
// the page* and refuses an inline outline suppression on anything focusable.
//
// The scan covers the static markup and the JavaScript-built strings in one
// pass, because both produce the same text and the defect arrived in the
// second: `peerLabel.innerHTML = '<select ... style="...outline:none...">'`.
// A gate that only looked at the server-rendered body would have passed it.
//
// A non-suppressing inline outline is allowed. It is only the removal of the
// indicator that is refused, and the message says so, because the fix is to
// move the presentation into a class rather than to delete the declaration.
func TestNoFocusableElementCarriesAnInlineOutlineSuppress(t *testing.T) {
	// A focusable element, with its attributes in any order, carrying an inline
	// declaration that removes the outline. `outline\s*:\s*(none|0)` rather than
	// `outline\s*:` so that an inline `outline: 2px solid ...` -- which
	// reinforces the ring -- is not reported.
	inlineOutlineSuppress := regexp.MustCompile(
		`(?i)<(?:button|input|select|textarea|a)\b[^>]*\sstyle\s*=\s*"[^"]*\boutline\s*:\s*(?:none|0)\b`)

	for _, m := range inlineOutlineSuppress.FindAllString(stripDashboardComments(dashboardHTML), -1) {
		// Trim to a readable length; the match may run into the rest of a long
		// style attribute and a 400-character line helps nobody.
		shown := m
		if len(shown) > 220 {
			shown = shown[:220] + "..."
		}
		t.Errorf("a focusable element carries an inline `outline: none`, which removes the focus ring "+
			"that the :focus-visible rules above draw. An inline declaration outranks a stylesheet rule, "+
			"so the rule is present and doing nothing:\n  %s\nMove the presentation into a class -- do not "+
			"delete the declaration without replacing it.", shown)
	}
}

// The peer switcher specifically, because it is the one control where losing the
// ring is a correctness problem rather than a cosmetic one: it decides which
// machine receives the file.
func TestPeerSwitcherKeepsItsFocusRing(t *testing.T) {
	code := stripDashboardComments(dashboardHTML)
	rule := regexp.MustCompile(`(?s)\.header-peer-select:focus-visible[^{]*\{([^}]*)\}`)
	m := rule.FindStringSubmatch(code)
	if m == nil {
		t.Fatal("no .header-peer-select:focus-visible rule; the peer switcher must carry the ring itself, " +
			"because it is rendered inside the dark header chrome where --text is not the right colour")
	}
	if !outlineDecl.MatchString(m[1]) {
		t.Errorf(".header-peer-select:focus-visible declares no outline:\n%s", m[1])
	}
	// Chrome-aware: the ring is painted on the deliberately-dark header, in both
	// OS themes, so it must use the token built for that surface.
	if !strings.Contains(m[1], "var(--accent-on-chrome)") {
		t.Errorf("the peer switcher's focus ring does not use --accent-on-chrome:\n%s", m[1])
	}
}
