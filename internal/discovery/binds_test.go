package discovery

// Gates for best-effort binds.
//
// Before this change a failed subnet-broadcast bind aborted Start outright, and
// because broadcast was bound first that also prevented mDNS from ever being
// attempted. The engine's whole vocabulary for a partly-working network --
// Health.Degraded, "discovery cannot reach this network", the Hub's
// degraded/recovered log transitions -- described states the product could not
// enter. Every test for them drove a Health literal directly, so they were green
// while the states were unreachable.
//
// These gates produce the condition for real by occupying the port, so the
// failure is something the operating system rejects rather than something a
// test asserts.

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// occupyUDP binds a UDP socket and returns it plus a closer, so the test holds
// the port for as long as it needs to.
func occupyUDP(t *testing.T, port int) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		t.Skipf("cannot occupy UDP %d on this host: %v", port, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestEngine_BroadcastPortTakenStillStartsAndUsesMulticast is the load-bearing
// gate for the asymmetry this change removes.
//
// It occupies the broadcast port, so the engine's bind genuinely fails, and then
// asserts the three things that were previously impossible: Start succeeds, the
// engine keeps running, and it advertises over mDNS rather than giving up. That
// last one is what makes the hub still discoverable -- the engine beacons to
// 224.0.0.251:5353 as well as to the subnet broadcast address, so one transport
// being unavailable was never a reason to lose both.
func TestEngine_BroadcastPortTakenStillStartsAndUsesMulticast(t *testing.T) {
	const port = 19905
	blocker := occupyUDP(t, port)

	engine, err := NewEngine(DiscoveryConfig{
		NodeName: "taken-broadcast", WirePort: 9877, BroadcastPort: port, Interval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer engine.Close()

	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("a taken broadcast port must not stop discovery starting: %v", err)
	}

	health := engine.Health()
	if !health.Running {
		t.Fatal("engine does not report running after a best-effort bind failed")
	}
	if health.Broadcast {
		t.Error("engine claims the broadcast socket is bound while another process holds that port")
	}
	// Health must name the condition and not just report a quieter engine.
	if !strings.Contains(health.LastError, "subnet broadcast") {
		t.Errorf("LastError does not say which transport failed: %q", health.LastError)
	}
	if !strings.Contains(health.LastError, "19905") {
		t.Errorf("LastError does not name the port it could not open: %q", health.LastError)
	}

	// The engine must ATTEMPT to beacon over the transport that is open. Which
	// of two outcomes is correct depends on the host, not on the product:
	//
	//   - BeaconsSent > 0: the host accepted a multicast write. Normal on a
	//     workstation with a real interface.
	//   - SendErrors > 0: the engine tried and the host refused. This is what a
	//     macOS CI runner does, which has no multicast-capable interface. The
	//     engine is behaving correctly and the network is not.
	//
	// The first version of this gate accepted only the first and failed on
	// macOS in CI. That was the gate's error, not a regression: it asserted a
	// property of the machine rather than a property of the code. What matters
	// is that a beacon is attempted, and that it is attempted only over a
	// transport that is actually open.
	//
	// The second half is the real invariant. broadcastBeacon gates every send on
	// the bound flag, so with Broadcast=false no write to 9879 happens at all;
	// any SendErrors recorded here therefore came from the mDNS write, which is
	// direct evidence the open transport was used and the closed one skipped.
	deadline := time.Now().Add(5 * time.Second)
	var h Health
	for time.Now().Before(deadline) {
		h = engine.Health()
		if h.BeaconsSent > 0 || h.SendErrors > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h.BeaconsSent == 0 && h.SendErrors == 0 {
		t.Errorf("the engine never attempted a beacon on the open transport, so this hub is silent: %+v", h)
	}
	if h.BeaconsSent == 0 {
		t.Logf("no multicast send succeeded on this host (%d attempts all refused); the engine attempted them, which is the product property under test", h.SendErrors)
	}
	_ = blocker
}

// TestEngine_HealthNamesTheLocalConflictNotTheNetwork is the other half of
// row 3.21.
//
// The sentence used to say "subnet broadcast only (multicast is unavailable on
// this network)" whenever the mDNS listener was closed. That names the
// network, and it is almost never the network: the mDNS group is shared, and
// the usual reason this machine's listener could not open is that the machine
// cannot join that group at all. A user reading "unavailable on this
// network" goes to their router.
//
// The condition is produced: the broadcast port is free, so the broadcast
// listener opens, and the mDNS group is one this host cannot join, so the
// mDNS listener cannot.
func TestEngine_HealthNamesTheLocalConflictNotTheNetwork(t *testing.T) {
	engine := startTestEngine(t, DiscoveryConfig{
		NodeName: "local-conflict", WirePort: 9877, BroadcastPort: 19911,
		// TEST-NET-3 is reserved by RFC 5737 and is never a real multicast
		// group, so this host cannot join it: a real bind failure from the
		// engine's point of view.
		MulticastAddr: "203.0.113.255:5353",
		Interval:      50 * time.Millisecond,
	})

	health := engine.Health()
	if !health.Broadcast {
		t.Skip("the subnet broadcast listener did not open on this host, so the sentence names " +
			"the other cause instead")
	}
	if health.Multicast {
		t.Skip("the mDNS listener opened on this host, so there is no local conflict to name")
	}
	if health.MulticastBindError == "" {
		t.Fatal("the mDNS listener is closed but no reason was recorded for it")
	}
	if !strings.Contains(health.Detail, "the mDNS listener could not be opened on this machine") {
		t.Errorf("the health sentence does not name the local cause: %q", health.Detail)
	}
	if strings.Contains(health.Detail, "unavailable on this network") {
		t.Errorf("the health sentence blames the network for a local failure: %q", health.Detail)
	}
}

// TestEngine_AnnouncesEvenWhenItsOwnListenerIsClosed pins the other half.
//
// The engine used to gate every send on this machine's own LISTENER, on the
// reasoning that a write to a port nobody is listening on is "writing into
// the void". That is true of this machine and not of the LAN: a subnet
// broadcast is received by OTHER machines' listeners, so whether we can
// receive one says nothing about whether we can be FOUND.
//
// The consequence was that any other process on the machine holding the
// broadcast port made this hub invisible to every other machine, while the
// write it suppressed would have succeeded and been heard.
//
// The condition is produced: the broadcast port is genuinely occupied, so the
// engine's own listener cannot open, and the mDNS group cannot be resolved,
// so the mDNS send is skipped as well. The subnet broadcast is then the only
// write the engine makes -- and it must make it.
func TestEngine_AnnouncesEvenWhenItsOwnListenerIsClosed(t *testing.T) {
	const port = 19909
	occupyUDP(t, port)

	engine := startTestEngine(t, DiscoveryConfig{
		NodeName: "closed-listener-still-announces", WirePort: 9877, BroadcastPort: port,
		// A group that cannot be resolved: the mDNS listener cannot open on
		// it and the mDNS send is skipped, so the subnet broadcast is the
		// only write under test.
		MulticastAddr: "not-a-real-group.invalid:5353",
		Interval:      50 * time.Millisecond,
	})

	health := engine.Health()
	if health.Broadcast {
		t.Fatal("engine claims the broadcast socket is bound while another process holds that port")
	}

	deadline := time.Now().Add(5 * time.Second)
	var h Health
	for time.Now().Before(deadline) {
		h = engine.Health()
		if h.BeaconsSent > 0 || h.SendErrors > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.BeaconsSent == 0 && h.SendErrors == 0 {
		t.Fatalf("the engine wrote no beacon at all with its own listener closed, so it is "+
			"invisible to every other machine on the network: %+v", h)
	}
}

// TestEngine_BothTransportsUnavailableIsReportedNotHidden covers the state the
// old code made unreachable: running, with nothing bound. Degraded is defined as
// Running && neither transport bound, which under abort-on-broadcast-failure
// could not occur -- so this asserts the state is now genuinely reachable rather
// than only constructible in a test.
//
// Both transports have to fail for real. Occupying the broadcast port is
// deterministic; the mDNS group cannot be occupied the same way, because a
// multicast listener on 224.0.0.251:5353 is shared by design and every other
// mDNS responder on the machine already holds it. So the engine is pointed at an
// address this host cannot join, which is a real bind failure from the engine's
// point of view rather than a simulated one. TEST-NET-3 is reserved by RFC 5737
// and is never a real multicast group.
func TestEngine_BothTransportsUnavailableIsReportedNotHidden(t *testing.T) {
	occupyUDP(t, 19907)

	engine := startTestEngine(t, DiscoveryConfig{
		NodeName: "both-closed", WirePort: 9877, BroadcastPort: 19907,
		MulticastAddr: "203.0.113.255:5353",
		Interval:      50 * time.Millisecond,
	})

	health := engine.Health()
	if !health.Running {
		t.Fatalf("engine must keep running with no transport open: %+v", health)
	}
	if health.Broadcast || health.Multicast {
		t.Fatalf("expected neither transport to be bound on this host: %+v", health)
	}
	if !health.Degraded {
		t.Errorf("running with nothing bound must report degraded: %+v", health)
	}
	if health.Usable {
		t.Error("an engine with nothing bound must not report usable")
	}
	// The sentence must name the LOCAL cause. It used to say "cannot reach
	// this network", which sends a user to their router when the cause is
	// another process on their own machine holding the port -- and here both
	// causes are exactly that. See docs/KNOWN-LIMITATIONS.md row 3.21.
	if !strings.Contains(health.Detail, "cannot find nearby hubs") {
		t.Errorf("degraded engine does not say discovery cannot find nearby hubs: %q", health.Detail)
	}
	if !strings.Contains(health.Detail, "could not be opened on this machine") {
		t.Errorf("the degraded sentence does not name the local cause: %q", health.Detail)
	}
	if strings.Contains(health.Detail, "this network") {
		t.Errorf("the degraded sentence blames the network for a local port conflict: %q", health.Detail)
	}
	// Both reasons must be present: naming only one would leave the user to guess
	// which half of their network is blocked.
	for _, want := range []string{"subnet broadcast", "mDNS"} {
		if !strings.Contains(health.LastError, want) {
			t.Errorf("LastError does not name %q: %q", want, health.LastError)
		}
	}
	// Give the beacon loop a chance to run, then check what it did.
	//
	// It used to assert that NOTHING was sent, on the grounds that a write to
	// a transport nobody is listening on is "writing into the void". That is
	// true of this machine and not of the LAN: a subnet broadcast is received
	// by OTHER machines' listeners, so the write reaches peers even when this
	// machine's own listener could not open. A hub that stopped announcing
	// because another process on the machine held the port was invisible to
	// the whole network, which is what row 3.21 records.
	//
	// So the engine announces either way, and what is asserted is that it
	// announced: a beacon was written, whether it was counted as sent or
	// recorded as an error.
	time.Sleep(200 * time.Millisecond)
	after := engine.Health()
	if after.BeaconsSent == 0 && after.SendErrors == 0 {
		t.Errorf("engine with no listener bound wrote no beacon at all: a hub that cannot " +
			"receive is still findable, and the announcement is what makes it findable")
	}
}

// TestEngine_CloseAfterBestEffortBindsIsClean checks the lifecycle did not
// regress: an engine that started on partial binds must still stop cleanly and
// report not-running, so the watcher does not see a phantom fault at shutdown.
func TestEngine_CloseAfterBestEffortBindsIsClean(t *testing.T) {
	const port = 19908
	occupyUDP(t, port)
	engine := startTestEngine(t, DiscoveryConfig{
		NodeName: "partial-close", WirePort: 9877, BroadcastPort: port, Interval: 50 * time.Millisecond,
	})
	if err := engine.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	health := engine.Health()
	if health.Running || health.Degraded || health.Broadcast || health.Multicast {
		t.Errorf("a closed engine still reports state: %+v", health)
	}
	if !strings.Contains(health.Detail, "not running") {
		t.Errorf("closed engine does not say it is not running: %q", health.Detail)
	}
}
