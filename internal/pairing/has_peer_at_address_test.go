package pairing

import "testing"

// Discovery correlates a discovered hub to a paired peer only by address
// host, because beacons carry no identity since the cleartext SAS was
// removed. The match must therefore ignore the port (the pairing listener
// uses 9878 while the wire port is 9877), accept the bare-host form, and
// never report a different machine as paired.
func TestPeerStoreHasPeerAtAddress(t *testing.T) {
	store, err := NewPeerStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	if err := store.AddPeer(Peer{
		Name:        "paired-pc",
		Address:     "192.168.1.100:9878",
		Fingerprint: "abcd1234ef567890",
	}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	cases := []struct {
		addr string
		want bool
	}{
		{"192.168.1.100:9878", true},  // exact stored address
		{"192.168.1.100:9877", true},  // same machine, wire port instead of pairing port
		{"192.168.1.100", true},       // bare host form
		{"192.168.1.101:9877", false}, // a different machine must never match
		{"192.168.1.10", false},       // a prefix of the stored host must not match
		{"", false},
		{"   ", false},
	}
	for _, tc := range cases {
		if got := store.HasPeerAtAddress(tc.addr); got != tc.want {
			t.Errorf("HasPeerAtAddress(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}

	// An empty store must report nothing as paired rather than guessing.
	fresh, err := NewPeerStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerStore (fresh): %v", err)
	}
	if fresh.HasPeerAtAddress("192.168.1.100:9877") {
		t.Error("a store with no peers must not report a match")
	}
}
