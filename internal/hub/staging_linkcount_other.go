//go:build !unix && !windows

package hub

import (
	"fmt"
	"os"
)

// stagingHardlinkedOS fails closed on platforms that expose neither a Unix
// link count nor a Windows handle link count. Resuming an unverifiable
// partial is less safe than starting a fresh transfer.
func stagingHardlinkedOS(*os.File) (bool, error) {
	return false, fmt.Errorf("hardlink verification is unsupported on this platform")
}
