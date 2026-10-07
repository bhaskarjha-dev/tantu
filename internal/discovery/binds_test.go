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

// TestEngine_BeaconDoesNotCountWritesToATransportThatIsNotOpen pins the other
// half. A write to a port nobody is listening on still succeeds -- UDP returns
// no error and delivers nothing -- so an engine that beaconed a closed
// transport would count beacons it never actually sent, and report a healthy
// hub that no peer can see. That is the precise failure the BeaconsSent counter
// was added to expose, so the counter must not commit it.
func TestEngine_BeaconDoesNotCountWritesToATransportThatIsNotOpen(t *testing.T) {
	const port = 19906
	occupyUDP(t, port)

	engine := startTestEngine(t, DiscoveryConfig{
		NodeName: "closed-transport", WirePort: 9877, BroadcastPort: port, Interval: 50 * time.Millisecond,
	})

	// mDNS is expected to be available in the test environment, so BeaconsSent
	// may legitimately rise. What must never happen is a beacon being counted
	// while both transports are reported closed, which is the only state in
	// which nothing could have been written.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		health := engine.Health()
		if !health.Broadcast && !health.Multicast && health.BeaconsSent > 0 {
			t.Fatalf("counted %d beacon(s) with no transport open: %+v", health.BeaconsSent, health)
		}
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
	if !strings.Contains(health.Detail, "cannot reach this network") {
		t.Errorf("degraded engine does not say discovery cannot reach the network: %q", health.Detail)
	}
	// Both reasons must be present: naming only one would leave the user to guess
	// which half of their network is blocked.
	for _, want := range []string{"subnet broadcast", "mDNS"} {
		if !strings.Contains(health.LastError, want) {
			t.Errorf("LastError does not name %q: %q", want, health.LastError)
		}
	}
	// Give the beacon loop a chance to run, then prove it sent nothing rather
	// than writing into the void and counting it.
	time.Sleep(200 * time.Millisecond)
	if got := engine.Health().BeaconsSent; got != 0 {
		t.Errorf("engine with no transport counted %d beacon(s) as sent", got)
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
