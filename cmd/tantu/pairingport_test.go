package main

// Gates for the dedicated pairing listener's port selection.
//
// This exists because a real defect survived reading and survived the suite.
// The responder bumped the listener off the Hub's port only when the port
// equalled the literal default 9877, so a Hub on any other p2p port -- a
// configured one, or a second Hub, which is precisely the case the loopback
// transport exists for -- announced a port the Hub already owned. Measured with
// a Hub on 19702: `tantu pair -port 19702` printed "Waiting for peer connection
// on 0.0.0.0:19702..." and then produced no further output, while the
// initiator's connection reached the Hub's p2p listener and returned "wsarecv:
// An existing connection was forcibly closed by the remote host".
//
// The rules under test were extracted out of the middle of the responder so this
// file gates production code. An earlier attempt defined the rule inside the test
// and asserted on it, which proved only that the test's own arithmetic worked --
// the same mistake as asserting on a hand-built value, one level down.

import (
	"net"
	"testing"
)

func TestHubP2PPortExtractsTheRealPort(t *testing.T) {
	cases := []struct {
		addr string
		want int
		ok   bool
	}{
		{"127.0.0.1:19702", 19702, true},
		{"[::]:9877", 9877, true},
		{"0.0.0.0:9878", 9878, true},
		// A malformed or absent address must report false rather than a guess:
		// a wrong port here would move the listener off a port that was never
		// in conflict, and the user would be told the wrong command to run.
		{"", 0, false},
		{"not-an-address", 0, false},
		{"127.0.0.1:notaport", 0, false},
		{"127.0.0.1:0", 0, false},
		{"127.0.0.1:70000", 0, false},
	}
	for _, c := range cases {
		got, ok := hubP2PPort(c.addr)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("hubP2PPort(%q) = (%d, %v), want (%d, %v)", c.addr, got, ok, c.want, c.ok)
		}
	}
}

func TestChoosePairingPortMovesOffTheHubPort(t *testing.T) {
	cases := []struct {
		name      string
		requested int
		hubPort   int
		want      int
		moved     bool
	}{
		// The defect: the old guard compared to 9877, so this case got no
		// adjustment at all and the responder sat on the Hub's port.
		{"collision with a non-default hub port", 19702, 19702, 19703, true},
		{"collision with the default hub port", 9877, 9877, 9878, true},
		{"no collision is left alone", 19712, 19702, 19712, false},
		{"explicit port below the hub port", 9000, 19702, 9000, false},
		// A Hub that did not report a port must not cause a bump. Bumping
		// unconditionally would silently move a perfectly good explicit port
		// and change the command printed for the other machine.
		{"unknown hub port leaves the request alone", 9877, 0, 9877, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, moved := choosePairingPort(c.requested, c.hubPort)
			if got != c.want || moved != c.moved {
				t.Errorf("choosePairingPort(%d, %d) = (%d, %v), want (%d, %v)",
					c.requested, c.hubPort, got, moved, c.want, c.moved)
			}
			if moved && got == c.hubPort {
				t.Errorf("reported a move but stayed on the Hub port %d", c.hubPort)
			}
		})
	}
}

// TestHubPortCollisionIsProducedNotAssumed produces the collision rather than
// asserting on a table row, because that is the whole lesson of the defect: the
// port the Hub owns is genuinely unavailable, and the port the responder moves
// to is genuinely bindable. If this ever skips, the produced-condition coverage
// is gone and only the arithmetic remains.
func TestHubPortCollisionIsProducedNotAssumed(t *testing.T) {
	hub, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Skipf("cannot bind a UDP socket on this host: %v", err)
	}
	defer hub.Close()
	hubPort := hub.LocalAddr().(*net.UDPAddr).Port

	// The condition: that port cannot be bound again.
	if probe, perr := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: hubPort}); perr == nil {
		probe.Close()
		t.Skipf("port %d accepted a second bind, so this host does not reproduce the collision", hubPort)
	}

	// The response: the rule moves off it, onto something bindable.
	chosen, moved := choosePairingPort(hubPort, hubPort)
	if !moved || chosen == hubPort {
		t.Fatalf("choosePairingPort(%d, %d) = (%d, %v): the responder would sit on the Hub's port", hubPort, hubPort, chosen, moved)
	}
	next, berr := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: chosen})
	if berr != nil {
		t.Fatalf("moved to port %d but that is not bindable either: %v", chosen, berr)
	}
	next.Close()
}
