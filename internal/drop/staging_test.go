package drop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSweepStalePartials(t *testing.T) {
	outDir := t.TempDir()
	staging := filepath.Join(outDir, StagingDirName)
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	oldPart := filepath.Join(staging, "old.part")
	if err := os.WriteFile(oldPart, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldPart, aged, aged); err != nil {
		t.Fatal(err)
	}
	freshPart := filepath.Join(staging, "fresh.part")
	if err := os.WriteFile(freshPart, []byte("live"), 0600); err != nil {
		t.Fatal(err)
	}
	// Non-.part files and subdirectories are never touched.
	keepFile := filepath.Join(staging, "notes.txt")
	if err := os.WriteFile(keepFile, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	removed, freed := SweepStalePartials(outDir, 24*time.Hour)
	if removed != 1 || freed != int64(len("stale")) {
		t.Fatalf("sweep = (%d, %d), want (1, 5)", removed, freed)
	}
	if _, err := os.Stat(oldPart); !os.IsNotExist(err) {
		t.Fatal("stale partial not removed")
	}
	if _, err := os.Stat(freshPart); err != nil {
		t.Fatal("fresh partial wrongly removed")
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Fatal("non-part file wrongly removed")
	}
	// Missing staging dir is a no-op.
	if removed, _ := SweepStalePartials(t.TempDir(), 0); removed != 0 {
		t.Fatal("sweep of missing dir removed files")
	}
}

func TestSweepStalePartialsIncludesMarkedLegacyLayout(t *testing.T) {
	outDir := t.TempDir()
	oldLegacy := filepath.Join(outDir, "download.bin.part-3")
	freshLegacy := filepath.Join(outDir, "download.bin.part-4")
	if err := os.WriteFile(oldLegacy, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteStagingManifest(oldLegacy, DropSend{
		DropID: "marked-legacy",
		Kind:   DropKindFile,
		Name:   "download.bin",
		Size:   3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(freshLegacy, []byte("fresh"), 0600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldLegacy, aged, aged); err != nil {
		t.Fatal(err)
	}
	removed, freed := SweepStalePartials(outDir, 24*time.Hour)
	if removed != 1 || freed != 3 {
		t.Fatalf("marked legacy sweep = (%d, %d), want (1, 3)", removed, freed)
	}
	if _, err := os.Stat(oldLegacy); !os.IsNotExist(err) {
		t.Fatal("stale marked legacy partial not removed")
	}
	if _, err := os.Stat(StagingManifestPath(oldLegacy)); !os.IsNotExist(err) {
		t.Fatal("stale marked legacy manifest not removed")
	}
	if _, err := os.Stat(freshLegacy); err != nil {
		t.Fatal("fresh legacy partial wrongly removed")
	}
}

func TestUnmarkedLegacyPartialsArePreserved(t *testing.T) {
	outDir := t.TempDir()
	paths := []string{
		filepath.Join(outDir, "notes.part"),
		filepath.Join(outDir, "archive.part-12"),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("user data"), 0600); err != nil {
			t.Fatal(err)
		}
		aged := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(path, aged, aged); err != nil {
			t.Fatal(err)
		}
	}
	if removed, _ := SweepStalePartials(outDir, 24*time.Hour); removed != 0 {
		t.Fatalf("sweep removed %d unmarked legacy file(s)", removed)
	}
	if removed, _ := EnforcePartialBudget(outDir, 1); removed != 0 {
		t.Fatalf("budget removed %d unmarked legacy file(s)", removed)
	}
	for _, path := range paths {
		if got, err := os.ReadFile(path); err != nil || string(got) != "user data" {
			t.Fatalf("unmarked legacy file %q changed: %q, %v", path, got, err)
		}
	}
}

func TestSweepStalePartialsIncludesNumberedPrivateStaging(t *testing.T) {
	outDir := t.TempDir()
	staging := filepath.Join(outDir, StagingDirName)
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}

	oldPart := filepath.Join(staging, "download.bin.part-7")
	if err := os.WriteFile(oldPart, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteStagingManifest(oldPart, DropSend{
		DropID: "numbered-stale",
		Kind:   DropKindFile,
		Name:   "download.bin",
		Size:   3,
	}); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldPart, aged, aged); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(StagingManifestPath(oldPart), aged, aged); err != nil {
		t.Fatal(err)
	}

	freshPart := filepath.Join(staging, "fresh.bin.part-2")
	if err := os.WriteFile(freshPart, []byte("fresh"), 0600); err != nil {
		t.Fatal(err)
	}
	removed, freed := SweepStalePartials(outDir, 24*time.Hour)
	if removed != 1 || freed != 3 {
		t.Fatalf("numbered staging sweep = (%d, %d), want (1, 3)", removed, freed)
	}
	if _, err := os.Stat(oldPart); !os.IsNotExist(err) {
		t.Fatal("stale numbered private partial was not removed")
	}
	if _, err := os.Stat(StagingManifestPath(oldPart)); !os.IsNotExist(err) {
		t.Fatal("stale numbered private manifest was not removed")
	}
	if _, err := os.Stat(freshPart); err != nil {
		t.Fatal("fresh numbered private partial was removed")
	}
}

func TestEnforcePartialBudgetIncludesNumberedPrivateStaging(t *testing.T) {
	outDir := t.TempDir()
	staging := filepath.Join(outDir, StagingDirName)
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	oldPart := filepath.Join(staging, "old.part-1")
	newPart := filepath.Join(staging, "new.part-2")
	for _, path := range []string{oldPart, newPart} {
		if err := os.WriteFile(path, []byte("1234"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldPart, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPart, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	removed, freed := EnforcePartialBudget(outDir, 4)
	if removed != 1 || freed != 4 {
		t.Fatalf("numbered private budget trim = (%d, %d), want (1, 4)", removed, freed)
	}
	if _, err := os.Stat(oldPart); !os.IsNotExist(err) {
		t.Fatal("oldest numbered private partial was not trimmed")
	}
	if _, err := os.Stat(newPart); err != nil {
		t.Fatal("newest numbered private partial was removed")
	}
}

func TestStagingDirectoryRootStaysAnchoredAfterRename(t *testing.T) {
	outDir := t.TempDir()
	stagingPath := filepath.Join(outDir, StagingDirName)
	if err := os.MkdirAll(stagingPath, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := OpenStagingDirectory(stagingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	oldPath := filepath.Join(outDir, StagingDirName+".old")
	if err := os.Rename(stagingPath, oldPath); err != nil {
		t.Skipf("cannot replace an open staging directory on this platform: %v", err)
	}
	replacement := filepath.Join(outDir, StagingDirName)
	if err := os.Mkdir(replacement, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile("anchored.part", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open through anchored root: %v", err)
	}
	_ = file.Close()
	if _, err := os.Stat(filepath.Join(replacement, "anchored.part")); !os.IsNotExist(err) {
		t.Fatalf("anchored write escaped to replacement directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldPath, "anchored.part")); err != nil {
		t.Fatalf("anchored write missing from original directory: %v", err)
	}
}

func TestStagingMaintenanceDoesNotFollowSymlink(t *testing.T) {
	outDir := t.TempDir()
	victimDir := t.TempDir()
	victimPart := filepath.Join(victimDir, "outside.part")
	if err := os.WriteFile(victimPart, []byte("do not delete"), 0600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(victimPart, aged, aged); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(outDir, StagingDirName)
	if err := os.Symlink(victimDir, staging); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	if removed, _ := SweepStalePartials(outDir, 24*time.Hour); removed != 0 {
		t.Fatalf("sweep followed staging symlink and removed %d file(s)", removed)
	}
	if removed, _ := EnforcePartialBudget(outDir, 1); removed != 0 {
		t.Fatalf("budget followed staging symlink and removed %d file(s)", removed)
	}
	if got, err := os.ReadFile(victimPart); err != nil || string(got) != "do not delete" {
		t.Fatalf("victim outside output directory was modified: %q, %v", got, err)
	}
}

func TestStagingActivityProtectsPartialFromMaintenance(t *testing.T) {
	outDir := t.TempDir()
	staging := filepath.Join(outDir, StagingDirName)
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	partPath := filepath.Join(staging, "active.part")
	if err := os.WriteFile(partPath, []byte("active payload"), 0600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(partPath, aged, aged); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireStagingActivity(partPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireStagingActivity(partPath); !errors.Is(err, ErrStagingPartActive) {
		t.Fatalf("second activity acquisition error = %v, want active error", err)
	}
	if err := TouchStagingActivity(partPath); err != nil {
		t.Fatalf("touch activity marker: %v", err)
	}
	if removed, _ := SweepStalePartials(outDir, 24*time.Hour); removed != 0 {
		t.Fatalf("sweep removed active partial (%d)", removed)
	}
	if removed, _ := EnforcePartialBudget(outDir, 1); removed != 0 {
		t.Fatalf("budget removed active partial (%d)", removed)
	}
	release()
	if removed, _ := EnforcePartialBudget(outDir, 1); removed != 1 {
		t.Fatalf("released partial was not trimmed, removed %d", removed)
	}
}

func TestStagingActivityReleaseCannotRemoveReplacementMarker(t *testing.T) {
	outDir := t.TempDir()
	staging := filepath.Join(outDir, StagingDirName)
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	partPath := filepath.Join(staging, "owner.part")
	release, err := AcquireStagingActivity(partPath)
	if err != nil {
		t.Fatal(err)
	}
	marker := partPath + stagingActivitySuffix
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("old owner removed replacement marker: %v", err)
	}
}

func TestEnforcePartialBudgetTrimsOldestAndProtectsActive(t *testing.T) {
	outDir := t.TempDir()
	staging := filepath.Join(outDir, StagingDirName)
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(staging, "one.part"),
		filepath.Join(staging, "two.part"),
		filepath.Join(staging, "three.part"),
	}
	for i, path := range paths {
		if err := os.WriteFile(path, []byte("1234"), 0600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(time.Duration(i-2) * time.Hour)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	removed, freed := EnforcePartialBudget(outDir, 8)
	if removed != 1 || freed != 4 {
		t.Fatalf("budget trim = (%d, %d), want (1, 4)", removed, freed)
	}
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatal("oldest partial was not trimmed")
	}

	active := filepath.Join(staging, "active.part")
	if err := os.WriteFile(active, []byte("123456"), 0600); err != nil {
		t.Fatal(err)
	}
	removed, freed = EnforcePartialBudget(outDir, 10, active)
	if removed != 1 || freed != 4 {
		t.Fatalf("protected budget trim = (%d, %d), want (1, 4)", removed, freed)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatal("protected active partial was removed")
	}
}

func TestStagingManifestBindsResumeMetadata(t *testing.T) {
	dir := t.TempDir()
	partPath := filepath.Join(dir, "transfer.part")
	if err := os.WriteFile(partPath, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	meta := DropSend{
		DropID:    "manifest-drop",
		Kind:      DropKindFile,
		Name:      "payload.bin",
		Size:      7,
		ChunkSize: DefaultChunkSize,
		HeadHash:  strings.Repeat("a", 64),
	}
	if err := WriteStagingManifest(partPath, meta); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadStagingManifest(partPath)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Matches(meta) {
		t.Fatal("stored manifest did not match original metadata")
	}
	meta.Name = "different.bin"
	if manifest.Matches(meta) {
		t.Fatal("manifest matched metadata with a different filename")
	}

	if err := os.Remove(partPath); err != nil {
		t.Fatal(err)
	}
	if err := RemoveStagingManifest(partPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStagingManifest(partPath); !os.IsNotExist(err) {
		t.Fatalf("removed manifest still readable: %v", err)
	}
}

func TestSweepPreservesDirectPartialWithInvalidManifest(t *testing.T) {
	outDir := t.TempDir()
	partPath := filepath.Join(outDir, "important.part")
	if err := os.WriteFile(partPath, []byte("user data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StagingManifestPath(partPath), []byte(`{"drop_id":"../bad","kind":"file","chunk_size":1,"created_at":"2026-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(partPath, aged, aged)
	_ = os.Chtimes(StagingManifestPath(partPath), aged, aged)
	if removed, _ := SweepStalePartials(outDir, 24*time.Hour); removed != 0 {
		t.Fatalf("invalid manifest caused %d file removals", removed)
	}
	if removed, _ := EnforcePartialBudget(outDir, 1); removed != 0 {
		t.Fatalf("invalid manifest caused %d budget removals", removed)
	}
	if got, err := os.ReadFile(partPath); err != nil || string(got) != "user data" {
		t.Fatalf("direct user partial changed: %q, %v", got, err)
	}
}

func TestSweepStalePartialsRemovesManifestSidecar(t *testing.T) {
	outDir := t.TempDir()
	staging := filepath.Join(outDir, StagingDirName)
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	partPath := filepath.Join(staging, "orphan.part")
	if err := os.WriteFile(partPath, []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteStagingManifest(partPath, DropSend{DropID: "orphan", Kind: DropKindFile, Name: "orphan.bin", Size: 6}); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(partPath, aged, aged); err != nil {
		t.Fatal(err)
	}
	manifestPath := StagingManifestPath(partPath)
	if err := os.Chtimes(manifestPath, aged, aged); err != nil {
		t.Fatal(err)
	}
	if removed, _ := SweepStalePartials(outDir, 24*time.Hour); removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("stale manifest remains: %v", err)
	}
}

func TestSweepStalePartialsReclaimsOrphanMarkersAndManifestTemps(t *testing.T) {
	outDir := t.TempDir()
	staging := filepath.Join(outDir, StagingDirName)
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(staging, "orphan.part.active")
	tempManifest := filepath.Join(staging, ".orphan.part.meta.tmp-123")
	for _, path := range []string{marker, tempManifest} {
		if err := os.WriteFile(path, []byte("artifact"), 0600); err != nil {
			t.Fatal(err)
		}
		aged := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(path, aged, aged); err != nil {
			t.Fatal(err)
		}
	}
	removed, freed := SweepStalePartials(outDir, 24*time.Hour)
	if removed != 2 || freed != int64(len("artifact")*2) {
		t.Fatalf("artifact sweep = (%d, %d), want (2, %d)", removed, freed, len("artifact")*2)
	}
	for _, path := range []string{marker, tempManifest} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("orphan artifact %q remains: %v", path, err)
		}
	}
}

func TestEnforcePartialBudgetCountsOrphanArtifacts(t *testing.T) {
	outDir := t.TempDir()
	staging := filepath.Join(outDir, StagingDirName)
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= DefaultRetainedPartialCount; i++ {
		path := filepath.Join(staging, "artifact-"+strconv.Itoa(i)+".part.active")
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		aged := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(path, aged, aged); err != nil {
			t.Fatal(err)
		}
	}
	if removed, _ := EnforcePartialBudget(outDir, DefaultRetainedPartialBudget); removed != 1 {
		t.Fatalf("artifact count-budget trim removed %d files, want 1", removed)
	}
}

func TestEnforcePartialBudgetBoundsPartialCount(t *testing.T) {
	outDir := t.TempDir()
	staging := filepath.Join(outDir, StagingDirName)
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= DefaultRetainedPartialCount; i++ {
		path := filepath.Join(staging, "partial-"+strconv.Itoa(i)+".part")
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	removed, _ := EnforcePartialBudget(outDir, DefaultRetainedPartialBudget)
	if removed != 1 {
		t.Fatalf("count-budget trim removed %d partials, want 1", removed)
	}
}

func FuzzValidateDropMetadata(f *testing.F) {
	seeds := []string{
		`{"drop_id":"abc","kind":"text","name":"n","size":10,"chunk_size":1048576}`,
		`{}`,
		`{"drop_id":"..","kind":"file"}`,
		`not json`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var meta DropSend
		if err := json.Unmarshal(data, &meta); err != nil {
			return
		}
		_ = validateDropMetadata(meta)
	})
}
