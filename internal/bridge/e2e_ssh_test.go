package bridge_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/testutil"
	"github.com/bhaskarjha-dev/tantu/internal/testutil/oauthclient"
	"github.com/bhaskarjha-dev/tantu/internal/testutil/oauthserver"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

var _ transport.Conn = (*sshChannelConn)(nil)

type sshChannelConn struct {
	channel    ssh.Channel
	enc        *protocol.Encoder
	dec        *protocol.Decoder
	localAddr  net.Addr
	remoteAddr net.Addr
}

func newSSHChannelConn(channel ssh.Channel, localAddr, remoteAddr net.Addr) *sshChannelConn {
	if localAddr == nil {
		localAddr = dummyAddr("127.0.0.1:0")
	}
	if remoteAddr == nil {
		remoteAddr = dummyAddr("127.0.0.1:0")
	}
	return &sshChannelConn{
		channel:    channel,
		enc:        protocol.NewEncoder(channel),
		dec:        protocol.NewDecoder(channel),
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
	}
}

func (c *sshChannelConn) Send(msgType string, payload any) error {
	return c.enc.Encode(msgType, payload)
}

func (c *sshChannelConn) Receive() (*protocol.Envelope, error) {
	return c.dec.Decode()
}

func (c *sshChannelConn) Close() error {
	return c.channel.Close()
}

func (c *sshChannelConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *sshChannelConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

type dummyAddr string

func (a dummyAddr) Network() string { return "ssh" }
func (a dummyAddr) String() string  { return string(a) }

// TestE2E_SSHBridge_TransparentOAuthFlow tests the full transparent OAuth flow
// using MockSSHServer and SSHTransport.
func TestE2E_SSHBridge_TransparentOAuthFlow(t *testing.T) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Start synthetic OAuth provider on Computer A/Cloud
	srv := oauthserver.New()
	defer srv.Close()

	// 2. Start synthetic OAuth client app on Computer B
	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-ssh-app",
		RedirectPath:  "/callback",
		Timeout:       6 * time.Second,
	})
	defer client.Close()

	authURL, waitAndExchange, err := client.DoFlow()
	if err != nil {
		t.Fatalf("client.DoFlow failed: %v", err)
	}

	// 3. Start MockSSHServer representing the SSH connection between machines
	sshServer, err := testutil.NewMockSSHServer()
	if err != nil {
		t.Fatalf("NewMockSSHServer failed: %v", err)
	}
	if err := sshServer.Start(); err != nil {
		t.Fatalf("sshServer.Start failed: %v", err)
	}
	defer sshServer.Close()

	// 4. Create SSHTransport on Computer B configured with client private key
	sshTr, err := transport.NewSSHTransport(transport.SSHTransportConfig{
		User:       "test-ssh-user",
		PrivateKey: sshServer.ClientKey,
	})
	if err != nil {
		t.Fatalf("NewSSHTransport failed: %v", err)
	}

	asidePortCh := make(chan int, 1)
	asideErrCh := make(chan error, 1)
	bsideErrCh := make(chan error, 1)
	browserErrCh := make(chan error, 1)

	// 5. Goroutine: A-side bridge handler on Computer A (accepts SSH channel from MockSSHServer)
	go func() {
		ch, err := sshServer.AcceptChannel()
		if err != nil {
			asideErrCh <- fmt.Errorf("sshServer.AcceptChannel failed: %w", err)
			return
		}
		rawConn := newSSHChannelConn(ch, nil, nil)
		defer rawConn.Close()

		aConn := &portRewritingConn{
			Conn: rawConn,
			onAckSent: func(port int) {
				asidePortCh <- port
			},
		}

		asideErrCh <- bridge.HandleASide(ctx, aConn, bridge.ASideConfig{Timeout: 6 * time.Second})
	}()

	// 6. Goroutine: B-side bridge handler on Computer B (dials SSHTransport to MockSSHServer)
	go func() {
		bConn, err := sshTr.Dial(sshServer.Addr)
		if err != nil {
			bsideErrCh <- fmt.Errorf("sshTr.Dial failed: %w", err)
			return
		}
		defer bConn.Close()

		bsideErrCh <- bridge.HandleBSide(ctx, bConn, authURL, bridge.BSideConfig{Timeout: 6 * time.Second})
	}()

	// 7. Goroutine: Browser simulation on Computer A
	go func() {
		select {
		case asidePort := <-asidePortCh:
			browserClient := &http.Client{
				Timeout: 5 * time.Second,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if req.URL.Hostname() == "127.0.0.1" || req.URL.Hostname() == "localhost" {
						req.URL.Host = fmt.Sprintf("127.0.0.1:%d", asidePort)
					}
					return nil
				},
			}

			resp, err := browserClient.Get(authURL)
			if err != nil {
				browserErrCh <- fmt.Errorf("browser visit failed: %w", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				browserErrCh <- fmt.Errorf("browser expected 200 OK, got %d: %s", resp.StatusCode, string(body))
				return
			}
			browserErrCh <- nil

		case <-ctx.Done():
			browserErrCh <- ctx.Err()
		}
	}()

	// 8. Wait for client app to receive callback and exchange code for token
	result, err := waitAndExchange()
	if err != nil {
		t.Fatalf("waitAndExchange failed: %v", err)
	}

	// 9. Verify all goroutines completed cleanly
	if browserErr := <-browserErrCh; browserErr != nil {
		t.Fatalf("browser error: %v", browserErr)
	}
	if asideErr := <-asideErrCh; asideErr != nil {
		t.Fatalf("A-side error: %v", asideErr)
	}
	if bsideErr := <-bsideErrCh; bsideErr != nil {
		t.Fatalf("B-side error: %v", bsideErr)
	}

	// 10. Verify token transparency
	if result.AccessToken == "" {
		t.Errorf("expected non-empty AccessToken")
	}
	if result.TokenType != "Bearer" {
		t.Errorf("expected TokenType 'Bearer', got %q", result.TokenType)
	}
	if result.ExpiresIn != 3600 {
		t.Errorf("expected ExpiresIn 3600, got %d", result.ExpiresIn)
	}

	elapsed := time.Since(start)
	t.Logf("SSH E2E OAuth flow completed successfully in %v", elapsed)
}

// TestE2E_SSHTransport_ListenDial_TransparentOAuthFlow tests the full transparent OAuth flow
// using SSHTransport for both Listen and Dial.
func TestE2E_SSHTransport_ListenDial_TransparentOAuthFlow(t *testing.T) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := oauthserver.New()
	defer srv.Close()

	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-ssh-listen-app",
		RedirectPath:  "/callback",
		Timeout:       6 * time.Second,
	})
	defer client.Close()

	authURL, waitAndExchange, err := client.DoFlow()
	if err != nil {
		t.Fatalf("client.DoFlow failed: %v", err)
	}

	keyPEM, err := testutil.GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatalf("GeneratePrivateKeyPEM: %v", err)
	}

	serverTr, err := transport.NewSSHTransport(transport.SSHTransportConfig{
		HostKey: keyPEM,
	})
	if err != nil {
		t.Fatalf("NewSSHTransport server: %v", err)
	}

	listener, err := serverTr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("serverTr.Listen: %v", err)
	}
	defer listener.Close()

	clientTr, err := transport.NewSSHTransport(transport.SSHTransportConfig{
		PrivateKey: keyPEM,
	})
	if err != nil {
		t.Fatalf("NewSSHTransport client: %v", err)
	}

	asidePortCh := make(chan int, 1)
	asideErrCh := make(chan error, 1)
	bsideErrCh := make(chan error, 1)
	browserErrCh := make(chan error, 1)

	go func() {
		rawConn, err := listener.Accept()
		if err != nil {
			asideErrCh <- fmt.Errorf("listener.Accept failed: %w", err)
			return
		}
		defer rawConn.Close()

		aConn := &portRewritingConn{
			Conn: rawConn,
			onAckSent: func(port int) {
				asidePortCh <- port
			},
		}

		asideErrCh <- bridge.HandleASide(ctx, aConn, bridge.ASideConfig{Timeout: 6 * time.Second})
	}()

	go func() {
		bConn, err := clientTr.Dial(listener.Addr().String())
		if err != nil {
			bsideErrCh <- fmt.Errorf("clientTr.Dial failed: %w", err)
			return
		}
		defer bConn.Close()

		bsideErrCh <- bridge.HandleBSide(ctx, bConn, authURL, bridge.BSideConfig{Timeout: 6 * time.Second})
	}()

	go func() {
		select {
		case asidePort := <-asidePortCh:
			browserClient := &http.Client{
				Timeout: 5 * time.Second,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if req.URL.Hostname() == "127.0.0.1" || req.URL.Hostname() == "localhost" {
						req.URL.Host = fmt.Sprintf("127.0.0.1:%d", asidePort)
					}
					return nil
				},
			}

			resp, err := browserClient.Get(authURL)
			if err != nil {
				browserErrCh <- fmt.Errorf("browser visit failed: %w", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				browserErrCh <- fmt.Errorf("browser expected 200 OK, got %d: %s", resp.StatusCode, string(body))
				return
			}
			browserErrCh <- nil

		case <-ctx.Done():
			browserErrCh <- ctx.Err()
		}
	}()

	result, err := waitAndExchange()
	if err != nil {
		t.Fatalf("waitAndExchange failed: %v", err)
	}

	if browserErr := <-browserErrCh; browserErr != nil {
		t.Fatalf("browser error: %v", browserErr)
	}
	if asideErr := <-asideErrCh; asideErr != nil {
		t.Fatalf("A-side error: %v", asideErr)
	}
	if bsideErr := <-bsideErrCh; bsideErr != nil {
		t.Fatalf("B-side error: %v", bsideErr)
	}

	if result.AccessToken == "" {
		t.Errorf("expected non-empty AccessToken")
	}

	elapsed := time.Since(start)
	t.Logf("SSHTransport Listen/Dial E2E OAuth flow completed in %v", elapsed)
}

// TestE2E_SSHBridge_FullCLIFlow simulates the complete CLI-like workflow over SSH:
// - MockSSHServer running as the SSH endpoint
// - Synthetic OAuth server running as the identity provider
// - Synthetic client application initiating OAuth flow on Computer B
// - Bridge serve (A-side) and bridge open (B-side) running concurrently over SSH
// - Browser simulation opening the auth URL and following redirect to A-side callback
// - Dynamic port fallback handled correctly
// - Complete callback proxying over SSH and token exchange
func TestE2E_SSHBridge_FullCLIFlow(t *testing.T) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Start synthetic OAuth provider on Computer A/Cloud
	srv := oauthserver.New()
	defer srv.Close()

	// 2. Start synthetic OAuth client app on Computer B
	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-cli-ssh-app",
		RedirectPath:  "/callback",
		Timeout:       6 * time.Second,
	})
	defer client.Close()

	authURL, waitAndExchange, err := client.DoFlow()
	if err != nil {
		t.Fatalf("client.DoFlow failed: %v", err)
	}

	appPort, err := bridge.ExtractCallbackPort(authURL)
	if err != nil {
		t.Fatalf("ExtractCallbackPort failed: %v", err)
	}

	// 3. Start MockSSHServer representing the remote SSH host
	sshServer, err := testutil.NewMockSSHServer()
	if err != nil {
		t.Fatalf("NewMockSSHServer failed: %v", err)
	}
	if err := sshServer.Start(); err != nil {
		t.Fatalf("sshServer.Start failed: %v", err)
	}
	defer sshServer.Close()

	// 4. Create SSHTransport for Computer B configured with client private key
	sshTr, err := transport.NewSSHTransport(transport.SSHTransportConfig{
		User:       "test-ssh-user",
		PrivateKey: sshServer.ClientKey,
	})
	if err != nil {
		t.Fatalf("NewSSHTransport failed: %v", err)
	}

	asidePortCh := make(chan int, 1)
	asideErrCh := make(chan error, 1)
	bsideErrCh := make(chan error, 1)
	browserErrCh := make(chan error, 1)
	openedURLCh := make(chan string, 1)

	openBrowserMock := func(url string) error {
		openedURLCh <- url
		return nil
	}

	// 5. Goroutine: A-side bridge handler on Computer A (accepts SSH channel from MockSSHServer)
	// Note: portRewritingConn rewrites CallbackPort to 0 for single-machine loopback execution.
	go func() {
		ch, err := sshServer.AcceptChannel()
		if err != nil {
			asideErrCh <- fmt.Errorf("sshServer.AcceptChannel failed: %w", err)
			return
		}
		rawConn := newSSHChannelConn(ch, nil, nil)
		defer rawConn.Close()

		aConn := &portRewritingConn{
			Conn: rawConn,
			onAckSent: func(port int) {
				asidePortCh <- port
			},
		}

		cfg := bridge.ASideConfig{
			Timeout:     6 * time.Second,
			OpenBrowser: openBrowserMock,
		}
		asideErrCh <- bridge.HandleASide(ctx, aConn, cfg)
	}()

	// 6. Goroutine: B-side bridge handler on Computer B (dials SSHTransport to MockSSHServer)
	go func() {
		bConn, err := sshTr.Dial(sshServer.Addr)
		if err != nil {
			bsideErrCh <- fmt.Errorf("sshTr.Dial failed: %w", err)
			return
		}
		defer bConn.Close()

		bsideErrCh <- bridge.HandleBSide(ctx, bConn, authURL, bridge.BSideConfig{Timeout: 6 * time.Second})
	}()

	// 7. Goroutine: Browser simulation on Computer A using fallback port
	go func() {
		select {
		case asidePort := <-asidePortCh:
			if asidePort == 0 {
				browserErrCh <- fmt.Errorf("expected non-zero fallback port")
				return
			}
			if asidePort == appPort {
				browserErrCh <- fmt.Errorf("expected fallback port to differ from occupied app port %d, got %d", appPort, asidePort)
				return
			}

			browserClient := &http.Client{
				Timeout: 5 * time.Second,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if req.URL.Hostname() == "127.0.0.1" || req.URL.Hostname() == "localhost" {
						req.URL.Host = fmt.Sprintf("127.0.0.1:%d", asidePort)
					}
					return nil
				},
			}

			resp, err := browserClient.Get(authURL)
			if err != nil {
				browserErrCh <- fmt.Errorf("browser visit failed: %w", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				browserErrCh <- fmt.Errorf("browser expected 200 OK, got %d: %s", resp.StatusCode, string(body))
				return
			}
			browserErrCh <- nil

		case <-ctx.Done():
			browserErrCh <- ctx.Err()
		}
	}()

	// 8. Verify OpenBrowser was invoked with authURL
	select {
	case openedURL := <-openedURLCh:
		if openedURL != authURL {
			t.Errorf("OpenBrowser received %q, want %q", openedURL, authURL)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for OpenBrowser invocation")
	}

	// 9. Wait for client app to receive callback and exchange code for token
	result, err := waitAndExchange()
	if err != nil {
		t.Fatalf("waitAndExchange failed: %v", err)
	}

	// 10. Verify all goroutines completed cleanly
	if browserErr := <-browserErrCh; browserErr != nil {
		t.Fatalf("browser error: %v", browserErr)
	}
	if asideErr := <-asideErrCh; asideErr != nil {
		t.Fatalf("A-side error: %v", asideErr)
	}
	if bsideErr := <-bsideErrCh; bsideErr != nil {
		t.Fatalf("B-side error: %v", bsideErr)
	}

	// 11. Verify token transparency
	if result.AccessToken == "" {
		t.Errorf("expected non-empty AccessToken")
	}
	if result.TokenType != "Bearer" {
		t.Errorf("expected TokenType 'Bearer', got %q", result.TokenType)
	}
	if result.ExpiresIn != 3600 {
		t.Errorf("expected ExpiresIn 3600, got %d", result.ExpiresIn)
	}

	elapsed := time.Since(start)
	t.Logf("Full CLI SSH E2E OAuth flow completed successfully in %v", elapsed)
}

// TestE2E_SSHBridge_DynamicPortFallback explicitly verifies that when callback port
// collision occurs on the A-side host over SSH transport, A-side gracefully falls back
// to a dynamically allocated port, communicates it via BridgeAck over SSH, and completes
// the full transparent OAuth flow.
func TestE2E_SSHBridge_OccupiedPortReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := oauthserver.New()
	defer srv.Close()

	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-ssh-fallback-app",
		RedirectPath:  "/callback",
		Timeout:       6 * time.Second,
	})
	defer client.Close()

	authURL, _, err := client.DoFlow()
	if err != nil {
		t.Fatalf("client.DoFlow failed: %v", err)
	}

	sshServer, err := testutil.NewMockSSHServer()
	if err != nil {
		t.Fatalf("NewMockSSHServer failed: %v", err)
	}
	if err := sshServer.Start(); err != nil {
		t.Fatalf("sshServer.Start failed: %v", err)
	}
	defer sshServer.Close()

	sshTr, err := transport.NewSSHTransport(transport.SSHTransportConfig{
		User:       "test-ssh-user",
		PrivateKey: sshServer.ClientKey,
	})
	if err != nil {
		t.Fatalf("NewSSHTransport failed: %v", err)
	}

	asideErrCh := make(chan error, 1)
	bsideErrCh := make(chan error, 1)

	// A-side bridge handler with portCapturingConn (no port rewriting: req.CallbackPort == appPort)
	go func() {
		ch, err := sshServer.AcceptChannel()
		if err != nil {
			asideErrCh <- fmt.Errorf("sshServer.AcceptChannel failed: %w", err)
			return
		}
		rawConn := newSSHChannelConn(ch, nil, nil)
		defer rawConn.Close()

		aConn := &portCapturingConn{
			Conn: rawConn,
		}

		asideErrCh <- bridge.HandleASide(ctx, aConn, bridge.ASideConfig{Timeout: 6 * time.Second})
	}()

	// B-side bridge handler dials SSHTransport
	go func() {
		bConn, err := sshTr.Dial(sshServer.Addr)
		if err != nil {
			bsideErrCh <- fmt.Errorf("sshTr.Dial failed: %w", err)
			return
		}
		defer bConn.Close()

		bsideErrCh <- bridge.HandleBSide(ctx, bConn, authURL, bridge.BSideConfig{Timeout: 6 * time.Second})
	}()

	// Verify A-side returns error because port is occupied
	select {
	case err := <-asideErrCh:
		if err == nil {
			t.Fatal("expected A-side error due to occupied port, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for A-side error")
	}

	// Verify B-side receives BridgeAck error from A-side
	select {
	case err := <-bsideErrCh:
		if err == nil {
			t.Fatal("expected B-side error from BridgeAck, got nil")
		}
		if !strings.Contains(err.Error(), "is in use on this machine") {
			t.Errorf("expected B-side error to mention port in use, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for B-side error")
	}
}
