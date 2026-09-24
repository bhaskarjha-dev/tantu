//go:build !unix

package hub

import "os"

// stagingHardlinkedOS is a no-op where FileInfo hides the link count
// (notably Windows): openResumePart still enforces the regular-file and
// SameFile identity checks, but cannot detect multi-linked files there.
func stagingHardlinkedOS(fi os.FileInfo) bool {
	return false
}
