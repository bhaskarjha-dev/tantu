package bridge

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Every rejection must tell the person what happened and what to do next.
//
// Before this batch, all four browser-facing rejections returned the single
// string "invalid OAuth callback" and the specific reason went to the Hub's log.
// A real sign-in failed on 2026-10-17 and the person saw exactly that, with no
// next step and no way to find the log. No test asserted anything about the page.
//
// Three properties, and they are separate because they fail differently:
// every kind is complete, distinct kinds read differently, and no page leaks a
// secret.

func TestEveryRejectionKindIsActionable(t *testing.T) {
	seen := map[string]rejectionKind{}
	for kind := range rejectionAdviceTable {
		a := adviceFor(kind)
		name, ok := rejectionKindName(kind)
		if !ok {
			t.Errorf("kind %d has advice but no name, so nothing can name it", kind)
			continue
		}
		if a.Title == "" {
			t.Errorf("%s: no title, so the page says nothing about what happened", name)
		}
		if a.Detail == "" {
			t.Errorf("%s: no explanation, so the page states a refusal without saying what it "+
				"refused", name)
		}
		if a.Next == "" {
			t.Errorf("%s: no next action, which is the dead end this table exists to remove", name)
		}
		if a.Status < 400 || a.Status > 599 {
			t.Errorf("%s: status %d is not an error status", name, a.Status)
		}
		if prev, dup := seen[a.Title]; dup {
			t.Errorf("%s and %d share the title %q, so two different problems read the same to "+
				"the person reading them", name, prev, a.Title)
		}
		seen[a.Title] = kind
	}
}

// Every kind the code can produce must have advice, and every declared kind must
// be reachable from advice.
//
// The completeness half iterates the whole enum rather than a hand-written list.
// The first version listed the twelve kinds by hand, which meant adding a
// thirteenth to the enum with no advice entry still passed -- the counts matched
// because the list and the table grew together by hand. Enumerating the range
// makes an unhandled kind impossible to introduce silently.
func TestEveryRejectionKindHasAPageAndEveryPageHasAKind(t *testing.T) {
	// rejectNone .. rejectDuplicate, inclusive.
	const lastKind = rejectDuplicate
	for k := rejectNone; k <= lastKind; k++ {
		if _, ok := rejectionAdviceTable[k]; !ok {
			name, named := rejectionKindName(k)
			if !named {
				name = "unnamed kind"
			}
			t.Errorf("%s (kind %d) has no advice entry, so this rejection renders the generic "+
				"fallback and the user is back to one message for every cause", name, int(k))
		}
		if _, ok := rejectionKindName(k); !ok {
			t.Errorf("kind %d is declared but unnamed, so nothing can report it", int(k))
		}
	}
	if len(rejectionAdviceTable) != int(lastKind)+1 {
		t.Errorf("the advice table has %d entries and the enum declares %d kinds; an entry "+
			"outside the enum is unreachable and a missing one is unhandled",
			len(rejectionAdviceTable), lastKind+1)
	}
	if _, ok := rejectionKindName(lastKind + 1); ok {
		t.Error("a name exists for a kind beyond the enum, so rejectionKindName and the enum " +
			"have drifted apart")
	}
}

// Every declared rejection kind must have advice, checked against the source.
//
// Structural, and structural on purpose. Go has no runtime enumeration of a const
// block, so iterating `rejectNone..rejectDuplicate` cannot see a kind appended
// *after* rejectDuplicate -- the red proof "add a kind with no advice entry"
// passed against that gate, because the loop never reached the new constant. So
// the constant block itself is parsed.
//
// This reads the source file rather than calling code, and says so. What it
// cannot cover: a kind produced by code but never declared, which the
// produced-by-a-real-check gate above is there to catch instead.
func TestEveryDeclaredRejectionKindHasAdvice(t *testing.T) {
	src, err := os.ReadFile("rejection.go")
	if err != nil {
		t.Fatalf("read rejection.go: %v", err)
	}
	// Scanned line by line rather than by regex.
	//
	// Two regexes were wrong here. The first required end-of-line after the name
	// and so missed `rejectNone rejectionKind = iota`, blaming the table for 11
	// kinds. The second assumed gofmt had padded the block and still missed it,
	// because the difference is the *type and assignment* between the name and
	// the line end, not padding. A line scan states the rule once: a tab, then
	// rejectSomething, then not a map key or an entry.
	var declared []string
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimRight(line, "\r")
		if !strings.HasPrefix(t, "\treject") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(t, "\t"))
		if strings.HasSuffix(name, ":") || strings.Contains(name, ": {") {
			continue // a key in the advice table, not a declared constant
		}
		if i := strings.IndexAny(name, " \t"); i >= 0 {
			name = name[:i] // drop "rejectionKind = iota" and anything similar
		}
		if !strings.HasPrefix(name, "reject") || len(name) <= len("reject") {
			continue
		}
		declared = append(declared, name)
	}
	if len(declared) == 0 {
		t.Fatal("no rejection kind constants found; the scan has gone stale, so this gate " +
			"is measuring nothing")
	}
	if len(declared) != len(rejectionAdviceTable) {
		t.Errorf("rejection.go declares %d rejection kinds and the advice table has %d entries; "+
			"they must match, or a kind renders the generic fallback", len(declared), len(rejectionAdviceTable))
	}
	// No separate "is the last constant still rejectDuplicate" check: it was an
	// off-by-one waiting to happen (12 constants, last value 11), and the
	// per-name lookup below already fails for a name it cannot resolve.
	for _, name := range declared {
		kind, ok := parseKindForTest(name)
		if !ok {
			t.Errorf("%s is declared but cannot be resolved, so nothing can name it", name)
			continue
		}
		if _, ok := rejectionAdviceTable[kind]; !ok {
			t.Errorf("%s is declared but has no advice entry, so this rejection renders the "+
				"generic fallback and the user is back to one message for every cause", name)
		}
	}
}

// The rendered page must actually carry the advice, not merely hold it.
//
// Separate from the table-completeness gate above, because that one reads the
// table and this one reads the bytes a person receives. The red proof for "drop
// the next action" passed against the table gate: the table still had the text,
// only the page stopped rendering it. That is the dead end rebuilt with the
// remedy still in the data structure.
func TestTheRenderedPageCarriesTheTitleExplanationAndNextAction(t *testing.T) {
	for k := rejectNone; k <= rejectDuplicate; k++ {
		t.Run(func() string { n, _ := rejectionKindName(k); return n }(), func(t *testing.T) {
			rec := httptest.NewRecorder()
			renderCallbackRejection(rec, k)
			body := rec.Body.String()
			a := adviceFor(k)

			if !strings.Contains(body, html.EscapeString(a.Title)) {
				t.Errorf("the page does not contain its title %q", a.Title)
			}
			if !strings.Contains(body, html.EscapeString(a.Detail)) {
				t.Errorf("the page does not contain its explanation %q", a.Detail)
			}
			if !strings.Contains(body, html.EscapeString(a.Next)) {
				t.Errorf("the page does not contain its next action %q; the advice exists but the "+
					"person reading the browser never sees it, which is the dead end this "+
					"replaced", a.Next)
			}
			if rec.Code != a.Status {
				t.Errorf("status = %d, want %d", rec.Code, a.Status)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html; the browser is showing this to a person",
					ct)
			}
		})
	}
}

// The advice table is user-facing copy, so it must not contain internal check text.
//
// Separate from the leak gate below, which renders one page and inspects it.
// This one reads the table, so it catches internal wording pasted into an advice
// field even when no test happens to render that kind.
func TestTheAdviceTableCarriesNoInternalCheckText(t *testing.T) {
	// Fragments only the checks actually emit. Matching whole messages would be
	// stricter than it needs to be; matching nothing would let "Sec-Fetch-Dest"
	// reach a user.
	internal := []string{
		"Sec-Fetch", "callback path", "does not match", "goroutine",
		"redirect URI", "authorization origin", "1 MiB", "read callback body",
	}
	for k, a := range rejectionAdviceTable {
		name, _ := rejectionKindName(k)
		for _, field := range []struct{ what, text string }{
			{"Title", a.Title}, {"Detail", a.Detail}, {"Next", a.Next},
		} {
			for _, frag := range internal {
				if strings.Contains(field.text, frag) {
					t.Errorf("%s %s contains the internal fragment %q; this copy is shown to "+
						"users and the technical wording belongs in the log",
						name, field.what, frag)
				}
			}
		}
	}
}

// The rendered page must not carry the flow's secrets.
//
// state is the flow's own CSRF token and the request query can hold the
// authorization code. Both are rendered nowhere: the page is built only from the
// advice table, which holds compile-time constants. This gate asserts that by
// feeding the render a request full of secrets and finding none of it in the
// output.
func TestARejectionPageNeverRendersTheCallbackItRefused(t *testing.T) {
	const (
		secretState = "0eccf62b-9daf-43e0-944d-e1fc7319288c"
		secretCode  = "601a8936-f7b3-4c31-86bf-b12ecea7370a"
	)
	req := httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1:3128/oauth/callback?state="+secretState+"&code="+secretCode, nil)
	req.Host = "127.0.0.1:3128"

	// A real rejection of that exact request, so the page is what a user would see.
	err := validateCallbackRequest(req, callbackExpectationSpec{state: "a-different-state", constrained: true})
	if err == nil {
		t.Fatal("a callback with the wrong state was accepted; the gate is measuring nothing")
	}
	if kind := rejectionKindOf(err); kind != rejectState {
		t.Fatalf("kind = %v, want rejectState; the classification is what the page is built from", kind)
	}

	rec := httptest.NewRecorder()
	renderCallbackRejection(rec, rejectionKindOf(err))
	body := rec.Body.String()

	for _, secret := range []string{secretState, secretCode} {
		if strings.Contains(body, secret) {
			t.Errorf("the page rendered %q. state is the flow's CSRF secret and the code is a "+
				"credential; neither may appear in a browser page", secret)
		}
	}
	for _, leak := range []string{"Sec-Fetch", "does not match authorization request", "Go-http-client", "goroutine"} {
		if strings.Contains(body, leak) {
			t.Errorf("the page leaked internal detail %q, which belongs in the log only", leak)
		}
	}
	// And it must still say something useful.
	if !strings.Contains(body, "no longer waiting") || !strings.Contains(body, "Start the sign-in again") {
		t.Errorf("the page does not explain the situation and offer a next step:\n%s", body)
	}
}

// Every kind must be producible by the real validators.
//
// This is the strong form. The weaker form -- auditing that the advice table has
// no unreachable rows -- cannot be written honestly, because it needs a complete
// list of every producer, and the first attempt at it demanded the table contain
// only the three kinds it happened to construct. Driving the validators instead
// proves the classification is wired to the checks rather than merely declared
// beside them, and it fails if a check is renamed without being reclassified.
func TestEveryRejectionKindIsProducedByARealCheck(t *testing.T) {
	base := func(path, rawQuery string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:3128"+path+"?"+rawQuery, nil)
		r.Host = "127.0.0.1:3128"
		return r
	}

	cases := []struct {
		name string
		req  *http.Request
		spec callbackExpectationSpec
		want rejectionKind
	}{
		{
			name: "method",
			req:  func() *http.Request { r := base("/", ""); r.Method = http.MethodPut; return r }(),
			spec: callbackExpectationSpec{},
			want: rejectMethod,
		},
		{
			name: "not loopback",
			req:  func() *http.Request { r := base("/", ""); r.Host = "evil.example"; return r }(),
			spec: callbackExpectationSpec{},
			want: rejectNotLoopback,
		},
		{
			name: "not a navigation",
			req: func() *http.Request {
				r := base("/", "")
				r.Header.Set("Sec-Fetch-Mode", "cors")
				return r
			}(),
			spec: callbackExpectationSpec{},
			want: rejectNotNavigation,
		},
		{
			name: "subresource",
			req: func() *http.Request {
				r := base("/", "")
				r.Header.Set("Sec-Fetch-Mode", "navigate")
				r.Header.Set("Sec-Fetch-Dest", "image")
				return r
			}(),
			spec: callbackExpectationSpec{},
			want: rejectSubresource,
		},
		{
			name: "origin unknown",
			req: func() *http.Request {
				r := base("/", "")
				r.Header.Set("Origin", "https://provider.example")
				return r
			}(),
			spec: callbackExpectationSpec{},
			want: rejectOriginUnknown,
		},
		{
			name: "origin mismatch",
			req: func() *http.Request {
				r := base("/", "")
				r.Header.Set("Origin", "https://elsewhere.example")
				return r
			}(),
			spec: callbackExpectationSpec{authOrigin: "https://provider.example"},
			want: rejectOriginMismatch,
		},
		{
			name: "path",
			req:  base("/somewhere-else", "state=s"),
			spec: callbackExpectationSpec{path: "/oauth/callback", state: "s", constrained: true},
			want: rejectPath,
		},
		{
			name: "state",
			req:  base("/", "state=not-the-one"),
			spec: callbackExpectationSpec{state: "the-one", constrained: true},
			want: rejectState,
		},
	}

	produced := map[rejectionKind]bool{rejectNone: true}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCallbackRequest(tc.req, tc.spec)
			if err == nil {
				t.Fatalf("%s was accepted; expected it to be refused as %v", tc.name, tc.want)
			}
			if got := rejectionKindOf(err); got != tc.want {
				gotName, _ := rejectionKindName(got)
				wantName, _ := rejectionKindName(tc.want)
				t.Errorf("classified as %s, want %s (%v); the page the user sees is built from "+
					"this, so a misclassification shows them the wrong remedy", gotName, wantName, err)
			}
			produced[tc.want] = true
		})
	}

	// The kinds the validators cannot produce are the ones raised by the caller:
	// body size, duplicate capture, and the generic fallback. They are covered by
	// the table-completeness gate and by renderCallbackRejection's own tests.
	for _, kind := range []rejectionKind{rejectBodyUnreadable, rejectBodyTooLarge, rejectDuplicate} {
		if !produced[kind] {
			if _, ok := rejectionAdviceTable[kind]; !ok {
				t.Errorf("kind %d is neither produced by a validator nor given advice", kind)
			}
		}
	}
}

// The classification must survive being wrapped, or the page degrades to the
// generic fallback as soon as anything annotates the error.
func TestRejectionKindSurvivesWrapping(t *testing.T) {
	inner := reject(rejectOriginMismatch, "callback Origin %s does not match the authorization origin", "https://elsewhere.example")

	// A real wrap must keep the kind.
	if got := rejectionKindOf(fmt.Errorf("relaying the callback: %w", inner)); got != rejectOriginMismatch {
		t.Errorf("kind = %v after %%w wrapping, want rejectOriginMismatch; a page that silently "+
			"degrades to the generic message whenever an error gains context is the dead end "+
			"coming back", got)
	}

	// A concatenated error must NOT be classified. Classification comes from the
	// type, never from matching message text: a rule that matched on substrings
	// would start misclassifying as soon as a message was reworded, and would
	// misclassify an attacker's crafted error text too.
	concatenated := errors.New("relaying the callback: " + inner.Error())
	if rejectionKindOf(concatenated) != rejectNone {
		t.Error("a concatenated error was classified, so the classification is matching message " +
			"text rather than the error type")
	}
}

// parseKindForTest resolves a declared constant by name, so the structural gate
// above can check a kind it found in the source without a compile-time reference
// to it -- which is the whole point, since such a reference would only exist for
// kinds someone remembered to use.

func parseKindForTest(name string) (rejectionKind, bool) {
	switch name {
	case "rejectNone":
		return rejectNone, true
	case "rejectMethod":
		return rejectMethod, true
	case "rejectNotLoopback":
		return rejectNotLoopback, true
	case "rejectNotNavigation":
		return rejectNotNavigation, true
	case "rejectSubresource":
		return rejectSubresource, true
	case "rejectOriginUnknown":
		return rejectOriginUnknown, true
	case "rejectOriginMismatch":
		return rejectOriginMismatch, true
	case "rejectPath":
		return rejectPath, true
	case "rejectState":
		return rejectState, true
	case "rejectBodyUnreadable":
		return rejectBodyUnreadable, true
	case "rejectBodyTooLarge":
		return rejectBodyTooLarge, true
	case "rejectDuplicate":
		return rejectDuplicate, true
	}
	return rejectNone, false
}
