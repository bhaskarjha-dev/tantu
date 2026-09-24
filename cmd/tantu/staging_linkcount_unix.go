//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// standalonePartHardlinkedOS reports whether the open partial has more than
// one hard link. A pre-planted hardlink to a victim file would otherwise pass
// the regular-file and SameFile checks used during standalone resume.
func standalonePartHardlinkedOS(f *os.File) (bool, error) {
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
