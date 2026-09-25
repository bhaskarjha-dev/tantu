package drop_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

func TestTombstones_BoundAndAgePrune(t *testing.T) {
	store := drop.NewTombstones("")
	old := drop.TombstoneEntry{Key: "old", SHA256: "abc", CompletedAt: time.Now().Add(-25 * time.Hour)}
	store.Record(old)
	if got := store.Count(); got != 0 {
		t.Fatalf("expired entry retained: count = %d", got)
	}
	for i := 0; i < 1030; i++ {
		store.Record(drop.TombstoneEntry{
			Key:         strings.Repeat("k", 4) + string(rune('a'+i/26)) + string(rune('a'+i%26)),
			SHA256:      "abc",
			CompletedAt: time.Now(),
		})
	}
	if got := store.Count(); got != 1024 {
		t.Fatalf("count = %d, want bound 1024", got)
	}
}

func TestE2E_TombstoneSuppressesRepublish(t *testing.T) {
	store := drop.NewTombstones("")
	var beforeCompleteCalls int
	recvCfg := drop.ReceiveDropConfig{
		Tombstones: store,
		BeforeComplete: func(*drop.ReceiveDropResult) error {
			beforeCompleteCalls++
			return nil
		},
	}
	payload := "tombstone suppresses the second publication"
	send := func(dropID string) (*drop.ReceiveDropResult, error, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		meta := drop.DropSend{
			DropID:         dropID,
			IdempotencyKey: "op-tomb-1",
			Kind:           drop.DropKindText,
			Name:           "t.txt",
			Size:           int64(len(payload)),
		}
		var buf bytes.Buffer
		return runE2EDrop(ctx, t, meta, strings.NewReader(payload), drop.SendDropConfig{}, &buf, recvCfg)
	}

	first, sendErr, recvErr := send("drop-first")
	if sendErr != nil || recvErr != nil || first == nil {
		t.Fatalf("first delivery failed: send=%v recv=%v", sendErr, recvErr)
	}
	if first.Duplicate {
		t.Fatal("first delivery must not report Duplicate")
	}
	second, sendErr, recvErr := send("drop-second")
	if sendErr != nil || recvErr != nil || second == nil {
		t.Fatalf("redelivery failed: send=%v recv=%v", sendErr, recvErr)
	}
	if !second.Duplicate {
		t.Fatal("redelivery with the same key must report Duplicate")
	}
	if second.SHA256 != first.SHA256 {
		t.Fatalf("redelivery digest %q != original %q", second.SHA256, first.SHA256)
	}
	if beforeCompleteCalls != 1 {
		t.Fatalf("BeforeComplete ran %d times, want exactly 1 (no second publication)", beforeCompleteCalls)
	}
	if got := store.Count(); got != 1 {
		t.Fatalf("store count = %d, want 1", got)
	}
}

func TestE2E_TombstoneMismatchFailsClosed(t *testing.T) {
	store := drop.NewTombstones("")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var buf bytes.Buffer
	if _, sendErr, recvErr := runE2EDrop(ctx, t, drop.DropSend{DropID: "d1", IdempotencyKey: "op-tomb-2", Kind: drop.DropKindText, Name: "a.txt", Size: 3}, strings.NewReader("hey"), drop.SendDropConfig{}, &buf, drop.ReceiveDropConfig{Tombstones: store}); sendErr != nil || recvErr != nil {
		t.Fatalf("first delivery failed: send=%v recv=%v", sendErr, recvErr)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	var buf2 bytes.Buffer
	_, sendErr, _ := runE2EDrop(ctx2, t, drop.DropSend{DropID: "d2", IdempotencyKey: "op-tomb-2", Kind: drop.DropKindText, Name: "b.txt", Size: 3}, strings.NewReader("hey"), drop.SendDropConfig{}, &buf2, drop.ReceiveDropConfig{Tombstones: store})
	if sendErr == nil || !strings.Contains(sendErr.Error(), "idempotency key reuse") {
		t.Fatalf("expected an idempotency-mismatch rejection, got %v", sendErr)
	}
}
