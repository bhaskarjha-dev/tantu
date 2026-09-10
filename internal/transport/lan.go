package transport

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// DefaultLANPort is the default TCP port for LAN transport connections.
const DefaultLANPort = "9877"

// LANTransportConfig holds mTLS configuration for LAN transport.
type LANTransportConfig struct {
	Cert                tls.Certificate      // Local identity (cert + private key)
	TrustedFingerprints []string             // SHA-256 fingerprints of trusted peers (lowercase hex)
	IsTrusted           func(fp string) bool // Dynamic peer trust check
	AllowPairing        bool                 // Allow untrusted peers to complete TLS handshake for in-band pairing
}

// LANTransport implements Transport over mutual TLS for LAN connections.
type LANTransport struct {
	config          LANTransportConfig
	serverTLSConfig *tls.Config
	clientTLSConfig *tls.Config
}

var _ Transport = (*LANTransport)(nil)
var _ Listener = (*lanListener)(nil)

// NewLANTransport creates a new LANTransport with the given mTLS configuration.
func NewLANTransport(config LANTransportConfig) (*LANTransport, error) {
	verifyPeer := func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("mTLS verification failed: no peer certificate provided")
		}

		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("mTLS verification failed: parse certificate: %w", err)
		}

		hash := sha256.Sum256(cert.Raw)
		peerFP := hex.EncodeToString(hash[:])

		if config.IsTrusted != nil && config.IsTrusted(peerFP) {
			return nil
		}

		for _, tf := range config.TrustedFingerprints {
			if strings.EqualFold(peerFP, tf) {
				return nil
			}
		}

		if config.AllowPairing {
			// Valid x509 certificate provided; allow connection at transport layer
			// so Dispatcher can route to in-band pairing. Application layer enforces
			// trust for OAuth and QuickDrop transfers.
			return nil
		}

		if len(config.TrustedFingerprints) == 0 && config.IsTrusted == nil {
			return errors.New("mTLS verification failed: no trusted peer fingerprints configured")
		}

		return fmt.Errorf("mTLS verification failed: untrusted peer certificate fingerprint %s", peerFP)
	}

	serverTLS := &tls.Config{
		MinVersion:            tls.VersionTLS12,
		Certificates:          []tls.Certificate{config.Cert},
		ClientAuth:            tls.RequireAnyClientCert,
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: verifyPeer,
	}

	clientTLS := &tls.Config{
		MinVersion:            tls.VersionTLS12,
		Certificates:          []tls.Certificate{config.Cert},
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: verifyPeer,
	}

	return &LANTransport{
		config:          config,
		serverTLSConfig: serverTLS,
		clientTLSConfig: clientTLS,
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
	return &lanListener{listener: tlsListener}, nil
}

// Dial connects to a LAN peer over mutual-TLS.
func (t *LANTransport) Dial(address string) (Conn, error) {
	if address == "" {
		return nil, errors.New("dial address cannot be empty")
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, DefaultLANPort)
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tlsConn, err := tls.DialWithDialer(dialer, "tcp", address, t.clientTLSConfig)
	if err != nil {
		return nil, fmt.Errorf("lan mTLS dial %s: %w", address, err)
	}

	return newTCPConn(tlsConn), nil
}

type lanListener struct {
	listener net.Listener
}

func (l *lanListener) Accept() (Conn, error) {
	nc, err := l.listener.Accept()
	if err != nil {
		return nil, err
	}
	// Complete the TLS handshake to trigger VerifyPeerCertificate
	if tc, ok := nc.(*tls.Conn); ok {
		if err := tc.Handshake(); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("lan mTLS handshake: %w", err)
		}
	}
	return newTCPConn(nc), nil
}

func (l *lanListener) Close() error {
	if l.listener == nil {
		return nil
	}
	return l.listener.Close()
}

func (l *lanListener) Addr() net.Addr {
	return l.listener.Addr()
}
