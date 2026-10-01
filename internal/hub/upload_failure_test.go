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

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

// TestWebDashboard_UploadRejectedWhenStagingCannotBeCreated exercises the
// receiver-rejected path end to end on every platform: the receiver cannot
// create its staging directory, the sender reports a failure that carries no
// duplicate risk, and nothing is published.
//
// The upload has to be a FILE. A text drop never touches the output directory
// at all - its payload is buffered in memory - so a blocked output directory
// cannot make the receiver refuse one, which is how the original version of
// this test asserted HTTP 500 while receiving 200 on Linux and macOS.
//
// The obstacle here is a regular file sitting where `.tantu-staging` must be
// created: every platform refuses that the same way, so this test runs
// everywhere and was verified on Windows as well as in CI.
func TestWebDashboard_UploadRejectedWhenStagingCannotBeCreated(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	staging := filepath.Join(h.OutputDir(), drop.StagingDirName)
	if err := os.WriteFile(staging, []byte("occupied by a regular file"), 0600); err != nil {
		t.Fatal(err)
	}

	status, payload := postRejectedFile(t, h, "rejected.txt", "rejected-file-probe")
	assertReceiverRejected(t, h, status, payload)
}

// TestWebDashboard_UploadReadOnlyOutputDir is the same contract for the case a
// user actually hits: an output directory the receiver has no permission to
// write. Unix-only, because Windows ACLs do not honor permission-bit removal.
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

	status, payload := postRejectedFile(t, h, "rejected.txt", "rejected-file-probe")
	assertReceiverRejected(t, h, status, payload)
}

// postRejectedFile uploads one small file through /api/drop/upload.
func postRejectedFile(t *testing.T, h *Hub, name, content string) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, content); err != nil {
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
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode rejection payload: %v", err)
	}
	return resp.StatusCode, payload
}

// assertReceiverRejected pins what a receiver rejection promises the sender,
// and that nothing was published. The ledger state is retryable rather than
// terminal, and that is the classifier's contract, not an accident:
// classifyRejection reports receiver rejections as retry-safe and pins it in
// classify_test.go, because a rejection leaves no partial state behind, so
// repeating the request cannot duplicate. What must never be claimed is that
// data was lost (data_safe) or that a retry might arrive twice
// (duplicate_risk).
func assertReceiverRejected(t *testing.T, h *Hub, status int, payload map[string]any) {
	t.Helper()
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
	if payload["code"] != "receiver_rejected" {
		t.Errorf("code = %v, want receiver_rejected", payload["code"])
	}
	if payload["duplicate_risk"] == true {
		t.Error("a rejected transfer must not report duplicate risk")
	}
	if payload["retry_safe"] != true {
		t.Errorf("retry_safe = %v, want true: a rejection leaves no partial state, so repeating it cannot duplicate", payload["retry_safe"])
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
	if !ops[0].RetrySafe || ops[0].DuplicateRisk || !ops[0].DataSafe {
		t.Fatalf("rejection flags wrong: retry_safe=%v (want true), duplicate_risk=%v (want false), data_safe=%v (want true)",
			ops[0].RetrySafe, ops[0].DuplicateRisk, ops[0].DataSafe)
	}

	entries, err := os.ReadDir(h.OutputDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "rejected.txt" {
			t.Fatalf("a rejected transfer published %s", filepath.Join(h.OutputDir(), e.Name()))
		}
	}
}
