package transport

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// DefaultLANPort is the default TCP port for LAN transport connections.
const DefaultLANPort = "9877"

const defaultHandshakeTimeout = 10 * time.Second

// LANTransportConfig holds mTLS configuration for LAN transport.
type LANTransportConfig struct {
	Cert                tls.Certificate      // Local identity (cert + private key)
	TrustedFingerprints []string             // SHA-256 fingerprints used when IsTrusted is nil
	IsTrusted           func(fp string) bool // Authoritative dynamic peer trust check when non-nil
	AllowPairing        bool                 // Allow untrusted peers to complete TLS handshake for in-band pairing

	// HandshakeTimeout bounds TLS handshakes. Values <= 0 use 10 seconds.
	HandshakeTimeout time.Duration
	// ReadTimeout and WriteTimeout reset the corresponding network deadline on
	// every Send/Receive. Values <= 0 leave application I/O deadlines unchanged.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	// OnHandshakeError observes rejected candidate connections. It may be called
	// concurrently and must return quickly.
	OnHandshakeError func(error)
	// ReturnHandshakeError makes Listener.Accept return the first rejected
	// candidate connection. Leave false for multiplexed listeners so one bad
	// peer cannot stop the accept loop.
	ReturnHandshakeError bool
}

// LANTransport implements Transport over mutual TLS for LAN connections.
type LANTransport struct {
	config               LANTransportConfig
	serverTLSConfig      *tls.Config
	clientTLSConfig      *tls.Config
	readTimeout          time.Duration
	writeTimeout         time.Duration
	handshakeTimeout     time.Duration
	returnHandshakeError bool
	onHandshakeError     func(error)
}

var _ Transport = (*LANTransport)(nil)
var _ Listener = (*lanListener)(nil)

func verifyLANPeerCertificate(rawCerts [][]byte, trustedFingerprints map[string]struct{}, isTrusted func(string) bool, allowPairing bool, expectedFingerprint string) error {
	if len(rawCerts) == 0 {
		return errors.New("mTLS verification failed: no peer certificate provided")
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("mTLS verification failed: parse certificate: %w", err)
	}
	hash := sha256.Sum256(cert.Raw)
	peerFP := hex.EncodeToString(hash[:])
	if expectedFingerprint != "" && !strings.EqualFold(peerFP, expectedFingerprint) {
		return fmt.Errorf("mTLS verification failed: peer fingerprint mismatch")
	}
	// When a live trust callback is supplied it is authoritative. Do not OR it
	// with the start-time snapshot: doing so would let an unpaired peer remain
	// authorized for the lifetime of a long-lived listener.
	if isTrusted != nil {
		if isTrusted(peerFP) {
			return nil
		}
	} else if _, ok := trustedFingerprints[strings.ToLower(peerFP)]; ok {
		return nil
	}
	if allowPairing {
		// A valid x509 certificate and possession of its private key were
		// proven by the TLS handshake. The application authorizes pairing.
		return nil
	}
	if len(trustedFingerprints) == 0 && isTrusted == nil {
		return errors.New("mTLS verification failed: no trusted peer fingerprints configured")
	}
	return fmt.Errorf("mTLS verification failed: untrusted peer certificate fingerprint %s", peerFP)
}

// NewLANTransport creates a new LANTransport with the given mTLS configuration.
func NewLANTransport(config LANTransportConfig) (*LANTransport, error) {
	if len(config.Cert.Certificate) == 0 || config.Cert.PrivateKey == nil {
		return nil, errors.New("LAN transport requires a TLS certificate and private key")
	}
	// Take a snapshot so later caller mutation cannot change the trust set.
	config.TrustedFingerprints = append([]string(nil), config.TrustedFingerprints...)
	trustedFingerprints := make(map[string]struct{}, len(config.TrustedFingerprints))
	for _, tf := range config.TrustedFingerprints {
		trustedFingerprints[strings.ToLower(tf)] = struct{}{}
	}

	verifyPeer := func(allowPairing bool) func([][]byte, [][]*x509.Certificate) error {
		return func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			return verifyLANPeerCertificate(rawCerts, trustedFingerprints, config.IsTrusted, allowPairing, "")
		}
	}

	serverTLS := &tls.Config{
		MinVersion:            tls.VersionTLS13,
		Certificates:          []tls.Certificate{config.Cert},
		ClientAuth:            tls.RequireAnyClientCert,
		InsecureSkipVerify:    true, // Certificate pinning is performed below.
		VerifyPeerCertificate: verifyPeer(config.AllowPairing),
	}

	clientTLS := &tls.Config{
		MinVersion:            tls.VersionTLS13,
		Certificates:          []tls.Certificate{config.Cert},
		InsecureSkipVerify:    true, // Certificate pinning is performed below.
		VerifyPeerCertificate: verifyPeer(false),
	}

	handshakeTimeout := config.HandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = defaultHandshakeTimeout
	}

	return &LANTransport{
		config:               config,
		serverTLSConfig:      serverTLS,
		clientTLSConfig:      clientTLS,
		readTimeout:          config.ReadTimeout,
		writeTimeout:         config.WriteTimeout,
		handshakeTimeout:     handshakeTimeout,
		returnHandshakeError: config.ReturnHandshakeError,
		onHandshakeError:     config.OnHandshakeError,
	}, nil
}

// Listen creates a mutual-TLS listener bound to the given address.
// If address is empty, defaults to "0.0.0.0:9877".
func (t *LANTransport) Listen(address string) (Listener, error) {
	if address == "" {
		address = net.JoinHostPort("0.0.0.0", DefaultLANPort)
	} else if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, DefaultLANPort)
	}

	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}

	tlsListener := tls.NewListener(l, t.serverTLSConfig)
	listener := &lanListener{
		listener:             tlsListener,
		handshakeTimeout:     t.handshakeTimeout,
		readTimeout:          t.readTimeout,
		writeTimeout:         t.writeTimeout,
		returnHandshakeError: t.returnHandshakeError,
		onHandshakeError:     t.onHandshakeError,
		closed:               make(chan struct{}),
		serveDone:            make(chan struct{}),
		results:              make(chan lanAcceptResult, 64),
		slots:                make(chan struct{}, 64),
		handshaking:          make(map[*tls.Conn]struct{}),
	}
	listener.wg.Add(1)
	go func() {
		defer listener.wg.Done()
		listener.serve()
	}()
	return listener, nil
}

// Dial connects to a LAN peer over mutual-TLS.
func (t *LANTransport) Dial(address string) (Conn, error) {
	if address == "" {
		return nil, errors.New("dial address cannot be empty")
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, DefaultLANPort)
	}

	dialer := &net.Dialer{Timeout: t.handshakeTimeout}
	tlsConn, err := tls.DialWithDialer(dialer, "tcp", address, t.clientTLSConfig)
	if err != nil {
		return nil, fmt.Errorf("lan mTLS dial %s: %w", address, err)
	}

	return newTCPConnWithTimeouts(tlsConn, t.readTimeout, t.writeTimeout), nil
}

// DialPinned establishes an operational LAN connection and verifies the
// authenticated leaf certificate before returning it to the caller. Pairing
// deliberately permits unknown certificates at the TLS layer, so a normal
// data dial must perform this second, destination-specific check; otherwise a
// substituted listener could receive OAuth URLs or QuickDrop bytes.
func (t *LANTransport) DialPinned(address, expectedFingerprint string) (Conn, error) {
	return t.DialPinnedContext(context.Background(), address, expectedFingerprint)
}

// DialPinnedContext is the cancellable form of DialPinned.
func (t *LANTransport) DialPinnedContext(ctx context.Context, address, expectedFingerprint string) (Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	expectedFingerprint = strings.ToLower(strings.TrimSpace(expectedFingerprint))
	if expectedFingerprint == "" {
		return nil, errors.New("expected peer fingerprint is required for a pinned LAN dial")
	}
	if address == "" {
		return nil, errors.New("dial address cannot be empty")
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, DefaultLANPort)
	}

	trusted := make(map[string]struct{}, len(t.config.TrustedFingerprints))
	for _, fingerprint := range t.config.TrustedFingerprints {
		trusted[strings.ToLower(fingerprint)] = struct{}{}
	}
	clientTLS := t.clientTLSConfig.Clone()
	clientTLS.VerifyPeerCertificate = func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		return verifyLANPeerCertificate(rawCerts, trusted, t.config.IsTrusted, false, expectedFingerprint)
	}
	dialer := &net.Dialer{Timeout: t.handshakeTimeout}
	tlsDialer := &tls.Dialer{NetDialer: dialer, Config: clientTLS}
	tlsConn, err := tlsDialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("pinned LAN mTLS dial %s: %w", address, err)
	}
	conn := newTCPConnWithTimeouts(tlsConn, t.readTimeout, t.writeTimeout)
	actualFingerprint := strings.ToLower(GetPeerFingerprint(conn))
	if actualFingerprint == "" || actualFingerprint != expectedFingerprint {
		_ = conn.Close()
		return nil, errors.New("pinned LAN peer fingerprint verification failed")
	}
	return conn, nil
}

type lanAcceptResult struct {
	conn Conn
	err  error
}

type lanListener struct {
	listener             net.Listener
	handshakeTimeout     time.Duration
	readTimeout          time.Duration
	writeTimeout         time.Duration
	returnHandshakeError bool
	onHandshakeError     func(error)

	closeOnce sync.Once
	closeErr  error
	closed    chan struct{}
	results   chan lanAcceptResult
	slots     chan struct{}

	mu          sync.Mutex
	handshaking map[*tls.Conn]struct{}
	serveDone   chan struct{}
	terminalErr error
	wg          sync.WaitGroup
}

func (l *lanListener) serve() {
	defer close(l.serveDone)
	for {
		nc, err := l.listener.Accept()
		if err != nil {
			select {
			case <-l.closed:
				return
			default:
				// A listener failure is not a candidate handshake failure. It
				// must terminate Accept even when rejected-candidate errors are
				// configured to be consumed by the dispatcher.
				l.mu.Lock()
				if l.terminalErr == nil {
					l.terminalErr = fmt.Errorf("lan listener accept: %w", err)
				}
				l.mu.Unlock()
				_ = l.listener.Close()
				l.closeHandshakes()
				return
			}
		}

		tc, ok := nc.(*tls.Conn)
		if !ok {
			_ = nc.Close()
			_, _ = l.rejectHandshake(nc, errors.New("accepted connection is not TLS"))
			continue
		}
		if !l.trackHandshake(tc) {
			_ = tc.Close()
			continue
		}
		select {
		case l.slots <- struct{}{}:
		default:
			l.untrackHandshake(tc)
			_ = tc.Close()
			_, _ = l.rejectHandshake(tc, errors.New("maximum concurrent TLS handshakes reached"))
			continue
		}
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			l.handshakeCandidate(tc)
		}()
	}
}

func (l *lanListener) handshakeCandidate(tc *tls.Conn) {
	defer func() { <-l.slots }()
	defer l.untrackHandshake(tc)

	if err := tc.SetDeadline(time.Now().Add(l.handshakeTimeout)); err != nil {
		_ = tc.Close()
		_, _ = l.rejectHandshake(tc, fmt.Errorf("set TLS handshake deadline: %w", err))
		return
	}
	if err := tc.Handshake(); err != nil {
		_ = tc.Close()
		_, _ = l.rejectHandshake(tc, err)
		return
	}
	if err := tc.SetDeadline(time.Time{}); err != nil {
		_ = tc.Close()
		_, _ = l.rejectHandshake(tc, fmt.Errorf("clear TLS handshake deadline: %w", err))
		return
	}

	conn := newTCPConnWithTimeouts(tc, l.readTimeout, l.writeTimeout)
	if !l.publishConnection(tc) {
		_ = conn.Close()
		return
	}
	select {
	case l.results <- lanAcceptResult{conn: conn}:
	case <-l.closed:
		_ = conn.Close()
	}
}

func (l *lanListener) trackHandshake(conn *tls.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.closed:
		return false
	default:
	}
	if l.handshaking == nil {
		l.handshaking = make(map[*tls.Conn]struct{})
	}
	l.handshaking[conn] = struct{}{}
	return true
}

func (l *lanListener) untrackHandshake(conn *tls.Conn) {
	l.mu.Lock()
	delete(l.handshaking, conn)
	l.mu.Unlock()
}

func (l *lanListener) closeHandshakes() {
	l.mu.Lock()
	conns := make([]*tls.Conn, 0, len(l.handshaking))
	for conn := range l.handshaking {
		conns = append(conns, conn)
	}
	l.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (l *lanListener) publishConnection(conn *tls.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.closed:
		return false
	default:
		return l.terminalErr == nil
	}
}

func (l *lanListener) Accept() (Conn, error) {
	for {
		l.mu.Lock()
		terminalErr := l.terminalErr
		l.mu.Unlock()
		if terminalErr != nil {
			return nil, terminalErr
		}
		select {
		case <-l.closed:
			return nil, net.ErrClosed
		case <-l.serveDone:
			l.mu.Lock()
			terminalErr = l.terminalErr
			l.mu.Unlock()
			if terminalErr != nil {
				return nil, terminalErr
			}
			return nil, net.ErrClosed
		case result := <-l.results:
			// Close and a terminal listener error take precedence over a result
			// that was queued concurrently. This prevents a post-close Accept
			// from handing a leaked connection to the dispatcher.
			select {
			case <-l.closed:
				if result.conn != nil {
					_ = result.conn.Close()
				}
				return nil, net.ErrClosed
			default:
			}
			l.mu.Lock()
			terminalErr = l.terminalErr
			l.mu.Unlock()
			if terminalErr != nil {
				if result.conn != nil {
					_ = result.conn.Close()
				}
				return nil, terminalErr
			}
			if result.conn == nil && result.err == nil {
				return nil, net.ErrClosed
			}
			return result.conn, result.err
		}
	}
}

// rejectHandshake reports a classified candidate-connection failure. The
// boolean is true when strict listener behavior was requested.
func (l *lanListener) rejectHandshake(conn net.Conn, err error) (error, bool) {
	remote := ""
	if conn != nil && conn.RemoteAddr() != nil {
		remote = conn.RemoteAddr().String()
	}
	handshakeErr := &HandshakeError{Transport: "lan", RemoteAddr: remote, Err: err}
	if l.onHandshakeError != nil {
		l.onHandshakeError(handshakeErr)
	}
	if l.returnHandshakeError {
		select {
		case l.results <- lanAcceptResult{err: handshakeErr}:
		case <-l.closed:
		}
	}
	return handshakeErr, l.returnHandshakeError
}

func (l *lanListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		l.closeErr = l.listener.Close()
		l.closeHandshakes()
	})
	// Wait for the accept loop and all in-flight handshakes. They all observe
	// closed and cannot enqueue new work after this point.
	l.wg.Wait()
	for {
		select {
		case result := <-l.results:
			if result.conn != nil {
				_ = result.conn.Close()
			}
		default:
			return l.closeErr
		}
	}
}

func (l *lanListener) Addr() net.Addr {
	return l.listener.Addr()
}
