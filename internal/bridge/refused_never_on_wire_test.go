package bridge

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

// A connection that records what was sent and refuses to receive anything.
//
// Nothing here implements a real peer, which is the point: the gate is about
// what leaves the process when a URL is refused, and a peer that answers nothing
// means the session cannot advance past the refusal by accident.
type recordingConn struct {
	sent []string
}

func newRecordingConn() *recordingConn { return &recordingConn{} }

func (c *recordingConn) Send(msgType string, payload any) error {
	c.sent = append(c.sent, msgType)
	return nil
}

func (c *recordingConn) Receive() (*protocol.Envelope, error) {
	return nil, errors.New("recording conn receives nothing")
}

func (c *recordingConn) Close() error         { return nil }
func (c *recordingConn) LocalAddr() net.Addr  { return nil }
func (c *recordingConn) RemoteAddr() net.Addr { return nil }
func (c *recordingConn) sentTypes() []string  { return append([]string{}, c.sent...) }

// Nothing may be sent on the wire for a refused URL.
//
// The table above says ExtractCallbackPort returns an error. This says the error
// stops the session, which is the part that would otherwise let a browser open
// anyway -- and it is produced: a real BSide session against a connection that
// records what it was asked to send.
func TestARefusedURLNeverReachesTheWire(t *testing.T) {
	const auth = "https://example.com/auth?state=S&redirect_uri=https%3A%2F%2Flocalhost%3A9000%2Fcb"

	rec := newRecordingConn()
	b := NewBSide(rec, BSideConfig{Timeout: 2 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := b.Run(ctx, auth)
	if err == nil {
		t.Fatal("the B-side reported success for an https loopback redirect_uri")
	}
	if got := rec.sentTypes(); len(got) != 0 {
		t.Errorf("a refused authorization URL still put %v on the wire; nothing may be "+
			"requested until the URL is known to be relayable, or the far side binds a "+
			"port and waits for a flow that can never work", got)
	}
	if !strings.Contains(err.Error(), "http") {
		t.Errorf("the session error does not say the scheme is the problem: %v", err)
	}
}

// The mirror image, so the pair cannot both be satisfied by refusing everything.
//
// A URL that *is* relayable must still put a bridge_request on the wire. Without
// this, "refuse early" and "refuse everything" look identical to the gate above.
func TestARelayableURLStillReachesTheWire(t *testing.T) {
	const auth = "https://app.kiro.dev/signin?state=S&redirect_uri=http%3A%2F%2Flocalhost%3A3128"

	rec := newRecordingConn()
	b := NewBSide(rec, BSideConfig{Timeout: 2 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// It will not complete -- the recording conn answers nothing -- but it must
	// get as far as asking.
	_ = b.Run(ctx, auth)

	got := rec.sentTypes()
	if len(got) == 0 {
		t.Fatal("an http loopback redirect_uri was refused before anything was requested; " +
			"this is the case that works, and refusing it would break every native OAuth client")
	}
	if got[0] != protocol.TypeBridgeRequest {
		t.Errorf("the first thing put on the wire was %q, want %q", got[0], protocol.TypeBridgeRequest)
	}
}
