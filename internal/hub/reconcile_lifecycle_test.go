package hub

// The reconciliation dial outlived Stop().
//
// The Hub's reconnection engine dials a discovered address to find out whether
// one of its peers has moved. That dial ran on `context.Background()` -- not
// the Hub's run context -- and it was on no WaitGroup at all, so `Stop()`
// could not cancel it and could not wait for it. It was also free to write to
// the peer store afterwards, which is exactly what the `heartbeatWG` field
// comment forbids:
//
//     Stop returning while a background goroutine is still writing to the
//     peer store is a lifecycle bug rather than a test inconvenience.
//
// The production consequence is benign -- an address written after shutdown --
// but the lifecycle guarantee `Stop()` claims is not kept, and the field
// comment is the rule it broke.
//
// See docs/KNOWN-LIMITATIONS.md row 3.30.

import (
	"net"
	"testing"
	"time"
)

// startBlackholeListener starts a TCP listener that accepts connections and
// never speaks, so a dial to it hangs in the TLS handshake.
//
// It is the blackhole, not the impostor: the impostor hangs up at once, which
// makes the dial fail immediately. This one has to hold the connection open,
// because the condition under test is a dial that is STILL IN FLIGHT when
// Stop is called.
func startBlackholeListener(t *testing.T) (addr string, conns chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	conns = make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
			select {
			case conns <- c:
			default:
			}
		}
	}()
	return ln.Addr().String(), conns
}

// TestStopRefusesToStartBackgroundWork is the gate for the WaitGroup contract
// violation.
//
// A background task that STARTS after Stop has begun waiting is a task Stop
// does not wait for, and a task that writes to the peer store is exactly what
// Stop promises not to leave running.
//
// The reconnection dial is the goroutine that got it wrong the first time: it
// ran on context.Background() and was on no WaitGroup at all, so it outlived
// Stop and rewrote peers.json afterwards. Fixing that by adding it to the
// WaitGroup was not enough on its own -- an Add that lands after the Wait has
// begun is untracked by the WaitGroup's own rule -- so the reservation now
// goes through beginBackgroundWork, which refuses once the Hub is stopping.
//
// The condition is produced: a stopped Hub, a discovered address, and a
// listener that would count the connection if one were made.
func TestStopRefusesToStartBackgroundWork(t *testing.T) {
	a, _ := startPairedHubs(t, t.TempDir())
	blackhole, conns := startBlackholeListener(t)

	// The peer A knows is recorded at a stale address, so the reconciliation
	// has a candidate to dial at the discovered one.
	staleAddress(t, a, "127.0.0.1:1")

	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// A beacon arriving after the Hub has stopped must not start a dial: the
	// dial would be untracked, and it writes to the peer store.
	a.reconcileDiscoveredAddress(blackhole, "somebody-else")

	select {
	case conn := <-conns:
		_ = conn.Close()
		t.Fatal("a stopped Hub dialled the discovered address: the dial was started after Stop " +
			"began waiting, so nothing was waiting for it, and it is free to write to the peer " +
			"store after the shutdown")
	case <-time.After(1500 * time.Millisecond):
		// No dial. That is the whole assertion.
	}
}

// TestStopCancelsTheReconciliationDial is the gate.
//
// The condition is produced, not constructed: a real dial to a real listener
// that holds the connection open, so the dial is genuinely in flight when
// Stop is called.
//
// After Stop returns, the dial must be finished. It is finished because Stop
// cancelled the Hub's run context, which the dial's context descends from.
func TestStopCancelsTheReconciliationDial(t *testing.T) {
	a, _ := startPairedHubs(t, t.TempDir())
	blackhole, conns := startBlackholeListener(t)

	// The peer A knows is recorded at a stale address, so the reconciliation
	// has a candidate to dial at the discovered one.
	staleAddress(t, a, "127.0.0.1:1")

	a.reconcileDiscoveredAddress(blackhole, "somebody-else")

	var dialed net.Conn
	select {
	case dialed = <-conns:
	case <-time.After(10 * time.Second):
		t.Fatal("the reconciliation never dialled, so this gate would pass against code that " +
			"does nothing at all")
	}

	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop returns, the dial must be over. The dial's context descends
	// from the Hub's run context, so Stop cancelled it and the connection it
	// held is closed.
	//
	// The connection is read until it errors rather than read once: the TLS
	// ClientHello is already in the buffer in BOTH cases, so a single read
	// succeeds whether the dial is finished or not. What distinguishes them
	// is whether the connection is still OPEN afterwards.
	//
	// Before the fix the dial ran on context.Background() and on no
	// WaitGroup, so it was still holding this connection when Stop returned,
	// and it was free to write to the peer store afterwards.
	_ = dialed.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	for {
		_, err := dialed.Read(buf)
		if err == nil {
			continue
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("the reconciliation dial was still in flight after Stop returned: it ran on " +
				"context.Background() rather than the Hub's run context, and it was on no WaitGroup, " +
				"so Stop could neither cancel it nor wait for it, and it could still write to the " +
				"peer store after the shutdown it belonged to")
		}
		return
	}
}
