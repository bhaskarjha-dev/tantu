package bridge

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

// A real OAuth response always reaches the loopback listener as a top-level
// browser navigation from the provider. A page that wants to inject a code
// issues fetch()/XHR/a subresource instead, and the browser labels it. These
// tests pin that distinction, because it is the entire browser-CSRF defence: a
// loopback Host check only stops DNS rebinding, not a page that addresses
// 127.0.0.1 by IP literal.
func TestCallbackRejectsPageInjectedFetch(t *testing.T) {
	expected := callbackExpectation("https://accounts.google.com/o/oauth2/auth?client_id=x&redirect_uri=http%3A%2F%2F127.0.0.1%3A9999%2Fcallback")

	cases := []struct {
		name    string
		headers map[string]string
		wantErr bool
	}{
		{
			name:    "attacker page fetch",
			headers: map[string]string{"Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty", "Origin": "https://evil.example"},
			wantErr: true,
		},
		{
			name:    "attacker no-cors subresource",
			headers: map[string]string{"Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image"},
			wantErr: true,
		},
		{
			name:    "attacker same-origin script load",
			headers: map[string]string{"Sec-Fetch-Mode": "same-origin", "Sec-Fetch-Dest": "script"},
			wantErr: true,
		},
		{
			name:    "image beacon",
			headers: map[string]string{"Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image", "Sec-Fetch-Site": "cross-site"},
			wantErr: true,
		},
		{
			name:    "foreign origin navigation",
			headers: map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Origin": "https://evil.example"},
			wantErr: true,
		},
		{
			name:    "legitimate provider redirect without origin",
			headers: map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "cross-site"},
			wantErr: false,
		},
		{
			name:    "legitimate form_post from the provider",
			headers: map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Origin": "https://accounts.google.com"},
			wantErr: false,
		},
		{
			name:    "provider origin differing only by case and trailing slash",
			headers: map[string]string{"Sec-Fetch-Mode": "navigate", "Origin": "https://Accounts.Google.com/"},
			wantErr: false,
		},
		{
			// A client that strips the fetch metadata is not locked out; it
			// falls back to the state check.
			name:    "no fetch metadata at all",
			headers: map[string]string{},
			wantErr: false,
		},
		{
			name:    "sandboxed iframe navigation",
			headers: map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe", "Origin": "null"},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCallbackRequest(t, http.MethodGet, "http://127.0.0.1:9999/callback?code=abc&state=s", tc.headers)
			err := validateCallbackRequestState(r, expected, "")
			if tc.wantErr && err == nil {
				t.Fatal("a page-injected callback was accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("a legitimate callback was rejected: %v", err)
			}
		})
	}
}

// The old behaviour was that an authorization URL with no loopback redirect_uri
// and no state produced a completely unconstrained listener: any path, no state
// check. That is the login-CSRF hole. The browser checks must still apply.
func TestUnboundFlowStillRequiresNavigation(t *testing.T) {
	// No redirect_uri, no state: nothing binds this flow to a redirect.
	expected := callbackExpectation("https://provider.example/authorize?client_id=x&response_type=code")
	if !expected.unbound {
		t.Fatal("a URL with no loopback redirect binding must be reported as unbound")
	}
	if expected.authOrigin != "https://provider.example" {
		t.Fatalf("authorization origin = %q, want https://provider.example", expected.authOrigin)
	}

	// A page-injected request must be refused even with no path or state to
	// check, because the browser still labels it.
	r := newCallbackRequest(t, http.MethodGet, "http://127.0.0.1:9999/anything?code=attacker",
		map[string]string{"Sec-Fetch-Mode": "cors", "Origin": "https://evil.example"})
	if err := validateCallbackRequestState(r, expected, ""); err == nil {
		t.Fatal("an unbound flow accepted a page-injected callback; the login-CSRF hole is open")
	}

	// A real navigation is still accepted, because the user is signing in.
	r = newCallbackRequest(t, http.MethodGet, "http://127.0.0.1:9999/anything?code=real",
		map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"})
	if err := validateCallbackRequestState(r, expected, ""); err != nil {
		t.Fatalf("an unbound flow rejected a legitimate navigation: %v", err)
	}
}

// A flow with no known authorization origin cannot validate a browser-supplied
// Origin, so it must refuse rather than guess.
func TestOriginWithoutAuthorizationOriginIsRefused(t *testing.T) {
	expected := callbackExpectationSpec{unbound: true, authOrigin: ""}
	r := newCallbackRequest(t, http.MethodGet, "http://127.0.0.1:9999/cb?code=x",
		map[string]string{"Sec-Fetch-Mode": "navigate", "Origin": "https://somewhere.example"})
	if err := validateCallbackRequestState(r, expected, ""); err == nil {
		t.Fatal("a callback Origin must not be accepted when the authorization origin is unknown")
	}
}

// The application's state is its CSRF token. A missing state must fail even
// when the request is otherwise a clean navigation.
func TestStateIsRequiredAndMismatchIsRejected(t *testing.T) {
	expected := callbackExpectation("https://provider.example/authorize?client_id=x&redirect_uri=http%3A%2F%2F127.0.0.1%3A9999%2Fcallback&state=expected-state")
	if expected.state != "expected-state" {
		t.Fatalf("expected state = %q", expected.state)
	}
	nav := map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}

	if err := validateCallbackRequestState(
		newCallbackRequest(t, http.MethodGet, "http://127.0.0.1:9999/callback?code=x&state=expected-state", nav),
		expected, ""); err != nil {
		t.Fatalf("a matching state was rejected: %v", err)
	}
	if err := validateCallbackRequestState(
		newCallbackRequest(t, http.MethodGet, "http://127.0.0.1:9999/callback?code=x&state=wrong", nav),
		expected, ""); err == nil {
		t.Fatal("a mismatched state was accepted")
	}
	if err := validateCallbackRequestState(
		newCallbackRequest(t, http.MethodGet, "http://127.0.0.1:9999/callback?code=x", nav),
		expected, ""); err == nil {
		t.Fatal("a missing state was accepted")
	}
	// The state may arrive in a form_post body instead of the query.
	if err := validateCallbackRequestState(
		newCallbackRequest(t, http.MethodPost, "http://127.0.0.1:9999/callback", nav),
		expected, "expected-state"); err != nil {
		t.Fatalf("a form-posted state was rejected: %v", err)
	}
}

// The path binding must still hold independently of the browser checks.
func TestPathBindingStillEnforced(t *testing.T) {
	expected := callbackExpectation("https://provider.example/authorize?client_id=x&redirect_uri=http%3A%2F%2F127.0.0.1%3A9999%2Fthe%2Fcallback")
	if expected.path != "/the/callback" {
		t.Fatalf("expected path = %q, want /the/callback", expected.path)
	}
	nav := map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}
	if err := validateCallbackRequestState(
		newCallbackRequest(t, http.MethodGet, "http://127.0.0.1:9999/wrong/path?code=x", nav),
		expected, ""); err == nil {
		t.Fatal("a callback on an unexpected path was accepted")
	}
	if err := validateCallbackRequestState(
		newCallbackRequest(t, http.MethodGet, "http://127.0.0.1:9999/the/callback?code=x", nav),
		expected, ""); err != nil {
		t.Fatalf("the expected path was rejected: %v", err)
	}
}

// A non-loopback Host is still refused; the new checks must not weaken that.
func TestNonLoopbackHostStillRefused(t *testing.T) {
	expected := callbackExpectation("https://provider.example/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A9999%2Fcb")
	r := newCallbackRequest(t, http.MethodGet, "http://evil.example:9999/cb?code=x",
		map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"})
	r.Host = "evil.example:9999"
	if err := validateCallbackRequestState(r, expected, ""); err == nil {
		t.Fatal("a non-loopback Host was accepted")
	}
}

// Unsupported methods stay refused.
func TestUnsupportedCallbackMethodRefused(t *testing.T) {
	expected := callbackExpectation("https://provider.example/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A9999%2Fcb")
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead} {
		r := newCallbackRequest(t, method, "http://127.0.0.1:9999/cb", map[string]string{"Sec-Fetch-Mode": "navigate"})
		if err := validateCallbackRequestState(r, expected, ""); err == nil {
			t.Errorf("method %s was accepted", method)
		}
	}
}

// The B-side must re-verify the navigation evidence rather than trusting the
// A-side, and the inspection-only headers must never reach the local app.
func TestRelayFetchMetadataRevalidation(t *testing.T) {
	expected := callbackExpectation("https://provider.example/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A9999%2Fcb")

	bad := newRelayFor("navigate", "https://evil.example")
	if err := validateRelayFetchMetadata(bad, expected); err == nil {
		t.Fatal("a relayed callback with a foreign Origin was accepted")
	}
	good := newRelayFor("navigate", "https://provider.example")
	if err := validateRelayFetchMetadata(good, expected); err != nil {
		t.Fatalf("a legitimate relay was rejected: %v", err)
	}
	fetchy := newRelayFor("cors", "")
	if err := validateRelayFetchMetadata(fetchy, expected); err == nil {
		t.Fatal("a relayed non-navigation callback was accepted")
	}

	// Inspection-only headers must be captured but never delivered.
	captured := callbackHeaders(http.Header{
		"Content-Type":     []string{"application/x-www-form-urlencoded"},
		"Sec-Fetch-Mode":   []string{"navigate"},
		"Origin":           []string{"https://provider.example"},
		"Cookie":           []string{"session=secret"},
		"Authorization":    []string{"Bearer secret"},
		"X-Requested-With": []string{"XMLHttpRequest"},
	})
	for _, want := range []string{"Sec-Fetch-Mode", "Origin", "Content-Type", "X-Requested-With"} {
		if captured[http.CanonicalHeaderKey(want)] == "" {
			t.Errorf("expected %s to be captured for inspection: %v", want, captured)
		}
	}
	for _, forbidden := range []string{"Cookie", "Authorization"} {
		if captured[http.CanonicalHeaderKey(forbidden)] != "" {
			t.Errorf("%s must never be relayed: %v", forbidden, captured)
		}
	}
	delivered := filterCallbackRelayHeaders(captured)
	for _, forbidden := range []string{"Sec-Fetch-Mode", "Origin", "Cookie", "Authorization"} {
		if delivered[http.CanonicalHeaderKey(forbidden)] != "" {
			t.Errorf("%s must not be delivered to the local application: %v", forbidden, delivered)
		}
	}
	if delivered["Content-Type"] != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type must still be delivered, got %v", delivered)
	}
}

func newRelayFor(mode, origin string) protocol.CallbackRelay {
	headers := map[string]string{}
	if mode != "" {
		headers["Sec-Fetch-Mode"] = mode
	}
	if origin != "" {
		headers["Origin"] = origin
	}
	return protocol.CallbackRelay{Headers: headers}
}

func newCallbackRequest(t *testing.T, method, rawURL string, headers map[string]string) *http.Request {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("test URL %q: %v", rawURL, err)
	}
	r := &http.Request{
		Method: method,
		URL:    parsed,
		Host:   parsed.Host,
		Header: http.Header{},
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestCallbackExpectationRecordsAuthOrigin(t *testing.T) {
	cases := []struct {
		name     string
		rawURL   string
		wantPath string
		unbound  bool
		origin   string
	}{
		{
			name:     "loopback redirect uri binds the path",
			rawURL:   "https://provider.example/a?redirect_uri=http%3A%2F%2Flocalhost%3A1234%2Fcb",
			wantPath: "/cb",
			origin:   "https://provider.example",
		},
		{
			name:    "no redirect uri is unbound",
			rawURL:  "https://provider.example/a?client_id=x",
			unbound: true,
			origin:  "https://provider.example",
		},
		{
			name:     "loopback state binds the path",
			rawURL:   "https://provider.example/a?state=http%3A%2F%2F127.0.0.1%3A1%2Ftrampoline",
			wantPath: "/trampoline",
			origin:   "https://provider.example",
		},
		{
			name:    "unparseable url is unbound with no origin",
			rawURL:  "://not a url",
			unbound: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := callbackExpectation(tc.rawURL)
			if got.unbound != tc.unbound {
				t.Errorf("unbound = %v, want %v", got.unbound, tc.unbound)
			}
			if got.path != tc.wantPath {
				t.Errorf("path = %q, want %q", got.path, tc.wantPath)
			}
			if tc.origin != "" && !strings.EqualFold(got.authOrigin, tc.origin) {
				t.Errorf("authOrigin = %q, want %q", got.authOrigin, tc.origin)
			}
		})
	}
}
