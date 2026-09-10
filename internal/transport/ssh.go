package transport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

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
	User            string               // SSH username
	Host            string               // SSH host (for Dial)
	Port            int                  // SSH port (default 22)
	PrivateKey      []byte               // PEM-encoded private key (client auth)
	HostKey         []byte               // PEM-encoded host key (for Listen/server mode)
	AuthorizedKeys  [][]byte             // Optional authorized client public keys for server mode
	HostKeyCallback ssh.HostKeyCallback // Optional custom host key verification callback for Dial
}

// SSHTransport implements Transport over SSH channels.
// Listen() starts an SSH server that accepts connections.
// Dial() connects as an SSH client and opens an "tantu" channel.
type SSHTransport struct {
	config       SSHTransportConfig
	clientSigner ssh.Signer
	hostSigner   ssh.Signer
}

// NewSSHTransport creates a new SSHTransport with the provided configuration.
// It validates and parses the configured private key or host key.
func NewSSHTransport(config SSHTransportConfig) (*SSHTransport, error) {
	if len(config.PrivateKey) == 0 && len(config.HostKey) == 0 {
		return nil, errors.New("missing private key: either PrivateKey or HostKey must be provided")
	}

	t := &SSHTransport{
		config: config,
	}

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

	serverConfig := &ssh.ServerConfig{
		NoClientAuth: true,
	}
	serverConfig.AddHostKey(hostSigner)

	if len(t.config.AuthorizedKeys) > 0 {
		serverConfig.NoClientAuth = false
		serverConfig.PublicKeyCallback = func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			keyBytes := key.Marshal()
			for _, ak := range t.config.AuthorizedKeys {
				if parsedKey, _, _, _, err := ssh.ParseAuthorizedKey(ak); err == nil {
					if bytes.Equal(parsedKey.Marshal(), keyBytes) {
						return nil, nil
					}
				} else if bytes.Equal(ak, keyBytes) {
					return nil, nil
				}
			}
			return nil, fmt.Errorf("unauthorized key for user %q", conn.User())
		}
	} else {
		// When no authorized keys are restricted, allow public keys without restriction
		serverConfig.PublicKeyCallback = func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, nil
		}
	}

	listener := &sshListener{
		listener:     l,
		serverConfig: serverConfig,
		results:      make(chan acceptResult, 16),
		closed:       make(chan struct{}),
		serverConns:  make(map[*ssh.ServerConn]struct{}),
	}
	go listener.serve()

	return listener, nil
}

// Dial connects to an SSH server and opens an "tantu" channel.
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

	hkCallback := t.config.HostKeyCallback
	if hkCallback == nil {
		hkCallback = ssh.InsecureIgnoreHostKey()
	}

	clientConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: hkCallback,
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", targetAddr, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", targetAddr, err)
	}

	channel, reqs, err := client.OpenChannel("tantu", nil)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ssh open channel tantu: %w", err)
	}
	go ssh.DiscardRequests(reqs)

	conn := newSSHConn(channel, client.LocalAddr(), client.RemoteAddr(), func() error {
		return client.Close()
	})
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
	closed       chan struct{}
	mu           sync.Mutex
	serverConns  map[*ssh.ServerConn]struct{}
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

		go l.handleTCPConn(nc)
	}
}

func (l *sshListener) handleTCPConn(nc net.Conn) {
	sConn, chans, reqs, err := ssh.NewServerConn(nc, l.serverConfig)
	if err != nil {
		_ = nc.Close()
		select {
		case l.results <- acceptResult{err: fmt.Errorf("ssh handshake failed: %w", err)}:
		case <-l.closed:
		}
		return
	}

	l.mu.Lock()
	select {
	case <-l.closed:
		l.mu.Unlock()
		_ = sConn.Close()
		return
	default:
		l.serverConns[sConn] = struct{}{}
		l.mu.Unlock()
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

		conn := newSSHConn(ch, sConn.LocalAddr(), sConn.RemoteAddr(), nil)
		select {
		case l.results <- acceptResult{conn: conn}:
		case <-l.closed:
			_ = ch.Close()
			return
		}
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
	var err error
	l.closeOnce.Do(func() {
		close(l.closed)
		err = l.listener.Close()
		l.mu.Lock()
		for sc := range l.serverConns {
			_ = sc.Close()
		}
		l.serverConns = nil
		l.mu.Unlock()
	})
	return err
}

func (l *sshListener) Addr() net.Addr {
	return l.listener.Addr()
}

type sshConn struct {
	channel    ssh.Channel
	encoder    *protocol.Encoder
	decoder    *protocol.Decoder
	localAddr  net.Addr
	remoteAddr net.Addr
	onClose    func() error
	mu         sync.Mutex
	closed     bool
}

func newSSHConn(channel ssh.Channel, localAddr, remoteAddr net.Addr, onClose func() error) *sshConn {
	if localAddr == nil {
		localAddr = sshAddr{network: "ssh", str: "127.0.0.1:0"}
	}
	if remoteAddr == nil {
		remoteAddr = sshAddr{network: "ssh", str: "127.0.0.1:0"}
	}
	return &sshConn{
		channel:    channel,
		encoder:    protocol.NewEncoder(channel),
		decoder:    protocol.NewDecoder(channel),
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		onClose:    onClose,
	}
}

func (c *sshConn) Send(msgType string, payload any) error {
	c.mu.Lock()
	closed := c.closed || c.channel == nil
	c.mu.Unlock()
	if closed {
		return errors.New("connection is closed")
	}
	return c.encoder.Encode(msgType, payload)
}

func (c *sshConn) Receive() (*protocol.Envelope, error) {
	c.mu.Lock()
	closed := c.closed || c.channel == nil
	c.mu.Unlock()
	if closed {
		return nil, errors.New("connection is closed")
	}
	return c.decoder.Decode()
}

func (c *sshConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

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

type sshAddr struct {
	network string
	str     string
}

func (a sshAddr) Network() string { return a.network }
func (a sshAddr) String() string  { return a.str }
