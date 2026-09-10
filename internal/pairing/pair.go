package pairing

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// DefaultPairingPort is the default port used during the pairing handshake.
const DefaultPairingPort = "9877"

func cleanPeerAddress(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return net.JoinHostPort(host, DefaultPairingPort)
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
	CertPEM []byte          `json:"cert_pem"`
	Name    string          `json:"name,omitempty"`
	Type    string          `json:"type,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
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
		CertPEM []byte          `json:"cert_pem"`
		Name    string          `json:"name,omitempty"`
		Type    string          `json:"type,omitempty"`
		Payload json.RawMessage `json:"payload,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.CertPEM) > 0 {
		p.CertPEM = raw.CertPEM
		p.Name = raw.Name
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
	if _, err := conn.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("write length: %w", err)
	}
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write body: %w", err)
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

func getOrGenerateIdentity(store *PeerStore) (*Identity, error) {
	id, err := store.LoadIdentity()
	if err != nil {
		return nil, fmt.Errorf("load identity: %w", err)
	}
	if id == nil {
		id, err = GenerateIdentity()
		if err != nil {
			return nil, fmt.Errorf("generate identity: %w", err)
		}
		if err := store.SaveIdentity(id); err != nil {
			return nil, fmt.Errorf("save identity: %w", err)
		}
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
	if err := sendJSON(conn, newPairHello(id.CertPEM, hostname)); err != nil {
		return nil, fmt.Errorf("send pair hello: %w", err)
	}

	// 2. Read Responder's PairHello
	var peerHello pairHello
	if err := readJSON(conn, &peerHello); err != nil {
		return nil, fmt.Errorf("receive responder hello: %w", err)
	}

	peerFP, err := Fingerprint(peerHello.CertPEM)
	if err != nil {
		return nil, fmt.Errorf("compute peer fingerprint: %w", err)
	}

	localSAS := SASCode(id.Fingerprint)
	peerSAS := SASCode(peerFP)

	// 3. User confirmation
	localAccepted := confirmFn(peerSAS, localSAS)

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

// PairResponder connects to an initiator at peerAddr for pairing.
// confirmFn is called with the peer's SAS code and local SAS code — return true to accept, false to reject.
func PairResponder(store *PeerStore, peerAddr string, confirmFn func(peerSAS, localSAS string) bool) (*PairResult, error) {
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
		MinVersion:         tls.VersionTLS12,
		Certificates:       []tls.Certificate{tlsCert},
		InsecureSkipVerify: true,
	}

	conn, err := tls.Dial("tcp", peerAddr, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("connect to pairing initiator: %w", err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "responder"
	}

	// 1. Send PairHello
	if err := sendJSON(conn, newPairHello(id.CertPEM, hostname)); err != nil {
		return nil, fmt.Errorf("send responder hello: %w", err)
	}

	// 2. Read Initiator's PairHello
	var peerHello pairHello
	if err := readJSON(conn, &peerHello); err != nil {
		return nil, fmt.Errorf("receive initiator hello: %w", err)
	}

	peerFP, err := Fingerprint(peerHello.CertPEM)
	if err != nil {
		return nil, fmt.Errorf("compute peer fingerprint: %w", err)
	}

	localSAS := SASCode(id.Fingerprint)
	peerSAS := SASCode(peerFP)

	// 3. User confirmation
	localAccepted := confirmFn(peerSAS, localSAS)

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
