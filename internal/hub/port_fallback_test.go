package hub

// Gates for wire-port fallback.
//
// The claim is not "the code walks a range". It is that a Hub whose configured
// wire port is genuinely taken still comes up, on a port that is really bound,
// without stealing the web port, and says out loud that it moved.
//
// Every gate here produces its condition rather than describing it. The ports
// are occupied by real listeners held open for the duration of the test, so the
// bind is rejected by the operating system rather than by a flag, a mock or a
// hand-written error. A gate that asked the function to report "in use" would
// pass against a Hub that never actually tried.
//
// This is also the batch where fallback became safe to add at all. A Hub that
// quietly moved ports and told nobody would be unreachable forever, because
// every peer's stored address still named the old port and nothing would correct
// them -- silent fallback manufactures exactly the unhealable drift the
// reconnection engine exists to repair.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// occupyPort holds a real listener on 127.0.0.1:port for the life of the test.
// Binding fails the test loudly: if the port could not be taken, the gates below
// would be exercising a fallback that never had to happen.
func occupyPort(t *testing.T, port int) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("could not occupy 127.0.0.1:%d, so this gate would test a fallback that "+
			"never happens: %v", port, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// freeQuad returns the first port of a run of five consecutive free loopback
// ports, found by actually binding them.
//
// Five, not four, and the extra one is not slack. The fallback scans down from
// the configured port, so the gate that exhausts the scan has to occupy a run
// that the scan actually walks: configured at the top, then the four below it.
// An earlier version returned a verified run of four and then occupied ports
// *below* that run, which it had never checked -- three of the four were
// unverified. It passed on a quiet machine and failed on a Windows runner with
// "could not occupy 127.0.0.1:50939", which is the failure mode this helper
// exists to prevent.
//
// Finding them by binding is the only trustworthy method: assuming a port is
// free is how a test passes locally and fails in CI.
func freeQuad(t *testing.T) int {
	t.Helper()
	const run = 5
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe for a free port: %v", err)
	}
	start := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	for attempt := 0; attempt < 500; attempt++ {
		base := start - attempt
		if base < 1024 {
			break
		}
		held := make([]net.Listener, 0, run)
		ok := true
		for i := 0; i < run; i++ {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", base+i))
			if err != nil {
				ok = false
				break
			}
			held = append(held, ln)
		}
		for _, ln := range held {
			_ = ln.Close()
		}
		if ok {
			return base
		}
	}
	t.Fatalf("no run of %d consecutive free loopback ports below %d", run, start)
	return 0
}

// startHubOnPorts builds and starts a Hub on explicit wire and web ports,
// failing the test if it does not become ready.
//
// The Err() check is load-bearing rather than ceremonial. Ready() means "Start
// returned", not "the Hub is up" -- the Hub deliberately wakes waiters on a
// failed startup so nobody blocks forever, and Err() is how the two are told
// apart. An earlier version of these gates read Ready() as success and so
// reported "Hub started with all four ports occupied" for a Hub that had died
// during startup, which is the opposite of what the gate exists to catch.
func startHubOnPorts(t *testing.T, wire, web int) *Hub {
	t.Helper()
	h, err := NewHub(HubConfig{
		TransportType: "lan",
		ListenAddr:    fmt.Sprintf("127.0.0.1:%d", wire),
		WebAddr:       fmt.Sprintf("127.0.0.1:%d", web),
		StoreDir:      t.TempDir(),
		OutputDir:     t.TempDir(),
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(15 * time.Second):
		t.Fatalf("Start never returned on wire %d / web %d", wire, web)
	}
	if err := h.Err(); err != nil {
		t.Fatalf("Hub failed to start on wire %d / web %d: %v", wire, web, err)
	}
	t.Cleanup(func() { _ = h.Stop() })
	return h
}

// portOf returns the numeric port of a Host:port string, failing the test if it
// cannot be parsed. A helper that returned 0 on a parse error would turn a broken
// assertion into a passing one.
func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("port in %q is not a number: %v", addr, err)
	}
	return port
}

// assertPortAnswersTantu proves the port is really held, because a number in a
// struct is not a listener.
func assertPortAnswersTantu(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("nothing is listening at %s, so the Hub is not really holding the port it "+
			"reported: %v", addr, err)
	}
	_ = conn.Close()
}

// TestWirePortFallbackMovesWhenThePortIsReallyTaken is the produced case: the
// configured wire port is occupied by a real listener, the bind really fails,
// and the Hub still comes up on a lower port which it really holds.
//
// Before this batch that was a hard exit. A user with a stale Tantu still on 9877
// was refused a start, with no route forward that did not involve finding and
// killing the other process by hand.
func TestWirePortFallbackMovesWhenThePortIsReallyTaken(t *testing.T) {
	base := freeQuad(t)
	wire, web := base, base+1
	occupyPort(t, wire)

	h := startHubOnPorts(t, wire, web)

	got := portOf(t, h.P2PAddr().String())
	if got == wire {
		t.Fatalf("Hub reports it is on the configured wire port %d, which is held by the "+
			"blocker listener", wire)
	}
	if got >= wire {
		t.Errorf("fallback moved upward to %d, want a port below the occupied %d", got, wire)
	}
	if got == web {
		t.Errorf("the wire listener took the web port %d", web)
	}
	assertPortAnswersTantu(t, h.P2PAddr().String())

	// The blocker still holds what it held, which is what makes the above a real
	// fallback rather than the Hub having somehow got the original port.
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", wire), time.Second); err != nil {
		t.Errorf("the blocker on %d is no longer reachable, so the test did not exercise a "+
			"taken port: %v", wire, err)
	}
}

// The web port must stay where the user configured it.
//
// This is the case the reserved-port guard exists for, and getting the
// arrangement right took two attempts. Both scanners walk *down* from a
// configured number, so the guard only matters when the wire port is configured
// **above** the web port -- 9877 above 9876, which is the shape of the real
// defaults. And it only matters when the web port is **free**: if the web port is
// occupied, the wire scan cannot take it whether or not the guard is present, so
// a gate built that way passed with the guard removed.
//
// The damage the guard prevents is not a crash. Wire 9877 is taken, the scan
// walks down, finds 9876 free and takes it, and the dashboard is then pushed to
// 9875 -- a port the user never configured and has no firewall rule for, moved
// silently, in order to serve a dashboard that had somewhere perfectly good to
// be.
func TestWireFallbackLeavesTheWebPortWhereTheUserConfiguredIt(t *testing.T) {
	base := freeQuad(t)
	web, wire := base, base+1 // wire above web, as 9877 is above 9876
	occupyPort(t, wire)

	h := startHubOnPorts(t, wire, web)

	gotWire := portOf(t, h.P2PAddr().String())
	if gotWire == wire {
		t.Fatalf("the wire port %d was occupied but the Hub did not move", wire)
	}
	gotWeb := portOf(t, h.WebAddr())
	if gotWeb != web {
		t.Errorf("the web port moved from the free configured %d to %d; the wire fallback "+
			"scans downward through it and must step over it, or the dashboard lands on a port "+
			"the user never configured and has no firewall rule for", web, gotWeb)
	}
	assertPortAnswersTantu(t, h.P2PAddr().String())
	assertServes(t, h.WebAddr())
}

// And when *both* configured ports are taken -- the only arrangement in which
// both scanners have to move at once -- the two must still land on different
// numbers. This one is a sanity assertion rather than the guard's gate, because
// the wire listener binds first and holds its port, so the web scanner cannot
// collide with it regardless. It is kept because "cannot collide" is a claim
// about ordering that nothing else checks.
func TestWireAndWebFallbackNeverChooseTheSamePort(t *testing.T) {
	base := freeQuad(t)
	web, wire := base, base+1
	occupyPort(t, wire)
	occupyPort(t, web)

	h := startHubOnPorts(t, wire, web)

	gotWire := portOf(t, h.P2PAddr().String())
	gotWeb := portOf(t, h.WebAddr())
	if gotWire == gotWeb {
		t.Fatalf("the wire listener and the web listener both bound %d", gotWeb)
	}
	if gotWire == web {
		t.Errorf("the wire fallback took the configured web port %d", web)
	}
	if gotWeb == wire {
		t.Errorf("the web fallback took the configured wire port %d", wire)
	}
	assertPortAnswersTantu(t, h.P2PAddr().String())
	assertServes(t, h.WebAddr())
}

// TestFallbackSaysWhichPortItMovedTo is the "out loud" claim.
//
// A Hub listening somewhere other than where it was configured is how a port
// problem turns into a "Tantu is broken" report hours later with nothing in the
// log to match against. The message is asserted for the facts a reader needs --
// both port numbers -- rather than for wording, because the wording will change
// and the facts will not.
func TestFallbackSaysWhichPortItMovedTo(t *testing.T) {
	base := freeQuad(t)
	wire, web := base, base+1
	occupyPort(t, wire)

	h := startHubOnPorts(t, wire, web)
	got := portOf(t, h.P2PAddr().String())

	var match *LogEvent
	for _, e := range h.EventLogger().GetRecent() {
		if strings.Contains(e.Message, strconv.Itoa(wire)) &&
			strings.Contains(e.Message, strconv.Itoa(got)) {
			copied := e
			match = &copied
			break
		}
	}
	if match == nil {
		t.Fatalf("no log entry names both the occupied port %d and the port actually used %d; a "+
			"silent port move is indistinguishable from a broken install. Recent entries:\n%s",
			wire, got, recentMessages(h))
	}
	if match.Level != LevelAction {
		t.Errorf("the port-move notice is logged at %s, want ACTION; someone looking at the "+
			"dashboard should see this without turning debug output on", match.Level)
	}
}

// TestFallbackIsSkippedWhenTheConfiguredPortIsFree guards the other direction: a
// fallback that fires when nothing is wrong would rewrite every peer's address
// for no reason on every restart.
func TestFallbackIsSkippedWhenTheConfiguredPortIsFree(t *testing.T) {
	base := freeQuad(t)
	wire, web := base, base+1

	h := startHubOnPorts(t, wire, web)
	if got := portOf(t, h.P2PAddr().String()); got != wire {
		t.Errorf("Hub moved from the free configured wire port %d to %d; nothing was in the way",
			wire, got)
	}
	for _, e := range h.EventLogger().GetRecent() {
		if strings.Contains(e.Message, "was already in use") {
			t.Errorf("a port-move notice was logged although %d was free: %q", wire, e.Message)
		}
	}
}

// TestFallbackGivesUpLoudlyWhenEveryPortIsTaken keeps the honest failure.
//
// The scan is four ports wide on purpose. When all of them are refused the Hub
// must still refuse to start: a Hub with no wire port cannot pair, and a silent
// partial start would be worse than a clear refusal.
func TestFallbackGivesUpLoudlyWhenEveryPortIsTaken(t *testing.T) {
	base := freeQuad(t)
	// The scan walks *down* from the configured port, so the port under test is
	// the top of the verified run and the occupied set is everything beneath it.
	// Occupying ports below the run would be occupying ports nothing verified.
	wire, web := base+4, base
	for i := 0; i < 5; i++ {
		occupyPort(t, base+i)
	}

	h, err := NewHub(HubConfig{
		TransportType: "lan",
		ListenAddr:    fmt.Sprintf("127.0.0.1:%d", wire),
		WebAddr:       fmt.Sprintf("127.0.0.1:%d", web),
		StoreDir:      t.TempDir(),
		OutputDir:     t.TempDir(),
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Start(ctx) }()

	// Ready() waking is not the assertion -- it wakes on failure too, by design.
	// What must hold is that Start reported an error, because a Hub with no wire
	// port cannot pair and a silent partial start would be worse than a refusal.
	select {
	case <-h.Ready():
	case <-time.After(15 * time.Second):
		t.Fatalf("Start never returned while all 4 candidate wire ports were occupied")
	}
	if err := h.Err(); err == nil {
		t.Fatalf("Hub started with all 4 candidate wire ports occupied; it has no wire port " +
			"and cannot pair, so this must be a refusal")
	}
	if !strings.Contains(h.Err().Error(), strconv.Itoa(wire)) {
		t.Errorf("the refusal does not name the configured wire port %d, so the user is told "+
			"something went wrong but not which port to look at: %v", wire, h.Err())
	}
}

// assertServes proves the web port is really answering, because a number in a
// struct is not a listener.
func assertServes(t *testing.T, webAddr string) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + webAddr + "/")
	if err != nil {
		t.Fatalf("the web port %s does not serve: %v", webAddr, err)
	}
	_ = resp.Body.Close()
}

func recentMessages(h *Hub) string {
	var b strings.Builder
	for _, e := range h.EventLogger().GetRecent() {
		b.WriteString("  " + e.Message + "\n")
	}
	if b.Len() == 0 {
		return "  (the event log is empty)"
	}
	return b.String()
}
