package bridge_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/testutil/oauthclient"
	"github.com/bhaskarjha-dev/tantu/internal/testutil/oauthserver"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// bRewritingConn intercepts BridgeRequest to set CallbackPort to 0 (dynamic loopback port)
// and intercepts BridgeAck to capture the allocated listening port for the browser simulation.
type bRewritingConn struct {
	transport.Conn
	onAckReceived func(port int)
}

func (c *bRewritingConn) Send(msgType string, payload any) error {
	if msgType == protocol.TypeBridgeRequest {
		if req, ok := payload.(protocol.BridgeRequest); ok {
			req.CallbackPort = 0 // dynamic free port on loopback
			return c.Conn.Send(msgType, req)
		}
	}
	return c.Conn.Send(msgType, payload)
}

func (c *bRewritingConn) Receive() (*protocol.Envelope, error) {
	env, err := c.Conn.Receive()
	if err != nil {
		return nil, err
	}
	if env.Type == protocol.TypeBridgeAck {
		var ack protocol.BridgeAck
		if err := env.DecodePayload(&ack); err == nil {
			if c.onAckReceived != nil {
				c.onAckReceived(ack.ListeningPort)
			}
		}
	}
	return env, nil
}

func TestE2E_MultiplexedOAuthAndDrop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 1. Start synthetic OAuth provider
	srv := oauthserver.New()
	defer srv.Close()

	// 2. Start synthetic OAuth client app
	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "multiplex-client-app",
		RedirectPath:  "/callback",
		Timeout:       10 * time.Second,
	})
	defer client.Close()

	authURL, waitAndExchange, err := client.DoFlow()
	if err != nil {
		t.Fatalf("client.DoFlow failed: %v", err)
	}

	// 3. Start loopback listener for multiplexed dispatcher
	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("tr.Listen failed: %v", err)
	}
	defer listener.Close()
	listenerAddr := listener.Addr().String()

	// 4. Generate 3MB random binary payload for QuickDrop
	const dropSize = 3 * 1024 * 1024 // 3MB
	payload := make([]byte, dropSize)
	if _, err := io.ReadFull(rand.Reader, payload); err != nil {
		t.Fatalf("failed to generate random drop payload: %v", err)
	}
	expectedDropHash := sha256.Sum256(payload)

	var dropBuf bytes.Buffer
	var dropMu sync.Mutex
	dropResultCh := make(chan *drop.ReceiveDropResult, 1)
	asideDoneCh := make(chan error, 1)

	cfg := bridge.DispatcherConfig{
		ASideConfig: bridge.ASideConfig{
			Timeout: 10 * time.Second,
		},
		DropConfig: drop.ReceiveDropConfig{
			Timeout: 10 * time.Second,
			MaxSize: -1, // Unlimited
			OnMeta: func(meta drop.DropSend) (io.Writer, error) {
				return &dropBuf, nil
			},
		},
		OnDropReceived: func(res *drop.ReceiveDropResult) {
			dropResultCh <- res
		},
		OnASideDone: func(err error) {
			asideDoneCh <- err
		},
	}

	dispatcher := bridge.NewDispatcher(listener, cfg)
	go func() {
		_ = dispatcher.Serve(ctx)
	}()

	// 5. Concurrently execute OAuth flow and QuickDrop transfer
	asidePortCh := make(chan int, 1)
	oauthErrCh := make(chan error, 1)
	browserErrCh := make(chan error, 1)
	exchangeErrCh := make(chan error, 1)
	dropErrCh := make(chan error, 1)

	var wg sync.WaitGroup
	wg.Add(4)

	// Worker 1: B-side bridge handler for OAuth
	go func() {
		defer wg.Done()
		rawConn, err := tr.Dial(listenerAddr)
		if err != nil {
			oauthErrCh <- fmt.Errorf("dial oauth failed: %w", err)
			return
		}
		defer rawConn.Close()

		conn := &bRewritingConn{
			Conn: rawConn,
			onAckReceived: func(port int) {
				asidePortCh <- port
			},
		}

		oauthErrCh <- bridge.HandleBSide(ctx, conn, authURL, bridge.BSideConfig{Timeout: 10 * time.Second})
	}()

	// Worker 2: Browser simulation following redirect to A-side's callback port
	go func() {
		defer wg.Done()
		select {
		case asidePort := <-asidePortCh:
			browserClient := &http.Client{
				Timeout: 8 * time.Second,
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

	// Worker 3: OAuth client waits for callback and exchanges code for access token
	go func() {
		defer wg.Done()
		result, err := waitAndExchange()
		if err != nil {
			exchangeErrCh <- fmt.Errorf("waitAndExchange failed: %w", err)
			return
		}
		if result.AccessToken == "" {
			exchangeErrCh <- fmt.Errorf("expected non-empty AccessToken")
			return
		}
		exchangeErrCh <- nil
	}()

	// Worker 4: QuickDrop sender transferring 3MB file in chunks over the same listener port
	go func() {
		defer wg.Done()
		dropConn, err := tr.Dial(listenerAddr)
		if err != nil {
			dropErrCh <- fmt.Errorf("dial drop failed: %w", err)
			return
		}
		defer dropConn.Close()

		meta := drop.DropSend{
			DropID:    "drop-concurrent-e2e-3mb",
			Kind:      drop.DropKindFile,
			Name:      "payload-3mb.bin",
			Size:      int64(dropSize),
			ChunkSize: 256 * 1024, // 256KB chunks
		}

		dropErrCh <- drop.SendDrop(ctx, dropConn, meta, bytes.NewReader(payload), drop.SendDropConfig{})
	}()

	// 6. Wait for all concurrent routines to complete
	wg.Wait()

	// 7. Check for errors
	if err := <-browserErrCh; err != nil {
		t.Fatalf("browser error: %v", err)
	}
	if err := <-oauthErrCh; err != nil {
		t.Fatalf("oauth B-side error: %v", err)
	}
	if err := <-exchangeErrCh; err != nil {
		t.Fatalf("oauth token exchange error: %v", err)
	}
	if err := <-dropErrCh; err != nil {
		t.Fatalf("QuickDrop send error: %v", err)
	}

	// 8. Verify QuickDrop integrity
	select {
	case res := <-dropResultCh:
		if res == nil {
			t.Fatal("expected non-nil ReceiveDropResult")
		}
		if res.BytesWritten != int64(dropSize) {
			t.Errorf("expected %d bytes written, got %d", dropSize, res.BytesWritten)
		}
		if res.Meta.Kind != drop.DropKindFile {
			t.Errorf("expected kind %q, got %q", drop.DropKindFile, res.Meta.Kind)
		}
		if res.Meta.Name != "payload-3mb.bin" {
			t.Errorf("expected name %q, got %q", "payload-3mb.bin", res.Meta.Name)
		}

		dropMu.Lock()
		actualDropHash := sha256.Sum256(dropBuf.Bytes())
		dropMu.Unlock()

		if actualDropHash != expectedDropHash {
			t.Fatalf("SHA-256 hash mismatch on QuickDrop payload!\nExpected: %s\nActual:   %s",
				hex.EncodeToString(expectedDropHash[:]),
				hex.EncodeToString(actualDropHash[:]))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for QuickDrop OnDropReceived callback")
	}

	// 9. Verify OAuth ASide finished cleanly
	select {
	case err := <-asideDoneCh:
		if err != nil {
			t.Fatalf("OAuth ASide reported error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OAuth OnASideDone callback")
	}
}

func TestE2E_MultiplexedMultipleConcurrentClients(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	srv := oauthserver.New()
	defer srv.Close()

	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "multiplex-multi-client",
		RedirectPath:  "/callback",
		Timeout:       10 * time.Second,
	})
	defer client.Close()

	authURL, waitAndExchange, err := client.DoFlow()
	if err != nil {
		t.Fatalf("client.DoFlow failed: %v", err)
	}

	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("tr.Listen failed: %v", err)
	}
	defer listener.Close()
	listenerAddr := listener.Addr().String()

	var mu sync.Mutex
	receivedDrops := make(map[string]*bytes.Buffer)
	dropDoneCh := make(chan string, 10)

	cfg := bridge.DispatcherConfig{
		ASideConfig: bridge.ASideConfig{
			Timeout: 10 * time.Second,
		},
		DropConfig: drop.ReceiveDropConfig{
			Timeout: 10 * time.Second,
			MaxSize: -1,
			OnMeta: func(meta drop.DropSend) (io.Writer, error) {
				mu.Lock()
				defer mu.Unlock()
				b := new(bytes.Buffer)
				receivedDrops[meta.DropID] = b
				return b, nil
			},
		},
		OnDropReceived: func(res *drop.ReceiveDropResult) {
			dropDoneCh <- res.Meta.DropID
		},
	}

	dispatcher := bridge.NewDispatcher(listener, cfg)
	go func() {
		_ = dispatcher.Serve(ctx)
	}()

	// 1MB file payload
	fileSize := 1024 * 1024
	filePayload := make([]byte, fileSize)
	if _, err := io.ReadFull(rand.Reader, filePayload); err != nil {
		t.Fatalf("rand.ReadFull failed: %v", err)
	}
	expectedFileHash := sha256.Sum256(filePayload)

	// Text payload
	textPayload := "Multiplexed concurrent text snippet across port 9877"

	asidePortCh := make(chan int, 1)
	errCh := make(chan error, 5)

	var wg sync.WaitGroup
	wg.Add(4)

	// Client 1: File Drop
	go func() {
		defer wg.Done()
		conn, err := tr.Dial(listenerAddr)
		if err != nil {
			errCh <- fmt.Errorf("dial file drop: %w", err)
			return
		}
		defer conn.Close()

		meta := drop.DropSend{
			DropID: "drop-file-1",
			Kind:   drop.DropKindFile,
			Name:   "test-1mb.bin",
			Size:   int64(fileSize),
		}
		if err := drop.SendDrop(ctx, conn, meta, bytes.NewReader(filePayload), drop.SendDropConfig{}); err != nil {
			errCh <- fmt.Errorf("send file drop: %w", err)
			return
		}
	}()

	// Client 2: Text Drop
	go func() {
		defer wg.Done()
		conn, err := tr.Dial(listenerAddr)
		if err != nil {
			errCh <- fmt.Errorf("dial text drop: %w", err)
			return
		}
		defer conn.Close()

		meta := drop.DropSend{
			DropID: "drop-text-2",
			Kind:   drop.DropKindText,
			Name:   "snippet.txt",
			Size:   int64(len(textPayload)),
		}
		if err := drop.SendDrop(ctx, conn, meta, strings.NewReader(textPayload), drop.SendDropConfig{}); err != nil {
			errCh <- fmt.Errorf("send text drop: %w", err)
			return
		}
	}()

	// Client 3: OAuth BSide
	go func() {
		defer wg.Done()
		rawConn, err := tr.Dial(listenerAddr)
		if err != nil {
			errCh <- fmt.Errorf("dial oauth: %w", err)
			return
		}
		defer rawConn.Close()

		conn := &bRewritingConn{
			Conn: rawConn,
			onAckReceived: func(port int) {
				asidePortCh <- port
			},
		}
		if err := bridge.HandleBSide(ctx, conn, authURL, bridge.BSideConfig{Timeout: 10 * time.Second}); err != nil {
			errCh <- fmt.Errorf("HandleBSide: %w", err)
			return
		}
	}()

	// Client 4: Browser Simulator
	go func() {
		defer wg.Done()
		select {
		case asidePort := <-asidePortCh:
			browserClient := &http.Client{
				Timeout: 8 * time.Second,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if req.URL.Hostname() == "127.0.0.1" || req.URL.Hostname() == "localhost" {
						req.URL.Host = fmt.Sprintf("127.0.0.1:%d", asidePort)
					}
					return nil
				},
			}
			resp, err := browserClient.Get(authURL)
			if err != nil {
				errCh <- fmt.Errorf("browser get: %w", err)
				return
			}
			_ = resp.Body.Close()
		case <-ctx.Done():
			errCh <- ctx.Err()
		}
	}()

	// OAuth Token Exchange
	tokenResult, exchangeErr := waitAndExchange()
	if exchangeErr != nil {
		t.Fatalf("waitAndExchange failed: %v", exchangeErr)
	}
	if tokenResult.AccessToken == "" {
		t.Errorf("expected non-empty AccessToken")
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent client error: %v", err)
		}
	}

	// Wait for both drops to finish
	receivedSet := make(map[string]bool)
	for i := 0; i < 2; i++ {
		select {
		case id := <-dropDoneCh:
			receivedSet[id] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for drop completion (received %d/2)", len(receivedSet))
		}
	}

	mu.Lock()
	defer mu.Unlock()

	// Verify file drop SHA-256
	fileBuf := receivedDrops["drop-file-1"]
	if fileBuf == nil {
		t.Fatal("drop-file-1 buffer not found")
	}
	actualFileHash := sha256.Sum256(fileBuf.Bytes())
	if actualFileHash != expectedFileHash {
		t.Errorf("drop-file-1 hash mismatch")
	}

	// Verify text drop content
	textBuf := receivedDrops["drop-text-2"]
	if textBuf == nil {
		t.Fatal("drop-text-2 buffer not found")
	}
	if textBuf.String() != textPayload {
		t.Errorf("drop-text-2 content mismatch, expected %q, got %q", textPayload, textBuf.String())
	}
}
