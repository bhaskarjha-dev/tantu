package main

// Gates for the pairing ceremony's wording.
//
// The pairing ceremony is a human comparing two screens. Two things made the
// CLI's version of it unable to fail at that job:
//
//   1. It printed the code TWICE -- as "My Code" and "Peer Code" -- and then
//      asked "Do the codes match on both machines?". Both printed values are
//      the same string by construction (`localSAS, peerSAS := sas, sas` in
//      internal/pairing), so a user who compared the two numbers in front of
//      them got a match every time and had still never looked at the other
//      machine. The comparison the ceremony depends on is between two screens.
//
//   2. It printed this machine's *identity* SAS -- the first six hex characters
//      of the certificate fingerprint -- under the label "Pairing Code (SAS)",
//      and asked the user to confirm it. pairing.SASCode's own doc says that
//      value "must never be used as a security decision: it is 24 bits,
//      static, and not bound to a session". This is the exact mistake the
//      dashboard was already fixed for (CHANGELOG: "The identity SAS is no
//      longer labeled as the pairing code"), still standing in the surface that
//      actually runs the ceremony.
//
// The first is a wording claim, so a gate over the rendered output is the
// right tool: the text the operator reads IS the domain, and no runtime state
// distinguishes "printed correctly" from "printed misleadingly".
//
// The gate drives confirmSAS, which is what the ceremony actually calls, and
// not the printSASPrompt inside it. A gate over printSASPrompt alone passed
// while the caller printed a second copy of the code straight after it -- the
// defect this pair of functions was written to remove, reproduced by the
// first version of this very gate.
//
// The second is a claim about a label applied to a value, so the gate reads
// the source -- the same technique the dashboard gates use for the same class
// of defect.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestConfirmSASShowsTheCodeExactlyOnce(t *testing.T) {
	const code = "WEED-HEAT-EACH-SIGN-CLAY-LENS"
	var b strings.Builder
	confirmSAS(&b, strings.NewReader("y\n"), code)
	out := b.String()

	if got := strings.Count(out, code); got != 1 {
		t.Errorf("the ceremony printed the code %d times, want exactly 1: a code shown twice "+
			"on one screen invites a same-screen comparison, which always matches\noutput:\n%s", got, out)
	}
	for _, banned := range []string{"My Code", "Peer Code", "my code", "peer code"} {
		if strings.Contains(out, banned) {
			t.Errorf("the ceremony still shows %q; the code must be shown once and compared "+
				"against the other machine's screen, not against a second copy of itself\noutput:\n%s",
				banned, out)
		}
	}
	if !strings.Contains(out, "remote screen") {
		t.Errorf("the ceremony does not say which screen to compare against; without that, "+
			"the user is not told what the prompt is for\noutput:\n%s", out)
	}
}

func TestConfirmSASReadsTheAnswer(t *testing.T) {
	const code = "WEED-HEAT-EACH-SIGN-CLAY-LENS"
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"\n", false},
		{"", false},
		{"maybe\n", false},
	} {
		var b strings.Builder
		if got := confirmSAS(&b, strings.NewReader(tc.input), code); got != tc.want {
			t.Errorf("confirmSAS(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestIdentitySASNoticeDisclaimsBeingThePairingCode(t *testing.T) {
	lower := strings.ToLower(identitySASNotice)
	if !strings.Contains(lower, "identity sas") {
		t.Errorf("the notice %q does not name what the value is", identitySASNotice)
	}
	if !strings.Contains(lower, "not the pairing code") {
		t.Errorf("the notice %q does not disclaim being the pairing code; that disclaimer is "+
			"the whole point of it, and its absence is the defect this gate exists for",
			identitySASNotice)
	}
}

// stripLineComments removes `//` comments so that a scan of the source is a
// scan of the code.
//
// A comment in pair.go quotes the old wording on purpose, to say why it is
// gone. Scanning the raw text would then report the explanation as the
// defect. A `//` preceded by a colon is left alone, so a URL is not mistaken
// for a comment.
func stripLineComments(text string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			before := byte(' ')
			if i > 0 {
				before = line[i-1]
			}
			if before == ' ' || before == '\t' {
				line = line[:i]
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestIdentitySASIsNeverPrintedUnderAPairingCodeLabel reads the CLI's own
// source.
//
// The defect was a label attached to a value, and the two print sites are
// three lines apart from the value they print, so a source scan is the
// mechanical way to hold them together. It asserts:
//
//   - every line that prints the identity SAS prints it under identitySASNotice
//   - every line that computes the identity SAS names it identitySAS, so the
//     value cannot drift back to a bare "sas" and escape the first rule
//   - the ceremony reaches the prompt through confirmSAS and nothing else, so
//     the wording cannot be bypassed by printing something beside it
//   - the old two-label wording does not come back
func TestIdentitySASIsNeverPrintedUnderAPairingCodeLabel(t *testing.T) {
	raw, err := os.ReadFile("pair.go")
	if err != nil {
		t.Fatalf("read pair.go: %v", err)
	}
	text := stripLineComments(string(raw))
	lines := strings.Split(text, "\n")

	printsIdentity := regexp.MustCompile(`\bPrint(f|ln)?\(`)
	for i, line := range lines {
		n := i + 1
		if strings.Contains(line, "identitySAS") && printsIdentity.MatchString(line) {
			if !strings.Contains(line, "identitySASNotice") {
				t.Errorf("pair.go:%d prints the identity SAS without identitySASNotice: %q\n"+
					"the identity SAS must be labelled as the identity SAS, never as a pairing code",
					n, strings.TrimSpace(line))
			}
		}
		if strings.Contains(line, "SASCode(") || strings.Contains(line, "Identity.SAS") {
			if !strings.Contains(line, "identitySAS") {
				t.Errorf("pair.go:%d computes the identity SAS without naming it identitySAS: %q\n"+
					"the name is what ties the value to its label; a bare name lets the two drift apart",
					n, strings.TrimSpace(line))
			}
		}
	}

	// One definition, one call. Two calls would mean the ceremony prints the
	// prompt twice; one would mean it never prints it.
	for _, fn := range []string{"confirmSAS(", "printSASPrompt("} {
		if got := strings.Count(text, fn); got != 2 {
			t.Errorf("pair.go mentions %s %d time(s) (definition included), want 2: the ceremony "+
				"must reach the prompt through the one function that words it", fn, got)
		}
	}

	for _, banned := range []string{"My Code", "Peer Code"} {
		if strings.Contains(text, banned) {
			t.Errorf("pair.go still contains %q; the two-label wording is what let a same-screen "+
				"comparison stand in for comparing two machines", banned)
		}
	}

	// The promptConfirm closure must add nothing of its own. The behavioural
	// gate drives confirmSAS, so output printed beside the prompt by the
	// caller would be invisible to it -- which is exactly how the first
	// version of the behavioural gate passed against a ceremony that printed
	// the code twice. The only way to change what the operator sees is to
	// change confirmSAS, and the only way to change the answer is to change
	// what confirmSAS reads.
	closure := closureBody(t, text, "promptConfirm := func")
	if strings.Contains(closure, "Print") {
		t.Errorf("the promptConfirm closure prints something of its own:\n%s\n"+
			"everything the operator sees must come from confirmSAS, or the gate that reads "+
			"confirmSAS's output cannot see it", strings.TrimSpace(closure))
	}
	if !strings.Contains(closure, "confirmSAS(") {
		t.Errorf("the promptConfirm closure does not go through confirmSAS:\n%s\n"+
			"the ceremony must reach the prompt through the one function that words it",
			strings.TrimSpace(closure))
	}
}

// closureBody returns the source of a closure whose declaration line contains
// decl, from that line to the line that closes it.
func closureBody(t *testing.T, text, decl string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, decl) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no closure declaration containing %q in pair.go", decl)
	}
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "}" {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("closure %q is not closed in pair.go", decl)
	return ""
}
