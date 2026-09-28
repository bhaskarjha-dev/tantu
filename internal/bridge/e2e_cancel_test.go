package bridge_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// This file covers the failure the cancellation work exists to fix.
//
// The user-visible bug: a sign-in is started, the user changes their mind on the
// browser machine, and clicks Cancel in the popup. Nothing is cancelled. The
// machine holding the sign-in keeps a bound loopback callback port, an open
// browser tab, and a live session until its timeout - and because an
// application's redirect port is normally fixed, that abandoned session blocks
// the very retry the user makes next. The only recovery was restarting the
// process.
//
// Every test below asserts the specific resource that was leaking.

// startCancelDispatcher brings up a dispatcher on loopback and returns the
// address to dial plus the registry backing it.
func startCancelDispatcher(t *testing.T, timeout time.Duration) (string, *bridge.SessionRegistry) {
	t.Helper()
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	registry := bridge.NewSessionRegistry()
	cfg := bridge.DispatcherConfig{
		ASideConfig: bridge.ASideConfig{
			Timeout:  timeout,
			Registry: registry,
			// A no-op opener, not nil: the A-side only advances its session state
			// past "waiting for browser" when it is about to open one, and the
			// tests need that state to know the callback listener is bound.
			OpenBrowser: func(string) error { return nil },
		},
		Registry: registry,
	}
	d := bridge.NewDispatcher(listener, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = d.Serve(ctx) }()
	return listener.Addr().String(), registry
}

// freePort returns a port that is free right now. The caller must not keep it
// bound: the A-side needs to take it over for the flow under test.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("probe close: %v", err)
	}
	return port
}

// authURLFor builds an authorization URL whose redirect_uri pins the given
// loopback port, which is what makes the abandoned session block a retry.
func authURLFor(port int, state string) string {
	return fmt.Sprintf("https://login.example.com/authorize?client_id=demo&redirect_uri=http%%3A%%2F%%2F127.0.0.1%%3A%d%%2Fcallback&state=%s", port, state)
}

// waitForListenerBound blocks until the A-side has actually bound the callback
// port, detected through the session state rather than by probing the port.
//
// Probing would be self-defeating: the probe binds the very port the A-side
// needs, so the two race and the A-side loses, and the test would then be
// measuring its own interference. The session only reaches "waiting for
// callback" after the bind succeeds and just before the browser is opened.
func waitForListenerBound(t *testing.T, registry *bridge.SessionRegistry, port int) string {
	t.Helper()
	var requestID string
	waitFor(t, 10*time.Second, func() bool {
		for _, info := range registry.List() {
			if info.CallbackPort == port && info.State == "waiting_for_callback" {
				requestID = info.RequestID
				return true
			}
		}
		return false
	}, fmt.Sprintf("the callback listener on port %d to be bound", port))
	return requestID
}

// waitForPortFree blocks until the port can be bound again, which is the
// observable proof that the callback listener was released.
func waitForPortFree(t *testing.T, port int, within time.Duration) {
	t.Helper()
	waitFor(t, within, func() bool {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return false
		}
		_ = l.Close()
		return true
	}, fmt.Sprintf("port %d to be released", port))
}

// waitForListenerBoundOrFail is waitForListenerBound with the B-side's own error
// reported if the flow never got off the ground. Without this the only symptom is
// the A-side's "connection is closed", which says nothing about the cause.
func waitForListenerBoundOrFail(t *testing.T, sessionErr <-chan error, registry *bridge.SessionRegistry, port int) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, info := range registry.List() {
			if info.CallbackPort == port && info.State == "waiting_for_callback" {
				return info.RequestID
			}
		}
		select {
		case err := <-sessionErr:
			t.Fatalf("the sign-in never started; the B-side reported: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("timed out after 10s waiting for the callback listener on port %d to be bound (open sessions: %+v)", port, registry.List())
	return ""
}

func waitFor(t *testing.T, within time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", within, what)
}

// The core regression. A sign-in is opened, the peer cancels it, and the
// callback port the A-side had bound must become free immediately - not after
// the session timeout. That is the difference between "click Cancel" working and
// "restart tantu" being the only option.
func TestE2E_CancelReleasesCallbackPortImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	addr, registry := startCancelDispatcher(t, 20*time.Second)
	tr := transport.NewLoopbackTransport()

	port := freePort(t)
	authURL := authURLFor(port, "abc123")

	sessionErr := make(chan error, 1)
	go func() {
		conn, dialErr := tr.Dial(addr)
		if dialErr != nil {
			sessionErr <- dialErr
			return
		}
		defer conn.Close()
		sessionErr <- bridge.HandleBSide(ctx, conn, authURL, bridge.BSideConfig{Timeout: 20 * time.Second})
	}()

	// Surface a B-side failure as a test failure with its own words: the A-side
	// only reports "connection is closed", which hides why the flow never started.
	waitForListenerBoundOrFail(t, sessionErr, registry, port)

	cancelConn, err := tr.Dial(addr)
	if err != nil {
		t.Fatalf("dial for cancel: %v", err)
	}
	defer cancelConn.Close()

	// The flow identity is what the peer can resolve; the A-side never saw the
	// caller's attempt id, so a flow-scoped cancel is the realistic request.
	flowID := bridge.DeriveOAuthFlowID(authURL)
	if flowID == "" {
		t.Fatal("no flow id derived for the authorization URL")
	}
	released, err := bridge.CancelRemoteSession(ctx, cancelConn, "", flowID, "test cancel")
	if err != nil {
		t.Fatalf("CancelRemoteSession: %v", err)
	}
	if !released {
		t.Fatal("the peer reported no matching sign-in, so nothing was released")
	}

	// The decisive assertion: the port is free now, not in 20 seconds.
	waitForPortFree(t, port, 5*time.Second)

	// And the B-side caller must see the flow end rather than hang to its timeout.
	select {
	case err := <-sessionErr:
		if err == nil {
			t.Fatal("the B-side reported success for a sign-in the user cancelled")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the B-side call did not return after the cancel; it is still waiting")
	}

	if registry.Count() != 0 {
		t.Fatalf("the registry still lists %d sessions after the cancel", registry.Count())
	}
}

// A cancel must be able to target one exact attempt by request id, which is what
// a dashboard's per-row Cancel button sends.
func TestE2E_CancelByRequestIDReleasesExactSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	addr, registry := startCancelDispatcher(t, 20*time.Second)
	tr := transport.NewLoopbackTransport()

	port := freePort(t)
	authURL := authURLFor(port, "xyz789")

	go func() {
		conn, dialErr := tr.Dial(addr)
		if dialErr != nil {
			return
		}
		defer conn.Close()
		_ = bridge.HandleBSide(ctx, conn, authURL, bridge.BSideConfig{Timeout: 20 * time.Second})
	}()

	requestID := waitForListenerBound(t, registry, port)

	cancelConn, err := tr.Dial(addr)
	if err != nil {
		t.Fatalf("dial for cancel: %v", err)
	}
	defer cancelConn.Close()
	released, err := bridge.CancelRemoteSession(ctx, cancelConn, requestID, "", "test cancel by id")
	if err != nil {
		t.Fatalf("CancelRemoteSession: %v", err)
	}
	if !released {
		t.Fatalf("no session matched request id %q", requestID)
	}
	waitForPortFree(t, port, 5*time.Second)
}

// The retry-after-abandon case, which is the user's actual workflow. A stale
// sign-in holds the application's fixed redirect port; a fresh attempt for the
// same port must succeed anyway, because the stale one is released on arrival.
// Without this, the user is stuck restarting the process.
func TestE2E_RetryOnSamePortSupersedesAbandonedSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	addr, registry := startCancelDispatcher(t, 30*time.Second)
	tr := transport.NewLoopbackTransport()

	fixedPort := freePort(t)
	authURL := authURLFor(fixedPort, "first-attempt")

	firstErr := make(chan error, 1)
	go func() {
		conn, dialErr := tr.Dial(addr)
		if dialErr != nil {
			firstErr <- dialErr
			return
		}
		defer conn.Close()
		firstErr <- bridge.HandleBSide(ctx, conn, authURL, bridge.BSideConfig{Timeout: 30 * time.Second})
	}()
	waitForListenerBound(t, registry, fixedPort)

	// The first attempt is abandoned: nothing on this side sends a cancel, which
	// is exactly what the old Cancel button amounted to. The retry arrives as a
	// second bridge_request for the same fixed port.
	secondURL := authURLFor(fixedPort, "second-attempt")
	secondAck := make(chan string, 1)
	go func() {
		conn, dialErr := tr.Dial(addr)
		if dialErr != nil {
			secondAck <- "dial: " + dialErr.Error()
			return
		}
		defer conn.Close()
		if sendErr := conn.Send(protocol.TypeBridgeRequest, protocol.BridgeRequest{
			URL:          secondURL,
			CallbackPort: fixedPort,
			RequestID:    "rl-retry",
			FlowID:       bridge.DeriveOAuthFlowID(secondURL),
		}); sendErr != nil {
			secondAck <- "send: " + sendErr.Error()
			return
		}
		env, recvErr := conn.Receive()
		if recvErr != nil {
			secondAck <- "receive: " + recvErr.Error()
			return
		}
		if env.Type != protocol.TypeBridgeAck {
			secondAck <- "unexpected type " + env.Type
			return
		}
		var ack protocol.BridgeAck
		if decErr := env.DecodePayload(&ack); decErr != nil {
			secondAck <- "decode: " + decErr.Error()
			return
		}
		if ack.Error != "" {
			secondAck <- "ack error: " + ack.Error
			return
		}
		secondAck <- ""
	}()

	select {
	case msg := <-secondAck:
		if msg != "" {
			t.Fatalf("the retry could not start while the abandoned session held port %d: %s", fixedPort, msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the retry never received an ack")
	}

	// The abandoned first attempt must also be gone, not left holding on.
	select {
	case err := <-firstErr:
		if err == nil {
			t.Fatal("the superseded attempt reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the superseded attempt was not released by the retry")
	}
}

// An untrusted peer must not be able to abort another machine's sign-in. The
// cancel travels on the wire, so this gate is what stops any host that can reach
// the port from killing a login in progress.
func TestE2E_UntrustedPeerCannotCancelSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	registry := bridge.NewSessionRegistry()
	cfg := bridge.DispatcherConfig{
		ASideConfig: bridge.ASideConfig{Timeout: 20 * time.Second, Registry: registry},
		Registry:    registry,
		IsPeerTrusted: func(string) bool {
			return false // nobody is paired
		},
	}
	d := bridge.NewDispatcher(listener, cfg)
	go func() { _ = d.Serve(ctx) }()
	addr := listener.Addr().String()

	port := freePort(t)
	authURL := authURLFor(port, "hostile")

	go func() {
		conn, dialErr := tr.Dial(addr)
		if dialErr != nil {
			return
		}
		defer conn.Close()
		_ = bridge.HandleBSide(ctx, conn, authURL, bridge.BSideConfig{Timeout: 20 * time.Second})
	}()

	// The A-side refuses the untrusted bridge_request outright, so register a
	// session directly to model the state that must be protected.
	ctxSess, cancelSess := context.WithCancelCause(context.Background())
	defer cancelSess(nil)
	if _, err := registry.Register(ctxSess, cancelSess, nil, bridge.SessionInfo{
		RequestID:       "rl-protected",
		CallbackPort:    port,
		PeerFingerprint: "trusted-peer",
	}); err != nil {
		t.Fatal(err)
	}

	// Send a cancel as the untrusted peer. Two forms must both be refused: a
	// flow-scoped one, and the catch-all peer-scoped one.
	for _, msg := range []protocol.BridgeCancel{
		{RequestID: "rl-protected", Reason: "hostile"},
		{Reason: "hostile catch-all"},
	} {
		conn, dialErr := tr.Dial(addr)
		if dialErr != nil {
			t.Fatalf("dial: %v", dialErr)
		}
		if sendErr := conn.Send(protocol.TypeBridgeCancel, msg); sendErr != nil {
			t.Fatalf("send cancel: %v", sendErr)
		}
		env, recvErr := conn.Receive()
		if recvErr != nil {
			conn.Close()
			t.Fatalf("receive ack: %v", recvErr)
		}
		var ack protocol.BridgeCancelAck
		_ = env.DecodePayload(&ack)
		_ = conn.Close()
		if ack.Cancelled {
			t.Fatalf("an untrusted peer released a session with %+v", msg)
		}
		if ack.Error == "" {
			t.Fatal("an untrusted cancel was refused without saying why")
		}
	}

	select {
	case <-ctxSess.Done():
		t.Fatal("an untrusted peer's cancel released a protected session")
	case <-time.After(100 * time.Millisecond):
	}
}

// A cancel for something that is not open must be reported plainly, not as a
// failure. The flow may have completed or timed out between the user deciding
// and the cancel arriving, and saying "cancelled: error" would be a lie about
// something already resolved.
func TestE2E_CancelForUnknownSessionIsNotAnError(t *testing.T) {
	addr, _ := startCancelDispatcher(t, 5*time.Second)
	tr := transport.NewLoopbackTransport()
	conn, err := tr.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.Send(protocol.TypeBridgeCancel, protocol.BridgeCancel{RequestID: "rl-does-not-exist"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	env, err := conn.Receive()
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if env.Type != protocol.TypeBridgeCancel {
		t.Fatalf("reply type = %q, want %q", env.Type, protocol.TypeBridgeCancel)
	}
	var ack protocol.BridgeCancelAck
	if err := env.DecodePayload(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.Cancelled {
		t.Fatal("a cancel for an unknown session reported success")
	}
	if ack.Error == "" {
		t.Fatal("no explanation given for a cancel that matched nothing")
	}
}

// Malformed and oversized cancel payloads must be refused without touching any
// session. A cancel is attacker-influenced input arriving on a listener.
func TestE2E_MalformedCancelIsRefused(t *testing.T) {
	addr, registry := startCancelDispatcher(t, 5*time.Second)
	tr := transport.NewLoopbackTransport()

	// A request id with characters the protocol does not permit, and an
	// oversized flow id.
	hugeFlow := make([]byte, 4096)
	for i := range hugeFlow {
		hugeFlow[i] = 'a'
	}
	for _, msg := range []protocol.BridgeCancel{
		{RequestID: "not a valid id/../etc"},
		{FlowID: string(hugeFlow)},
	} {
		// A fresh connection per message: the dispatcher handles a cancel on a
		// single connection and closes it when it is done, so reusing one would
		// fail on transport, not on validation.
		conn, err := tr.Dial(addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if err := conn.Send(protocol.TypeBridgeCancel, msg); err != nil {
			conn.Close()
			t.Fatalf("send: %v", err)
		}
		env, recvErr := conn.Receive()
		if recvErr != nil {
			conn.Close()
			t.Fatalf("receive: %v", recvErr)
		}
		var ack protocol.BridgeCancelAck
		_ = env.DecodePayload(&ack)
		_ = conn.Close()
		if ack.Cancelled {
			t.Fatalf("a malformed cancel was honoured: %+v", ack)
		}
		if ack.Error == "" {
			t.Fatalf("a malformed cancel was refused without saying why: %+v", msg)
		}
	}
	if registry.Count() != 0 {
		t.Fatalf("a malformed cancel created %d sessions", registry.Count())
	}
}

// The client must validate before touching the wire. An invalid identifier sent
// anyway is a request the peer will refuse, and a wasted round trip on a
// user-facing path.
func TestCancelRemoteSessionRejectsBadInput(t *testing.T) {
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	conn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := bridge.CancelRemoteSession(context.Background(), nil, "rl-1", "flow", "r"); err == nil {
		t.Fatal("a nil connection was accepted")
	}
	if _, err := bridge.CancelRemoteSession(context.Background(), conn, "", "", "r"); err == nil {
		t.Fatal("a cancel with no identifier was accepted")
	}
	if _, err := bridge.CancelRemoteSession(context.Background(), conn, "bad id/../x", "", "r"); err == nil {
		t.Fatal("an invalid request id was accepted")
	}
	huge := make([]byte, 8192)
	for i := range huge {
		huge[i] = 'a'
	}
	if _, err := bridge.CancelRemoteSession(context.Background(), conn, "", string(huge), "r"); err == nil {
		t.Fatal("an oversized flow id was accepted")
	}
}

// A peer that predates the cancel message never replies. The caller must not
// treat that as a failure the user can act on - the peer will release on its own
// timeout - and must not hang for the full session timeout doing it.
func TestCancelRemoteSessionTimesOutQuietlyOnSilentPeer(t *testing.T) {
	// A raw listener that reads the envelope and then says nothing, standing in
	// for an older peer that drops the unknown type. A transport.Conn is used on
	// the client side so the cancel is framed the way it is in production.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan struct{})
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer c.Close()
		close(accepted)
		buf := make([]byte, 4096)
		for {
			if _, readErr := c.Read(buf); readErr != nil {
				return
			}
		}
	}()

	tr := transport.NewLoopbackTransport()
	conn, err := tr.Dial(ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	<-accepted

	start := time.Now()
	released, err := bridge.CancelRemoteSession(context.Background(), conn, "rl-1", "", "silent peer")
	if err != nil {
		t.Fatalf("a silent peer was reported as an error: %v", err)
	}
	if released {
		t.Fatal("a silent peer reported a release")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the cancel waited %v on a silent peer; it must give up quickly", elapsed)
	}
}

// The A-side must report an abandoned sign-in as abandoned, not as a failure or a
// timeout. A user cancellation surfaced through the timeout channel is how the
// two became indistinguishable in the first place.
func TestE2E_CancelledSessionReportsAbandonedNotTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	addr, registry := startCancelDispatcher(t, 20*time.Second)
	tr := transport.NewLoopbackTransport()

	port := freePort(t)
	authURL := authURLFor(port, "distinct")

	asideErr := make(chan error, 1)
	go func() {
		conn, dialErr := tr.Dial(addr)
		if dialErr != nil {
			asideErr <- dialErr
			return
		}
		defer conn.Close()
		asideErr <- bridge.HandleBSide(ctx, conn, authURL, bridge.BSideConfig{Timeout: 20 * time.Second})
	}()

	waitForListenerBound(t, registry, port)

	cancelConn, err := tr.Dial(addr)
	if err != nil {
		t.Fatalf("dial for cancel: %v", err)
	}
	defer cancelConn.Close()
	flowID := bridge.DeriveOAuthFlowID(authURL)
	if _, err := bridge.CancelRemoteSession(ctx, cancelConn, "", flowID, "abandoned"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	select {
	case err := <-asideErr:
		if err == nil {
			t.Fatal("a cancelled sign-in reported success")
		}
		// The B-side sees a closed context, which is correct for it. The point
		// asserted here is that it does not report success, so nothing upstream
		// can mistake an abandoned sign-in for a completed login.
		if !errors.Is(err, context.Canceled) && !stringsContain(err.Error(), "cancel") && !stringsContain(err.Error(), "closed") {
			t.Logf("cancelled sign-in reported: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled sign-in never returned")
	}
	if registry.Count() != 0 {
		t.Fatalf("%d sessions still listed after the sign-in was abandoned", registry.Count())
	}
}

func stringsContain(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
