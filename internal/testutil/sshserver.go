package testutil

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// GeneratePrivateKeyPEM generates an ephemeral RSA-2048 private key in PEM format.
func GeneratePrivateKeyPEM() ([]byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate rsa key: %w", err)
	}
	keyBytes := x509.MarshalPKCS1PrivateKey(key)
	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: keyBytes,
	}
	return pem.EncodeToMemory(pemBlock), nil
}

// MockSSHServer provides an in-memory SSH server for testing.
// It generates ephemeral RSA keypairs and accepts "tantu" channel requests.
type MockSSHServer struct {
	Addr      string // Actual listen address after Start()
	HostKey   []byte // PEM-encoded host private key
	ClientKey []byte // PEM-encoded client private key (authorized)

	listener     net.Listener
	serverConfig *ssh.ServerConfig
	channels     chan ssh.Channel
	closed       chan struct{}
	closeOnce    sync.Once
	mu           sync.Mutex
	serverConns  map[*ssh.ServerConn]struct{}
}

// NewMockSSHServer creates a new mock SSH server with freshly generated keys.
func NewMockSSHServer() (*MockSSHServer, error) {
	hostKeyPEM, err := GeneratePrivateKeyPEM()
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}

	clientKeyPEM, err := GeneratePrivateKeyPEM()
	if err != nil {
		return nil, fmt.Errorf("generate client key: %w", err)
	}

	hostSigner, err := ssh.ParsePrivateKey(hostKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}

	clientSigner, err := ssh.ParsePrivateKey(clientKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse client key: %w", err)
	}

	clientPubKey := clientSigner.PublicKey()

	serverConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), clientPubKey.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unauthorized public key for user %q", conn.User())
		},
	}
	serverConfig.AddHostKey(hostSigner)

	return &MockSSHServer{
		HostKey:      hostKeyPEM,
		ClientKey:    clientKeyPEM,
		serverConfig: serverConfig,
		channels:     make(chan ssh.Channel, 16),
		closed:       make(chan struct{}),
		serverConns:  make(map[*ssh.ServerConn]struct{}),
	}, nil
}

// Start binds to a dynamic port on 127.0.0.1 and starts accepting connections.
func (s *MockSSHServer) Start() error {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("mock ssh server listen: %w", err)
	}
	s.listener = l
	s.Addr = l.Addr().String()
	go s.serve()
	return nil
}

func (s *MockSSHServer) serve() {
	for {
		nc, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
				return
			}
		}

		go s.handleTCPConn(nc)
	}
}

func (s *MockSSHServer) handleTCPConn(nc net.Conn) {
	sConn, chans, reqs, err := ssh.NewServerConn(nc, s.serverConfig)
	if err != nil {
		_ = nc.Close()
		return
	}

	s.mu.Lock()
	select {
	case <-s.closed:
		s.mu.Unlock()
		_ = sConn.Close()
		return
	default:
		s.serverConns[sConn] = struct{}{}
		s.mu.Unlock()
	}

	defer func() {
		s.mu.Lock()
		delete(s.serverConns, sConn)
		s.mu.Unlock()
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

		select {
		case s.channels <- ch:
		case <-s.closed:
			_ = ch.Close()
			return
		}
	}
}

// AcceptChannel waits for and returns the next accepted "tantu" channel.
func (s *MockSSHServer) AcceptChannel() (ssh.Channel, error) {
	select {
	case ch := <-s.channels:
		return ch, nil
	default:
	}

	select {
	case <-s.closed:
		return nil, errors.New("mock ssh server closed")
	case ch := <-s.channels:
		return ch, nil
	}
}

// Close gracefully stops the mock SSH server and closes all active connections.
func (s *MockSSHServer) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.closed)
		if s.listener != nil {
			err = s.listener.Close()
		}
		s.mu.Lock()
		for sc := range s.serverConns {
			_ = sc.Close()
		}
		s.serverConns = nil
		s.mu.Unlock()
	})
	return err
}
