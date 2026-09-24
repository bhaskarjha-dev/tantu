// Package transport defines network transport abstractions and implementations
// for communication between Computer A and Computer B.
package transport

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

// ErrHandshake identifies a per-connection TLS or SSH handshake failure.
var ErrHandshake = errors.New("transport handshake failed")

// HandshakeError classifies a failure associated with one candidate
// connection. Listener implementations consume these errors by default so a
// malformed or unauthorized peer cannot terminate a multiplexed accept loop.
type HandshakeError struct {
	Transport  string
	RemoteAddr string
	Err        error
}

func (e *HandshakeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	remote := e.RemoteAddr
	if remote == "" {
		remote = "unknown"
	}
	transport := e.Transport
	if transport == "" {
		transport = "transport"
	}
	prefix := transport + " handshake with " + remote + " failed"
	if e.Err == nil {
		return prefix
	}
	return prefix + ": " + e.Err.Error()
}

func (e *HandshakeError) Unwrap() error { return e.Err }

// Is allows errors.Is(err, ErrHandshake).
func (e *HandshakeError) Is(target error) bool { return target == ErrHandshake }

// Timeout preserves net.Error timeout classification for callers that inspect
// the original cause.
func (e *HandshakeError) Timeout() bool {
	var netErr net.Error
	return errors.As(e.Err, &netErr) && netErr.Timeout()
}

// Temporary is retained for compatibility with the historical net.Error
// interface. It reports that the failure applies to one candidate connection.
func (e *HandshakeError) Temporary() bool { return true }

// IsHandshakeError reports whether err is or wraps a HandshakeError.
func IsHandshakeError(err error) bool {
	var handshakeErr *HandshakeError
	return errors.As(err, &handshakeErr)
}

// Conn represents a bidirectional connection between A-side and B-side.
// It wraps a protocol.Encoder and protocol.Decoder for message exchange.
type Conn interface {
	// Send encodes and sends a protocol message.
	Send(msgType string, payload any) error
	// Receive decodes and returns the next protocol message.
	Receive() (*protocol.Envelope, error)
	// Close closes the underlying connection.
	Close() error
	// LocalAddr returns the local network address.
	LocalAddr() net.Addr
	// RemoteAddr returns the remote network address.
	RemoteAddr() net.Addr
}

// Listener accepts incoming transport connections.
type Listener interface {
	// Accept waits for and returns the next successfully established connection.
	// Implementations may report rejected candidate connections through their
	// transport-specific error callback without returning them from Accept.
	Accept() (Conn, error)
	// Close stops the listener.
	Close() error
	// Addr returns the listener's network address.
	Addr() net.Addr
}

// Transport defines how A-side and B-side establish connections.
type Transport interface {
	// Listen creates a listener bound to the given address.
	// For loopback, address is "127.0.0.1:PORT" or "127.0.0.1:0" for dynamic port.
	Listen(address string) (Listener, error)
	// Dial connects to the given address.
	Dial(address string) (Conn, error)
}

// PeerPinningTransport is an optional transport capability for authenticated
// operational dials. A transport that implements it must verify the peer's
// cryptographic identity against expectedFingerprint before returning the
// connection. Pairing listeners may accept unknown certificates, but a normal
// data connection must not inherit that permissive policy.
type PeerPinningTransport interface {
	DialPinned(address, expectedFingerprint string) (Conn, error)
}

// ContextPeerPinningTransport is the cancellable form used by background
// discovery work. It is optional so existing transports remain source
// compatible.
type ContextPeerPinningTransport interface {
	DialPinnedContext(ctx context.Context, address, expectedFingerprint string) (Conn, error)
}

// DialPinned uses the transport's pinning capability when available and
// otherwise preserves compatibility with transports that have no remote
// identity (for example loopback and test transports).
func DialPinned(tr Transport, address, expectedFingerprint string) (Conn, error) {
	return DialPinnedContext(context.Background(), tr, address, expectedFingerprint)
}

// DialPinnedContext is DialPinned with cancellation for callers such as Hub
// roaming probes and HTTP-triggered operations.
func DialPinnedContext(ctx context.Context, tr Transport, address, expectedFingerprint string) (Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if pinning, ok := tr.(ContextPeerPinningTransport); ok {
		return pinning.DialPinnedContext(ctx, address, expectedFingerprint)
	}
	if pinning, ok := tr.(PeerPinningTransport); ok {
		return pinning.DialPinned(address, expectedFingerprint)
	}
	return tr.Dial(address)
}

// PeerIdentity is an optional interface implemented by transport connections
// that carry cryptographic peer identity (e.g. mutual TLS).
type PeerIdentity interface {
	PeerFingerprint() string
}

// GetPeerFingerprint retrieves the cryptographic fingerprint (lowercase hex SHA-256)
// of the connected peer, or an empty string if not available.
func GetPeerFingerprint(conn Conn) string {
	if pi, ok := conn.(PeerIdentity); ok {
		return pi.PeerFingerprint()
	}
	return ""
}

// Deadliner is an optional interface implemented by transport connections
// that support I/O deadlines.
type Deadliner interface {
	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}
