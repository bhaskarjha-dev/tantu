package pairing

import (
	"encoding/json"
	"os"
	"path/filepath"
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

	// Add peer
	p1 := Peer{
		Fingerprint: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Name:        "laptop-a",
		Address:     "192.168.1.50:9877",
		CertPEM:     []byte("test-cert-pem"),
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
	p2 := Peer{
		Fingerprint: "1122334455667788990011223344556677889900112233445566778899001122",
		Name:        "desktop-b",
		Address:     "192.168.1.60:9877",
		CertPEM:     []byte("test-cert-pem-2"),
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
