package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/discovery"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// The dashboard's Nearby response must flag a discovered hub whose host
// matches a paired peer, must not flag strangers, and must not carry any
// identity field: discovery has none to report, so emitting sas/fingerprint
// would invite a UI to present unverified data as a verification code.
func TestBuildDiscoveredPeers(t *testing.T) {
	store, err := pairing.NewPeerStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	if err := store.AddPeer(pairing.Peer{
		Name:        "paired-pc",
		Address:     "192.168.1.100:9878",
		Fingerprint: "abcd1234ef567890",
	}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	nodes := []discovery.DiscoveredNode{
		{InstanceName: "paired-pc", Address: "192.168.1.100:9877", Port: 9877, LastSeen: time.Now()},
		{InstanceName: "stranger", Address: "192.168.1.50:9877", Port: 9877, LastSeen: time.Now()},
	}

	out := buildDiscoveredPeers(nodes, store)
	if len(out) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(out))
	}
	if !out[0].IsPaired {
		t.Error("a hub at a paired peer's host must be flagged is_paired; otherwise every paired device is offered for re-pairing")
	}
	if out[1].IsPaired {
		t.Error("an unknown host must not be flagged is_paired")
	}

	// Identity keys must never appear in the serialized response.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	allowed := map[string]bool{"name": true, "address": true, "port": true, "is_paired": true}
	for _, entry := range decoded {
		for key := range entry {
			if !allowed[key] {
				t.Errorf("discovery response carries unexpected key %q: %s", key, raw)
			}
		}
	}

	// A nil store must not panic and must claim nothing as paired.
	nilOut := buildDiscoveredPeers(nodes, nil)
	if nilOut[0].IsPaired || nilOut[1].IsPaired {
		t.Error("with no store nothing may be reported as paired")
	}
}
