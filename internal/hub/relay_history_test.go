package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelayLedger_Bounded(t *testing.T) {
	ledger := NewRelayLedger(3)
	for i := 0; i < 5; i++ {
		ledger.Add(RelayAttempt{ID: string(rune('a' + i)), State: RelayStateFailed, CreatedAt: time.Now()})
	}
	got := ledger.GetAll()
	if len(got) != 3 || got[0].ID != "c" || got[2].ID != "e" {
		t.Fatalf("ledger eviction wrong: %+v", got)
	}
}

func TestSafeRelayOrigin_RedactsEverythingButHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://accounts.google.com/o/oauth2/v2/auth?client_id=x&state=SECRET&code=ABC", "accounts.google.com"},
		{"http://127.0.0.1:9876/authorize?code=SECRET", "127.0.0.1"},
		{"ftp://example.com/auth?code=SECRET999", "example.com"},
		{"notaurl", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := safeRelayOrigin(tc.in); got != tc.want {
			t.Errorf("safeRelayOrigin(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRelayHistory_PersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	ops := []RelayAttempt{{
		ID: "rl-1", TargetPeer: "Devbox", SafeOrigin: "accounts.google.com",
		State: RelayStateComplete, CreatedAt: now, UpdatedAt: now, DiagnosticID: "rl-1",
	}}
	if err := persistRelayHistory(dir, ops); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, RelayHistoryFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "state=SECRET") || strings.Contains(string(raw), "code=") {
		t.Error("persisted history must not contain URL query material")
	}
	loaded := loadRelayHistory(dir)
	if len(loaded) != 1 || loaded[0].ID != "rl-1" || loaded[0].SafeOrigin != "accounts.google.com" {
		t.Fatalf("reload wrong: %+v", loaded)
	}
}

func TestWebDashboard_RelayOpenResponseContract(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]string{"url": "ftp://contract.example/x?code=SECRET"})
	resp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/relay/open", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	opID, _ := payload["operation_id"].(string)
	if !strings.HasPrefix(opID, "rl-") {
		t.Errorf("operation_id = %q, want rl- prefix", opID)
	}
	if payload["destination"] == "" || payload["destination"] == nil {
		t.Error("failure must name its destination")
	}
	if payload["next_action"] == "" || payload["next_action"] == nil {
		t.Error("failure must carry a next action")
	}
	// The response must point at the recorded ledger entry.
	opsResp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/relay/recent")
	if err != nil {
		t.Fatal(err)
	}
	defer opsResp.Body.Close()
	var ops []RelayAttempt
	if err := json.NewDecoder(opsResp.Body).Decode(&ops); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].ID != opID {
		t.Fatalf("ledger must contain the responded attempt %q: %+v", opID, ops)
	}
}

func TestWebDashboard_RelayOpenInvalidRecordsAttempt(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// Invalid scheme: rejected before any dial or browser action.
	secretURL := "ftp://example.com/auth?code=SECRET999"
	body, _ := json.Marshal(map[string]string{"url": secretURL})
	resp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/relay/open", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
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
	if len(ops) != 1 {
		t.Fatalf("ledger length = %d, want 1", len(ops))
	}
	op := ops[0]
	if op.State != RelayStateFailed || op.ErrorCode != "invalid_input" {
		t.Errorf("attempt wrong: %+v", op)
	}
	if op.SafeOrigin != "example.com" {
		t.Errorf("origin = %q, want host only", op.SafeOrigin)
	}
	raw, _ := json.Marshal(ops)
	if strings.Contains(string(raw), "SECRET999") || strings.Contains(string(raw), "code=") {
		t.Error("authorization record leaked URL secret material")
	}
	if strings.Contains(string(raw), secretURL) {
		t.Error("authorization record stored the raw URL")
	}
}
