package bridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// CancelRemoteTimeout bounds a cancel request. Cancellation is a courtesy to
// the peer, not part of the user's critical path: if the peer is slow or gone,
// the local wait must still end promptly.
const CancelRemoteTimeout = 3 * time.Second

// CancelRemoteSession asks the peer to release an in-flight sign-in.
//
// This is the message the original protocol was missing. When a user walks away
// from a sign-in on the browser machine, the application machine keeps a bound
// loopback callback port, an open browser tab, and a live session until its
// timeout. Because the application's redirect port is normally fixed, the next
// attempt cannot even bind, so the only recovery is restarting the process.
//
// Both identifiers are optional but at least one is required: RequestID targets
// one exact attempt, FlowID targets every attempt for a logical flow, and
// neither targets all of the peer's open sign-ins.
//
// A peer that does not know the cancel type is a peer running an older release.
// The send simply fails or the reply never arrives, which is reported as
// Cancelled=false rather than as an error: the peer will release the session on
// its own timeout, and the caller has nothing useful to do about it.
func CancelRemoteSession(ctx context.Context, conn transport.Conn, requestID, flowID, reason string) (bool, error) {
	if conn == nil {
		return false, errors.New("cancel requires a connection")
	}
	if requestID == "" && flowID == "" {
		return false, errors.New("cancel requires a request id or a flow id")
	}
	if requestID != "" && !validBridgeRequestID(requestID) {
		return false, errors.New("invalid request id for cancel")
	}
	if len(flowID) > maxOAuthFlowIDLength {
		return false, errors.New("flow id is too long for cancel")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, CancelRemoteTimeout)
	defer cancel()

	msg := protocol.BridgeCancel{
		RequestID: requestID,
		FlowID:    flowID,
		Reason:    reason,
	}
	if err := conn.Send(protocol.TypeBridgeCancel, msg); err != nil {
		return false, fmt.Errorf("send bridge_cancel: %w", err)
	}

	// A reply is best-effort. The A-side sends one on the same connection, so
	// waiting briefly tells us whether the session was actually released, which
	// is what lets the caller report "cancelled" rather than "cancelled, maybe".
	var ack protocol.BridgeCancelAck
	for {
		env, err := receiveEnvelope(ctx, conn)
		if err != nil {
			// No ack: the peer either predates the message or went away. Either
			// way it is not a failure the user can act on.
			return false, nil
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			_ = env.DecodePayload(&hb)
			_ = conn.Send(protocol.TypeHeartbeat, hb)
			continue
		}
		if env.Type != protocol.TypeBridgeCancel {
			return false, nil
		}
		if err := env.DecodePayload(&ack); err != nil {
			return false, nil
		}
		return ack.Cancelled, nil
	}
}
