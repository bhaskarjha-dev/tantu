package bridge

import (
	"strings"
	"testing"
)

// ExtractCallbackPort and callbackExpectation must agree about what a loopback
// binding is.
//
// They did not, and the disagreement was a silent failure rather than a
// conservative one. `ExtractCallbackPort` accepted a loopback redirect_uri of any
// scheme and handed the port on; `callbackExpectation` required http and so
// marked the flow `unbound`. Both were "correct" about their own question, and
// together they meant an application registering
// `redirect_uri=https://localhost:9000/cb` was given a **plaintext** HTTP
// catcher on the port its browser would speak TLS to -- and was then warned, in a
// log tab, that its flow was "weaker" and to "continue only if you trust the
// page", as though it might work.
//
// Two gates, separate because they fail differently. The first is a table: the
// specific https-loopback case, refusing with a reason. The second is the
// invariant that would have caught any future divergence in either direction.

// An https loopback redirect_uri is refused, before anything is requested.
//
// A pure gate, and the right shape for it: ExtractCallbackPort is a decision
// function over a URL, so the input is the whole domain.
func TestAnHTTPSLoopbackRedirectIsRefusedWithAReason(t *testing.T) {
	const auth = "https://example.com/auth?state=S&redirect_uri=https%3A%2F%2Flocalhost%3A9000%2Fcb"

	port, err := ExtractCallbackPort(auth)
	if err == nil {
		t.Fatalf("an https loopback redirect_uri was accepted and bound to port %d. Tantu's "+
			"catcher is a plaintext HTTP listener, so the application's browser would "+
			"speak TLS to a server that answers in cleartext.", port)
	}

	// The message must say what happened and what to do, not merely that it did
	// not work.
	msg := err.Error()
	for _, want := range []string{"https", "plain HTTP", "http://"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q, so the person reading it cannot act: %v", want, err)
		}
	}
	// And it must not echo the URL, which can carry provider-specific material.
	if strings.Contains(msg, "example.com") || strings.Contains(msg, "state=") {
		t.Errorf("the refusal echoed the authorization URL: %v", err)
	}
}

// Every authorization URL that survives port extraction must derive a bound
// expectation.
//
// This is the gate that makes the disagreement class unfindable. Not "the https
// case is handled" -- that is the table above, and the next divergence would not
// look like it. This one states the relationship the two functions must keep,
// so any future change that lets one accept what the other rejects fails here
// with the URL that proves it.
func TestEveryURlThatBindsAPortAlsoDerivesABoundExpectation(t *testing.T) {
	urls := []string{
		// The case that motivated this.
		"https://example.com/auth?state=S&redirect_uri=https%3A%2F%2Flocalhost%3A9000%2Fcb",
		"https://example.com/auth?state=S&redirect_uri=https%3A%2F%2F127.0.0.1%3A9000%2Fcb",
		// The shapes that must keep working.
		"https://app.kiro.dev/signin?state=S&redirect_uri=http%3A%2F%2Flocalhost%3A3128",
		"https://accounts.google.com/o/oauth2/v2/auth?state=S&redirect_uri=http%3A%2F%2Flocalhost%3A53682%2Foauth2%2Fcallback",
		"https://vscode.dev/redirect?state=http%3A%2F%2Flocalhost%3A54321%2Fcallback&redirect_uri=https%3A%2F%2Fvscode.dev%2Fredirect",
		"https://github.com/login/oauth/authorize?state=S&redirect_uri=http%3A%2F%2F127.0.0.1%3A49152",
		"https://example.com/auth?state=S&redirect_uri=http%3A%2F%2Flocalhost%3A9000%2F",
		"https://example.com/auth?state=S&redirect_uri=http%3A%2F%2FLOCALHOST%3A9000%2Fcb",
	}

	for _, raw := range urls {
		t.Run(truncateForGate(raw, 58), func(t *testing.T) {
			port, err := ExtractCallbackPort(raw)
			if err != nil {
				return // refused: nothing was bound, so nothing to agree about
			}
			if port <= 0 || port > 65535 {
				t.Fatalf("extraction succeeded with port %d, which is not a port", port)
			}
			exp := callbackExpectation(raw)
			if exp.unbound {
				t.Errorf("port extraction bound port %d, but the expectation came back unbound. "+
					"The catcher will accept a callback on any path and there is no path "+
					"binding to check, while the person relaying it was told nothing. Either "+
					"refuse this URL in ExtractCallbackPort or teach callbackExpectation its "+
					"shape -- what must not happen is one accepting what the other rejects.\n"+
					"  URL: %s", port, raw)
			}
		})
	}
}

// And the weaker direction: a URL the catcher would call unbound must be refused
// before a browser opens, or the two are only half agreeing.
func TestEveryUnboundURLIsRefusedBeforeABrowserOpens(t *testing.T) {
	urls := []string{
		"https://example.com/auth?state=S&redirect_uri=https%3A%2F%2Fapp.example.com%2Fcb",
		"https://example.com/auth?state=S",
		"https://example.com/auth?state=S&redirect_uri=https%3A%2F%2Flocalhost%3A9000%2Fcb",
	}
	for _, raw := range urls {
		t.Run(truncateForGate(raw, 58), func(t *testing.T) {
			if !callbackExpectation(raw).unbound {
				t.Skip("this URL is bound, so it is not part of this gate's domain")
			}
			if _, err := ExtractCallbackPort(raw); err == nil {
				t.Errorf("the catcher treats this URL as unbound -- weaker checks, no path "+
					"binding -- yet port extraction accepted it and would open a browser for "+
					"a flow that cannot be relied on.\n  URL: %s", raw)
			}
		})
	}
}

func truncateForGate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "~"
}
