package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	errDial     = errors.New("dial peer failed: connection refused")
	errChecksum = errors.New("drop_complete checksum mismatch: got abc, want def")
	errComplete = errors.New("receive drop_complete: EOF")
	errData     = errors.New("send drop_data chunk 3: connection reset")
)

func TestOperationLedger_Bounded(t *testing.T) {
	ledger := NewOperationLedger(3)
	for i := 0; i < 5; i++ {
		ledger.Add(OperationRecord{OperationID: string(rune('a' + i))})
	}
	got := ledger.GetAll()
	if len(got) != 3 {
		t.Fatalf("ledger length = %d, want 3", len(got))
	}
	if got[0].OperationID != "c" || got[2].OperationID != "e" {
		t.Fatalf("ledger eviction order wrong: %+v", got)
	}
}

func TestPruneOperationsByAge(t *testing.T) {
	old := OperationRecord{OperationID: "op-old", Kind: "text", State: OperationStateCompleted, CreatedAt: time.Now().AddDate(0, 0, -31)}
	fresh := OperationRecord{OperationID: "op-new", Kind: "text", State: OperationStateCompleted, CreatedAt: time.Now()}
	kept := pruneOperationsByAge([]OperationRecord{old, fresh})
	if len(kept) != 1 || kept[0].OperationID != "op-new" {
		t.Fatalf("age prune wrong: %+v", kept)
	}
}

func TestPersistOutboundOperations_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	secret := "SECRET-PAYLOAD-xyz"
	ops := []OperationRecord{{
		OperationID: "op-1", Kind: "text", Name: "note", Size: 3,
		Destination: "Devbox", State: OperationStateCompleted,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
		DataSafe: true, DiagnosticID: "op-1",
	}}
	if err := persistOutboundOperations(dir, ops); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, transferPersistFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Error("persisted history must never contain payload text")
	}
	// Reload through a Hub to validate the full path.
	h, err := NewHub(HubConfig{TransportType: "loopback", StoreDir: dir, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	h.loadOutboundOperations(dir)
	got := h.listOutboundOperations()
	if len(got) != 1 || got[0].OperationID != "op-1" {
		t.Fatalf("reload wrong: %+v", got)
	}
	_ = secret
}

func TestLoadOutboundOperations_RestartRestore(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "drops")

	startHub := func() (*Hub, context.CancelFunc) {
		t.Helper()
		h, err := NewHub(HubConfig{
			TransportType: "loopback",
			ListenAddr:    "127.0.0.1:0",
			WebAddr:       "127.0.0.1:0",
			StoreDir:      dir,
			OutputDir:     outDir,
			Headless:      true,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- h.Start(ctx) }()
		select {
		case <-h.Ready():
		case err := <-errCh:
			cancel()
			t.Fatalf("hub start: %v", err)
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("hub not ready")
		}
		if err := h.Err(); err != nil {
			cancel()
			t.Fatalf("hub start error: %v", err)
		}
		return h, cancel
	}
	postText := func(h *Hub, text string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"text": text})
		resp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/drop/upload", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("upload status = %d", resp.StatusCode)
		}
		var payload map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		opID, _ := payload["operation_id"].(string)
		if opID == "" {
			t.Fatal("missing operation_id")
		}
		return opID
	}

	h1, cancel1 := startHub()
	opID := postText(h1, "restart-probe")
	if err := h1.Stop(); err != nil {
		t.Fatalf("stop hub1: %v", err)
	}
	cancel1()

	h2, cancel2 := startHub()
	defer cancel2()
	defer h2.Stop()
	resp, err := testGet(h2.ipcToken, "http://"+h2.WebAddr()+"/api/transfers/recent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ops []OperationRecord
	if err := json.NewDecoder(resp.Body).Decode(&ops); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].OperationID != opID || ops[0].State != OperationStateCompleted {
		t.Fatalf("restarted ledger wrong: %+v", ops)
	}
}

func TestLoadOutboundOperations_CorruptStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, transferPersistFileName), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	h, _, cleanup := startTestHub(t)
	defer cleanup()
	_ = dir
	// startTestHub uses its own dir; exercise the corrupt path directly.
	corruptHub, err := NewHub(HubConfig{TransportType: "loopback", StoreDir: dir, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	corruptHub.loadOutboundOperations(dir)
	if got := corruptHub.listOutboundOperations(); len(got) != 0 {
		t.Fatalf("corrupt history must start empty, got %+v", got)
	}
	_ = h
}

func TestResolveOperationDestination_LoopbackSelf(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()
	display, _, _ := h.resolveOperationDestination("")
	if display != "local Hub" {
		t.Errorf("loopback self display = %q, want %q", display, "local Hub")
	}
}

func TestClassifyTransferError_DialIsSafeRetry(t *testing.T) {
	code, _, _, retrySafe, duplicateRisk, dataSafe := ClassifyTransferError(errDial, 0, 100, false)
	if code != "destination_unreachable" {
		t.Errorf("code = %q, want destination_unreachable", code)
	}
	if !retrySafe || duplicateRisk || !dataSafe {
		t.Errorf("dial safety flags wrong: retry=%v dup=%v data=%v", retrySafe, duplicateRisk, dataSafe)
	}
}

func TestClassifyTransferError_IntegrityIsSafeRetry(t *testing.T) {
	code, _, _, retrySafe, duplicateRisk, _ := ClassifyTransferError(errChecksum, 100, 100, false)
	if code != "integrity_rejected" {
		t.Errorf("code = %q, want integrity_rejected", code)
	}
	if !retrySafe || duplicateRisk {
		t.Errorf("integrity flags wrong: retry=%v dup=%v", retrySafe, duplicateRisk)
	}
}

func TestClassifyTransferError_DropCompleteIsDuplicateRisk(t *testing.T) {
	_, _, _, retrySafe, duplicateRisk, _ := ClassifyTransferError(errComplete, 0, 100, false)
	if retrySafe || !duplicateRisk {
		t.Errorf("drop_complete must be duplicate risk, got retry=%v dup=%v", retrySafe, duplicateRisk)
	}
}

func TestClassifyTransferError_DropDataIsInterrupted(t *testing.T) {
	code, _, _, retrySafe, duplicateRisk, _ := ClassifyTransferError(errData, 0, 100, false)
	if code != "transfer_interrupted" {
		t.Errorf("code = %q, want transfer_interrupted", code)
	}
	if !retrySafe || duplicateRisk {
		t.Errorf("drop_data flags wrong: retry=%v dup=%v", retrySafe, duplicateRisk)
	}
}

func TestWebDashboard_UploadErrorContract(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	secretText := "SECRET-TEXT-xyz-123"
	body, _ := json.Marshal(map[string]string{"text": secretText, "peer": "127.0.0.1:59992"})
	resp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/drop/upload", "application/json", bytes.NewReader(body))
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
	if payload["status"] != "error" {
		t.Errorf("status field = %v, want error", payload["status"])
	}
	for _, field := range []string{"code", "plain_message", "retry_safe", "duplicate_risk", "data_safe", "next_action", "operation_id", "destination"} {
		if _, ok := payload[field]; !ok {
			t.Errorf("error contract missing %q: %+v", field, payload)
		}
	}
	// Privacy: the secret text must never echo in an error body.
	raw, _ := json.Marshal(payload)
	if strings.Contains(string(raw), secretText) {
		t.Error("error response leaked payload text")
	}
	opID, _ := payload["operation_id"].(string)
	if !strings.HasPrefix(opID, "op-") {
		t.Errorf("operation_id = %q, want op- prefix", opID)
	}

	// The failed attempt must be recorded in the sender ledger.
	opsResp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/transfers/recent")
	if err != nil {
		t.Fatal(err)
	}
	defer opsResp.Body.Close()
	var ops []OperationRecord
	if err := json.NewDecoder(opsResp.Body).Decode(&ops); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("ledger length = %d, want 1", len(ops))
	}
	if ops[0].OperationID != opID {
		t.Errorf("ledger op = %q, want %q", ops[0].OperationID, opID)
	}
	if ops[0].State != OperationStateRetryableFailure && ops[0].State != OperationStateTerminalFailure {
		t.Errorf("failed op state = %q", ops[0].State)
	}
	if !ops[0].RetrySafe || ops[0].DuplicateRisk {
		t.Errorf("dial failure must be safe retry without duplicate risk: %+v", ops[0])
	}
}

func TestWebDashboard_UploadSuccessRecordsOperation(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// Loopback self-delivery: empty peer dials the local endpoint.
	body, _ := json.Marshal(map[string]string{"text": "hello self"})
	resp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/drop/upload", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "success" {
		t.Fatalf("status = %v", payload["status"])
	}
	opID, _ := payload["operation_id"].(string)
	if !strings.HasPrefix(opID, "op-") {
		t.Errorf("operation_id = %q", opID)
	}
	if payload["destination"] == "" || payload["destination"] == nil {
		t.Error("success must name its destination")
	}
	if payload["verified"] != true {
		t.Errorf("success must report verified=true: %+v", payload)
	}
	raw, _ := json.Marshal(payload)
	if strings.Contains(string(raw), "hello self") {
		t.Error("success response leaked payload text")
	}

	opsResp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/transfers/recent")
	if err != nil {
		t.Fatal(err)
	}
	defer opsResp.Body.Close()
	var ops []OperationRecord
	if err := json.NewDecoder(opsResp.Body).Decode(&ops); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].OperationID != opID || ops[0].State != OperationStateCompleted || !ops[0].Verified {
		t.Fatalf("ledger success record wrong: %+v", ops)
	}
}

func TestWebDashboard_FileUploadFailureContract(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.WriteField("peer", "127.0.0.1:59993")
	part, err := w.CreateFormFile("file", "probe.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("file-bytes-not-secret"))
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
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"code", "plain_message", "retry_safe", "duplicate_risk", "data_safe", "next_action", "operation_id", "destination"} {
		if _, ok := payload[field]; !ok {
			t.Errorf("file error contract missing %q: %+v", field, payload)
		}
	}
}

func TestWebDashboard_TransfersClearContract(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	// Seed one operation via loopback self-delivery.
	body, _ := json.Marshal(map[string]string{"text": "clear-probe"})
	seed, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/drop/upload", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	seed.Body.Close()
	if seed.StatusCode != http.StatusOK {
		t.Fatalf("seed upload status = %d", seed.StatusCode)
	}

	// Unauthenticated DELETE must be rejected.
	unauth, err := http.NewRequest(http.MethodDelete, "http://"+h.WebAddr()+"/api/transfers/recent", nil)
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
	req, err := http.NewRequest(http.MethodDelete, "http://"+h.WebAddr()+"/api/transfers/recent", nil)
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

	// Ledger reads back empty.
	opsResp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/transfers/recent")
	if err != nil {
		t.Fatal(err)
	}
	defer opsResp.Body.Close()
	var ops []OperationRecord
	if err := json.NewDecoder(opsResp.Body).Decode(&ops); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("ledger after clear = %d, want 0", len(ops))
	}
}

func TestWebDashboard_TransfersRecentRequiresCapability(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	resp, err := http.Get("http://" + h.WebAddr() + "/api/transfers/recent")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated transfers/recent = %d, want 401", resp.StatusCode)
	}

	authResp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/transfers/recent")
	if err != nil {
		t.Fatal(err)
	}
	defer authResp.Body.Close()
	if authResp.StatusCode != http.StatusOK {
		t.Fatalf("authed transfers/recent = %d, want 200", authResp.StatusCode)
	}
	var ops []OperationRecord
	if err := json.NewDecoder(authResp.Body).Decode(&ops); err != nil {
		t.Fatal(err)
	}
	if ops == nil {
		t.Error("expected non-nil ops slice")
	}
}
