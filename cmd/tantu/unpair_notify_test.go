package main

import (
	"context"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// The dashboard's Unpair is not the only way to remove a peer - `tantu unpair
// --name` edits the store directly, and before the notice existed it left the
// other machine paired forever, diverging silently from this one. This is the
// full CLI path against a live hub: remove locally, dial the removed peer over
// the authenticated wire, and watch the receiver converge. Without the notify
// call the receiver's store keeps its entry and the test times out - which is
// exactly what the field report described.
func TestRunUnpair_NotifiesRemovedPeerAndReceiverConverges(t *testing.T) {
	// This machine's store: an identity (pre-created so the hub can trust the
	// exact fingerprint the CLI will dial with) and one peer pointing at the
	// hub's live wire address.
	cliStoreDir := t.TempDir()
	cliStore, err := pairing.NewPeerStore(cliStoreDir)
	if err != nil {
		t.Fatalf("cli store: %v", err)
	}
	cliIdentity, err := cliStore.LoadOrCreateIdentity()
	if err != nil {
		t.Fatalf("cli identity: %v", err)
	}

	h, err := hub.NewHub(hub.HubConfig{
		TransportType: "lan",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      t.TempDir(),
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("hub not ready")
	}
	defer h.Stop()

	// The hub trusts this machine, so the notice passes its trust gate.
	if err := h.Store().AddPeer(pairing.Peer{
		Fingerprint: cliIdentity.Fingerprint,
		Name:        "cli-unpair-test",
		Address:     "127.0.0.1:9877",
		CertPEM:     cliIdentity.CertPEM,
	}); err != nil {
		t.Fatalf("hub AddPeer: %v", err)
	}
	// This machine lists the hub as a paired peer named "victim".
	if err := cliStore.AddPeer(pairing.Peer{
		Fingerprint: h.Identity().Fingerprint,
		Name:        "victim",
		Address:     h.P2PAddr().String(),
		CertPEM:     h.Identity().CertPEM,
	}); err != nil {
		t.Fatalf("cli AddPeer: %v", err)
	}

	runUnpair([]string{"--name=victim", "--store-dir=" + cliStoreDir})

	if got := len(cliStore.ListPeers()); got != 0 {
		t.Fatalf("cli store still lists %d peer(s) after unpair", got)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.Store().ListPeers()) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("receiver still lists %d trusted peer(s) after 5s - the CLI unpair never sent the notice", len(h.Store().ListPeers()))
}
