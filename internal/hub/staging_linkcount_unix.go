//go:build unix

package hub

import (
	"fmt"
	"os"
	"syscall"
)

// stagingHardlinkedOS reports whether the open partial has more than one hard
// link. A pre-planted hardlink to a victim file would pass the SameFile
// identity check (same inode), so multi-linked partials are never resumed
// into. The open handle is inspected so a path swap cannot change the object
// being checked after the initial identity validation.
func stagingHardlinkedOS(f *os.File) (bool, error) {
	info, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("stat partial handle: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("partial handle does not expose link count")
	}
	return st.Nlink > 1, nil
}
