package hub

// Batch Z32 gates: the dashboard must not reach for a native dialog, every
// empty state must offer a next action, and the in-page notification and
// confirmation surfaces must keep the accessibility contract that a native
// alert() never had.
//
// Each check here is marker-based on the served document. That is deliberate:
// the defects these guard against are invisible to the Go suite's behaviour
// tests (which exercise the HTTP API, not the page) and were only ever
// discoverable by clicking. The browser acceptance harness
// (tools/uxtest/run.mjs) is the runtime half - it drives the same controls and
// fails the run on any native dialog.

import (
	"regexp"
	"strings"
	"testing"
)

// stripDashboardComments removes JavaScript and CSS comments so a prose mention
// of alert() in a rationale cannot satisfy - or trip - the native-dialog gate.
//
// It is deliberately simple and deliberately conservative: block comments are
// tracked across lines, a whole-line `//` comment is dropped, and a trailing
// comment is dropped only when the `//` is not preceded by a quote on that line,
// so a `//` inside a string literal survives.
func stripDashboardComments(src string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		out := ""
		for i := 0; i < len(line); i++ {
			if inBlock {
				if strings.HasPrefix(line[i:], "*/") {
					inBlock = false
					i++
				}
				continue
			}
			switch {
			case strings.HasPrefix(line[i:], "/*"):
				inBlock = true
				i++
			case strings.HasPrefix(line[i:], "//"):
				// Only a real comment: a `//` inside a quote is content.
				if !quoteBefore(out, len(out)) {
					i = len(line)
				} else {
					out += "//"
					i++
				}
			default:
				out += string(line[i])
			}
		}
		b.WriteString(out)
		b.WriteByte('\n')
	}
	return b.String()
}

func quoteBefore(line string, idx int) bool {
	var single, double, backtick bool
	for i := 0; i < idx && i < len(line); i++ {
		switch line[i] {
		case '\'':
			if !double {
				single = !single
			}
		case '"':
			if !single {
				double = !double
			}
		case '`':
			if !single && !double {
				backtick = !backtick
			}
		case '\\':
			if single || double || backtick {
				i++
			}
		}
	}
	return single || double || backtick
}

// nativeDialogCall matches a real call to a browser-modal dialog. The word
// boundary is what keeps the shims (`askConfirm`, `askPrompt`) and the
// identically-prefixed product functions (`promptChangeDownloadDir`) out of the
// match set.
var nativeDialogCall = regexp.MustCompile(`\b(?:window\.)?(alert|confirm|prompt)\s*\(`)

// TestWebDashboard_NoNativeDialogCalls is the gate for the defect this batch
// removed: every failure path in the page called alert(), so a refused alias
// edit, a failed folder open and a rejected unpair each froze the whole UI
// behind an OS-owned modal that ignored the app's themes and told a
// screen-reader user nothing about what to do next.
func TestWebDashboard_NoNativeDialogCalls(t *testing.T) {
	code := stripDashboardComments(dashboardHTML)

	matches := nativeDialogCall.FindAllStringSubmatchIndex(code, -1)
	if len(matches) > 0 {
		var where []string
		for _, m := range matches {
			start := m[0]
			line := 1 + strings.Count(code[:start], "\n")
			where = append(where, code[m[2]:m[3]]+"() at page line "+itoa(line))
		}
		t.Errorf("dashboard calls %d native dialog(s): %s. Use notify()/askConfirm()/askPrompt() so the outcome is themed, non-modal and announced.",
			len(matches), strings.Join(where, "; "))
	}

	// The replacements must actually exist, or the gate above would pass on a
	// page that simply lost its error reporting.
	for _, want := range []string{"function notify(", "function askConfirm(", "function askPrompt(", "function dismissToast("} {
		if !strings.Contains(code, want) {
			t.Errorf("dashboard no longer defines %s, so removing native dialogs also removed the reporting", want)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestWebDashboard_NotificationRegionsAreAnnounced proves the notification
// surface is two live regions rather than one. Errors must be assertive - the
// user's action produced nothing - while success and progress must be polite,
// or a routine send interrupts whatever the user was reading.
func TestWebDashboard_NotificationRegionsAreAnnounced(t *testing.T) {
	for _, want := range []string{
		`id="toastRegionPolite" role="status" aria-live="polite"`,
		`id="toastRegionAssertive" role="alert" aria-live="assertive"`,
	} {
		if !strings.Contains(dashboardHTML, want) {
			t.Errorf("dashboard is missing notification region %q", want)
		}
	}

	// Errors must not auto-vanish: a failure the user never saw is a failure
	// they will repeat. WCAG 2.2.1 also requires the timer to stop while the
	// notification is being read or operated.
	if !strings.Contains(stripDashboardComments(dashboardHTML), "const TOAST_TIMEOUT_MS = { info: 6000, success: 6000, warn: 10000, error: 0 };") {
		t.Error("error notifications must have no auto-dismiss timeout; the timeout table has changed")
	}
	for _, want := range []string{"'mouseenter', pause", "'focusin', pause", "'mouseleave', schedule", "'focusout', schedule"} {
		if !strings.Contains(dashboardHTML, want) {
			t.Errorf("toast auto-dismiss is not paused on hover/focus (%q missing), so a notification can disappear while it is being read", want)
		}
	}

	// Notification text is built with textContent, never innerHTML: the message
	// and the next action both carry Hub and peer-supplied strings.
	if strings.Contains(stripDashboardComments(dashboardHTML), "text.innerHTML") {
		t.Error("a notification builds its message with innerHTML; peer-supplied text would then be interpreted as markup")
	}
}

// TestWebDashboard_ActionDialogMechanics pins the contract the native dialogs
// did not have: a labelled modal, Escape to cancel, a trapped Tab, and focus
// returned to the opener rather than dumped on <body>.
func TestWebDashboard_ActionDialogMechanics(t *testing.T) {
	code := stripDashboardComments(dashboardHTML)

	for _, want := range []string{
		`id="actionDialog"`,
		`role="dialog"`,
		`aria-modal="true"`,
		`aria-labelledby="actionDialogTitle"`,
		`aria-describedby="actionDialogBody"`,
		"function closeActionDialog(",
		"function dialogFocusables()",
		"function submitActionDialog()",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("action dialog is missing %q", want)
		}
	}

	// Escape and the Tab trap must both be handled while it is open.
	if !strings.Contains(code, "event.key === 'Escape' && actionDialog && actionDialog.style.display !== 'none'") {
		t.Error("Escape does not cancel the action dialog")
	}
	if !strings.Contains(code, "actionDialog.style.display !== 'none' && event.key === 'Tab'") {
		t.Error("Tab is not trapped inside the action dialog")
	}
	// Focus restoration is the part a native alert() did badly: it left focus
	// on the document, so keyboard use restarted from the top of the page.
	for _, want := range []string{"dialogReturnFocus", "dialogOpenerSelector", "el === document.body"} {
		if !strings.Contains(code, want) {
			t.Errorf("action dialog focus restoration does not guard %q", want)
		}
	}
	// A second confirmation must not stack on top of the first: two open
	// dialogs is how a user approves the wrong request.
	if !strings.Contains(code, "if (dialogResolve) closeActionDialog(") {
		t.Error("opening a second dialog does not resolve the first one as cancelled")
	}
}

// TestWebDashboard_EmptyStatesOfferANextAction closes limitation 4.3. An empty
// list that only says "nothing here" leaves the user with no route forward;
// the plan's own rule is one next action per empty state.
func TestWebDashboard_EmptyStatesOfferANextAction(t *testing.T) {
	regions := []struct {
		id    string
		needs string
	}{
		{"receivedDropsList", "load-recent"},
		{"authorizationsList", "goto-relay"},
		{"transfersList", "goto-drop"},
		{"peersList", "open-pair-modal"},
	}
	for _, r := range regions {
		inner := elementInnerHTML(t, r.id)
		if !strings.Contains(inner, `class="empty-state"`) {
			t.Errorf("#%s first paint is not an .empty-state block, so it cannot carry a next action", r.id)
			continue
		}
		if !strings.Contains(inner, "data-action") {
			t.Errorf("#%s offers an empty state with no action in it", r.id)
		}
	}

	// The JavaScript re-renders must carry the same affordance, or the next
	// poll silently removes it.
	code := stripDashboardComments(dashboardHTML)
	for _, want := range []string{
		"const EMPTY_TRANSFERS_HTML =",
		"const EMPTY_AUTHORIZATIONS_HTML =",
		"const EMPTY_INBOX_HTML =",
		"function emptyStateHTML(",
		"list.innerHTML = EMPTY_TRANSFERS_HTML;",
		"list.innerHTML = EMPTY_AUTHORIZATIONS_HTML;",
		"list.innerHTML = EMPTY_INBOX_HTML;",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("empty-state re-render lost %q", want)
		}
	}
	for _, action := range []string{"goto-drop", "goto-relay", "goto-peers", "refresh-discovery"} {
		if !strings.Contains(code, "case '"+action+"':") {
			t.Errorf("empty-state action %q has no handler", action)
		}
	}

	// The old text-only placeholders must be gone: they read as a dead end and
	// they are what a marker would otherwise keep passing on.
	for _, gone := range []string{"No transfers yet.<br>", "No authorizations yet.<br>", "No received items yet.<br>"} {
		if strings.Contains(dashboardHTML, gone) {
			t.Errorf("legacy text-only empty state %q is still present", gone)
		}
	}
}

// TestWebDashboard_PeerActionsAreConfirmedInPage documents the specific
// surfaces limitation 4.1 named. Each of these used to reach confirm() or
// prompt(); the gate fails if one silently reverts.
func TestWebDashboard_PeerActionsAreConfirmedInPage(t *testing.T) {
	code := stripDashboardComments(dashboardHTML)
	for _, want := range []string{
		"await askConfirm(unpairConsequence(name))",
		"await askConfirm(approvePairingConsequence(lastPendingPairingName))",
		"await askPrompt({",
		"danger: true",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("peer action confirmation is missing %q", want)
		}
	}
	// Every peer-management failure must report through notify rather than
	// disappearing: an alias edit that silently failed looked identical to one
	// that succeeded.
	for _, fn := range []string{"switchActivePeer", "promptEditAlias", "setDefaultPeer", "confirmUnpair", "openDownloadFolder", "promptChangeDownloadDir"} {
		body := functionBody(t, code, fn)
		if !strings.Contains(body, "notify('error'") {
			t.Errorf("%s has no failure notification; a refused call would be silent", fn)
		}
		if nativeDialogCall.MatchString(body) {
			t.Errorf("%s calls a native dialog", fn)
		}
	}
}

// functionBody extracts one top-level function's source from comment-stripped
// JavaScript by brace matching. It is intentionally a text tool: the checks
// above are about the shape of the product source, not about executing it.
func functionBody(t *testing.T, code, name string) string {
	t.Helper()
	start := strings.Index(code, "function "+name+"(")
	if start < 0 {
		t.Fatalf("function %s not found in the dashboard script", name)
	}
	open := strings.Index(code[start:], "{")
	if open < 0 {
		t.Fatalf("function %s has no body", name)
	}
	depth, i := 0, start+open
	for ; i < len(code); i++ {
		switch code[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return code[start : i+1]
			}
		}
	}
	t.Fatalf("function %s body is not brace-balanced", name)
	return ""
}

// elementInnerHTML returns the markup between the opening tag of the element
// with the given id and its matching closing tag, so a check can be scoped to
// one region instead of the whole page.
func elementInnerHTML(t *testing.T, id string) string {
	t.Helper()
	openRe := regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9]*)[^<>]*\bid="` + regexp.QuoteMeta(id) + `"[^<>]*>`)
	loc := openRe.FindStringIndex(dashboardHTML)
	if loc == nil {
		t.Fatalf("element #%s not found in the dashboard page", id)
	}
	tag := openRe.FindStringSubmatch(dashboardHTML)[1]
	if strings.HasSuffix(strings.TrimSpace(dashboardHTML[loc[0]:loc[1]]), "/>") {
		t.Fatalf("element #%s is self-closing; it cannot contain an empty state", id)
	}
	rest := dashboardHTML[loc[1]:]
	next := regexp.MustCompile(`</?` + tag + `\b`)
	depth := 1
	for offset := 0; offset < len(rest); {
		m := next.FindStringIndex(rest[offset:])
		if m == nil {
			break
		}
		abs := offset + m[0]
		if strings.HasPrefix(rest[abs:], "</") {
			depth--
			if depth == 0 {
				return rest[:abs]
			}
		} else if !isSelfClosingTag(rest, abs) {
			depth++
		}
		gt := strings.Index(rest[abs:], ">")
		if gt < 0 {
			break
		}
		offset = abs + gt + 1
	}
	t.Fatalf("element #%s has no matching closing tag", id)
	return ""
}

// isSelfClosingTag reports whether the tag beginning at pos closes itself.
func isSelfClosingTag(src string, pos int) bool {
	end := strings.Index(src[pos:], ">")
	if end < 0 {
		return false
	}
	return strings.HasSuffix(strings.TrimSpace(src[pos:pos+end]), "/")
}
