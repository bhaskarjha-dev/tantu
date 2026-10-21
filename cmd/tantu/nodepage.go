package main

import _ "embed"

// nodePageCSS is the shell the node's two pages share: the QuickDrop page
// `tantu drop` serves, and the relay page `tantu relay` serves.
//
// They were the same page written out twice -- the same card, the same
// sections, the same fields and the same buttons, with small differences
// between the two files and a SECOND palette in both of them: a third set
// of values, hand-copied, in a product whose other pages used the design
// system.
//
// One shell, drawn from the shared design system.
//
//go:embed nodepage.css
var nodePageCSS string
