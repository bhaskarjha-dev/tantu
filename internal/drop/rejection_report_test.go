package drop_test

// A receiver that refuses a transfer has told the sender exactly what
// happened. Reporting that refusal as "the file may already be saved" is a
// false alarm: it sends the user to check a receiver inbox for a file the
// receiver has just said it did not write, and it marks a clean, understood,
// retryable failure as unsafe to retry.
//
// These tests pin the distinction between the two cases that used to be
// conflated, and they reproduce the scenario observed on a real two-machine
// run: reusing an idempotency key with different content.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

// TestE2E_IdempotencyKeyConflictIsAKnownRejection is the regression test for
// the live misreport. Sending different content under a key that already
// completed is refused by the receiver; the sender must surface that as a
// rejection, not as an unknown outcome.
func TestE2E_IdempotencyKeyConflictIsAKnownRejection(t *testing.T) {
	store := drop.NewTombstones("")
	recvCfg := drop.ReceiveDropConfig{Tombstones: store}

	send := func(dropID, payload string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Metadata is held identical across attempts so the conflict is
		// decided on content, which is the case a live two-machine run
		// produced and the one that reaches the streaming path.
		meta := drop.DropSend{
			DropID:         dropID,
			IdempotencyKey: "op-key-conflict",
			Kind:           drop.DropKindText,
			Name:           "d.txt",
			Size:           64,
			ChunkSize:      drop.DefaultChunkSize,
		}
		var buf bytes.Buffer
		_, sendErr, recvErr := runE2EDrop(ctx, t, meta, strings.NewReader(payload),
			drop.SendDropConfig{}, &buf, recvCfg)
		// A refusal is the receiver behaving correctly, so it is expected
		// here; the assertion is on what the sender reports.
		_ = recvErr
		return sendErr
	}

	original := strings.Repeat("A", 64)
	if err := send("drop-key-a", original); err != nil {
		t.Fatalf("first delivery failed: %v", err)
	}

	// Same key, same metadata, different bytes. The receiver must refuse
	// rather than silently treat it as the earlier transfer.
	err := send("drop-key-b", strings.Repeat("B", 64))
	if err == nil {
		t.Fatal("a reused key with different content must be refused")
	}

	var rejection *drop.RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("want a *drop.RejectionError, got %T: %v", err, err)
	}
	if !strings.Contains(rejection.Reason, "idempotency key") {
		t.Errorf("the receiver's own reason must survive to the sender, got %q", rejection.Reason)
	}
	// The bytes were fully streamed, which is exactly why the old
	// byte-count heuristic mislabelled this as a possible duplicate.
	if rejection.BytesReceived == 0 {
		t.Error("the receiver should report how much it had taken when it refused")
	}
}

// TestE2E_MetadataConflictIsAlsoAKnownRejection covers the earlier refusal
// path: a key reused for different transfer metadata is rejected during
// negotiation, before any payload moves. It is a refusal for the same reason
// and must be reported the same way.
func TestE2E_MetadataConflictIsAlsoAKnownRejection(t *testing.T) {
	store := drop.NewTombstones("")
	recvCfg := drop.ReceiveDropConfig{Tombstones: store}

	send := func(dropID string, size int64, payload string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		meta := drop.DropSend{
			DropID:         dropID,
			IdempotencyKey: "op-meta-conflict",
			Kind:           drop.DropKindText,
			Name:           "d.txt",
			Size:           size,
			ChunkSize:      drop.DefaultChunkSize,
		}
		var buf bytes.Buffer
		_, sendErr, recvErr := runE2EDrop(ctx, t, meta, strings.NewReader(payload),
			drop.SendDropConfig{}, &buf, recvCfg)
		_ = recvErr
		return sendErr
	}

	if err := send("drop-meta-a", 32, strings.Repeat("A", 32)); err != nil {
		t.Fatalf("first delivery failed: %v", err)
	}
	// Same key, different declared size: refused during negotiation.
	if err := send("drop-meta-b", 48, strings.Repeat("B", 48)); err == nil {
		t.Fatal("a reused key with different metadata must be refused")
	}
}

// TestSendDropRejectionIsNotADropCompleteTransportError separates the two
// cases the sender must not confuse: a receiver that answered, and a
// connection that died before answering. Only the second is an unknown
// outcome.
func TestSendDropRejectionIsNotADropCompleteTransportError(t *testing.T) {
	rejection := &drop.RejectionError{Reason: "content does not match the recorded completion for this idempotency key"}

	// errors.Is must work on the type alone, so callers never have to match
	// on the receiver's wording.
	if !errors.Is(rejection, &drop.RejectionError{}) {
		t.Error("errors.Is should match any RejectionError by type")
	}
	// A transport error that merely mentions drop_complete must NOT match,
	// or every lost acknowledgement would be reported as a clean refusal.
	lost := errors.New("receive drop_complete: connection reset by peer")
	if errors.Is(lost, &drop.RejectionError{}) {
		t.Error("a lost acknowledgement must not be reported as a receiver rejection")
	}
	if got := rejection.Error(); !strings.Contains(got, "idempotency key") {
		t.Errorf("the reason must be preserved in the message, got %q", got)
	}
}

func TestRejectionErrorHandlesNilReceiver(t *testing.T) {
	var e *drop.RejectionError
	if got := e.Error(); got == "" {
		t.Error("a nil rejection must still render a message rather than panic")
	}
}
