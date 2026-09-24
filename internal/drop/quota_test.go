package drop_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

func TestTransferQuotaZeroValueIsUsable(t *testing.T) {
	var quota drop.TransferQuota
	lease, err := quota.Acquire("peer", 1)
	if err != nil || lease == nil {
		t.Fatalf("zero-value quota acquire = %v, %v", lease, err)
	}
	lease.Release()
	if got := quota.ActiveTransfers(); got != 0 {
		t.Fatalf("zero-value quota retained transfer: %d", got)
	}
}

func TestTransferQuotaBoundsReservations(t *testing.T) {
	quota := drop.NewTransferQuota(drop.TransferQuotaConfig{
		MaxTransfers:        2,
		MaxBytes:            10,
		MaxPerPeerTransfers: 1,
		MaxPerPeerBytes:     6,
	})
	first, err := quota.Acquire("peer-a", 4)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := quota.Acquire("peer-a", 1); !errors.Is(err, drop.ErrTransferQuota) {
		t.Fatalf("same-peer second acquire error = %v, want quota error", err)
	}
	second, err := quota.Acquire("peer-b", 6)
	if err != nil {
		t.Fatalf("second peer acquire: %v", err)
	}
	if _, err := quota.Acquire("peer-c", 1); !errors.Is(err, drop.ErrTransferQuota) {
		t.Fatalf("global byte-limit acquire error = %v, want quota error", err)
	}
	first.Release()
	first.Release() // idempotent
	if got := quota.ActiveTransfers(); got != 1 {
		t.Fatalf("active transfers after release = %d, want 1", got)
	}
	second.Release()
	if got := quota.ActiveBytes(); got != 0 {
		t.Fatalf("active bytes after release = %d, want 0", got)
	}
}

func TestTransferQuotaUnknownSizeReservesBoundedBudget(t *testing.T) {
	quota := drop.NewTransferQuota(drop.TransferQuotaConfig{
		MaxTransfers:    1,
		MaxBytes:        drop.DefaultChunkSize + 6,
		MaxPerPeerBytes: drop.DefaultChunkSize + 6,
	})
	lease, err := quota.Acquire("peer", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := quota.ActiveBytes(); got != drop.DefaultChunkSize {
		t.Fatalf("initial unknown-size reservation = %d, want %d", got, drop.DefaultChunkSize)
	}
	if err := lease.AddBytes(6); err != nil {
		t.Fatal(err)
	}
	if got := quota.ActiveBytes(); got != drop.DefaultChunkSize+6 {
		t.Fatalf("grown unknown-size reservation = %d, want %d", got, drop.DefaultChunkSize+6)
	}
	if _, err := quota.Acquire("peer", 1); !errors.Is(err, drop.ErrTransferQuota) {
		t.Fatalf("second acquire error = %v, want quota error", err)
	}
	lease.Release()
}

func TestReceiveDropEnforcesTransferQuota(t *testing.T) {
	payload := []byte("quota")
	meta := drop.DropSend{
		DropID: "quota-rejected",
		Kind:   drop.DropKindFile,
		Name:   "quota.bin",
		Size:   int64(len(payload)),
	}
	quota := drop.NewTransferQuota(drop.TransferQuotaConfig{MaxBytes: 1})
	_, sendErr, recvErr := runDropPair(t, meta, bytes.NewReader(payload), drop.SendDropConfig{Timeout: time.Second}, &bytes.Buffer{}, drop.ReceiveDropConfig{
		Quota: quota,
	})
	if !errors.Is(recvErr, drop.ErrTransferQuota) {
		t.Fatalf("receiver error = %v, want transfer quota error", recvErr)
	}
	if sendErr == nil || !strings.Contains(sendErr.Error(), drop.ErrTransferQuota.Error()) {
		t.Fatalf("sender error = %v, want quota rejection", sendErr)
	}
}
