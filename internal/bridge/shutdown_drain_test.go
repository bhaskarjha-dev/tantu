package bridge

import (
	"context"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// drainRig starts a dispatcher on loopback with a controllable drain window and
// returns a live session plus the cancel function that begins shutdown.
//
// The session is a heartbeat exchange rather than a transfer, and that is the
// whole trick. A transfer's duration is a property of the network, so a gate
// that interrupts one is a timing race: on a fast machine the transfer finishes
// before the interrupt lands, and the gate passes without ever producing the
// condition. A heartbeat session is open, counted, and under the test's control
// -- it ends when the test ends it, and not a moment sooner.
type drainRig struct {
	conn        transport.Conn
	dispatcher  *Dispatcher
	cancel      context.CancelFunc
	serveDone   chan error
	drainWindow time.Duration
}

func startDrainRig(t *testing.T, drainWindow time.Duration) *drainRig {
	t.Helper()
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	d := NewDispatcher(listener, DispatcherConfig{SessionDrainGrace: drainWindow})
	go func() { serveDone <- d.Serve(ctx) }()

	conn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	r := &drainRig{conn: conn, dispatcher: d, cancel: cancel,
		serveDone: serveDone, drainWindow: drainWindow}
	t.Cleanup(func() { _ = conn.Close() })

	// The round trip is the proof that the dispatcher has accepted the
	// connection and its handler is now inside the session loop. Until this
	// returns there is nothing in flight to drain, and the gate below would be
	// asserting about a session that never existed.
	if err := conn.Send(protocol.TypeHeartbeat, protocol.Heartbeat{RequestID: "drain-open"}); err != nil {
		t.Fatalf("open the session: %v", err)
	}
	env, err := conn.Receive()
	if err != nil {
		t.Fatalf("session did not answer its heartbeat: %v", err)
	}
	if env.Type != protocol.TypeHeartbeat {
		t.Fatalf("expected a heartbeat echo, got %s", env.Type)
	}

	// And wait until the dispatcher counts it, so "in flight" means the
	// dispatcher's own accounting agrees rather than the test's assumption.
	deadline := time.Now().Add(5 * time.Second)
	for d.ActiveSessions() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the dispatcher never counted the session, so there is nothing to drain")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return r
}

// roundTrip sends one heartbeat and reads the echo. It is the test's liveness
// probe: a session that answers is being serviced, a session that errors has
// been aborted underneath the peer.
func (r *drainRig) roundTrip(t *testing.T, id string) error {
	t.Helper()
	if err := r.conn.Send(protocol.TypeHeartbeat, protocol.Heartbeat{RequestID: id}); err != nil {
		return err
	}
	env, err := r.conn.Receive()
	if err != nil {
		return err
	}
	if env.Type != protocol.TypeHeartbeat {
		t.Errorf("expected a heartbeat echo, got %s", env.Type)
	}
	return nil
}

// TestDispatcherShutdownDrainsASessionBeforeAbortingIt is the first half of
// limitation 4.8, and the half that had never been stated anywhere.
//
// Before the fix, ctx cancellation closed every active connection on the next
// statement. That is safe -- a dropped connection aborts cleanly -- but it means
// a session that was about to succeed was killed instead, and a session
// unwinding after its connection died kept writing while the Hub finished
// stopping. That second part is how a test's own cleanup could find the peer
// store being written after the Hub was gone.
//
// So the claim is narrow and checkable: shutdown must not take a working
// session away. Not "finishes transfers" -- that is not possible and not
// promised -- but "leaves it alone long enough to finish".
func TestDispatcherShutdownDrainsASessionBeforeAbortingIt(t *testing.T) {
	const window = 2 * time.Second
	r := startDrainRig(t, window)

	// Shutdown begins.
	r.cancel()

	// The session must still be answering some moments later. The probe runs
	// well inside the drain window and is retried, because the exact moment
	// cancel() lands relative to the dispatcher's own wakeup is not something a
	// test should be asserting to the microsecond.
	var probeErr error
	deadline := time.Now().Add(window / 2)
	for time.Now().Before(deadline) {
		if probeErr = r.roundTrip(t, "drain-still-alive"); probeErr == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if probeErr != nil {
		t.Fatalf("the session was taken away the instant shutdown began (%v). A session already in "+
			"flight has to be given its drain window: it is usually seconds from done, and killing it "+
			"leaves a half-finished transfer on the peer", probeErr)
	}

	// Now end the session the way a real one ends, and require Serve to return
	// promptly rather than waiting out the remainder of the window. Without this
	// the gate would also pass against an implementation that simply never
	// aborts anything.
	start := time.Now()
	_ = r.conn.Close()
	select {
	case <-r.serveDone:
	case <-time.After(window):
		t.Fatalf("Serve did not return within the drain window after its last session ended")
	}
	if elapsed := time.Since(start); elapsed > window {
		t.Errorf("Serve took %s to return once its session had ended", elapsed)
	}
}

// TestDispatcherShutdownAbortsASessionThatOverrunsTheWindow is the other half,
// and it is the half that stops the drain from becoming a hang.
//
// A drain with no deadline is a quit button that does nothing. A session that
// will never finish -- a peer that stops reading mid-transfer, which is exactly
// what a 5GB send to a sleeping laptop looks like -- must still be released
// when the window closes.
//
// The window is set to 250ms rather than the production five seconds so the
// branch is exercised on every run for the price of a quarter second. The
// production value is a judgement about a quit button and is documented at
// sessionDrainGrace.
func TestDispatcherShutdownAbortsASessionThatOverrunsTheWindow(t *testing.T) {
	const window = 250 * time.Millisecond
	r := startDrainRig(t, window)

	r.cancel()

	// The session stays open on purpose. Nothing here ever ends it, so the only
	// thing that can release it is the abort.
	select {
	case <-r.serveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve never returned. A session that outlives the drain window has to be aborted; " +
			"otherwise quitting the Hub waits on a peer that may never send another byte")
	}

	// And the peer must actually observe the abort rather than being left with
	// a connection that looks alive and is not serviced.
	if err := r.roundTrip(t, "after-abort"); err == nil {
		t.Error("the session still answered after the abort; the connection was released one way " +
			"on the dispatcher's side and not on the peer's")
	}
}
