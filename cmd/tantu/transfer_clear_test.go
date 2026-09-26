package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
)

func TestClearSavedRelayHistoryFile(t *testing.T) {
	dir := t.TempDir()
	data := `{"version":1,"authorizations":[{"id":"rl-1","state":"complete","created_at":"2026-09-25T00:00:00Z","updated_at":"2026-09-25T00:00:01Z"}]}`
	if err := os.WriteFile(filepath.Join(dir, hub.RelayHistoryFilename), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	removed, err := clearSavedRelayHistoryFile(dir)
	if err != nil || !removed {
		t.Fatalf("clear = %v, %v; want true, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, hub.RelayHistoryFilename)); !os.IsNotExist(err) {
		t.Fatal("history file must be gone after clear")
	}
	removed, err = clearSavedRelayHistoryFile(dir)
	if err != nil || removed {
		t.Fatalf("second clear = %v, %v; want false, nil", removed, err)
	}
}
