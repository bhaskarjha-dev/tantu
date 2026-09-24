//go:build unix

package hub

import (
	"os"
	"syscall"
)

// stagingHardlinkedOS reports whether fi has more than one hard link. A
// pre-planted hardlink to a victim file would pass the SameFile identity
// check (same inode), so multi-linked partials are never resumed into.
func stagingHardlinkedOS(fi os.FileInfo) bool {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Nlink > 1
	}
	return false
}
