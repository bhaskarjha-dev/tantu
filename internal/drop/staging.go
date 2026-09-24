package drop

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StagingDirName is the per-output-directory staging area for partial files.
// All receivers (Hub and standalone) share this layout.
const StagingDirName = ".tantu-staging"

// DefaultStagingMaxAge bounds how long a failed transfer's partial file is
// retained for a same-DropID resume retry. Sender DropIDs are random per
// attempt, so most orphans are never resumed; without sweeping, repeated
// aborted multi-gigabyte transfers would fill the disk with .part files.
const DefaultStagingMaxAge = 24 * time.Hour

// SweepStalePartials removes *.part files under outDir/.tantu-staging whose
// modification time is older than maxAge (non-positive uses
// DefaultStagingMaxAge). It returns the number of files removed and the bytes
// reclaimed. A missing staging directory is not an error. Files that cannot
// be inspected or removed are skipped so one odd entry cannot abort the sweep.
func SweepStalePartials(outDir string, maxAge time.Duration) (removed int, freed int64) {
	if maxAge <= 0 {
		maxAge = DefaultStagingMaxAge
	}
	cutoff := time.Now().Add(-maxAge)
	stagingDir := filepath.Join(outDir, StagingDirName)
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return 0, 0
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		path := filepath.Join(stagingDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		freed += info.Size()
		if err := os.Remove(path); err == nil {
			removed++
		} else {
			freed -= info.Size()
		}
	}
	return removed, freed
}
