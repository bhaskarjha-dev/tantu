package drop

// The protocol's resume rule, in one place.
//
// Two receivers once answered the same question differently. The Hub resumed a
// partial it could not verify, and the standalone receiver restarted it, so
// the same transfer was accepted by one and refused by the other -- and the
// peer saw two different answers, because the resume offset travels in the
// acknowledgement.
//
// See docs/KNOWN-LIMITATIONS.md row 3.29.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestResumePrefix(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 8192) // 128 KiB
	head := sha256.Sum256(payload[:HeadHashSize])
	headHex := hex.EncodeToString(head[:])

	cases := []struct {
		name     string
		partSize int64
		meta     DropSend
		onDisk   []byte
		want     int64
		why      string
	}{
		{
			name:     "a partial with no head hash is not resumed",
			partSize: 32 * 1024,
			meta:     DropSend{Size: int64(len(payload)), ChunkSize: 4096},
			onDisk:   payload[:32*1024],
			want:     0,
			why:      "the head hash is what binds a partial to the transfer, and without it the transfer restarts",
		},
		{
			name:     "a partial smaller than one chunk is not resumed",
			partSize: 100,
			meta:     DropSend{Size: int64(len(payload)), ChunkSize: 4096, HeadHash: headHex},
			onDisk:   payload[:100],
			want:     0,
			why:      "the resume offset travels in the acknowledgement and is chunk-aligned",
		},
		{
			name:     "a complete partial is not resumed",
			partSize: int64(len(payload)),
			meta:     DropSend{Size: int64(len(payload)), ChunkSize: 4096, HeadHash: headHex},
			onDisk:   payload,
			want:     0,
			why:      "there is nothing left to send",
		},
		{
			name:     "a partial below the head-check window is resumed unverified",
			partSize: 32 * 1024,
			meta:     DropSend{Size: int64(len(payload)), ChunkSize: 32 * 1024, HeadHash: headHex},
			onDisk:   payload[:32*1024],
			want:     32 * 1024,
			why: "the head hash covers 64 KiB, so there is nothing to compare yet; the " +
				"whole-payload digest is the barrier, and this is the answer the Hub gave",
		},
		{
			name:     "a partial at the head-check window with a matching head is resumed",
			partSize: 64 * 1024,
			meta:     DropSend{Size: int64(len(payload)), ChunkSize: 32 * 1024, HeadHash: headHex},
			onDisk:   payload[:64*1024],
			want:     64 * 1024,
			why:      "the head matches the transfer it claims to be",
		},
		{
			name:     "a partial at the head-check window with a mismatched head is not resumed",
			partSize: 64 * 1024,
			meta:     DropSend{Size: int64(len(payload)), ChunkSize: 32 * 1024, HeadHash: headHex},
			onDisk:   bytes.Repeat([]byte{0xA5}, 64*1024),
			want:     0,
			why:      "the bytes on disk are not the bytes this transfer is about",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "part.bin")
			if err := os.WriteFile(path, tc.onDisk, 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(path, os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()

			got := ResumePrefix(f, tc.partSize, tc.meta)
			if got != tc.want {
				t.Errorf("ResumePrefix = %d, want %d: %s", got, tc.want, tc.why)
			}
		})
	}
}

// The rule is the same for every receiver, and it is exercised from the
// receiver that used to differ.
//
// The standalone receiver required a 64 KiB partial before it would resume
// anything, so a smaller one restarted from zero, while the Hub resumed it.
// The peer saw the two different offsets in the acknowledgement.
func TestResumePrefixIsTheSameRuleForEveryReceiver(t *testing.T) {
	// The values the two implementations used to disagree on, asserted
	// together: the standalone's extra 64 KiB requirement is gone, and the
	// head check is still applied whenever there is enough of the partial to
	// apply it.
	payload := bytes.Repeat([]byte("0123456789abcdef"), 8192)
	head := sha256.Sum256(payload[:HeadHashSize])
	headHex := hex.EncodeToString(head[:])
	meta := DropSend{Size: int64(len(payload)), ChunkSize: 32 * 1024, HeadHash: headHex}

	dir := t.TempDir()
	path := filepath.Join(dir, "part.bin")
	if err := os.WriteFile(path, payload[:32*1024], 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if got := ResumePrefix(f, 32*1024, meta); got != 32*1024 {
		t.Errorf("ResumePrefix = %d for a 32 KiB partial, want 32768: the standalone receiver "+
			"restarted here while the Hub resumed, and the peer saw both answers", got)
	}
}
