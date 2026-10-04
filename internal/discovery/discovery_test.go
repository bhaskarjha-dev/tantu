package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func strPtr(s string) *string { return &s }

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

	// The contract is that OUR beacon is never recorded - not that the
	// machine is quiet. Discovery is shared by design (every engine joins
	// the same group), and CI runs package test binaries in parallel: run
	// #37 failed this test's old zero-nodes assertion because a concurrent
	// lan-hub test's beacons (the runner's hostname) were discovered
	// correctly. Foreign instances may legitimately appear; our own name
	// must not.
	assertOwnNotRecorded := func(stage string) {
		t.Helper()
		for _, n := range e.ListNodes() {
			if n.InstanceName == "test-node-self" {
				t.Fatalf("%s: own beacon recorded as a peer: %+v", stage, n)
			}
		}
	}

	// Wait for multiple broadcast cycles so our own echo returns to us.
	time.Sleep(250 * time.Millisecond)
	assertOwnNotRecorded("broadcast echo")

	// Deterministic teeth: replay a beacon carrying the engine's own nonce
	// straight at its listener, so the filter stays exercised on hosts
	// where broadcast never loops back to the sender (and so the check
	// cannot pass vacuously because nothing was received).
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 19879})
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
	send(beaconPayload{Version: 1, Name: "test-node-self", Port: 9877, BeaconNonce: e.beaconNonce})
	// Delivery time, and it leaves the 50ms per-source rate window clear
	// for the probe below: self beacons are dropped before the limiter, so
	// only spacing against real traffic matters here.
	time.Sleep(120 * time.Millisecond)
	assertOwnNotRecorded("own-nonce replay")

	// Non-vacuous: a foreign beacon must still be recorded, so an engine
	// that filters everything (or never receives anything) cannot pass.
	send(beaconPayload{Version: 1, Name: "probe-peer", Port: 9877, BeaconNonce: "probe"})
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, n := range e.ListNodes() {
			if n.InstanceName == "probe-peer" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("foreign beacon not recorded; the self-filter checks above prove nothing: %+v", e.ListNodes())
		}
		time.Sleep(20 * time.Millisecond)
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

	// The contract is that the malformed packets created no nodes - not
	// that the table is empty: other instances on this machine are
	// legitimately discovered (discovery is shared by design; CI runs
	// package tests in parallel). Only coordinates that broken validation
	// could have accepted - out-of-range port, empty or control-bearing
	// name - can come from the three malformed packets sent above; every
	// other node in the table arrived as a valid beacon.
	for _, n := range e.ListNodes() {
		if n.Port < 1 || n.Port > 65535 || n.InstanceName == "" || containsControl(n.InstanceName) {
			t.Errorf("malformed packet was recorded as a node: %+v", n)
		}
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
		{"legacy sas only", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: strPtr("a1b2c3")}, false},
		{"legacy fp only", beaconPayload{Version: 1, Name: "n", Port: 9877, FP: strPtr(strings.Repeat("a", 64))}, false},
		{"legacy both", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: strPtr("a1b2c3"), FP: strPtr(strings.Repeat("b", 64))}, false},
		{"legacy sas present but empty", beaconPayload{Version: 1, Name: "n", Port: 9877, SAS: strPtr("")}, false},
		{"legacy fp present but empty", beaconPayload{Version: 1, Name: "n", Port: 9877, FP: strPtr("")}, false},
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
// and the UI must not be able to present it as verified. DiscoveredNode has no
// identity fields at all (the compiler enforces that), so this pins the
// serialized shape: adding an identity key later fails here.
func TestDiscoveredNodeNeverCarriesIdentity(t *testing.T) {
	e, err := NewEngine(DiscoveryConfig{NodeName: "n", WirePort: 9877, BroadcastPort: 19899})
	if err != nil {
		t.Fatal(err)
	}
	e.recordNode(DiscoveredNode{
		InstanceName: "hint",
		Address:      "10.0.0.9:9877",
		Port:         9877,
		LastSeen:     time.Now(),
	})
	nodes := e.ListNodes()
	if len(nodes) != 1 {
		t.Fatalf("expected one node, got %d", len(nodes))
	}

	encoded, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode discovered node list: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected one serialized node, got %d", len(decoded))
	}
	allowed := map[string]bool{"name": true, "addr": true, "port": true, "last_seen": true}
	for key := range decoded[0] {
		if !allowed[key] {
			t.Errorf("discovered node serialized unexpected key %q: %s", key, encoded)
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
		if b.SAS != nil || b.FP != nil {
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

// End to end: our own broadcast echo is filtered and a foreign peer's beacon
// is discovered. Legacy identity-bearing rejection is covered separately by
// TestEngine_RejectsLegacyBeaconEndToEnd, which orders packets so the
// per-source rate limiter cannot mask a validation failure.
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

	deadline := time.Now().Add(2 * time.Second)
	for {
		// Name-based contract: the foreign peer must be recorded and our own
		// name must not appear. Other instances on this machine are fair
		// game for the table (discovery is shared by design; CI runs package
		// test binaries in parallel), so counting entries is not this
		// machine's contract - run #37's failures were all count assertions
		// hit by legitimate foreign beacons.
		foundForeign := false
		for _, n := range e.ListNodes() {
			if n.InstanceName == "self-node" {
				t.Fatalf("own beacon recorded as a peer: %+v", n)
			}
			if n.InstanceName == "foreign" {
				foundForeign = true
			}
		}
		if foundForeign {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("foreign peer not discovered: %+v", e.ListNodes())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Pre-d7de281 peers declared "sas" and "fp" without omitempty, so a legacy
// scanner with nothing to advertise emitted them as empty strings. Rejecting
// only non-empty values accepts that beacon, contradicting the release note
// that any beacon still setting those fields is dropped.
func TestValidBeaconRejectsPresentButEmptyLegacyFields(t *testing.T) {
	for _, raw := range []string{
		`{"v":1,"name":"legacy","port":9877,"sas":"","fp":""}`,
		`{"v":1,"name":"legacy","port":9877,"sas":""}`,
		`{"v":1,"name":"legacy","port":9877,"fp":""}`,
	} {
		var b beaconPayload
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if validBeacon(b) {
			t.Errorf("accepted a legacy beacon that still sets identity fields: %s", raw)
		}
	}
}

// End to end for that rejection, structured so it cannot pass vacuously: the
// legacy packet is sent FIRST, so the per-source rate limiter lets it through
// and only validation can be what refuses it; a well-formed peer from the same
// source is then sent to prove the engine is alive. The old ordering sent the
// valid beacon first and the legacy one was dropped by the 50ms rate limiter
// regardless of validation.
func TestEngine_RejectsLegacyBeaconEndToEnd(t *testing.T) {
	port := 19887
	e, err := NewEngine(DiscoveryConfig{
		NodeName:      "rejector",
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
	send := func(raw string) {
		t.Helper()
		if _, err := conn.Write([]byte(BeaconPrefix + raw)); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}

	send(`{"v":1,"name":"legacy","port":9877,"sas":"","fp":""}`)
	legacyDeadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(legacyDeadline) {
		for _, n := range e.ListNodes() {
			if n.InstanceName == "legacy" {
				t.Fatalf("legacy identity-bearing beacon was recorded as a node: %+v", n)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	send(`{"v":1,"name":"current","port":9877,"nonce":"theirs"}`)
	acceptDeadline := time.Now().Add(2 * time.Second)
	for {
		nodes := e.ListNodes()
		for _, n := range nodes {
			if n.InstanceName == "current" {
				return
			}
			if n.InstanceName == "legacy" {
				t.Fatalf("legacy beacon recorded: %+v", n)
			}
		}
		if time.Now().After(acceptDeadline) {
			t.Fatalf("well-formed peer not discovered, so the rejection above proves nothing: %+v", nodes)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// One source must not be able to empty the discovery table. Claiming rotating
// ports manufactures distinct Address keys, and the global oldest-first
// eviction then removes every legitimate peer.
func TestRecordNodeBoundsPerSourceHost(t *testing.T) {
	const maxNodesPerSourceHost = 8 // policy bound asserted independently of the implementation
	e, err := NewEngine(DiscoveryConfig{NodeName: "n", WirePort: 9877, BroadcastPort: 19898})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.recordNode(DiscoveredNode{
		InstanceName: "legit",
		Address:      "10.9.9.9:9877",
		Port:         9877,
		LastSeen:     now,
	})
	for i := 0; i < 400; i++ {
		e.recordNode(DiscoveredNode{
			InstanceName: "spoof",
			Address:      fmt.Sprintf("10.0.0.5:%d", 1024+i),
			Port:         9877,
			LastSeen:     now.Add(time.Duration(i+1) * time.Millisecond),
		})
	}

	nodes := e.ListNodes()
	legitFound := false
	perHost := 0
	for _, n := range nodes {
		if n.InstanceName == "legit" && n.Address == "10.9.9.9:9877" {
			legitFound = true
		}
		if strings.HasPrefix(n.Address, "10.0.0.5:") {
			perHost++
		}
	}
	if !legitFound {
		t.Errorf("a single source host evicted every legitimate peer from discovery (%d nodes left)", len(nodes))
	}
	if perHost > maxNodesPerSourceHost {
		t.Errorf("one source host occupies %d table slots, want <= %d", perHost, maxNodesPerSourceHost)
	}
}
