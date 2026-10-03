package hub

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

// Planted staging bytes must never reach a delivered file, in either resume
// window. Below 64 KiB the head-identity check in OnMeta is skipped and only
// the receiver's whole-payload digest stands between the planted prefix and
// the output directory; at or above 64 KiB the head check must discard the
// planted file and restart from scratch. TestHub_DropResumption pins only
// the honest-bytes path, so a change that trusts the on-disk prefix would
// otherwise ship undetected.
func TestHub_DropResumeNeverPublishesTamperedStagingBytes(t *testing.T) {
	cases := []struct {
		name       string
		preSeeded  int64
		wantAck    int64
		wantReject bool
	}{
		// 32 KiB: validBytes < 64 KiB, so the head-identity check is skipped
		// and the final digest comparison is the only barrier.
		{name: "below head-check window", preSeeded: 32 * 1024, wantAck: 32 * 1024, wantReject: true},
		// 64 KiB: the head check runs, finds the planted bytes, and must
		// restart the transfer from zero instead of resuming them.
		{name: "at head-check window", preSeeded: 64 * 1024, wantAck: 0, wantReject: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runTamperedResumeCase(t, tc.preSeeded, tc.wantAck, tc.wantReject)
		})
	}
}

func runTamperedResumeCase(t *testing.T, preSeededBytes, wantAck int64, wantReject bool) {
	t.Helper()

	tempDir := t.TempDir()
	outDir := filepath.Join(tempDir, "drops")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatalf("failed to create drops outDir: %v", err)
	}

	chunkSize := 32 * 1024
	payload := bytes.Repeat([]byte("HubDropResumptionTestPayload123!"), 3200) // 102,400 bytes
	fileName := "tampered-resume.bin"
	dropID := "tampered-drop"

	// Plant a prefix of the right length whose bytes differ from the payload
	// everywhere, as a local attacker with staging access would.
	stagingDir := filepath.Join(outDir, ".tantu-staging")
	if err := os.MkdirAll(stagingDir, 0700); err != nil {
		t.Fatalf("failed to create staging directory: %v", err)
	}
	partPath := filepath.Join(stagingDir, dropID+".part")
	if err := os.WriteFile(partPath, bytes.Repeat([]byte{0xA5}, int(preSeededBytes)), 0600); err != nil {
		t.Fatalf("failed to plant part file: %v", err)
	}

	h, err := NewHub(HubConfig{
		TransportType: "loopback",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      tempDir,
		OutputDir:     outDir,
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Start(ctx)
	}()

	select {
	case <-h.Ready():
	case err := <-errCh:
		t.Fatalf("Hub failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for Hub ready")
	}
	defer h.Stop()

	tr := transport.NewLoopbackTransport()
	conn, err := tr.Dial(h.P2PAddr().String())
	if err != nil {
		t.Fatalf("failed to dial hub P2P addr: %v", err)
	}
	defer conn.Close()

	// The wire metadata is honest — a real sender declares the true head
	// hash and payload digest; only the on-disk prefix is tampered with.
	head := sha256.Sum256(payload[:64*1024])
	meta := drop.DropSend{
		DropID:    dropID,
		Kind:      drop.DropKindFile,
		Name:      fileName,
		Size:      int64(len(payload)),
		ChunkSize: chunkSize,
		HeadHash:  hex.EncodeToString(head[:]),
	}
	if err := drop.WriteStagingManifest(partPath, meta); err != nil {
		t.Fatalf("write staging manifest: %v", err)
	}

	var acceptedOffset int64
	sendErr := drop.SendDrop(ctx, conn, meta, bytes.NewReader(payload), drop.SendDropConfig{
		OnAck: func(ack drop.DropAck) { acceptedOffset = ack.ReceivedBytes },
	})

	destPath := filepath.Join(outDir, fileName)

	if wantReject {
		if sendErr == nil {
			t.Fatalf("tampered resume was accepted; planted staging bytes reached the wire as valid data")
		}
		if !strings.Contains(sendErr.Error(), "SHA-256") {
			t.Errorf("rejection reason = %q, want a SHA-256 mismatch", sendErr)
		}
		if acceptedOffset != wantAck {
			t.Errorf("resume ACK offset = %d, want %d", acceptedOffset, wantAck)
		}
		// Nothing may appear under the delivered name — not even briefly.
		// Give the pipeline its usual completion window, then verify no
		// file was published in the output directory at all.
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(destPath); err == nil {
				t.Fatalf("tampered staging bytes were published to %s", destPath)
			}
			time.Sleep(50 * time.Millisecond)
		}
		entries, err := os.ReadDir(outDir)
		if err != nil {
			t.Fatalf("read outDir: %v", err)
		}
		for _, entry := range entries {
			if entry.Name() != ".tantu-staging" {
				t.Errorf("output directory contains unexpected entry %q after rejected transfer", entry.Name())
			}
		}
		return
	}

	if sendErr != nil {
		t.Fatalf("SendDrop failed: %v", sendErr)
	}
	if acceptedOffset != wantAck {
		t.Errorf("resume ACK offset = %d, want %d (the head check must discard planted bytes, not resume them)", acceptedOffset, wantAck)
	}
	deadline := time.Now().Add(3 * time.Second)
	var content []byte
	for time.Now().Before(deadline) {
		if _, err := os.Stat(destPath); err == nil {
			content, _ = os.ReadFile(destPath)
			if len(content) == len(payload) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !bytes.Equal(content, payload) {
		t.Fatalf("published content must equal the honest payload byte-for-byte (got %d bytes)", len(content))
	}
	if _, err := os.Stat(partPath); !os.IsNotExist(err) {
		t.Errorf("expected part file to be removed after completion, but it exists")
	}
}
