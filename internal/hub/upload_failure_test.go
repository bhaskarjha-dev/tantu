package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestWebDashboard_UploadReadOnlyOutputDir exercises the receiver-rejected
// path end to end: the receiver cannot stage, the sender reports a failure
// that carries no duplicate risk, and nothing is published.
//
// The upload has to be a FILE. A text drop never touches the output directory
// at all - its payload is buffered in memory - so chmod'ing the output
// directory cannot make the receiver refuse one: the test got HTTP 200 while
// asserting 500 on Linux and macOS, and only appeared to work because the
// case never ran on Windows. The file branch is the one that creates
// `.tantu-staging` inside the output directory and publishes into it, so it
// is the one an unwritable output directory actually stops.
//
// The ledger state is retryable rather than terminal, and that is the
// classifier's contract, not an accident: classifyRejection reports receiver
// rejections as retry-safe and pins it in classify_test.go, because a
// rejection leaves no partial state behind and repeating the request cannot
// duplicate anything. What matters for a rejection is that it never claims
// data was lost (data_safe) or that a retry might arrive twice
// (duplicate_risk).
//
// Unix-only: Windows ACLs do not honor permission-bit removal the same way.
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

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "rejected.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "rejected-file-probe"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	resp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/drop/upload", w.FormDataContentType(), bytes.NewReader(body.Bytes()))
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
	if payload["data_safe"] != true {
		t.Errorf("data_safe = %v, want true: a rejection means the sender still holds the bytes", payload["data_safe"])
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
	if len(ops) != 1 || ops[0].OperationID != opID || ops[0].State != OperationStateRetryableFailure {
		t.Fatalf("ledger rejection record wrong: %+v", ops)
	}
	if ops[0].RetrySafe || ops[0].DuplicateRisk || !ops[0].DataSafe {
		t.Fatalf("rejection flags wrong: retry_safe=%v duplicate_risk=%v data_safe=%v",
			ops[0].RetrySafe, ops[0].DuplicateRisk, ops[0].DataSafe)
	}

	// Nothing may be published: the output directory is still exactly the
	// empty directory it was before the attempt (read permission remains).
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "rejected.txt" {
			t.Fatalf("a rejected transfer published %s", filepath.Join(outDir, e.Name()))
		}
	}
}
