package bridge

import (
	"testing"
)

// The rule for deriving a callback path from an authorization URL, over the shapes
// that exist in the wild.
//
// A pure decision function, gated as one: callbackExpectation takes a URL and
// returns what the callback catcher will accept, so the input *is* the whole
// domain. The end-to-end gates in e2e_unstated_path_test.go prove the two shapes
// that broke a real sign-in; this covers the shapes around them, including the
// VS Code trampoline form that neither e2e gate exercises.
//
// The rule in one sentence: a path constrains the callback only when some part of
// the authorization URL states one. A loopback binding that names no path never
// invents "/", and never loosens a path another part did state.

// pathAccepted reports whether the catcher would accept a callback on this path,
// by the same rule validateCallbackRequestState applies.
func pathAccepted(exp callbackExpectationSpec, got string) bool {
	return exp.path == "" || exp.path == got
}

func TestCallbackPathIsOnlyConstrainedWhenOneIsStated(t *testing.T) {
	cases := []struct {
		name    string
		authURL string
		want    string // "" means unconstrained
		accept  string // a path the catcher must accept
		refuse  string // a path the catcher must refuse, or "" if it accepts all
	}{
		{
			// The field report. Kiro registers host and port and no path, and its
			// listener serves whatever path it likes.
			name:    "Kiro: redirect_uri with no path",
			authURL: "https://app.kiro.dev/signin?state=st-1&code_challenge=abc&redirect_uri=http://localhost:3128",
			want:    "",
			accept:  "/oauth/callback",
			refuse:  "",
		},
		{
			name:    "redirect_uri states a path",
			authURL: "https://accounts.google.com/o/oauth2/v2/auth?state=st-2&redirect_uri=http://localhost:53682/oauth2/callback",
			want:    "/oauth2/callback",
			accept:  "/oauth2/callback",
			refuse:  "/somewhere-else",
		},
		{
			// An explicitly root path is a stated path, not an unstated one. This
			// is the distinction the old normalisation destroyed.
			name:    "redirect_uri states the root path",
			authURL: "https://example.com/auth?state=st-3&redirect_uri=http://localhost:1234/",
			want:    "/",
			accept:  "/",
			refuse:  "/oauth/callback",
		},
		{
			// VS Code puts the real loopback callback in state while redirect_uri
			// points at an external HTTPS service.
			name:    "VS Code trampoline in state, path stated",
			authURL: "https://vscode.dev/redirect?state=http%3A%2F%2Flocalhost%3A54321%2Fcallback&redirect_uri=https://vscode.dev/redirect",
			want:    "/callback",
			accept:  "/callback",
			refuse:  "/other",
		},
		{
			name:    "VS Code trampoline in state, no path stated",
			authURL: "https://vscode.dev/redirect?state=http%3A%2F%2Flocalhost%3A54321&redirect_uri=https://vscode.dev/redirect",
			want:    "",
			accept:  "/anything/at/all",
			refuse:  "",
		},
		{
			// Both a loopback redirect and a loopback trampoline, where only the
			// trampoline omits a path. The stated redirect path must survive: a
			// later, less specific statement may not loosen an earlier specific one.
			name:    "trampoline without a path must not erase a stated redirect path",
			authURL: "https://example.com/auth?state=http%3A%2F%2Flocalhost%3A9999&redirect_uri=http%3A%2F%2Flocalhost%3A9999%2Fstated",
			want:    "/stated",
			accept:  "/stated",
			refuse:  "/elsewhere",
		},
		{
			name:    "trampoline path supersedes a loopback redirect path",
			authURL: "https://example.com/auth?state=http%3A%2F%2Flocalhost%3A9999%2Ftrampoline&redirect_uri=http%3A%2F%2Flocalhost%3A9999%2Fstated",
			want:    "/trampoline",
			accept:  "/trampoline",
			refuse:  "/stated",
		},
		{
			// No loopback binding at all: the flow is reported unbound and the
			// browser headers plus state carry it.
			name:    "no loopback binding",
			authURL: "https://example.com/auth?state=st-8&redirect_uri=https://example.com/cb",
			want:    "",
			accept:  "/whatever",
			refuse:  "",
		},
		{
			// A non-loopback redirect must never be treated as a binding.
			name:    "non-loopback redirect_uri with a path",
			authURL: "https://example.com/auth?state=st-9&redirect_uri=https://elsewhere.example/cb",
			want:    "",
			accept:  "/anything",
			refuse:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp := callbackExpectation(tc.authURL)
			if exp.path != tc.want {
				t.Errorf("derived path = %q, want %q.\n  This is the field report of 2026-10-17 in "+
					"reverse: an invented path rejects a callback the application would have "+
					"accepted, and a lost path accepts one it would not.\n  URL: %s",
					exp.path, tc.want, tc.authURL)
			}
			if !pathAccepted(exp, tc.accept) {
				t.Errorf("the catcher would refuse %q, but this shape must accept it", tc.accept)
			}
			if tc.refuse != "" && pathAccepted(exp, tc.refuse) {
				t.Errorf("the catcher would accept %q, but this shape states its path and must "+
					"refuse anything else (stated path: %q)", tc.refuse, exp.path)
			}
		})
	}
}

// The state and origin derivations must survive the path change untouched, since
// they are what actually defend the flow.
func TestCallbackExpectationStillDerivesStateAndOrigin(t *testing.T) {
	exp := callbackExpectation("https://app.kiro.dev/signin?state=st-1&redirect_uri=http://localhost:3128")
	if exp.state != "st-1" {
		t.Errorf("state = %q, want \"st-1\"; the state comparison is what stops a hostile page "+
			"forging a callback, and it must not be affected by the path rule", exp.state)
	}
	if exp.authOrigin != "https://app.kiro.dev" {
		t.Errorf("authOrigin = %q, want \"https://app.kiro.dev\"", exp.authOrigin)
	}
	if exp.unbound {
		t.Error("a flow with a loopback redirect_uri is bound and must not be reported unbound")
	}
	if !exp.constrained {
		t.Error("constrained = false for a flow with a loopback redirect_uri")
	}
}

// A flow with no loopback binding is still reported unbound, so the product can
// say the flow is weaker rather than pretending it is bound.
func TestACallbackWithNoLoopbackBindingIsStillReportedUnbound(t *testing.T) {
	exp := callbackExpectation("https://example.com/auth?state=st-2&redirect_uri=https://example.com/cb")
	if !exp.unbound {
		t.Error("a flow with no loopback redirect binding is not reported unbound, so the " +
			"product cannot tell the user its flow is weaker")
	}
}
