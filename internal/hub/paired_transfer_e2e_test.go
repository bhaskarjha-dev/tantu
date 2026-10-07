package hub

// The gate for a transfer that depends on pairing trust.
//
// Two separate things were already covered, and neither covers this:
//
//   - A real Hub sending real bytes to a real Hub (relpath_e2e_test.go) over
//     loopback transport, where IsPeerTrusted returns true unconditionally. That
//     proves bytes survive the seams in validation, staging and publication, and
//     says nothing about trust.
//   - Two real Hubs pairing over LAN transport (unpair_notify_test.go), which
//     proves the ceremony and store convergence, and sends nothing.
//
// So "pair, then send" had never been exercised: the store resolution that turns
// a peer name into an address, and the trust check that decides whether A will
// talk to B at all, were never on the path of a real payload. KNOWN-LIMITATIONS
// 2.6 records the two-machine evidence as one-directional for exactly this
// reason.
//
// AutoAcceptPairing is the product's own unattended-pairing switch. Nothing here
// bypasses the SAS check: that lives in the CLI's interactive confirm callback,
// and this gate is about what happens to bytes once trust exists.
//
// Both drop kinds are covered because they land in different places, and getting
// that wrong is itself the kind of assumption this repository keeps paying for.
// A text drop is held in memory and surfaces through /api/drop/recent with its
// content inline; only a file drop is written into the output directory. An
// earlier version of this file sent text and then asserted a file appeared on
// disk, which failed -- and the failure was the test's error, not the product's.
//
// Two claims this file deliberately does NOT make, both because writing the gate
// disproved them:
//
//   - That the unpaired refusal is a trust decision. It is not: it is store
//     resolution, before a socket opens. See
//     TestUnpairedDestinationIsRefusedBeforeAnyConnection.
//   - That a peer serving an unexpected certificate is rejected at the handshake.
//     A gate asserting that reported a real-looking failure -- A accepted a
//     connection to a store record whose fingerprint belonged to a different
//     identity, and the response even said "verified": true. It is not a
//     vulnerability. `IsPeerTrusted` is `store.GetPeer(fp)`: the store *is* the
//     trust anchor, and the gate had written its own impostor into that anchor
//     with a direct AddPeer call, which no remote peer can do. The gate was
//     measuring its own shortcut.
//
// The property that actually matters is therefore narrower, and is already gated
// elsewhere: the store is only written through an approved pairing
// (TestWebDashboard_InboundPairing_PendingApprovalAndRejection asserts zero
// peers recorded before approval), and a record's certificate must match its own
// fingerprint (internal/pairing's TestPeerStore_CertFingerprintBinding).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// startPairedHubs brings up two real Hubs on LAN transport -- the transport
// that actually enforces IsPeerTrusted -- with unattended pairing, and returns
// them already paired. B writes into outB so publication is observable.
func startPairedHubs(t *testing.T, outB string) (a *Hub, b *Hub) {
	t.Helper()
	newHub := func(outDir string) *Hub {
		h, err := NewHub(HubConfig{
			TransportType:     "lan",
			ListenAddr:        "127.0.0.1:0",
			WebAddr:           "127.0.0.1:0",
			StoreDir:          t.TempDir(),
			OutputDir:         outDir,
			Headless:          true,
			AutoAcceptPairing: true,
		})
		if err != nil {
			t.Fatalf("NewHub: %v", err)
		}
		return h
	}
	a = newHub(t.TempDir())
	b = newHub(outB)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = a.Start(ctx) }()
	go func() { _ = b.Start(ctx) }()
	for name, h := range map[string]*Hub{"A": a, "B": b} {
		select {
		case <-h.Ready():
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for Hub %s", name)
		}
	}
	t.Cleanup(func() {
		_ = a.Stop()
		_ = b.Stop()
	})

	// A initiates, so A's store records B's exact wire address -- the address the
	// sends below resolve and dial.
	pairBody, _ := json.Marshal(map[string]string{"peer_addr": b.P2PAddr().String()})
	req, _ := http.NewRequest(http.MethodPost, "http://"+a.WebAddr()+"/api/pair/initiate", bytes.NewReader(pairBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+a.WebAddr())
	req.Header.Set(IPCTokenHeader, a.ipcToken)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("pair initiate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair initiate = %d, want 200", resp.StatusCode)
	}
	if got := len(a.Store().ListPeers()); got != 1 {
		t.Fatalf("A lists %d peers after pairing, want 1", got)
	}
	if got := len(b.Store().ListPeers()); got != 1 {
		t.Fatalf("B lists %d peers after pairing, want 1", got)
	}
	return a, b
}

// solePeerName returns the name a user would pick in the dashboard. The send
// resolves a peer the same way a real one does, so this exercises the store
// lookup rather than a hardcoded address.
func solePeerName(t *testing.T, a *Hub) string {
	t.Helper()
	peers := a.Store().ListPeers()
	if len(peers) != 1 {
		t.Fatalf("expected exactly 1 peer to send to, got %d", len(peers))
	}
	if peers[0].Alias != "" {
		return peers[0].Alias
	}
	return peers[0].Name
}

func postJSON(t *testing.T, url, token, origin string, body []byte) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set(IPCTokenHeader, token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.String()
}

// recentFrom fetches B's received-items buffer, which is where a text drop lands.
func recentFrom(t *testing.T, b *Hub) []ReceivedDropItem {
	t.Helper()
	resp, err := testGet(b.ipcToken, "http://"+b.WebAddr()+"/api/drop/recent")
	if err != nil {
		t.Fatalf("GET /api/drop/recent: %v", err)
	}
	defer resp.Body.Close()
	var items []ReceivedDropItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode recent: %v", err)
	}
	return items
}

// TestPairedHubsTransferTextOverLANTransport covers the text path: in-memory on
// B, surfaced through the same endpoint the dashboard polls.
func TestPairedHubsTransferTextOverLANTransport(t *testing.T) {
	a, b := startPairedHubs(t, t.TempDir())
	target := solePeerName(t, a)

	payload := "paired end to end: trust was established by pairing, not assumed by loopback"
	body, _ := json.Marshal(map[string]string{"text": payload, "name": "gate-note", "peer": target})
	code, respBody := postJSON(t, "http://"+a.WebAddr()+"/api/drop/upload", a.ipcToken, "http://"+a.WebAddr(), body)
	if code != http.StatusOK {
		t.Fatalf("upload = %d: %s", code, respBody)
	}

	deadline := time.Now().Add(10 * time.Second)
	var found *ReceivedDropItem
	for time.Now().Before(deadline) {
		for i := range recentFrom(t, b) {
			if items := recentFrom(t, b); items[i].Name == "gate-note" {
				found = &items[i]
				break
			}
		}
		if found != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if found == nil {
		t.Fatalf("B recorded no received item named gate-note within 10s; buffer holds %d item(s)", len(recentFrom(t, b)))
	}
	if found.Content != payload {
		t.Errorf("received content = %q, want %q", found.Content, payload)
	}
	if found.SavedPath != "" {
		t.Errorf("a text drop reported a saved path %q; text is held in memory", found.SavedPath)
	}
}

// TestPairedHubsTransferFileOverLANTransport is the stronger claim: a real file
// published into B's output directory, byte-for-byte, over a connection that
// required pairing. This is the path a user actually relies on.
func TestPairedHubsTransferFileOverLANTransport(t *testing.T) {
	outB := t.TempDir()
	a, _ := startPairedHubs(t, outB)
	target := solePeerName(t, a)

	payload := bytes.Repeat([]byte("tantu paired file payload 0123456789\n"), 4096) // 164 KiB
	want := sha256Hex(payload)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("peer", target)
	part, err := mw.CreateFormFile("file", "paired.bin")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://"+a.WebAddr()+"/api/drop/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", "http://"+a.WebAddr())
	req.Header.Set(IPCTokenHeader, a.ipcToken)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d, want 200", resp.StatusCode)
	}

	// Publication may be renamed to avoid a collision, so accept any published
	// file and require the digest to match rather than asserting a fixed name.
	deadline := time.Now().Add(20 * time.Second)
	var matched string
	for time.Now().Before(deadline) && matched == "" {
		entries, _ := os.ReadDir(outB)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(outB, e.Name()))
			if readErr != nil {
				continue
			}
			if sha256Hex(data) == want {
				matched = e.Name()
				break
			}
		}
		if matched == "" {
			time.Sleep(150 * time.Millisecond)
		}
	}
	if matched == "" {
		entries, _ := os.ReadDir(outB)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("B published no file matching the sent digest within 20s; output dir holds %v", names)
	}
	if !strings.HasPrefix(matched, "paired") {
		t.Errorf("published name %q does not derive from the sent name", matched)
	}
}

// TestPairedStoreRefusesAnImpostorRecordAtARealPeersAddress covers the layer
// between store resolution and the handshake, and it started life as a gate that
// skipped.
//
// The original intent was to pair two Hubs, then rewrite A's record so the
// stored fingerprint no longer matched B's, and assert the transfer was refused.
// The store refused the write outright: "peer certificate does not match
// fingerprint". That is the right behaviour and it means a stale-trust record
// cannot be *persisted* at all -- so the gate is rewritten to assert the
// invariant that actually holds, at the address of a genuinely paired peer.
//
// What this still does NOT cover, and is recorded in KNOWN-LIMITATIONS rather
// than implied: a peer that re-keys at runtime while keeping its address, so the
// mismatch appears on the wire rather than in the store. Building that needs a
// second Hub on a fixed port, restarted with a fresh store, which is a larger rig
// than this file sets up. internal/pairing's TestPeerStore_CertFingerprintBinding
// covers the store half; the handshake half remains unexercised.
func TestPairedStoreRefusesAnImpostorRecordAtARealPeersAddress(t *testing.T) {
	outB := t.TempDir()
	a, b := startPairedHubs(t, outB)

	peers := a.Store().ListPeers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	real := peers[0]

	// The real peer's record is writable and intact.
	if real.Fingerprint == "" {
		t.Fatal("the paired peer record carries no fingerprint")
	}
	if !strings.Contains(real.Address, ":") {
		t.Errorf("the paired peer record has no address: %+v", real)
	}

	// An impostor at that exact address, carrying B's real certificate but a
	// different fingerprint, must be refused. If this ever succeeds, a stale or
	// tampered trust record can be persisted and the transfer gates above would
	// no longer mean what they claim.
	impostor := pairing.Peer{
		Name:        real.Name,
		Address:     real.Address,
		Fingerprint: "0000000000000000000000000000000000000000000000000000000000000000",
		Alias:       real.Alias,
		CertPEM:     real.CertPEM,
	}
	if err := a.Store().AddPeer(impostor); err == nil {
		t.Fatal("the store accepted a record whose certificate does not match its fingerprint")
	}

	// And the honest record still works, so the refusal above is a real check
	// rather than the store refusing everything.
	if got := len(a.Store().ListPeers()); got != 1 {
		t.Errorf("A now lists %d peers; a rejected write must leave the store unchanged", got)
	}
	_ = b
	target := solePeerName(t, a)
	body, _ := json.Marshal(map[string]string{"text": "still trusted", "name": "ok", "peer": target})
	if code, resp := postJSON(t, "http://"+a.WebAddr()+"/api/drop/upload", a.ipcToken, "http://"+a.WebAddr(), body); code != http.StatusOK {
		t.Fatalf("the legitimate peer was refused after a rejected impostor write (%d): %s", code, resp)
	}
}

// TestUnpairedDestinationIsRefusedBeforeAnyConnection covers the refusal side,
// and it is deliberately narrow about what it proves.
//
// The first version of this gate was described as proving that trust is
// enforced. It does not. Flipping these hubs to loopback transport -- where
// IsPeerTrusted returns true unconditionally -- left the gate green, and the
// diagnostic showed why:
//
//	dial peer failed: peer target is not a trusted paired peer:
//	no paired peers found; pair with remote machine first
//
// So the refusal happens in store resolution, before a socket is opened. That is
// a real and worthwhile property, and it is a *different* property from the
// handshake check. This gate measures the lookup, and says so rather than
// borrowing credibility from the handshake.
func TestUnpairedDestinationIsRefusedBeforeAnyConnection(t *testing.T) {
	outB := t.TempDir()
	newHub := func(outDir string) *Hub {
		h, err := NewHub(HubConfig{
			TransportType:     "lan",
			ListenAddr:        "127.0.0.1:0",
			WebAddr:           "127.0.0.1:0",
			StoreDir:          t.TempDir(),
			OutputDir:         outDir,
			Headless:          true,
			AutoAcceptPairing: true,
		})
		if err != nil {
			t.Fatalf("NewHub: %v", err)
		}
		return h
	}
	hA, hB := newHub(t.TempDir()), newHub(outB)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = hA.Start(ctx) }()
	go func() { _ = hB.Start(ctx) }()
	for name, h := range map[string]*Hub{"A": hA, "B": hB} {
		select {
		case <-h.Ready():
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for Hub %s", name)
		}
	}
	defer func() { _ = hA.Stop(); _ = hB.Stop() }()

	// Deliberately NOT paired. Naming B's address directly is what a user does
	// with an address that was never paired, and it must be refused rather than
	// delivered.
	body, _ := json.Marshal(map[string]string{
		"text": "should never arrive",
		"name": "refused",
		"peer": hB.P2PAddr().String(),
	})
	code, respBody := postJSON(t, "http://"+hA.WebAddr()+"/api/drop/upload", hA.ipcToken, "http://"+hA.WebAddr(), body)
	if code == http.StatusOK {
		t.Fatalf("an unpaired destination was accepted (200): %s", respBody)
	}
	if entries, _ := os.ReadDir(outB); len(entries) != 0 {
		t.Errorf("B received %d item(s) from an unpaired sender", len(entries))
	}
	if items := recentFrom(t, hB); len(items) != 0 {
		t.Errorf("B recorded %d recent item(s) from an unpaired sender: %+v", len(items), items)
	}
}
