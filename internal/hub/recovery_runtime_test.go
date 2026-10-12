package hub

// Gate for limitation 3.6, and for the failure that made it look closed.
//
// Z46 rewrote the session banner to name `tantu dashboard` instead of "press o",
// and its gate passed. The gate was wrong, and it was wrong in exactly the way
// this repository keeps finding: it asserted on a substring of the static markup.
//
// The page has four runtime messages that write the same banner element. Three
// of them -- the 403 a user gets when their one-time link was already used, the
// 503 when the Hub has too many sessions, and the "cannot reach the Hub" case --
// assign to `dashboardBlockReason`, which is then copied over the banner's text
// at line 4242. So the copy Z46 wrote was replaced at runtime in every one of
// those cases, and a user whose link had expired -- the most likely reason to be
// looking at that banner at all -- was told to press `o` in a terminal they may
// not have open. That is the original 3.6 defect, still shipped.
//
// It was found by opening the real dashboard in a browser against a real Hub and
// reading the banner. No gate noticed, because every gate looked at markup that
// this code overwrites.
//
// So this gate reads the *assignments* rather than the markup. A message that can
// be displayed must name a recovery command that works from wherever the reader
// is: `tantu dashboard` finds the running Hub from any directory, while `o`
// exists only inside the Hub's terminal.

import (
	"strings"
	"testing"
)

// dashboardBlockReasonAssignment finds every `dashboardBlockReason = '...'`
// literal in the page's script.
func dashboardBlockReasonAssignments(t *testing.T) []string {
	t.Helper()
	code := stripDashboardComments(dashboardHTML)
	const marker = "dashboardBlockReason = '"
	var out []string
	for {
		i := strings.Index(code, marker)
		if i < 0 {
			break
		}
		rest := code[i+len(marker):]
		j := strings.Index(rest, "'")
		if j < 0 {
			t.Fatal("a dashboardBlockReason assignment has no closing quote; the page's script is malformed")
		}
		out = append(out, rest[:j])
		code = rest[j:]
	}
	// The declaration initialises the variable to the empty string, which is not
	// a message anyone is shown. Counting it produced a failure against `""`,
	// which is a bug in this gate rather than in the page.
	kept := out[:0]
	for _, m := range out {
		if strings.TrimSpace(m) != "" {
			kept = append(kept, m)
		}
	}
	out = kept
	if len(out) == 0 {
		t.Fatal("no non-empty dashboardBlockReason assignments found; if they were renamed, point this gate at the new mechanism")
	}
	return out
}

func TestEveryRuntimeRecoveryMessageNamesACommandThatWorks(t *testing.T) {
	msgs := dashboardBlockReasonAssignments(t)
	for _, m := range msgs {
		if !strings.Contains(m, "tantu dashboard") {
			t.Errorf("a recovery message a user can be shown does not name `tantu dashboard`, so it can "+
				"only be acted on from the Hub terminal: %q", m)
		}
		// "Press o" alone is the 3.6 defect. It may appear as a secondary hint
		// once the command is named, but never as the only instruction.
		if strings.Contains(m, "reload the page the Hub opened for you") {
			t.Errorf("a recovery message still offers the advice that fails for anyone who reached the "+
				"page by hand: %q", m)
		}
	}
}

// TestTheBannerIsNotSilentlyOverwritten pins the mechanism, not the copy. The
// copy is asserted above; this asserts that the block reason still overwrites
// the banner at all -- because if that line were removed, the runtime messages
// would stop being reachable and this file's first gate would pass while the page
// showed something else entirely.
func TestTheBannerIsNotSilentlyOverwritten(t *testing.T) {
	code := stripDashboardComments(dashboardHTML)
	if !strings.Contains(code, "document.getElementById('sessionBannerDetail').textContent = dashboardBlockReason") {
		t.Error("the page no longer copies dashboardBlockReason into the banner. Either that was " +
			"removed on purpose, or the runtime messages are no longer reachable -- either way this " +
			"gate's premise has changed and needs rechecking.")
	}
}
