package pairing

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

// In-band pairing protocol message types.
const (
	TypePairHello     = "pair_hello"
	TypePairDecision  = "pair_decision"
	LegacyPairingPort = "9878"
)

// ErrInBandProtocolUnsupported indicates that a peer understood the TLS
// connection but did not understand the framed in-band pairing protocol. It
// is the only class of handshake error for which legacy compatibility fallback
// is safe; arbitrary TLS/certificate/user-decision failures must not trigger a
// second SAS prompt.
var ErrInBandProtocolUnsupported = errors.New("in-band pairing protocol unsupported")

// PairingOptions carries the actual listener identity that cannot be inferred
// from an outbound client socket's ephemeral LocalAddr.
type PairingOptions struct {
	LocalListenPort int
}

func (o PairingOptions) listenPort() int {
	if o.LocalListenPort > 0 && o.LocalListenPort <= 65535 {
		return o.LocalListenPort
	}
	return 0
}

// Conn represents a bidirectional framed connection for protocol exchange.
type Conn interface {
	Send(msgType string, payload any) error
	Receive() (*protocol.Envelope, error)
	Close() error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

// PairHelloPayload represents the payload of a pair_hello protocol message.
func validatePairHelloFields(certPEM []byte, name string, listenPort int) error {
	if len(certPEM) == 0 || len(certPEM) > maxPairMessageSize {
		return errors.New("pair hello certificate is missing or too large")
	}
	if len(name) > 256 {
		return errors.New("pair hello name is too long")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("pair hello name contains a control character")
		}
	}
	if listenPort < 0 || listenPort > 65535 {
		return errors.New("pair hello listen port is invalid")
	}
	return nil
}

func validatePairHello(hello PairHelloPayload) error {
	return validatePairHelloFields(hello.CertPEM, hello.Name, hello.ListenPort)
}

type PairHelloPayload struct {
	CertPEM    []byte `json:"cert_pem"`
	Name       string `json:"name,omitempty"`
	ListenPort int    `json:"listen_port,omitempty"`
}

// PairDecisionPayload represents the payload of a pair_decision protocol message.
type PairDecisionPayload struct {
	Accept bool   `json:"accept"`
	Reason string `json:"reason,omitempty"`
}

// FramedConn wraps a net.Conn with protocol length-prefixed JSON framing.
type FramedConn struct {
	conn    net.Conn
	encoder *protocol.Encoder
	decoder *protocol.Decoder
}

// NewFramedConn creates a new FramedConn wrapping nc.
func NewFramedConn(nc net.Conn) *FramedConn {
	decoder := protocol.NewDecoder(nc)
	// Pairing carries only small JSON control messages. Do not allocate the
	// general 4 MiB data-frame allowance for an unauthenticated pairing frame.
	decoder.SetMaxMessageSize(maxPairMessageSize)
	return &FramedConn{
		conn:    nc,
		encoder: protocol.NewEncoder(nc),
		decoder: decoder,
	}
}

// Send encodes and writes a framed message.
func (c *FramedConn) Send(msgType string, payload any) error {
	if c.conn == nil {
		return errors.New("connection is closed")
	}
	return c.encoder.Encode(msgType, payload)
}

// Receive decodes and returns the next framed message.
func (c *FramedConn) Receive() (*protocol.Envelope, error) {
	if c.conn == nil {
		return nil, errors.New("connection is closed")
	}
	return c.decoder.Decode()
}

// Close closes the underlying network connection.
func (c *FramedConn) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// LocalAddr returns the local network address.
func (c *FramedConn) LocalAddr() net.Addr {
	if c.conn == nil {
		return nil
	}
	return c.conn.LocalAddr()
}

// RemoteAddr returns the remote network address.
func (c *FramedConn) RemoteAddr() net.Addr {
	if c.conn == nil {
		return nil
	}
	return c.conn.RemoteAddr()
}

// PeerFingerprint returns the verified TLS leaf fingerprint when the framed
// connection is TLS. It is intentionally optional so plain test/in-process
// connections can still use the pairing protocol.
func (c *FramedConn) PeerFingerprint() string {
	if c.conn == nil {
		return ""
	}
	tlsConn, ok := c.conn.(*tls.Conn)
	if !ok {
		return ""
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return ""
	}
	digest := sha256.Sum256(state.PeerCertificates[0].Raw)
	return hex.EncodeToString(digest[:])
}

func (c *FramedConn) SetDeadline(t time.Time) error {
	if c.conn == nil {
		return errors.New("connection is closed")
	}
	return c.conn.SetDeadline(t)
}

func (c *FramedConn) SetReadDeadline(t time.Time) error {
	if c.conn == nil {
		return errors.New("connection is closed")
	}
	return c.conn.SetReadDeadline(t)
}

func (c *FramedConn) SetWriteDeadline(t time.Time) error {
	if c.conn == nil {
		return errors.New("connection is closed")
	}
	return c.conn.SetWriteDeadline(t)
}

type pairPeerIdentity interface {
	PeerFingerprint() string
}

func advertisedListenPort(conn Conn) int {
	// A TLS client wrapper's LocalAddr is its ephemeral source port, not a
	// listening socket. Never persist that value as a peer address.
	if _, ok := conn.(*FramedConn); ok {
		return 0
	}
	identity, ok := conn.(pairPeerIdentity)
	if !ok || identity.PeerFingerprint() == "" {
		// Plain in-process/test connections do not carry a wire listener
		// advertisement; their ephemeral local port must not be persisted.
		return 0
	}
	return localListenPort(conn.LocalAddr())
}

func pairingListenPort(conn Conn, options PairingOptions) int {
	if port := options.listenPort(); port != 0 {
		return port
	}
	return advertisedListenPort(conn)
}

func localListenPort(addr net.Addr) int {
	if addr == nil {
		return 0
	}
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		return tcpAddr.Port
	}
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0
	}
	var value int
	_, _ = fmt.Sscanf(port, "%d", &value)
	return value
}

func verifyPairTransportIdentity(conn Conn, claimedFingerprint string) error {
	identity, ok := conn.(pairPeerIdentity)
	if !ok {
		return nil
	}
	transportFingerprint := identity.PeerFingerprint()
	if transportFingerprint != "" && !strings.EqualFold(transportFingerprint, claimedFingerprint) {
		return fmt.Errorf("pair hello certificate does not match TLS peer certificate")
	}
	return nil
}

func receivePairEnvelope(ctx context.Context, conn Conn) (*protocol.Envelope, error) {
	stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopConn()
	if dl, ok := conn.(interface{ SetReadDeadline(time.Time) error }); ok {
		if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
			_ = dl.SetReadDeadline(deadline)
			defer func() { _ = dl.SetReadDeadline(time.Time{}) }()
		}
		return conn.Receive()
	}
	type result struct {
		env *protocol.Envelope
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		env, err := conn.Receive()
		resultCh <- result{env: env, err: err}
	}()
	select {
	case <-ctx.Done():
		_ = conn.Close()
		return nil, ctx.Err()
	case result := <-resultCh:
		return result.env, result.err
	}
}

// HandleInboundPairing processes an incoming in-band pairing handshake over a Conn.
// If firstEnv is nil, it reads the initial envelope from conn.
func HandleInboundPairing(ctx context.Context, conn Conn, firstEnv *protocol.Envelope, store *PeerStore, confirmFn func(peerSAS, localSAS string) bool) (*PairResult, error) {
	return HandleInboundPairingWithOptions(ctx, conn, firstEnv, store, confirmFn, PairingOptions{})
}

// HandleInboundPairingWithOptions is HandleInboundPairing with an explicit
// local listener port for dynamic-port deployments.
func HandleInboundPairingWithOptions(ctx context.Context, conn Conn, firstEnv *protocol.Envelope, store *PeerStore, confirmFn func(peerSAS, localSAS string) bool, options PairingOptions) (*PairResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if firstEnv == nil {
		env, err := receivePairEnvelope(ctx, conn)
		if err != nil {
			return nil, fmt.Errorf("receive pair hello: %w", err)
		}
		firstEnv = env
	}
	if firstEnv.Type != TypePairHello {
		return nil, fmt.Errorf("%w: unexpected message type %q, expected %q", ErrInBandProtocolUnsupported, firstEnv.Type, TypePairHello)
	}

	var peerHello PairHelloPayload
	if err := firstEnv.DecodePayload(&peerHello); err != nil {
		return nil, fmt.Errorf("decode pair hello: %w", err)
	}
	if err := validatePairHello(peerHello); err != nil {
		return nil, err
	}

	peerFP, err := Fingerprint(peerHello.CertPEM)
	if err != nil {
		return nil, fmt.Errorf("compute peer fingerprint: %w", err)
	}
	if err := verifyPairTransportIdentity(conn, peerFP); err != nil {
		return nil, err
	}

	id, err := getOrGenerateIdentity(store)
	if err != nil {
		return nil, fmt.Errorf("load identity: %w", err)
	}

	localSAS := SASCode(id.Fingerprint)
	peerSAS := SASCode(peerFP)

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "initiator"
	}

	// 1. Send our PairHello
	if err := conn.Send(TypePairHello, PairHelloPayload{
		CertPEM:    id.CertPEM,
		Name:       hostname,
		ListenPort: pairingListenPort(conn, options),
	}); err != nil {
		return nil, fmt.Errorf("send pair hello: %w", err)
	}

	// 2. SAS Confirmation
	localAccepted := invokeConfirm(confirmFn, peerSAS, localSAS)

	// 3. Send local decision
	if err := conn.Send(TypePairDecision, PairDecisionPayload{
		Accept: localAccepted,
	}); err != nil {
		return nil, fmt.Errorf("send pair decision: %w", err)
	}

	// 4. Receive peer decision
	decEnv, err := receivePairEnvelope(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("receive peer decision: %w", err)
	}
	if decEnv.Type != TypePairDecision {
		return nil, fmt.Errorf("unexpected message type %q, expected %q", decEnv.Type, TypePairDecision)
	}

	var peerDec PairDecisionPayload
	if err := decEnv.DecodePayload(&peerDec); err != nil {
		return nil, fmt.Errorf("decode peer decision: %w", err)
	}

	success := localAccepted && peerDec.Accept
	result := &PairResult{
		PeerFingerprint: peerFP,
		PeerSAS:         peerSAS,
		LocalSAS:        localSAS,
		Accepted:        success,
	}

	if success {
		peerAddr := addressWithPort(conn.RemoteAddr(), peerHello.ListenPort)
		peerName := peerHello.Name
		if peerName == "" {
			peerName = "peer-" + peerSAS
		}
		if err := store.AddPeer(Peer{
			Fingerprint: peerFP,
			Name:        peerName,
			Address:     peerAddr,
			CertPEM:     peerHello.CertPEM,
		}); err != nil {
			return nil, fmt.Errorf("save trusted peer: %w", err)
		}
	}

	return result, nil
}

// PairResponderInBand connects to a peer over an established Conn and executes the pairing exchange.
func PairResponderInBand(ctx context.Context, conn Conn, store *PeerStore, confirmFn func(peerSAS, localSAS string) bool) (*PairResult, error) {
	return PairResponderInBandWithOptions(ctx, conn, store, confirmFn, PairingOptions{})
}

// PairResponderInBandWithOptions is PairResponderInBand with an explicit local
// listener port for dynamic-port deployments.
func PairResponderInBandWithOptions(ctx context.Context, conn Conn, store *PeerStore, confirmFn func(peerSAS, localSAS string) bool, options PairingOptions) (*PairResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	id, err := getOrGenerateIdentity(store)
	if err != nil {
		return nil, fmt.Errorf("load identity: %w", err)
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "responder"
	}

	// 1. Send our PairHello
	if err := conn.Send(TypePairHello, PairHelloPayload{
		CertPEM:    id.CertPEM,
		Name:       hostname,
		ListenPort: pairingListenPort(conn, options),
	}); err != nil {
		return nil, fmt.Errorf("send responder hello: %w", err)
	}

	// 2. Receive peer's PairHello
	env, err := receivePairEnvelope(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("receive initiator hello: %w", err)
	}
	if env.Type != TypePairHello {
		return nil, fmt.Errorf("%w: unexpected message type %q, expected %q", ErrInBandProtocolUnsupported, env.Type, TypePairHello)
	}

	var peerHello PairHelloPayload
	if err := env.DecodePayload(&peerHello); err != nil {
		return nil, fmt.Errorf("decode initiator hello: %w", err)
	}
	if err := validatePairHello(peerHello); err != nil {
		return nil, err
	}

	peerFP, err := Fingerprint(peerHello.CertPEM)
	if err != nil {
		return nil, fmt.Errorf("compute peer fingerprint: %w", err)
	}
	if err := verifyPairTransportIdentity(conn, peerFP); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	localSAS := SASCode(id.Fingerprint)
	peerSAS := SASCode(peerFP)

	// 3. User SAS verification
	localAccepted := invokeConfirm(confirmFn, peerSAS, localSAS)

	// 4. Send local decision
	if err := conn.Send(TypePairDecision, PairDecisionPayload{
		Accept: localAccepted,
	}); err != nil {
		return nil, fmt.Errorf("send pair decision: %w", err)
	}

	// 5. Receive peer's decision
	decEnv, err := receivePairEnvelope(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("receive peer decision: %w", err)
	}
	if decEnv.Type != TypePairDecision {
		return nil, fmt.Errorf("unexpected message type %q, expected %q", decEnv.Type, TypePairDecision)
	}

	var peerDec PairDecisionPayload
	if err := decEnv.DecodePayload(&peerDec); err != nil {
		return nil, fmt.Errorf("decode peer decision: %w", err)
	}

	success := localAccepted && peerDec.Accept
	result := &PairResult{
		PeerFingerprint: peerFP,
		PeerSAS:         peerSAS,
		LocalSAS:        localSAS,
		Accepted:        success,
	}

	if success {
		peerName := peerHello.Name
		if peerName == "" {
			peerName = "peer-" + peerSAS
		}
		storedAddr := addressWithPort(conn.RemoteAddr(), peerHello.ListenPort)
		if err := store.AddPeer(Peer{
			Fingerprint: peerFP,
			Name:        peerName,
			Address:     storedAddr,
			CertPEM:     peerHello.CertPEM,
		}); err != nil {
			return nil, fmt.Errorf("save trusted peer: %w", err)
		}
	}

	return result, nil
}

// DialInBandPairing dials the target peer using the legacy-compatible API.
func DialInBandPairing(peerAddr string, store *PeerStore, confirmFn func(peerSAS, localSAS string) bool) (*PairResult, error) {
	return dialInBandPairing(context.Background(), peerAddr, store, confirmFn, PairingOptions{})
}

// DialInBandPairingWithContext is the cancellable form used by HTTP and Hub
// lifecycle paths.
func DialInBandPairingWithContext(ctx context.Context, peerAddr string, store *PeerStore, confirmFn func(peerSAS, localSAS string) bool) (*PairResult, error) {
	return dialInBandPairing(ctx, peerAddr, store, confirmFn, PairingOptions{})
}

// DialInBandPairingWithOptions allows the caller to advertise its actual
// listener port instead of an ephemeral outbound socket port.
func DialInBandPairingWithOptions(ctx context.Context, peerAddr string, store *PeerStore, confirmFn func(peerSAS, localSAS string) bool, options PairingOptions) (*PairResult, error) {
	return dialInBandPairing(ctx, peerAddr, store, confirmFn, options)
}

func dialInBandPairing(parent context.Context, peerAddr string, store *PeerStore, confirmFn func(peerSAS, localSAS string) bool, options PairingOptions) (*PairResult, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	id, err := getOrGenerateIdentity(store)
	if err != nil {
		return nil, err
	}

	tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("load x509 key pair: %w", err)
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{tlsCert},
		ClientAuth:         tls.RequireAnyClientCert,
		InsecureSkipVerify: true,
	}

	hadExplicitPort := false
	host := peerAddr
	port := DefaultPairingPort
	if h, p, err := net.SplitHostPort(peerAddr); err == nil {
		hadExplicitPort = true
		host = h
		port = p
	}

	dialAddr := net.JoinHostPort(host, port)

	// If connecting specifically to legacy/dedicated pairing port 9878, use PairResponder directly
	if port == LegacyPairingPort {
		res, legacyErr := PairResponderWithContextAndOptions(ctx, store, dialAddr, confirmFn, options)
		if legacyErr == nil {
			peers := store.ListPeers()
			for _, p := range peers {
				if p.Fingerprint == res.PeerFingerprint {
					p.Address = net.JoinHostPort(host, DefaultPairingPort)
					_ = store.AddPeer(p)
					break
				}
			}
			return res, nil
		}
		return nil, legacyErr
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var lastErr error
	allowLegacyFallback := true
	tlsDialer := &tls.Dialer{NetDialer: dialer, Config: tlsConfig}
	tlsConn, err := tlsDialer.DialContext(ctx, "tcp", dialAddr)
	if err == nil {
		defer tlsConn.Close()
		framed := NewFramedConn(tlsConn)
		res, inBandErr := PairResponderInBandWithOptions(ctx, framed, store, confirmFn, options)
		if inBandErr == nil {
			return res, nil
		}
		lastErr = inBandErr
		// Once a peer has answered the framed protocol, an arbitrary failure
		// must not replay confirmFn through the legacy protocol. Only an
		// explicit protocol mismatch is safe to classify as compatibility.
		allowLegacyFallback = errors.Is(inBandErr, ErrInBandProtocolUnsupported)
	} else {
		lastErr = err
	}

	if allowLegacyFallback {
		// Fallback to PairResponder on dialAddr only for a peer that did not
		// speak in-band framing (or a connection that never reached the
		// framed exchange).
		if res, legacyErr := PairResponderWithContextAndOptions(ctx, store, dialAddr, confirmFn, options); legacyErr == nil {
			peers := store.ListPeers()
			for _, p := range peers {
				if p.Fingerprint == res.PeerFingerprint {
					p.Address = cleanPeerAddress(dialAddr)
					_ = store.AddPeer(p)
					break
				}
			}
			return res, nil
		}
	}

	// If no explicit port was given and 9877 failed, attempt legacy port 9878 fallback
	if allowLegacyFallback && !hadExplicitPort {
		legacyAddr := net.JoinHostPort(host, LegacyPairingPort)
		res, legacyErr := PairResponderWithContextAndOptions(ctx, store, legacyAddr, confirmFn, options)
		if legacyErr == nil {
			// The legacy listener does not advertise its operational port;
			// retain the historical 9877 default for that compatibility path.
			peers := store.ListPeers()
			for _, p := range peers {
				if p.Fingerprint == res.PeerFingerprint {
					p.Address = net.JoinHostPort(host, DefaultPairingPort)
					_ = store.AddPeer(p)
					break
				}
			}
			return res, nil
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("pairing negotiation failed: %w", lastErr)
	}
	return nil, errors.New("pairing negotiation failed")
}
