// Package webui owns the design system every page in the product draws
// from: the tokens, the type scale and the icon set.
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
package webui

import _ "embed"

// ThemeCSS is the design system: the tokens, the type scale and the icon
// set. A page includes it in its own <style>, so the cascade order
// still decides who overrides whom.
//
//go:embed theme.css
var ThemeCSS string

// RelayShellCSS is the shell the Hub's relay pages share: the
// confirmation popup, the dead-end page beside it, and the two one-pagers
// a browser is shown after a relay succeeds or fails.
//
// The class names are prefixed because the dashboard has its own .btn and
// its own .hint-text, and a shared file that quietly overrode them would
// be the worst kind of coupling: invisible until something looked wrong.
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
