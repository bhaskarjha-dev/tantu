package drop_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

// A receiver tombstone hit must be visible to the sender. The wire reply is
// still a success, so without the Duplicate bit a sender reports a fresh
// verified copy when nothing new was written on the peer.
func TestE2E_SuppressedDuplicateReachesTheSender(t *testing.T) {
	store := drop.NewTombstones("")
	recvCfg := drop.ReceiveDropConfig{Tombstones: store}
	payload := "tombstone suppression must be visible to the sender"

	send := func(dropID string) (drop.DropComplete, error, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		meta := drop.DropSend{
			DropID:         dropID,
			IdempotencyKey: "op-dup-visible",
			Kind:           drop.DropKindText,
			Name:           "d.txt",
			Size:           int64(len(payload)),
		}
		var got drop.DropComplete
		var buf bytes.Buffer
		_, sendErr, recvErr := runE2EDrop(ctx, t, meta, strings.NewReader(payload),
			drop.SendDropConfig{OnComplete: func(c drop.DropComplete) { got = c }}, &buf, recvCfg)
		return got, sendErr, recvErr
	}

	first, sendErr, recvErr := send("drop-dup-a")
	if sendErr != nil || recvErr != nil {
		t.Fatalf("first delivery failed: send=%v recv=%v", sendErr, recvErr)
	}
	if first.Duplicate {
		t.Fatal("a first delivery must never be reported as a duplicate")
	}
	if !first.Success {
		t.Fatalf("first delivery must succeed, got %+v", first)
	}

	second, sendErr, recvErr := send("drop-dup-b")
	if sendErr != nil || recvErr != nil {
		t.Fatalf("redelivery failed: send=%v recv=%v", sendErr, recvErr)
	}
	if !second.Duplicate {
		t.Fatal("a tombstoned redelivery must set the Duplicate bit the sender reads")
	}
	// A suppressed duplicate is a success, not a failure: the operation did not
	// fail and the file is on the peer.
	if !second.Success {
		t.Fatalf("a suppressed duplicate must still report success, got %+v", second)
	}
	if second.SHA256 != first.SHA256 {
		t.Fatalf("a suppressed duplicate must re-acknowledge the recorded digest, got %q want %q", second.SHA256, first.SHA256)
	}
}
