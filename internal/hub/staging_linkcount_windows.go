//go:build windows

package hub

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// stagingHardlinkedOS queries the open Windows handle so a path replacement
// cannot change the object whose link count is checked.
func stagingHardlinkedOS(f *os.File) (bool, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return false, fmt.Errorf("query partial link count: %w", err)
	}
	return info.NumberOfLinks > 1, nil
}
