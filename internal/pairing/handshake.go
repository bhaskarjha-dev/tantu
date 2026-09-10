package pairing

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

// In-band pairing protocol message types.
const (
	TypePairHello    = "pair_hello"
	TypePairDecision = "pair_decision"
	LegacyPairingPort = "9878"
)

// Conn represents a bidirectional framed connection for protocol exchange.
type Conn interface {
	Send(msgType string, payload any) error
	Receive() (*protocol.Envelope, error)
	Close() error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

// PairHelloPayload represents the payload of a pair_hello protocol message.
type PairHelloPayload struct {
	CertPEM []byte `json:"cert_pem"`
	Name    string `json:"name,omitempty"`
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
	return &FramedConn{
		conn:    nc,
		encoder: protocol.NewEncoder(nc),
		decoder: protocol.NewDecoder(nc),
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

// HandleInboundPairing processes an incoming in-band pairing handshake over a Conn.
// If firstEnv is nil, it reads the initial envelope from conn.
func HandleInboundPairing(ctx context.Context, conn Conn, firstEnv *protocol.Envelope, store *PeerStore, confirmFn func(peerSAS, localSAS string) bool) (*PairResult, error) {
	if firstEnv == nil {
		env, err := conn.Receive()
		if err != nil {
			return nil, fmt.Errorf("receive pair hello: %w", err)
		}
		firstEnv = env
	}
	if firstEnv.Type != TypePairHello {
		return nil, fmt.Errorf("unexpected message type %q, expected %q", firstEnv.Type, TypePairHello)
	}

	var peerHello PairHelloPayload
	if err := firstEnv.DecodePayload(&peerHello); err != nil {
		return nil, fmt.Errorf("decode pair hello: %w", err)
	}

	peerFP, err := Fingerprint(peerHello.CertPEM)
	if err != nil {
		return nil, fmt.Errorf("compute peer fingerprint: %w", err)
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
		CertPEM: id.CertPEM,
		Name:    hostname,
	}); err != nil {
		return nil, fmt.Errorf("send pair hello: %w", err)
	}

	// 2. SAS Confirmation
	localAccepted := confirmFn(peerSAS, localSAS)

	// 3. Send local decision
	if err := conn.Send(TypePairDecision, PairDecisionPayload{
		Accept: localAccepted,
	}); err != nil {
		return nil, fmt.Errorf("send pair decision: %w", err)
	}

	// 4. Receive peer decision
	decEnv, err := conn.Receive()
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
		peerAddr := cleanPeerAddress(conn.RemoteAddr().String())
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
		CertPEM: id.CertPEM,
		Name:    hostname,
	}); err != nil {
		return nil, fmt.Errorf("send responder hello: %w", err)
	}

	// 2. Receive peer's PairHello
	env, err := conn.Receive()
	if err != nil {
		return nil, fmt.Errorf("receive initiator hello: %w", err)
	}
	if env.Type != TypePairHello {
		return nil, fmt.Errorf("unexpected message type %q, expected %q", env.Type, TypePairHello)
	}

	var peerHello PairHelloPayload
	if err := env.DecodePayload(&peerHello); err != nil {
		return nil, fmt.Errorf("decode initiator hello: %w", err)
	}

	peerFP, err := Fingerprint(peerHello.CertPEM)
	if err != nil {
		return nil, fmt.Errorf("compute peer fingerprint: %w", err)
	}

	localSAS := SASCode(id.Fingerprint)
	peerSAS := SASCode(peerFP)

	// 3. User SAS verification
	localAccepted := confirmFn(peerSAS, localSAS)

	// 4. Send local decision
	if err := conn.Send(TypePairDecision, PairDecisionPayload{
		Accept: localAccepted,
	}); err != nil {
		return nil, fmt.Errorf("send pair decision: %w", err)
	}

	// 5. Receive peer's decision
	decEnv, err := conn.Receive()
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
		storedAddr := cleanPeerAddress(conn.RemoteAddr().String())
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

// DialInBandPairing dials the target peer. It attempts in-band pairing on port 9877 first.
// If dialing port 9877 fails and no explicit port was specified, it falls back to legacy port 9878.
func DialInBandPairing(peerAddr string, store *PeerStore, confirmFn func(peerSAS, localSAS string) bool) (*PairResult, error) {
	id, err := getOrGenerateIdentity(store)
	if err != nil {
		return nil, err
	}

	tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("load x509 key pair: %w", err)
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		Certificates:       []tls.Certificate{tlsCert},
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
		res, legacyErr := PairResponder(store, dialAddr, confirmFn)
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
	tlsConn, err := tls.DialWithDialer(dialer, "tcp", dialAddr, tlsConfig)
	if err == nil {
		defer tlsConn.Close()
		framed := NewFramedConn(tlsConn)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		res, inBandErr := PairResponderInBand(ctx, framed, store, confirmFn)
		if inBandErr == nil {
			return res, nil
		}
		lastErr = inBandErr
	} else {
		lastErr = err
	}

	// Fallback to PairResponder on dialAddr if PairResponderInBand failed
	if res, legacyErr := PairResponder(store, dialAddr, confirmFn); legacyErr == nil {
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

	// If no explicit port was given and 9877 failed, attempt legacy port 9878 fallback
	if !hadExplicitPort {
		legacyAddr := net.JoinHostPort(host, LegacyPairingPort)
		res, legacyErr := PairResponder(store, legacyAddr, confirmFn)
		if legacyErr == nil {
			// Successfully paired via legacy port, but strictly normalize stored address to 9877
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
