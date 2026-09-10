package drop_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func runDropPair(
	t *testing.T,
	meta drop.DropSend,
	payload io.Reader,
	sendCfg drop.SendDropConfig,
	recvWriter io.Writer,
	recvCfg drop.ReceiveDropConfig,
) (*drop.ReceiveDropResult, error, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
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

func TestDropText(t *testing.T) {
	msg := "Hello, QuickDrop!"
	meta := drop.DropSend{
		Kind: drop.DropKindText,
		Name: "snippet",
		Size: int64(len(msg)),
	}

	var buf bytes.Buffer
	res, sendErr, recvErr := runDropPair(
		t,
		meta,
		strings.NewReader(msg),
		drop.SendDropConfig{},
		&buf,
		drop.ReceiveDropConfig{},
	)

	if sendErr != nil {
		t.Fatalf("SendDrop failed: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop failed: %v", recvErr)
	}
	if res == nil {
		t.Fatal("expected non-nil ReceiveDropResult")
	}

	if buf.String() != msg {
		t.Errorf("received payload %q, expected %q", buf.String(), msg)
	}
	if res.Meta.Kind != drop.DropKindText {
		t.Errorf("expected Kind=%s, got %s", drop.DropKindText, res.Meta.Kind)
	}
	if res.BytesWritten != int64(len(msg)) {
		t.Errorf("expected BytesWritten=%d, got %d", len(msg), res.BytesWritten)
	}
}

func TestDropFile(t *testing.T) {
	fileData := bytes.Repeat([]byte("AuthBridgeQuickDropFileContentTest"), 3) // ~102 bytes
	meta := drop.DropSend{
		Kind:     drop.DropKindFile,
		Name:     "auth-dump.bin",
		Size:     int64(len(fileData)),
		MIMEType: "application/octet-stream",
	}

	var buf bytes.Buffer
	res, sendErr, recvErr := runDropPair(
		t,
		meta,
		bytes.NewReader(fileData),
		drop.SendDropConfig{},
		&buf,
		drop.ReceiveDropConfig{},
	)

	if sendErr != nil {
		t.Fatalf("SendDrop failed: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop failed: %v", recvErr)
	}
	if res == nil {
		t.Fatal("expected non-nil ReceiveDropResult")
	}

	if !bytes.Equal(buf.Bytes(), fileData) {
		t.Errorf("payload mismatch: received %d bytes, expected %d bytes", buf.Len(), len(fileData))
	}
	if res.Meta.Kind != drop.DropKindFile {
		t.Errorf("expected Kind=%s, got %s", drop.DropKindFile, res.Meta.Kind)
	}
	if res.Meta.Name != "auth-dump.bin" {
		t.Errorf("expected Name=%s, got %s", "auth-dump.bin", res.Meta.Name)
	}
	if res.BytesWritten != int64(len(fileData)) {
		t.Errorf("expected BytesWritten=%d, got %d", len(fileData), res.BytesWritten)
	}
}

func TestDropMultiChunk(t *testing.T) {
	// Generate 2.5MB of deterministic data (> DefaultChunkSize = 1MB)
	pattern := []byte("0123456789abcdef")
	count := 160000 // 160,000 * 16 = 2,560,000 bytes (~2.44 MB)
	largeData := bytes.Repeat(pattern, count)

	meta := drop.DropSend{
		Kind: drop.DropKindFile,
		Name: "large.bin",
		Size: int64(len(largeData)),
	}

	var buf bytes.Buffer
	res, sendErr, recvErr := runDropPair(
		t,
		meta,
		bytes.NewReader(largeData),
		drop.SendDropConfig{},
		&buf,
		drop.ReceiveDropConfig{},
	)

	if sendErr != nil {
		t.Fatalf("SendDrop failed: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop failed: %v", recvErr)
	}
	if res == nil {
		t.Fatal("expected non-nil ReceiveDropResult")
	}

	if buf.Len() != len(largeData) {
		t.Fatalf("expected %d bytes, got %d bytes", len(largeData), buf.Len())
	}
	if !bytes.Equal(buf.Bytes(), largeData) {
		t.Fatal("multi-chunk payload bytes do not match expected input")
	}
	if res.BytesWritten != int64(len(largeData)) {
		t.Errorf("expected BytesWritten=%d, got %d", len(largeData), res.BytesWritten)
	}
}

func TestDropEmptyPayload(t *testing.T) {
	meta := drop.DropSend{
		Kind: drop.DropKindText,
		Name: "empty.txt",
		Size: 0,
	}

	var buf bytes.Buffer
	res, sendErr, recvErr := runDropPair(
		t,
		meta,
		bytes.NewReader(nil),
		drop.SendDropConfig{},
		&buf,
		drop.ReceiveDropConfig{},
	)

	if sendErr != nil {
		t.Fatalf("SendDrop failed: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop failed: %v", recvErr)
	}
	if res == nil {
		t.Fatal("expected non-nil ReceiveDropResult")
	}

	if buf.Len() != 0 {
		t.Errorf("expected 0 bytes in buffer, got %d", buf.Len())
	}
	if res.BytesWritten != 0 {
		t.Errorf("expected BytesWritten=0, got %d", res.BytesWritten)
	}
}

func TestDropRejectedMaxSize(t *testing.T) {
	payload := bytes.Repeat([]byte("X"), 1000)
	meta := drop.DropSend{
		Kind: drop.DropKindFile,
		Name: "oversized.bin",
		Size: int64(len(payload)),
	}

	var buf bytes.Buffer
	_, sendErr, recvErr := runDropPair(
		t,
		meta,
		bytes.NewReader(payload),
		drop.SendDropConfig{},
		&buf,
		drop.ReceiveDropConfig{
			MaxSize: 100, // Small limit to reject 1000 bytes
		},
	)

	if sendErr == nil {
		t.Fatal("expected SendDrop to fail due to receiver rejection, got nil")
	}
	if recvErr == nil {
		t.Fatal("expected ReceiveDrop to reject oversized drop, got nil")
	}

	if !strings.Contains(sendErr.Error(), "exceed") && !strings.Contains(sendErr.Error(), "rejected") {
		t.Errorf("expected error mentioning exceed/rejected, got: %v", sendErr)
	}
}

func TestDropUnlimitedMaxSize(t *testing.T) {
	payload := bytes.Repeat([]byte("UnlimitedDropPayload"), 50)
	meta := drop.DropSend{
		Kind: drop.DropKindFile,
		Name: "unlimited.bin",
		Size: int64(len(payload)),
	}

	var buf bytes.Buffer
	res, sendErr, recvErr := runDropPair(
		t,
		meta,
		bytes.NewReader(payload),
		drop.SendDropConfig{},
		&buf,
		drop.ReceiveDropConfig{
			MaxSize: -1, // Unlimited
		},
	)

	if sendErr != nil {
		t.Fatalf("SendDrop failed: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop failed: %v", recvErr)
	}
	if res.BytesWritten != int64(len(payload)) {
		t.Errorf("expected BytesWritten=%d, got %d", len(payload), res.BytesWritten)
	}
	if !bytes.Equal(buf.Bytes(), payload) {
		t.Error("payload received in unlimited mode does not match expected bytes")
	}
}

func TestDropSHA256Integrity(t *testing.T) {
	payload := []byte("CryptographicIntegrityTestPayload1234567890")
	expectedHash := sha256.Sum256(payload)
	expectedSHA := hex.EncodeToString(expectedHash[:])

	meta := drop.DropSend{
		Kind: drop.DropKindFile,
		Name: "integrity.txt",
		Size: int64(len(payload)),
	}

	var buf bytes.Buffer
	res, sendErr, recvErr := runDropPair(
		t,
		meta,
		bytes.NewReader(payload),
		drop.SendDropConfig{},
		&buf,
		drop.ReceiveDropConfig{},
	)

	if sendErr != nil {
		t.Fatalf("SendDrop failed: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop failed: %v", recvErr)
	}
	if res.SHA256 != expectedSHA {
		t.Errorf("expected SHA256 %s, got %s", expectedSHA, res.SHA256)
	}
	if !bytes.Equal(buf.Bytes(), payload) {
		t.Error("payload content mismatch")
	}
}

func TestDropResumption(t *testing.T) {
	// Create 128KB payload with 32KB chunk size
	chunkSize := 32 * 1024
	payload := bytes.Repeat([]byte("AuthBridgeQuickDropResumptionChunk"), 4000) // ~136KB
	totalSize := int64(len(payload))
	expectedHash := sha256.Sum256(payload)
	expectedSHA := hex.EncodeToString(expectedHash[:])

	// Pre-populate interim file with first 2 chunks (64KB)
	initialBytes := int64(2 * chunkSize)
	tmpFile, err := os.CreateTemp("", "quickdrop-resume-*.part")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := tmpFile.Write(payload[:initialBytes]); err != nil {
		t.Fatalf("failed to pre-write partial file: %v", err)
	}

	meta := drop.DropSend{
		Kind:      drop.DropKindFile,
		Name:      "large-resumed.bin",
		Size:      totalSize,
		ChunkSize: chunkSize,
	}

	res, sendErr, recvErr := runDropPair(
		t,
		meta,
		bytes.NewReader(payload),
		drop.SendDropConfig{},
		tmpFile,
		drop.ReceiveDropConfig{
			ReceivedBytes: initialBytes,
		},
	)

	if sendErr != nil {
		t.Fatalf("SendDrop failed: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop failed: %v", recvErr)
	}
	if res.BytesWritten != totalSize {
		t.Errorf("expected BytesWritten=%d, got %d", totalSize, res.BytesWritten)
	}
	if res.SHA256 != expectedSHA {
		t.Errorf("expected SHA256=%s, got %s", expectedSHA, res.SHA256)
	}

	// Verify entire file on disk
	diskContent, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to read resumed file from disk: %v", err)
	}
	if !bytes.Equal(diskContent, payload) {
		t.Errorf("resumed file content on disk mismatch: len=%d, expected=%d", len(diskContent), len(payload))
	}
}

func TestDropSHA256Mismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	var recvErr error
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

		var buf bytes.Buffer
		_, recvErr = drop.ReceiveDrop(ctx, conn, &buf, drop.ReceiveDropConfig{})
	}()

	go func() {
		defer wg.Done()
		conn, err := tr.Dial(listener.Addr().String())
		if err != nil {
			return
		}
		defer conn.Close()

		dropID := "test-mismatch-drop"
		// 1. Send DropSend
		_ = conn.Send(drop.TypeDropSend, drop.DropSend{
			DropID: dropID,
			Kind:   drop.DropKindFile,
			Name:   "bad-hash.txt",
			Size:   10,
		})

		// 2. Read Ack
		_, _ = conn.Receive()

		// 3. Send DropData with bad SHA256
		_ = conn.Send(drop.TypeDropData, drop.DropData{
			DropID:     dropID,
			ChunkIndex: 0,
			Data:       []byte("0123456789"),
			Final:      true,
			SHA256:     "bad0000000000000000000000000000000000000000000000000000000000000",
		})

		// 4. Read DropComplete
		_, _ = conn.Receive()
	}()

	wg.Wait()

	if recvErr == nil {
		t.Fatal("expected ReceiveDrop to fail with SHA-256 mismatch, got nil")
	}
	if !strings.Contains(recvErr.Error(), "SHA-256 checksum mismatch") {
		t.Errorf("expected error containing 'SHA-256 checksum mismatch', got: %v", recvErr)
	}
}


