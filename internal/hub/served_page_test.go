package hub

// The page the browser actually receives.
//
// The dashboard's gates used to read `dashboardHTML` -- the embedded source
// file -- and check it for things like a focus ring. That made them gates
// about a FILE rather than about the page.
//
// It matters now that the focus ring, the visually-hidden utility, the
// empty state and the button vocabulary live in the shared design system
// instead of in the dashboard's own stylesheet. The dashboard still has a
// focus ring; it gets it from the stylesheet it loads, the same way the
// node's pages get theirs. A gate that reads only the file reports that
// the dashboard has no focus ring -- and the dashboard has one.
//
// A gate that fails for the wrong reason teaches its readers to ignore it,
// which is worse than not having the gate. So the gates read the SERVED
// page: the embedded source with the design system substituted in, which
// is byte for byte what the browser parses.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/webui"
)

// servedDashboardPage is the dashboard as the server sends it, before the
// per-response {{VERSION}} and {{NONCE}} substitutions.
func servedDashboardPage(t *testing.T) string {
	t.Helper()
	return injectTheme(dashboardHTML)
}

// servedDashboardCSS is every stylesheet the browser receives for the
// dashboard: the shared design system -- tokens, type scale, icon set and
// components -- followed by the dashboard's own rules.
func servedDashboardCSS(t *testing.T) string {
	t.Helper()
	page := servedDashboardPage(t)
	re := regexp.MustCompile(`(?s)<style[^>]*>(.*?)</style>`)
	matches := re.FindAllStringSubmatch(page, -1)
	if len(matches) == 0 {
		t.Fatal("the served dashboard carries no <style> block; the gates below would pass on nothing")
	}
	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		parts = append(parts, m[1])
	}
	joined := strings.Join(parts, "\n")
	// The design system must actually be in there. A page that lost its
	// <style> include would otherwise make every CSS gate below vacuously
	// true or vacuously false, and neither is a result.
	if !strings.Contains(joined, "--accent-gradient-strong") {
		t.Fatal("the served dashboard's stylesheets do not carry the shared design system")
	}
	if !strings.Contains(joined, webui.ComponentsCSS) {
		t.Fatal("the served dashboard's stylesheets carry the theme but not the components")
	}
	return joined
}

// servedDashboardOwnCSS is the CSS the DASHBOARD contributes -- its own
// rules, without the shared design system.
//
// The distinction matters for any gate that reads tokens or colours: the
// design system's own :root block defines --bg, --text and the rest, so a
// gate that searches "the page" for a :root finds the THEME's, and then
// reports that the dashboard redefined it. It did not; the theme did.
func servedDashboardOwnCSS(t *testing.T) string {
	t.Helper()
	page := servedDashboardPage(t)
	ds := webui.DesignSystemCSS()
	re := regexp.MustCompile(`(?s)<style[^>]*>(.*?)</style>`)
	var parts []string
	for _, m := range re.FindAllStringSubmatch(page, -1) {
		if strings.Contains(m[1], ds) {
			continue // the design system's own block
		}
		parts = append(parts, m[1])
	}
	if len(parts) == 0 {
		t.Fatal("the served dashboard carries no stylesheet of its own; the gates below would pass " +
			"on nothing")
	}
	return strings.Join(parts, "\n")
}

// lightThemeBlocks returns the body of every
// "@media (prefers-color-scheme: light) { ... }" block in a stylesheet,
// with the braces matched.
//
// A stylesheet's light theme is a set of OVERRIDES on a base block that
// holds the dark theme's values, so "is this token defined in the light
// theme" is not the same question as "does its name appear after the
// first light media query". A token that is theme-independent -- a
// gutter, a content width -- appears once, in the base, and belongs to
// both themes. A token that appears only inside a light block is a token
// the dark theme was never given.
func lightThemeBlocks(css string) []string {
	var out []string
	needle := "@media (prefers-color-scheme: light)"
	for i := 0; ; {
		j := strings.Index(css[i:], needle)
		if j < 0 {
			return out
		}
		j += i
		open := strings.Index(css[j:], "{")
		if open < 0 {
			return out
		}
		open += j
		depth := 0
		end := -1
		for k := open; k < len(css); k++ {
			switch css[k] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = k
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			return out
		}
		out = append(out, css[open+1:end])
		i = end
	}
}

// cssWithoutLightTheme is the stylesheet with every light-theme block
// removed: what remains is the base, which is the dark theme's values.
func cssWithoutLightTheme(css string) string {
	var b strings.Builder
	rest := css
	for {
		blocks := lightThemeBlocks(rest)
		if len(blocks) == 0 {
			b.WriteString(rest)
			return b.String()
		}
		// Remove the first light block from the string, by locating its
		// media query and cutting to the matching close brace.
		needle := "@media (prefers-color-scheme: light)"
		j := strings.Index(rest, needle)
		if j < 0 {
			b.WriteString(rest)
			return b.String()
		}
		open := strings.Index(rest[j:], "{")
		if open < 0 {
			b.WriteString(rest)
			return b.String()
		}
		open += j
		depth := 0
		end := -1
		for k := open; k < len(rest); k++ {
			switch rest[k] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = k
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:j])
		rest = rest[end+1:]
	}
}

// rootTokens returns the token names defined in a stylesheet's :root
// blocks.
func rootTokens(css string) map[string]bool {
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?s):root\s*\{(.*?)\}`).FindAllStringSubmatch(css, -1) {
		for _, tok := range regexp.MustCompile(`(--[a-z0-9-]+)\s*:`).FindAllStringSubmatch(m[1], -1) {
			out[tok[1]] = true
		}
	}
	return out
}

// The dashboard may add its own LAYOUT tokens -- a gutter, a content
// width, the tab band's own surface -- and it must: they are the
// dashboard's frame, and only the dashboard has a frame.
//
// It may not define a token the design system already owns. The
// distinction matters because the design system's whole job is to be the
// one place a value lives, and a page that redefines `--accent` is a
// page that will drift from the token it shadows -- silently, because
// the page still renders, still looks right, and still passes every
// check that reads the page rather than the stylesheet.
//
// The design system's own token names are read from theme.css rather
// than listed here, so a token added to the theme tomorrow is covered
// tomorrow.
func TestTheDashboardDoesNotRedefineADesignSystemToken(t *testing.T) {
	own := servedDashboardOwnCSS(t)
	themeTokens := map[string]bool{}
	for _, m := range regexp.MustCompile(`(--[a-z0-9-]+)\s*:`).FindAllStringSubmatch(webui.ThemeCSS, -1) {
		themeTokens[m[1]] = true
	}
	if len(themeTokens) == 0 {
		t.Fatal("no tokens found in the theme; this gate would pass on nothing")
	}
	redefinition := ""
	for _, m := range regexp.MustCompile(`(?s):root\s*\{(.*?)\}`).FindAllStringSubmatch(own, -1) {
		for _, tok := range regexp.MustCompile(`(--[a-z0-9-]+)\s*:`).FindAllStringSubmatch(m[1], -1) {
			if themeTokens[tok[1]] {
				redefinition = tok[1]
				break
			}
		}
		if redefinition != "" {
			break
		}
	}
	if redefinition != "" {
		t.Errorf("the dashboard defines %s, which the design system already owns. A page that "+
			"redefines a shared token is a second owner: the page renders, and drifts the day the "+
			"owner moves.", redefinition)
	}
}

// The dashboard's own tokens are allowed to be colours -- they are the
// tab band's surfaces, which only the dashboard has -- but a token that
// is only given a value in the LIGHT theme is a token the dark theme was
// never given, and it will read the light theme's surface in the dark one.
//
// A token defined once, in the base, belongs to both themes: the base
// holds the dark theme's values and the light theme overrides the ones
// that differ.
func TestTheDashboardsOwnTokensAreDefinedInBothThemes(t *testing.T) {
	own := servedDashboardOwnCSS(t)
	themeTokens := map[string]bool{}
	for _, m := range regexp.MustCompile(`(--[a-z0-9-]+)\s*:`).FindAllStringSubmatch(webui.ThemeCSS, -1) {
		themeTokens[m[1]] = true
	}
	base := rootTokens(cssWithoutLightTheme(own))
	light := map[string]bool{}
	for _, block := range lightThemeBlocks(own) {
		for k, v := range rootTokens(block) {
			light[k] = v
		}
	}
	for tok := range light {
		if themeTokens[tok] {
			continue
		}
		if !base[tok] {
			t.Errorf("the dashboard's own token %s is given a value only in the light theme, so the "+
				"dark theme inherits a value that was never written for it", tok)
		}
	}
}

// The dashboard must not define a component the design system owns.
//
// The design system's whole claim is that a component has ONE owner. A
// page that defines its own `.btn-primary` is a second owner, and the
// two drift -- which is not a theory: this repository's dashboard
// carried three separate `.btn-primary` blocks and three `.btn-sm`
// blocks, in one file, and the narrow-viewport rule that was supposed
// to stop a button overflowing named three button classes that exist
// nowhere in the product.
//
// The class names come from the design system's own components.css, so a
// component added there tomorrow is covered tomorrow.
func TestTheDashboardDoesNotRedefineADesignSystemComponent(t *testing.T) {
	own := servedDashboardOwnCSS(t)
	// The components the design system owns, read from its own file.
	owned := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\.([a-z0-9-]+)\s*[,{]`).FindAllStringSubmatch(webui.ComponentsCSS, -1) {
		owned[m[1]] = true
	}
	if len(owned) == 0 {
		t.Fatal("no components found in the design system; this gate would pass on nothing")
	}
	// Every class the dashboard's own CSS defines.
	for _, m := range regexp.MustCompile(`(?m)^\.([a-z0-9-]+)\s*[,{]`).FindAllStringSubmatch(own, -1) {
		if owned[m[1]] {
			t.Errorf("the dashboard defines .%s, which the design system owns. A hand-copied value "+
				"is a defect the day the owner moves.", m[1])
		}
	}
}

// The red proofs for the two gates above.
//
// A gate that has never been seen to fail is a gate that has never been
// tested. Both of these are exercised against a stylesheet with the
// defect written into it.
func TestTheDashboardTokenGatesCatchASecondOwner(t *testing.T) {
	themeTokens := map[string]bool{}
	for _, m := range regexp.MustCompile(`(--[a-z0-9-]+)\s*:`).FindAllStringSubmatch(webui.ThemeCSS, -1) {
		themeTokens[m[1]] = true
	}
	if len(themeTokens) == 0 {
		t.Fatal("no tokens found in the theme")
	}
	// A page that redefines a token the theme owns.
	broken := ":root { --accent: #ff0000; }"
	found := ""
	for _, m := range regexp.MustCompile(`(?s):root\s*\{(.*?)\}`).FindAllStringSubmatch(broken, -1) {
		for _, tok := range regexp.MustCompile(`(--[a-z0-9-]+)\s*:`).FindAllStringSubmatch(m[1], -1) {
			if themeTokens[tok[1]] {
				found = tok[1]
			}
		}
	}
	if found == "" {
		t.Fatal("the redefinition check did not notice a page redefining --accent")
	}

	// A token given a value only in the light theme.
	css := ":root { --gutter: 1rem; }\n@media (prefers-color-scheme: light) {\n  :root { --tab-track: #262d41; }\n}"
	base := rootTokens(cssWithoutLightTheme(css))
	light := map[string]bool{}
	for _, block := range lightThemeBlocks(css) {
		for k, v := range rootTokens(block) {
			light[k] = v
		}
	}
	if base["--tab-track"] {
		t.Fatal("the light-only check did not notice a token that only the light theme defines")
	}
	if !base["--gutter"] {
		t.Fatal("the light-only check treated a theme-independent token as light-only")
	}

	// A page that defines a component the design system owns.
	owned := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\.([a-z0-9-]+)\s*[,{]`).FindAllStringSubmatch(webui.ComponentsCSS, -1) {
		owned[m[1]] = true
	}
	if len(owned) == 0 {
		t.Fatal("no components found in the design system")
	}
	brokenPage := ".btn-primary { color: red; }\n.card { color: var(--text); }"
	caught := ""
	for _, m := range regexp.MustCompile(`(?m)^\.([a-z0-9-]+)\s*[,{]`).FindAllStringSubmatch(brokenPage, -1) {
		if owned[m[1]] {
			caught = m[1]
			break
		}
	}
	if caught == "" {
		t.Fatal("the component check did not notice a page redefining .btn-primary")
	}
}
