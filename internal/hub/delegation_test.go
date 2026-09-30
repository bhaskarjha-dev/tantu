package hub

import (
	"regexp"
	"strings"
	"testing"
)

// The dashboard binds every user action from one delegated listener on
// `document` rather than from inline handlers (a CSP requirement). That makes
// the identity of the element a handler reads from a single shared fact, and
// the wrong fact fails silently in a way no other test can see: the case still
// matches, the handler still runs, and it throws before doing anything.
//
// `this` inside a listener attached to `document` *is* `document`. `document`
// has no `getAttribute`, so `this.getAttribute(...)` raised
// "TypeError: this.getAttribute is not a function" on every click of the
// dashboard's "Cancel sign-in" button. The sign-in stayed stuck, no message
// appeared, and the suite stayed green because nothing had ever clicked it.
//
// These two checks are gates, not evidence of working UI — the browser harness
// in tools/uxtest is what exercises behaviour. They exist because a gate is
// cheaper than a diagnosis and can run on every commit.

const (
	clickListenerAnchor = `document.addEventListener('click', function(event) {`
	nextListenerAnchor  = `document.addEventListener('submit',`
)

// delegatedClickHandler returns the body of the single delegated click
// listener. It is delimited by the listener that follows it rather than by
// brace counting, so a string or template literal containing a brace cannot
// shift the boundary.
func delegatedClickHandler(t *testing.T) string {
	t.Helper()
	start := strings.Index(dashboardHTML, clickListenerAnchor)
	if start < 0 {
		t.Fatal("delegated click listener not found: the dashboard no longer binds actions from a single document-level listener, so the delegation invariants below cannot be checked")
	}
	end := strings.Index(dashboardHTML[start:], nextListenerAnchor)
	if end < 0 {
		t.Fatal("delegated click listener is not followed by the submit listener: cannot locate the end of the click handler")
	}
	return dashboardHTML[start : start+end]
}

// TestDelegatedClickHandlerNeverReadsFromThis pins the identity a delegated
// handler reads element attributes from. Every case must read `target` — the
// element that matched `[data-action]` — never `this`.
func TestDelegatedClickHandlerNeverReadsFromThis(t *testing.T) {
	body := delegatedClickHandler(t)

	// `this` in an addEventListener callback is the EventTarget the listener is
	// attached to, which here is `document`. Any use of it is either this bug
	// or a misunderstanding of the same fact.
	if idx := strings.Index(body, "this."); idx >= 0 {
		line := body[:idx]
		if len(line) > 160 {
			line = line[len(line)-160:]
		}
		t.Errorf("the delegated click handler reads from `this`, which is `document` in this listener and has no getAttribute; use `target` (the matched [data-action] element)\noffending use preceded by ...%s", line)
	}
}

// TestEveryDataActionHasAHandler guards the other half of the same wiring: an
// action rendered into the DOM with no matching dispatch entry is a button
// that does nothing at all. The page generates many of these rows from data
// (peers, received items, transfers, relays), so a missing entry is easy to
// introduce and impossible to notice without clicking.
func TestEveryDataActionHasAHandler(t *testing.T) {
	body := delegatedClickHandler(t)

	// Handlers in the click switch: `case 'name':`.
	handled := map[string]bool{}
	for _, m := range regexp.MustCompile(`case '([a-z0-9]+(?:-[a-z0-9]+)*)':`).FindAllStringSubmatch(body, -1) {
		handled[m[1]] = true
	}

	// Actions bound by a sibling listener instead of the click switch. These
	// are compared against a literal in their own `getAttribute` check.
	for _, m := range regexp.MustCompile(`getAttribute\('data-action'\) === '([a-z0-9-]+)'`).FindAllStringSubmatch(dashboardHTML, -1) {
		handled[m[1]] = true
	}

	// Every action the page can render, in static markup and in generated
	// HTML alike.
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`data-action\s*=\s*["']([a-z0-9-]+)["']`).FindAllStringSubmatch(dashboardHTML, -1) {
		declared[m[1]] = true
	}

	if len(declared) == 0 {
		t.Fatal("no data-action attributes found in the dashboard page; the extraction is wrong, so this check would pass vacuously")
	}

	var missing []string
	for name := range declared {
		if !handled[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d data-action(s) are rendered with no handler, so those controls do nothing when clicked: %s",
			len(missing), strings.Join(missing, ", "))
	}
}
