package drop

// The other half of TestMaintenanceFailureIsObservable.
//
// That test asserts a failed sweep reports why. It cannot see the other half:
// the error was never cleared, so a sweep that ran perfectly left the
// previous sweep's failure behind, and `LastMaintenanceError`'s own contract
// -- "nil if it ran or never attempted" -- was false.
//
// The product does not read the error after a sweep (it reads it after the
// budget pass, which clears it), so this never reached a user. It is an
// exported contract, and an exported contract that is wrong is a trap for the
// next caller. See docs/KNOWN-LIMITATIONS.md row 3.27.

import (
	"testing"
	"time"
)

func TestAMaintenanceFailureIsNotSticky(t *testing.T) {
	// Start from a clean slate, so an earlier test's failure cannot be
	// mistaken for this one's.
	recordMaintenanceError(nil)
	if err := LastMaintenanceError(); err != nil {
		t.Fatalf("expected no maintenance error at the start, got %v", err)
	}

	dir := t.TempDir()
	unlock, err := acquireStagingProcessLock(stagingProcessLockPathForOutput(dir))
	if err != nil {
		t.Fatalf("hold the maintenance lock: %v", err)
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()

	// A sweep that cannot take the lock fails, and says so.
	SweepStalePartials(dir, time.Hour)
	if err := LastMaintenanceError(); err == nil {
		t.Fatal("a sweep that could not take the maintenance lock must report why")
	}

	// The lock is released, and the next sweep runs.
	unlock()
	locked = false
	SweepStalePartials(dir, time.Hour)
	if err := LastMaintenanceError(); err != nil {
		t.Fatalf("a sweep that ran left the previous sweep's failure behind: %v -- "+
			"LastMaintenanceError describes the most recent attempt, and this was one", err)
	}
}
