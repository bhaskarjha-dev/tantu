package hub

import (
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
