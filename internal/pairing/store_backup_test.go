package pairing

// The interrupted replacement.
//
// writeAtomic renames the destination aside before renaming the replacement
// into place, because Windows cannot replace a file in place. A crash between
// those two renames leaves the destination ABSENT and the preserved copy
// holding everything.
//
// The preserved copy used to carry a random suffix, so nothing in the
// repository could find it: the comment said the backup exists so "a crash or
// a failed retry never destroys the only known-good copy", and a copy under a
// name no code and no human knows to look for destroys it just as surely.
//
// For identity.json it is worse: that file holds the node's long-lived
// private key, so losing it loses the identity and every pairing made with
// it.
//
// See docs/KNOWN-LIMITATIONS.md row 5.7.

import (
	"os"
	"path/filepath"
	"testing"
)

// newBackupTestStore makes a store in a temporary directory.
func newBackupTestStore(t *testing.T) (*PeerStore, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewPeerStore(dir)
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	return store, dir
}

// TestAnInterruptedReplacementIsRecovered is the gate.
//
// The condition is produced rather than constructed: the destination is moved
// aside exactly as writeAtomic's Windows fallback moves it, and the
// replacement never arrives -- which is what a crash between the two renames
// leaves behind.
func TestAnInterruptedReplacementIsRecovered(t *testing.T) {
	store, _ := newBackupTestStore(t)

	const body = `{"fingerprint":{"name":"kept","address":"127.0.0.1:9877"}}`
	path := store.peersPath()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write the peer store: %v", err)
	}

	// The interruption: the destination is moved aside by the product's own
	// step -- the same one writeAtomic uses when the platform cannot replace
	// a file in place -- and the replacement never lands.
	backup, err := preserveDestination(path)
	if err != nil {
		t.Fatalf("preserve the destination: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the destination is still there, so the interruption was not produced")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("the preserved copy is not where the product will look for it: %v", err)
	}

	// A load must find the preserved copy and put it back.
	peers, err := store.loadPeersMap()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("the store holds %d peer(s) after an interrupted replacement, want 1: the "+
			"preserved copy was not restored, so every pairing is gone", len(peers))
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the destination was not put back: %v", err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Errorf("the preserved copy is still there after being restored")
	}
}

// TestAnInterruptedIdentityReplacementIsRecovered is the same for the file
// that matters most: the node's private key.
func TestAnInterruptedIdentityReplacementIsRecovered(t *testing.T) {
	store, _ := newBackupTestStore(t)

	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := store.SaveIdentity(id); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	path := store.identityPath()
	backup, err := preserveDestination(path)
	if err != nil {
		t.Fatalf("preserve the identity: %v", err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("the preserved identity is not where the product will look for it: %v", err)
	}

	loaded, err := store.LoadIdentity()
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if loaded == nil {
		t.Fatal("the identity is gone after an interrupted replacement: the node's long-lived " +
			"private key was under a name nothing could find, so the node has lost its identity " +
			"and every pairing made with it")
	}
	if loaded.Fingerprint != id.Fingerprint {
		t.Errorf("the restored identity is %q, want %q", loaded.Fingerprint, id.Fingerprint)
	}
}

// TestThePreservedCopyHasAFindableName pins the name.
//
// A random suffix cannot be looked for. A deterministic one can, by the
// restore above and by a person who has just lost their pairing list.
func TestThePreservedCopyHasAFindableName(t *testing.T) {
	_, dir := newBackupTestStore(t)
	path := filepath.Join(dir, "peers.json")

	if err := os.WriteFile(path, []byte(`{"a":{}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	backup, err := preserveDestination(path)
	if err != nil {
		t.Fatalf("preserve the destination: %v", err)
	}
	// The name is derived from the destination, so it is the same every time
	// and is discoverable by name rather than by search.
	if filepath.Base(backup) != "peers.json.bak" {
		t.Errorf("the preserved copy is named %q, want a deterministic name derived from the "+
			"destination", filepath.Base(backup))
	}
	if _, err := os.Stat(backup); err != nil {
		t.Errorf("the preserved copy is not there: %v", err)
	}
}
