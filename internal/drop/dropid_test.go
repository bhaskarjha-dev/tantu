package drop_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

func TestNewDropID_UniqueAndValid(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		id := drop.NewDropID()
		if strings.TrimSpace(id) == "" {
			t.Fatal("NewDropID returned empty ID")
		}
		if len(id) > drop.MaxDropIDLength {
			t.Fatalf("DropID %q exceeds max length", id)
		}
		for _, r := range id {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				t.Fatalf("DropID %q contains invalid character %q", id, r)
			}
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate DropID %q in 100 samples", id)
		}
		seen[id] = struct{}{}
	}
}

func TestE2E_IdempotencyKeyRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	text := "idempotency key must survive the wire"
	meta := drop.DropSend{
		DropID:         drop.NewDropID(),
		IdempotencyKey: "op-abc123",
		Kind:           drop.DropKindText,
		Name:           "key.txt",
		Size:           int64(len(text)),
	}
	var buf bytes.Buffer
	res, sendErr, recvErr := runE2EDrop(
		ctx, t, meta, strings.NewReader(text), drop.SendDropConfig{},
		&buf, drop.ReceiveDropConfig{},
	)
	if sendErr != nil {
		t.Fatalf("SendDrop error: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop error: %v", recvErr)
	}
	if res.Meta.IdempotencyKey != "op-abc123" {
		t.Errorf("received key = %q, want %q", res.Meta.IdempotencyKey, "op-abc123")
	}
}

func TestE2E_IdempotencyKeyRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	meta := drop.DropSend{
		DropID:         drop.NewDropID(),
		IdempotencyKey: "../evil",
		Kind:           drop.DropKindText,
		Name:           "key.txt",
		Size:           3,
	}
	var buf bytes.Buffer
	_, sendErr, _ := runE2EDrop(
		ctx, t, meta, strings.NewReader("hey"), drop.SendDropConfig{},
		&buf, drop.ReceiveDropConfig{},
	)
	if sendErr == nil || !strings.Contains(sendErr.Error(), "idempotency") {
		t.Fatalf("expected an idempotency rejection, got %v", sendErr)
	}
}
