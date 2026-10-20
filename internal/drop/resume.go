package drop

// The protocol's resume rule, in one place.
//
// Two receivers once answered the same question differently. The Hub resumed a
// partial it could not verify, and the standalone receiver restarted it, so
// the same transfer was accepted by one and refused by the other -- and the
// peer saw two different answers, because the resume offset travels in the
// acknowledgement.
//
// The rule belongs to the protocol, not to a receiver, so it lives here and
// both receivers call it. See docs/KNOWN-LIMITATIONS.md row 3.29.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
)

// HeadHashSize is the number of leading bytes the transfer's head hash
// covers.
//
// It is the size of the head a receiver compares against a partial's first
// bytes, and therefore the size a partial must hold before its head can be
// checked at all.
const HeadHashSize = 64 * 1024

// ResumePrefix returns the number of bytes of an existing partial that a
// receiver may resume, given the partial's size on disk and the transfer's
// metadata.
//
// The returned count is chunk-aligned, never exceeds the partial's size, and
// is zero when the transfer must start again from scratch.
//
//   - A resume needs the transfer's head hash. The head hash is what binds an
//     on-disk partial to the transfer it claims to be, and without it the
//     transfer restarts.
//   - The head hash covers the first HeadHashSize bytes, so it can only be
//     CHECKED when the partial holds at least that much. Below that the
//     prefix is resumed unverified, and the receiver's whole-payload digest
//     is the barrier -- the same rule for both receivers.
//   - A head that does not match is not resumed at all: the transfer
//     restarts from zero.
//
// r is used only to read and verify the head, and its position is restored
// before returning. A head that cannot be READ is treated exactly like a
// head that does not match: the transfer restarts. Restarting is always
// safe, and it is the same answer whichever receiver is asked.
func ResumePrefix(r io.ReadSeeker, partSize int64, meta DropSend) int64 {
	chunkSize := int64(meta.ChunkSize)
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	validBytes := (partSize / chunkSize) * chunkSize
	if meta.Size <= 0 || validBytes <= 0 || validBytes >= meta.Size || meta.HeadHash == "" {
		return 0
	}
	if validBytes < HeadHashSize {
		// There is not enough of the partial to compare a head against, so
		// there is nothing to check. The prefix is resumed and the
		// whole-payload digest decides the transfer.
		return validBytes
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0
	}
	head := make([]byte, HeadHashSize)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0
	}
	sum := sha256.Sum256(head)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), meta.HeadHash) {
		// The bytes on disk are not the bytes this transfer is about.
		return 0
	}
	return validBytes
}
