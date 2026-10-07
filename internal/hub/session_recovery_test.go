package hub

// Gate for limitation 3.6: the dashboard's recovery instructions must name a
// command that actually works from where the reader is.
//
// The gap was found by getting it wrong. Opening 127.0.0.1:9876 by hand produces
// this banner, and the previous wording offered exactly two options:
//
//   - "Reopen the dashboard from the Hub terminal (press o)"
//   - "reload the page the Hub opened for you"
//
// Neither helps someone who got here by typing the address, opening a bookmark,
// or starting from a new tab -- there is no Hub-opened tab to reload, and the
// `o` shortcut only exists in a terminal the user may not have open. The most
// likely way to reach this page by hand is also the one the instructions cannot
// recover from.
//
// `tantu dashboard` works in every case: it finds the running Hub, mints a fresh
// one-time link, and prints it. That is now what both messages say.
//
// The page cannot recover itself, and that is deliberate rather than an
// oversight. `/api/dashboard-url` mints a fresh authenticated link, but it sits
// behind the same capability check as every other endpoint -- which is precisely
// what has just been lost. Exempting it so the page could self-heal would let
// any local page, including a cross-site request to 127.0.0.1, obtain a live
// dashboard link. So the honest fix is to say the right thing, not to make the
// wrong thing possible.

import (
	"os"
	"strings"
	"testing"
)

// TestSessionRecoveryNamesACommandThatWorksFromAnywhere is the gate. Both
// recovery surfaces must name `tantu dashboard`, because that is the only
// instruction that helps a reader who did not arrive from the Hub.
func TestSessionRecoveryNamesACommandThatWorksFromAnywhere(t *testing.T) {
	cases := []struct {
		name string
		want string
		html string
	}{
		{
			// The standing banner, shown on first paint.
			name: "session banner",
			html: elementInnerHTML(t, "sessionBanner"),
		},
		{
			// The inline status line shown when an action is attempted.
			name: "action status line",
			html: elementInnerHTML(t, "relayStatus"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.html == "" {
				// The relay status line is empty until first paint, so fall back
				// to the whole document for that one rather than reporting a
				// false failure.
				c.html = dashboardHTML
			}
			if !strings.Contains(c.html, "tantu dashboard") {
				t.Errorf("the recovery instruction does not name `tantu dashboard`, so a reader who opened this page by hand has no way back: %s", excerpt(c.html))
			}
		})
	}

	// The page must not claim the reader can reload their way out unless the Hub
	// opened the page, because that is the advice that fails for exactly the
	// hand-opened case. It may still be offered as a secondary option.
	code := stripDashboardComments(dashboardHTML)
	if strings.Contains(code, "reload the page the Hub opened for you.") &&
		!strings.Contains(code, "If the Hub") {
		t.Error("the reload advice is offered without saying it only applies when the Hub opened the tab")
	}
}

// TestRelayPathsDeclareWhichIsPrimary pins the relay tab's hierarchy.
//
// The bookmarklet and the manual forwarder were presented as equals: one button
// above a paragraph, and a second card below, with no indication which to use.
// They are not equals. The bookmarklet relays the page you are already looking
// at in one click, with no URL to copy; the forwarder requires selecting a long
// URL out of an address bar and pasting it. A user picking between two controls
// that look equally hard will pick the one that looks easiest to reason about,
// which is the wrong one.
//
// The unavailable state matters for the same reason: without a dashboard session
// the href is javascript:void(0), so the button used to look draggable and live
// and silently do nothing.
func TestRelayPathsDeclareWhichIsPrimary(t *testing.T) {
	// Assert on the *markup*, not on the class name anywhere in the page. The
	// first version of this gate checked `path-label--fallback` as a substring,
	// which also matches the stylesheet -- so deleting the badge from the page
	// left the gate green. That is the class of gate this repository keeps
	// fixing, and red proof caught it here rather than CI.
	code := stripDashboardComments(dashboardHTML)
	if !strings.Contains(code, `<span class="path-label path-label--primary">`) {
		t.Error("the relay tab has no primary-path badge in its markup, so the bookmarklet and the forwarder still look like equals")
	}
	if !strings.Contains(code, `<span class="path-label path-label--fallback">`) {
		t.Error("the manual forwarder has no fallback badge in its markup, so it reads as an equal second option")
	}
	// The primary label must actually name the bookmarklet, not just exist.
	if !strings.Contains(code, "One click") {
		t.Error("the primary label does not say what makes it primary; \"One click\" is the whole advantage")
	}

	// Every placeholder the page uses must be substituted by the server, or it
	// ships to the browser as literal text.
	for _, ph := range []string{"{{BOOKMARKLET_HREF}}", "{{BOOKMARKLET_STATE}}"} {
		if !strings.Contains(dashboardHTML, ph) {
			t.Errorf("placeholder %s is no longer in the page but the server may still replace it", ph)
		}
	}

	// And the server must derive the unavailable state rather than hardcode it.
	if !strings.Contains(readFileForGate(t, "web_dashboard.go"), "bookmarkletState = \"unavailable\"") {
		t.Error("the server no longer marks the bookmarklet unavailable when there is no capability, so the pill can again look live and do nothing")
	}
}

// readFileForGate reads a sibling source file for a structural assertion.
func readFileForGate(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// excerpt trims a long string so a failure message stays readable.
func excerpt(s string) string {
	const max = 200
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
