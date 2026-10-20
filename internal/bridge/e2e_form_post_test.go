package bridge_test

// A provider that uses the form_post response mode must be able to sign in.
//
// The OAuth 2.0 response mode `form_post` delivers the authorization code in
// the body of a POST rather than in the query of a GET. It is a cross-origin
// top-level navigation, so the browser stamps it with the provider's Origin
// header -- which is exactly what the A-side's own comment says a genuine
// OAuth response looks like:
//
//     ... either no Origin is sent (a GET redirect) or the Origin is the
//     provider's own (a form_post response)
//
// The A-side validated the callback twice, and the first pass -- the one
// before the body is read -- was handed an EMPTY expectation. An empty
// expectation has no authorization origin, so any request carrying a browser
// Origin was refused as "the authorization origin is unknown". Every form_post
// callback therefore failed, with a page telling the user that Tantu could not
// verify where the sign-in came from, which was false: Tantu knew the origin
// and had compared against the wrong expectation.
//
// The second pass, which has the real expectation, would have accepted the
// same request, and was never reached.
//
// Nothing in the repository could have caught this. The only gate that mentions
// a form_post is a pure-function gate on the second pass
// (callback_csrf_test.go), and no test harness can produce one because
// internal/testutil/oauthserver implements GET /authorize only. The register
// said a form_post redirect was "accepted".
//
// Produced, not constructed: a real bridge pair over a real transport, a real
// loopback listener, and a real HTTP POST carrying the headers a browser
// sends.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// formPostResult is what the flow produced, so a gate can assert on both
// halves: what the application received, and what the person in the browser
// was shown.
type formPostResult struct {
	status       int
	pageBody     string
	clientCalled bool
	clientMethod string
	clientBody   string
	bsideErr     error
	asideErr     error
}

// runFormPostFlow runs a real bridge pair and delivers a callback to the
// A-side's callback port exactly as a browser would for a form_post response.
//
// The browser's POST is the only thing simulated. Everything else is real: a
// real listener bound on a real loopback port, a real bridge over a real
// transport, and a real delivery to the application.
func runFormPostFlow(t *testing.T, client *anyPathClient, authURL, callbackPath, origin, body string, sessionTimeout time.Duration) formPostResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tr := transport.NewLoopbackTransport()
	bridgeListener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("tr.Listen: %v", err)
	}
	defer bridgeListener.Close()

	asidePortCh := make(chan int, 1)
	asideErrCh := make(chan error, 1)
	bsideErrCh := make(chan error, 1)

	// A-side: the machine running the application.
	go func() {
		rawConn, err := bridgeListener.Accept()
		if err != nil {
			asideErrCh <- fmt.Errorf("aside accept: %w", err)
			return
		}
		defer rawConn.Close()
		aConn := &portRewritingConn{
			Conn:      rawConn,
			onAckSent: func(port int) { asidePortCh <- port },
		}
		asideErrCh <- bridge.HandleASide(ctx, aConn, bridge.ASideConfig{Timeout: sessionTimeout})
	}()

	// B-side: the machine running the browser.
	go func() {
		bConn, err := tr.Dial(bridgeListener.Addr().String())
		if err != nil {
			bsideErrCh <- fmt.Errorf("bside dial: %w", err)
			return
		}
		defer bConn.Close()
		bsideErrCh <- bridge.HandleBSide(ctx, bConn, authURL, bridge.BSideConfig{Timeout: sessionTimeout})
	}()

	var res formPostResult
	browserDone := make(chan error, 1)
	go func() {
		select {
		case port := <-asidePortCh:
			nav := &http.Client{Timeout: 10 * time.Second}
			req, reqErr := http.NewRequest(http.MethodPost,
				fmt.Sprintf("http://127.0.0.1:%d%s", port, callbackPath),
				strings.NewReader(body))
			if reqErr != nil {
				browserDone <- reqErr
				return
			}
			// The headers a browser sends for a form_post response.
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Sec-Fetch-Mode", "navigate")
			req.Header.Set("Sec-Fetch-Dest", "document")
			req.Header.Set("Origin", origin)
			resp, doErr := nav.Do(req)
			if doErr != nil {
				browserDone <- fmt.Errorf("browser navigation failed: %w", doErr)
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			res.status = resp.StatusCode
			res.pageBody = string(b)
			browserDone <- nil
		case <-ctx.Done():
			browserDone <- ctx.Err()
		}
	}()

	// The browser's response is the last thing to arrive: the A-side holds it
	// until the relay has been delivered. So once it is in, either the
	// application was called or the callback was refused.
	select {
	case err := <-browserDone:
		if err != nil {
			t.Fatalf("browser: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out: the callback never produced a browser response")
	}

	res.bsideErr = <-bsideErrCh
	res.asideErr = <-asideErrCh

	select {
	case <-client.got:
		res.clientCalled = true
		res.clientMethod = client.gotMethod
		res.clientBody = client.gotBody
	default:
	}
	return res
}

// formPostAuthURL is a provider's authorization URL for a flow that will
// answer with form_post: an https provider, a state, a PKCE challenge, and a
// loopback redirect_uri that names the path the application listens on.
func formPostAuthURL(appAddr, path, state string) string {
	return "https://accounts.google.com/o/oauth2/v2/auth?state=" + state +
		"&code_challenge=uDVzYE6slmbBbw_r61ZhoVzIQ63M0ECO0Dg1vJiroNo" +
		"&code_challenge_method=S256" +
		"&response_mode=form_post" +
		"&redirect_uri=" + url.QueryEscape("http://"+appAddr+path)
}

// TestEOECallbackByFormPostIsRelayed is the sign-in itself.
//
// A provider that answers with form_post puts the code and the state in the
// body of a POST and stamps the request with its own Origin. The application
// must receive both, and the person in the browser must be told it worked.
func TestEOECallbackByFormPostIsRelayed(t *testing.T) {
	client := newAnyPathClient(t)
	state := "4f2b8e1c-0d3a-4c7e-9a55-1f0d2b6c8e91"
	const path = "/oauth2/callback"
	authURL := formPostAuthURL(client.addr, path, state)

	body := "code=7c1f0d2b-6a55-4c7e-9a55-1f0d2b6c8e91&state=" + state
	res := runFormPostFlow(t, client, authURL, path, "https://accounts.google.com", body, 15*time.Second)

	if res.bsideErr != nil {
		t.Errorf("B-side: %v", res.bsideErr)
	}
	if res.asideErr != nil {
		t.Errorf("A-side: %v", res.asideErr)
	}
	if !res.clientCalled {
		t.Fatalf("the application was never called; a form_post response must be relayed, "+
			"and the browser was shown %d with %q", res.status, strings.TrimSpace(res.pageBody))
	}
	if res.clientMethod != http.MethodPost {
		t.Errorf("the application was called with %s, not POST", res.clientMethod)
	}
	if !strings.Contains(res.clientBody, "code=7c1f0d2b") {
		t.Errorf("the authorization code did not survive the relay in the body; body was %q", res.clientBody)
	}
	if !strings.Contains(res.clientBody, "state="+state) {
		t.Errorf("the state did not survive the relay in the body; body was %q", res.clientBody)
	}
	if res.status != http.StatusOK {
		t.Errorf("the browser saw %d rather than the success page; body: %q",
			res.status, strings.TrimSpace(res.pageBody))
	}
}

// TestEOECallbackByFormPostFromAnotherOriginIsRefused is the other half, and
// it is the half that matters.
//
// Accepting a legitimate form_post must not mean accepting a form_post from
// somewhere else. The A-side compares the browser's Origin against the origin
// the sign-in started on, and a callback from a different site must still be
// refused -- and refused for the right reason, which is a different website,
// not "Tantu could not verify where this sign-in came from".
func TestEOECallbackByFormPostFromAnotherOriginIsRefused(t *testing.T) {
	client := newAnyPathClient(t)
	state := "4f2b8e1c-0d3a-4c7e-9a55-1f0d2b6c8e91"
	const path = "/oauth2/callback"
	authURL := formPostAuthURL(client.addr, path, state)

	body := "code=7c1f0d2b-6a55-4c7e-9a55-1f0d2b6c8e91&state=" + state
	// A short session timeout: the callback is refused immediately, and the
	// gate should not wait out a full session to prove it.
	res := runFormPostFlow(t, client, authURL, path, "https://attacker.example", body, 2*time.Second)

	if res.clientCalled {
		t.Fatalf("a form_post callback from %s was delivered to the application; "+
			"the browser-CSRF defence is gone", "https://attacker.example")
	}
	if res.status == http.StatusOK {
		t.Errorf("the browser was shown a success page for a callback from another site")
	}
	lower := strings.ToLower(res.pageBody)
	if !strings.Contains(lower, "different website") {
		t.Errorf("the refusal does not say the callback came from a different website, so the "+
			"person reading it cannot tell what happened; body: %s", strings.TrimSpace(res.pageBody))
	}
	if strings.Contains(lower, "could not verify where this sign-in came from") {
		t.Errorf("the refusal says Tantu could not verify where the sign-in came from, which is "+
			"false -- Tantu knows the origin and compared it; body: %s", strings.TrimSpace(res.pageBody))
	}
}
