package discovery

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEngine_SelfFiltering(t *testing.T) {
	e, err := NewEngine(DiscoveryConfig{
		NodeName:      "test-node-self",
		WirePort:      9877,
		BroadcastPort: 19879,
		Interval:      100 * time.Millisecond,
		TTL:           500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := e.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer e.Close()

	// Wait for multiple broadcast cycles so our own echo returns to us.
	time.Sleep(250 * time.Millisecond)

	if nodes := e.ListNodes(); len(nodes) != 0 {
		t.Errorf("self-filtering failed: found self in discovered nodes: %+v", nodes)
	}
}

func TestEngine_DiscoveryRoundTrip(t *testing.T) {
	e1, err := NewEngine(DiscoveryConfig{
		NodeName:      "node-alpha",
		WirePort:      9871,
		BroadcastPort: 19880,
		Interval:      50 * time.Millisecond,
		TTL:           2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewEngine e1 failed: %v", err)
	}

	node := DiscoveredNode{
		InstanceName: "node-beta",
		Address:      "192.168.1.50:9872",
		Port:         9872,
		LastSeen:     time.Now(),
	}

	var callbackCalled bool
	var cbMu sync.Mutex
	e1.OnNodeDiscovered(func(discovered DiscoveredNode) {
		cbMu.Lock()
		defer cbMu.Unlock()
		if discovered.InstanceName == "node-beta" {
			callbackCalled = true
		}
	})

	e1.recordNode(node)

	nodes := e1.ListNodes()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if nodes[0].InstanceName != "node-beta" {
		t.Errorf("expected node-beta, got %s", nodes[0].InstanceName)
	}
	if nodes[0].Port != 9872 {
		t.Errorf("expected port 9872, got %d", nodes[0].Port)
	}

	cbMu.Lock()
	called := callbackCalled
	cbMu.Unlock()
	if !called {
		t.Error("expected callback to be called on discovery")
	}
}

func TestEngine_TTLExpiration(t *testing.T) {
	e, err := NewEngine(DiscoveryConfig{
		NodeName:      "test-exp-node",
		WirePort:      9877,
		BroadcastPort: 19881,
		Interval:      20 * time.Millisecond,
		TTL:           60 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	// Insert a node already older than the TTL.
	e.recordNode(DiscoveredNode{
		InstanceName: "soon-to-expire",
		Address:      "10.0.0.5:9877",
		Port:         9877,
		LastSeen:     time.Now().Add(-100 * time.Millisecond),
	})

	if nodes := e.ListNodes(); len(nodes) != 0 {
		t.Errorf("expected 0 active nodes due to TTL, got %d", len(nodes))
	}

	e.pruneStaleNodes()

	e.mu.RLock()
	rawLen := len(e.nodes)
	e.mu.RUnlock()
	if rawLen != 0 {
		t.Errorf("expected stale nodes to be deleted from map, got %d", rawLen)
	}
}

func TestEngine_MalformedPacket(t *testing.T) {
	port := 19882
	e, err := NewEngine(DiscoveryConfig{
		NodeName:      "resilient-node",
		WirePort:      9877,
		BroadcastPort: port,
		Interval:      100 * time.Millisecond,
		TTL:           1 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := e.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer e.Close()

	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatalf("DialUDP failed: %v", err)
	}
	defer conn.Close()

	_, _ = conn.Write([]byte("GARBAGE_PACKET_NOT_AUTHDISC"))
	_, _ = conn.Write([]byte(BeaconPrefix + "{invalid-json-content}"))
	_, _ = conn.Write([]byte(BeaconPrefix + `{"v":1,"port":-1}`))

	time.Sleep(100 * time.Millisecond)

	if nodes := e.ListNodes(); len(nodes) != 0 {
		t.Errorf("expected 0 nodes from malformed packets, got %d", len(nodes))
	}
}

func TestEngine_ValidUDPBeacon(t *testing.T) {
	port := 19883
	e, err := NewEngine(DiscoveryConfig{
		NodeName:      "receiver-node",
		WirePort:      9877,
		BroadcastPort: port,
		Interval:      100 * time.Millisecond,
		TTL:           1 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := e.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer e.Close()

	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatalf("DialUDP failed: %v", err)
	}
	defer conn.Close()

	payload, _ := json.Marshal(beaconPayload{
		Version: 1,
		Name:    "remote-peer-test",
		Port:    9888,
	})
	if _, err := conn.Write(append([]byte(BeaconPrefix), payload...)); err != nil {
		t.Fatalf("Write packet failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		for _, n := range e.ListNodes() {
			if n.InstanceName != "remote-peer-test" {
				continue
			}
			found = true
			if !strings.HasSuffix(n.Address, ":9888") {
				t.Errorf("expected address to end with :9888, got %s", n.Address)
			}
			break
		}
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !found {
		t.Fatal("expected peer to be discovered from UDP beacon")
	}
}

// A beacon carries no identity. This is the control that stops a network
// observer from learning the SAS a user is about to verify by eye, which would
// otherwise be enough to grind a matching certificate and defeat pairing.
func TestBeaconPayloadCarriesNoIdentity(t *testing.T) {
	payload, err := json.Marshal(beaconPayload{Version: 1, Name: "hub", Port: 9877, BeaconNonce: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, forbidden := range []string{`"sas"`, `"fp"`} {
		if strings.Contains(encoded, forbidden) {
			t.Errorf("a broadcast beacon must not carry %s: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(encoded, `"name"`) || !strings.Contains(encoded, `"port"`) {
		t.Errorf("a beacon must still advertise where to connect: %s", encoded)
	}
}

// A beacon from an older peer still carrying identity fields must be rejected
// outright, so the cleartext leak cannot be re-accepted by a mixed-version
// network.
func TestValidBeaconRejectsLegacyIdentityBearingBeacons(t *testing.T) {
	cases := []struct {
		name   string
		beacon beaconPayload
		want   bool
	}{
		{"no identity", beaconPayload{Version: 1, Name: "n", Port: 9877}, true},
		{"with nonce", beaconPayload{Version: 1, Name: "n", Port: 9877, BeaconNonce: "deadbeef"}, true},
		{"legacy sas only", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: "a1b2c3"}, false},
		{"legacy fp only", beaconPayload{Version: 1, Name: "n", Port: 9877, FP: strings.Repeat("a", 64)}, false},
		{"legacy both", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: "a1b2c3", FP: strings.Repeat("a", 64)}, false},
		{"bad version", beaconPayload{Version: 2, Name: "n", Port: 9877}, false},
		{"bad port", beaconPayload{Version: 1, Name: "n", Port: 0}, false},
		{"control in name", beaconPayload{Version: 1, Name: "a\x01b", Port: 9877}, false},
		{"overlong nonce", beaconPayload{Version: 1, Name: "n", Port: 9877, BeaconNonce: strings.Repeat("x", 65)}, false},
	}
	for _, tc := range cases {
		if got := validBeacon(tc.beacon); got != tc.want {
			t.Errorf("validBeacon(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A discovered node must never carry an identity: it is an unauthenticated hint,
// and the UI must not be able to present it as verified.
func TestDiscoveredNodeNeverCarriesIdentity(t *testing.T) {
	e, err := NewEngine(DiscoveryConfig{NodeName: "n", WirePort: 9877, BroadcastPort: 19899})
	if err != nil {
		t.Fatal(err)
	}
	e.recordNode(DiscoveredNode{
		InstanceName: "hint",
		Address:      "10.0.0.9:9877",
		Port:         9877,
		// Even if a caller supplies them, recordNode must not retain them.
		SAS:         "a1b2c3",
		Fingerprint: strings.Repeat("a", 64),
		LastSeen:    time.Now(),
	})
	nodes := e.ListNodes()
	if len(nodes) != 1 {
		t.Fatalf("expected one node, got %d", len(nodes))
	}
	if nodes[0].SAS != "" || nodes[0].Fingerprint != "" {
		t.Fatalf("a discovered node must not retain identity: sas=%q fp=%q", nodes[0].SAS, nodes[0].Fingerprint)
	}

	// The node list must also be free of identity once serialized.
	encoded, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"sas"`, `"fp"`, "a1b2c3", strings.Repeat("a", 64)} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("discovered node list leaked %q: %s", forbidden, encoded)
		}
	}
}

func FuzzValidBeacon(f *testing.F) {
	seeds := []string{
		`{"v":1,"name":"n","port":9877}`,
		`{"v":1,"name":"n","port":9877,"nonce":"abc"}`,
		`{"v":1,"name":"n","port":9877,"sas":"a1b2c3","fp":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		`{"v":2,"port":-1}`,
		`not json`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var b beaconPayload
		if err := json.Unmarshal(data, &b); err != nil {
			return
		}
		if !validBeacon(b) {
			return
		}
		// A beacon that is accepted must never be carrying identity, and must
		// always describe a reachable port on a known version.
		if b.SAS != "" || b.FP != "" {
			t.Fatalf("accepted beacon carries identity: %+v", b)
		}
		if b.Version != 1 || b.Port <= 0 || b.Port > 65535 {
			t.Fatalf("accepted beacon with bad version/port: %+v", b)
		}
	})
}

// Self-recognition now uses a per-start nonce rather than a fingerprint or SAS.
// A foreign beacon must never be filtered, and our own must always be.
func TestIsSelfBeaconUsesOnlyTheNonce(t *testing.T) {
	own := "our-nonce"
	other := beaconPayload{Version: 1, Name: "other", Port: 9877, BeaconNonce: "their-nonce"}
	if isSelfBeacon(own, other) {
		t.Fatal("a foreign beacon must not be classified as self")
	}
	if !isSelfBeacon(own, beaconPayload{Version: 1, Name: "self", Port: 9877, BeaconNonce: own}) {
		t.Fatal("our own beacon was not recognized")
	}
	// An engine without a nonce, or a beacon without one, must not match.
	if isSelfBeacon("", other) {
		t.Fatal("an empty nonce must not match anything")
	}
	if isSelfBeacon(own, beaconPayload{Version: 1, Name: "self", Port: 9877}) {
		t.Fatal("a nonce-less beacon must not be classified as self")
	}
}

// End to end: our own broadcast echo is filtered, a foreign peer's beacon is
// discovered, and a legacy identity-bearing beacon is refused.
func TestEngine_SelfFilterEndToEndUDP(t *testing.T) {
	port := 19885
	e, err := NewEngine(DiscoveryConfig{
		NodeName:      "self-node",
		WirePort:      9877,
		BroadcastPort: port,
		Interval:      time.Hour,
		TTL:           time.Minute,
	})
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer e.Close()

	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatalf("DialUDP failed: %v", err)
	}
	defer conn.Close()
	send := func(b beaconPayload) {
		t.Helper()
		payload, _ := json.Marshal(b)
		if _, err := conn.Write(append([]byte(BeaconPrefix), payload...)); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}
	send(beaconPayload{Version: 1, Name: "foreign", Port: 9877, BeaconNonce: "theirs"})
	// A legacy peer that still advertises its SAS must be ignored entirely.
	send(beaconPayload{Version: 1, Name: "legacy", Port: 9877, SAS: "a1b2c3",
		FP: strings.Repeat("b", 64)})

	deadline := time.Now().Add(2 * time.Second)
	for {
		nodes := e.ListNodes()
		if len(nodes) == 1 && nodes[0].InstanceName == "foreign" {
			return
		}
		if len(nodes) > 1 {
			t.Fatalf("unexpected nodes: %+v", nodes)
		}
		for _, n := range nodes {
			if n.InstanceName == "self-node" {
				t.Fatalf("own broadcast echo recorded as a peer: %+v", n)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("foreign peer not discovered: %+v", nodes)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
