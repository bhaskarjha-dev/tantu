package hub

import (
	"fmt"
	"testing"
	"time"
)

// Stop must not wait out a probe's handshake to a peer that is not answering.
//
// Produced by starting a Hub with a peer at an unroutable TEST-NET-1 address and
// stopping while a probe's dial is in flight. TEST-NET-1 is unroutable by design,
// so the dial runs to transport.defaultHandshakeTimeout -- ten seconds.
//
// Measured before the fix: **9.5 seconds**, twice. After: under a second.
//
// Z80 made Stop wait on heartbeatWG, which was correct and necessary -- it was
// returning while a probe goroutine was still writing to the peer store, which CI
// found as a file-lock failure on Windows. But it turned an unbounded background
// dial into a bounded wait on that dial, and nobody measured what the wait was
// worth. It was worth nine and a half seconds of a user's terminal doing nothing
// after they asked it to quit.
//
// A duration is the right instrument here, which is the opposite of the rule
// everywhere else in this repository. Those gates avoid durations because a fast
// machine passes them while carrying the defect. This defect *is* slowness: the
// broken path is slow on every machine and the fixed path is fast on every
// machine, so a slow machine makes the defect more visible, never less. The
// threshold sits far from both measurements -- 3s against 9.5s broken and ~0s
// fixed -- rather than between them.
func TestStopDoesNotWaitForAnUnreachablePeersHandshake(t *testing.T) {
	const budget = 3 * time.Second

	h := startHeartbeatHub(t, "lan", 200*time.Millisecond)
	seedBlackholePeer(t, h, "192.0.2.98:9877")

	// Let a probe get into its dial, which is the state that matters. A dial to an
	// unroutable address hangs for defaultHandshakeTimeout, so a probe that has
	// started is still running when Stop arrives.
	time.Sleep(1500 * time.Millisecond)

	start := time.Now()
	if err := h.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
	took := time.Since(start)

	if took > budget {
		t.Errorf("Stop took %v with a peer at an unroutable address, over the budget of %v; "+
			"Stop cancels the heartbeat and then waits for the probe goroutines, so a probe "+
			"whose dial ignores cancellation makes quitting wait out the handshake. Measured "+
			"at 9.5s before the dial was made cancellable", took, budget)
	}
}

// And with several unreachable peers, so the gate cannot pass by cancelling one
// dial out of a pool that happens to be idle.
func TestStopDoesNotWaitForSeveralUnreachablePeers(t *testing.T) {
	const budget = 3 * time.Second

	h := startHeartbeatHub(t, "lan", 200*time.Millisecond)
	// More than the probe pool is wide, so at least one dial is mid-handshake
	// whatever the scheduler does.
	for i := 0; i < 10; i++ {
		seedBlackholePeer(t, h, fmt.Sprintf("192.0.2.%d:9877", i))
	}

	time.Sleep(2 * time.Second)

	start := time.Now()
	if err := h.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
	took := time.Since(start)

	if took > budget {
		t.Errorf("Stop took %v with ten peers at unroutable addresses, over the budget of %v; "+
			"cancelling one dial is not enough -- every probe has to be cancellable, or "+
			"quitting waits for the slowest one", took, budget)
	}
}
