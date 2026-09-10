package drop_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// Helper to run a concurrent E2E pair over loopback transport
func runE2EDrop(
	ctx context.Context,
	t *testing.T,
	meta drop.DropSend,
	payload io.Reader,
	sendCfg drop.SendDropConfig,
	recvWriter io.Writer,
	recvCfg drop.ReceiveDropConfig,
) (*drop.ReceiveDropResult, error, error) {
	t.Helper()

	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("tr.Listen failed: %v", err)
	}
	defer listener.Close()

	var recvResult *drop.ReceiveDropResult
	var recvErr, sendErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		conn, err := listener.Accept()
		if err != nil {
			recvErr = err
			return
		}
		defer conn.Close()
		recvResult, recvErr = drop.ReceiveDrop(ctx, conn, recvWriter, recvCfg)
	}()

	go func() {
		defer wg.Done()
		conn, err := tr.Dial(listener.Addr().String())
		if err != nil {
			sendErr = err
			return
		}
		defer conn.Close()
		sendErr = drop.SendDrop(ctx, conn, meta, payload, sendCfg)
	}()

	wg.Wait()
	return recvResult, sendErr, recvErr
}

func TestE2E_TextDrop_RoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sampleText := "QuickDrop E2E Text RoundTrip: tantu is fast, encrypted, and reliable."
	meta := drop.DropSend{
		Kind: drop.DropKindText,
		Name: "notes.txt",
		Size: int64(len(sampleText)),
	}

	var buf bytes.Buffer
	res, sendErr, recvErr := runE2EDrop(
		ctx,
		t,
		meta,
		strings.NewReader(sampleText),
		drop.SendDropConfig{},
		&buf,
		drop.ReceiveDropConfig{},
	)

	if sendErr != nil {
		t.Fatalf("SendDrop error: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop error: %v", recvErr)
	}
	if res == nil {
		t.Fatal("expected non-nil ReceiveDropResult")
	}

	if buf.String() != sampleText {
		t.Fatalf("content mismatch:\nexpected: %s\ngot:      %s", sampleText, buf.String())
	}
	if res.Meta.Kind != drop.DropKindText {
		t.Errorf("expected kind %s, got %s", drop.DropKindText, res.Meta.Kind)
	}
	if res.Meta.Name != "notes.txt" {
		t.Errorf("expected name notes.txt, got %s", res.Meta.Name)
	}
	if res.BytesWritten != int64(len(sampleText)) {
		t.Errorf("expected BytesWritten %d, got %d", len(sampleText), res.BytesWritten)
	}
}

func TestE2E_FileDrop_MultiChunk_Integrity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 3MB deterministic byte sequence (forces chunking with 1MB chunks)
	pattern := []byte{0xAB, 0xCD}
	totalBytes := 3 * 1024 * 1024
	fileData := bytes.Repeat(pattern, totalBytes/len(pattern))

	hasher := sha256.New()
	hasher.Write(fileData)
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	meta := drop.DropSend{
		Kind: drop.DropKindFile,
		Name: "test-payload.bin",
		Size: int64(len(fileData)),
	}

	var buf bytes.Buffer
	res, sendErr, recvErr := runE2EDrop(
		ctx,
		t,
		meta,
		bytes.NewReader(fileData),
		drop.SendDropConfig{},
		&buf,
		drop.ReceiveDropConfig{},
	)

	if sendErr != nil {
		t.Fatalf("SendDrop error: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop error: %v", recvErr)
	}
	if res == nil {
		t.Fatal("expected non-nil ReceiveDropResult")
	}

	recvHasher := sha256.New()
	recvHasher.Write(buf.Bytes())
	actualHash := hex.EncodeToString(recvHasher.Sum(nil))

	if actualHash != expectedHash {
		t.Fatalf("SHA-256 hash mismatch on 3MB payload:\nexpected: %s\ngot:      %s", expectedHash, actualHash)
	}
	if res.BytesWritten != int64(len(fileData)) {
		t.Errorf("expected BytesWritten %d, got %d", len(fileData), res.BytesWritten)
	}
	if res.Meta.Kind != drop.DropKindFile {
		t.Errorf("expected kind %s, got %s", drop.DropKindFile, res.Meta.Kind)
	}
	if res.Meta.Name != "test-payload.bin" {
		t.Errorf("expected name test-payload.bin, got %s", res.Meta.Name)
	}
}

func TestE2E_FileDrop_SmallFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	data := bytes.Repeat([]byte{0x42}, 100)
	meta := drop.DropSend{
		Kind:     drop.DropKindFile,
		Name:     "avatar.png",
		Size:     100,
		MIMEType: "image/png",
	}

	var buf bytes.Buffer
	res, sendErr, recvErr := runE2EDrop(
		ctx,
		t,
		meta,
		bytes.NewReader(data),
		drop.SendDropConfig{},
		&buf,
		drop.ReceiveDropConfig{},
	)

	if sendErr != nil {
		t.Fatalf("SendDrop error: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop error: %v", recvErr)
	}
	if res == nil {
		t.Fatal("expected non-nil ReceiveDropResult")
	}

	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatal("received bytes do not match expected 100 bytes")
	}
	if res.Meta.Name != "avatar.png" {
		t.Errorf("expected name avatar.png, got %s", res.Meta.Name)
	}
	if res.Meta.MIMEType != "image/png" {
		t.Errorf("expected MIMEType image/png, got %s", res.Meta.MIMEType)
	}
	if res.BytesWritten != 100 {
		t.Errorf("expected BytesWritten 100, got %d", res.BytesWritten)
	}
}

func TestE2E_ReceiverRejectsOversizeFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	payload := bytes.Repeat([]byte("A"), 10*1024) // 10KB
	meta := drop.DropSend{
		Kind: drop.DropKindFile,
		Name: "large_file.dat",
		Size: int64(len(payload)),
	}

	var buf bytes.Buffer
	_, sendErr, recvErr := runE2EDrop(
		ctx,
		t,
		meta,
		bytes.NewReader(payload),
		drop.SendDropConfig{},
		&buf,
		drop.ReceiveDropConfig{
			MaxSize: 1024, // Limit to 1KB
		},
	)

	if sendErr == nil {
		t.Fatal("expected send error due to receiver rejection, got nil")
	}
	if recvErr == nil {
		t.Fatal("expected receive error due to exceeding max size, got nil")
	}

	errMsg := sendErr.Error()
	if !strings.Contains(errMsg, "rejected") && !strings.Contains(errMsg, "exceed") {
		t.Errorf("expected send error to contain 'rejected' or 'exceed', got: %v", sendErr)
	}
}

func TestE2E_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	payload := bytes.Repeat([]byte("Z"), 2*1024*1024)
	meta := drop.DropSend{
		Kind: drop.DropKindFile,
		Name: "cancelled.bin",
		Size: int64(len(payload)),
	}

	var wg sync.WaitGroup
	var sendErr, recvErr error
	wg.Add(2)

	go func() {
		defer wg.Done()
		conn, err := listener.Accept()
		if err != nil {
			recvErr = err
			return
		}
		defer conn.Close()
		var buf bytes.Buffer
		_, recvErr = drop.ReceiveDrop(ctx, conn, &buf, drop.ReceiveDropConfig{})
	}()

	go func() {
		defer wg.Done()
		conn, err := tr.Dial(listener.Addr().String())
		if err != nil {
			sendErr = err
			return
		}
		defer conn.Close()
		// Cancel context immediately during send
		cancel()
		sendErr = drop.SendDrop(ctx, conn, meta, bytes.NewReader(payload), drop.SendDropConfig{})
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Operation completed promptly after cancellation without hanging
		if sendErr == nil && recvErr == nil {
			t.Log("Note: operation completed before cancellation hook fired")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("operation hung after context cancellation")
	}
}
