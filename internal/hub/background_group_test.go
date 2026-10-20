package hub

// A gate about a gate.
//
// Every background task in this package must take its slot in the group
// `Stop()` waits for through `beginBackgroundWork`, which refuses once the Hub
// is stopping. A direct `heartbeatWG.Add` anywhere else can land AFTER
// `Stop()` has called `Wait()` -- and `sync.WaitGroup` says, in as many
// words:
//
//	Note that calls with a positive delta that occur when the counter is
//	zero must happen before a Wait.
//
// A task counted too late is a task `Stop()` does not wait for, and a task
// that writes to the peer store is exactly what `Stop()` promises not to
// leave running. That is not a hypothetical: the heartbeat's probes were
// Add-ed from inside the scheduler's tick, and a probe wrote to the store
// while a test wiped it, which is the Windows cleanup failure of 2026-10-20.
//
// The defect was readable here the whole time -- in the field's own comment,
// which claimed a guarantee the code did not keep -- and nothing checked it.
// This checks it now.
//
// It counts the Adds in the package's own source rather than trusting a
// reader to notice a fourth one, because the whole point is that a reader
// does not notice.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// hubSourceDir returns the directory holding this package's source.
func hubSourceDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this package's source")
	}
	return filepath.Dir(file)
}

// TestTheBackgroundGroupHasExactlyOneDoor is the gate.
//
// The group is added to in exactly one place, and that place is the helper
// that refuses once the Hub is stopping.
func TestTheBackgroundGroupHasExactlyOneDoor(t *testing.T) {
	dir := hubSourceDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}

	const marker = "heartbeatWG.Add("
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, marker) {
				continue
			}
			total++
			if e.Name() != "hub.go" {
				t.Errorf("%s:%d adds to the background group directly: %s", e.Name(), i+1, strings.TrimSpace(line))
			}
		}
	}
	if total != 1 {
		t.Errorf("the background group is added to in %d place(s), want exactly 1: every task must "+
			"take its slot through beginBackgroundWork, which refuses once the Hub is stopping, "+
			"because a task counted after Stop begins waiting is a task Stop does not wait for",
			total)
	}
}
