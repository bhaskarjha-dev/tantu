package drop

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// VerifyStagedDigest re-reads the staged bytes through the receiver's own open
// descriptor and fails unless they hash to expectedSHA. The file is rewound
// before it returns, so a caller that then falls back to a copy needs no extra
// seek. An empty expectedSHA verifies nothing and reports success, which is how
// the copy path already treats a transfer that declared no digest.
//
// Publication needs this because its preferred path is a rename. A rename moves
// bytes without reading them, so without this check the digest reported to the
// sender would be the one computed while streaming, *assumed* still to describe
// the file at the moment of publication - and that assumption would hold only
// on the platforms where the rename path happens not to be taken. Verification
// belongs before publication, not after: a file must never appear under its
// delivered name with unverified content, not even for the instant between a
// rename and a check that then has to undo it.
//
// The cost is one sequential read of bytes that were just written, and no
// write: peak disk stays at N, which is the property the rename exists to
// preserve.
func VerifyStagedDigest(source *os.File, expectedSHA string) error {
	if source == nil {
		return errors.New("missing open staging file")
	}
	if expectedSHA == "" {
		return nil
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind staging file for verification: %w", err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, source); err != nil {
		_, _ = source.Seek(0, io.SeekStart)
		return fmt.Errorf("read staging file for verification: %w", err)
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind staging file after verification: %w", err)
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); !strings.EqualFold(got, expectedSHA) {
		return fmt.Errorf("staged content digest %s does not match verified digest %s", got, expectedSHA)
	}
	return nil
}
