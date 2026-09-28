package drop

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublicationTempNamesAreRecognized(t *testing.T) {
	const validHex = "0123456789abcdef01234567"
	if !isPublicationTempName(publicationTempPrefix + validHex + publicationTempSuffix) {
		t.Error("a generated publication temp name must be recognized")
	}
	// Anything that is not exactly the generated shape must be treated as a
	// delivered file so a sweep can never remove user data.
	for _, name := range []string{
		"holiday.zip",
		"holiday (1).zip",
		publicationTempPrefix + "me.zip",
		publicationTempPrefix + validHex,                               // no suffix
		publicationTempPrefix + validHex + ".partial",                  // wrong suffix
		publicationTempPrefix + "nothex-but-long-enough-0000" + ".tmp", // non-hex
		".tantu-publish-" + validHex + ".tmp.txt",                      // suffix after .tmp
		"notes.part",
	} {
		if isPublicationTempName(name) {
			t.Errorf("%q must not be treated as a publication temporary", name)
		}
	}
}

func TestNewPublicationTempNameIsUniqueAndHidden(t *testing.T) {
	dir := t.TempDir()
	sd, err := OpenStagingDirectory(dir)
	if err != nil {
		t.Fatalf("open staging directory: %v", err)
	}
	defer sd.Close()

	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		name, err := sd.NewPublicationTempName()
		if err != nil {
			t.Fatalf("NewPublicationTempName: %v", err)
		}
		if seen[name] {
			t.Fatalf("duplicate publication temp name %q", name)
		}
		seen[name] = true
		if !isPublicationTempName(name) {
			t.Fatalf("generated name %q is not recognizable as a temporary", name)
		}
		if len(filepath.Base(name)) <= len(publicationTempPrefix) {
			t.Fatalf("generated name %q is too short to be unique", name)
		}
	}
}

func TestPublishStagedFileIsAtomicAndVisibleOnlyWhenComplete(t *testing.T) {
	dir := t.TempDir()
	sd, err := OpenStagingDirectory(dir)
	if err != nil {
		t.Fatalf("open staging directory: %v", err)
	}
	defer sd.Close()

	temp, err := sd.NewPublicationTempName()
	if err != nil {
		t.Fatal(err)
	}
	f, err := sd.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("partial content")); err != nil {
		t.Fatal(err)
	}

	// While the temporary is being filled, the delivered name must not exist.
	if _, err := os.Lstat(filepath.Join(dir, "holiday.zip")); !os.IsNotExist(err) {
		t.Fatalf("delivered name must not exist before the rename, stat err = %v", err)
	}

	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sd.PublishStagedFile(temp, "holiday.zip"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "holiday.zip"))
	if err != nil || string(got) != "partial content" {
		t.Fatalf("published content mismatch: %q err=%v", got, err)
	}
	if _, err := sd.Lstat(temp); !os.IsNotExist(err) {
		t.Fatalf("temporary still present after publication: %v", err)
	}
}

func TestSweepPublicationTempsReclaimsOnlyStaleTemporaries(t *testing.T) {
	dir := t.TempDir()
	sd, err := OpenStagingDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer sd.Close()

	stale, err := sd.NewPublicationTempName()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sd.OpenFile(stale, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600); err != nil {
		t.Fatal(err)
	}
	fresh, err := sd.NewPublicationTempName()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sd.OpenFile(fresh, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600); err != nil {
		t.Fatal(err)
	}
	// A delivered file that shares the prefix shape must survive the sweep.
	if _, err := sd.OpenFile("holiday.zip", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600); err != nil {
		t.Fatal(err)
	}

	// Age only the stale temporary.
	old := time.Now().Add(-48 * time.Hour)
	if err := sd.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	n, err := sd.SweepPublicationTemps(24 * time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d temporaries, want 1", n)
	}
	if _, err := sd.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temporary should be gone, stat err = %v", err)
	}
	if _, err := sd.Lstat(fresh); err != nil {
		t.Errorf("fresh temporary must be retained: %v", err)
	}
	if _, err := sd.Lstat("holiday.zip"); err != nil {
		t.Errorf("a delivered file must never be swept: %v", err)
	}
}

// Publication by rename must never overwrite an existing delivered file.
func TestPublishFromStagingRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, StagingDirName, "a.part")
	if err := os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte("incoming"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	sd, err := OpenStagingDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer sd.Close()

	ok, err := sd.PublishFromStaging(part, "a.txt")
	if ok || !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected an existence refusal, got ok=%v err=%v", ok, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(got) != "existing" {
		t.Fatalf("existing file was modified: %q err=%v", got, err)
	}
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("staging partial should be untouched: %v", err)
	}
}

func TestPublishFromStagingRenamesInPlace(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, StagingDirName, "b.part")
	if err := os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("streamed bytes")
	if err := os.WriteFile(part, payload, 0600); err != nil {
		t.Fatal(err)
	}
	sd, err := OpenStagingDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer sd.Close()

	ok, err := sd.PublishFromStaging(part, "b.txt")
	if !ok || err != nil {
		t.Fatalf("rename publication failed: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("staging name should have been consumed, stat err = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "b.txt"))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("published content mismatch: %q err=%v", got, err)
	}
}

func TestSweepPublicationTempsInIgnoresNonDirectories(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := SweepPublicationTempsIn(file, time.Hour); err != nil || n != 0 {
		t.Fatalf("sweep of a non-directory must be a no-op, got n=%d err=%v", n, err)
	}
}
