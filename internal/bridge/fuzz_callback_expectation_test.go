package bridge

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The generalisation of the Kiro field report, stated once:
//
//	A callback catcher must never require anything the authorization URL did not
//	state.
//
// Kiro registers `redirect_uri=http://localhost:3128` -- host and port, no path
// -- and calls back on `/oauth/callback`. Tantu normalised the empty path to "/"
// and then required the callback to arrive there, so it refused a callback for
// breaking a rule the application never set. Every other client shape that has
// ever been tried would have been caught by the same sentence.
//
// This is a fuzz target rather than a longer table, and that is the whole point.
// A table samples; a fuzz target over the derivation plus the validator covers
// shapes nobody thought of, which is the only way to answer "will this work for
// any application" rather than "did anyone think of an application".
//
// Four properties:
//
//  1. Nothing panics. This is the literal answer to "will it crash on any
//     portal": the derivation and every check must survive arbitrary input.
//  2. The derived path is never stronger than what the URL stated. This is the
//     Kiro invariant, and it is what the normalisation violated.
//  3. A callback on the stated path is accepted; when no path was stated, a
//     callback on any path is accepted. Together with (2) that is the whole
//     contract, in both directions.
//  4. Adding a path to the URL never loosens what is accepted. Monotonicity:
//     a more specific authorization URL must not become a less strict catcher.
func FuzzCallbackExpectation(f *testing.F) {
	// Real shapes, so the seeds are not invented. Each is the authorization URL
	// of a client that exists, with its secret material replaced.
	seeds := []string{
		// Kiro IDE, the field report. No path stated.
		"https://app.kiro.dev/signin?state=0eccf62b-9daf-43e0-944d-e1fc7319288c&code_challenge=uDVzYE6slmbBbw_r61ZhoVzIQ63M0ECO0Dg1vJiroNo&code_challenge_method=S256&redirect_uri=http%3A%2F%2Flocalhost%3A3128&redirect_from=KiroIDE",
		// Google, a stated path.
		"https://accounts.google.com/o/oauth2/v2/auth?state=S&client_id=x&redirect_uri=http%3A%2F%2Flocalhost%3A53682%2Foauth2%2Fcallback&response_type=code",
		// VS Code, loopback in state, external redirect_uri.
		"https://vscode.dev/redirect?state=http%3A%2F%2Flocalhost%3A54321%2Fcallback&redirect_uri=https%3A%2F%2Fvscode.dev%2Fredirect",
		// A CLI, loopback on 127.0.0.1 rather than localhost.
		"https://github.com/login/oauth/authorize?client_id=x&state=S&redirect_uri=http%3A%2F%2F127.0.0.1%3A49152",
		// An explicit root path, which IS a stated path.
		"https://example.com/auth?state=S&redirect_uri=http%3A%2F%2Flocalhost%3A9000%2F",
		// A hosted redirect, with no loopback anywhere.
		"https://example.com/auth?state=S&redirect_uri=https%3A%2F%2Fapp.example.com%2Fcb",
		// Nothing but a host.
		"https://example.com/auth?state=S",
		// Degenerate input a real client would never send, which is the point.
		"",
		"::::",
		"?redirect_uri=",
		"https://example.com/auth?redirect_uri=%%%",
		"http://localhost:1/",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, rawURL string) {
		// Property 1: no panic. Any panic here fails the target, which is the
		// whole of the "any platform, any application" crash claim.
		exp := callbackExpectation(rawURL)

		// Property 2: the derived path is empty, or a path the URL actually
		// stated. Nothing else is allowed.
		if exp.path != "" {
			if !strings.HasPrefix(exp.path, "/") {
				t.Fatalf("derived path %q does not begin with a slash; the URL was %q",
					exp.path, rawURL)
			}
			if !urlStatesPath(rawURL, exp.path) {
				t.Fatalf("derived path %q is a constraint the authorization URL never "+
					"stated (stated: %v). This is the Kiro defect: the catcher was "+
					"stricter than the application it exists to serve.\n  URL: %q",
					exp.path, pathsStatedBy(rawURL), rawURL)
			}
		}

		// Property 3: round-trip. A callback on the stated path is accepted, and
		// when nothing was stated, any path is accepted.
		state := exp.state
		cbPath := exp.path
		if cbPath == "" {
			cbPath = "/some/path/the/client/never/mentioned"
		}
		req := callbackRequestForFuzz(http.MethodGet, cbPath, state)
		if err := validateCallbackRequest(req, exp); err != nil {
			t.Fatalf("a legitimate callback on %q was refused for an authorization URL that "+
				"derives exactly that expectation: %v\n  URL: %q", cbPath, err, rawURL)
		}

		// Property 5: a stated constraint is never silently dropped.
		//
		// Properties 2 and 3 only forbid being *stricter* than the URL. Together
		// they are satisfied by never constraining anything at all -- which is
		// how the red proof "never derive a path" passed. That is a real
		// weakening: a client that registered /oauth2/callback would then accept
		// a callback on any path.
		//
		// Stated here as the rule the product should implement, so the target
		// compares the product against the rule rather than against itself. And
		// it runs *before* the monotonicity check's early exit: the first version
		// sat after `if exp.path == "" { return }`, so removing derivation
		// entirely skipped it -- exactly the case it exists to catch.
		if want := expectedPathFor(rawURL); exp.path != want {
			t.Fatalf("derived path %q, but the authorization URL states %q.\n"+
				"  redirect_uri path:      %q\n  state trampoline path: %q\n  URL: %q",
				exp.path, want, redirectPathOf(rawURL), trampolinePathOf(rawURL), rawURL)
		}

		// Property 4: monotonicity. Appending a path to a loopback redirect_uri
		// must not make the catcher accept more, so a second URL that states a
		// path cannot be the weaker of the pair.
		if exp.path == "" {
			return
		}
		tightened := withStatedPath(rawURL, exp.path)
		if tightened == rawURL {
			return // could not tighten this URL; nothing to compare
		}
		tight := callbackExpectation(tightened)
		if tight.path == "" {
			t.Fatalf("adding the path %q that was already derived removed the constraint "+
				"entirely; a more specific authorization URL must not become a weaker "+
				"catcher.\n  was: %q\n  now: %q", exp.path, rawURL, tightened)
		}
	})
}

// redirectPathOf is the path the redirect_uri states, empty if it states none.
func redirectPathOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	redirect := parsed.Query().Get("redirect_uri")
	if redirect == "" {
		return ""
	}
	r, err := url.Parse(redirect)
	if err != nil || r.Hostname() == "" {
		return ""
	}
	if !isLoopbackHost(r.Hostname()) || !strings.EqualFold(r.Scheme, "http") {
		return ""
	}
	return r.Path
}

// trampolinePathOf is the path a loopback state value states, empty otherwise.
func trampolinePathOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	state := parsed.Query().Get("state")
	if state == "" {
		return ""
	}
	u, err := url.Parse(state)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	if !isLoopbackHost(u.Hostname()) || !strings.EqualFold(u.Scheme, "http") {
		return ""
	}
	return u.Path
}

// expectedPathFor is the path the authorization URL states, with the trampoline
// superseding the redirect_uri. Stated here as the rule the product should
// implement, so the fuzz target compares the product against the rule and not
// against itself.
func expectedPathFor(rawURL string) string {
	if p := trampolinePathOf(rawURL); p != "" {
		return p
	}
	return redirectPathOf(rawURL)
}

// pathsStatedBy collects every callback path the authorization URL itself names:
// the redirect_uri's, and the loopback state trampoline's.
func pathsStatedBy(rawURL string) []string {
	var out []string
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return out
	}
	add := func(s string) {
		if s == "" {
			return
		}
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return
		}
		out = append(out, u.Path)
	}
	add(parsed.Query().Get("redirect_uri"))
	add(parsed.Query().Get("state"))
	return out
}

// urlStatesPath reports whether one of the stated paths equals want.
func urlStatesPath(rawURL, want string) bool {
	for _, p := range pathsStatedBy(rawURL) {
		if p == want {
			return true
		}
	}
	return false
}

// withStatedPath returns rawURL with an explicit path added to its redirect_uri,
// so monotonicity can be compared. Returns rawURL unchanged when that is not
// possible, which the caller treats as "nothing to compare".
func withStatedPath(rawURL, path string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return rawURL
	}
	q := parsed.Query()
	redirect := q.Get("redirect_uri")
	if redirect == "" {
		return rawURL
	}
	r, err := url.Parse(redirect)
	if err != nil || r.Host == "" {
		return rawURL
	}
	if !isLoopbackHost(r.Hostname()) {
		return rawURL
	}
	r.Path = path
	q.Set("redirect_uri", r.String())
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// callbackRequestForFuzz builds the request a browser would make, without a
// strict URL parser.
//
// httptest.NewRequest panics on a target containing a space, and the fuzzer found
// that in three seconds: a `state` value of `http://LoCAlhost/ ` derives the path
// `/ `, and concatenating that into a URL is what panicked. Worth recording
// *where* that finding landed, because it is the same lesson as everywhere else
// in this file: the panic was in the harness, not the product. The product
// compares strings and refuses the callback, and the person gets the "arrived on
// an address this sign-in did not register" page with a next action -- a safe,
// actionable outcome rather than a crash.
//
// So the harness builds the request structurally, and the fuzzer is free to
// derive whatever it likes.
func callbackRequestForFuzz(method, path, state string) *http.Request {
	u := &url.URL{Scheme: "http", Host: "127.0.0.1:9000", Path: path}
	if state != "" {
		u.RawQuery = "state=" + url.QueryEscape(state)
	}
	return &http.Request{
		Method: method,
		URL:    u,
		Host:   "127.0.0.1:9000",
		Header: http.Header{
			"Sec-Fetch-Mode": []string{"navigate"},
			"Sec-Fetch-Dest": []string{"document"},
		},
	}
}

var _ = httptest.NewRequest
