package drop

import (
	"encoding/json"
	"os"
	"path/filepath"
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
