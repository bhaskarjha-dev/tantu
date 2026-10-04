package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// A received file's SavedPath is what the inbox displays, what the download
// link serves, and what the inline preview resolves against the output
// directory. Publication returned a bare file name on both of its paths, so
// filepath.Abs resolved the name against the Hub's working directory, the
// containment check in serveReceivedFile failed closed, and preview and
// download 404'd for every file anyone actually received. Every test that
// touched this path injected an absolute SavedPath by hand, which is why the
// defect had no failing test: only a real receive exercises it.
func TestHub_ReceivedFileSavedPathIsAbsoluteAndServes(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	content := []byte("received through the real publication path")
	tr := transport.NewLoopbackTransport()
	conn, err := tr.Dial(h.P2PAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	meta := drop.DropSend{
		DropID:         "saved-path-1",
		IdempotencyKey: "saved-path-1",
		Kind:           drop.DropKindFile,
		Name:           "saved-path.txt",
		Size:           int64(len(content)),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := drop.SendDrop(ctx, conn, meta, bytes.NewReader(content), drop.SendDropConfig{}); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	// The inbox entry is recorded after the acknowledgement, so poll for it
	// rather than assuming the two sides are ordered.
	var item ReceivedDropItem
	deadline := time.Now().Add(5 * time.Second)
	for {
		found, ok := receivedItemNamed(t, h, "saved-path.txt")
		if ok {
			item = found
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the received file never appeared in the inbox")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if item.SavedPath == "" {
		t.Fatal("the received file recorded no saved path")
	}
	if !filepath.IsAbs(item.SavedPath) {
		t.Fatalf("saved path %q is a bare file name: the inbox resolves it against the output directory, so preview and download 404", item.SavedPath)
	}
	got, err := os.ReadFile(item.SavedPath)
	if err != nil {
		t.Fatalf("saved path %q does not resolve to the received bytes: %v", item.SavedPath, err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("file at saved path holds %q, want the received bytes", got)
	}

	req, err := http.NewRequest(http.MethodGet, "http://"+h.WebAddr()+"/api/drop/file?id="+item.ID+"&mode=inline", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(IPCTokenHeader, h.ipcToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("inline preview = %d %q, want 200 with the received bytes", resp.StatusCode, truncateForLog(body))
	}
}

// Publication has to reject a digest that does not describe the staged bytes,
// and has to report the full path it published under. Both properties used to
// hold only on the copy path: the rename path skipped the check and returned
// a bare name, so this test failed on macOS and Linux - the platforms where
// the rename is the normal outcome.
func TestFinalizePartFileVerifiesDigestAndReturnsFullPath(t *testing.T) {
	payload := []byte("received")

	t.Run("a mismatched digest is refused", func(t *testing.T) {
		f, part, desired := stagePayload(t, payload)
		wrong := sha256Sum("different bytes")
		if _, err := finalizePartFile(f, part, desired, filepath.Dir(desired), wrong, int64(len(payload))); err == nil {
			t.Fatal("publication succeeded with a mismatched digest")
		}
		if _, err := os.Stat(desired); !os.IsNotExist(err) {
			t.Fatalf("a refused publication created the delivered name, stat err = %v", err)
		}
	})

	t.Run("the published path is the full path", func(t *testing.T) {
		f, part, desired := stagePayload(t, payload)
		published, err := finalizePartFile(f, part, desired, filepath.Dir(desired), sha256Sum(string(payload)), int64(len(payload)))
		if err != nil {
			t.Fatalf("finalizePartFile failed: %v", err)
		}
		if published != desired {
			t.Fatalf("published %q, want the full path %q", published, desired)
		}
		got, err := os.ReadFile(published)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("published content mismatch: %q err=%v", got, err)
		}
		if _, err := os.Stat(part); !os.IsNotExist(err) {
			t.Fatalf("staging partial still present after publication: %v", err)
		}
	})
}

// stagePayload writes payload into a private staging partial beside the
// destination, the state a completed transfer is in when publication starts.
func stagePayload(t *testing.T, payload []byte) (*os.File, string, string) {
	t.Helper()
	dir := t.TempDir()
	desired := filepath.Join(dir, "payload.txt")
	part := filepath.Join(dir, drop.StagingDirName, "payload.txt.part")
	if err := os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	return f, part, desired
}

func receivedItemNamed(t *testing.T, h *Hub, name string) (ReceivedDropItem, bool) {
	t.Helper()
	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/drop/recent")
	if err != nil {
		t.Fatalf("GET /api/drop/recent failed: %v", err)
	}
	defer resp.Body.Close()
	var items []ReceivedDropItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode recent drops: %v", err)
	}
	for _, it := range items {
		if it.Name == name {
			return it, true
		}
	}
	return ReceivedDropItem{}, false
}

func sha256Sum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func truncateForLog(b []byte) string {
	if len(b) > 120 {
		return string(b[:120]) + "..."
	}
	return string(b)
}
