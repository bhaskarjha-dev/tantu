package bridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// maxBridgeCancelResponseWait bounds how long a cancel handler waits for the
// registry to release a session. Releasing is a context cancel and is
// effectively immediate; the bound exists so a misbehaving registry cannot hold
// a wire connection open indefinitely.
const maxBridgeCancelResponseWait = 2 * time.Second

// handleBridgeCancel releases an in-flight A-side sign-in on request from its
// owning peer, and answers with a BridgeCancelAck on the same message type.
//
// The ack travels back on `bridge_cancel` rather than a new type so a peer that
// sends a cancel always receives a reply it can interpret, and so an older peer
// that does not know the type simply times out instead of mis-reading the
// reply.
func (d *Dispatcher) handleBridgeCancel(ctx context.Context, conn transport.Conn, _ transport.Conn, peerFP string, firstEnv *protocol.Envelope) {
	var msg protocol.BridgeCancel
	if err := firstEnv.DecodePayload(&msg); err != nil {
		d.logf("⚠️ Cannot decode bridge_cancel from %s: %v", conn.RemoteAddr(), err)
		_ = conn.Send(protocol.TypeBridgeCancel, protocol.BridgeCancelAck{Error: "malformed cancel request"})
		return
	}
	if len(msg.FlowID) > maxOAuthFlowIDLength || (msg.RequestID != "" && !validBridgeRequestID(msg.RequestID)) {
		_ = conn.Send(protocol.TypeBridgeCancel, protocol.BridgeCancelAck{
			RequestID: msg.RequestID, FlowID: msg.FlowID, Error: "invalid cancel identifier",
		})
		return
	}

	ack := protocol.BridgeCancelAck{RequestID: msg.RequestID, FlowID: msg.FlowID}
	released := 0

	switch {
	case msg.RequestID != "":
		if info, ok := d.cfg.Registry.Cancel(msg.RequestID, peerFP); ok {
			released = 1
			d.logf("🚪 Released sign-in %s on callback port %d at the peer's request (reason: %s)",
				info.RequestID, info.CallbackPort, safeCancelReason(msg.Reason))
		}
	case msg.FlowID != "":
		released = d.cfg.Registry.CancelByFlow(msg.FlowID, peerFP)
		if released > 0 {
			d.logf("🚪 Released %d sign-in(s) for a cancelled flow at the peer's request (reason: %s)", released, safeCancelReason(msg.Reason))
		}
	default:
		released = len(d.cfg.Registry.CancelAll(peerFP))
		if released > 0 {
			d.logf("🚪 Released %d sign-in(s) at the peer's request (reason: %s)", released, safeCancelReason(msg.Reason))
		}
	}

	ack.Cancelled = released > 0
	if !ack.Cancelled && ack.Error == "" {
		// Not an error: the flow may simply have completed or timed out between
		// the user deciding and this arriving. Saying so plainly stops the peer
		// from reporting a failure the user cannot act on.
		ack.Error = "no matching sign-in was open"
	}

	if err := conn.Send(protocol.TypeBridgeCancel, ack); err != nil {
		d.logf("⚠️ Cannot send bridge_cancel ack to %s: %v", conn.RemoteAddr(), err)
		return
	}

	// Give the released session a moment to unwind so the callback port is
	// actually free before the peer reports success and the user retries. The
	// port is released by the session's own deferred Close, so a peer that
	// retries immediately should not race a teardown.
	if released > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(maxBridgeCancelResponseWait):
		}
	}
}

// safeCancelReason keeps a peer's free-text reason bounded and free of anything
// that could carry a URL, a code, or a token into the local log. The reason is
// operator-supplied text and is never forwarded anywhere.
func safeCancelReason(reason string) string {
	trimmed := reason
	if len(trimmed) > 120 {
		trimmed = trimmed[:120]
	}
	safe := make([]rune, 0, len(trimmed))
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			safe = append(safe, r)
		case r == ' ' || r == '-' || r == '_' || r == '.':
			safe = append(safe, r)
		default:
			// Drop anything that could be part of a URL, a query string, or a
			// control character.
		}
	}
	if len(safe) == 0 {
		return "unspecified"
	}
	return string(safe)
}

// ErrNoOpenSession reports that a cancel did not match any in-flight sign-in.
var ErrNoOpenSession = errors.New("no matching sign-in was open")

// CancelOpenSession releases a tracked sign-in by request id. It is the
// programmatic entry point for surfaces that cancel locally, such as a
// dashboard button on the machine holding the session.
func (r *SessionRegistry) CancelOpenSession(requestID, peerFingerprint string) error {
	if _, ok := r.Cancel(requestID, peerFingerprint); ok {
		return nil
	}
	return fmt.Errorf("cancel sign-in %q: %w", requestID, ErrNoOpenSession)
}
