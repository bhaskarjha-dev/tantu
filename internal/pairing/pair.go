package pairing

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// DefaultPairingPort is the default port used during the pairing handshake.
const DefaultPairingPort = "9877"

func addressWithPort(remote net.Addr, port int) string {
	if remote == nil {
		return net.JoinHostPort("127.0.0.1", DefaultPairingPort)
	}
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil || host == "" {
		host = remote.String()
	}
	if port <= 0 || port > 65535 {
		port = 9877
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}

func cleanPeerAddress(addr string) string {
	if host, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		// Preserve an explicitly advertised/custom wire port.  Replacing it
		// with 9877 made pairing appear successful but left the peer
		// unreachable on non-default listeners.
		return net.JoinHostPort(host, port)
	}
	return net.JoinHostPort(addr, DefaultPairingPort)
}

// PairResult contains the outcome of a pairing attempt.
type PairResult struct {
	PeerFingerprint string `json:"peer_fingerprint"`
	PeerSAS         string `json:"peer_sas"`
	LocalSAS        string `json:"local_sas"`
	Accepted        bool   `json:"accepted"`
}

type pairHello struct {
	CertPEM    []byte          `json:"cert_pem"`
	Name       string          `json:"name,omitempty"`
	ListenPort int             `json:"listen_port,omitempty"`
	Type       string          `json:"type,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

func newPairHello(certPEM []byte, name string) pairHello {
	inner, _ := json.Marshal(map[string]any{
		"cert_pem": certPEM,
		"name":     name,
	})
	return pairHello{
		CertPEM: certPEM,
		Name:    name,
		Type:    "pair_hello",
		Payload: inner,
	}
}

func (p *pairHello) UnmarshalJSON(data []byte) error {
	var raw struct {
		CertPEM    []byte          `json:"cert_pem"`
		Name       string          `json:"name,omitempty"`
		ListenPort int             `json:"listen_port,omitempty"`
		Type       string          `json:"type,omitempty"`
		Payload    json.RawMessage `json:"payload,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.CertPEM) > 0 {
		p.CertPEM = raw.CertPEM
		p.Name = raw.Name
		p.ListenPort = raw.ListenPort
		p.Type = raw.Type
		p.Payload = raw.Payload
		return nil
	}
	if len(raw.Payload) > 0 {
		var inner struct {
			CertPEM []byte `json:"cert_pem"`
			Name    string `json:"name,omitempty"`
		}
		if err := json.Unmarshal(raw.Payload, &inner); err == nil && len(inner.CertPEM) > 0 {
			p.CertPEM = inner.CertPEM
			p.Name = inner.Name
			return nil
		}
	}
	return errors.New("missing cert_pem in pair hello")
}

type pairDecision struct {
	Accept  bool            `json:"accept"`
	Reason  string          `json:"reason,omitempty"`
	Type    string          `json:"type,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func newPairDecision(accept bool, reason string) pairDecision {
	inner, _ := json.Marshal(map[string]any{
		"accept": accept,
		"reason": reason,
	})
	return pairDecision{
		Accept:  accept,
		Reason:  reason,
		Type:    "pair_decision",
		Payload: inner,
	}
}

func (d *pairDecision) UnmarshalJSON(data []byte) error {
	var raw struct {
		Accept  bool            `json:"accept"`
		Reason  string          `json:"reason,omitempty"`
		Type    string          `json:"type,omitempty"`
		Payload json.RawMessage `json:"payload,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	d.Accept = raw.Accept
	d.Reason = raw.Reason
	d.Type = raw.Type
	d.Payload = raw.Payload
	if len(raw.Payload) > 0 {
		var inner struct {
			Accept bool   `json:"accept"`
			Reason string `json:"reason,omitempty"`
		}
		if err := json.Unmarshal(raw.Payload, &inner); err == nil {
			d.Accept = inner.Accept
			d.Reason = inner.Reason
		}
	}
	return nil
}

func sendJSON(conn net.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if err := writeAll(conn, lenBuf[:]); err != nil {
		return fmt.Errorf("write length: %w", err)
	}
	if err := writeAll(conn, data); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func verifyTLSClaim(conn net.Conn, claimedCertPEM []byte) error {
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return nil
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return errors.New("pairing peer did not present a TLS certificate")
	}
	claimedFP, err := Fingerprint(claimedCertPEM)
	if err != nil {
		return err
	}
	actual := sha256.Sum256(state.PeerCertificates[0].Raw)
	if !strings.EqualFold(claimedFP, hex.EncodeToString(actual[:])) {
		return errors.New("pairing certificate does not match the authenticated TLS peer")
	}
	return nil
}

const maxPairMessageSize = 64 * 1024 // 64KB — pairing messages are small JSON

func readJSON(conn net.Conn, dest any) error {
	var lenBuf [4]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return fmt.Errorf("read length: %w", err)
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
	if length > maxPairMessageSize {
		return fmt.Errorf("pairing message too large: %d bytes (max %d)", length, maxPairMessageSize)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if err := json.Unmarshal(buf, dest); err != nil {
		return fmt.Errorf("unmarshal json: %w", err)
	}
	return nil
}

func invokeConfirm(confirmFn func(string, string) bool, peerSAS, localSAS string) bool {
	if confirmFn == nil {
		return false
	}
	return confirmFn(peerSAS, localSAS)
}

// confirmWithContext runs the blocking human SAS confirmation while honoring
// cancellation: checking ctx.Err() only after confirm returns would hang an
// unattended prompt forever. On expiry the connection is closed by the caller
// (deferred Close); the buffered channel lets a late answer drain instead of
// leaking the goroutine.
func confirmWithContext(ctx context.Context, confirmFn func(string, string) bool, peerSAS, localSAS string) (bool, error) {
	decisionCh := make(chan bool, 1)
	go func() { decisionCh <- invokeConfirm(confirmFn, peerSAS, localSAS) }()
	select {
	case accepted := <-decisionCh:
		return accepted, ctx.Err()
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func getOrGenerateIdentity(store *PeerStore) (*Identity, error) {
	if store == nil {
		return nil, errors.New("peer store is nil")
	}
	id, err := store.LoadOrCreateIdentity()
	if err != nil {
		return nil, fmt.Errorf("load or create identity: %w", err)
	}
	return id, nil
}

// PairInitiator listens for an incoming pairing connection on listenAddr.
// It returns the PairResult after certificate exchange.
// confirmFn is called with the peer's SAS code and local SAS code — return true to accept, false to reject.
func PairInitiator(store *PeerStore, listenAddr string, confirmFn func(peerSAS, localSAS string) bool) (*PairResult, error) {
	if listenAddr == "" {
		listenAddr = net.JoinHostPort("0.0.0.0", DefaultPairingPort)
	} else if _, _, err := net.SplitHostPort(listenAddr); err != nil {
		listenAddr = net.JoinHostPort(listenAddr, DefaultPairingPort)
	}

	l, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("pairing listen: %w", err)
	}
	defer l.Close()

	return PairInitiatorWithListener(store, l, confirmFn)
}

// PairInitiatorWithListener runs the pairing initiator using an existing net.Listener.
func PairInitiatorWithListener(store *PeerStore, l net.Listener, confirmFn func(peerSAS, localSAS string) bool) (*PairResult, error) {
	// Bound the whole pairing attempt, including the human SAS comparison
	// during which the network deadline is cleared. Without this an
	// unattended prompt (or an attacker holding the connection) hangs the
	// initiator forever; the responder side is already bounded by its own
	// 2-minute context.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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
	tlsListener := tls.NewListener(l, tlsConfig)

	// Wait for responder connection with 2-minute timeout
	type acceptRes struct {
		conn net.Conn
		err  error
	}
	acceptChan := make(chan acceptRes, 1)
	go func() {
		c, err := tlsListener.Accept()
		acceptChan <- acceptRes{conn: c, err: err}
	}()

	var conn net.Conn
	select {
	case res := <-acceptChan:
		if res.err != nil {
			return nil, fmt.Errorf("pairing accept: %w", res.err)
		}
		conn = res.conn
	case <-ctx.Done():
		return nil, fmt.Errorf("pairing initiator cancelled while waiting for responder: %w", ctx.Err())
	case <-time.After(2 * time.Minute):
		return nil, errors.New("pairing initiator timed out waiting for responder")
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "initiator"
	}

	// 1. Send PairHello
	hello := newPairHello(id.CertPEM, hostname)
	hello.ListenPort = localListenPort(l.Addr())
	if err := sendJSON(conn, hello); err != nil {
		return nil, fmt.Errorf("send pair hello: %w", err)
	}

	// 2. Read Responder's PairHello
	var peerHello pairHello
	if err := readJSON(conn, &peerHello); err != nil {
		return nil, fmt.Errorf("receive responder hello: %w", err)
	}
	if err := validatePairHelloFields(peerHello.CertPEM, peerHello.Name, peerHello.ListenPort); err != nil {
		return nil, err
	}

	peerFP, err := Fingerprint(peerHello.CertPEM)
	if err != nil {
		return nil, fmt.Errorf("compute peer fingerprint: %w", err)
	}
	if err := verifyTLSClaim(conn, peerHello.CertPEM); err != nil {
		return nil, err
	}

	localSAS := SASCode(id.Fingerprint)
	peerSAS := SASCode(peerFP)

	// 3. User confirmation. Do not keep the network deadline running while a
	// human compares SAS values; re-arm it for the decision exchange below.
	_ = conn.SetDeadline(time.Time{})
	localAccepted, err := confirmWithContext(ctx, confirmFn, peerSAS, localSAS)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Send local decision
	if err := sendJSON(conn, newPairDecision(localAccepted, "")); err != nil {
		return nil, fmt.Errorf("send pair decision: %w", err)
	}

	// Read peer decision
	var peerDec pairDecision
	if err := readJSON(conn, &peerDec); err != nil {
		return nil, fmt.Errorf("receive peer decision: %w", err)
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

// PairResponder connects to an initiator at peerAddr for pairing.
// confirmFn is called with the peer's SAS code and local SAS code — return true to accept, false to reject.
func PairResponder(store *PeerStore, peerAddr string, confirmFn func(peerSAS, localSAS string) bool) (*PairResult, error) {
	return PairResponderWithContext(context.Background(), store, peerAddr, confirmFn)
}

// PairResponderWithContext is the cancellable legacy pairing form.
func PairResponderWithContext(parent context.Context, store *PeerStore, peerAddr string, confirmFn func(peerSAS, localSAS string) bool) (*PairResult, error) {
	return pairResponderWithContext(parent, store, peerAddr, confirmFn, PairingOptions{})
}

// PairResponderWithContextAndOptions carries the actual listener port for a
// legacy compatibility exchange.
func PairResponderWithContextAndOptions(parent context.Context, store *PeerStore, peerAddr string, confirmFn func(peerSAS, localSAS string) bool, options PairingOptions) (*PairResult, error) {
	return pairResponderWithContext(parent, store, peerAddr, confirmFn, options)
}

func pairResponderWithContext(parent context.Context, store *PeerStore, peerAddr string, confirmFn func(peerSAS, localSAS string) bool, options PairingOptions) (*PairResult, error) {
	if parent == nil {
		parent = context.Background()
	}
	id, err := getOrGenerateIdentity(store)
	if err != nil {
		return nil, err
	}

	tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("load x509 key pair: %w", err)
	}

	if _, _, err := net.SplitHostPort(peerAddr); err != nil {
		peerAddr = net.JoinHostPort(peerAddr, DefaultPairingPort)
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{tlsCert},
		ClientAuth:         tls.RequireAnyClientCert,
		InsecureSkipVerify: true,
	}

	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	rawConn, err := dialer.DialContext(ctx, "tcp", peerAddr)
	if err != nil {
		return nil, fmt.Errorf("connect to pairing initiator: %w", err)
	}
	conn := tls.Client(rawConn, tlsConfig)
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return nil, fmt.Errorf("set pairing deadline: %w", err)
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("pairing TLS handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "responder"
	}

	// 1. Send PairHello
	hello := newPairHello(id.CertPEM, hostname)
	hello.ListenPort = options.listenPort()
	if err := sendJSON(conn, hello); err != nil {
		return nil, fmt.Errorf("send responder hello: %w", err)
	}

	// 2. Read Initiator's PairHello
	var peerHello pairHello
	if err := readJSON(conn, &peerHello); err != nil {
		return nil, fmt.Errorf("receive initiator hello: %w", err)
	}
	if err := validatePairHelloFields(peerHello.CertPEM, peerHello.Name, peerHello.ListenPort); err != nil {
		return nil, err
	}

	peerFP, err := Fingerprint(peerHello.CertPEM)
	if err != nil {
		return nil, fmt.Errorf("compute peer fingerprint: %w", err)
	}
	if err := verifyTLSClaim(conn, peerHello.CertPEM); err != nil {
		return nil, err
	}

	localSAS := SASCode(id.Fingerprint)
	peerSAS := SASCode(peerFP)

	// 3. User confirmation. Do not keep the network deadline running while a
	// human compares SAS values; re-arm it for the decision exchange below.
	_ = conn.SetDeadline(time.Time{})
	localAccepted, err := confirmWithContext(ctx, confirmFn, peerSAS, localSAS)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Send local decision
	if err := sendJSON(conn, newPairDecision(localAccepted, "")); err != nil {
		return nil, fmt.Errorf("send pair decision: %w", err)
	}

	// Read peer decision
	var peerDec pairDecision
	if err := readJSON(conn, &peerDec); err != nil {
		return nil, fmt.Errorf("receive peer decision: %w", err)
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
		storedAddr := cleanPeerAddress(peerAddr)
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
