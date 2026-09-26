package drop_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

type failingWriter struct {
	failAfter int
	written   int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.written+len(p) > w.failAfter {
		return 0, errors.New("no space left on device")
	}
	w.written += len(p)
	return len(p), nil
}

func TestE2E_ReceiverWriteFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	text := "this payload never fully lands on the receiver disk"
	meta := drop.DropSend{
		DropID: drop.NewDropID(),
		Kind:   drop.DropKindText,
		Name:   "disk.txt",
		Size:   int64(len(text)),
	}
	sink := &failingWriter{failAfter: 4}
	_, sendErr, recvErr := runE2EDrop(
		ctx, t, meta, strings.NewReader(text), drop.SendDropConfig{},
		sink, drop.ReceiveDropConfig{},
	)
	// The receiver reports the raw local error; the sender observes the
	// messaged failure relayed through the completion frame.
	if recvErr == nil || !strings.Contains(recvErr.Error(), "no space left on device") {
		t.Fatalf("expected the raw disk error locally, got %v", recvErr)
	}
	if sendErr == nil || !strings.Contains(sendErr.Error(), "write payload") {
		t.Fatalf("expected the sender to observe the write failure, got %v", sendErr)
	}
}
