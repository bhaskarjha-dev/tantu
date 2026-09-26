package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"testing"
)

// TestWebDashboard_UploadReadOnlyOutputDir exercises the receiver-rejected
// path end to end: the receiver cannot stage, the sender reports a terminal
// (not duplicate-risk) failure, and nothing is published. Unix-only: Windows
// ACLs do not honor permission-bit removal the same way.
func TestWebDashboard_UploadReadOnlyOutputDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit removal is not portable to Windows ACLs")
	}
	if os.Geteuid() == 0 {
		t.Skip("read-only directories are still writable as root")
	}
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	outDir := h.OutputDir()
	if err := os.Chmod(outDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(outDir, 0700) }()

	body, _ := json.Marshal(map[string]string{"text": "rejected-probe"})
	resp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/drop/upload", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["code"] != "receiver_rejected" {
		t.Errorf("code = %v, want receiver_rejected", payload["code"])
	}
	if payload["duplicate_risk"] == true {
		t.Error("a rejected transfer must not report duplicate risk")
	}
	opID, _ := payload["operation_id"].(string)
	if opID == "" {
		t.Fatal("rejection must carry an operation ID")
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
	if len(ops) != 1 || ops[0].OperationID != opID || ops[0].State != OperationStateTerminalFailure {
		t.Fatalf("ledger rejection record wrong: %+v", ops)
	}
}
