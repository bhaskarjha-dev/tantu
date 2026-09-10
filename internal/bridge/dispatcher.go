package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// PrefetchedConn wraps a transport.Conn and replays a prefetched protocol.Envelope
// on the first call to Receive. All subsequent calls delegate directly to the underlying Conn.
type PrefetchedConn struct {
	transport.Conn
	firstEnv  *protocol.Envelope
	delivered bool
	mu        sync.Mutex
}

// NewPrefetchedConn creates a new PrefetchedConn wrapping conn with firstEnv.
func NewPrefetchedConn(conn transport.Conn, firstEnv *protocol.Envelope) *PrefetchedConn {
	return &PrefetchedConn{
		Conn:     conn,
		firstEnv: firstEnv,
	}
}

// Receive returns firstEnv on its first invocation, and delegates to the underlying Conn afterwards.
func (p *PrefetchedConn) Receive() (*protocol.Envelope, error) {
	p.mu.Lock()
	if !p.delivered && p.firstEnv != nil {
		p.delivered = true
		env := p.firstEnv
		p.mu.Unlock()
		return env, nil
	}
	p.mu.Unlock()
	return p.Conn.Receive()
}

// PeerFingerprint delegates cryptographic identity retrieval to the underlying Conn.
func (p *PrefetchedConn) PeerFingerprint() string {
	return transport.GetPeerFingerprint(p.Conn)
}

// SetDeadline delegates to the underlying Conn if supported.
func (p *PrefetchedConn) SetDeadline(t time.Time) error {
	if dl, ok := p.Conn.(transport.Deadliner); ok {
		return dl.SetDeadline(t)
	}
	return nil
}

// SetReadDeadline delegates to the underlying Conn if supported.
func (p *PrefetchedConn) SetReadDeadline(t time.Time) error {
	if dl, ok := p.Conn.(transport.Deadliner); ok {
		return dl.SetReadDeadline(t)
	}
	return nil
}

// SetWriteDeadline delegates to the underlying Conn if supported.
func (p *PrefetchedConn) SetWriteDeadline(t time.Time) error {
	if dl, ok := p.Conn.(transport.Deadliner); ok {
		return dl.SetWriteDeadline(t)
	}
	return nil
}

// DispatcherConfig configures the multiplexed Dispatcher.
type DispatcherConfig struct {
	ASideConfig      ASideConfig
	DropConfig       drop.ReceiveDropConfig
	DropWriter       io.Writer // Optional fallback writer for QuickDrop if OnMeta is nil
	OnDropReceived   func(res *drop.ReceiveDropResult)
	OnDropDone       func(dropID string, res *drop.ReceiveDropResult, err error)
	OnASideDone      func(err error)
	OnPairing        func(conn transport.Conn, firstEnv *protocol.Envelope)
	IsPeerTrusted    func(fingerprint string) bool // Optional authorization callback to verify peer identity
	Logger           *log.Logger
	HandshakeTimeout time.Duration
}

// Dispatcher multiplexes incoming connections on a single transport.Listener,
// inspecting the initial envelope and routing dynamically to either the OAuth ASide handler
// or the QuickDrop receiver.
type Dispatcher struct {
	listener       transport.Listener
	cfg            DispatcherConfig
	log            *log.Logger
	activeSessions atomic.Int64
}

// NewDispatcher creates a new Dispatcher for the given listener.
func NewDispatcher(listener transport.Listener, cfg DispatcherConfig) *Dispatcher {
	l := cfg.Logger
	if l == nil {
		l = log.Default()
	}
	return &Dispatcher{
		listener: listener,
		cfg:      cfg,
		log:      l,
	}
}

// ActiveSessions returns the number of currently active multiplexed sessions.
func (d *Dispatcher) ActiveSessions() int64 {
	return d.activeSessions.Load()
}

func (d *Dispatcher) logf(format string, v ...any) {
	if d.log != nil {
		d.log.Printf(format, v...)
	}
}

// Serve accepts connections on the listener and routes each connection based on its
// opening envelope type. It blocks until ctx is cancelled or the listener is closed.
func (d *Dispatcher) Serve(ctx context.Context) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	var mu sync.Mutex
	activeConns := make(map[transport.Conn]struct{})

	stopCh := make(chan struct{})
	defer close(stopCh)

	go func() {
		select {
		case <-ctx.Done():
			_ = d.listener.Close()
			mu.Lock()
			for c := range activeConns {
				_ = c.Close()
			}
			mu.Unlock()
		case <-stopCh:
		}
	}()

	for {
		conn, err := d.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}

		if d.activeSessions.Load() >= 100 {
			d.logf("⚠️ Connection rejected from %s: max active sessions (100) reached", conn.RemoteAddr())
			_ = conn.Close()
			continue
		}

		mu.Lock()
		activeConns[conn] = struct{}{}
		mu.Unlock()

		wg.Add(1)
		d.activeSessions.Add(1)
		go func(c transport.Conn) {
			defer wg.Done()
			defer d.activeSessions.Add(-1)
			defer func() {
				mu.Lock()
				delete(activeConns, c)
				mu.Unlock()
			}()
			d.handleConn(ctx, c)
		}(conn)
	}
}

// ServeDispatcher is a convenience function that creates and runs a Dispatcher.
func ServeDispatcher(ctx context.Context, listener transport.Listener, cfg DispatcherConfig) error {
	return NewDispatcher(listener, cfg).Serve(ctx)
}

func (d *Dispatcher) handleConn(ctx context.Context, conn transport.Conn) {
	defer conn.Close()

	handshakeTimeout := d.cfg.HandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = 30 * time.Second
	}

	if dl, ok := conn.(transport.Deadliner); ok {
		_ = dl.SetReadDeadline(time.Now().Add(handshakeTimeout))
	}

	// Read initial envelope to inspect protocol type
	var firstEnv *protocol.Envelope
	for {
		env, err := conn.Receive()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				d.logf("⚠️ Error receiving initial envelope from %s: %v", conn.RemoteAddr(), err)
			}
			return
		}

		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			_ = env.DecodePayload(&hb)
			_ = conn.Send(protocol.TypeHeartbeat, hb)
			continue
		}

		firstEnv = env
		break
	}

	// Clear read deadline for active session
	if dl, ok := conn.(transport.Deadliner); ok {
		_ = dl.SetReadDeadline(time.Time{})
	}

	prefetched := NewPrefetchedConn(conn, firstEnv)
	peerFP := transport.GetPeerFingerprint(conn)

	switch {
	case firstEnv.Type == protocol.TypeBridgeRequest:
		if d.cfg.IsPeerTrusted != nil && !d.cfg.IsPeerTrusted(peerFP) {
			d.logf("⚠️ Unauthorized OAuth bridge attempt from untrusted peer %s (fp: %s)", conn.RemoteAddr(), peerFP)
			var req protocol.BridgeRequest
			_ = firstEnv.DecodePayload(&req)
			_ = conn.Send(protocol.TypeBridgeAck, protocol.BridgeAck{
				RequestID: req.RequestID,
				Error:     "unauthorized: peer certificate not paired or trusted",
			})
			return
		}
		d.logf("[DEBUG] 🔀 Multiplexer: routing connection from %s to OAuth ASide", conn.RemoteAddr())
		err := HandleASide(ctx, prefetched, d.cfg.ASideConfig)
		if d.cfg.OnASideDone != nil {
			d.cfg.OnASideDone(err)
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			d.logf("❌ OAuth ASide error from %s: %v", conn.RemoteAddr(), err)
		}

	case firstEnv.Type == drop.TypeDropSend:
		if d.cfg.IsPeerTrusted != nil && !d.cfg.IsPeerTrusted(peerFP) {
			d.logf("⚠️ Unauthorized QuickDrop transfer attempt from untrusted peer %s (fp: %s)", conn.RemoteAddr(), peerFP)
			var meta drop.DropSend
			_ = firstEnv.DecodePayload(&meta)
			_ = conn.Send(drop.TypeDropAck, drop.DropAck{
				DropID:   meta.DropID,
				Accepted: false,
				Error:    "unauthorized: peer certificate not paired or trusted",
			})
			return
		}
		d.logf("[DEBUG] 🔀 Multiplexer: routing connection from %s to QuickDrop receiver", conn.RemoteAddr())
		var meta drop.DropSend
		_ = firstEnv.DecodePayload(&meta)

		res, err := drop.ReceiveDrop(ctx, prefetched, d.cfg.DropWriter, d.cfg.DropConfig)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				d.logf("❌ QuickDrop receive error from %s: %v", conn.RemoteAddr(), err)
			}
		} else if d.cfg.OnDropReceived != nil {
			d.cfg.OnDropReceived(res)
		}
		if d.cfg.OnDropDone != nil {
			d.cfg.OnDropDone(meta.DropID, res, err)
		}

	case strings.HasPrefix(firstEnv.Type, "pair_"):
		d.logf("[DEBUG] 🔀 Multiplexer: routing connection from %s to In-Band Pairing", conn.RemoteAddr())
		if d.cfg.OnPairing != nil {
			d.cfg.OnPairing(conn, firstEnv)
		} else {
			d.logf("⚠️ Multiplexer: no in-band pairing handler configured for %s", conn.RemoteAddr())
		}

	default:
		d.logf("⚠️ Multiplexer: unknown initial message type %q from %s", firstEnv.Type, conn.RemoteAddr())
	}
}
