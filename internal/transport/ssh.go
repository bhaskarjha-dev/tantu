package transport

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

// Compile-time interface assertions.
var (
	_ Transport = (*SSHTransport)(nil)
	_ Listener  = (*sshListener)(nil)
	_ Conn      = (*sshConn)(nil)
)

// SSHTransportConfig holds SSH connection and authentication parameters.
type SSHTransportConfig struct {
	User                   string              // SSH username
	Host                   string              // SSH host (for Dial)
	Port                   int                 // SSH port (default 22)
	PrivateKey             []byte              // PEM-encoded private key (client auth)
	HostKey                []byte              // PEM-encoded host key (for Listen/server mode)
	AuthorizedKeys         [][]byte            // Optional authorized client public keys for server mode
	HostKeyCallback        ssh.HostKeyCallback // Optional custom host key verification callback for Dial
	KnownHostsFile         string              // OpenSSH known_hosts file used when no callback is supplied
	HostKeyFingerprint     string              // Optional SHA256 fingerprint (for example SHA256:...) pin
	AllowInsecureHostKey   bool                // Explicit compatibility escape hatch; never use on untrusted networks
	AllowLegacyHostKeyAuth bool                // Server compatibility: authorize the host key when no AuthorizedKeys are supplied
	AuthorizedUsers        []string            // Optional allow-list of SSH usernames for server mode

	// HandshakeTimeout bounds TCP/SSH negotiation and authentication.
	// Values <= 0 use 10 seconds.
	HandshakeTimeout time.Duration
	// MaxConcurrentHandshakes bounds pre-authentication SSH goroutines.
	// Values <= 0 use 64.
	MaxConcurrentHandshakes int
	// ReadTimeout and WriteTimeout close an SSH channel when one Receive or Send
	// exceeds the timeout. Values <= 0 disable application I/O timeouts.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	// OnHandshakeError observes rejected candidate connections. It may be called
	// concurrently and must return quickly.
	OnHandshakeError func(error)
	// ReturnHandshakeError makes Listener.Accept return authentication and
	// protocol handshake failures. Leave false for multiplexed listeners.
	ReturnHandshakeError bool
}

// SSHTransport implements Transport over SSH channels.
// Listen() starts an SSH server that accepts connections.
// Dial() connects as an SSH client and opens a "tantu" channel.
type SSHTransport struct {
	config                  SSHTransportConfig
	clientSigner            ssh.Signer
	hostSigner              ssh.Signer
	authorizedKeys          map[string]struct{}
	handshakeTimeout        time.Duration
	maxConcurrentHandshakes int
	readTimeout             time.Duration
	writeTimeout            time.Duration
	returnHandshakeError    bool
	onHandshakeError        func(error)
}

// validateHostKeyFingerprint enforces the full pin format: SHA256 scheme
// plus base64 of exactly 32 bytes (a SHA-256 digest). A scheme-only check
// would accept "SHA256:garbage" at construction and fail only at Dial.
func validateHostKeyFingerprint(fingerprint string) error {
	body, ok := strings.CutPrefix(strings.TrimSpace(fingerprint), "SHA256:")
	if !ok || body == "" {
		return errors.New("SSH host key fingerprint must use SHA256:... format")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(body, "="))
	if err != nil {
		return fmt.Errorf("SSH host key fingerprint is not valid base64: %w", err)
	}
	if len(raw) != 32 {
		return fmt.Errorf("SSH host key fingerprint must decode to 32 bytes, got %d", len(raw))
	}
	return nil
}

func (t *SSHTransport) dialHostKeyCallback() (ssh.HostKeyCallback, error) {
	if t.config.HostKeyCallback != nil {
		return t.config.HostKeyCallback, nil
	}
	if t.config.AllowInsecureHostKey {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	if fingerprint := strings.TrimSpace(t.config.HostKeyFingerprint); fingerprint != "" {
		if err := validateHostKeyFingerprint(fingerprint); err != nil {
			return nil, err
		}
		return func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got := ssh.FingerprintSHA256(key)
			if subtle.ConstantTimeCompare([]byte(got), []byte(fingerprint)) != 1 {
				return fmt.Errorf("SSH host key fingerprint mismatch: got %s", got)
			}
			return nil
		}, nil
	}
	knownHostsFile := strings.TrimSpace(t.config.KnownHostsFile)
	if knownHostsFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("SSH host-key verification requires --ssh-known-hosts or a home directory: %w", err)
		}
		knownHostsFile = filepath.Join(home, ".ssh", "known_hosts")
	}
	callback, err := knownhosts.New(knownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("load SSH known_hosts %q: %w", knownHostsFile, err)
	}
	return callback, nil
}

// NewSSHTransport creates a new SSHTransport with the provided configuration.
// It validates and parses the configured private key or host key.
func NewSSHTransport(config SSHTransportConfig) (*SSHTransport, error) {
	if len(config.PrivateKey) == 0 && len(config.HostKey) == 0 {
		return nil, errors.New("missing private key: either PrivateKey or HostKey must be provided")
	}
	// Fail fast on a malformed pin: dialHostKeyCallback would otherwise
	// surface it only at first Dial.
	if fp := strings.TrimSpace(config.HostKeyFingerprint); fp != "" {
		if err := validateHostKeyFingerprint(fp); err != nil {
			return nil, err
		}
	}

	t := &SSHTransport{config: config}

	if len(config.PrivateKey) > 0 {
		signer, err := ssh.ParsePrivateKey(config.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("invalid private key: %w", err)
		}
		t.clientSigner = signer
	}

	if len(config.HostKey) > 0 {
		signer, err := ssh.ParsePrivateKey(config.HostKey)
		if err != nil {
			return nil, fmt.Errorf("invalid host key: %w", err)
		}
		t.hostSigner = signer
	}

	t.authorizedKeys = make(map[string]struct{}, len(config.AuthorizedKeys))
	for _, authorizedKey := range config.AuthorizedKeys {
		if parsed, _, _, _, err := ssh.ParseAuthorizedKey(authorizedKey); err == nil {
			t.authorizedKeys[string(parsed.Marshal())] = struct{}{}
			continue
		}
		// Accept a private-key PEM as a convenience for symmetric CLI modes;
		// the server only retains its public portion in the allow-list.
		if signer, err := ssh.ParsePrivateKey(authorizedKey); err == nil {
			t.authorizedKeys[string(signer.PublicKey().Marshal())] = struct{}{}
			continue
		}
		// Preserve support for callers supplying an already-marshaled SSH
		// public-key blob. Callback comparison is against key.Marshal().
		t.authorizedKeys[string(append([]byte(nil), authorizedKey...))] = struct{}{}
	}

	// Server mode must normally have an independent client credential. The
	// legacy host-key bootstrap is retained only behind an explicit opt-in for
	// compatibility with older single-key deployments.
	if len(t.authorizedKeys) == 0 && len(config.HostKey) > 0 {
		if !config.AllowLegacyHostKeyAuth {
			return nil, errors.New("ssh server requires AuthorizedKeys (or explicit AllowLegacyHostKeyAuth)")
		}
		hostSigner := t.hostSigner
		if hostSigner == nil {
			hostSigner = t.clientSigner
		}
		if hostSigner != nil {
			t.authorizedKeys[string(hostSigner.PublicKey().Marshal())] = struct{}{}
		}
	}
	if len(t.authorizedKeys) == 0 && len(config.HostKey) > 0 {
		return nil, errors.New("ssh server authentication requires AuthorizedKeys")
	}

	t.handshakeTimeout = config.HandshakeTimeout
	if t.handshakeTimeout <= 0 {
		t.handshakeTimeout = defaultHandshakeTimeout
	}
	t.maxConcurrentHandshakes = config.MaxConcurrentHandshakes
	if t.maxConcurrentHandshakes <= 0 {
		t.maxConcurrentHandshakes = 64
	}
	t.readTimeout = config.ReadTimeout
	t.writeTimeout = config.WriteTimeout
	t.returnHandshakeError = config.ReturnHandshakeError
	t.onHandshakeError = config.OnHandshakeError
	return t, nil
}

// Listen creates an SSH listener bound to the given address.
// address is "host:port" or ":port" (or empty for default "127.0.0.1:22").
func (t *SSHTransport) Listen(address string) (Listener, error) {
	hostSigner := t.hostSigner
	if hostSigner == nil {
		hostSigner = t.clientSigner
	}
	if hostSigner == nil {
		return nil, errors.New("ssh transport listener requires HostKey or PrivateKey in config")
	}
	if len(t.authorizedKeys) == 0 {
		return nil, errors.New("ssh transport listener requires at least one authorized client key")
	}

	bindAddr := address
	if bindAddr == "" {
		bindAddr = "127.0.0.1:22"
	} else if !strings.Contains(bindAddr, ":") {
		bindAddr = ":" + bindAddr
	}

	l, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return nil, fmt.Errorf("ssh listen on %s: %w", bindAddr, err)
	}

	allowedUsers := make(map[string]struct{}, len(t.config.AuthorizedUsers))
	for _, user := range t.config.AuthorizedUsers {
		if user = strings.TrimSpace(user); user != "" {
			allowedUsers[user] = struct{}{}
		}
	}
	serverConfig := &ssh.ServerConfig{
		NoClientAuth: false,
		MaxAuthTries: 3,
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if len(allowedUsers) > 0 {
				if _, ok := allowedUsers[conn.User()]; !ok {
					return nil, fmt.Errorf("unauthorized SSH user %q", conn.User())
				}
			}
			if _, ok := t.authorizedKeys[string(key.Marshal())]; ok {
				fingerprint := ssh.FingerprintSHA256(key)
				return &ssh.Permissions{Extensions: map[string]string{"peer_fingerprint": fingerprint}}, nil
			}
			return nil, fmt.Errorf("unauthorized key for user %q", conn.User())
		},
	}
	serverConfig.AddHostKey(hostSigner)

	listener := &sshListener{
		listener:             l,
		serverConfig:         serverConfig,
		results:              make(chan acceptResult, 16),
		closed:               make(chan struct{}),
		pendingConns:         make(map[net.Conn]struct{}),
		serverConns:          make(map[*ssh.ServerConn]struct{}),
		handshakeTimeout:     t.handshakeTimeout,
		handshakeSlots:       make(chan struct{}, t.maxConcurrentHandshakes),
		readTimeout:          t.readTimeout,
		writeTimeout:         t.writeTimeout,
		returnHandshakeError: t.returnHandshakeError,
		onHandshakeError:     t.onHandshakeError,
	}
	go listener.serve()

	return listener, nil
}

// Dial connects to an SSH server and opens a "tantu" channel.
func (t *SSHTransport) Dial(address string) (Conn, error) {
	clientSigner := t.clientSigner
	if clientSigner == nil {
		clientSigner = t.hostSigner
	}
	if clientSigner == nil {
		return nil, errors.New("ssh transport dialer requires PrivateKey in config")
	}

	targetAddr := address
	if targetAddr == "" {
		port := t.config.Port
		if port == 0 {
			port = 22
		}
		host := t.config.Host
		if host == "" {
			host = "127.0.0.1"
		}
		targetAddr = net.JoinHostPort(host, strconv.Itoa(port))
	} else if !strings.Contains(targetAddr, ":") {
		port := t.config.Port
		if port == 0 {
			port = 22
		}
		targetAddr = net.JoinHostPort(targetAddr, strconv.Itoa(port))
	}

	user := t.config.User
	if user == "" {
		user = "tantu"
	}

	hkCallback, err := t.dialHostKeyCallback()
	if err != nil {
		return nil, err
	}
	var hostKeyFingerprint string
	verifiedCallback := hkCallback
	verifiedCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := hkCallback(hostname, remote, key); err != nil {
			return err
		}
		hostKeyFingerprint = ssh.FingerprintSHA256(key)
		return nil
	}

	clientConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: verifiedCallback,
		Timeout:         t.handshakeTimeout,
	}

	// ssh.ClientConfig.Timeout only bounds the underlying TCP dial in some
	// x/crypto versions; it does not reliably bound banner exchange, key
	// exchange, or authentication. Establish the socket ourselves and keep a
	// real deadline across the complete SSH handshake.
	dialer := &net.Dialer{Timeout: t.handshakeTimeout}
	rawConn, err := dialer.Dial("tcp", targetAddr)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", targetAddr, err)
	}
	if err := rawConn.SetDeadline(time.Now().Add(t.handshakeTimeout)); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("ssh handshake deadline: %w", err)
	}
	sshConn, chans, handshakeReqs, err := ssh.NewClientConn(rawConn, targetAddr, clientConfig)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("ssh dial %s: %w", targetAddr, err)
	}
	if err := rawConn.SetDeadline(time.Time{}); err != nil {
		_ = sshConn.Close()
		return nil, fmt.Errorf("clear SSH handshake deadline: %w", err)
	}
	client := ssh.NewClient(sshConn, chans, handshakeReqs)

	type channelResult struct {
		channel  ssh.Channel
		requests <-chan *ssh.Request
		err      error
	}
	channelCh := make(chan channelResult, 1)
	go func() {
		channel, reqs, openErr := client.OpenChannel("tantu", nil)
		channelCh <- channelResult{channel: channel, requests: reqs, err: openErr}
	}()
	var channel ssh.Channel
	var reqs <-chan *ssh.Request
	select {
	case result := <-channelCh:
		channel, reqs, err = result.channel, result.requests, result.err
	case <-time.After(t.handshakeTimeout):
		_ = client.Close()
		return nil, fmt.Errorf("ssh open channel tantu: %w", os.ErrDeadlineExceeded)
	}
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ssh open channel tantu: %w", err)
	}
	go ssh.DiscardRequests(reqs)

	conn := newSSHConnWithTimeouts(
		channel,
		client.LocalAddr(),
		client.RemoteAddr(),
		func() error { return client.Close() },
		t.readTimeout,
		t.writeTimeout,
	)
	conn.peerFingerprint = hostKeyFingerprint
	return conn, nil
}

type acceptResult struct {
	conn Conn
	err  error
}

type sshListener struct {
	listener     net.Listener
	serverConfig *ssh.ServerConfig
	results      chan acceptResult
	closeOnce    sync.Once
	closeErr     error
	closed       chan struct{}
	mu           sync.Mutex
	pendingConns map[net.Conn]struct{}
	serverConns  map[*ssh.ServerConn]struct{}

	handshakeTimeout     time.Duration
	handshakeSlots       chan struct{}
	readTimeout          time.Duration
	writeTimeout         time.Duration
	returnHandshakeError bool
	onHandshakeError     func(error)
}

func (l *sshListener) serve() {
	for {
		nc, err := l.listener.Accept()
		if err != nil {
			select {
			case <-l.closed:
				return
			default:
				select {
				case l.results <- acceptResult{err: err}:
				case <-l.closed:
				}
				return
			}
		}

		if !l.trackPendingConn(nc) {
			_ = nc.Close()
			continue
		}
		select {
		case l.handshakeSlots <- struct{}{}:
		default:
			l.untrackPendingConn(nc)
			_ = nc.Close()
			l.publishHandshakeError(nc, errors.New("maximum concurrent SSH handshakes reached"))
			continue
		}
		go func(nc net.Conn) {
			l.handleTCPConn(nc)
		}(nc)
	}
}

func (l *sshListener) trackPendingConn(nc net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.closed:
		return false
	default:
	}
	if l.pendingConns == nil {
		l.pendingConns = make(map[net.Conn]struct{})
	}
	l.pendingConns[nc] = struct{}{}
	return true
}

func (l *sshListener) untrackPendingConn(nc net.Conn) {
	l.mu.Lock()
	delete(l.pendingConns, nc)
	l.mu.Unlock()
}

func (l *sshListener) promoteServerConn(nc net.Conn, sConn *ssh.ServerConn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.pendingConns, nc)
	select {
	case <-l.closed:
		return false
	default:
	}
	if l.serverConns == nil {
		l.serverConns = make(map[*ssh.ServerConn]struct{})
	}
	l.serverConns[sConn] = struct{}{}
	return true
}

func (l *sshListener) handleTCPConn(nc net.Conn) {
	slotReleased := false
	defer func() {
		if !slotReleased {
			<-l.handshakeSlots
		}
	}()
	defer l.untrackPendingConn(nc)

	if err := nc.SetDeadline(time.Now().Add(l.handshakeTimeout)); err != nil {
		_ = nc.Close()
		l.publishHandshakeError(nc, fmt.Errorf("set SSH handshake deadline: %w", err))
		return
	}
	sConn, chans, reqs, err := ssh.NewServerConn(nc, l.serverConfig)
	if err != nil {
		_ = nc.Close()
		l.publishHandshakeError(nc, err)
		return
	}
	// The semaphore limits pre-authentication work, not the lifetime of an
	// authenticated SSH connection. Release it as soon as authentication and
	// channel negotiation complete.
	slotReleased = true
	<-l.handshakeSlots
	if err := nc.SetDeadline(time.Time{}); err != nil {
		_ = sConn.Close()
		l.publishHandshakeError(nc, fmt.Errorf("clear SSH handshake deadline: %w", err))
		return
	}

	if !l.promoteServerConn(nc, sConn) {
		_ = sConn.Close()
		return
	}

	defer func() {
		l.mu.Lock()
		delete(l.serverConns, sConn)
		l.mu.Unlock()
		_ = sConn.Close()
	}()

	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "tantu" {
			_ = newChan.Reject(ssh.UnknownChannelType, fmt.Sprintf("unsupported channel type: %s", newChan.ChannelType()))
			continue
		}

		ch, requests, err := newChan.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(requests)

		conn := newSSHConnWithTimeouts(ch, sConn.LocalAddr(), sConn.RemoteAddr(), nil, l.readTimeout, l.writeTimeout)
		if sConn.Permissions != nil {
			conn.peerFingerprint = sConn.Permissions.Extensions["peer_fingerprint"]
		}
		select {
		case l.results <- acceptResult{conn: conn}:
		case <-l.closed:
			_ = ch.Close()
			return
		}
	}
}

func (l *sshListener) publishHandshakeError(nc net.Conn, err error) {
	remote := ""
	if nc != nil && nc.RemoteAddr() != nil {
		remote = nc.RemoteAddr().String()
	}
	handshakeErr := &HandshakeError{Transport: "ssh", RemoteAddr: remote, Err: err}
	if l.onHandshakeError != nil {
		l.onHandshakeError(handshakeErr)
	}
	if !l.returnHandshakeError {
		return
	}
	select {
	case l.results <- acceptResult{err: handshakeErr}:
	case <-l.closed:
	}
}

func (l *sshListener) Accept() (Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case res := <-l.results:
		return res.conn, res.err
	}
}

func (l *sshListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		l.closeErr = l.listener.Close()

		l.mu.Lock()
		pending := make([]net.Conn, 0, len(l.pendingConns))
		for nc := range l.pendingConns {
			pending = append(pending, nc)
		}
		servers := make([]*ssh.ServerConn, 0, len(l.serverConns))
		for sConn := range l.serverConns {
			servers = append(servers, sConn)
		}
		l.pendingConns = nil
		l.serverConns = nil
		l.mu.Unlock()

		for _, nc := range pending {
			_ = nc.Close()
		}
		for _, sConn := range servers {
			_ = sConn.Close()
		}
	})
	return l.closeErr
}

func (l *sshListener) Addr() net.Addr {
	return l.listener.Addr()
}

type sshConn struct {
	channel         ssh.Channel
	encoder         *protocol.Encoder
	decoder         *protocol.Decoder
	localAddr       net.Addr
	remoteAddr      net.Addr
	onClose         func() error
	peerFingerprint string

	readTimeout  time.Duration
	writeTimeout time.Duration

	mu         sync.Mutex
	closed     bool
	deadlineMu sync.Mutex
	readTimer  *time.Timer
	writeTimer *time.Timer
}

func newSSHConn(channel ssh.Channel, localAddr, remoteAddr net.Addr, onClose func() error) *sshConn {
	return newSSHConnWithTimeouts(channel, localAddr, remoteAddr, onClose, 0, 0)
}

func newSSHConnWithTimeouts(
	channel ssh.Channel,
	localAddr, remoteAddr net.Addr,
	onClose func() error,
	readTimeout, writeTimeout time.Duration,
) *sshConn {
	if localAddr == nil {
		localAddr = sshAddr{network: "ssh", str: "127.0.0.1:0"}
	}
	if remoteAddr == nil {
		remoteAddr = sshAddr{network: "ssh", str: "127.0.0.1:0"}
	}
	return &sshConn{
		channel:      channel,
		encoder:      protocol.NewEncoder(channel),
		decoder:      protocol.NewDecoder(channel),
		localAddr:    localAddr,
		remoteAddr:   remoteAddr,
		onClose:      onClose,
		readTimeout:  readTimeout,
		writeTimeout: writeTimeout,
	}
}

func (c *sshConn) Send(msgType string, payload any) error {
	c.mu.Lock()
	closed := c.closed || c.channel == nil
	c.mu.Unlock()
	if closed {
		return errors.New("connection is closed")
	}
	timedOut := c.startIOTimeout(c.writeTimeout)
	err := c.encoder.Encode(msgType, payload)
	if timedOut() {
		return fmt.Errorf("ssh write timeout after %s: %w", c.writeTimeout, os.ErrDeadlineExceeded)
	}
	return err
}

func (c *sshConn) Receive() (*protocol.Envelope, error) {
	c.mu.Lock()
	closed := c.closed || c.channel == nil
	c.mu.Unlock()
	if closed {
		return nil, errors.New("connection is closed")
	}
	timedOut := c.startIOTimeout(c.readTimeout)
	env, err := c.decoder.Decode()
	if timedOut() {
		return nil, fmt.Errorf("ssh read timeout after %s: %w", c.readTimeout, os.ErrDeadlineExceeded)
	}
	return env, err
}

func (c *sshConn) startIOTimeout(timeout time.Duration) func() bool {
	if timeout <= 0 {
		return func() bool { return false }
	}
	var fired atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		fired.Store(true)
		_ = c.Close()
	})
	return func() bool {
		timer.Stop()
		return fired.Load()
	}
}

func (c *sshConn) DeadlineSupported() bool { return c != nil && c.channel != nil }

func (c *sshConn) setDeadlineTimer(timer **time.Timer, t time.Time) {
	c.deadlineMu.Lock()
	if *timer != nil {
		(*timer).Stop()
		*timer = nil
	}
	if !t.IsZero() {
		delay := time.Until(t)
		if delay < 0 {
			delay = 0
		}
		*timer = time.AfterFunc(delay, func() { _ = c.Close() })
	}
	c.deadlineMu.Unlock()
}

func (c *sshConn) SetDeadline(t time.Time) error {
	if c == nil || c.channel == nil {
		return errors.New("connection is closed")
	}
	c.setDeadlineTimer(&c.readTimer, t)
	c.setDeadlineTimer(&c.writeTimer, t)
	return nil
}

func (c *sshConn) SetReadDeadline(t time.Time) error {
	if c == nil || c.channel == nil {
		return errors.New("connection is closed")
	}
	c.setDeadlineTimer(&c.readTimer, t)
	return nil
}

func (c *sshConn) SetWriteDeadline(t time.Time) error {
	if c == nil || c.channel == nil {
		return errors.New("connection is closed")
	}
	c.setDeadlineTimer(&c.writeTimer, t)
	return nil
}

func (c *sshConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.deadlineMu.Lock()
	if c.readTimer != nil {
		c.readTimer.Stop()
		c.readTimer = nil
	}
	if c.writeTimer != nil {
		c.writeTimer.Stop()
		c.writeTimer = nil
	}
	c.deadlineMu.Unlock()

	var err error
	if c.channel != nil {
		if cerr := c.channel.Close(); cerr != nil && !errors.Is(cerr, io.EOF) && !errors.Is(cerr, net.ErrClosed) && cerr.Error() != "EOF" {
			err = cerr
		}
	}
	if c.onClose != nil {
		if cerr := c.onClose(); cerr != nil && !errors.Is(cerr, io.EOF) && !errors.Is(cerr, net.ErrClosed) && cerr.Error() != "EOF" && err == nil {
			err = cerr
		}
	}
	return err
}

func (c *sshConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *sshConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

// PeerFingerprint returns the authenticated SSH host/client key fingerprint,
// when the transport supplied one. It is intentionally not a TLS certificate
// fingerprint; callers should treat it as an SSH principal identity.
func (c *sshConn) PeerFingerprint() string {
	return c.peerFingerprint
}

type sshAddr struct {
	network string
	str     string
}

func (a sshAddr) Network() string { return a.network }
func (a sshAddr) String() string  { return a.str }
