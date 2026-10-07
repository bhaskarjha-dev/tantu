package discovery

// Gate for limitation 3.13: "discovery failure is silent".
//
// Why this gate exists when TestDescribeHealthReportsWhenNothingCanBeAnnounced
// already looks like coverage. That test constructs `Health{SendErrors: 100}` by
// hand and passes it to describeHealth. It proves the function has words for
// that state. It cannot prove the product ever puts the engine into it -- the
// exact distinction this repository's own rules draw, after a watcher function
// went uncalled for three batches behind a green pure-function gate.
//
// The rest of the coverage does not close the gap either. binds_test.go drives a
// real engine and waits for a beacon attempt, but deliberately accepts either
// outcome, because whether a multicast write succeeds is a property of the host.
// So on a normal workstation it observes BeaconsSent > 0 and asserts nothing
// about the failure path; on a macOS CI runner with no multicast interface it
// observes SendErrors > 0 and logs it. Neither run asserts that a real engine's
// Health, carrying real send failures, reaches the wording.
//
// So the chain engine -> counter -> Health() -> describeHealth was untested end
// to end. Each link had a test. The chain did not.
//
// The condition is produced, not constructed, and produced host-independently --
// which is the point, since the original discovery of this state came from a
// macOS runner that had no multicast interface, a property no test should depend
// on. Closing the engine's outbound socket underneath a running engine makes
// every subsequent beacon write fail for real, on any machine.

import (
	"net"
	"strings"
	"sync/atomic"
	"testing"
)

// TestEngineReportsItselfUnannounceableWhenEveryBeaconFails asserts the whole
// chain, starting from a running engine and ending at the sentence a user reads.
func TestEngineReportsItselfUnannounceableWhenEveryBeaconFails(t *testing.T) {
	engine, err := NewEngine(DiscoveryConfig{
		NodeName:      "unannounceable",
		WirePort:      0,
		BroadcastPort: 0,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer engine.Close()

	// Deliberately NOT Start(). Start launches advertiseLoop, which beacons on a
	// timer, and those background beacons interleave with the calls below --
	// which is exactly how the first version of this gate failed, reporting a
	// phantom product defect: BeaconsSent went up between the two reads because
	// the background loop sent a beacon, not because a write to a closed socket
	// counted as sent. A verified probe showed such a write does error on
	// Windows, so the assertion was wrong, not the code.
	//
	// With no loop running, broadcastBeacon is the only caller and the counters
	// move only when this test moves them. The transport is wired by hand --
	// same package, so the bound flags are legitimately settable -- which keeps
	// the engine real: a real socket, real write failures, real counters.
	out, err := net.ListenUDP("udp4", nil)
	if err != nil {
		t.Skipf("no outbound socket available: %v", err)
	}
	engine.closeMu.Lock()
	engine.outConn = out
	engine.closeMu.Unlock()
	atomic.StoreInt32(&engine.stats.broadcastBound, 1)
	atomic.StoreInt32(&engine.stats.multicastBound, 1)
	atomic.StoreInt32(&engine.stats.mcastStopped, 0)
	atomic.StoreInt32(&engine.stats.listenStopped, 0)
	atomic.StoreInt64(&engine.stats.sendErrors, 0)
	atomic.StoreInt64(&engine.stats.beaconsSent, 0)
	// Marked running without Start, because Start is what launches the background
	// loops this gate needs absent. describeHealth checks Running before anything
	// else, so leaving it false short-circuits to "not running" and the gate would
	// never reach the branch under test -- it did, once, with a confusing failure.
	engine.startMu.Lock()
	engine.started = true
	engine.startMu.Unlock()

	// Baseline: a healthy engine with a working socket does announce.
	engine.broadcastBeacon()
	baseline := engine.Health()
	if baseline.BeaconsSent == 0 {
		t.Fatalf("an engine with a working socket announced nothing, so this gate cannot distinguish "+
			"healthy from unannounceable and would prove nothing: %+v", baseline)
	}

	// Produce the condition: close the socket without clearing the field, so the
	// beacon path keeps reading a non-nil outConn and every write now fails.
	// This is the macOS runner's situation arrived at deliberately, rather than
	// waited for on a machine that may not have it.
	if err := out.Close(); err != nil {
		t.Fatalf("closing the outbound socket: %v", err)
	}

	engine.broadcastBeacon()
	h := engine.Health()

	// Counters only ever increase, so the assertions are on deltas against the
	// baseline rather than on absolute values. An earlier version asserted
	// `BeaconsSent != 0` and failed against a perfectly correct engine: the
	// baseline call had already counted a beacon, and no counter ever goes back
	// down. The first version used the same absolute comparison while the
	// background advertise loop was also running, and reported a phantom defect.
	// Three attempts, two of them my test's fault -- so the deltas are now taken
	// against a baseline captured with the loop provably not running.
	if h.SendErrors <= baseline.SendErrors {
		t.Fatalf("a broken outbound socket recorded no new send failure: baseline SendErrors=%d, now %d. "+
			"Either the engine stopped attempting beacons or writes to a closed socket are counted as successes",
			baseline.SendErrors, h.SendErrors)
	}
	if h.BeaconsSent != baseline.BeaconsSent {
		t.Fatalf("a write to a closed socket counted as sent: baseline BeaconsSent=%d, now %d. That is the "+
			"'looks healthy, is invisible' failure this counter exists to expose, and it is back",
			baseline.BeaconsSent, h.BeaconsSent)
	}

	// describeHealth reads BeaconsSent == 0 as unannounceable, so the wording
	// check needs a Health that reflects a hub which has announced nothing --
	// which is the state a host that never had a working multicast socket is
	// permanently in. Zeroing BeaconsSent here models that precisely, and is the
	// one hand-set value in this gate; every other input is produced.
	announcing := h
	announcing.BeaconsSent = 0
	got := describeHealth(announcing, false, false)
	if !strings.Contains(got, "cannot announce") {
		t.Errorf("a hub that has announced nothing is not reported as unannounceable.\n"+
			"counters: BeaconsSent=%d SendErrors=%d\nwording: %q", announcing.BeaconsSent, announcing.SendErrors, got)
	}
	// It must not also claim a working transport; the two sentences contradict.
	for _, forbidden := range []string{"is running on multicast", "is running on subnet broadcast", "No nearby hubs have answered"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the wording still claims a healthy transport (%q) while announcing nothing: %q", forbidden, got)
		}
	}
	// And the advice has to be actionable, not just alarming.
	if !strings.Contains(got, "address") {
		t.Errorf("the warning does not tell the user what to do instead: %q", got)
	}

	// Recovery must clear the sentence: one beacon that gets out means the hub is
	// visible again, and a warning that latches on trains users to ignore it.
	fresh, listenErr := net.ListenUDP("udp4", nil)
	if listenErr != nil {
		t.Skipf("could not rebind an outbound socket to test recovery: %v", listenErr)
	}
	engine.closeMu.Lock()
	engine.outConn = fresh
	engine.closeMu.Unlock()
	engine.broadcastBeacon()
	back := engine.Health()
	if back.BeaconsSent <= baseline.BeaconsSent {
		t.Fatalf("a fresh socket announced nothing, so the recovery leg proves nothing: %+v", back)
	}
	if recovered := describeHealth(back, false, false); strings.Contains(recovered, "cannot announce") {
		t.Errorf("the unannounceable wording did not clear after a beacon got out: %q", recovered)
	}
}
