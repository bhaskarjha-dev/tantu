package drop_test

// The digest contract at the library boundary.
//
// The receiver hashes the bytes it writes and never re-reads the destination,
// so a destination longer than the payload -- a stale tail, a pre-existing
// file -- yields a digest that matches the sender's while the file on disk
// hashes differently.
//
// The product is protected: both publication paths re-read the staged bytes
// and compare against the whole-file digest, so a stale tail is refused and
// nothing is published. The hazard was at the library boundary, where the
// result's digest was called "Verified SHA-256 hex digest" and a caller could
// read it as a claim about its own file.
//
// So the receiver now refuses a destination it can see is too long, and the
// result's field is named for what it is. See
// docs/KNOWN-LIMITATIONS.md row 3.28.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

func TestReceiveRefusesADestinationLongerThanThePayload(t *testing.T) {
	payload := []byte("short")
	meta := drop.DropSend{
		Kind: drop.DropKindFile,
		Name: "stale-tail.bin",
		Size: int64(len(payload)),
	}

	// A destination carrying a hundred stale bytes, with the writer at the
	// start: the stale bytes are a TAIL, which is exactly the case where the
	// bytes written and the bytes on disk differ.
	destPath := filepath.Join(t.TempDir(), "stale-tail.bin")
	if err := os.WriteFile(destPath, bytes.Repeat([]byte("S"), 100), 0o600); err != nil {
		t.Fatalf("write the stale destination: %v", err)
	}
	dest, err := os.OpenFile(destPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open the stale destination: %v", err)
	}
	defer dest.Close()

	res, _, recvErr := runDropPair(
		t,
		meta,
		bytes.NewReader(payload),
		drop.SendDropConfig{},
		dest,
		drop.ReceiveDropConfig{},
	)

	if recvErr == nil {
		t.Fatalf("the receive reported success (%+v) into a destination that is longer than the "+
			"payload: the digest it returns is the sender's, and the file on disk hashes "+
			"differently", res)
	}

	// And the stale bytes must still be there: the refusal happened before
	// anything was written, so the destination is untouched.
	onDisk, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read the destination back: %v", err)
	}
	if !bytes.Equal(onDisk, bytes.Repeat([]byte("S"), 100)) {
		t.Errorf("the destination was modified by a refused receive: %d bytes", len(onDisk))
	}
}
