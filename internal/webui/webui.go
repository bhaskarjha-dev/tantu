// Package webui owns the design system every page in the product draws
// from: the tokens, the type scale, the icon set, and the components.
//
// It is a package of assets and nothing else -- no logic, no
// dependencies -- and it exists because the product had three palettes.
//
// The Hub's pages used the tokens in the Hub's own stylesheet. The
// node's pages -- the ones `tantu drop` and `tantu relay` serve --
// carried a SECOND palette, hand-copied into each of them, and the relay
// popup carried a THIRD copy of the first one, with the same values
// written out as hex. Every page agreed with every other page by
// coincidence, and the moment a token moved, the copies drifted.
//
// One owner, several consumers. A page that needs a new colour adds a
// token here, and every page gets it.
//
// The same drift happened to the COMPONENTS, and it was worse there,
// because a missing component is a missing shape rather than a wrong
// colour: the node's pages use `.empty-state`, and the only definition of
// it lived inside the dashboard's stylesheet, which those pages never
// load -- so their empty state rendered as an unstyled paragraph of body
// text. Components live in components.css and are served, with the theme,
// at a single {{DESIGN_SYSTEM}} marker, so a page cannot include one
// without the other.
package webui

import _ "embed"

// ThemeCSS is the design system: the tokens, the type scale and the icon
// set. A page includes it in its own <style>, so the cascade order
// still decides who overrides whom.
//
//go:embed theme.css
var ThemeCSS string

// ComponentsCSS is the component layer: the primitives every page is built
// from -- the visually-hidden utility, the empty state, the button
// vocabulary, the focus ring, the hint.
//
// It is separate from ThemeCSS because it answers a different question. The
// theme says what the product looks like; the components say what the
// product is made of. The theme alone was enough for the dashboard, which
// was the only surface that existed when the design system was written, and
// not enough for the node's pages -- which use `.empty-state` and had no
// definition of it anywhere they could reach, so their empty state rendered
// as an unstyled paragraph of body text.
//
//go:embed components.css
var ComponentsCSS string

// DesignSystemCSS is everything a page needs to look like this product:
// the tokens, the type scale, the icon set, and the components.
//
// It is served at ONE marker, {{DESIGN_SYSTEM}}, rather than at two. A
// second marker is a second thing a page can forget: a page that includes
// the theme and not the components renders, and its buttons and empty
// states quietly lose their styling, which is the exact defect the
// component layer was written to remove.
func DesignSystemCSS() string {
	return ThemeCSS + "\n" + ComponentsCSS
}

// RelayShellCSS is the shell the Hub's relay pages share: the
// confirmation popup, the dead-end page beside it, and the two one-pagers
// a browser is shown after a relay succeeds or fails.
//
// It carries only what is specific to a popup -- its density, and its
// focus behaviour -- and takes the button shape, the type and the focus
// ring from the design system. It used to carry its own copy of the
// `.sr-only` utility and its own button treatment, which is how the relay
// popup came to have a primary button a different size from the
// dashboard's.
//
//go:embed relay_shell.css
var RelayShellCSS string

// RelayOKHTML is the page a browser is shown when a relay succeeds.
//
// It is here, and not in the Hub, because the node's `tantu relay`
// command shows the same page: the same words, the same card, the same
// mark. Two copies of a page that says the same thing is two copies to
// keep in step.
//
//go:embed relay_ok.html
var RelayOKHTML string

// RelayFailedHTML is the page a browser is shown when a relay fails.
//
//go:embed relay_failed.html
var RelayFailedHTML string
