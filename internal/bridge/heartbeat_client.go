package bridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// PingRemoteTimeout bounds one heartbeat round-trip.
//
// Short on purpose. A peer that is absent is the expected case this is
// instrumented for, and a five-second wait per absent peer per attempt is a long
// time to hold a goroutine for an answer nobody is waiting on.
const PingRemoteTimeout = 5 * time.Second

// PingRemote asks the peer whether it is there, and reports whether it answered.
//
// This is the outbound half of a heartbeat. The inbound half has always existed:
// the dispatcher, both OAuth sides and the drop receiver all answer a heartbeat
// when one arrives. Nothing ever *sent* one, which is why a heartbeat was a
// keepalive that only the receiver could keep alive -- it refreshed the binding
// on whichever side happened to dial, and never on the side that wanted it.
//
// A reply is required rather than inferred. "The dial succeeded" only proves a
// socket opened and a certificate matched, and it does not prove the peer is
// running a dispatcher rather than wedged. Since the whole point is a truthful
// answer to "can I reach this peer right now", the answer has to be a real reply
// to a real question.
//
// The connection is left to the caller to close. PingRemote does not close it
// because the same connection is worth reusing for a follow-up, and because the
// existing callers all treat close as their own business.
func PingRemote(ctx context.Context, conn transport.Conn) error {
	if conn == nil {
		return errors.New("ping requires a connection")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, PingRemoteTimeout)
	defer cancel()

	// An empty RequestID is the protocol's "general keepalive" form. Nothing
	// correlates heartbeats on the far side, so there is nothing to fill in.
	if err := conn.Send(protocol.TypeHeartbeat, protocol.Heartbeat{}); err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}

	// The first heartbeat to arrive is the answer. There is no loop here, unlike
	// CancelRemoteSession's reply wait: that one skips over heartbeats because it
	// is waiting for a *particular* message and a keepalive may arrive first.
	// Here a keepalive is the only thing being waited for, so looping would only
	// ever discard the answer and ask again.
	var hb protocol.Heartbeat
	env, err := receiveEnvelope(ctx, conn)
	if err != nil {
		return fmt.Errorf("await heartbeat reply: %w", err)
	}
	if env.Type != protocol.TypeHeartbeat {
		// Anything else on a connection that was only opened to ask one question
		// is not an answer to it. A peer-unpair notice is the realistic case, and
		// the dispatcher handles that on its own connection goroutine rather than
		// here, so the right response is to say so rather than to count it.
		return fmt.Errorf("expected %q, got %q", protocol.TypeHeartbeat, env.Type)
	}
	if err := env.DecodePayload(&hb); err != nil {
		return fmt.Errorf("decode heartbeat reply: %w", err)
	}
	return nil
}
