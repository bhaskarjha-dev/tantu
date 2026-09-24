package pairing

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDefaultStoreDir(t *testing.T) {
	dir, err := DefaultStoreDir()
	if err != nil {
		t.Fatalf("DefaultStoreDir failed: %v", err)
	}
	if dir == "" {
		t.Fatal("expected non-empty store directory")
	}
	if filepath.Base(dir) != "tantu" {
		t.Errorf("expected base directory 'tantu', got %s", filepath.Base(dir))
	}
}

func TestPeerStoreIdentity(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}

	// Missing identity on first run
	if store.HasIdentity() {
		t.Error("expected HasIdentity() to be false initially")
	}
	id, err := store.LoadIdentity()
	if err != nil {
		t.Fatalf("LoadIdentity error on missing file: %v", err)
	}
	if id != nil {
		t.Fatalf("expected nil identity, got %v", id)
	}

	// Generate and save identity
	genID, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := store.SaveIdentity(genID); err != nil {
		t.Fatalf("SaveIdentity failed: %v", err)
	}

	if !store.HasIdentity() {
		t.Error("expected HasIdentity() to be true after save")
	}

	// Load identity back
	loadedID, err := store.LoadIdentity()
	if err != nil {
		t.Fatalf("LoadIdentity failed: %v", err)
	}
	if loadedID == nil {
		t.Fatal("expected non-nil loaded identity")
	}
	if loadedID.Fingerprint != genID.Fingerprint {
		t.Errorf("fingerprint mismatch: %s != %s", loadedID.Fingerprint, genID.Fingerprint)
	}
	if string(loadedID.CertPEM) != string(genID.CertPEM) {
		t.Errorf("cert PEM mismatch")
	}
	if string(loadedID.KeyPEM) != string(genID.KeyPEM) {
		t.Errorf("key PEM mismatch")
	}

	// Verify persistence across new store instance
	store2, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore 2: %v", err)
	}
	if !store2.HasIdentity() {
		t.Error("expected store2 to have identity")
	}
	loaded2, err := store2.LoadIdentity()
	if err != nil || loaded2 == nil || loaded2.Fingerprint != genID.Fingerprint {
		t.Errorf("failed to reload identity from store2: %v", err)
	}
}

func TestPeerStoreCRUD(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}

	// Missing peers initially
	peers := store.ListPeers()
	if len(peers) != 0 {
		t.Fatalf("expected empty peer list, got %d", len(peers))
	}
	if store.IsTrusted("non-existent") {
		t.Error("expected IsTrusted to be false for non-existent peer")
	}
	_, ok := store.GetPeer("non-existent")
	if ok {
		t.Error("expected GetPeer to return false for non-existent peer")
	}

	// Add peer. Peer fixtures use real generated certificates so the
	// fingerprint/certificate binding enforced by AddPeer is exercised.
	peerID1, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	p1 := Peer{
		Fingerprint: peerID1.Fingerprint,
		Name:        "laptop-a",
		Address:     "192.168.1.50:9877",
		CertPEM:     peerID1.CertPEM,
	}
	if err := store.AddPeer(p1); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}

	if !store.IsTrusted(p1.Fingerprint) {
		t.Error("expected IsTrusted to be true")
	}

	gotPeer, ok := store.GetPeer(p1.Fingerprint)
	if !ok {
		t.Fatalf("GetPeer failed for added peer")
	}
	if gotPeer.Name != p1.Name || gotPeer.Address != p1.Address {
		t.Errorf("peer data mismatch: got %+v, want %+v", gotPeer, p1)
	}
	if gotPeer.FirstSeen.IsZero() || gotPeer.LastSeen.IsZero() {
		t.Errorf("expected non-zero timestamps: first=%v last=%v", gotPeer.FirstSeen, gotPeer.LastSeen)
	}

	// Update existing peer preserves FirstSeen
	firstSeenOrig := gotPeer.FirstSeen
	time.Sleep(10 * time.Millisecond)
	p1Updated := gotPeer
	p1Updated.Address = "192.168.1.51:9877"
	if err := store.AddPeer(p1Updated); err != nil {
		t.Fatalf("AddPeer update failed: %v", err)
	}
	gotUpdated, ok := store.GetPeer(p1.Fingerprint)
	if !ok {
		t.Fatal("GetPeer failed for updated peer")
	}
	if gotUpdated.Address != "192.168.1.51:9877" {
		t.Errorf("expected updated address, got %s", gotUpdated.Address)
	}
	if !gotUpdated.FirstSeen.Equal(firstSeenOrig) {
		t.Errorf("FirstSeen should be preserved: got %v, want %v", gotUpdated.FirstSeen, firstSeenOrig)
	}

	// Add second peer
	peerID2, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	p2 := Peer{
		Fingerprint: peerID2.Fingerprint,
		Name:        "desktop-b",
		Address:     "192.168.1.60:9877",
		CertPEM:     peerID2.CertPEM,
	}
	if err := store.AddPeer(p2); err != nil {
		t.Fatalf("AddPeer p2: %v", err)
	}

	allPeers := store.ListPeers()
	if len(allPeers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(allPeers))
	}

	// Verify peers.json format on disk has fingerprint as keys
	peersFile := filepath.Join(tmpDir, "peers.json")
	rawJSON, err := os.ReadFile(peersFile)
	if err != nil {
		t.Fatalf("read peers.json: %v", err)
	}
	var rawMap map[string]Peer
	if err := json.Unmarshal(rawJSON, &rawMap); err != nil {
		t.Fatalf("unmarshal peers.json map: %v", err)
	}
	if _, ok := rawMap[p1.Fingerprint]; !ok {
		t.Errorf("expected fingerprint key %s in JSON map", p1.Fingerprint)
	}

	// Remove peer
	if err := store.RemovePeer(p1.Fingerprint); err != nil {
		t.Fatalf("RemovePeer failed: %v", err)
	}
	if store.IsTrusted(p1.Fingerprint) {
		t.Error("expected p1 to be removed from trusted peers")
	}
	if len(store.ListPeers()) != 1 {
		t.Errorf("expected 1 peer remaining, got %d", len(store.ListPeers()))
	}
}

func TestPeerStore_ResolvePeer(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}

	pA := Peer{
		Fingerprint: "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		Name:        "workstation-linux",
		Alias:       "work",
		Address:     "10.0.0.10:9877",
	}
	pB := Peer{
		Fingerprint: "11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff",
		Name:        "macbook-air",
		Alias:       "mac",
		IsDefault:   true,
		Address:     "10.0.0.20:9877",
	}

	_ = store.AddPeer(pA)
	_ = store.AddPeer(pB)

	// 1. Empty query should resolve to designated default peer (Peer B)
	resolved, err := store.ResolvePeer("")
	if err != nil {
		t.Fatalf("ResolvePeer(\"\") failed: %v", err)
	}
	if resolved.Fingerprint != pB.Fingerprint {
		t.Errorf("expected default peer B, got %s", resolved.DisplayName())
	}

	// 2. Resolve by Alias
	resolved, err = store.ResolvePeer("work")
	if err != nil || resolved.Fingerprint != pA.Fingerprint {
		t.Errorf("expected peer A by alias 'work', got %+v, err: %v", resolved, err)
	}

	// 3. Resolve by Name
	resolved, err = store.ResolvePeer("macbook-air")
	if err != nil || resolved.Fingerprint != pB.Fingerprint {
		t.Errorf("expected peer B by name 'macbook-air', got %+v, err: %v", resolved, err)
	}

	// 4. Resolve by SAS Code
	sasA := pA.SAS()
	resolved, err = store.ResolvePeer(sasA)
	if err != nil || resolved.Fingerprint != pA.Fingerprint {
		t.Errorf("expected peer A by SAS %s, got %+v, err: %v", sasA, resolved, err)
	}

	// 5. Resolve by Fingerprint Prefix
	resolved, err = store.ResolvePeer("aabbccddee")
	if err != nil || resolved.Fingerprint != pA.Fingerprint {
		t.Errorf("expected peer A by prefix, got %+v, err: %v", resolved, err)
	}

	// 6. Resolve by Address
	resolved, err = store.ResolvePeer("10.0.0.10:9877")
	if err != nil || resolved.Fingerprint != pA.Fingerprint {
		t.Errorf("expected peer A by host:port, got %+v, err: %v", resolved, err)
	}
	resolved, err = store.ResolvePeer("10.0.0.10")
	if err != nil || resolved.Fingerprint != pA.Fingerprint {
		t.Errorf("expected peer A by IP host, got %+v, err: %v", resolved, err)
	}

	// 7. Unknown query returns error
	_, err = store.ResolvePeer("non-existent-box")
	if err == nil {
		t.Errorf("expected error for non-existent peer query")
	}
}

func TestPeerStore_SetAliasAndDefault(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}

	pA := Peer{
		Fingerprint: "fp-alpha-1234567890",
		Name:        "rig-alpha",
		Address:     "192.168.1.100:9877",
	}
	pB := Peer{
		Fingerprint: "fp-beta-1234567890",
		Name:        "rig-beta",
		Address:     "192.168.1.101:9877",
	}
	_ = store.AddPeer(pA)
	_ = store.AddPeer(pB)

	// SetAlias
	if err := store.SetAlias(pA.Fingerprint, "my-alpha"); err != nil {
		t.Fatalf("SetAlias failed: %v", err)
	}
	gotA, _ := store.GetPeer(pA.Fingerprint)
	if gotA.Alias != "my-alpha" || gotA.DisplayName() != "my-alpha" {
		t.Errorf("expected Alias 'my-alpha', got %s", gotA.Alias)
	}

	// SetDefault
	if err := store.SetDefault(pA.Fingerprint); err != nil {
		t.Fatalf("SetDefault failed: %v", err)
	}
	gotA, _ = store.GetPeer(pA.Fingerprint)
	gotB, _ := store.GetPeer(pB.Fingerprint)
	if !gotA.IsDefault || gotB.IsDefault {
		t.Errorf("expected A to be default and B not default: A=%v B=%v", gotA.IsDefault, gotB.IsDefault)
	}

	// Switch default to B
	if err := store.SetDefault(pB.Fingerprint); err != nil {
		t.Fatalf("SetDefault B failed: %v", err)
	}
	gotA, _ = store.GetPeer(pA.Fingerprint)
	gotB, _ = store.GetPeer(pB.Fingerprint)
	if gotA.IsDefault || !gotB.IsDefault {
		t.Errorf("expected B to be default and A cleared: A=%v B=%v", gotA.IsDefault, gotB.IsDefault)
	}

	// UpdatePeerAddress
	if err := store.UpdatePeerAddress(pA.Fingerprint, "192.168.1.150:9877"); err != nil {
		t.Fatalf("UpdatePeerAddress failed: %v", err)
	}
	gotA, _ = store.GetPeer(pA.Fingerprint)
	if gotA.Address != "192.168.1.150:9877" {
		t.Errorf("expected updated address 192.168.1.150:9877, got %s", gotA.Address)
	}
}

func TestPeerStore_BackwardCompatibility(t *testing.T) {
	tmpDir := t.TempDir()
	// Write legacy peers.json without alias or is_default
	legacyJSON := `{
		"legacy123": {
			"fingerprint": "legacy123",
			"name": "legacy-box",
			"address": "192.168.1.200:9877"
		}
	}`
	peersFile := filepath.Join(tmpDir, "peers.json")
	if err := os.WriteFile(peersFile, []byte(legacyJSON), 0644); err != nil {
		t.Fatalf("failed to write legacy peers.json: %v", err)
	}

	store, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore failed: %v", err)
	}

	p, ok := store.GetPeer("legacy123")
	if !ok {
		t.Fatalf("expected to read legacy peer from disk")
	}
	if p.Alias != "" {
		t.Errorf("expected empty alias for legacy peer, got %s", p.Alias)
	}
	if p.IsDefault {
		t.Errorf("expected is_default=false for legacy peer")
	}
	if p.DisplayName() != "legacy-box" {
		t.Errorf("expected DisplayName 'legacy-box', got %s", p.DisplayName())
	}
}

func TestPeerStore_SASAmbiguityRejected(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	// Two peers sharing a 6-hex SAS prefix: SAS() is fp[:6].
	pA := Peer{Fingerprint: "aabbcc0000000000000000000000000000000000000000000000000000000001", Name: "a", Address: "10.0.0.1:9877"}
	pB := Peer{Fingerprint: "aabbccffffffffffffffffffffffffffffffffffffffffffffffffffff0002", Name: "b", Address: "10.0.0.2:9877"}
	if err := store.AddPeer(pA); err != nil {
		t.Fatalf("AddPeer A: %v", err)
	}
	if err := store.AddPeer(pB); err != nil {
		t.Fatalf("AddPeer B: %v", err)
	}
	if _, err := store.ResolvePeer("aabbcc"); err == nil {
		t.Fatal("expected ambiguous SAS error, got nil")
	}
}

func TestPeerStore_CertFingerprintBinding(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	// Mismatched certificate must be rejected.
	bad := Peer{Fingerprint: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", Name: "bad", Address: "10.0.0.3:9877", CertPEM: id.CertPEM}
	if err := store.AddPeer(bad); err == nil {
		t.Fatal("expected certificate/fingerprint mismatch error, got nil")
	}
	// Matching certificate is accepted.
	good := Peer{Fingerprint: id.Fingerprint, Name: "good", Address: "10.0.0.3:9877", CertPEM: id.CertPEM}
	if err := store.AddPeer(good); err != nil {
		t.Fatalf("AddPeer with matching cert: %v", err)
	}
	// Oversized certificate blobs are rejected.
	huge := make([]byte, 64*1024+1)
	big := Peer{Fingerprint: id.Fingerprint, Name: "big", Address: "10.0.0.3:9877", CertPEM: huge}
	if err := store.AddPeer(big); err == nil {
		t.Fatal("expected oversized certificate error, got nil")
	}
}

func TestPeerStore_AddressValidation(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	valid := []string{"192.168.1.50:9877", "10.0.0.5", "example-host", "127.0.0.1", "host:9999"}
	for i, addr := range valid {
		p := Peer{Fingerprint: "aabbccddeeff0000000000000000000000000000000000000000000000000000", Name: "v", Address: addr}
		p.Fingerprint = "aabbccddeeff00000000000000000000000000000000000000000000000000" + string(rune('0'+i)) + string(rune('0'+i))
		if err := store.AddPeer(p); err != nil {
			t.Errorf("AddPeer(%q) = %v, want nil", addr, err)
		}
	}
	invalid := []string{"host:notaport", "host:", ":9877", "has space:9877", "bad\x01addr:9877",
		"https://evil.com:9877", "evil.com/path:9877", "user@host:9877", "host:0", "host:65536", "host:http"}
	for _, addr := range invalid {
		p := Peer{Fingerprint: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff00", Name: "x", Address: addr}
		if err := store.AddPeer(p); err == nil {
			t.Errorf("AddPeer(%q) succeeded, want error", addr)
		}
	}
	if err := store.UpdatePeerAddress(p_FingerprintForUpdate(t, store), "not a port:xyz"); err == nil {
		t.Error("UpdatePeerAddress with invalid address succeeded, want error")
	}
}

func p_FingerprintForUpdate(t *testing.T, store *PeerStore) string {
	t.Helper()
	peers := store.ListPeers()
	if len(peers) == 0 {
		t.Fatal("no peers available for update test")
	}
	return peers[0].Fingerprint
}

func TestPeerStore_PermissionRepair(t *testing.T) {
	// os.Chmod cannot express owner-only ACLs on Windows (only the read-only
	// attribute is mapped), so permission repair is a Unix-enforced control.
	// The repair calls remain in the code path as harmless no-ops on Windows.
	if runtime.GOOS == "windows" {
		t.Skip("permission repair requires Unix file modes")
	}
	tmpDir := t.TempDir()
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	if info, err := os.Stat(tmpDir); err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("store dir perms not repaired: %v %v", info.Mode(), err)
	}
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := store.SaveIdentity(id); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	if err := os.Chmod(filepath.Join(tmpDir, "identity.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadIdentity(); err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if info, err := os.Stat(filepath.Join(tmpDir, "identity.json")); err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("identity file perms not repaired: %v %v", info.Mode(), err)
	}
}

func TestConfirmWithContext(t *testing.T) {
	// Immediate answer honored.
	accepted, err := confirmWithContext(context.Background(), func(_, _ string) bool { return true }, "aaaaaa", "bbbbbb")
	if err != nil || !accepted {
		t.Fatalf("confirm = %v, %v; want true, nil", accepted, err)
	}
	// Blocking prompt unblocks on cancellation instead of hanging forever.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := confirmWithContext(ctx, func(_, _ string) bool {
			select {}
		}, "aaaaaa", "bbbbbb"); err == nil {
			t.Error("expected cancellation error, got nil")
		}
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmWithContext did not respect cancellation")
	}
	// Expired context fails closed even with an accepting prompt.
	expired, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := confirmWithContext(expired, func(_, _ string) bool { return true }, "a", "b"); err == nil {
		t.Error("expected error for expired context, got nil")
	}
}

func TestPeerStore_ResolveCrossTierAmbiguity(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	// Peer A has SAS "aabbcc"; peer B's fingerprint starts with "aabbcc".
	pA := Peer{Fingerprint: "aabbcc0000000000000000000000000000000000000000000000000000000001", Name: "a", Address: "10.0.0.1:9877"}
	pB := Peer{Fingerprint: "aabbccffffffffffffffffffffffffffffffffffffffffffffffffffff0002", Name: "b", Address: "10.0.0.2:9877"}
	if err := store.AddPeer(pA); err != nil {
		t.Fatalf("AddPeer A: %v", err)
	}
	if err := store.AddPeer(pB); err != nil {
		t.Fatalf("AddPeer B: %v", err)
	}
	if _, err := store.ResolvePeer("aabbcc"); err == nil {
		t.Fatal("cross-tier SAS/prefix collision resolved silently, want ambiguity error")
	}
}

func TestPeerStore_ResolveEmptyRequiresDefaultOrSingle(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewPeerStore(tmpDir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	pA := Peer{Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "a", Address: "10.0.0.1:9877"}
	if err := store.AddPeer(pA); err != nil {
		t.Fatalf("AddPeer A: %v", err)
	}
	// Single peer resolves implicitly.
	if resolved, err := store.ResolvePeer(""); err != nil || resolved.Fingerprint != pA.Fingerprint {
		t.Fatalf("single-peer empty resolve = %v, %v; want A, nil", resolved, err)
	}
	pB := Peer{Fingerprint: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Name: "b", Address: "10.0.0.2:9877"}
	if err := store.AddPeer(pB); err != nil {
		t.Fatalf("AddPeer B: %v", err)
	}
	// Multiple peers without default must not silently pick one.
	if _, err := store.ResolvePeer(""); err == nil {
		t.Fatal("multi-peer empty resolve succeeded, want error")
	}
	// Designating a default restores implicit resolution.
	if err := store.SetDefault(pB.Fingerprint); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	if resolved, err := store.ResolvePeer(""); err != nil || resolved.Fingerprint != pB.Fingerprint {
		t.Fatalf("default empty resolve = %v, %v; want B, nil", resolved, err)
	}
}
