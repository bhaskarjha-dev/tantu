package hub

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDashboardSessionTTLContract(t *testing.T) {
	if dashboardSessionTTL != 24*time.Hour {
		t.Fatalf("dashboardSessionTTL = %v, want 24h", dashboardSessionTTL)
	}
}

func TestTouchDashboardSession(t *testing.T) {
	h, err := NewHub(HubConfig{Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	if h.touchDashboardSession("", time.Now()) {
		t.Error("empty session ID must never validate")
	}
	if h.touchDashboardSession("no-such-session", time.Now()) {
		t.Error("unknown session ID must never validate")
	}

	id, err := h.newDashboardSession()
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	if !h.touchDashboardSession(id, base) {
		t.Fatal("fresh session must validate")
	}
	if got := h.dashboardSessions[id]; !got.Equal(base.Add(dashboardSessionTTL)) {
		t.Fatalf("fresh expiry = %v, want %v", got, base.Add(dashboardSessionTTL))
	}

	// Activity near the end of the window renews for a full fresh TTL.
	later := base.Add(23 * time.Hour)
	if !h.touchDashboardSession(id, later) {
		t.Fatal("session used at 23h must still validate")
	}
	if got := h.dashboardSessions[id]; !got.Equal(later.Add(dashboardSessionTTL)) {
		t.Fatalf("renewed expiry = %v, want %v", got, later.Add(dashboardSessionTTL))
	}

	// Past the renewed window the session is dead and pruned.
	if h.touchDashboardSession(id, later.Add(dashboardSessionTTL+time.Second)) {
		t.Fatal("expired session must not validate")
	}
	if _, ok := h.dashboardSessions[id]; ok {
		t.Fatal("expired session must be pruned from the map")
	}
}

// exchangeDashboardSession trades a hub's one-time bootstrap value for a
// session, returning the Set-Cookie cookies exactly as the server sent them.
func exchangeDashboardSession(t *testing.T, h *Hub) []*http.Cookie {
	t.Helper()
	dashboardURL, err := url.Parse(h.DashboardURL())
	if err != nil {
		t.Fatalf("parse dashboard URL: %v", err)
	}
	bootstrap := strings.TrimPrefix(dashboardURL.Fragment, "tantu_bootstrap=")
	if bootstrap == "" {
		t.Fatal("DashboardURL did not contain a bootstrap fragment")
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+h.WebAddr()+"/api/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Tantu-Dashboard-Bootstrap", bootstrap)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap exchange status = %d, want 200", resp.StatusCode)
	}
	return resp.Cookies()
}

// applyToBrowserJar applies Set-Cookie values the way a browser does: the jar
// is keyed by cookie name for a fixed domain and path, so a later set with the
// same name REPLACES the earlier value. Browsers never key by port, which is
// the whole hazard for two Hubs sharing a loopback host.
func applyToBrowserJar(jar map[string]string, cookies []*http.Cookie) {
	for _, c := range cookies {
		jar[c.Name] = c.Value
	}
}

// jarCookieHeader serializes the jar the way the browser would send it: every
// stored cookie for the target host, regardless of which Hub set it.
func jarCookieHeader(jar map[string]string) string {
	parts := make([]string, 0, len(jar))
	for name, value := range jar {
		parts = append(parts, name+"="+value)
	}
	return strings.Join(parts, "; ")
}

// Two Hubs on one loopback host share the browser's cookie jar, because a
// cookie is keyed by name/domain/path and never by port. Before the fix every
// session cookie was named identically, so the second Hub's login REPLACED the
// first Hub's value: the first dashboard then failed every poll with 401 and
// showed "Dashboard session not established" while the Hub was healthy - a
// permanent dead state found in the Z30 live pairing run. The gate models the
// shared jar literally and requires both Hubs to keep working after both
// logins.
func TestDashboardSession_SecondHubExchangeDoesNotClobberFirstHub(t *testing.T) {
	hA, _, cleanupA := startTestHub(t)
	defer cleanupA()
	hB, _, cleanupB := startTestHub(t)
	defer cleanupB()
	if hA.WebAddr() == hB.WebAddr() {
		t.Fatal("test requires two Hubs on distinct ports")
	}

	jar := map[string]string{}
	applyToBrowserJar(jar, exchangeDashboardSession(t, hA))
	applyToBrowserJar(jar, exchangeDashboardSession(t, hB))

	if len(jar) != 2 {
		names := make([]string, 0, len(jar))
		for name := range jar {
			names = append(names, name)
		}
		t.Fatalf("browser jar holds %d cookie(s) after two Hubs exchanged sessions, want 2 (one per Hub): %v - "+
			"the second login replaced the first session", len(jar), names)
	}

	cookieHeader := jarCookieHeader(jar)
	for _, tc := range []struct {
		label string
		hub   *Hub
	}{{"first hub", hA}, {"second hub", hB}} {
		req, err := http.NewRequest(http.MethodGet, "http://"+tc.hub.WebAddr()+"/api/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Cookie", cookieHeader)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: status request: %v", tc.label, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s (port %s) rejected the shared browser jar with %d - a session cookie is not scoped to its Hub's port",
				tc.label, tc.hub.WebAddr(), resp.StatusCode)
		}
	}
}
