package hub

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// unpairNoticeTimeout bounds one best-effort notice attempt end to end. It is
// short on purpose: the removal itself has already happened by the time this
// runs, so a peer that cannot be reached within the window falls back to the
// pull path - its next transfer is refused with the honest "you are no longer
// trusted" reason instead of a generic receiver refusal.
const unpairNoticeTimeout = 3 * time.Second

// NotifyUnpaired tells a just-removed peer that this machine no longer trusts
// it, so the peer's store can converge instead of discovering the removal
// only by being refused on its next send. Best effort by contract: an error
// here never fails the unpair that triggered it, and the receiver treats the
// notice as a fact to cross-check against its own certificate authentication,
// never as an instruction it must obey.
//
// The dial pins the removed peer by the fingerprint it held while paired.
// The local store no longer lists it, so trust-set membership is gone, but
// the pin still stops a substituted listener at the same address from
// receiving the notice. That the sender no longer trusts the receiver cannot
// weaken the dial: only the receiver authenticates the sender, which is the
// direction that matters - the receiver will only ever remove trust in
// response to a statement it can attribute to the peer itself.
func NotifyUnpaired(parent context.Context, id *pairing.Identity, peer pairing.Peer) error {
	if id == nil {
		return errors.New("identity is required")
	}
	if peer.Fingerprint == "" {
		return errors.New("removed peer has no fingerprint")
	}
	if peer.Address == "" {
		return errors.New("removed peer has no address to notify")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, unpairNoticeTimeout)
	defer cancel()

	tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		return fmt.Errorf("load identity key pair: %w", err)
	}
	tr, err := transport.NewLANTransport(transport.LANTransportConfig{
		Cert:                tlsCert,
		TrustedFingerprints: []string{peer.Fingerprint},
	})
	if err != nil {
		return fmt.Errorf("build notice transport: %w", err)
	}
	conn, err := transport.DialPinnedContext(ctx, tr, peer.Address, peer.Fingerprint)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if dl, isDeadliner := conn.(transport.Deadliner); isDeadliner {
			_ = dl.SetDeadline(deadline)
		}
	}
	return conn.Send(protocol.TypePeerUnpaired, protocol.PeerUnpaired{
		ByFingerprint: id.Fingerprint,
	})
}

// notifyPeerUnpairedAsync captures everything it needs synchronously - the
// identity above all, because the store backing it may be a test fixture that
// outlives neither the request nor the hub - then dials in the background so
// an unpair never waits three seconds to answer its own HTTP request.
func (h *Hub) notifyPeerUnpairedAsync(peer pairing.Peer) {
	if peer.Fingerprint == "" || peer.Address == "" {
		return
	}
	id := h.Identity()
	if id == nil {
		return
	}
	name := peer.DisplayName()
	go func() {
		if err := NotifyUnpaired(context.Background(), id, peer); err != nil {
			h.logger.Warn(DomainPeer, fmt.Sprintf(
				"Could not notify %s that it was unpaired (%v); its next send will get an honest \"no longer trusted\" reason instead of a generic refusal",
				name, err))
			return
		}
		h.logger.Action(DomainPeer, fmt.Sprintf("📣 Told %s it was unpaired; its paired-device list can now clear", name))
	}()
}

// applyPeerUnpaired is the receiving half: a peer this machine still trusts
// has just authenticated a statement that it removed us. Converging our side
// keeps both stores honest - the field report behind this was a dashboard
// still listing a paired device that had already forgotten us. The removal
// only ever goes in the safe direction (dropping trust we can restore by
// pairing again), which is why it needs no confirmation on this side: the
// other machine's consent was never required to remove us either.
func (h *Hub) applyPeerUnpaired(fp string) {
	h.mu.RLock()
	store := h.store
	h.mu.RUnlock()
	if store == nil {
		return
	}
	name := "A paired device"
	if p, ok := store.GetPeer(fp); ok {
		if d := p.DisplayName(); d != "" {
			name = d
		}
	}
	if err := store.RemovePeer(fp); err != nil {
		h.logger.Warn(DomainPeer, fmt.Sprintf("Unpair notice from %s could not be applied: %v", name, err))
		return
	}
	if h.GetActivePeer() == fp {
		_ = h.SetActivePeer("")
	}
	h.logger.Action(DomainPeer, fmt.Sprintf(
		"🗑️ %s removed this machine from its trusted peers (you are unpaired). Pair again from either dashboard to restore transfers.",
		name))
}
