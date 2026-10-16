package hub

import (
	"errors"
	"testing"
	"time"
)

// Stop must report whether the run loop actually finished, and the two answers
// have to be distinguishable.
//
// The select this replaced returned the same error from both branches, so the
// path where force-closing the listeners worked -- the recovery, the whole point
// of the fallback -- was reported as the path where the Hub failed to stop.
//
// A pure decision function, gated as one. The input *is* the whole domain here:
// whether the run loop finished, and by when, relative to the grace period.
// There is no product state to stand up, and producing a run loop that is still
// going after five seconds would mean building a listener that ignores Close --
// so this gate is honest about being a decision gate and not a behaviour gate.
//
// What it cannot cover, stated: that Stop actually reaches this call on the
// timeout path at all. That is checked by reading the two lines in Stop, which
// call it with hubStopForceGrace.
func TestStopReportsWhetherTheRunLoopFinished(t *testing.T) {
	t.Run("finished before Stop asked", func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		if err := waitForRunCompletion(done, time.Second); err != nil {
			t.Errorf("waitForRunCompletion reported %v for a run loop that had already "+
				"finished; the recovery path -- force-closing the listeners works -- is "+
				"reported as the failure path", err)
		}
	})

	t.Run("finished inside the grace period", func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			time.Sleep(30 * time.Millisecond)
			close(done)
		}()
		if err := waitForRunCompletion(done, 5*time.Second); err != nil {
			t.Errorf("waitForRunCompletion reported %v for a run loop that finished 30ms "+
				"into a 5s grace period", err)
		}
	})

	t.Run("never finished", func(t *testing.T) {
		done := make(chan struct{})
		defer close(done)
		err := waitForRunCompletion(done, 50*time.Millisecond)
		if err == nil {
			t.Fatal("waitForRunCompletion reported success for a run loop that never " +
				"finished; Stop would return as though the Hub stopped while its serve " +
				"loops were still running")
		}
		if !errors.Is(err, errHubStopRunTimeout) {
			t.Errorf("waitForRunCompletion reported %v, which is not the timeout error, so a "+
				"caller cannot tell a shutdown failure from any other failure", err)
		}
	})

	t.Run("no run loop at all", func(t *testing.T) {
		// Defensive: a nil channel blocks forever, so this must not become an
		// unbounded wait.
		if err := waitForRunCompletion(nil, 50*time.Millisecond); !errors.Is(err, errHubStopRunTimeout) {
			t.Errorf("waitForRunCompletion(nil) = %v; a nil run loop can never complete, so "+
				"this must be the timeout rather than an unbounded wait", err)
		}
	})
}

// The two answers must actually differ, which is the defect stated as its
// smallest possible form: one error value cannot mean both "it stopped" and "it
// did not stop".
func TestTheTwoStopOutcomesAreDistinguishable(t *testing.T) {
	done := make(chan struct{})
	close(done)
	if err := waitForRunCompletion(done, time.Second); err != nil {
		t.Errorf("a finished run loop reports %v rather than success, which is the "+
			"misreporting this batch is about", err)
	}

	never := make(chan struct{})
	defer close(never)
	if err := waitForRunCompletion(never, 50*time.Millisecond); !errors.Is(err, errHubStopRunTimeout) {
		t.Errorf("an unfinished run loop reports %v rather than %v", err, errHubStopRunTimeout)
	}
}
