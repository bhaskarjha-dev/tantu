package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestWebDashboard_RelayClearContract(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// Seed one failed attempt (invalid scheme: no dial, no browser).
	body, _ := json.Marshal(map[string]string{"url": "ftp://clear.example/x"})
	seed, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/relay/open", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	seed.Body.Close()
	if seed.StatusCode != http.StatusBadGateway {
		t.Fatalf("seed status = %d, want 502", seed.StatusCode)
	}

	// Unauthenticated DELETE must be rejected.
	unauth, err := http.NewRequest(http.MethodDelete, "http://"+h.WebAddr()+"/api/relay/recent", nil)
	if err != nil {
		t.Fatal(err)
	}
	unauthResp, err := http.DefaultClient.Do(unauth)
	if err != nil {
		t.Fatal(err)
	}
	unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated DELETE = %d, want 401", unauthResp.StatusCode)
	}

	// Authenticated DELETE clears memory and disk.
	req, err := http.NewRequest(http.MethodDelete, "http://"+h.WebAddr()+"/api/relay/recent", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "success" {
		t.Errorf("clear status = %v", payload["status"])
	}
	if n, _ := payload["removed"].(float64); n != 1 {
		t.Errorf("removed = %v, want 1", payload["removed"])
	}

	opsResp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/relay/recent")
	if err != nil {
		t.Fatal(err)
	}
	defer opsResp.Body.Close()
	var ops []RelayAttempt
	if err := json.NewDecoder(opsResp.Body).Decode(&ops); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("ledger after clear = %d, want 0", len(ops))
	}
}
