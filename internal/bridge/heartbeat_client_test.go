package bridge

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// Gates for PingRemote.
//
// The claim is the one that makes the heartbeat worth anything: **a successful
// dial is not proof the peer is there.** An open socket and a matching
// certificate prove that something is listening and that it is who we think it
// is; they do not prove it is running rather than wedged. So the reply is
// required, and these gates are what hold it.
//
// The condition under test is "a peer that accepts the connection and then does
// not answer", which is produced rather than described: a real socket, a real
// protocol codec, and a real far end that is deliberately silent. That is not a
// stand-in for the behaviour -- it *is* the behaviour's only interesting input.

func TestPingRemoteSucceedsWhenThePeerAnswers(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		env, err := protocol.NewDecoder(server).Decode()
		if err != nil {
			return
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			if env.DecodePayload(&hb) == nil {
				_ = protocol.NewEncoder(server).Encode(protocol.TypeHeartbeat, hb)
			}
		}
	}()

	if err := PingRemote(context.Background(), &pipeConn{Conn: client}); err != nil {
		t.Fatalf("a peer that answered was reported as not answering: %v", err)
	}
}

// The load-bearing gate. Without requiring a reply, PingRemote would return nil
// the moment the send succeeded, and the Hub would report every peer it could
// open a socket to as reachable -- including one that had stopped speaking.
func TestPingRemoteFailsWhenThePeerNeverAnswers(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// The far end reads the heartbeat and says nothing, forever.
	go func() {
		_, _ = protocol.NewDecoder(server).Decode()
		_, _ = io.Copy(io.Discard, server)
	}()

	// The caller's own deadline is deliberately generous. PingRemote is supposed
	// to give up at PingRemoteTimeout on its own; this test asserts it does, by
	// checking how long it took. With a tight caller deadline the test would
	// pass even if the internal bound were deleted, and with no deadline it would
	// hang until the test binary's own timeout -- which is a gate that fails by
	// hanging rather than by failing, and reads the same in CI.
	ctx, cancel := context.WithTimeout(context.Background(), 3*PingRemoteTimeout)
	defer cancel()

	start := time.Now()
	err := PingRemote(ctx, &pipeConn{Conn: client})
	if err == nil {
		t.Fatal("a peer that accepted the connection and never answered was reported as " +
			"answering; reachability would then mean nothing more than 'the socket opened'")
	}
	if elapsed := time.Since(start); elapsed > PingRemoteTimeout+3*time.Second {
		t.Errorf("PingRemote took %s to give up on a silent peer, want about %s; it is not "+
			"bounding its own wait and is relying on the caller to interrupt it",
			elapsed, PingRemoteTimeout)
	}
	if !strings.Contains(err.Error(), "heartbeat") {
		t.Errorf("the error does not say what was being awaited: %q", err)
	}
}

// A peer that answers with something else has not answered the question. The
// realistic case is a peer-unpair notice arriving first, which the dispatcher
// handles on its own connection goroutine.
func TestPingRemoteRejectsAReplyThatIsNotAHeartbeat(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		if _, err := protocol.NewDecoder(server).Decode(); err != nil {
			return
		}
		_ = protocol.NewEncoder(server).Encode(protocol.TypeBridgeCancel, protocol.BridgeCancel{RequestID: "x"})
	}()

	err := PingRemote(context.Background(), &pipeConn{Conn: client})
	if err == nil {
		t.Fatal("a peer that replied with something other than a heartbeat was counted as " +
			"having answered")
	}
	if !strings.Contains(err.Error(), protocol.TypeHeartbeat) {
		t.Errorf("the error does not name what was expected: %q", err)
	}
}

// A connection that cannot even be written to is not a peer that is there.
func TestPingRemoteReportsAnUnwritableConnection(t *testing.T) {
	client, server := net.Pipe()
	server.Close()
	defer client.Close()

	if err := PingRemote(context.Background(), &pipeConn{Conn: client}); err == nil {
		t.Fatal("a closed connection produced no error")
	}
}

func TestPingRemoteRejectsANilConnection(t *testing.T) {
	if err := PingRemote(context.Background(), nil); err == nil {
		t.Fatal("a nil connection produced no error")
	}
}

// The context is honoured, so a caller already bounding the probe does not have
// to wait out PingRemoteTimeout on top of its own deadline.
func TestPingRemoteHonoursACallerDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		_, _ = protocol.NewDecoder(server).Decode()
		_, _ = io.Copy(io.Discard, server)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := PingRemote(ctx, &pipeConn{Conn: client}); err == nil {
		t.Fatal("a silent peer past the caller's deadline produced no error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("PingRemote took %s despite a 200ms caller deadline", elapsed)
	}
}

// pipeConn adapts a net.Conn to transport.Conn.
//
// Deliberately does not implement transport.Deadliner: receiveEnvelope has a
// path for connections without one, and a gate that only ever runs the other
// path would leave that branch untested by everything else in this package.
type pipeConn struct{ net.Conn }

func (c *pipeConn) Send(msgType string, payload any) error {
	return protocol.NewEncoder(c.Conn).Encode(msgType, payload)
}

func (c *pipeConn) Receive() (*protocol.Envelope, error) {
	return protocol.NewDecoder(c.Conn).Decode()
}

var (
	_ transport.Conn = (*pipeConn)(nil)
	_ net.Conn       = (*pipeConn)(nil)
	_                = errors.New
)
