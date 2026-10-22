package webui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The pages and the pages' sources. The node's two are Go string
// constants, so the source is what there is to read; the page is inside
// it.
var pageFiles = []struct {
	name string
	path string
}{
	{"relay interstitial", "../hub/relay_interstitial.html"},
	{"relay blocked", "../hub/relay_blocked.html"},
	{"relay ok", "relay_ok.html"},
	{"relay failed", "relay_failed.html"},
	{"quickdrop page", "../../cmd/tantu/drop.go"},
	{"relay page", "../../cmd/tantu/relay.go"},
}

func readPages(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range pageFiles {
		body, err := os.ReadFile(p.path)
		if err != nil {
			t.Errorf("reading %s: %v", p.path, err)
			continue
		}
		out[p.name] = string(body)
	}
	return out
}

// Every mark a page asks for is a mark the design system has.
//
// A page asks for a mark with `class="ico ico--<name>"`, and the
// mapping from that name to the drawing is a rule in the theme. If the
// theme has no such a rule, the page is not wrong in any way a reader
// can see: the element is there, it has the right size, the text is
// right, and the mark is simply not painted. The browser says so only
// in a console message, and a page nobody looked at is a page whose
// console nobody read.
//
// This is the same shape as the bug the node's pages had: their
// Content-Security-Policy refused every data: mask, so every icon on
// them was invisible, and the layout was right.
func TestEveryMarkAPageAsksForExists(t *testing.T) {
	asks := regexp.MustCompile(`ico--([a-z0-9-]+)`)
	for name, body := range readPages(t) {
		seen := map[string]bool{}
		for _, m := range asks.FindAllStringSubmatch(body, -1) {
			glyph := m[1]
			if seen[glyph] {
				continue
			}
			seen[glyph] = true
			if !strings.Contains(ThemeCSS, "--icon-"+glyph+":") {
				t.Errorf("%s asks for the mark .ico--%s and the design system has no such a glyph, so it paints nothing", name, glyph)
			}
		}
	}
}

// Every mapping in the theme points at a glyph the theme has.
//
// The other direction of the same check: a rule that maps a name to a
// glyph that does not exist paints nothing, and it does so for every
// page at once.
func TestEveryMappingPointsAtAGlyphThatExists(t *testing.T) {
	mapped := regexp.MustCompile(`(?m)^\s*\.ico--([a-z0-9-]+)\s*\{`).FindAllStringSubmatch(ThemeCSS, -1)
	if len(mapped) == 0 {
		t.Fatal("the theme maps no marks at all; the extraction lost them")
	}
	for _, m := range mapped {
		name := m[1]
		if !strings.Contains(ThemeCSS, "--icon-"+name+":") {
			t.Errorf("the theme maps .ico--%s to a glyph it does not define", name)
		}
	}
}

// The design system is one file, and the pages that use it say so.
//
// The node's pages carried a second palette -- a third set of values,
// hand-copied -- and the node's two pages each carried their own copy
// of the same layout. Nothing detected that: the pages rendered, the
// words were right, and the difference was invisible to everyone
// except somebody comparing the two palettes side by side.
//
// So: a page that defines its own colours is a page that has left the
// design system, and this says so.
func TestPagesDoNotCarryTheirOwnPalette(t *testing.T) {
	for name, body := range readPages(t) {
		if strings.Contains(body, ":root {") {
			t.Errorf("%s defines its own :root block; a page that carries its own colours has left the design system", name)
		}
		if !strings.Contains(body, "{{DESIGN_SYSTEM}}") {
			t.Errorf("%s does not include the shared design system", name)
		}
	}
}

// The design system marker only ever appears as a style include, or as the
// literal the server substitutes it with.
//
// The marker is a hole in the page, and the server fills it with the whole
// design system -- tokens, type scale, icon set and components. If the
// marker ever appears somewhere else, the server fills that hole too, and
// the page carries the system twice: the same rule written twice, which is
// the same defect as two owners, with the second copy the one that dies
// quietly.
//
// A page file may hold more than one marker -- the node's relay command
// serves two pages, the relay page and the dead end in front of it -- so
// the count is not the rule. The shape is.
func TestTheDesignSystemMarkerOnlyEverAppearsAsAStyleInclude(t *testing.T) {
	const marker = "{{DESIGN_SYSTEM}}"
	for name, body := range readPages(t) {
		if !strings.Contains(body, marker) {
			t.Errorf("%s does not include the shared design system", name)
			continue
		}
		// Every occurrence must be a style include, or the quoted literal
		// the server substitutes it with. Strip those and nothing may be
		// left over.
		leftover := strings.ReplaceAll(body, "<style>"+marker+"</style>", "")
		leftover = strings.ReplaceAll(leftover, `"`+marker+`"`, "")
		if strings.Contains(leftover, marker) {
			t.Errorf("%s uses the design system marker somewhere other than a <style> include", name)
		}
	}
}

// The design system is served as one stylesheet, so a page cannot get the
// tokens without the components.
//
// This is the assertion that keeps the component layer honest. When the
// theme and the components were two markers, a page could include one and
// not the other, and the node's pages did exactly that: they used
// `.empty-state` and had no definition of it anywhere they could reach.
func TestTheDesignSystemCarriesBothTheThemeAndTheComponents(t *testing.T) {
	ds := DesignSystemCSS()
	for _, want := range []string{
		"--accent:",    // the tokens
		"--icon-drop:", // the icon set
		".sr-only {",   // the components
		".empty-state {",
		".btn-primary {",
	} {
		if !strings.Contains(ds, want) {
			t.Errorf("the served design system does not carry %q", want)
		}
	}
}
