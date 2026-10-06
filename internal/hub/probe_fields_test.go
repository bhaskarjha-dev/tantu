package hub

// Gates for the runtime facts the CLI needs from the Hub.
//
// This file exists because of a defect that a green suite missed twice. `tantu
// pair` starts a dedicated pairing listener and has to avoid the port the local
// Hub already owns. It compared against the literal default 9877 rather than the
// Hub's real port, so any Hub on another port silently reused it. The first fix
// compared against status.P2PAddr -- and passed its own tests, because
// `hubP2PPort` was handed synthetic strings while the field it read was never
// actually sent. The comparison was correct and had never been given a real
// value.
//
// So these gates do not test the parsing. They test that the wire actually
// carries the value the CLI depends on, which is the part that was missing.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestProbeReportsTheWireAddressTheCLIDependsOn is the gate for that. A unit
// test of hubP2PPort proves the parse; this proves the fact is available.
func TestProbeReportsTheWireAddressTheCLIDependsOn(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/probe")
	if err != nil {
		t.Fatalf("GET /api/probe: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/probe status = %d", resp.StatusCode)
	}
	var body struct {
		P2PAddr string `json:"p2p_addr"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	if strings.TrimSpace(body.P2PAddr) == "" {
		t.Fatal("/api/probe omits p2p_addr, so `tantu pair` cannot learn which port the Hub owns and will reuse it")
	}
	// It must be a real address with a port, not just present.
	if !strings.Contains(body.P2PAddr, ":") {
		t.Errorf("p2p_addr = %q, which carries no port", body.P2PAddr)
	}
}

// TestStatusStillReportsTheWireAddress guards the sibling endpoint, which the
// pairing command may reach instead depending on how it probes.
func TestStatusStillReportsTheWireAddress(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()
	var status HubStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.P2PAddr == "" {
		t.Error("/api/status omits p2p_addr; the CLI has no way to avoid the Hub's own port")
	}
}

// TestProbeStillOmitsWhatItPromisedToOmit pins the other direction. The probe is
// deliberately minimal, and adding a field must not quietly turn it into a
// second /api/status.
func TestProbeStillOmitsWhatItPromisedToOmit(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/probe")
	if err != nil {
		t.Fatalf("GET /api/probe: %v", err)
	}
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	for _, forbidden := range []string{"identity", "peers", "logs", "received", "tokens", "keys"} {
		if _, present := raw[forbidden]; present {
			t.Errorf("/api/probe now exposes %q; the endpoint is documented as omitting identity, peer and content data", forbidden)
		}
	}
}
