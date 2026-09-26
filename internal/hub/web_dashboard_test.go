package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

func testGet(token, endpoint string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set(IPCTokenHeader, token)
	}
	return http.DefaultClient.Do(req)
}

func testPost(token, endpoint, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(IPCTokenHeader, token)
	return http.DefaultClient.Do(req)
}

func startTestHub(t *testing.T) (*Hub, context.CancelFunc, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "hub-dashboard-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	h, err := NewHub(HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      tempDir,
		OutputDir:     filepath.Join(tempDir, "drops"),
		Headless:      true,
	})
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("NewHub failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Start(ctx)
	}()

	select {
	case <-h.Ready():
	case err := <-errCh:
		cancel()
		os.RemoveAll(tempDir)
		t.Fatalf("Hub failed to start: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		os.RemoveAll(tempDir)
		t.Fatalf("timed out waiting for Hub")
	}

	cleanup := func() {
		cancel()
		os.RemoveAll(tempDir)
	}

	return h, cancel, cleanup
}

func TestWebDashboard_ServeSPA(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	resp, err := http.Get("http://" + h.WebAddr() + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' 'nonce-") || !strings.Contains(csp, "'unsafe-hashes'") {
		t.Errorf("dashboard CSP does not authorize the nonce/hash-scoped script: %q", csp)
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("dashboard CSP regressed to unsafe-inline scripts: %q", csp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	content := string(body)

	requiredStrings := []string{
		"<!DOCTYPE html>",
		"tantu",
		"QuickDrop",
		"OAuth Relay",
		"Peers & Network",
		"Live Logs",
		"/api/drop/upload",
		"/api/relay/open",
		"/api/events",
	}

	for _, s := range requiredStrings {
		if !strings.Contains(content, s) {
			t.Errorf("dashboard HTML missing expected substring %q", s)
		}
	}

	// The dashboard is embedded in a Go raw string. A literal script-closing
	// tag anywhere in the JavaScript (including a comment) closes the HTML
	// script element early, leaving the rest of the source as visible text
	// and preventing the polling/bootstrap code from running.
	if got := strings.Count(content, "</script>"); got != 1 {
		t.Fatalf("dashboard HTML contains %d script-closing tags, want exactly one", got)
	}
}

func TestWebDashboard_ProtectedReadsRequireCapability(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	paths := []string{"/api/status", "/api/logs", "/api/drop/recent", "/api/transfers/recent", "/api/relay/recent", "/api/pair/pending", "/api/discovery/peers"}
	for _, path := range paths {
		resp, err := http.Get("http://" + h.WebAddr() + path)
		if err != nil {
			t.Fatalf("GET %s failed: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected unauthenticated GET %s to be unauthorized, got %d", path, resp.StatusCode)
		}
	}

	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/drop/recent")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("capability-bearing GET returned %d", resp.StatusCode)
	}
}

func TestWebDashboard_HeadlessURLEndpointMintsAuthenticatedLink(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	resp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/dashboard-url", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard URL endpoint status = %d, want 200", resp.StatusCode)
	}
	var payload struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "ok" {
		t.Fatalf("dashboard URL status = %q", payload.Status)
	}
	parsed, err := url.Parse(payload.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "http" || parsed.Host != h.WebAddr() || parsed.Path != "/" {
		t.Fatalf("dashboard URL points at unexpected endpoint: %q", payload.URL)
	}
	if !strings.HasPrefix(parsed.Fragment, "tantu_bootstrap=") || strings.Contains(parsed.Fragment, "&") {
		t.Fatalf("dashboard URL has invalid bootstrap fragment: %q", parsed.Fragment)
	}
	if strings.Contains(resp.Header.Get("Cache-Control"), "max-age") {
		t.Fatal("dashboard URL response must not be cacheable")
	}

	unauth, err := http.Post("http://"+h.WebAddr()+"/api/dashboard-url", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated dashboard URL status = %d, want 401", unauth.StatusCode)
	}
}

func TestHub_NewDashboardURLRotatesBootstrap(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()
	first := h.DashboardURL()
	second := h.NewDashboardURL()
	if first == "" || second == "" || first == second {
		t.Fatalf("dashboard bootstrap links were not independently minted: first=%q second=%q", first, second)
	}
}

func TestWebDashboard_UnauthenticatedPageAvoidsAPIProbeBurst(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	resp, err := http.Get("http://" + h.WebAddr() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	content := string(body)
	if !strings.Contains(content, "const pageSessionReady = false;") {
		t.Fatal("unauthenticated dashboard did not advertise its session state")
	}
	if strings.Contains(content, h.ipcToken) || strings.Contains(content, h.relayToken) {
		t.Fatal("dashboard HTML exposed a local capability")
	}
}

func TestWebDashboard_BootstrapSessionAndNoEmbeddedTokens(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	rootResp, err := http.Get("http://" + h.WebAddr() + "/")
	if err != nil {
		t.Fatal(err)
	}
	rootBody, _ := io.ReadAll(rootResp.Body)
	rootResp.Body.Close()
	if rootResp.StatusCode != http.StatusOK {
		t.Fatalf("root status = %d", rootResp.StatusCode)
	}
	if strings.Contains(string(rootBody), h.ipcToken) || strings.Contains(string(rootBody), h.relayToken) {
		t.Fatal("dashboard HTML exposed a long-lived local capability")
	}
	if got := rootResp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("root Cache-Control = %q, want no-store", got)
	}

	dashboardURL, err := url.Parse(h.DashboardURL())
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := strings.TrimPrefix(dashboardURL.Fragment, "tantu_bootstrap=")
	if bootstrap == "" {
		t.Fatal("DashboardURL did not contain a bootstrap fragment")
	}

	exchange, err := http.NewRequest(http.MethodPost, "http://"+h.WebAddr()+"/api/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	exchange.Header.Set("X-Tantu-Dashboard-Bootstrap", bootstrap)
	exchangeResp, err := http.DefaultClient.Do(exchange)
	if err != nil {
		t.Fatal(err)
	}
	exchangeResp.Body.Close()
	if exchangeResp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap exchange status = %d", exchangeResp.StatusCode)
	}
	cookies := exchangeResp.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "tantu_dashboard_session" || !cookies[0].HttpOnly {
		t.Fatalf("bootstrap response did not set an HttpOnly session cookie: %+v", cookies)
	}

	second, err := http.NewRequest(http.MethodPost, "http://"+h.WebAddr()+"/api/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	second.Header.Set("X-Tantu-Dashboard-Bootstrap", bootstrap)
	secondResp, err := http.DefaultClient.Do(second)
	if err != nil {
		t.Fatal(err)
	}
	secondResp.Body.Close()
	if secondResp.StatusCode != http.StatusForbidden {
		t.Fatalf("reused bootstrap status = %d, want 403", secondResp.StatusCode)
	}

	statusReq, err := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/api/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	statusReq.AddCookie(cookies[0])
	statusResp, err := http.DefaultClient.Do(statusReq)
	if err != nil {
		t.Fatal(err)
	}
	statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("session-authorized status = %d", statusResp.StatusCode)
	}
	authenticatedRoot, err := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	authenticatedRoot.AddCookie(cookies[0])
	authenticatedRootResp, err := http.DefaultClient.Do(authenticatedRoot)
	if err != nil {
		t.Fatal(err)
	}
	authenticatedRootBody, _ := io.ReadAll(authenticatedRootResp.Body)
	authenticatedRootResp.Body.Close()
	if !strings.Contains(string(authenticatedRootBody), "/relay?url=") || strings.Contains(string(authenticatedRootBody), "ticket=") {
		t.Fatal("authenticated dashboard bookmark must be ticket-free and permanent")
	}
	cookieOnlyRelay, err := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/relay?url=https%3A%2F%2Fexample.com%2F", nil)
	if err != nil {
		t.Fatal(err)
	}
	cookieOnlyRelay.AddCookie(cookies[0])
	cookieOnlyRelayResp, err := http.DefaultClient.Do(cookieOnlyRelay)
	if err != nil {
		t.Fatal(err)
	}
	cookieOnlyRelayBody, _ := io.ReadAll(cookieOnlyRelayResp.Body)
	cookieOnlyRelayResp.Body.Close()
	// The session cookie must not authorize a relay by itself: a cookie-only
	// request renders the confirmation shell (200) but records no attempt.
	// The relay itself still needs an explicit click plus POST-time session.
	if cookieOnlyRelayResp.StatusCode != http.StatusOK {
		t.Fatalf("cookie-only legacy relay status = %d, want 200 interstitial", cookieOnlyRelayResp.StatusCode)
	}
	if !strings.Contains(string(cookieOnlyRelayBody), "Relay this authorization?") {
		t.Fatal("cookie-only relay did not render the confirmation interstitial")
	}
	if got := h.listRelayAttempts(); len(got) != 0 {
		t.Fatalf("rendering the interstitial must not record an attempt, got %+v", got)
	}
}

func TestWebDashboard_ForgedAuthorityRejected(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	req, err := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/api/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "127.0.0.1:1"
	req.Header.Set("Origin", "http://127.0.0.1:1")
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged authority status = %d, want 403", resp.StatusCode)
	}
}

func TestWebDashboard_LegacyRelayBookmarkletEndpoint(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// 1. Missing URL parameter (JSON by default)
	resp, err := http.Get("http://" + h.WebAddr() + "/relay")
	if err != nil {
		t.Fatalf("GET /relay failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 for missing url, got %d", resp.StatusCode)
	}

	var data relayResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if data.Status != "error" {
		t.Errorf("expected error status, got %s", data.Status)
	}

	// 2. Missing URL parameter with Accept: text/html (Browser popup mode)
	req, _ := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/relay", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	htmlResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /relay with Accept: text/html failed: %v", err)
	}
	defer htmlResp.Body.Close()

	if htmlResp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 for missing url in html mode, got %d", htmlResp.StatusCode)
	}
	if ct := htmlResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected Content-Type text/html, got %q", ct)
	}
	htmlBody, _ := io.ReadAll(htmlResp.Body)
	if !strings.Contains(string(htmlBody), "Relay Error") {
		t.Errorf("expected HTML body to contain 'Relay Error', got: %s", string(htmlBody))
	}

	// Missing-URL validation must not emit wildcard CORS.
	if origin := resp.Header.Get("Access-Control-Allow-Origin"); origin != "" {
		t.Errorf("expected no wildcard CORS header, got %q", origin)
	}
}

func TestWebDashboard_RelayInterstitial(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// 1. A burned/expired ticket degrades to the interstitial, not a 403.
	expired, err := http.Get("http://" + h.WebAddr() + "/relay?url=https%3A%2F%2Fexample.com%2Flogin&ticket=deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	expiredBody, _ := io.ReadAll(expired.Body)
	expired.Body.Close()
	if expired.StatusCode != http.StatusOK {
		t.Fatalf("expired-ticket relay status = %d, want 200 interstitial", expired.StatusCode)
	}
	if !strings.Contains(string(expiredBody), "already expired") {
		t.Error("expired-ticket interstitial must explain the extra step")
	}
	if got := h.listRelayAttempts(); len(got) != 0 {
		t.Fatalf("interstitial must not record an attempt, got %+v", got)
	}

	// 2. Secrets never enter the shell: query values stay out of the HTML.
	secret, err := http.Get("http://" + h.WebAddr() + "/relay?url=" + url.QueryEscape("https://accounts.google.com/o/oauth2/v2/auth?client_id=x&code=SECRET999"))
	if err != nil {
		t.Fatal(err)
	}
	secretBody, _ := io.ReadAll(secret.Body)
	secret.Body.Close()
	if secret.StatusCode != http.StatusOK {
		t.Fatalf("secret-URL relay status = %d, want 200", secret.StatusCode)
	}
	if strings.Contains(string(secretBody), "SECRET999") || strings.Contains(string(secretBody), "code=") {
		t.Error("interstitial leaked URL secret material")
	}
	if !strings.Contains(string(secretBody), "accounts.google.com") {
		t.Error("interstitial must show the safe origin host")
	}
	if !strings.Contains(string(secretBody), "btnRelay.focus()") {
		t.Error("interstitial must autofocus the Relay button for keyboard confirmation")
	}
	// The shell must be fully static: a different secret in the same origin
	// must produce a byte-identical page (nothing user-specific embedded).
	secret2, err := http.Get("http://" + h.WebAddr() + "/relay?url=" + url.QueryEscape("https://accounts.google.com/o/oauth2/v2/auth?client_id=x&code=OTHER777"))
	if err != nil {
		t.Fatal(err)
	}
	secretBody2, _ := io.ReadAll(secret2.Body)
	secret2.Body.Close()
	if string(secretBody) != string(secretBody2) {
		t.Error("interstitial shell is not static across secrets")
	}

	// 3. Non-relayable URLs fail closed before any shell renders.
	bad, err := http.Get("http://" + h.WebAddr() + "/relay?url=" + url.QueryEscape("javascript:alert(1)"))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("dangerous-scheme relay status = %d, want 400", bad.StatusCode)
	}
}

func TestWebDashboard_APIStatus(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/status")
	if err != nil {
		t.Fatalf("GET /api/status failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var status statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}

	if status.Status != "online" {
		t.Errorf("expected status online, got %s", status.Status)
	}
	if status.Version != HubVersion {
		t.Errorf("expected version %s, got %s", HubVersion, status.Version)
	}
	if status.Transport != "loopback" {
		t.Errorf("expected transport loopback, got %s", status.Transport)
	}
	if status.WebAddr != h.WebAddr() {
		t.Errorf("expected web addr %s, got %s", h.WebAddr(), status.WebAddr)
	}
	if status.P2PAddr != h.P2PAddr().String() {
		t.Errorf("expected p2p addr %s, got %s", h.P2PAddr().String(), status.P2PAddr)
	}
	if status.Identity.Fingerprint == "" || status.Identity.SAS == "" {
		t.Errorf("expected non-empty identity fingerprint and SAS: %+v", status.Identity)
	}
}

func TestWebDashboard_EventsSSE(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	reqCtx, reqCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer reqCancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://"+h.WebAddr()+"/api/events", nil)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	req.Header.Set(IPCTokenHeader, h.ipcToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read SSE initial event line: %v", err)
	}
	if !strings.HasPrefix(line, "data:") && !strings.HasPrefix(line, ":") {
		t.Errorf("expected initial SSE line to start with 'data:' or ':', got %q", line)
	}
}

func TestWebDashboard_DropRecent(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// Initially empty
	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/drop/recent")
	if err != nil {
		t.Fatalf("GET /api/drop/recent failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	var items []ReceivedDropItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items initially, got %d", len(items))
	}

	// Add item and check again
	h.RecentDrops().Add(ReceivedDropItem{
		ID:        "drop-1",
		Timestamp: time.Now(),
		Kind:      "text",
		Name:      "snippet.txt",
		Size:      12,
		Content:   "hello world",
		FromPeer:  "TestPeer",
		IsURL:     false,
	})

	resp2, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/drop/recent")
	if err != nil {
		t.Fatalf("GET /api/drop/recent failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp2.StatusCode)
	}
	var items2 []ReceivedDropItem
	if err := json.NewDecoder(resp2.Body).Decode(&items2); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}
	if len(items2) != 1 || items2[0].Content != "hello world" {
		t.Errorf("expected 1 item with content 'hello world', got %+v", items2)
	}
}

func TestWebDashboard_MutatingEndpointRequiresIPCToken(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	body := strings.NewReader(`{"output_dir":"/tmp/tantu-token-test"}`)
	req, err := http.NewRequest(http.MethodPost, "http://"+h.WebAddr()+"/api/config", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected token-less mutation to be unauthorized, got %d", resp.StatusCode)
	}
}

func TestWebDashboard_ConfigEndpoints(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// 1. GET /api/config
	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/config")
	if err != nil {
		t.Fatalf("GET /api/config failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	var cfg map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode config failed: %v", err)
	}
	if cfg["output_dir"] != h.OutputDir() {
		t.Errorf("expected output_dir %q, got %q", h.OutputDir(), cfg["output_dir"])
	}

	// 2. POST /api/config
	newDir := filepath.Join(os.TempDir(), "new-drops-dir")
	postBody, _ := json.Marshal(map[string]string{"output_dir": newDir})
	postResp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/config", "application/json", strings.NewReader(string(postBody)))
	if err != nil {
		t.Fatalf("POST /api/config failed: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for POST /api/config, got %d", postResp.StatusCode)
	}
	if h.OutputDir() != newDir {
		t.Errorf("expected h.OutputDir() to be %q, got %q", newDir, h.OutputDir())
	}

	// 3. POST /api/config with a file-as-directory fails fast instead of
	// accepting a directory that later breaks every transfer.
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	badBody, _ := json.Marshal(map[string]string{"output_dir": blocker})
	badResp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/config", "application/json", strings.NewReader(string(badBody)))
	if err != nil {
		t.Fatalf("POST /api/config bad dir failed: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for file-as-directory, got %d", badResp.StatusCode)
	}
	if h.OutputDir() != newDir {
		t.Errorf("bad config must not change OutputDir: got %q", h.OutputDir())
	}
}

func TestWebDashboard_OpenFolderEndpoint(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	resp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/open-folder", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/open-folder failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for POST /api/open-folder, got %d", resp.StatusCode)
	}
	var res map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode open-folder response failed: %v", err)
	}
	if res["status"] != "success" {
		t.Errorf("expected status 'success', got %q", res["status"])
	}
}

func TestWebDashboard_PeersManagementEndpoints(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// Seed two peers
	peer1 := pairing.Peer{
		Fingerprint: "fp-1111111111111111",
		Address:     "192.168.1.10:9877",
		Name:        "Laptop",
	}
	peer2 := pairing.Peer{
		Fingerprint: "fp-2222222222222222",
		Address:     "192.168.1.20:9877",
		Name:        "Desktop",
	}
	if err := h.Store().AddPeer(peer1); err != nil {
		t.Fatalf("add peer1 failed: %v", err)
	}
	if err := h.Store().AddPeer(peer2); err != nil {
		t.Fatalf("add peer2 failed: %v", err)
	}

	webAddr := h.WebAddr()

	// 1. POST /api/peers/alias
	aliasReq, _ := json.Marshal(map[string]string{
		"fingerprint": "fp-1111111111111111",
		"alias":       "MacBook Pro",
	})
	resp, err := testPost(h.ipcToken, "http://"+webAddr+"/api/peers/alias", "application/json", bytes.NewReader(aliasReq))
	if err != nil {
		t.Fatalf("POST /api/peers/alias failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for /api/peers/alias, got %d", resp.StatusCode)
	}

	p1, err := h.Store().ResolvePeer("fp-1111111111111111")
	if err != nil || p1.Alias != "MacBook Pro" {
		t.Errorf("expected alias 'MacBook Pro', got %q (err: %v)", p1.Alias, err)
	}

	// 2. POST /api/peers/default
	defReq, _ := json.Marshal(map[string]string{
		"fingerprint": "fp-2222222222222222",
	})
	resp2, err := testPost(h.ipcToken, "http://"+webAddr+"/api/peers/default", "application/json", bytes.NewReader(defReq))
	if err != nil {
		t.Fatalf("POST /api/peers/default failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for /api/peers/default, got %d", resp2.StatusCode)
	}

	p2, err := h.Store().ResolvePeer("fp-2222222222222222")
	if err != nil || !p2.IsDefault {
		t.Errorf("expected peer2 to be default, got isDefault=%v", p2.IsDefault)
	}

	// 3. POST /api/peers/active
	actReq, _ := json.Marshal(map[string]string{
		"peer": "fp-1111111111111111",
	})
	resp3, err := testPost(h.ipcToken, "http://"+webAddr+"/api/peers/active", "application/json", bytes.NewReader(actReq))
	if err != nil {
		t.Fatalf("POST /api/peers/active failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for /api/peers/active, got %d", resp3.StatusCode)
	}
	if h.GetActivePeer() != "fp-1111111111111111" {
		t.Errorf("expected active peer fp-1111111111111111, got %s", h.GetActivePeer())
	}

	// 4. GET /api/status reflection
	respStatus, err := testGet(h.ipcToken, "http://"+webAddr+"/api/status")
	if err != nil {
		t.Fatalf("GET /api/status failed: %v", err)
	}
	defer respStatus.Body.Close()
	var statusData statusResponse
	if err := json.NewDecoder(respStatus.Body).Decode(&statusData); err != nil {
		t.Fatalf("decode /api/status failed: %v", err)
	}
	if statusData.ActivePeer != "fp-1111111111111111" {
		t.Errorf("expected status ActivePeer fp-1111111111111111, got %s", statusData.ActivePeer)
	}
	if len(statusData.Peers) != 2 {
		t.Fatalf("expected 2 peers in status, got %d", len(statusData.Peers))
	}
	var foundP1, foundP2 bool
	for _, pr := range statusData.Peers {
		if pr.Fingerprint == "fp-1111111111111111" {
			foundP1 = true
			if !pr.Active || pr.Alias != "MacBook Pro" || pr.Name != "MacBook Pro" {
				t.Errorf("unexpected pr1 status: %+v", pr)
			}
		}
		if pr.Fingerprint == "fp-2222222222222222" {
			foundP2 = true
			if !pr.IsDefault {
				t.Errorf("expected pr2 to be default in status: %+v", pr)
			}
		}
	}
	if !foundP1 || !foundP2 {
		t.Errorf("did not find both peers in status response")
	}

	// 5. POST /api/peers/remove
	remReq, _ := json.Marshal(map[string]string{
		"fingerprint": "fp-1111111111111111",
	})
	resp4, err := testPost(h.ipcToken, "http://"+webAddr+"/api/peers/remove", "application/json", bytes.NewReader(remReq))
	if err != nil {
		t.Fatalf("POST /api/peers/remove failed: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for /api/peers/remove, got %d", resp4.StatusCode)
	}
	if len(h.Store().ListPeers()) != 1 {
		t.Errorf("expected 1 peer remaining, got %d", len(h.Store().ListPeers()))
	}
	if h.GetActivePeer() != "" {
		t.Errorf("expected active peer to be cleared, got %q", h.GetActivePeer())
	}
}

func TestHub_ActivePeerLiveValidationAfterExternalUnpair(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()
	peer := pairing.Peer{
		Fingerprint: "ababababababababababababababababababababababababababababababababab",
		Name:        "external-unpair",
		Address:     "10.0.0.44:9877",
	}
	if err := h.Store().AddPeer(peer); err != nil {
		t.Fatal(err)
	}
	if err := h.SetActivePeer(peer.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := h.Store().RemovePeer(peer.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if got := h.GetActivePeer(); got != "" {
		t.Fatalf("active peer survived external unpair: %q", got)
	}
}

func TestWebDashboard_DropUploadWithTargetPeer(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	webAddr := h.WebAddr()

	// 1. JSON text upload with target peer (dials non-existent target address, returns 502)
	textReq, _ := json.Marshal(map[string]string{
		"text": "Hello Peer Forwarding",
		"peer": "127.0.0.1:59992",
	})
	resp1, err := testPost(h.ipcToken, "http://"+webAddr+"/api/drop/upload", "application/json", bytes.NewReader(textReq))
	if err != nil {
		t.Fatalf("POST /api/drop/upload text failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 Bad Gateway for unreachable peer, got %d", resp1.StatusCode)
	}

	// 2. Multipart file upload with target peer
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.WriteField("peer", "127.0.0.1:59993")
	part, err := w.CreateFormFile("file", "test.txt")
	if err != nil {
		t.Fatalf("CreateFormFile failed: %v", err)
	}
	_, _ = part.Write([]byte("file drop peer test"))
	_ = w.Close()

	req, err := http.NewRequest(http.MethodPost, "http://"+webAddr+"/api/drop/upload", &b)
	if err != nil {
		t.Fatalf("create multipart request failed: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set(IPCTokenHeader, h.ipcToken)

	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("multipart POST failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 Bad Gateway for unreachable multipart peer, got %d", resp2.StatusCode)
	}
}

func TestWebDashboard_DiscoveryPeersEndpoint(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	webAddr := h.WebAddr()

	// 1. GET /api/discovery/peers returns 200 and a valid JSON list
	resp, err := testGet(h.ipcToken, "http://"+webAddr+"/api/discovery/peers")
	if err != nil {
		t.Fatalf("GET /api/discovery/peers failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("expected application/json content-type, got %q", contentType)
	}

	var discovered []discoveredPeerResponse
	if err := json.NewDecoder(resp.Body).Decode(&discovered); err != nil {
		t.Fatalf("failed to decode /api/discovery/peers JSON: %v", err)
	}

	if discovered == nil {
		t.Errorf("expected non-nil discovered peers slice")
	}

	// 2. GET /api/status includes discovered_count / discovered_peers
	statusResp, err := testGet(h.ipcToken, "http://"+webAddr+"/api/status")
	if err != nil {
		t.Fatalf("GET /api/status failed: %v", err)
	}
	defer statusResp.Body.Close()

	var statusData map[string]any
	if err := json.NewDecoder(statusResp.Body).Decode(&statusData); err != nil {
		t.Fatalf("failed to decode /api/status JSON: %v", err)
	}

	if _, ok := statusData["discovered_count"]; !ok {
		t.Errorf("expected 'discovered_count' field in status response")
	}
	if _, ok := statusData["discovered_peers"]; !ok {
		t.Errorf("expected 'discovered_peers' field in status response")
	}

	// 3. Test method not allowed for POST
	postResp, err := testPost(h.ipcToken, "http://"+webAddr+"/api/discovery/peers", "application/json", strings.NewReader("{}"))
	if err == nil {
		defer postResp.Body.Close()
		if postResp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("expected status 405 Method Not Allowed, got %d", postResp.StatusCode)
		}
	}

	// 4. Test Stop() lifecycle method terminates cleanly
	if err := h.Stop(); err != nil {
		t.Errorf("Hub.Stop() returned unexpected error: %v", err)
	}
	if h.running {
		t.Errorf("expected Hub.running to be false after Stop()")
	}
}

func TestWebDashboard_CrossOriginProtection(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	client := &http.Client{Timeout: 2 * time.Second}
	webAddr := h.WebAddr()

	// 1. Foreign origin request to /api/status MUST be rejected with 403 Forbidden
	req, _ := http.NewRequest(http.MethodGet, "http://"+webAddr+"/api/status", nil)
	req.Header.Set("Origin", "http://evil.com")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403 Forbidden for Origin evil.com, got %d", resp.StatusCode)
	}

	// 2. Foreign origin request to /api/config MUST be rejected with 403 Forbidden
	req, _ = http.NewRequest(http.MethodPost, "http://"+webAddr+"/api/config", strings.NewReader(`{"output_dir":"/tmp"}`))
	req.Header.Set("Origin", "http://attacker.site")
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403 Forbidden for mutating config, got %d", resp.StatusCode)
	}

	// 3. Foreign referer request to /api/open-folder MUST be rejected with 403 Forbidden
	req, _ = http.NewRequest(http.MethodPost, "http://"+webAddr+"/api/open-folder", nil)
	req.Header.Set("Referer", "http://malicious.com/exploit.html")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403 Forbidden for Referer malicious.com, got %d", resp.StatusCode)
	}

	// 4. Allowed local origin request to /api/status succeeds (200 OK) and receives Access-Control-Allow-Origin
	req, _ = http.NewRequest(http.MethodGet, "http://"+webAddr+"/api/status", nil)
	req.Header.Set("Origin", "http://"+h.WebAddr())
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for local origin, got %d", resp.StatusCode)
	}
	if allowOrigin := resp.Header.Get("Access-Control-Allow-Origin"); allowOrigin != "http://"+h.WebAddr() {
		t.Errorf("expected Access-Control-Allow-Origin %q, got %q", "http://"+h.WebAddr(), allowOrigin)
	}

	// 5. Preflight OPTIONS request from allowed local origin returns 204 No Content
	req, _ = http.NewRequest(http.MethodOptions, "http://"+webAddr+"/api/status", nil)
	req.Header.Set("Origin", "http://localhost:"+strings.Split(h.WebAddr(), ":")[1])
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected status 204 No Content for OPTIONS, got %d", resp.StatusCode)
	}
}

func TestWebDashboard_PairInitiate_Success(t *testing.T) {
	tempDirA, err := os.MkdirTemp("", "hub-dash-pair-a-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDirA)

	tempDirB, err := os.MkdirTemp("", "hub-dash-pair-b-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDirB)

	hA, err := NewHub(HubConfig{
		TransportType:     "lan",
		ListenAddr:        "127.0.0.1:0",
		WebAddr:           "127.0.0.1:0",
		StoreDir:          tempDirA,
		Headless:          true,
		AutoAcceptPairing: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	hB, err := NewHub(HubConfig{
		TransportType:     "lan",
		ListenAddr:        "127.0.0.1:0",
		WebAddr:           "127.0.0.1:0",
		StoreDir:          tempDirB,
		Headless:          true,
		AutoAcceptPairing: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = hA.Start(ctx) }()
	go func() { _ = hB.Start(ctx) }()

	select {
	case <-hA.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Hub A")
	}
	select {
	case <-hB.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Hub B")
	}

	defer func() {
		_ = hA.Stop()
		_ = hB.Stop()
		cancel()
	}()

	// Hub B browser dashboard initiates pairing with Hub A's P2P address
	pairReqBody, _ := json.Marshal(map[string]string{
		"peer_addr": hA.P2PAddr().String(),
	})
	req, _ := http.NewRequest(http.MethodPost, "http://"+hB.WebAddr()+"/api/pair/initiate", bytes.NewReader(pairReqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+hB.WebAddr())
	req.Header.Set(IPCTokenHeader, hB.ipcToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/pair/initiate failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(body))
	}

	var pairRes struct {
		Status   string `json:"status"`
		Accepted bool   `json:"accepted"`
		PeerAddr string `json:"peer_addr"`
		PeerSAS  string `json:"peer_sas"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pairRes); err != nil {
		t.Fatalf("decode pair response failed: %v", err)
	}

	if pairRes.Status != "paired" || !pairRes.Accepted {
		t.Fatalf("unexpected pair result: %+v", pairRes)
	}

	// Verify peers in stores
	peersB := hB.Store().ListPeers()
	if len(peersB) != 1 {
		t.Fatalf("expected 1 peer in hB store, got %d", len(peersB))
	}

	// Verify hub A received peer
	deadline := time.Now().Add(2 * time.Second)
	var peersA []pairing.Peer
	for time.Now().Before(deadline) {
		peersA = hA.Store().ListPeers()
		if len(peersA) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(peersA) != 1 {
		t.Fatalf("expected 1 peer in hA store, got %d", len(peersA))
	}
}

func TestWebDashboard_RejectedPairingIsNotReportedSuccess(t *testing.T) {
	newPairHub := func(t *testing.T) *Hub {
		t.Helper()
		h, err := NewHub(HubConfig{
			TransportType:     "lan",
			ListenAddr:        "127.0.0.1:0",
			WebAddr:           "127.0.0.1:0",
			StoreDir:          t.TempDir(),
			Headless:          true,
			AutoAcceptPairing: false,
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	hA, hB := newPairHub(t), newPairHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errA, errB := make(chan error, 1), make(chan error, 1)
	go func() { errA <- hA.Start(ctx) }()
	go func() { errB <- hB.Start(ctx) }()
	waitReady := func(h *Hub, errCh <-chan error) {
		t.Helper()
		select {
		case <-h.Ready():
		case err := <-errCh:
			t.Fatalf("Hub failed to start: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("Hub start timed out")
		}
	}
	waitReady(hA, errA)
	waitReady(hB, errB)
	defer func() { _ = hA.Stop(); _ = hB.Stop() }()

	initiate := make(chan *http.Response, 1)
	initiateErr := make(chan error, 1)
	go func() {
		body, _ := json.Marshal(map[string]string{"peer_addr": hA.P2PAddr().String()})
		req, _ := http.NewRequest(http.MethodPost, "http://"+hB.WebAddr()+"/api/pair/initiate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(IPCTokenHeader, hB.ipcToken)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			initiateErr <- err
			return
		}
		initiate <- resp
	}()

	waitPending := func(h *Hub) *PendingPairing {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/pair/pending")
			if err == nil {
				var list []*PendingPairing
				_ = json.NewDecoder(resp.Body).Decode(&list)
				resp.Body.Close()
				if len(list) > 0 {
					return list[0]
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("pending pairing did not appear")
		return nil
	}
	pendingA := waitPending(hA)
	pendingB := waitPending(hB)
	decodeDecision := func(h *Hub, p *PendingPairing) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"id": p.ID, "accept": false})
		req, _ := http.NewRequest(http.MethodPost, "http://"+h.WebAddr()+"/api/pair/decision", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(IPCTokenHeader, h.ipcToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("pair decision status = %d", resp.StatusCode)
		}
	}
	decodeDecision(hA, pendingA)
	decodeDecision(hB, pendingB)

	select {
	case err := <-initiateErr:
		t.Fatalf("pair initiation request failed: %v", err)
	case resp := <-initiate:
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("rejected pairing status = %d, want 409", resp.StatusCode)
		}
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result["status"] != "rejected" || result["accepted"] != false {
			t.Fatalf("unexpected rejected response: %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pair initiation did not finish")
	}
}

func TestWebDashboard_InboundPairing_PendingApprovalAndRejection(t *testing.T) {
	tempDirA := t.TempDir()
	tempDirB := t.TempDir()

	hA, err := NewHub(HubConfig{
		TransportType:     "lan",
		ListenAddr:        "127.0.0.1:0",
		WebAddr:           "127.0.0.1:0",
		StoreDir:          tempDirA,
		Headless:          true,
		AutoAcceptPairing: false, // Strict zero-trust mode requiring approval
	})
	if err != nil {
		t.Fatal(err)
	}

	hB, err := NewHub(HubConfig{
		TransportType:     "lan",
		ListenAddr:        "127.0.0.1:0",
		WebAddr:           "127.0.0.1:0",
		StoreDir:          tempDirB,
		Headless:          true,
		AutoAcceptPairing: false, // Require approval on both sides.
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = hA.Start(ctx) }()
	go func() { _ = hB.Start(ctx) }()

	select {
	case <-hA.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Hub A")
	}
	select {
	case <-hB.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Hub B")
	}

	defer func() {
		_ = hA.Stop()
		_ = hB.Stop()
	}()

	// 1. Initially, no pending pairings
	pendingResp, err := testGet(hA.ipcToken, "http://"+hA.WebAddr()+"/api/pair/pending")
	if err != nil {
		t.Fatal(err)
	}
	var initialPending []*PendingPairing
	_ = json.NewDecoder(pendingResp.Body).Decode(&initialPending)
	pendingResp.Body.Close()
	if len(initialPending) != 0 {
		t.Fatalf("expected 0 initial pending pairings, got %d", len(initialPending))
	}

	// 2. Hub B initiates pairing in background
	type pairResult struct {
		resp *http.Response
		err  error
	}
	pairResultCh := make(chan pairResult, 1)
	go func() {
		pairReqBody, _ := json.Marshal(map[string]string{
			"peer_addr": hA.P2PAddr().String(),
		})
		req, _ := http.NewRequest(http.MethodPost, "http://"+hB.WebAddr()+"/api/pair/initiate", bytes.NewReader(pairReqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://"+hB.WebAddr())
		req.Header.Set(IPCTokenHeader, hB.ipcToken)
		client := &http.Client{Timeout: 10 * time.Second}
		res, rErr := client.Do(req)
		pairResultCh <- pairResult{resp: res, err: rErr}
	}()

	// 3. Poll Hub A's /api/pair/pending until request appears
	deadline := time.Now().Add(5 * time.Second)
	var activePending []*PendingPairing
	for time.Now().Before(deadline) {
		pResp, pErr := testGet(hA.ipcToken, "http://"+hA.WebAddr()+"/api/pair/pending")
		if pErr == nil {
			var list []*PendingPairing
			_ = json.NewDecoder(pResp.Body).Decode(&list)
			pResp.Body.Close()
			if len(list) > 0 {
				activePending = list
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(activePending) == 0 {
		t.Fatal("expected pending pairing to appear on Hub A, but none found")
	}

	pendingReq := activePending[0]
	if pendingReq.ID == "" || pendingReq.PeerSAS == "" {
		t.Fatalf("invalid pending request fields: %+v", pendingReq)
	}

	// 4. Hub A user approves the pairing via POST /api/pair/decision
	decisionBody, _ := json.Marshal(map[string]any{
		"id":     pendingReq.ID,
		"accept": true,
	})
	decReq, _ := http.NewRequest(http.MethodPost, "http://"+hA.WebAddr()+"/api/pair/decision", bytes.NewReader(decisionBody))
	decReq.Header.Set("Content-Type", "application/json")
	decReq.Header.Set("Origin", "http://"+hA.WebAddr())
	decReq.Header.Set(IPCTokenHeader, hA.ipcToken)
	decResp, dErr := http.DefaultClient.Do(decReq)
	if dErr != nil {
		t.Fatalf("POST /api/pair/decision failed: %v", dErr)
	}
	if decResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for decision, got %d", decResp.StatusCode)
	}
	decResp.Body.Close()

	// 5. Hub B must also explicitly approve the outbound side of the
	// handshake. This prevents a web request from silently auto-accepting a
	// peer that the initiating user has not reviewed.
	outboundDeadline := time.Now().Add(5 * time.Second)
	var outboundPending []*PendingPairing
	for time.Now().Before(outboundDeadline) {
		pResp, pErr := testGet(hB.ipcToken, "http://"+hB.WebAddr()+"/api/pair/pending")
		if pErr == nil {
			var list []*PendingPairing
			_ = json.NewDecoder(pResp.Body).Decode(&list)
			pResp.Body.Close()
			if len(list) > 0 {
				outboundPending = list
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(outboundPending) == 0 {
		t.Fatal("expected outbound pairing approval request on Hub B")
	}
	outboundDecision, _ := json.Marshal(map[string]any{
		"id":     outboundPending[0].ID,
		"accept": true,
	})
	outReq, _ := http.NewRequest(http.MethodPost, "http://"+hB.WebAddr()+"/api/pair/decision", bytes.NewReader(outboundDecision))
	outReq.Header.Set("Content-Type", "application/json")
	outReq.Header.Set("Origin", "http://"+hB.WebAddr())
	outReq.Header.Set(IPCTokenHeader, hB.ipcToken)
	outResp, outErr := http.DefaultClient.Do(outReq)
	if outErr != nil {
		t.Fatalf("POST outbound pairing decision failed: %v", outErr)
	}
	outResp.Body.Close()
	if outResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for outbound decision, got %d", outResp.StatusCode)
	}

	// 6. Verify Hub B's initiate call completes successfully
	select {
	case pr := <-pairResultCh:
		if pr.err != nil {
			t.Fatalf("Hub B initiate call returned error: %v", pr.err)
		}
		defer pr.resp.Body.Close()
		if pr.resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(pr.resp.Body)
			t.Fatalf("expected 200 OK from initiate, got %d: %s", pr.resp.StatusCode, string(b))
		}
		var finalRes struct {
			Accepted bool   `json:"accepted"`
			Status   string `json:"status"`
		}
		_ = json.NewDecoder(pr.resp.Body).Decode(&finalRes)
		if !finalRes.Accepted || finalRes.Status != "paired" {
			t.Fatalf("expected accepted and paired, got: %+v", finalRes)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Hub B initiate call to complete")
	}

	// 6. Verify peers in store
	if len(hA.Store().ListPeers()) != 1 {
		t.Errorf("expected 1 peer in hA store, got %d", len(hA.Store().ListPeers()))
	}
	if len(hB.Store().ListPeers()) != 1 {
		t.Errorf("expected 1 peer in hB store, got %d", len(hB.Store().ListPeers()))
	}
}

func TestWebDashboard_UploadConcurrencyCapped(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// Exhaust every upload slot directly, then a multipart upload must fail
	// fast with 503 before buffering any body.
	for i := 0; i < maxHubConcurrentUploads; i++ {
		if !h.acquireUploadSlot() {
			t.Fatalf("slot %d unexpectedly unavailable", i)
		}
	}
	defer func() {
		for i := 0; i < maxHubConcurrentUploads; i++ {
			h.releaseUploadSlot()
		}
	}()

	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, err := w.CreateFormFile("file", "test.txt")
	if err != nil {
		t.Fatalf("CreateFormFile failed: %v", err)
	}
	_, _ = part.Write([]byte("capped"))
	_ = w.Close()

	req, err := http.NewRequest(http.MethodPost, "http://"+h.WebAddr()+"/api/drop/upload", &b)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with exhausted upload slots, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 503")
	}
}

func TestWebDashboard_UploadProgressAndPeerRenderMarkers(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	resp, err := http.Get("http://" + h.WebAddr() + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	content := string(body)

	// Real upload progress (XHR) + cancel + accessible progress/status.
	for _, s := range []string{
		"xhr.upload.onprogress",
		"cancelUpload",
		"btnCancelUpload",
		`role="progressbar"`,
		`aria-live="polite"`,
		"lastPeerSignature",
		"headerPeerSelect",
		"clipboardData",
		"addEventListener('paste'",
		"TextEncoder",
		"maxTextBytes",
		"received-item-preview",
		"mode=inline",
		"mode=download",
		"sessionBanner",
		"pageSessionReady",
		"scheduleRecentDropsReload",
		"event delegation",
		`rel="icon"`,
		"focus-visible",
		"sr-only",
		`aria-label="Active peer"`,
		`role="button"`,
		`aria-label="OAuth authorization URL"`,
		`aria-label="Search live logs"`,
		`for="pairRemoteAddrInput"`,
		"stageFileForSend",
		"confirmPendingSend",
		"cancelPendingSend",
		"filePreviewCard",
		"btnConfirmSend",
		"btnCancelPreview",
		"expertImmediateSend",
		"destinationSummary",
		"nextActionBanner",
		"tab-btn-transfers",
		"tab-transfers",
		"loadTransfers",
		"transfersList",
		"/api/transfers/recent",
		"prefers-reduced-motion",
		"scroll-margin-top",
		" · Trusted",
		"Destination: ",
		"querySelectorAll('button, input')",
		"clear-transfers",
		"clearTransfers",
		"clear-authorizations",
		"clearAuthorizations",
		"api/relay/recent",
		"authorizationsList",
		"loadAuthorizations",
		"renderAuthorizations",
		"stay where they were",
		"See Recent Authorizations below",
		"via ' + data.destination",
		"never expires",
	} {
		if !strings.Contains(content, s) {
			t.Errorf("dashboard HTML missing %q (upload-progress / focus-preservation UX)", s)
		}
	}
	// Clipboard and file inputs must stage for confirmation, never auto-send.
	// The old trusted-but-vague peer label and generic success text are gone.
	// Bookmarks must never carry single-use tickets again.
	for _, s := range []string{
		"uploadFile(image)",
		"uploadFile(e.dataTransfer.files[0])",
		"uploadFile(fileInput.files[0])",
		"[Paired · ready]",
		"File transferred successfully to peer!",
		"ticket=",
	} {
		if strings.Contains(content, s) {
			t.Errorf("dashboard HTML still contains unsafe/auto-send pattern %q", s)
		}
	}
	// The old faked progress must be gone: no hard-coded staged percentages.
	for _, s := range []string{
		"fill.style.width = '20%'",
		"fill.style.width = '60%'",
		"'Streaming...'",
	} {
		if strings.Contains(content, s) {
			t.Errorf("dashboard HTML still contains faked upload progress %q", s)
		}
	}
	// Multi-GB units must render beyond MB.
	if !strings.Contains(content, "'GB'") || !strings.Contains(content, "'TB'") {
		t.Error("formatBytes lacks GB/TB units")
	}
}

func TestWebDashboard_DropFileServe(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	outDir := h.OutputDir()
	if err := os.MkdirAll(outDir, 0700); err != nil {
		t.Fatal(err)
	}
	// 1x1 PNG (valid header + minimal bytes; content is not decoded server-side).
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	pngPath := filepath.Join(outDir, "photo.png")
	if err := os.WriteFile(pngPath, pngBytes, 0600); err != nil {
		t.Fatal(err)
	}
	h.recentDrops.Add(ReceivedDropItem{ID: "file-png-1", Kind: "file", Name: "photo.png", Size: int64(len(pngBytes)), SavedPath: pngPath, FromPeer: "peer"})

	svgPath := filepath.Join(outDir, "evil.svg")
	if err := os.WriteFile(svgPath, []byte(`<svg onload="alert(1)">`), 0600); err != nil {
		t.Fatal(err)
	}
	h.recentDrops.Add(ReceivedDropItem{ID: "file-svg-1", Kind: "file", Name: "evil.svg", Size: 22, SavedPath: svgPath, FromPeer: "peer"})

	h.recentDrops.Add(ReceivedDropItem{ID: "file-outside-1", Kind: "file", Name: "secret.txt", Size: 6, SavedPath: filepath.Join(t.TempDir(), "secret.txt"), FromPeer: "peer"})
	h.recentDrops.Add(ReceivedDropItem{ID: "text-1", Kind: "text", Name: "note", Size: 4, Content: "hey", FromPeer: "peer"})

	get := func(query string, token string) *http.Response {
		req, err := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/api/drop/file"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set(IPCTokenHeader, token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp
	}

	// Inline raster preview.
	resp := get("?id=file-png-1&mode=inline", h.ipcToken)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("inline png = %d %q, want 200 image/png", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if disp := resp.Header.Get("Content-Disposition"); disp != "inline" {
		t.Errorf("inline disposition = %q, want inline", disp)
	}
	// Default is a forced download.
	resp = get("?id=file-png-1", h.ipcToken)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("download png = %d %q, want 200 octet-stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if disp := resp.Header.Get("Content-Disposition"); len(disp) < 10 || disp[:10] != "attachment" {
		t.Errorf("download disposition = %q, want attachment", disp)
	}
	// SVG is never inline, even when requested.
	resp = get("?id=file-svg-1&mode=inline", h.ipcToken)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("svg inline attempt = %d %q, want 200 octet-stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	// Outside-dir, text-kind, unknown, and missing IDs are 404.
	for _, q := range []string{"?id=file-outside-1", "?id=text-1", "?id=nope", ""} {
		if resp := get(q, h.ipcToken); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %q = %d, want 404", q, resp.StatusCode)
		}
	}
	// Unauthenticated is rejected by middleware.
	if resp := get("?id=file-png-1", ""); resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Errorf("unauthenticated file serve = %d, want 401/403", resp.StatusCode)
	}
}

func TestWebDashboard_DropFileServeSymlinkRejected(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	outDir := h.OutputDir()
	if err := os.MkdirAll(outDir, 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim.dat")
	if err := os.WriteFile(victim, []byte("victim"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outDir, "linked.png")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	h.recentDrops.Add(ReceivedDropItem{ID: "file-link-1", Kind: "file", Name: "linked.png", Size: 6, SavedPath: link, FromPeer: "peer"})

	req, _ := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/api/drop/file?id=file-link-1&mode=inline", nil)
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("symlinked file serve = %d, want 404", resp.StatusCode)
	}
	if got, _ := os.ReadFile(victim); string(got) != "victim" {
		t.Fatalf("victim modified: %q", got)
	}
}

func TestWebDashboard_CLIVersionSkewLogged(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/api/probe", nil)
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	req.Header.Set("X-CLI-Version", "0.0.0-skew-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe = %d, want 200", resp.StatusCode)
	}

	logsResp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer logsResp.Body.Close()
	body, _ := io.ReadAll(logsResp.Body)
	if !strings.Contains(string(body), "version skew") || !strings.Contains(string(body), "0.0.0-skew-test") {
		t.Fatalf("skew warning missing from logs: %s", body)
	}

	// Repeat with the same caller version: deduped, no second entry.
	req2, _ := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/api/probe", nil)
	req2.Header.Set(IPCTokenHeader, h.ipcToken)
	req2.Header.Set("X-CLI-Version", "0.0.0-skew-test")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	logsResp2, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer logsResp2.Body.Close()
	body2, _ := io.ReadAll(logsResp2.Body)
	if n := strings.Count(string(body2), "0.0.0-skew-test"); n != 1 {
		t.Fatalf("skew warning logged %d times, want deduped 1", n)
	}
}

func TestCanonicalVersion(t *testing.T) {
	if CanonicalVersion("1.0.0-dev+abc.dirty") != CanonicalVersion("1.0.0-dev+abc") {
		t.Fatal("dirty suffix should not count as skew")
	}
	if CanonicalVersion("1.0.0-dev+abc") == CanonicalVersion("1.0.0") {
		t.Fatal("dev vs release must count as skew")
	}
	if CanonicalVersion("  1.0.0  ") != "1.0.0" {
		t.Fatal("whitespace not trimmed")
	}
}

func TestWebDashboard_RestartDuringUploadsReleasesSlots(t *testing.T) {
	h, cancel, cleanup := startTestHub(t)
	defer cleanup()

	// Slow multipart bodies hold all 4 upload slots in flight.
	prs := make([]*io.PipeReader, 0, maxHubConcurrentUploads)
	pws := make([]*io.PipeWriter, 0, maxHubConcurrentUploads)
	for i := 0; i < maxHubConcurrentUploads; i++ {
		pr, pw := io.Pipe()
		prs = append(prs, pr)
		pws = append(pws, pw)
	}
	uploadErrs := make(chan error, maxHubConcurrentUploads)
	for _, pr := range prs {
		go func(body io.Reader) {
			req, err := http.NewRequest(http.MethodPost, "http://"+h.WebAddr()+"/api/drop/upload", body)
			if err != nil {
				uploadErrs <- err
				return
			}
			req.Header.Set("Content-Type", "multipart/form-data; boundary=slowboundary")
			req.Header.Set(IPCTokenHeader, h.ipcToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				uploadErrs <- err
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			uploadErrs <- nil
		}(pr)
	}
	// Feed headers + a trickle so handlers are inside body reads.
	for _, pw := range pws {
		_, _ = pw.Write([]byte("--slowboundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"slow.bin\"\r\n\r\n"))
	}
	time.Sleep(300 * time.Millisecond)

	// A 5th upload must fail fast, not queue behind the stalled ones.
	quick, _ := http.NewRequest(http.MethodPost, "http://"+h.WebAddr()+"/api/drop/upload", strings.NewReader("--slowboundary--\r\n"))
	quick.Header.Set("Content-Type", "multipart/form-data; boundary=slowboundary")
	quick.Header.Set(IPCTokenHeader, h.ipcToken)
	quickResp, err := http.DefaultClient.Do(quick)
	if err != nil {
		t.Fatalf("capped upload request failed: %v", err)
	}
	quickResp.Body.Close()
	if quickResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("5th concurrent upload = %d, want 503", quickResp.StatusCode)
	}

	// Restart mid-upload: Stop must return, in-flight uploads must error
	// out (not hang), and the next generation must have free slots.
	for _, pw := range pws {
		_ = pw.Close()
	}
	if err := h.Stop(); err != nil {
		t.Fatalf("Stop during uploads: %v", err)
	}
	cancel()
	deadline := time.After(15 * time.Second)
	for i := 0; i < maxHubConcurrentUploads; i++ {
		select {
		case <-uploadErrs:
		case <-deadline:
			t.Fatal("in-flight upload did not terminate after Stop")
		}
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	errCh := make(chan error, 1)
	go func() { errCh <- h.Start(ctx2) }()
	select {
	case <-h.Ready():
	case err := <-errCh:
		t.Fatalf("restart failed: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("restarted hub not ready")
	}
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, _ := w.CreateFormFile("file", "after.txt")
	_, _ = part.Write([]byte("after restart"))
	_ = w.Close()
	req, _ := http.NewRequest(http.MethodPost, "http://"+h.WebAddr()+"/api/drop/upload", &b)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post-restart upload failed: %v", err)
	}
	defer resp.Body.Close()
	// Loopback self-delivery succeeds: 200 proves a slot was acquired (a
	// leaked slot would still be 503) and the full pipeline works post-restart.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-restart upload = %d, want 200 (slot acquired, self-delivery)", resp.StatusCode)
	}
}

func TestWebDashboard_DropFileServeSymlinkedDirRejected(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	outDir := h.OutputDir()
	if err := os.MkdirAll(outDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "x.png")
	if err := os.WriteFile(victim, []byte{0x89, 0x50, 0x4e, 0x47}, 0600); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(outDir, "linkdir")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	// Lexically inside the output dir, but resolves outside via linkdir.
	h.recentDrops.Add(ReceivedDropItem{ID: "file-linkdir-1", Kind: "file", Name: "x.png", Size: 4,
		SavedPath: filepath.Join(linkDir, "x.png"), FromPeer: "peer"})

	req, _ := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/api/drop/file?id=file-linkdir-1&mode=inline", nil)
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("symlinked-dir file serve = %d, want 404", resp.StatusCode)
	}
}
