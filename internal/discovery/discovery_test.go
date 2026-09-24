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
	// Realistic identities so the beacon passes wire validation and the
	// self-filtering path is genuinely exercised (not vacuously empty).
	myFP := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mySAS := "123456"

	e, err := NewEngine(DiscoveryConfig{
		NodeName:      "test-node-self",
		WirePort:      9877,
		SAS:           mySAS,
		Fingerprint:   myFP,
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

	// Wait for multiple broadcast cycles
	time.Sleep(250 * time.Millisecond)

	nodes := e.ListNodes()
	for _, n := range nodes {
		if n.Fingerprint == myFP || n.SAS == mySAS {
			t.Errorf("self-filtering failed: found self in discovered nodes: %+v", n)
		}
	}
}

func TestEngine_DiscoveryRoundTrip(t *testing.T) {
	port := 19880

	e1, err := NewEngine(DiscoveryConfig{
		NodeName:      "node-alpha",
		WirePort:      9871,
		SAS:           "alpha1",
		Fingerprint:   "fp-alpha-001",
		BroadcastPort: port,
		Interval:      50 * time.Millisecond,
		TTL:           2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewEngine e1 failed: %v", err)
	}

	// Test directly recording an incoming beacon
	node := DiscoveredNode{
		InstanceName: "node-beta",
		Address:      "192.168.1.50:9872",
		Port:         9872,
		SAS:          "beta02",
		Fingerprint:  "fp-beta-002",
		LastSeen:     time.Now(),
	}

	var callbackCalled bool
	var cbMu sync.Mutex
	e1.OnNodeDiscovered(func(discovered DiscoveredNode) {
		cbMu.Lock()
		defer cbMu.Unlock()
		if discovered.Fingerprint == "fp-beta-002" {
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
		SAS:           "exp123",
		Fingerprint:   "fp-exp",
		BroadcastPort: 19881,
		Interval:      20 * time.Millisecond,
		TTL:           60 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	// Insert active node
	e.recordNode(DiscoveredNode{
		InstanceName: "soon-to-expire",
		Address:      "10.0.0.5:9877",
		Port:         9877,
		SAS:          "old001",
		Fingerprint:  "fp-old",
		LastSeen:     time.Now().Add(-100 * time.Millisecond), // already older than TTL
	})

	// Before pruning, ListNodes filters by TTL
	nodes := e.ListNodes()
	if len(nodes) != 0 {
		t.Errorf("expected 0 active nodes due to TTL, got %d", len(nodes))
	}

	// Trigger prune
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
		SAS:           "res123",
		Fingerprint:   "fp-res",
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

	// Send garbage UDP packets directly to broadcast port
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatalf("DialUDP failed: %v", err)
	}
	defer conn.Close()

	_, _ = conn.Write([]byte("GARBAGE_PACKET_NOT_AUTHDISC"))
	_, _ = conn.Write([]byte(BeaconPrefix + "{invalid-json-content}"))
	_, _ = conn.Write([]byte(BeaconPrefix + `{"v":1,"port":-1}`))

	time.Sleep(100 * time.Millisecond)

	// Engine should survive and have no nodes registered
	nodes := e.ListNodes()
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes from malformed packets, got %d", len(nodes))
	}
}

func TestEngine_ValidUDPBeacon(t *testing.T) {
	port := 19883
	e, err := NewEngine(DiscoveryConfig{
		NodeName:      "receiver-node",
		WirePort:      9877,
		SAS:           "rcv123",
		Fingerprint:   "fp-receiver",
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

	// Send valid beacon packet over UDP
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatalf("DialUDP failed: %v", err)
	}
	defer conn.Close()

	b := beaconPayload{
		Version: 1,
		Name:    "remote-peer-test",
		Port:    9888,
		SAS:     "beef01",
		FP:      "9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa",
	}
	payload, _ := json.Marshal(b)
	packet := append([]byte(BeaconPrefix), payload...)

	_, err = conn.Write(packet)
	if err != nil {
		t.Fatalf("Write packet failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		nodes := e.ListNodes()
		for _, n := range nodes {
			if n.Fingerprint == "9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa" && n.InstanceName == "remote-peer-test" {
				found = true
				if !strings.HasSuffix(n.Address, ":9888") {
					t.Errorf("expected address to end with :9888, got %s", n.Address)
				}
				break
			}
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

func TestValidBeacon_RejectsMalformedIdentity(t *testing.T) {
	goodFP := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cases := []struct {
		name   string
		beacon beaconPayload
		want   bool
	}{
		{"valid", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: "a1b2c3", FP: goodFP}, true},
		{"empty fp", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: "a1b2c3", FP: ""}, false},
		{"short fp", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: "a1b2c3", FP: "abcd"}, false},
		{"non-hex fp", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: "a1b2c3", FP: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}, false},
		{"empty sas", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: "", FP: goodFP}, false},
		{"long sas", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: "toolong7", FP: goodFP}, false},
		{"non-hex sas", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: "zzzzzz", FP: goodFP}, false},
		{"bad version", beaconPayload{Version: 2, Name: "n", Port: 9877, SAS: "a1b2c3", FP: goodFP}, false},
		{"bad port", beaconPayload{Version: 1, Name: "n", Port: 0, SAS: "a1b2c3", FP: goodFP}, false},
		{"control in name", beaconPayload{Version: 1, Name: "a\x01b", Port: 9877, SAS: "a1b2c3", FP: goodFP}, false},
	}
	for _, tc := range cases {
		if got := validBeacon(tc.beacon); got != tc.want {
			t.Errorf("validBeacon(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func FuzzValidBeacon(f *testing.F) {
	seeds := []string{
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
		if validBeacon(b) {
			if !isHexFingerprint(b.FP) || !isSASCode(b.SAS) {
				t.Fatalf("valid beacon with malformed identity: %+v", b)
			}
			if b.Version != 1 || b.Port <= 0 || b.Port > 65535 {
				t.Fatalf("valid beacon with bad version/port: %+v", b)
			}
		}
	})
}

func TestEngine_SelfFilterSASCollision(t *testing.T) {
	// A legitimate peer whose 24-bit SAS collides with ours must not be
	// hidden when our fingerprint is configured: FP comparison is exact.
	e, err := NewEngine(DiscoveryConfig{
		NodeName:      "self-node",
		WirePort:      9877,
		SAS:           "a1b2c3",
		Fingerprint:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BroadcastPort: 19884,
		Interval:      time.Hour,
		TTL:           time.Minute,
	})
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	// Simulate an inbound beacon from a different node with a colliding SAS.
	e.recordNode(DiscoveredNode{
		InstanceName: "other-node",
		Address:      "192.168.1.99:9877",
		Port:         9877,
		SAS:          "a1b2c3",
		Fingerprint:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		LastSeen:     time.Now(),
	})
	nodes := e.ListNodes()
	if len(nodes) != 1 || nodes[0].InstanceName != "other-node" {
		t.Fatalf("SAS-colliding peer hidden: %+v", nodes)
	}
}
