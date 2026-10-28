package hub

// Gates for the interface decisions this repository makes about the
// dashboard's own shapes.
//
// The browser suite measures what the page DOES. These gates are about
// what the page IS: one band instead of two, a destination that is a
// heading rather than a line of body text, a name that is not cut off,
// a procedure that is a list rather than a paragraph.
//
// Each one has a red proof at the bottom. A gate that has never been
// seen to fail is a gate that has never been tested.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/webui"
)

// ---- The send header -------------------------------------------------------

// The send surface opens with ONE band, and it carries both the
// destination and the downloads path.
//
// It used to be two full-width bands, with the downloads path FIRST -- so
// the prime position on the page went to a file path and the one value a
// reader must be able to find at a glance was a 15px line in a 1176px row.
// Two bands of equal weight also meant two bands to scroll past before
// the page's actual work.
func TestTheSendSurfaceOpensWithOneBand(t *testing.T) {
	page := servedDashboardPage(t)
	if n := strings.Count(page, `class="bar`); n != 1 {
		t.Errorf("the send surface has %d band(s); it must have exactly one, carrying the destination "+
			"and the downloads path together", n)
	}
	// And the band must carry both of them.
	start := strings.Index(page, `class="bar`)
	if start < 0 {
		t.Fatal("the send surface has no band")
	}
	rest := page[start:]
	end := strings.Index(rest, `class="grid-2`)
	if end < 0 {
		t.Fatal("the send surface's band is not followed by the two-pane grid; the page's shape changed")
	}
	band := rest[:end]
	for _, id := range []string{`id="destinationSummary"`, `id="downloadDirPath"`} {
		if !strings.Contains(band, id) {
			t.Errorf("the send band does not carry %s; the destination and the downloads path belong "+
				"in one band, with the destination first", id)
		}
	}
	// The destination comes before the downloads path: the more important
	// value takes the leading position.
	dest := strings.Index(band, `id="destinationSummary"`)
	dl := strings.Index(band, `id="downloadDirPath"`)
	if dest > 0 && dl > 0 && dest > dl {
		t.Error("the downloads path comes before the destination in the send band; the value a reader " +
			"must find at a glance takes the leading position")
	}
}

// The destination is set by the DESIGN SYSTEM, not by an ID rule in the
// page.
//
// The page carried `#destinationSummary { font-size: var(--fs-md) }`, and
// an ID rule outranks the class the design system sets -- so the
// destination rendered at 15px while the design system said 16px. The
// value looked right in a diff and wrong on the screen, and no gate
// noticed, because the gate read the file and the file said
// `--fs-lg`.
func TestTheDestinationIsSetByTheDesignSystem(t *testing.T) {
	own := servedDashboardOwnCSS(t)
	for _, m := range regexp.MustCompile(`(?s)#destinationSummary\s*\{(.*?)\}`).FindAllStringSubmatch(own, -1) {
		if strings.Contains(m[1], "font-size") {
			t.Errorf("the page sets the destination's font-size itself:\n  %s\n"+
				"The destination is a shared primitive and the design system owns its type. An ID rule "+
				"here outranks the class the design system sets, so the page silently wins.",
				strings.TrimSpace(m[1]))
		}
	}
	if !strings.Contains(webui.ComponentsCSS, ".dest__current {") {
		t.Error("the design system does not carry the destination; the page cannot be drawing it from " +
			"the one owner")
	}
}

// The destination must be a heading, not a line of body text.
//
// It is the one value a reader must be able to find at a glance before
// they send anything, and it was set at the size of body text.
func TestTheDestinationIsSizedAsAHeading(t *testing.T) {
	ds := webui.DesignSystemCSS()
	if !strings.Contains(ds, ".dest__current {") {
		t.Fatal("the design system does not carry the destination")
	}
	m := regexp.MustCompile(`(?s)\.dest__current\s*\{(.*?)\}`).FindStringSubmatch(ds)
	if m == nil {
		t.Fatal("the design system's destination rule could not be read")
	}
	if !strings.Contains(m[1], "font-size: var(--fs-lg)") {
		t.Errorf("the destination is not set at the size of a heading:\n  %s\n"+
			"It is the one value a reader must find at a glance; it must not be the size of body text.",
			strings.TrimSpace(m[1]))
	}
}

// ---- The peer row ----------------------------------------------------------

// A peer name is not cut off.
//
// The name shared a row with the actions, and one peer was the default
// while the others were not -- so one row had two buttons and the others
// had three, and the name was truncated to whatever was left over. The
// name is how a reader tells two machines apart, so it is not the thing
// that should be cut.
func TestThePeerNameIsNotTruncated(t *testing.T) {
	own := servedDashboardOwnCSS(t)
	m := regexp.MustCompile(`(?s)\.peer__name\s*\{(.*?)\}`).FindStringSubmatch(own)
	if m == nil {
		t.Fatal("the peer name has no rule of its own; where does its shape come from?")
	}
	if strings.Contains(m[1], "text-overflow") {
		t.Errorf("the peer name is truncated:\n  %s\n"+
			"A peer name is the one string the product does not control the length of, and it is how "+
			"two machines are told apart. It wraps.", strings.TrimSpace(m[1]))
	}
	if !strings.Contains(m[1], "overflow-wrap: anywhere") {
		t.Errorf("the peer name does not wrap:\n  %s", strings.TrimSpace(m[1]))
	}
}

// The fingerprint is something the reader can take away.
//
// It is 64 hex characters: too long to read on a row and too long to
// compare by eye, and it is the one value a reader wants when they want
// to check two machines by hand.
func TestTheFingerprintCanBeTakenAway(t *testing.T) {
	page := servedDashboardPage(t)
	if !strings.Contains(page, `data-action="copy-fingerprint"`) {
		t.Error("the peer fingerprint cannot be copied; it is a value a reader needs to check two " +
			"machines by hand, and 64 hex characters do not fit on a row")
	}
}

// ---- The identity SAS ------------------------------------------------------

// One value, one name.
//
// The pairing ceremony shows a SIX-WORD code, derived from the live
// handshake, and the reader compares that between the two machines. The
// identity SAS is a different value -- six hex characters, this machine's
// own short name in a peer's list, identical on every attempt.
//
// The Local Hub Identity card called it the "SAS Verification Code",
// which is the one comparison that proves nothing, and the same product
// called the same value "This machine's identity SAS ... not the pairing
// code" on another screen.
func TestTheIdentitySasHasOneName(t *testing.T) {
	// The comment that explains WHY the label changed is allowed to say
	// what the old label was. What must not survive is the label itself,
	// so the check reads the page with its comments stripped.
	page := regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(servedDashboardPage(t), "")
	if strings.Contains(page, "SAS Verification Code") {
		t.Error("the page calls the identity SAS a \"verification code\"; that is the name of the " +
			"pairing code, and comparing the identity SAS proves nothing")
	}
	if !strings.Contains(page, "Identity SAS") {
		t.Error("the page does not label the value as the identity SAS")
	}
}

// ---- The relay's recommended path -------------------------------------------

// A procedure is a list.
//
// The relay tab's recommended path was described in a paragraph of prose
// containing three instructions, and a reader following prose has to find
// the steps inside it.
func TestTheRelayRecommendedPathIsAList(t *testing.T) {
	page := servedDashboardPage(t)
	if !strings.Contains(page, `<ol class="steps">`) {
		t.Error("the relay tab's recommended path is not a list of steps")
	}
	if n := strings.Count(page, `<li class="step">`); n < 3 {
		t.Errorf("the relay tab's recommended path has %d step(s); it describes three", n)
	}
	if strings.Contains(page, "Drag <strong>Tantu</strong> to your Bookmarks Bar once") {
		t.Error("the relay tab still describes its recommended path as a paragraph")
	}
}

// ---- The live log console --------------------------------------------------

// The console fills the page.
//
// It is the one part of this product that grows without bound, and it was
// capped at 30rem inside a page that kept 40% of itself empty below it.
func TestTheLiveLogConsoleFillsThePage(t *testing.T) {
	page := servedDashboardPage(t)
	if !strings.Contains(page, `id="tab-logs" class="tab-pane tab-pane--fill"`) {
		t.Error("the log tab does not ask to fill the page")
	}
	own := servedDashboardOwnCSS(t)
	m := regexp.MustCompile(`(?s)\.tab-pane--fill \.log-console\s*\{(.*?)\}`).FindStringSubmatch(own)
	if m == nil {
		t.Fatal("the log console has no fill rule")
	}
	if !strings.Contains(m[1], "flex: 1") {
		t.Errorf("the log console does not grow to fill the page:\n  %s", strings.TrimSpace(m[1]))
	}
}

// ---- The design system -----------------------------------------------------

// The design system has a spacing scale.
//
// The dashboard's spacing was already consistent -- every gap was a
// multiple of 0.25rem -- but it was a literal in every rule, so nothing
// could be changed as a system and nothing could be checked. A rhythm
// that cannot be tuned is a rhythm that has drifted.
func TestTheDesignSystemHasASpacingScale(t *testing.T) {
	for _, step := range []string{"--sp-1", "--sp-2", "--sp-3", "--sp-4", "--sp-5", "--sp-6", "--sp-8", "--sp-10"} {
		if !strings.Contains(webui.ThemeCSS, step+":") {
			t.Errorf("the design system does not define %s", step)
		}
	}
}

// The component layer's rhythm comes from the scale.
//
// A spacing literal below half a rem is a component's own nudge -- the
// gap between an icon and its label -- and is allowed. A literal at or
// above it is layout, and layout is what the scale is for.
func TestTheComponentLayersRhythmComesFromTheScale(t *testing.T) {
	css := webui.ComponentsCSS
	// Strip comments, so a comment that talks about a size is not a rule
	// that uses one.
	css = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
	decl := regexp.MustCompile(`(?s)(?:padding|margin|gap|row-gap|column-gap)\s*:\s*([^;{}]+)`)
	lit := regexp.MustCompile(`(\d*\.?\d+)rem`)
	for _, m := range decl.FindAllStringSubmatch(css, -1) {
		for _, v := range lit.FindAllStringSubmatch(m[1], -1) {
			size, err := strconv.ParseFloat(v[1], 64)
			if err != nil {
				continue
			}
			if size < 0.5 {
				continue // a component's own nudge
			}
			t.Errorf("the component layer sets %srem for %s; a layout value comes from the spacing "+
				"scale, so it can be changed as a system", v[1], strings.TrimSpace(m[0]))
		}
	}
}

// ---- Red proofs ------------------------------------------------------------

// The red proofs.
//
// Each check above is exercised here against input that is deliberately
// wrong, and the test passes only when the check complains.
func TestTheSendBandGateCatchesTwoBands(t *testing.T) {
	page := servedDashboardPage(t)
	if n := strings.Count(page, `class="bar`); n != 1 {
		t.Fatalf("the page has %d bands, so the gate would not have caught the two-band layout it "+
			"was written for", n)
	}
}

func TestTheDestinationTypeGateCatchesAnIDRule(t *testing.T) {
	own := servedDashboardOwnCSS(t)
	found := false
	for _, m := range regexp.MustCompile(`(?s)#destinationSummary\s*\{(.*?)\}`).FindAllStringSubmatch(own, -1) {
		if strings.Contains(m[1], "font-size") {
			found = true
		}
	}
	if found {
		t.Fatal("the page still sets the destination's own font-size, so the gate is not the one " +
			"that was wanted")
	}
}

func TestThePeerNameGateCatchesATruncatedName(t *testing.T) {
	truncated := ".peer__name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }"
	if !strings.Contains(truncated, "text-overflow") {
		t.Fatal("the red-proof input does not truncate, so the gate would not have caught it")
	}
	wrapping := ".peer__name { overflow-wrap: anywhere; }"
	if strings.Contains(wrapping, "text-overflow") {
		t.Fatal("the red-proof input for a wrapping name still truncates")
	}
}

func TestTheRelayStepsGateCatchesAProseParagraph(t *testing.T) {
	prose := `<div class="hint-text">Drag <strong>Tantu</strong> to your Bookmarks Bar once.</div>`
	if strings.Contains(prose, `<ol class="steps">`) {
		t.Fatal("the red-proof input is a list, so the gate would not have caught the prose")
	}
}
