package hub

// End-to-end gate for directory-structure preservation.
//
// Everything else in the rel-path gates checks one decision. This one drives a
// real Hub over a real loopback connection with real bytes, because the claim
// users care about is "the project I sent is the project I got" - and the only
// way that can be false is in the seams between validation, staging and
// publication, which no unit test of those three can see.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// startRelPathHub brings up a real Hub on a loopback transport with the given
// output directory.
func startRelPathHub(t *testing.T, storeDir, outDir string) *Hub {
	t.Helper()
	h, err := NewHub(HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      storeDir,
		OutputDir:     outDir,
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- h.Start(ctx) }()
	select {
	case <-h.Ready():
	case err := <-errCh:
		cancel()
		t.Fatalf("Hub failed to start: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("timed out waiting for Hub ready")
	}
	t.Cleanup(func() {
		cancel()
		h.Stop()
	})
	return h
}

// sendToHub streams one drop to a running Hub over a real connection.
func sendToHub(t *testing.T, h *Hub, meta drop.DropSend, body []byte) error {
	t.Helper()
	tr := transport.NewLoopbackTransport()
	conn, err := tr.Dial(h.P2PAddr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return drop.SendDrop(ctx, conn, meta, bytes.NewReader(body), drop.SendDropConfig{Timeout: 20 * time.Second})
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// TestHub_DirectorySendPreservesStructure sends a small tree the way a directory
// send does - one transfer per file, each carrying its path - and asserts the
// receiver's downloads folder is that tree.
func TestHub_DirectorySendPreservesStructure(t *testing.T) {
	tempDir := t.TempDir()
	outDir := filepath.Join(tempDir, "downloads")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h := startRelPathHub(t, tempDir, outDir)

	tree := map[string]string{
		"project/README.md":                       "readme",
		"project/docs/api/openapi.yaml":           "openapi",
		"project/docs/api/notes.txt":              "notes",
		"project/src/main.go":                     "package main",
		"project/src/deep/nested/deeper/leaf.bin": "\x00\x01\x02leaf",
		"project/empty-dir-marker/.keep":          "",
	}
	for relPath, body := range tree {
		meta := drop.DropSend{
			DropID:         "send-" + strings.NewReplacer("/", "-", ".", "-", " ", "-").Replace(relPath),
			IdempotencyKey: "key-" + strings.NewReplacer("/", "-", ".", "-", " ", "-").Replace(relPath),
			Kind:           drop.DropKindFile,
			Name:           filepath.Base(relPath),
			RelPath:        relPath,
			Size:           int64(len(body)),
			ChunkSize:      32 * 1024,
			HeadHash:       digestOf([]byte(body)),
		}
		if err := sendToHub(t, h, meta, []byte(body)); err != nil {
			t.Fatalf("send %s: %v", relPath, err)
		}
	}

	for relPath, want := range tree {
		onDisk := filepath.Join(outDir, filepath.FromSlash(relPath))
		got, err := os.ReadFile(onDisk)
		if err != nil {
			t.Errorf("expected %s on disk: %v", relPath, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", relPath, got, want)
		}
	}

	// Nothing may be published flat. The old behaviour folded the whole path
	// into the filename, so its residue is a file at the top of the downloads
	// folder named after a path.
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == drop.StagingDirName {
			// The receiver's private staging area lives inside the output
			// directory by design; it is not delivered content.
			continue
		}
		if !entry.IsDir() {
			t.Errorf("a flat file was published alongside the tree: %q", entry.Name())
		}
	}
}

// TestHub_DirectorySendRefusesAnEscapingPath is the security half: a peer that
// sends ".." must be refused, nothing may be written outside the output
// directory, and the refusal must arrive as a rejection the sender can report
// as a known outcome rather than as an unknown one.
//
// The frames are crafted by hand rather than produced by drop.SendDrop, because
// SendDrop runs the same validator locally and would refuse before the Hub ever
// saw the transfer. The sender-side gate is worth having, but it is not evidence
// about the receiver - and the receiver is the thing a malicious peer is
// actually talking to.
func TestHub_DirectorySendRefusesAnEscapingPath(t *testing.T) {
	tempDir := t.TempDir()
	outDir := filepath.Join(tempDir, "downloads")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A sentinel one level above the output directory: the classic target.
	sentinel := filepath.Join(tempDir, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := startRelPathHub(t, tempDir, outDir)

	for _, relPath := range []string{
		"../sentinel.txt",
		"../../sentinel.txt",
		"project/../../sentinel.txt",
		"/absolute/sentinel.txt",
	} {
		tr := transport.NewLoopbackTransport()
		conn, err := tr.Dial(h.P2PAddr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		meta := drop.DropSend{
			DropID:    drop.NewDropID(),
			Kind:      drop.DropKindFile,
			Name:      "sentinel.txt",
			RelPath:   relPath,
			Size:      8,
			ChunkSize: 32 * 1024,
		}
		if err := conn.Send(drop.TypeDropSend, meta); err != nil {
			conn.Close()
			t.Fatalf("send crafted drop_send: %v", err)
		}
		typeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if deadline, ok := conn.(transport.Deadliner); ok {
			_ = deadline.SetReadDeadline(time.Now().Add(10 * time.Second))
		}
		_ = typeCtx
		env, recvErr := conn.Receive()
		cancel()
		if recvErr != nil {
			conn.Close()
			t.Errorf("rel_path %q: no answer from the receiver: %v", relPath, recvErr)
			continue
		}
		var ack drop.DropAck
		decodeErr := env.DecodePayload(&ack)
		conn.Close()
		if decodeErr != nil {
			t.Errorf("rel_path %q: undecodable answer %v", relPath, decodeErr)
			continue
		}
		if ack.Accepted {
			t.Errorf("rel_path %q was accepted by the receiver", relPath)
			continue
		}
		if strings.TrimSpace(ack.Error) == "" {
			t.Errorf("rel_path %q was refused without a reason; the sender would have nothing to show", relPath)
		}
	}

	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "untouched" {
		t.Errorf("the sentinel was modified: %q err=%v", data, err)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == drop.StagingDirName {
			continue
		}
		t.Errorf("a refused transfer created %q inside the output directory", entry.Name())
	}
}

// TestSendDropRefusesAnEscapingPathLocally is the cheap half of the same rule,
// on the path a cooperating CLI takes. It is separate from the receiver gate
// because the two can fail independently, and only one of them protects the
// disk.
func TestSendDropRefusesAnEscapingPathLocally(t *testing.T) {
	tempDir := t.TempDir()
	outDir := filepath.Join(tempDir, "downloads")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h := startRelPathHub(t, tempDir, outDir)

	for _, relPath := range []string{"../escape.txt", "/etc/passwd", "a/../../b", "a//b"} {
		meta := drop.DropSend{
			DropID:    drop.NewDropID(),
			Kind:      drop.DropKindFile,
			Name:      "escape.txt",
			RelPath:   relPath,
			Size:      4,
			ChunkSize: 32 * 1024,
		}
		err := sendToHub(t, h, meta, []byte("body"))
		if err == nil {
			t.Errorf("drop.SendDrop accepted rel_path %q", relPath)
			continue
		}
		// The message must come from the local validator: the sender refuses
		// before the frame is written, so a hostile value never reaches the
		// network at all.
		if !strings.Contains(err.Error(), "rel_path") {
			t.Errorf("rel_path %q failed with %v, which is not the local validator", relPath, err)
		}
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != drop.StagingDirName {
			t.Errorf("a locally refused send still reached the receiver: %q", entry.Name())
		}
	}
}

// TestHub_ResendingADirectoryProducesASecondTree is the collision gate, driven
// through the Hub rather than through the publication helper.
//
// A second send of the same directory is the ordinary case - you fixed one
// file and sent the project again - and it is where the destination is
// disambiguated. That decision happens before publication, in the receiver's
// metadata handler, and it was writing the disambiguated name as a *child* of
// the delivered file. Nothing in the publication tests could see it, because
// they were handed an already-chosen destination. It was found by sending the
// same directory twice against the real binary.
func TestHub_ResendingADirectoryProducesASecondTree(t *testing.T) {
	tempDir := t.TempDir()
	outDir := filepath.Join(tempDir, "downloads")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h := startRelPathHub(t, tempDir, outDir)

	body := []byte("v1")
	send := func(id string) {
		t.Helper()
		meta := drop.DropSend{
			DropID:    id,
			Kind:      drop.DropKindFile,
			Name:      "README.md",
			RelPath:   "project/README.md",
			Size:      int64(len(body)),
			ChunkSize: 32 * 1024,
		}
		if err := sendToHub(t, h, meta, body); err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
	}
	send("send-1")
	send("send-2")

	if data, err := os.ReadFile(filepath.Join(outDir, "project", "README.md")); err != nil || string(data) != "v1" {
		t.Fatalf("first delivery: %q err=%v", data, err)
	}
	second := filepath.Join(outDir, "project", "README (1).md")
	if data, err := os.ReadFile(second); err != nil || string(data) != "v1" {
		t.Errorf("second delivery at %s: %q err=%v", second, data, err)
	}
	// The decisive shape: no directory named after the delivered file, which is
	// what a name appended to the whole path would produce.
	if info, err := os.Stat(filepath.Join(outDir, "project", "README.md")); err == nil && info.IsDir() {
		t.Errorf("the delivered file became a directory: %v", info)
	}
}

// TestHub_SingleFileSendIsUnchanged is the blast-radius gate. Every transfer the
// product performs in practice is a single-file send with no relative path; if
// that changed, everything would be at risk and nothing in the feature's own
// tests would say so.
func TestHub_SingleFileSendIsUnchanged(t *testing.T) {
	tempDir := t.TempDir()
	outDir := filepath.Join(tempDir, "downloads")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h := startRelPathHub(t, tempDir, outDir)

	body := []byte("a plain file")
	meta := drop.DropSend{
		DropID:         "plain-1",
		IdempotencyKey: "plain-key-1",
		Kind:           drop.DropKindFile,
		Name:           "notes.txt",
		Size:           int64(len(body)),
		ChunkSize:      32 * 1024,
	}
	if err := sendToHub(t, h, meta, body); err != nil {
		t.Fatalf("send: %v", err)
	}
	onDisk := filepath.Join(outDir, "notes.txt")
	got, err := os.ReadFile(onDisk)
	if err != nil || string(got) != string(body) {
		t.Fatalf("published %q err=%v", got, err)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == drop.StagingDirName {
			continue
		}
		if entry.IsDir() {
			t.Errorf("a single-file send created directory %q", entry.Name())
		}
	}
}
