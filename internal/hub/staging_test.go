package hub

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A planted symlink at the staging path must never cause a victim file to be
// opened or truncated. Exclusive creation unlinks the planted entry and
// creates a fresh regular file instead.
func TestCreateStagingPart_PlantedsymlinkDoesNotTouchVictim(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.dat")
	if err := os.WriteFile(victim, []byte("precious-contents"), 0600); err != nil {
		t.Fatal(err)
	}
	partPath := filepath.Join(dir, "drop123.part")
	if err := os.Symlink(victim, partPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	f, err := createStagingPart(partPath)
	if err != nil {
		t.Fatalf("createStagingPart with planted symlink: %v", err)
	}
	_ = f.Close()

	if got, _ := os.ReadFile(victim); string(got) != "precious-contents" {
		t.Fatalf("victim file was modified: %q", got)
	}
	info, err := os.Lstat(partPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("staging path is not a fresh regular file: info=%v err=%v", info, err)
	}
	if info.Size() != 0 {
		t.Fatalf("staging file size = %d, want 0", info.Size())
	}
}

// A stale regular partial (e.g. rejected resume candidate) is replaced with a
// fresh empty file, preserving the previous truncate-and-restart behavior.
func TestCreateStagingPart_ReplacesStaleRegularFile(t *testing.T) {
	dir := t.TempDir()
	partPath := filepath.Join(dir, "drop123.part")
	if err := os.WriteFile(partPath, []byte("stale-partial-data"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := createStagingPart(partPath)
	if err != nil {
		t.Fatalf("createStagingPart with stale file: %v", err)
	}
	_ = f.Close()
	info, err := os.Stat(partPath)
	if err != nil || info.Size() != 0 {
		t.Fatalf("stale partial was not replaced with empty file: info=%v err=%v", info, err)
	}
}

// A conflicting directory fails closed instead of being traversed.
func TestCreateStagingPart_ConflictingDirectoryFails(t *testing.T) {
	dir := t.TempDir()
	partPath := filepath.Join(dir, "drop123.part")
	if err := os.Mkdir(partPath, 0700); err != nil {
		t.Fatal(err)
	}
	// Make removal fail: a non-empty directory cannot be removed by os.Remove.
	if err := os.WriteFile(filepath.Join(partPath, "x"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if f, err := createStagingPart(partPath); err == nil {
		_ = f.Close()
		t.Fatal("createStagingPart over non-empty directory succeeded, want error")
	}
}

// openResumePart must refuse a file whose identity changed between inspection
// and open (simulated by passing a descriptor for a different file).
func TestOpenResumePart_IdentityChangeRejected(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.part")
	other := filepath.Join(dir, "other.part")
	if err := os.WriteFile(real, []byte("real"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	inspected, err := os.Lstat(other)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := openResumePart(real, inspected); err == nil {
		_ = f.Close()
		t.Fatal("openResumePart with mismatched identity succeeded, want error")
	}
}

// openResumePart opens a genuinely unchanged file for resume.
func TestOpenResumePart_UnchangedFileOpens(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "resume.part")
	if err := os.WriteFile(part, []byte("partial-data"), 0600); err != nil {
		t.Fatal(err)
	}
	inspected, err := os.Lstat(part)
	if err != nil {
		t.Fatal(err)
	}
	f, err := openResumePart(part, inspected)
	if err != nil {
		t.Fatalf("openResumePart: %v", err)
	}
	_ = f.Close()
}

// openResumePart reports a raced-away file as retryable, not fatal.
func TestOpenResumePart_MissingFileIsRetryable(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "gone.part")
	if err := os.WriteFile(part, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	inspected, err := os.Lstat(part)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(part); err != nil {
		t.Fatal(err)
	}
	_, err = openResumePart(part, inspected)
	var retry *retryStagingOpen
	if !errors.As(err, &retry) {
		t.Fatalf("openResumePart missing file error = %v, want retryStagingOpen", err)
	}
}

func TestEnsureStagingDir_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "staging-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	if err := ensureStagingDir(link); err == nil {
		t.Fatal("symlinked staging dir accepted, want error")
	}
	if err := ensureStagingDir(real); err != nil {
		t.Fatalf("real staging dir rejected: %v", err)
	}
}

func TestOpenResumePart_RejectsHardlink(t *testing.T) {
	// Link-count enforcement is Unix-only (Windows FileInfo hides nlink).
	if runtime.GOOS == "windows" {
		t.Skip("hardlink detection requires Unix file modes")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "victim.dat")
	if err := os.WriteFile(real, []byte("victim-contents"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "drop123.part")
	if err := os.Link(real, link); err != nil {
		t.Skipf("hardlinks not supported: %v", err)
	}
	inspected, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := openResumePart(link, inspected); err == nil {
		_ = f.Close()
		t.Fatal("multi-linked partial opened for resume, want error")
	}
	if got, _ := os.ReadFile(real); string(got) != "victim-contents" {
		t.Fatalf("victim modified: %q", got)
	}
}
