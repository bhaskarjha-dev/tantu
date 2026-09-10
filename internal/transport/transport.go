// Package transport defines network transport abstractions and implementations
// for communication between Computer A and Computer B.
package transport

import (
	"net"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

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
	// Accept waits for and returns the next connection.
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
