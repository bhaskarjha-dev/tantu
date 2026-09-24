//go:build !unix && !windows

package main

import (
	"fmt"
	"os"
)

// standalonePartHardlinkedOS fails closed on platforms that expose neither a
// Unix link count nor a Windows handle link count. Starting fresh is safer
// than resuming an unverifiable partial.
func standalonePartHardlinkedOS(*os.File) (bool, error) {
	return false, fmt.Errorf("hardlink verification is unsupported on this platform")
}
