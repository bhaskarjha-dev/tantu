package transport

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

// LoopbackTransport implements Transport over localhost TCP (127.0.0.1).
type LoopbackTransport struct {
	readTimeout  time.Duration
	writeTimeout time.Duration
}

// NewLoopbackTransport creates a new LoopbackTransport without implicit
// application I/O timeouts. Deadliner methods remain available on each Conn.
func NewLoopbackTransport() *LoopbackTransport {
	return &LoopbackTransport{}
}

// NewLoopbackTransportWithTimeouts creates a loopback transport that resets
// the corresponding network deadline before every Send and Receive. Values
// <= 0 leave application I/O deadlines unchanged.
func NewLoopbackTransportWithTimeouts(readTimeout, writeTimeout time.Duration) *LoopbackTransport {
	return &LoopbackTransport{readTimeout: readTimeout, writeTimeout: writeTimeout}
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

	return &tcpListener{
		listener:     l,
		readTimeout:  t.readTimeout,
		writeTimeout: t.writeTimeout,
	}, nil
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
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	nc, err := dialer.Dial("tcp", dialAddr)
	if err != nil {
		return nil, err
	}

	return newTCPConnWithTimeouts(nc, t.readTimeout, t.writeTimeout), nil
}

type tcpListener struct {
	listener     net.Listener
	readTimeout  time.Duration
	writeTimeout time.Duration
	closeOnce    sync.Once
	closeErr     error
}

func (l *tcpListener) Accept() (Conn, error) {
	nc, err := l.listener.Accept()
	if err != nil {
		return nil, err
	}
	return newTCPConnWithTimeouts(nc, l.readTimeout, l.writeTimeout), nil
}

func (l *tcpListener) Close() error {
	l.closeOnce.Do(func() {
		if l.listener != nil {
			l.closeErr = l.listener.Close()
		}
	})
	return l.closeErr
}

func (l *tcpListener) Addr() net.Addr {
	return l.listener.Addr()
}

type tcpConn struct {
	conn    net.Conn
	encoder *protocol.Encoder
	decoder *protocol.Decoder

	readTimeout  time.Duration
	writeTimeout time.Duration

	closeOnce sync.Once
	closeErr  error
	closed    atomic.Bool
}

func newTCPConn(nc net.Conn) *tcpConn {
	return newTCPConnWithTimeouts(nc, 0, 0)
}

func newTCPConnWithTimeouts(nc net.Conn, readTimeout, writeTimeout time.Duration) *tcpConn {
	return &tcpConn{
		conn:         nc,
		encoder:      protocol.NewEncoder(nc),
		decoder:      protocol.NewDecoder(nc),
		readTimeout:  readTimeout,
		writeTimeout: writeTimeout,
	}
}

func (c *tcpConn) Send(msgType string, payload any) error {
	if c.closed.Load() {
		return errors.New("connection is closed")
	}
	if c.writeTimeout > 0 {
		if err := c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout)); err != nil {
			return fmt.Errorf("set write deadline: %w", err)
		}
	}
	err := c.encoder.Encode(msgType, payload)
	if isTimeoutError(err) {
		_ = c.Close()
	}
	return err
}

func (c *tcpConn) Receive() (*protocol.Envelope, error) {
	if c.closed.Load() {
		return nil, errors.New("connection is closed")
	}
	if c.readTimeout > 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(c.readTimeout)); err != nil {
			return nil, fmt.Errorf("set read deadline: %w", err)
		}
	}
	env, err := c.decoder.Decode()
	if isTimeoutError(err) {
		_ = c.Close()
	}
	return env, err
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (c *tcpConn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if c.conn != nil {
			c.closeErr = c.conn.Close()
		}
	})
	return c.closeErr
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
	if c.closed.Load() {
		return errors.New("connection is closed")
	}
	return c.conn.SetDeadline(t)
}

func (c *tcpConn) SetReadDeadline(t time.Time) error {
	if c.closed.Load() {
		return errors.New("connection is closed")
	}
	return c.conn.SetReadDeadline(t)
}

func (c *tcpConn) SetWriteDeadline(t time.Time) error {
	if c.closed.Load() {
		return errors.New("connection is closed")
	}
	return c.conn.SetWriteDeadline(t)
}
