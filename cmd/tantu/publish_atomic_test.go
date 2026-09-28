package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// Publication must be atomic: a reader never observes a partially written file
// under the delivered name, and an interrupted publication leaves a hidden
// temporary rather than a truncated deliverable.
func TestFinalizeIncomingPartRejectsDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	desired := filepath.Join(dir, "payload.bin")
	part := filepath.Join(dir, ".tantu-staging", "payload.bin.part")
	if err := os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("the real bytes")
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}

	// A digest of different content must fail the publication: the reported
	// "verified" digest has to describe what actually lands on disk.
	wrong := sha256.Sum256([]byte("different bytes"))
	if _, err := finalizeIncomingPart(f, part, desired, hex.EncodeToString(wrong[:]), int64(len(payload))); err == nil {
		t.Fatal("publication succeeded with a mismatched digest")
	}
	if _, err := os.Stat(desired); !os.IsNotExist(err) {
		t.Fatalf("a failed publication must not create the delivered name, stat err = %v", err)
	}
}

func TestFinalizeIncomingPartRejectsSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	desired := filepath.Join(dir, "payload.bin")
	part := filepath.Join(dir, ".tantu-staging", "payload.bin.part")
	if err := os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("twelve bytes")
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	// The sender declared more bytes than are staged.
	if _, err := finalizeIncomingPart(f, part, desired, hex.EncodeToString(sum[:]), int64(len(payload))+5); err == nil {
		t.Fatal("publication succeeded with a short staged file")
	}
	if _, err := os.Stat(desired); !os.IsNotExist(err) {
		t.Fatalf("a failed publication must not create the delivered name, stat err = %v", err)
	}
}

// A successful publication consumes the staging name, so a later sweep cannot
// mistake the delivered file for a resumable partial.
func TestFinalizeIncomingPartConsumesStagingName(t *testing.T) {
	dir := t.TempDir()
	desired := filepath.Join(dir, "payload.txt")
	part := filepath.Join(dir, ".tantu-staging", "payload.txt.part")
	if err := os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("done")
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	published, err := finalizeIncomingPart(f, part, desired, hex.EncodeToString(sum[:]), int64(len(payload)))
	if err != nil {
		t.Fatalf("finalizeIncomingPart failed: %v", err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("staging partial still present after publication: %v", err)
	}
	got, err := os.ReadFile(published)
	if err != nil || string(got) != "done" {
		t.Fatalf("published content mismatch: %q err=%v", got, err)
	}
}
