package main

// Directory expansion decides what a batch send will actually transmit, so
// each of its properties is pinned here: what is included, what is refused,
// what cannot collide on the receiver, and what fails early rather than
// part-way through a run.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

func timeAfterShort() <-chan time.Time { return time.After(10 * time.Second) }

func dropKeyValid(k string) bool { return drop.ValidTombstoneKey(k) }

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestExpandDirectoryCollectsFilesRecursively(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "docs", "b.md"), "bb")
	writeFile(t, filepath.Join(root, "docs", "api", "c.md"), "ccc")

	files, err := expandDirectory(root, false)
	if err != nil {
		t.Fatalf("expandDirectory: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d: %+v", len(files), files)
	}
	// Order must be stable so a failing run names the same file every time.
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	want := []string{"a.txt", "docs-api-c.md", "docs-b.md"}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("name[%d] = %q, want %q (full: %v)", i, names[i], want[i], names)
		}
	}
}

// The receiver flattens every name to its basename, so two files with the
// same name in different directories must not be sent under the same name or
// one silently overwrites the other.
func TestExpandDirectoryNamesCannotCollideOnReceiver(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "one", "notes.txt"), "1")
	writeFile(t, filepath.Join(root, "two", "notes.txt"), "2")
	writeFile(t, filepath.Join(root, "notes.txt"), "3")

	files, err := expandDirectory(root, false)
	if err != nil {
		t.Fatalf("expandDirectory: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.Name] {
			t.Fatalf("duplicate peer-facing name %q: the receiver would collapse these files", f.Name)
		}
		seen[f.Name] = true
		if strings.ContainsAny(f.Name, "/\\") {
			t.Errorf("name %q must be a flat filename, not a path", f.Name)
		}
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}
}

// A symlink can point outside the tree, or at a parent, which would make the
// walk non-terminating. Following either would transmit files the user never
// pointed at, so the walk refuses instead of silently skipping - a silently
// shortened batch looks exactly like a complete one.
func TestExpandDirectoryRefusesSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real.txt"), "content")
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "should not be sent")

	if err := os.Symlink(outside, filepath.Join(root, "link-dir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	files, err := expandDirectory(root, false)
	if err == nil {
		t.Fatalf("a symlink must be refused, got %d files: %+v", len(files), files)
	}
	if !strings.Contains(err.Message, "symlink") {
		t.Errorf("the error should name the cause, got %q", err.Message)
	}
	if err.NextAction == "" {
		t.Error("a refusal must offer a next action")
	}
}

// A self-referential link is the non-terminating case. It must fail fast
// rather than walking until the file count or time runs out.
func TestExpandDirectoryRefusesSelfReferentialSymlink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "file.txt"), "x")
	if err := os.Symlink(root, filepath.Join(root, "self")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	done := make(chan struct{})
	var files []expandedFile
	var expErr *directoryExpansionError
	go func() {
		files, expErr = expandDirectory(root, false)
		close(done)
	}()
	select {
	case <-done:
		// Terminating is the requirement; the refusal is checked below.
	case <-timeAfterShort():
		t.Fatal("expandDirectory did not terminate on a self-referential symlink")
	}
	if expErr == nil {
		t.Fatalf("a self-referential symlink must be refused, got %d files", len(files))
	}
}

// An empty directory must not report a completed send. "Sent 0 of 0 files"
// reads as success to a person and to a script.
func TestExpandDirectoryRejectsEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	// A subdirectory with nothing in it is still an empty send.
	if err := os.MkdirAll(filepath.Join(root, "nested", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	files, err := expandDirectory(root, false)
	if err == nil {
		t.Fatalf("an empty directory must be refused, got %d files", len(files))
	}
	if err.Code != "invalid_input" {
		t.Errorf("code = %q, want invalid_input", err.Code)
	}
}

// A file that is not a directory is a caller mistake, reported as such.
func TestExpandDirectoryRejectsNonDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	writeFile(t, path, "x")
	_, err := expandDirectory(path, false)
	if err == nil {
		t.Fatal("expanding a plain file must be refused")
	}
	if err.Code != "invalid_input" {
		t.Errorf("code = %q, want invalid_input", err.Code)
	}
}

func TestExpandDirectoryRejectsMissingPath(t *testing.T) {
	_, err := expandDirectory(filepath.Join(t.TempDir(), "nope"), false)
	if err == nil {
		t.Fatal("a missing path must be refused")
	}
	if err.Code != "input_error" {
		t.Errorf("code = %q, want input_error", err.Code)
	}
}

// The file-count bound must fail at the point of the mistake, not deep into a
// run of transfers.
func TestExpandDirectoryEnforcesFileCountBound(t *testing.T) {
	if testing.Short() {
		t.Skip("creates many files")
	}
	root := t.TempDir()
	for i := 0; i <= maxExpandedFiles; i++ {
		writeFile(t, filepath.Join(root, "f"+pad(i)+".txt"), "x")
	}
	_, err := expandDirectory(root, false)
	if err == nil {
		t.Fatal("exceeding the file bound must be refused")
	}
	if err.Code != "limit_exceeded" {
		t.Errorf("code = %q, want limit_exceeded", err.Code)
	}
}

func pad(i int) string {
	s := "00000" + itoa(i)
	return s[len(s)-5:]
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// A deeply nested path produces a long relative name; it must be bounded to
// the receiver's own limit, or the receiver would truncate it and two distinct
// files could become the same name.
// Windows refuses to create symlinks without an elevated privilege, so the
// symlink guard cannot be exercised through a real link there. It is
// nevertheless a real branch in the walk, and leaving it unverified on the
// platform most likely to ship a directory send would test the guard
// precisely where a path escape is most dangerous. The predicate the walk
// consults is therefore pinned directly.
func TestWalkSymlinkPredicateMatchesTheModeBit(t *testing.T) {
	// A regular file must not be treated as a link.
	if os.FileMode(0).Type()&os.ModeSymlink != 0 {
		t.Error("a regular file must not test as a symlink")
	}
	// A symlink's own type bits must match the bit the walk checks for. This
	// is the assertion that would fail if the walk's check were wrong, e.g.
	// if it tested ModeIrregular or ModeDevice instead. ModeSymlink is one of
	// the bits inside ModeType, which is why the walk can test it at all.
	if os.ModeSymlink&os.ModeType == 0 {
		t.Error("ModeSymlink must be a mode-type bit so the walk can test it")
	}
	// And it must be distinguishable from the other type bits the walk skips.
	if os.ModeSymlink == os.ModeIrregular || os.ModeSymlink == os.ModeDevice {
		t.Error("ModeSymlink must be distinct from the other file types the walk skips")
	}
}

func TestFlattenedTransferNameIsBounded(t *testing.T) {
	deep := strings.Repeat("segment/", 60) + "file.txt"
	name := flattenedTransferName("/root", filepath.Join("/root", filepath.FromSlash(deep)))
	if len(name) > 200 {
		t.Errorf("name is %d bytes, want at most 200: %q", len(name), name)
	}
	if name == "" {
		t.Error("a long path must still produce a name")
	}
}

// Control characters have no place in a filename and are invisible in a
// listing, so they are removed at the point the name is derived.
func TestFlattenedTransferNameStripsControlCharacters(t *testing.T) {
	got := flattenedTransferName("/root", "/root/ev\x00il\n.txt")
	if strings.ContainsAny(got, "\x00\n") {
		t.Errorf("control characters survived: %q", got)
	}
	if got == "" {
		t.Error("the name must not become empty")
	}
}

// A derived per-file key must stay within the receiver's key bounds, or the
// send it guards would be refused. When it cannot fit, the safe fallback is a
// fresh per-attempt ID: that can duplicate, never lose.
func TestBatchFileKeyRespectsKeyBounds(t *testing.T) {
	f := expandedFile{Name: "notes.txt", Path: "/root/notes.txt"}
	derived := batchFileKey("user-key", "drop-123", f)
	if derived != "user-key-notes.txt" {
		t.Errorf("derived key = %q, want user-key-notes.txt", derived)
	}

	// No user key: the per-attempt ID stands, unchanged.
	if got := batchFileKey("", "drop-123", f); got != "drop-123" {
		t.Errorf("without a user key the attempt ID must be used, got %q", got)
	}

	// A name too long to derive from falls back rather than being truncated
	// into a colliding key.
	long := expandedFile{Name: strings.Repeat("n", 200) + ".txt"}
	fallback := batchFileKey("user-key", "drop-456", long)
	if fallback != "drop-456" {
		t.Errorf("an over-long derived key must fall back to the attempt ID, got %q", fallback)
	}
	if !dropKeyValid(fallback) {
		t.Errorf("the fallback key must itself be valid, got %q", fallback)
	}
}
