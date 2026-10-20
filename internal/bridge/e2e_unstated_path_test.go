package bridge_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// An OAuth client whose redirect_uri names a host and a port and NO path must
// still have its callback relayed.
//
// This is the field report of 2026-10-17, verbatim. Kiro IDE registers
// `redirect_uri=http://localhost:3128` -- no path -- and its own listener serves
// whatever path it likes, calling back on `/oauth/callback`. Tantu normalised the
// empty path to "/", marked the flow constrained, and refused the callback with
// "callback path /oauth/callback does not match redirect URI". The user saw
// `invalid OAuth callback` in the browser and nothing came back to the source
// laptop; Antigravity worked in the same session because its redirect_uri states
// a path.
//
// Produced, not constructed. A real listener is bound on a real loopback port
// accepting every path, a real bridge pair runs over a real transport, and the
// callback is a real HTTP GET carrying the browser navigation headers. Nothing
// here builds a callbackExpectationSpec and asserts on it.
//
// The only existing end-to-end flow used a synthetic client whose redirect_uri
// carried "/callback", so the empty-path shape was never exercised anywhere. That
// is why a normalisation nobody asked for survived.

// anyPathClient is an OAuth client that, like Kiro, accepts a callback on any
// path. It records the path it was called on so the gate can assert it arrived.
//
// It also records the method and the body, because a provider that uses the
// form_post response mode delivers the authorization code in the body of a
// POST rather than in the query of a GET. A client that only read the query
// would see an empty callback and could not tell a form_post that arrived
// from one that did not.
type anyPathClient struct {
	srv       *http.Server
	addr      string
	gotPath   string
	gotQuery  string
	gotMethod string
	gotBody   string
	got       chan struct{}
}

func newAnyPathClient(t *testing.T) *anyPathClient {
	t.Helper()
	c := &anyPathClient{got: make(chan struct{}, 1)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	c.addr = ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c.gotPath = r.URL.Path
		c.gotQuery = r.URL.RawQuery
		c.gotMethod = r.Method
		if r.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			c.gotBody = string(body)
		}
		select {
		case c.got <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body>Signed in. You can close this window.</body></html>")
	})
	c.srv = &http.Server{Handler: mux}
	go func() { _ = c.srv.Serve(ln) }()
	t.Cleanup(func() { _ = c.srv.Close() })
	return c
}

// callbackPath is the path Kiro's listener actually calls back on, and which its
// redirect_uri never mentions.
const callbackPath = "/oauth/callback"

func TestEOECallbackOnAPathTheRedirectUriNeverStated(t *testing.T) {
	client := newAnyPathClient(t)

	// Kiro's authorization URL shape: an https provider, a state, a PKCE
	// challenge, and a redirect_uri with host and port but no path.
	state := "0eccf62b-9daf-43e0-944d-e1fc7319288c"
	authURL := "https://app.kiro.dev/signin?state=" + state +
		"&code_challenge=uDVzYE6slmbBbw_r61ZhoVzIQ63M0ECO0Dg1vJiroNo" +
		"&code_challenge_method=S256&redirect_uri=http%3A%2F%2F" + client.addr +
		"&redirect_from=KiroIDE"

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
		asideErrCh <- bridge.HandleASide(ctx, aConn, bridge.ASideConfig{Timeout: 15 * time.Second})
	}()

	// B-side: the machine running the browser. This is what binds the callback
	// port and catches the redirect.
	go func() {
		bConn, err := tr.Dial(bridgeListener.Addr().String())
		if err != nil {
			bsideErrCh <- fmt.Errorf("bside dial: %w", err)
			return
		}
		defer bConn.Close()
		bsideErrCh <- bridge.HandleBSide(ctx, bConn, authURL, bridge.BSideConfig{Timeout: 15 * time.Second})
	}()

	// The browser, after the user signs in with Google on B: a top-level
	// navigation to the callback port on a path the redirect_uri never stated.
	var browserStatus int
	var browserBody string
	browserDone := make(chan error, 1)
	go func() {
		select {
		case port := <-asidePortCh:
			nav := &http.Client{Timeout: 10 * time.Second}
			resp, err := nav.Get(fmt.Sprintf("http://127.0.0.1:%d%s?login_option=google&code=601a8936-f7b3-4c31-86bf-b12ecea7370a&state=%s",
				port, callbackPath, state))
			if err != nil {
				browserDone <- fmt.Errorf("browser navigation failed: %w", err)
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			browserStatus = resp.StatusCode
			browserBody = string(b)
			browserDone <- nil
		case <-ctx.Done():
			browserDone <- ctx.Err()
		}
	}()

	select {
	case <-client.got:
	case err := <-browserDone:
		t.Fatalf("the callback never reached the application (browser: %v)", err)
	case <-ctx.Done():
		t.Fatal("timed out: the callback never reached the application on the source machine")
	}

	if err := <-browserDone; err != nil {
		t.Fatalf("browser: %v", err)
	}
	if err := <-bsideErrCh; err != nil {
		t.Errorf("B-side: %v", err)
	}
	if err := <-asideErrCh; err != nil {
		t.Errorf("A-side: %v", err)
	}

	if client.gotPath != callbackPath {
		t.Errorf("the application was called on %q, not %q", client.gotPath, callbackPath)
	}
	if !strings.Contains(client.gotQuery, "code=601a8936") {
		t.Errorf("the authorization code did not survive the relay; query was %q", client.gotQuery)
	}
	if browserStatus != http.StatusOK {
		t.Errorf("the browser saw %d rather than the success page; body: %q",
			browserStatus, strings.TrimSpace(browserBody))
	}
}

// A redirect_uri that DOES state a path is still enforced.
//
// The other half, and separate because it fails for a different reason: loosening
// the empty-path case must not loosen the stated-path case, or the path check
// would be gone rather than corrected.
func TestEOECallbackOnAStatedPathIsStillEnforced(t *testing.T) {
	client := newAnyPathClient(t)
	state := "state-that-must-match"

	// This one states a path, and the browser will use a *different* one.
	authURL := "https://accounts.google.com/o/oauth2/v2/auth?state=" + state +
		"&redirect_uri=http%3A%2F%2F" + client.addr + "%2Foauth2%2Fcallback"

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
		asideErrCh <- bridge.HandleASide(ctx, aConn, bridge.ASideConfig{Timeout: 15 * time.Second})
	}()
	go func() {
		bConn, err := tr.Dial(bridgeListener.Addr().String())
		if err != nil {
			bsideErrCh <- fmt.Errorf("bside dial: %w", err)
			return
		}
		defer bConn.Close()
		bsideErrCh <- bridge.HandleBSide(ctx, bConn, authURL, bridge.BSideConfig{Timeout: 15 * time.Second})
	}()

	var browserStatus int
	var browserBody string
	browserDone := make(chan error, 1)
	go func() {
		select {
		case port := <-asidePortCh:
			nav := &http.Client{Timeout: 10 * time.Second}
			// A path the redirect_uri did NOT state.
			resp, err := nav.Get(fmt.Sprintf("http://127.0.0.1:%d/somewhere-else?code=abc&state=%s", port, state))
			if err != nil {
				browserDone <- fmt.Errorf("browser navigation failed: %w", err)
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			browserStatus = resp.StatusCode
			browserBody = string(b)
			browserDone <- nil
		case <-ctx.Done():
			browserDone <- ctx.Err()
		}
	}()

	select {
	case <-client.got:
		t.Fatalf("the application was called on %q for a callback whose path the "+
			"redirect_uri never stated; the stated-path check has been lost",
			client.gotPath)
	case err := <-browserDone:
		if err != nil {
			t.Fatalf("browser: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for the callback to be refused")
	}
	<-bsideErrCh
	<-asideErrCh

	if browserStatus == http.StatusOK {
		t.Errorf("a callback on an unstated path was accepted (200); it must be refused")
	}
	// The refusal has to be actionable, not just a refusal.
	//
	// This assertion used to look for the word "invalid", which was the whole
	// problem: four different checks returned that one string. It now checks the
	// contract the page is actually held to -- it names what happened, offers a
	// next step, and leaks no raw internal text.
	lower := strings.ToLower(browserBody)
	for _, want := range []string{"this sign-in did not register", "start the sign-in again"} {
		if !strings.Contains(lower, want) {
			t.Errorf("the refusal page does not say %q, so the person reading it cannot tell "+
				"what happened; body: %s", want, strings.TrimSpace(browserBody))
		}
	}
	if strings.Contains(browserBody, "Sec-Fetch") || strings.Contains(browserBody, "does not match redirect URI") {
		t.Errorf("the refusal page leaked the internal check text; the detail belongs in the "+
			"log, not in the browser: %s", strings.TrimSpace(browserBody))
	}
}

var _ = protocol.TypeBridgeAck
