package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

func TestBuildHealthReport_FreshStore(t *testing.T) {
	dir := t.TempDir()
	report := buildHealthReport(dir, "")
	if report.Status != "degraded" {
		t.Errorf("fresh store status = %q, want degraded", report.Status)
	}
	if !strings.Contains(report.NextAction, "tantu pair") {
		t.Errorf("next action = %q, want pair guidance", report.NextAction)
	}
	if len(report.Peers) != 0 {
		t.Errorf("peers = %d, want 0", len(report.Peers))
	}
	raw, _ := json.Marshal(report)
	lower := strings.ToLower(string(raw))
	for _, secret := range []string{"private", "secret", "token", "clipboard", "password"} {
		if strings.Contains(lower, secret) {
			t.Errorf("health JSON leaked %q: %s", secret, raw)
		}
	}
}

func TestBuildHealthReport_LiveHub(t *testing.T) {
	dir := t.TempDir()
	h, err := hub.NewHub(hub.HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      dir,
		OutputDir:     filepath.Join(dir, "drops"),
		Headless:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer h.Stop()
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("hub not ready")
	}
	if err := h.Err(); err != nil {
		t.Fatalf("hub start error: %v", err)
	}

	report := buildHealthReport(dir, "")
	if !report.HubRunning {
		t.Error("expected HubRunning=true against live hub")
	}
	if report.Identity == "" {
		t.Error("expected identity after hub start")
	}
	if report.OutputDir == "" {
		t.Error("expected output dir from live hub")
	}
}

func TestFetchTransferRecords_AfterUpload(t *testing.T) {
	dir := t.TempDir()
	h, err := hub.NewHub(hub.HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      dir,
		OutputDir:     filepath.Join(dir, "drops"),
		Headless:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer h.Stop()
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("hub not ready")
	}
	if err := h.Err(); err != nil {
		t.Fatalf("hub start error: %v", err)
	}
	webAddr := h.WebAddr()

	// Empty ledger first.
	empty, err := fetchTransferRecords(webAddr, dir)
	if err != nil {
		t.Fatalf("fetch empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty ledger = %d, want 0", len(empty))
	}

	// One loopback self-delivery via the Hub upload API.
	token := hub.RuntimeIPCTokenForAddr(dir, webAddr)
	if token == "" {
		t.Fatal("no IPC token for live hub")
	}
	body, _ := json.Marshal(map[string]string{"text": "doctor-test"})
	req, _ := http.NewRequest(http.MethodPost, "http://"+webAddr+"/api/drop/upload", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(hub.IPCTokenHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", resp.StatusCode)
	}

	records, err := fetchTransferRecords(webAddr, dir)
	if err != nil {
		t.Fatalf("fetch after upload: %v", err)
	}
	if len(records) != 1 || records[0].State != "completed" {
		t.Fatalf("records after upload wrong: %+v", records)
	}
}

func TestReadSavedTransferRecords_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	saved := `{"version":1,"operations":[{"operation_id":"op-saved-1","kind":"file","name":"a.bin","size":7,"destination":"Devbox","state":"completed","created_at":"2026-09-25T00:00:00Z","updated_at":"2026-09-25T00:00:01Z","retry_safe":false,"duplicate_risk":false,"data_safe":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "transfers.json"), []byte(saved), 0600); err != nil {
		t.Fatal(err)
	}
	records, err := readSavedTransferRecords(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].OperationID != "op-saved-1" || records[0].Destination != "Devbox" {
		t.Fatalf("saved records wrong: %+v", records)
	}
}

func TestWriteSupportBundle_OfflineRedacted(t *testing.T) {
	dir := t.TempDir()
	report := buildHealthReport(dir, "")
	out := filepath.Join(dir, "bundle.json")
	if err := writeSupportBundle(out, report, dir, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var bundle map[string]any
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"report", "transfers", "logs", "redaction_note"} {
		if _, ok := bundle[key]; !ok {
			t.Errorf("bundle missing %q", key)
		}
	}
	lower := strings.ToLower(string(raw))
	for _, secret := range []string{"cert_pem", "key_pem", "private_key", "ipc_token", "tantu_bootstrap", "password", "client_secret", "code_verifier", "refresh_token"} {
		if strings.Contains(lower, secret) {
			t.Errorf("bundle leaked %q", secret)
		}
	}
}

func TestWriteSupportBundle_LiveHub(t *testing.T) {
	dir := t.TempDir()
	h, err := hub.NewHub(hub.HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      dir,
		OutputDir:     filepath.Join(dir, "drops"),
		Headless:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer h.Stop()
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("hub not ready")
	}
	if err := h.Err(); err != nil {
		t.Fatalf("hub start error: %v", err)
	}
	webAddr := h.WebAddr()
	token := hub.RuntimeIPCTokenForAddr(dir, webAddr)
	body, _ := json.Marshal(map[string]string{"text": "bundle-probe"})
	req, _ := http.NewRequest(http.MethodPost, "http://"+webAddr+"/api/drop/upload", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(hub.IPCTokenHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	report := buildHealthReport(dir, "")
	out := filepath.Join(dir, "live-bundle.json")
	if err := writeSupportBundle(out, report, dir, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	var bundle struct {
		Transfers       []transferRecord `json:"transfers"`
		TransfersSource string           `json:"transfers_source"`
		Logs            []hub.LogEvent   `json:"logs"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.TransfersSource != "hub-live" || len(bundle.Transfers) != 1 {
		t.Fatalf("live bundle transfers wrong: %+v", bundle)
	}
	if len(bundle.Logs) == 0 {
		t.Error("live bundle should include Hub logs")
	}
	if strings.Contains(string(raw), "bundle-probe") {
		t.Error("bundle leaked payload text")
	}
}

func TestClearSavedTransferFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "transfers.json"), []byte(`{"version":1,"operations":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	removed, err := clearSavedTransferFile(dir)
	if err != nil || !removed {
		t.Fatalf("clear = %v, %v; want true, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "transfers.json")); !os.IsNotExist(err) {
		t.Fatal("history file must be gone after clear")
	}
	removed, err = clearSavedTransferFile(dir)
	if err != nil || removed {
		t.Fatalf("second clear = %v, %v; want false, nil", removed, err)
	}
}

func TestCockpitDestinationName(t *testing.T) {
	if got := cockpitDestinationName(nil, ""); got != "default peer" {
		t.Errorf("nil hub = %q", got)
	}
	if got := cockpitDestinationName(nil, "devbox"); got != "devbox" {
		t.Errorf("nil hub passthrough = %q", got)
	}
	dir := t.TempDir()
	h, err := hub.NewHub(hub.HubConfig{TransportType: "loopback", StoreDir: dir, OutputDir: filepath.Join(dir, "drops"), Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer h.Stop()
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("hub not ready")
	}
	if err := h.Store().AddPeer(pairing.Peer{Fingerprint: "fp-1111111111111111", Address: "192.168.1.10:9877", Name: "Laptop"}); err != nil {
		t.Fatal(err)
	}
	if got := cockpitDestinationName(h, ""); got != "Laptop" {
		t.Errorf("single peer = %q, want Laptop", got)
	}
	if got := cockpitDestinationName(h, "no-such-peer-xyz"); got != "no-such-peer-xyz" {
		t.Errorf("unresolvable must echo, got %q", got)
	}
}
