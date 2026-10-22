package main

// Gates for the node's two pages: the QuickDrop page `tantu drop` serves
// and the relay page `tantu relay` serves.
//
// These pages are the least-covered surfaces in the product. The browser
// harness proved they were not broken -- they rendered, they did not
// overflow, their contrast was fine -- and nothing at all proved they were
// any good. The specific things that were wrong:
//
//   - the drop zone was a div with a click handler: no role, no tabindex,
//     no key handler. The file input was display:none. So the ONE control
//     the page exists for was completely unreachable with a keyboard.
//   - the status lines were not live regions, so the outcome of a send was
//     painted and never announced.
//   - the two text fields had placeholder-only labels. A placeholder
//     disappears the moment the field is used, so the field loses its name
//     exactly when the reader needs it.
//   - the empty state carried the class `.empty-state`, and the only
//     definition of that class in the product lived in the dashboard's
//     stylesheet, which these pages never load. So the empty state
//     rendered as an unstyled paragraph of body text.
//   - the primary buttons were `.action-btn` / `.submit-btn`: a hand-copied
//     third copy of the product's primary button, a different size and
//     shape from the dashboard's.
//
// None of these is visible in a diff. Every one of them is visible on a
// screen, or to a reader who is not using a mouse and a pair of eyes.
//
// These gates read the SERVED page -- the page the browser parses --
// because a gate about the source file is a gate about the wrong thing.
//
// Every check below is a function that returns the problems it finds, and
// every one of them has a red proof at the bottom of this file: broken
// input, and an assertion that the checker complains. A gate that has
// never been seen to fail is a gate that has never been tested.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/webui"
)

// ---- The page as the browser receives it -----------------------------------

// nodePageCSSText is the node's shared shell, as the pages load it.
func nodePageCSSText() string { return nodePageCSS }

// stripCSSComments removes /* */ comments from a stylesheet.
//
// A stylesheet's comments are prose about the code, and a gate that reads
// them finds the words the prose uses. This gate once failed because the
// design system's own comment mentioned the two class names it had just
// retired -- the prose was correct and the gate was wrong.
func stripCSSComments(src string) string {
	re := regexp.MustCompile(`(?s)/\*.*?\*/`)
	return re.ReplaceAllString(src, "")
}

// stripStyleBlocks removes the <style> blocks from a served page, leaving
// the markup and the script.
//
// The class names a page uses live in the markup. A class name mentioned
// in a stylesheet's comment, or in the design system's own prose, is not a
// class the page uses.
func stripStyleBlocks(page string) string {
	re := regexp.MustCompile(`(?s)<style[^>]*>.*?</style>`)
	return re.ReplaceAllString(page, "")
}

// nodePages returns the served markup of the node's two pages.
//
// The pages are Go string constants, so the "served" page is the constant
// with the design system substituted in -- the same substitution the server
// performs.
func nodePages(t *testing.T) map[string]string {
	t.Helper()
	ds := webui.DesignSystemCSS()
	pages := map[string]string{
		"quickdrop": dropPageHTML,
		"relay":     relayPageHTML,
	}
	out := map[string]string{}
	for name, body := range pages {
		if !strings.Contains(body, "{{DESIGN_SYSTEM}}") {
			t.Fatalf("the %s page does not include the shared design system", name)
		}
		served := strings.ReplaceAll(body, "{{DESIGN_SYSTEM}}", ds)
		if strings.Contains(served, "{{DESIGN_SYSTEM}}") {
			t.Fatalf("the %s page still carries an unsubstituted design system marker", name)
		}
		out[name] = served
	}
	return out
}

// ---- The checks ------------------------------------------------------------

// keyboardReachabilityProblems finds controls the reader cannot reach.
//
// The drop zone is the one that matters: it is a div with a click handler,
// and the file input beside it is display:none. A keyboard that cannot
// reach the drop zone cannot send a file at all -- the page's only job.
//
// The rule is checked on the rendered page, because the failure this
// guards against is a rendering failure: the element is in the markup and
// is unreachable in the browser.
func keyboardReachabilityProblems(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<(\w+)([^>]*\bonclick=[^>]*)>`).FindAllStringSubmatch(body, -1) {
		tag, attrs := m[1], m[2]
		switch tag {
		case "input", "button", "a", "select", "textarea":
			continue // natively focusable
		}
		if !strings.Contains(attrs, `tabindex="0"`) {
			out = append(out, "a <"+tag+"> with a click handler cannot be reached by keyboard: it has no tabindex")
		}
		if !strings.Contains(attrs, `role="button"`) {
			out = append(out, "a <"+tag+"> with a click handler has no role, so a screen reader announces it as a plain group")
		}
	}
	return out
}

// keyboardActivationProblems finds controls that can be focused but not
// operated.
//
// A focusable control that does nothing on Enter or Space is worse than
// one that is not focusable: the reader can see it, tab to it, and be
// told nothing happened.
func keyboardActivationProblems(body string) []string {
	var out []string
	idx := strings.Index(body, `id="dropZone"`)
	if idx < 0 {
		return []string{"the QuickDrop page has no drop zone"}
	}
	start := strings.LastIndex(body[:idx], "<")
	tag := body[start : strings.Index(body[idx:], ">")+idx+1]
	for _, want := range []string{`tabindex="0"`, `role="button"`, "onkeydown"} {
		if !strings.Contains(tag, want) {
			out = append(out, "the drop zone's tag is missing "+want)
		}
	}
	handler := strings.Index(body, "onkeydown")
	if handler < 0 {
		return append(out, "the drop zone has no key handler")
	}
	end := handler + 400
	if end > len(body) {
		end = len(body)
	}
	window := body[handler:end]
	for _, key := range []string{"Enter", "' '"} {
		if !strings.Contains(window, key) {
			out = append(out, "the drop zone's key handler does not handle "+key)
		}
	}
	return out
}

// missingLabelProblems finds fields with no label.
//
// A placeholder is the field's instruction, not its name. It disappears
// the moment the field is used, so a screen reader reads an empty name and
// a person looking back at an empty field reads no label at all. WCAG
// 3.3.2 and 4.1.2 both want a real one.
func missingLabelProblems(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<(input|textarea)\b[^>]*id="([^"]+)"[^>]*>`).FindAllStringSubmatch(body, -1) {
		tag, id := m[1], m[2]
		if tag == "input" && strings.Contains(m[0], `type="file"`) {
			continue // display:none, driven by the drop zone
		}
		hasLabel := strings.Contains(body, `for="`+id+`"`)
		hasAria := strings.Contains(m[0], `aria-label=`) || strings.Contains(m[0], `aria-labelledby=`)
		if !hasLabel && !hasAria {
			out = append(out, "the "+tag+"#"+id+" has no label: no <label for>, no aria-label. A placeholder is not a label -- it disappears when the field is used")
		}
	}
	return out
}

// missingLiveRegionProblems finds status lines that are painted and never
// announced.
//
// The dashboard's status lines have been live regions for a while. The
// node's pages did not, so sending a snippet said "Sent successfully!" on
// the screen and said nothing at all to a screen reader.
func missingLiveRegionProblems(body string) []string {
	var out []string
	for _, id := range []string{"textStatus", "fileStatus", "sessionBanner"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			continue // not on this page
		}
		idx := strings.Index(body, `id="`+id+`"`)
		start := strings.LastIndex(body[:idx], "<")
		tag := body[start : strings.Index(body[idx:], ">")+idx+1]
		if !strings.Contains(tag, `role="status"`) && !strings.Contains(tag, `aria-live=`) {
			out = append(out, "the #"+id+" is not a live region, so its message is painted and never announced")
		}
	}
	return out
}

// legacyButtonProblems finds the node pages' hand-copied buttons.
func legacyButtonProblems(markup string) []string {
	var out []string
	for _, legacy := range []string{"action-btn", "submit-btn", "small-btn"} {
		if strings.Contains(markup, legacy) {
			out = append(out, "the page still uses "+legacy+", which is a hand-copied copy of the product's button")
		}
	}
	return out
}

// inlineOutlineSuppressProblems finds focusable elements that remove their
// own focus ring.
//
// An inline declaration outranks the design system's focus ring, so the
// rule is present and doing nothing.
func inlineOutlineSuppressProblems(body string) []string {
	re := regexp.MustCompile(
		`(?i)<(?:button|input|select|textarea|a)\b[^>]*\sstyle\s*=\s*"[^"]*\boutline\s*:\s*(?:none|0)\b`)
	var out []string
	for _, m := range re.FindAllString(body, -1) {
		out = append(out, "a focusable element carries an inline outline suppression: "+m)
	}
	return out
}

// ---- The gates -------------------------------------------------------------

// The node's pages must carry the design system's components, because the
// components are what makes a node page a Tantu page rather than a page
// that happens to share a name.
//
// The empty state is the proof. It is a class the node pages USE, and it
// used to have no definition anywhere they could reach, so it rendered as
// an ordinary paragraph. This asserts the rule is reachable AND that it
// reaches these pages.
func TestNodePagesCarryTheDesignSystemsComponents(t *testing.T) {
	ds := webui.DesignSystemCSS()
	for _, want := range []string{".empty-state {", ".sr-only {", ".btn-primary {", "[role=\"button\"]:focus-visible"} {
		if !strings.Contains(ds, want) {
			t.Errorf("the design system does not carry %q, so the node's pages cannot use it", want)
		}
	}
	for name, body := range nodePages(t) {
		if !strings.Contains(body, webui.ComponentsCSS) {
			t.Errorf("the %s page does not carry the design system's components", name)
		}
	}
}

func TestNodePagesAreKeyboardReachable(t *testing.T) {
	for name, body := range nodePages(t) {
		for _, p := range keyboardReachabilityProblems(body) {
			t.Errorf("the %s page: %s. Give it role=\"button\" and tabindex=\"0\", and a key handler "+
				"that activates it.", name, p)
		}
	}
}

func TestTheDropZoneActivatesFromTheKeyboard(t *testing.T) {
	for _, p := range keyboardActivationProblems(nodePages(t)["quickdrop"]) {
		t.Error(p)
	}
}

func TestNodePageFieldsHaveLabels(t *testing.T) {
	for name, body := range nodePages(t) {
		for _, p := range missingLabelProblems(body) {
			t.Errorf("the %s page: %s", name, p)
		}
	}
}

func TestNodePageStatusLinesAreLiveRegions(t *testing.T) {
	for name, body := range nodePages(t) {
		for _, p := range missingLiveRegionProblems(body) {
			t.Errorf("the %s page: %s", name, p)
		}
	}
}

func TestNodePagesUseTheSharedButtonVocabulary(t *testing.T) {
	for name, body := range nodePages(t) {
		for _, p := range legacyButtonProblems(stripStyleBlocks(body)) {
			t.Errorf("the %s page: %s. Use the design system's .btn .btn-primary / .btn .btn-sm instead.",
				name, p)
		}
	}
}

// The node shell must not define a primitive the design system owns.
//
// The shell used to carry its own `.sr-only`, its own focus ring and its
// own button treatment. The button treatment is gone; this pins the other
// two, and pins the empty state too -- the node pages USE `.empty-state`,
// and for a while nothing anywhere they could reach defined it.
func TestTheNodeShellDoesNotRedefineADesignSystemPrimitive(t *testing.T) {
	shell := stripCSSComments(nodePageCSSText())
	for _, owner := range []string{".sr-only", ".empty-state", ".btn-primary", ".btn-sm"} {
		if strings.Contains(shell, owner+" {") {
			t.Errorf("the node's shell defines %s, which the design system owns. A hand-copied value "+
				"is a defect the day the owner moves.", owner)
		}
	}
	// The shell may still style the SURFACE -- the card, the sections, the
	// drop zone -- as long as it does not restate a shared primitive.
	if !strings.Contains(shell, ".drop-zone {") {
		t.Error("the node's shell no longer styles the drop zone; the page's own shape has moved " +
			"somewhere it does not belong")
	}
}

// Every control in the product must inherit the product's typeface.
//
// A control that does not say which typeface it uses gets the browser's
// default, and the browser's default is not the product's. This is the
// defect the dashboard had: its text inputs and its tab bar resolved to
// Arial while the rest of the page used the design system's stack.
func TestEveryControlInheritsTheProductTypeface(t *testing.T) {
	ds := webui.DesignSystemCSS()
	if !strings.Contains(ds, "font-family: inherit;") {
		t.Error("the design system does not tell form controls to inherit the typeface, so a control " +
			"that does not set one gets the browser's default -- which is not the product's")
	}
	// The node shell sets the page's font; a control that sets its own
	// family instead of inheriting is the defect. The comments are
	// stripped first: a stylesheet's prose is not a declaration.
	shell := stripCSSComments(nodePageCSSText())
	for _, m := range regexp.MustCompile(`(?s)(^|[,{}])\s*(input|textarea|button|select)\b[^{]*\{([^}]*)\}`).FindAllStringSubmatch(shell, -1) {
		if strings.Contains(m[3], "font-family:") && !strings.Contains(m[3], "font-family: inherit;") {
			t.Errorf("the node's shell sets a literal font-family on a %s rule:\n  %s", m[2], strings.TrimSpace(m[3]))
		}
	}
}

func TestNoNodePageControlSuppressesItsFocusRing(t *testing.T) {
	for name, body := range nodePages(t) {
		for _, p := range inlineOutlineSuppressProblems(body) {
			t.Errorf("the %s page: %s", name, p)
		}
	}
}

// The narrowest phone must still have a usable card.
//
// The node pages carried 2.5rem of padding at every width, so on a 320px
// screen the content column was 206px -- a phone-width page with a column
// narrower than a phone.
func TestTheNodeCardMakesRoomAtTheNarrowestWidth(t *testing.T) {
	shell := stripCSSComments(nodePageCSSText())
	if !strings.Contains(shell, "@media (max-width: 420px)") {
		t.Fatal("the node's shell has no narrow-viewport rule, so the card keeps its desktop padding " +
			"on a 320px screen")
	}
	re := regexp.MustCompile(`(?s)@media \(max-width: 420px\)\s*\{(.*?)\n\}`)
	m := re.FindStringSubmatch(shell)
	if m == nil {
		t.Fatal("the node's narrow-viewport rule could not be read")
	}
	if !strings.Contains(m[1], ".card") {
		t.Errorf("the node's narrow-viewport rule does not touch the card:\n%s", m[1])
	}
}

// The node's shell must not carry a colour of its own.
//
// The node's pages once carried a SECOND palette -- a third set of values,
// hand-copied -- in a product whose other pages used the design system.
// The shell may use the tokens; it may not define a new one.
func TestTheNodeShellCarriesNoColourOfItsOwn(t *testing.T) {
	shell := stripCSSComments(nodePageCSSText())
	hex := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	for _, m := range hex.FindAllString(shell, -1) {
		// The one deliberate exception is the white on the accent gradient,
		// which is a contrast requirement rather than a palette.
		if m == "#fff" || m == "#ffffff" {
			continue
		}
		t.Errorf("the node's shell carries the literal colour %s; colours belong to the design system's "+
			"tokens, or a second palette starts here", m)
	}
	if strings.Contains(shell, ":root {") {
		t.Error("the node's shell defines its own :root block; a page that carries its own colours has " +
			"left the design system")
	}
}

// The design system's component layer must be reachable from the node's
// pages as well as the dashboard's, or the two are different products.
func TestTheDesignSystemIsTheSameForEverySurface(t *testing.T) {
	shell := nodePageCSSText()
	if strings.Contains(shell, "--accent-gradient-strong") && !strings.Contains(webui.ThemeCSS, "--accent-gradient-strong") {
		t.Error("the node's shell uses a token the theme does not define")
	}
	if _, err := os.Stat("nodepage.css"); err != nil {
		t.Errorf("the node's shell is not where the build expects it: %v", err)
	}
}

// ---- Red proofs ------------------------------------------------------------
//
// A gate that has never been seen to fail is a gate that has never been
// tested. Each check above is exercised here against input that is
// deliberately wrong, and the test passes only when the check complains.

func TestKeyboardReachabilityCatchesAnUnreachableControl(t *testing.T) {
	// The defect as it was: a div with a click handler and nothing else.
	broken := `<div class="drop-zone" id="dropZone" onclick="pick()">Click here</div>`
	if len(keyboardReachabilityProblems(broken)) == 0 {
		t.Fatal("the keyboard check accepted a click-only div; it would not have caught the defect it " +
			"was written for")
	}
	// And the fixed shape is accepted.
	fixed := `<div class="drop-zone" id="dropZone" role="button" tabindex="0" onclick="pick()">Click here</div>`
	if p := keyboardReachabilityProblems(fixed); len(p) != 0 {
		t.Fatalf("the keyboard check rejected a correctly marked control: %v", p)
	}
}

func TestKeyboardActivationCatchesADeadKeyHandler(t *testing.T) {
	broken := `<div id="dropZone" role="button" tabindex="0" onkeydown="doSomething()">x</div>`
	if len(keyboardActivationProblems(broken)) == 0 {
		t.Fatal("the activation check accepted a key handler that handles neither Enter nor Space")
	}
}

func TestMissingLabelCatchesAPlaceholderOnlyField(t *testing.T) {
	broken := `<input type="text" id="urlInput" placeholder="Paste OAuth URL">`
	if len(missingLabelProblems(broken)) == 0 {
		t.Fatal("the label check accepted a placeholder-only field")
	}
	fixed := `<label class="sr-only" for="urlInput">OAuth URL</label><input type="text" id="urlInput">`
	if p := missingLabelProblems(fixed); len(p) != 0 {
		t.Fatalf("the label check rejected a properly labelled field: %v", p)
	}
}

func TestMissingLiveRegionCatchesASilentStatusLine(t *testing.T) {
	broken := `<div class="status-msg" id="textStatus"></div>`
	if len(missingLiveRegionProblems(broken)) == 0 {
		t.Fatal("the live-region check accepted a status line that is never announced")
	}
	fixed := `<div class="status-msg" id="textStatus" role="status" aria-live="polite"></div>`
	if p := missingLiveRegionProblems(fixed); len(p) != 0 {
		t.Fatalf("the live-region check rejected a proper live region: %v", p)
	}
}

func TestLegacyButtonCheckCatchesTheHandCopiedButton(t *testing.T) {
	if len(legacyButtonProblems(`<button class="action-btn">Send</button>`)) == 0 {
		t.Fatal("the button check accepted a hand-copied button class")
	}
	if p := legacyButtonProblems(`<button class="btn btn-primary">Send</button>`); len(p) != 0 {
		t.Fatalf("the button check rejected the shared vocabulary: %v", p)
	}
}

func TestInlineOutlineCheckCatchesASuppressedRing(t *testing.T) {
	broken := `<button style="outline: none" id="x">x</button>`
	if len(inlineOutlineSuppressProblems(broken)) == 0 {
		t.Fatal("the inline-outline check accepted a control that removes its own focus ring")
	}
	ok := `<button style="outline: 2px solid var(--accent)" id="x">x</button>`
	if p := inlineOutlineSuppressProblems(ok); len(p) != 0 {
		t.Fatalf("the inline-outline check rejected a ring that reinforces the focus indicator: %v", p)
	}
}

func TestStripStyleBlocksRemovesTheDesignSystemsProse(t *testing.T) {
	// The false positive that made this gate wrong once: the design
	// system's comment named the class names the gate was looking for.
	page := `<style>/* .action-btn was retired */ .btn {}</style><button class="btn btn-primary">x</button>`
	if strings.Contains(stripStyleBlocks(page), "action-btn") {
		t.Fatal("the style blocks were not stripped, so a class name in a comment reads as a class in use")
	}
}
