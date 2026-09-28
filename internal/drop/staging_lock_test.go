package drop

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The staging process lock used to be stealable: its directory mtime was set at
// Mkdir and never refreshed, so a holder doing legitimate work longer than the
// stale window had its lock removed, and the original owner's release then
// removed the thief's lock. A heartbeat distinguishes "still working" from
// "crashed" instead of guessing from a fixed timeout.
func TestStagingProcessLockIsNotStolenFromALiveHolder(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")

	unlock, err := acquireStagingProcessLock(lockPath)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Simulate a holder that has been working longer than the stale window
	// without releasing.
	old := time.Now().Add(-2 * stagingProcessLockStale)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("age the lock: %v", err)
	}

	// A concurrent waiter must keep waiting rather than stealing the lock, and
	// must give up with an error instead of proceeding unlocked.
	if _, err := acquireStagingProcessLock(lockPath); err == nil {
		unlock()
		t.Fatal("a live holder's lock was taken by a concurrent waiter")
	}

	// The original release must still remove the lock it owns.
	unlock()
	if _, err := os.Lstat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("the lock survived its owner's release: %v", err)
	}
}

// After a holder actually dies, a later process must be able to reclaim the
// lock, or a crash would disable staging maintenance forever.
func TestStagingProcessLockIsReclaimedAfterAStaleDeath(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")

	dead := deadPID(t)
	// Simulate a crashed holder: the lock exists and is old, and the recorded
	// owner is a pid that cannot be running.
	if err := os.Mkdir(lockPath, 0700); err != nil {
		t.Fatalf("create stale lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockPath, stagingProcessLockHeartbeat),
		[]byte(strconv.Itoa(dead)), 0600); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
	old := time.Now().Add(-2 * stagingProcessLockStale)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("age the stale lock: %v", err)
	}
	if lockHolderAlive(dead) {
		t.Fatalf("pid %d unexpectedly reported as running", dead)
	}

	// A lock whose owner is gone must be reclaimable, or a crash wedges staging
	// maintenance for the life of the process.
	unlock, err := acquireStagingProcessLock(lockPath)
	if err != nil {
		t.Fatalf("a lock abandoned by a dead process must be reclaimable: %v", err)
	}
	unlock()
}

// deadPID is a pid that is not running. It is discovered rather than hardcoded
// so the test does not depend on a particular pid being unused.
func deadPID(t *testing.T) int {
	t.Helper()
	for candidate := 4194300; candidate > 1000; candidate-- {
		if !lockHolderAlive(candidate) {
			return candidate
		}
	}
	t.Skip("could not find an unused pid on this system")
	return 0
}

// A maintenance pass that cannot take the lock used to return (0, 0), which the
// caller logged only when files were removed. That made "reclamation is
// switched off" indistinguishable from "there was nothing to reclaim", which is
// exactly the condition that fills a user's disk.
func TestMaintenanceFailureIsObservable(t *testing.T) {
	// Start from a clean slate.
	recordMaintenanceError(nil)
	if err := LastMaintenanceError(); err != nil {
		t.Fatalf("expected no maintenance error at the start, got %v", err)
	}

	// Hold the output-directory maintenance lock so the sweep must fail.
	dir := t.TempDir()
	lockPath := stagingProcessLockPathForOutput(dir)
	unlock, err := acquireStagingProcessLock(lockPath)
	if err != nil {
		t.Fatalf("hold the maintenance lock: %v", err)
	}
	defer unlock()

	removed, freed := SweepStalePartials(dir, time.Hour)
	if removed != 0 || freed != 0 {
		t.Fatalf("a blocked sweep must not claim to have reclaimed anything, got removed=%d freed=%d", removed, freed)
	}
	if err := LastMaintenanceError(); err == nil {
		t.Fatal("a sweep that could not take the maintenance lock must report why")
	}
}
