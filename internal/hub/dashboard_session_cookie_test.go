package hub

// The dashboard session cookie's lifetime.
//
// `dashboardSessionTTL` is documented as "24 hours sliding from last use", and
// the SERVER side slides: `touchDashboardSession` renews the expiry on every
// use. The COOKIE did not. `http.Cookie.MaxAge` is absolute from the moment
// the cookie is set, and the cookie was set once -- at the bootstrap exchange
// -- and never re-set. So the browser stopped presenting the cookie
// twenty-four hours after the BOOTSTRAP, whatever the server thought, and
// the stated intent ("the previous 30-minute absolute expiry logged out even
// actively used dashboards") was only half delivered: the expiry was
// absolute, and forty-eight times longer.
//
// The existing gates could not see this. `TestTouchDashboardSession` exercises
// the server-side map and never touches an `http.Cookie`; the bootstrap test
// checked the cookie's name and HttpOnly and not its lifetime.
//
// See docs/KNOWN-LIMITATIONS.md row 3.31.

import (
	"net/http"
	"testing"
)

// TestDashboardSessionCookieSlides asserts the whole claim: the cookie is
// re-issued on every cookie-authenticated request, with a fresh Max-Age, so
// the browser's expiry slides with the server's.
func TestDashboardSessionCookieSlides(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	jar := map[string]string{}
	cookies := exchangeDashboardSession(t, h)
	if len(cookies) != 1 {
		t.Fatalf("bootstrap set %d cookies, want 1", len(cookies))
	}
	bootstrapCookie := cookies[0]
	if got := bootstrapCookie.MaxAge; got != int(dashboardSessionTTL.Seconds()) {
		t.Errorf("bootstrap cookie Max-Age = %d, want %d", got, int(dashboardSessionTTL.Seconds()))
	}
	applyToBrowserJar(jar, cookies)

	// An authenticated request must re-issue the cookie. Without it the
	// browser's expiry is absolute from the bootstrap and the "sliding"
	// claim is false of the side that decides whether the request is made at
	// all.
	req, err := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/api/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", jarCookieHeader(jar))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status request = %d, want 200", resp.StatusCode)
	}
	reissued := resp.Cookies()
	if len(reissued) != 1 {
		t.Fatalf("an authenticated request re-issued %d cookies, want 1: the cookie's Max-Age is "+
			"absolute, so it has to be re-set for the expiry to slide", len(reissued))
	}
	if reissued[0].Name != bootstrapCookie.Name {
		t.Errorf("re-issued cookie name = %q, want %q", reissued[0].Name, bootstrapCookie.Name)
	}
	if reissued[0].Value != bootstrapCookie.Value {
		t.Errorf("re-issued cookie value = %q, want the same session %q", reissued[0].Value, bootstrapCookie.Value)
	}
	if got := reissued[0].MaxAge; got != int(dashboardSessionTTL.Seconds()) {
		t.Errorf("re-issued cookie Max-Age = %d, want %d", got, int(dashboardSessionTTL.Seconds()))
	}
	if !reissued[0].HttpOnly {
		t.Error("the re-issued cookie lost HttpOnly")
	}
}
