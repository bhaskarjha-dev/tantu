package hub

import (
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// securityMiddleware enforces exactly two things: the one-time bootstrap
// exchange on /api/session (POST only), and — for every other path under
// /api/ — a capability (IPC header token or dashboard session cookie).
// Routes registered OUTSIDE the /api/ prefix are served with no capability
// check at all, so the set of public routes must stay small and deliberate.
//
// This test enumerates routes by reading the package source instead of a
// hand-maintained list, so a route added later cannot ship unclassified:
// an unknown route fails the test until someone decides, explicitly,
// whether it is protected or public. The earlier denial test pinned seven
// hard-coded paths; this one covers the whole surface and both directions
// (denied without capability, public only with a reason).

var (
	routeRegRe     = regexp.MustCompile(`mux\.Handle(?:Func)?\(`)
	routeLiteralRe = regexp.MustCompile(`^mux\.Handle(?:Func)?\(\s*"([^"]*)"`)
)

// publicRoutes is the complete allowlist of routes reachable without a
// capability. Adding an entry is a security decision: document why here.
var publicRoutes = map[string]string{
	"/":        "dashboard shell — the bootstrap fragment flow must load it before a session exists",
	"/healthz": "liveness probe — static \"ok\", no runtime data",
	"/relay":   "OAuth popup interstitial — refuses cookie auth and performs nothing without a relay token",
}

func enumeratedDashboardRoutes(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var routes []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		content := string(src)
		for _, loc := range routeRegRe.FindAllStringIndex(content, -1) {
			m := routeLiteralRe.FindStringSubmatch(content[loc[0]:])
			if m == nil {
				t.Fatalf("%s registers a route without a string literal at offset %d; this test cannot enumerate it — add an explicit auth assertion for it instead", name, loc[0])
			}
			if m[1] == "" {
				t.Fatalf("%s registers an empty route pattern", name)
			}
			routes = append(routes, m[1])
		}
	}
	if len(routes) == 0 {
		t.Fatal("no routes enumerated from package source — the scan pattern no longer matches the registration style")
	}
	sort.Strings(routes)
	return routes
}

func TestDashboardRoutesAreClassifiedAndGuarded(t *testing.T) {
	routes := enumeratedDashboardRoutes(t)

	h, _, cleanup := startTestHub(t)
	defer cleanup()
	base := "http://" + h.WebAddr()

	// Denials must arrive promptly. Without a bound, a request that
	// escapes the middleware into a long-lived handler (the /api/events
	// SSE stream, a resume upload) blocks the sweep forever instead of
	// failing it; the timeout turns that into a named assertion.
	client := &http.Client{Timeout: 10 * time.Second}

	// want performs an unauthenticated request and asserts the exact status.
	want := func(method, path string, status int) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, nil)
		if err != nil {
			t.Fatalf("build %s %s: %v", method, path, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("unauthenticated %s %s never got a response (reached a live handler?): %v", method, path, err)
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != status {
			t.Errorf("unauthenticated %s %s = %d, want %d", method, path, resp.StatusCode, status)
		}
	}

	seen := map[string]bool{}
	for _, route := range routes {
		if seen[route] {
			continue
		}
		seen[route] = true

		if !strings.HasPrefix(route, "/api/") {
			if _, ok := publicRoutes[route]; !ok {
				t.Errorf("route %q is registered outside /api/ — securityMiddleware serves it with no capability check; either move it under /api/ or add it to publicRoutes with a reason", route)
			}
			continue
		}

		if route == "/api/session" {
			// The only unauthenticated API operation: POST-only, one-use
			// bootstrap value, 403 without it. It must never degrade into
			// a generic session mint for capability-less callers.
			want(http.MethodGet, route, http.StatusMethodNotAllowed)
			want(http.MethodOptions, route, http.StatusMethodNotAllowed)
			want(http.MethodPost, route, http.StatusForbidden)
			continue
		}

		want(http.MethodGet, route, http.StatusUnauthorized)
		want(http.MethodPost, route, http.StatusUnauthorized)
		want(http.MethodPatch, route, http.StatusUnauthorized)

		// OPTIONS must be answered by the middleware itself — 204 with an
		// empty body — and never reach an unauthenticated handler.
		req, err := http.NewRequest(http.MethodOptions, base+route, nil)
		if err != nil {
			t.Fatalf("build OPTIONS %s: %v", route, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("unauthenticated OPTIONS %s never got a response (reached a live handler?): %v", route, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent || len(body) != 0 {
			t.Errorf("unauthenticated OPTIONS %s = %d (body %d bytes), want 204 with no body — the preflight reached the handler", route, resp.StatusCode, len(body))
		}
	}

	// A renamed or removed public route must not leave a stale allowlist
	// entry claiming the surface is still deliberate.
	for route := range publicRoutes {
		if !seen[route] {
			t.Errorf("publicRoutes lists %q but no handler registers it — remove the stale entry", route)
		}
	}

	// The public probes stay byte-benign: no session state, no runtime data.
	for _, probe := range []string{"/", "/healthz"} {
		resp, err := client.Get(base + probe)
		if err != nil {
			t.Fatalf("GET %s failed: %v", probe, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", probe, resp.StatusCode)
		}
		if probe == "/healthz" && string(body) != "ok" {
			t.Errorf("GET /healthz body = %q, want static \"ok\"", body)
		}
		if probe == "/" && !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			t.Errorf("GET / Content-Type = %q, want text/html", resp.Header.Get("Content-Type"))
		}
	}

	// /relay is public only because it refuses to act without a relay
	// token; assert it is genuinely reachable (the classification above is
	// accurate) and that cookie auth alone does not flip it to a
	// capability surface expectation. Its token gating is covered by the
	// relay-flow tests.
	resp, err := client.Get(base + "/relay")
	if err != nil {
		t.Fatalf("GET /relay failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Errorf("GET /relay = %d — the route is listed public but behaves as a capability surface; move it out of publicRoutes", resp.StatusCode)
	}
}
