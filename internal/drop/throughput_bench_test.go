package drop_test

// Standing performance harness for QuickDrop transfers. Run with:
//
//	go test -bench . -benchtime 10x -run '^$' ./internal/drop/
//
// Baselines are recorded in docs/DEV-RECORD.md. The harness uses only the
// standard library and the loopback transport; it measures framing, chunking,
// disk staging, and SHA-256 verification end to end.

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func runBenchDrop(
	b *testing.B,
	ctx context.Context,
	meta drop.DropSend,
	payload []byte,
) {
	b.Helper()

	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		b.Fatalf("tr.Listen failed: %v", err)
	}
	defer listener.Close()

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
		var sink bytes.Buffer
		_, recvErr = drop.ReceiveDrop(ctx, conn, &sink, drop.ReceiveDropConfig{})
	}()

	go func() {
		defer wg.Done()
		conn, err := tr.Dial(listener.Addr().String())
		if err != nil {
			sendErr = err
			return
		}
		defer conn.Close()
		sendErr = drop.SendDrop(ctx, conn, meta, bytes.NewReader(payload), drop.SendDropConfig{})
	}()

	wg.Wait()
	if sendErr != nil {
		b.Fatalf("SendDrop error: %v", sendErr)
	}
	if recvErr != nil {
		b.Fatalf("ReceiveDrop error: %v", recvErr)
	}
}

// BenchmarkE2E_1MBFile measures a 1 MiB file transfer: framing, one 1 MiB
// data chunk, staging sync, and full SHA-256 verification on both ends.
func BenchmarkE2E_1MBFile(b *testing.B) {
	payload := bytes.Repeat([]byte{0xA5}, 1<<20)
	meta := drop.DropSend{
		DropID:    drop.NewDropID(),
		Kind:      drop.DropKindFile,
		Name:      "bench.bin",
		Size:      int64(len(payload)),
		ChunkSize: drop.DefaultChunkSize,
	}
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		runBenchDrop(b, ctx, meta, payload)
		cancel()
	}
}
