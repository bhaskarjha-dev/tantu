package hub

// The peer store could not be read, and the product said "no trusted peers".
//
// The store is the product's record of which machines it trusts. A store that
// fails to parse leaves the product unable to say who it trusts, and every
// caller that wanted a list got one that looked exactly like "you have no
// trusted peers" -- so a user whose pairing record was lost was told they had
// never paired at all, and would pair again with a machine they had already
// paired.
//
// The distinction is the user's trust decisions, which is the most expensive
// kind of claim to be wrong about. These gates hold the difference: the status
// response carries the fault, the fault names the file, and a store that is
// merely EMPTY still reports no fault at all.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// statusPeerStoreFault reads /api/status and returns the peer list and the
// peer-store fault it reported.
func statusPeerStoreFault(t *testing.T, h *Hub) ([]map[string]any, string) {
	t.Helper()
	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/status")
	if err != nil {
		t.Fatalf("GET /api/status failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/status returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /api/status: %v", err)
	}
	var status struct {
		Peers          []map[string]any `json:"peers"`
		PeerStoreError string           `json:"peer_store_error"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("decode /api/status: %v\n%s", err, body)
	}
	return status.Peers, status.PeerStoreError
}

// TestStatusReportsAnUnreadablePeerStore is the gate on the fault itself.
func TestStatusReportsAnUnreadablePeerStore(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	peersPath := filepath.Join(h.cfg.StoreDir, "peers.json")
	if err := os.WriteFile(peersPath, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("write the corrupt peer store: %v", err)
	}

	peers, fault := statusPeerStoreFault(t, h)
	if fault == "" {
		t.Fatalf("an unreadable peer store was reported as no fault at all, so the dashboard " +
			"still says the user has no trusted peers")
	}
	if !strings.Contains(fault, "peers.json") {
		t.Errorf("the fault does not name the file that is broken: %q", fault)
	}
	if len(peers) != 0 {
		t.Errorf("a store that could not be read reported %d peer(s); it can only report none, "+
			"and the fault is the point", len(peers))
	}
}

// TestStatusReportsNoFaultForAnEmptyPeerStore is the control, and it matters
// as much as the case above: without it, a gate that reported a fault
// unconditionally would pass.
func TestStatusReportsNoFaultForAnEmptyPeerStore(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	peers, fault := statusPeerStoreFault(t, h)
	if fault != "" {
		t.Errorf("a peer store with no peers in it reported a fault: %q", fault)
	}
	if len(peers) != 0 {
		t.Errorf("a fresh Hub reported %d peer(s)", len(peers))
	}
}

// TestDashboardRendersAPeerStoreFaultRatherThanTheEmptyState is the gate on
// what the user is shown.
//
// The empty state says "No paired peers yet" and offers two routes forward.
// That is a claim about the user's trust decisions, and it is the wrong claim
// for a store that could not be read: the routes forward are all still there,
// but the reason nothing is paired is a file, not a decision.
func TestDashboardRendersAPeerStoreFaultRatherThanTheEmptyState(t *testing.T) {
	// The comments are stripped, so these assertions are about code and not
	// about prose that happens to mention the same words.
	code := stripDashboardComments(dashboardHTML)
	if !strings.Contains(code, "peerStoreFaultHTML") {
		t.Fatal("the dashboard has no rendering for a peer store that could not be read, so a " +
			"corrupt store still arrives as the empty state")
	}
	if !strings.Contains(code, "peer_store_error") {
		t.Fatal("the dashboard never reads the peer-store fault from the status response")
	}
	// The fault has to be part of the render signature, or a fault that
	// appears while the peer list is unchanged would never be painted.
	if !strings.Contains(code, "'|' + (peerStoreFault || '')") {
		t.Error("the peer-store fault is not part of the render signature, so a fault that " +
			"appears while the peer list is unchanged would never be shown")
	}
	// And the empty state must still be reachable, or the fault would have
	// replaced it everywhere.
	if !strings.Contains(code, "EMPTY_PEERS_HTML") {
		t.Error("the empty state is gone from the dashboard; a store that is merely empty " +
			"must still render it")
	}
}
