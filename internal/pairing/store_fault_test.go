package pairing

// A peer store that cannot be read is not a peer store with no peers.
//
// The store is the product's record of which machines it trusts. A store that
// fails to parse used to arrive at every caller that wanted a list as an
// empty one, so a user whose pairing record was lost was told they had never
// paired at all -- and pairing could not be redone either, because the write
// path read the same broken file.
//
// These gates hold the distinction the rest of the product now depends on:
// LastLoadError is nil for a store that is merely empty, and non-nil for one
// that cannot be read, and it names the file.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPeerStoreReportsAFileItCannotRead is the store's own half.
func TestPeerStoreReportsAFileItCannotRead(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPeerStore(dir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}

	// A store that is merely empty has no fault, and saying so matters as
	// much as the case below: a gate that reported a fault unconditionally
	// would pass.
	if fault := store.LastLoadError(); fault != nil {
		t.Fatalf("a store with no peers in it reported a fault: %v", fault)
	}
	store.ListPeers()
	if fault := store.LastLoadError(); fault != nil {
		t.Fatalf("reading an empty store reported a fault: %v", fault)
	}

	// Now break the file.
	peersPath := filepath.Join(dir, "peers.json")
	if err := os.WriteFile(peersPath, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("write the corrupt peer store: %v", err)
	}

	peers := store.ListPeers()
	if len(peers) != 0 {
		t.Errorf("a store that could not be read reported %d peer(s); it can only report none", len(peers))
	}
	fault := store.LastLoadError()
	if fault == nil {
		t.Fatal("a store that could not be read reported no fault, so every surface that " +
			"wants a list is told the user has no trusted peers")
	}
	if !strings.Contains(fault.Error(), "peers.json") {
		t.Errorf("the fault does not name the file that is broken: %q", fault.Error())
	}

	// And the fault clears when the file becomes readable again, so a fault
	// that was fixed does not haunt the product for ever.
	if err := os.WriteFile(peersPath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("repair the peer store: %v", err)
	}
	store.ListPeers()
	if fault := store.LastLoadError(); fault != nil {
		t.Errorf("a repaired store still reports a fault: %v", fault)
	}
}

// TestPeerStoreKeepsTheFaultAcrossReads is the part that would be easy to
// get wrong: the fault has to survive the read that discovered it, because
// the caller reads the list first and asks about the fault afterwards.
func TestPeerStoreKeepsTheFaultAcrossReads(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPeerStore(dir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "peers.json"), []byte("not json at all"), 0o600); err != nil {
		t.Fatalf("write the corrupt peer store: %v", err)
	}

	// A read that succeeds on a later call must not clear a fault that is
	// still true, and a read that fails must set it every time.
	for i := 0; i < 3; i++ {
		store.ListPeers()
		if store.LastLoadError() == nil {
			t.Fatalf("read %d: the fault did not survive the read that discovered it", i)
		}
	}
}
