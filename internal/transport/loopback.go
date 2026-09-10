package transport

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

// LoopbackTransport implements Transport over localhost TCP (127.0.0.1).
type LoopbackTransport struct{}

// NewLoopbackTransport creates a new LoopbackTransport.
func NewLoopbackTransport() *LoopbackTransport {
	return &LoopbackTransport{}
}

// Listen creates a TCP listener strictly bound to 127.0.0.1.
// Address must be in the format "127.0.0.1:PORT" or "127.0.0.1:0" for dynamic allocation.
func (t *LoopbackTransport) Listen(address string) (Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", address, err)
	}

	if host != "127.0.0.1" && host != "localhost" && host != "" {
		return nil, fmt.Errorf("loopback transport must bind to 127.0.0.1 only (got %q)", host)
	}

	// Always bind strictly to 127.0.0.1
	bindAddr := net.JoinHostPort("127.0.0.1", port)
	l, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return nil, err
	}

	return &tcpListener{listener: l}, nil
}

// Dial connects to a loopback address over TCP (127.0.0.1).
func (t *LoopbackTransport) Dial(address string) (Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", address, err)
	}

	if host != "127.0.0.1" && host != "localhost" {
		return nil, fmt.Errorf("loopback transport must dial 127.0.0.1 only (got %q)", host)
	}

	dialAddr := net.JoinHostPort("127.0.0.1", port)
	nc, err := net.Dial("tcp", dialAddr)
	if err != nil {
		return nil, err
	}

	return newTCPConn(nc), nil
}

type tcpListener struct {
	listener net.Listener
}

func (l *tcpListener) Accept() (Conn, error) {
	nc, err := l.listener.Accept()
	if err != nil {
		return nil, err
	}
	return newTCPConn(nc), nil
}

func (l *tcpListener) Close() error {
	if l.listener == nil {
		return nil
	}
	return l.listener.Close()
}

func (l *tcpListener) Addr() net.Addr {
	return l.listener.Addr()
}

type tcpConn struct {
	conn    net.Conn
	encoder *protocol.Encoder
	decoder *protocol.Decoder
}

func newTCPConn(nc net.Conn) *tcpConn {
	return &tcpConn{
		conn:    nc,
		encoder: protocol.NewEncoder(nc),
		decoder: protocol.NewDecoder(nc),
	}
}

func (c *tcpConn) Send(msgType string, payload any) error {
	if c.conn == nil {
		return errors.New("connection is closed")
	}
	return c.encoder.Encode(msgType, payload)
}

func (c *tcpConn) Receive() (*protocol.Envelope, error) {
	if c.conn == nil {
		return nil, errors.New("connection is closed")
	}
	return c.decoder.Decode()
}

func (c *tcpConn) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *tcpConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *tcpConn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

// PeerFingerprint returns the lowercase hex SHA-256 fingerprint of the verified
// peer certificate if the underlying connection is an mTLS connection, or empty string.
func (c *tcpConn) PeerFingerprint() string {
	if c.conn == nil {
		return ""
	}
	if tc, ok := c.conn.(*tls.Conn); ok {
		state := tc.ConnectionState()
		if len(state.PeerCertificates) > 0 {
			hash := sha256.Sum256(state.PeerCertificates[0].Raw)
			return hex.EncodeToString(hash[:])
		}
	}
	return ""
}

func (c *tcpConn) SetDeadline(t time.Time) error {
	if c.conn == nil {
		return errors.New("connection is closed")
	}
	return c.conn.SetDeadline(t)
}

func (c *tcpConn) SetReadDeadline(t time.Time) error {
	if c.conn == nil {
		return errors.New("connection is closed")
	}
	return c.conn.SetReadDeadline(t)
}

func (c *tcpConn) SetWriteDeadline(t time.Time) error {
	if c.conn == nil {
		return errors.New("connection is closed")
	}
	return c.conn.SetWriteDeadline(t)
}
