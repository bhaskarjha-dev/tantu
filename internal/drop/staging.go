package drop

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StagingDirName is the per-output-directory staging area for partial files.
// All receivers (Hub and standalone) share this layout.
const StagingDirName = ".tantu-staging"

// stagingProcessLockHeartbeat is the file a lock holder rewrites to record
// that it is still alive, so a concurrent waiter can tell a slow-but-running
// holder from a crashed one instead of guessing from a fixed timeout.
const stagingProcessLockHeartbeat = "heartbeat"

// lockHolderAlive is platform-specific: it lives with the pid-heartbeat reader
// in staging_lockholder_{windows,other}.go, because a liveness probe is
// implemented differently on Windows, where os.Process.Signal is not available.

// DefaultStagingMaxAge bounds how long a failed transfer's partial file is
// retained for a same-DropID resume retry. Sender DropIDs are random per
// attempt, so most orphans are never resumed; without sweeping, repeated
// aborted multi-gigabyte transfers would fill the disk with .part files.
const DefaultStagingMaxAge = 24 * time.Hour

// DefaultRetainedPartialBudget bounds the bytes retained by failed transfers
// after their active transfer reservation has been released. It is separate
// from DefaultTransferQuota: active writes and resumable partials otherwise
// have different lifetimes. Receivers invoke EnforcePartialBudget after a
// failed receive and at startup.
const DefaultRetainedPartialBudget int64 = 8 * 1024 * 1024 * 1024

// DefaultRetainedPartialCount prevents a peer from exhausting inodes with
// many tiny failed partials even when their logical byte sizes are small.
const DefaultRetainedPartialCount = 1024

var stagingMaintenanceMu sync.Mutex

// ErrStagingPartActive indicates that another receiver currently owns a
// partial. Activity markers are intentionally conservative across processes;
// a crashed owner becomes reclaimable only after DefaultStagingMaxAge.
var ErrStagingPartActive = errors.New("staging partial is active")

const (
	stagingActivitySuffix   = ".active"
	stagingProcessLockName  = ".tantu-staging.process.lock"
	stagingProcessLockWait  = 2 * time.Second
	stagingProcessLockStale = 30 * time.Second
)

type partialCandidate struct {
	path     string
	info     os.FileInfo
	artifact bool
	staging  bool
}

// SweepStalePartials removes stale partials under outDir/.tantu-staging and
// manifest-marked legacy partials directly under outDir. Unmarked direct
// .part/.part-N files are left untouched because they cannot be distinguished
// safely from user files in a shared output directory. Non-positive maxAge
// uses DefaultStagingMaxAge. A missing directory is not an error. Files that
// cannot be inspected or removed are skipped so one odd entry cannot abort the
// sweep.
func isRealStagingOutputDir(outDir string) bool {
	info, err := os.Lstat(outDir)
	return err == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir()
}

func SweepStalePartials(outDir string, maxAge time.Duration) (removed int, freed int64) {
	if !isRealStagingOutputDir(outDir) {
		return 0, 0
	}
	stagingMaintenanceMu.Lock()
	defer stagingMaintenanceMu.Unlock()
	unlockProcess, err := acquireStagingProcessLock(stagingProcessLockPathForOutput(outDir))
	if err != nil {
		// Reclamation exists to bound disk, so failing to take the maintenance
		// lock must not be indistinguishable from "nothing to reclaim". Record
		// it for LastMaintenanceError and let the caller log it; returning
		// (0, 0) silently meant a crashed receiver's stale lock looked like a
		// clean sweep for as long as that lock existed.
		recordMaintenanceError(err)
		return 0, 0
	}
	defer unlockProcess()
	return sweepStalePartials(outDir, maxAge)
}

// lastMaintenanceErr records the most recent reason staging maintenance could
// not run. It is only ever written from inside the stagingMaintenanceMu
// critical section that every maintenance entry point already holds, so no
// additional locking is taken here - re-locking it would deadlock.
var lastMaintenanceErr error

// recordMaintenanceError is called with stagingMaintenanceMu already held.
func recordMaintenanceError(err error) {
	lastMaintenanceErr = err
}

// LastMaintenanceError returns why the most recent staging maintenance pass
// could not run, or nil if it ran or never attempted. Callers that log only
// when files were removed must consult this: a silent (0, 0) result is
// ambiguous between "nothing to do" and "reclamation is switched off".
func LastMaintenanceError() error {
	stagingMaintenanceMu.Lock()
	defer stagingMaintenanceMu.Unlock()
	return lastMaintenanceErr
}

// LockStagingMaintenance serializes partial creation/registration with the
// maintenance scans performed by this process. Callers must release the
// returned function before invoking SweepStalePartials or
// EnforcePartialBudget, which acquire the same lock themselves.
func LockStagingMaintenance() func() {
	stagingMaintenanceMu.Lock()
	return stagingMaintenanceMu.Unlock
}

func stagingProcessLockPathForOutput(outDir string) string {
	return filepath.Join(outDir, stagingProcessLockName)
}

func stagingOutputDirForPart(partPath string) string {
	dir := filepath.Dir(partPath)
	if filepath.Base(dir) == StagingDirName {
		return filepath.Dir(dir)
	}
	return dir
}

func stagingProcessLockPathForPart(partPath string) string {
	return stagingProcessLockPathForOutput(stagingOutputDirForPart(partPath))
}

// StagingDirectory is a verified directory handle for the private staging
// area. os.Root keeps operations anchored to the opened directory if a
// pathname is renamed or replaced, and rejects paths that escape the root.
type StagingDirectory struct {
	root *os.Root
	path string
}

func OpenStagingDirectory(path string) (*StagingDirectory, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("staging path is not a directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if !os.SameFile(info, rootInfo) {
		_ = root.Close()
		return nil, errors.New("staging directory changed during open")
	}
	return &StagingDirectory{root: root, path: path}, nil
}

func (d *StagingDirectory) Close() error {
	if d == nil || d.root == nil {
		return nil
	}
	return d.root.Close()
}

func (d *StagingDirectory) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

func (d *StagingDirectory) OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	return d.root.OpenFile(name, flag, perm)
}

func (d *StagingDirectory) Lstat(name string) (os.FileInfo, error) {
	return d.root.Lstat(name)
}

func (d *StagingDirectory) Remove(name string) error {
	return d.root.Remove(name)
}

func (d *StagingDirectory) Chtimes(name string, atime, mtime time.Time) error {
	return d.root.Chtimes(name, atime, mtime)
}

func (d *StagingDirectory) Rename(oldName, newName string) error {
	return d.root.Rename(oldName, newName)
}

func (d *StagingDirectory) ReadDir() ([]os.DirEntry, error) {
	dir, err := d.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.ReadDir(-1)
}

// publicationTempPrefix marks an in-progress publication inside the destination
// directory. A reclaimable temporary must match the full shape
// prefix + 24 hex characters + ".tmp", so a delivered file is never mistaken
// for one and a sweep can only ever remove something this code created.
const publicationTempPrefix = ".tantu-publish-"

const publicationTempSuffix = ".tmp"

// isPublicationTempName reports whether name is a publication temporary this
// package generated. The hex segment is required so no user-supplied filename,
// however it was sanitized, can be reclaimed by a sweep.
func isPublicationTempName(name string) bool {
	if !strings.HasPrefix(name, publicationTempPrefix) || !strings.HasSuffix(name, publicationTempSuffix) {
		return false
	}
	hexPart := strings.TrimSuffix(strings.TrimPrefix(name, publicationTempPrefix), publicationTempSuffix)
	if len(hexPart) != 24 {
		return false
	}
	for _, r := range hexPart {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// NewPublicationTempName returns a unique hidden temporary name in the same
// directory used for atomic publication. The caller renames it to the final
// name once the bytes are durable; until then the partial content is never
// visible under a name the user would open.
func (d *StagingDirectory) NewPublicationTempName() (string, error) {
	for i := 0; i < 64; i++ {
		raw := make([]byte, 12)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("generate publication temp name: %w", err)
		}
		name := publicationTempPrefix + hex.EncodeToString(raw) + publicationTempSuffix
		if _, err := d.Lstat(name); err == nil {
			continue // collision, try again
		} else if !os.IsNotExist(err) {
			return "", err
		}
		return name, nil
	}
	return "", errors.New("could not allocate a unique publication temp name")
}

// PublishStagedFile atomically moves a fully-written publication temporary into
// its final name. Renaming within one directory is atomic on every supported
// platform, so a reader sees either the previous state or the complete file,
// never a partially written one.
func (d *StagingDirectory) PublishStagedFile(tempName, finalName string) error {
	if !isPublicationTempName(tempName) {
		return errors.New("publication temp name is not recognized")
	}
	return d.Rename(tempName, finalName)
}

// PublishFromStaging renames an existing staging partial directly to its final
// name. Because the private staging area lives inside the output directory, this
// is a same-filesystem rename: it is atomic, costs no I/O proportional to the
// payload, and keeps peak disk at N rather than 2N. The published bytes are the
// very bytes that were hashed during the transfer, so no second copy can
// diverge from the digest the receiver verified.
//
// It reports ok=false when partPath lies on a different filesystem (or outside
// this root), in which case the caller falls back to a verified copy.
func (d *StagingDirectory) PublishFromStaging(partPath, finalName string) (ok bool, err error) {
	rel, relErr := filepath.Rel(d.path, partPath)
	if relErr != nil || rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
		return false, nil
	}
	// The final name must not already exist: publication never overwrites.
	if _, statErr := d.Lstat(finalName); statErr == nil {
		return false, os.ErrExist
	} else if !os.IsNotExist(statErr) {
		return false, statErr
	}
	if err := d.Rename(rel, finalName); err != nil {
		if os.IsExist(err) {
			return false, os.ErrExist
		}
		// A cross-device or otherwise unsupported rename falls back to a copy.
		return false, nil
	}
	return true, nil
}

// SweepPublicationTempsIn reclaims publication temporaries left in an output
// directory by an interrupted publication.
func SweepPublicationTempsIn(outDir string, maxAge time.Duration) (int, error) {
	if !isRealStagingOutputDir(outDir) {
		return 0, nil
	}
	dir, err := OpenStagingDirectory(outDir)
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	return dir.SweepPublicationTemps(maxAge)
}

// SweepPublicationTemps removes publication temporaries left behind by a crash
// mid-copy. They are never user-visible under a final name, so reclaiming them
// cannot destroy a delivered file.
func (d *StagingDirectory) SweepPublicationTemps(maxAge time.Duration) (int, error) {
	entries, err := d.ReadDir()
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if !isPublicationTempName(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := d.Remove(name); err == nil {
			removed++
		}
	}
	return removed, nil
}

// OpenStagingFile opens a file relative to a verified private staging root.
// The directory handle is closed after open; the returned file descriptor is
// still anchored to the directory that was verified.
func OpenStagingFile(partPath string, flag int, perm os.FileMode) (*os.File, error) {
	dir, partName, rooted, err := openManifestDirectory(partPath)
	if err != nil {
		return nil, err
	}
	if !rooted {
		return os.OpenFile(partPath, flag, perm)
	}
	defer dir.Close()
	return dir.OpenFile(partName, flag, perm)
}

func LstatStagingFile(partPath string) (os.FileInfo, error) {
	dir, partName, rooted, err := openManifestDirectory(partPath)
	if err != nil {
		return nil, err
	}
	if !rooted {
		return os.Lstat(partPath)
	}
	defer dir.Close()
	return dir.Lstat(partName)
}

func RemoveStagingFile(partPath string) error {
	dir, partName, rooted, err := openManifestDirectory(partPath)
	if err != nil {
		return err
	}
	if !rooted {
		return os.Remove(partPath)
	}
	defer dir.Close()
	return dir.Remove(partName)
}

// RemoveStagingFileIfSame removes a private partial only when the path still
// names the same regular file observed by the caller. It is used after
// publication so a path replacement cannot make cleanup remove a different
// file.
func RemoveStagingFileIfSame(partPath string, expected os.FileInfo) error {
	dir, partName, rooted, err := openManifestDirectory(partPath)
	if err != nil {
		return err
	}
	if !rooted {
		current, statErr := os.Lstat(partPath)
		if statErr != nil {
			return statErr
		}
		if !current.Mode().IsRegular() || !os.SameFile(expected, current) {
			return errors.New("staging file identity changed")
		}
		return os.Remove(partPath)
	}
	defer dir.Close()
	current, statErr := dir.Lstat(partName)
	if statErr != nil {
		return statErr
	}
	if !current.Mode().IsRegular() || !os.SameFile(expected, current) {
		return errors.New("staging file identity changed")
	}
	return dir.Remove(partName)
}

// acquireStagingProcessLock coordinates maintenance and activity-marker
// transitions across independent receiver processes. It is deliberately a
// sibling of .tantu-staging so a staging-directory replacement cannot redirect
// the lock itself.
func acquireStagingProcessLock(lockPath string) (func(), error) {
	deadline := time.Now().Add(stagingProcessLockWait)
	for {
		err := os.Mkdir(lockPath, 0700)
		if err == nil {
			// Write a heartbeat immediately so a concurrent waiter can tell this
			// holder is alive from the moment the lock exists.
			_ = touchStagingProcessLock(lockPath)
			var once sync.Once
			return func() {
				once.Do(func() { _ = os.RemoveAll(lockPath) })
			}, nil
		}
		if !os.IsExist(err) {
			if !os.IsPermission(err) {
				return nil, fmt.Errorf("acquire staging process lock: %w", err)
			}
			if _, statErr := os.Lstat(lockPath); statErr != nil {
				return nil, fmt.Errorf("acquire staging process lock: %w", err)
			}
		}
		if info, statErr := os.Lstat(lockPath); statErr == nil && info.IsDir() && time.Since(info.ModTime()) > stagingProcessLockStale {
			// The lock directory's mtime is set at Mkdir and never refreshed, so
			// a holder doing legitimate work longer than the stale window had its
			// lock stolen, and the original owner's release then removed the
			// thief's lock. The holder now refreshes a heartbeat carrying its pid
			// on every poll, so a lock is reclaimed only when its owner is
			// genuinely gone - which is the case that otherwise wedged staging
			// maintenance permanently.
			if pid, known := lockHolderPID(lockPath); known && lockHolderAlive(pid) {
				// The owner is alive: keep waiting rather than stealing the lock.
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("staging process lock timeout: held by a live process")
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}
			// Either the owner is gone, or it cannot be identified. Reclaiming is
			// strictly better than blocking maintenance forever; a lock that was
			// merely slow is refreshed, so it will not look this old while alive.
			_ = os.RemoveAll(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("staging process lock timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// touchStagingProcessLock refreshes a held lock and records the owning process
// id, so a concurrent waiter can distinguish a slow-but-running holder from one
// that died holding the lock. The heartbeat carries the pid rather than just a
// timestamp: refreshing alone would make a crashed holder indistinguishable
// from a live one, and the lock would then never be reclaimed.
func touchStagingProcessLock(lockPath string) error {
	marker := filepath.Join(lockPath, stagingProcessLockHeartbeat)
	if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return err
	}
	// Refresh the directory mtime explicitly for filesystems that do not update
	// it for a file that already exists.
	now := time.Now()
	return os.Chtimes(lockPath, now, now)
}

// AcquireStagingActivity creates a cross-process activity marker for a
// partial. Maintenance skips marked files, so a second receiver cannot unlink
// an active transfer merely because it shares an output directory. The
// marker is reclaimed after DefaultStagingMaxAge if the owner crashed.
func AcquireStagingActivity(partPath string) (func(), error) {
	activityName := filepath.Base(partPath) + stagingActivitySuffix
	stagingPath := filepath.Dir(partPath)
	if !isRealStagingOutputDir(stagingOutputDirForPart(partPath)) {
		return nil, errors.New("staging output directory is not a real directory")
	}
	unlockProcess, err := acquireStagingProcessLock(stagingProcessLockPathForPart(partPath))
	if err != nil {
		return nil, err
	}
	defer unlockProcess()
	dir, err := OpenStagingDirectory(stagingPath)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	for attempt := 0; attempt < 2; attempt++ {
		f, err := dir.OpenFile(activityName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			if closeErr := f.Close(); closeErr != nil {
				_ = dir.Remove(activityName)
				return nil, closeErr
			}
			ownedInfo, statErr := dir.Lstat(activityName)
			if statErr != nil || ownedInfo.Mode()&os.ModeSymlink != 0 || !ownedInfo.Mode().IsRegular() {
				_ = dir.Remove(activityName)
				if statErr == nil {
					statErr = errors.New("activity marker is not a regular file")
				}
				return nil, statErr
			}
			var once sync.Once
			return func() {
				once.Do(func() {
					unlock, lockErr := acquireStagingProcessLock(stagingProcessLockPathForPart(partPath))
					if lockErr != nil {
						return
					}
					defer unlock()
					currentDir, openErr := OpenStagingDirectory(stagingPath)
					if openErr != nil {
						return
					}
					defer currentDir.Close()
					current, currentErr := currentDir.Lstat(activityName)
					if currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(ownedInfo, current) {
						return
					}
					_ = currentDir.Remove(activityName)
				})
			}, nil
		}
		lockExists := os.IsExist(err)
		if !lockExists && os.IsPermission(err) {
			_, statErr := dir.Lstat(activityName)
			lockExists = statErr == nil
		}
		if !lockExists {
			return nil, err
		}
		info, statErr := dir.Lstat(activityName)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return nil, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, ErrStagingPartActive
		}
		if time.Since(info.ModTime()) <= DefaultStagingMaxAge {
			return nil, ErrStagingPartActive
		}
		current, currentErr := dir.Lstat(activityName)
		if currentErr != nil || !os.SameFile(info, current) || time.Since(current.ModTime()) <= DefaultStagingMaxAge {
			continue
		}
		if removeErr := dir.Remove(activityName); removeErr != nil && !os.IsNotExist(removeErr) {
			return nil, removeErr
		}
	}
	return nil, ErrStagingPartActive
}

// TouchStagingActivity refreshes an owned activity marker while a transfer is
// making progress. A missing or replaced marker is treated as an ownership
// failure so a receiver never silently continues without cross-process
// protection.
func TouchStagingActivity(partPath string) error {
	activityName := filepath.Base(partPath) + stagingActivitySuffix
	if !isRealStagingOutputDir(stagingOutputDirForPart(partPath)) {
		return ErrStagingPartActive
	}
	unlockProcess, err := acquireStagingProcessLock(stagingProcessLockPathForPart(partPath))
	if err != nil {
		return err
	}
	defer unlockProcess()
	dir, err := OpenStagingDirectory(filepath.Dir(partPath))
	if err != nil {
		return err
	}
	defer dir.Close()
	info, err := dir.Lstat(activityName)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrStagingPartActive
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrStagingPartActive
	}
	now := time.Now()
	if err := dir.Chtimes(activityName, now, now); err != nil {
		return err
	}
	after, err := dir.Lstat(activityName)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(info, after) {
		return ErrStagingPartActive
	}
	return nil
}

// ReleaseStagingActivity removes a transfer's activity marker. It is safe to
// call more than once and is used by both success and failure cleanup paths.
func releaseStagingActivityUnlocked(partPath string) error {
	dir, err := OpenStagingDirectory(filepath.Dir(partPath))
	if err != nil {
		return err
	}
	defer dir.Close()
	err = dir.Remove(filepath.Base(partPath) + stagingActivitySuffix)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func ReleaseStagingActivity(partPath string) error {
	if !isRealStagingOutputDir(stagingOutputDirForPart(partPath)) {
		return nil
	}
	unlockProcess, err := acquireStagingProcessLock(stagingProcessLockPathForPart(partPath))
	if err != nil {
		return err
	}
	defer unlockProcess()
	return releaseStagingActivityUnlocked(partPath)
}

func stagingPartIsActiveInDir(dir *StagingDirectory, name string) bool {
	info, err := dir.Lstat(name + stagingActivitySuffix)
	if err != nil {
		return !os.IsNotExist(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return true
	}
	return time.Since(info.ModTime()) <= DefaultStagingMaxAge
}

func stagingPartIsActive(partPath string) bool {
	dir, err := OpenStagingDirectory(filepath.Dir(partPath))
	if err != nil {
		return true
	}
	defer dir.Close()
	info, err := dir.Lstat(filepath.Base(partPath) + stagingActivitySuffix)
	if err != nil {
		return !os.IsNotExist(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return true
	}
	return time.Since(info.ModTime()) <= DefaultStagingMaxAge
}

func sweepStalePartials(outDir string, maxAge time.Duration) (removed int, freed int64) {
	if info, err := os.Lstat(outDir); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return 0, 0
	}
	if maxAge <= 0 {
		maxAge = DefaultStagingMaxAge
	}
	cutoff := time.Now().Add(-maxAge)

	removeStale := func(entries []os.DirEntry, dir *StagingDirectory, baseDir string, legacy bool) {
		for _, entry := range entries {
			if entry.IsDir() || !isPartialName(entry.Name()) {
				continue
			}
			name := entry.Name()
			path := filepath.Join(baseDir, name)
			if dir != nil {
				if stagingPartIsActiveInDir(dir, name) {
					continue
				}
			} else if stagingPartIsActive(path) {
				continue
			}
			if legacy && !hasStagingManifest(path) {
				// An unmarked direct .part file is indistinguishable from a
				// user file in a shared working directory. Never delete it
				// automatically; legacy Tantu files without a sidecar require
				// an explicit operator cleanup after review.
				continue
			}
			var info os.FileInfo
			var err error
			if dir != nil {
				info, err = dir.Lstat(name)
			} else {
				info, err = os.Lstat(path)
			}
			if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
				continue
			}
			if dir != nil {
				if stagingPartIsActiveInDir(dir, name) {
					continue
				}
			} else if stagingPartIsActive(path) {
				continue
			}
			if dir != nil {
				err = dir.Remove(name)
			} else {
				err = os.Remove(path)
			}
			if err != nil {
				continue
			}
			removed++
			freed += info.Size()
			// The sidecar is metadata for this partial, not a separate
			// transfer. Remove it after the bytes so a future retry cannot
			// mistake an orphan manifest for a live resumable session.
			if dir != nil {
				_ = dir.Remove(name + stagingActivitySuffix)
				_ = dir.Remove(name + ".meta")
			} else {
				_ = releaseStagingActivityUnlocked(path)
				_ = RemoveStagingManifest(path)
			}
		}
	}

	stagingDir, entries, stagingOK := readPrivateStagingEntries(outDir)
	if stagingOK {
		defer stagingDir.Close()
		removeStale(entries, stagingDir, stagingDir.Path(), false)

		// Clean sidecars left behind by a crash between publishing the
		// manifest and creating the partial. Fresh manifests are retained
		// alongside their fresh partials.
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta") {
				continue
			}
			name := entry.Name()
			partName := strings.TrimSuffix(name, ".meta")
			if !isPartialName(partName) || stagingPartIsActiveInDir(stagingDir, partName) {
				continue
			}
			info, infoErr := stagingDir.Lstat(name)
			if infoErr != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
				continue
			}
			if _, statErr := stagingDir.Lstat(partName); statErr == nil {
				continue
			} else if !os.IsNotExist(statErr) {
				continue
			}
			_ = stagingDir.Remove(name)
		}

		// A crash can leave an activity marker or manifest temporary without
		// its partial. Reclaim those Tantu-owned artifacts after the same
		// stale window so repeated failures cannot accumulate unbounded inodes.
		for _, entry := range entries {
			if entry.IsDir() || (!isActivityMarkerName(entry.Name()) && !isManifestTempName(entry.Name())) {
				continue
			}
			name := entry.Name()
			partPath := stagingArtifactPartPath(stagingDir.Path(), name)
			if partPath == "" {
				continue
			}
			partName := filepath.Base(partPath)
			if _, statErr := stagingDir.Lstat(partName); statErr == nil {
				continue
			} else if !os.IsNotExist(statErr) {
				continue
			}
			info, infoErr := stagingDir.Lstat(name)
			if infoErr != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
				continue
			}
			if err := stagingDir.Remove(name); err == nil {
				removed++
				freed += info.Size()
			}
		}
	}

	// Before private staging was introduced, standalone receivers wrote
	// partials beside the final file as name.part or name.part-N. Only files
	// with a valid Tantu manifest are eligible for automatic cleanup; an
	// unmarked file may belong to the user and must be reviewed manually.
	legacyEntries, legacyErr := os.ReadDir(outDir)
	if legacyErr == nil {
		removeStale(legacyEntries, nil, outDir, true)
	}
	return removed, freed
}

// readPrivateStagingEntries opens the private staging directory through a
// verified os.Root. Maintenance operations below use that root-relative handle,
// so replacing .tantu-staging after this function returns cannot redirect a
// read or removal outside the originally opened directory.
func readPrivateStagingEntries(outDir string) (*StagingDirectory, []os.DirEntry, bool) {
	stagingDir, err := OpenStagingDirectory(filepath.Join(outDir, StagingDirName))
	if err != nil {
		return nil, nil, false
	}
	entries, err := stagingDir.ReadDir()
	if err != nil {
		_ = stagingDir.Close()
		return nil, nil, false
	}
	return stagingDir, entries, true
}

func hasStagingManifest(partPath string) bool {
	if info, err := os.Lstat(filepath.Dir(partPath)); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	_, err := ReadStagingManifest(partPath)
	return err == nil
}

// EnforcePartialBudget removes the oldest non-protected partials until the
// total bytes visible under outDir are no greater than maxBytes and the
// retained partial count is bounded. A non-positive maxBytes uses
// DefaultRetainedPartialBudget. Protected paths
// are active transfers and are never removed. The function is safe to call
// after a failed receive and during startup; it is idempotent.
func EnforcePartialBudget(outDir string, maxBytes int64, protected ...string) (removed int, freed int64) {
	if !isRealStagingOutputDir(outDir) {
		return 0, 0
	}
	if maxBytes <= 0 {
		maxBytes = DefaultRetainedPartialBudget
	}
	stagingMaintenanceMu.Lock()
	defer stagingMaintenanceMu.Unlock()
	unlockProcess, err := acquireStagingProcessLock(stagingProcessLockPathForOutput(outDir))
	if err != nil {
		recordMaintenanceError(err)
		return 0, 0
	}
	defer unlockProcess()
	lastMaintenanceErr = nil
	if info, statErr := os.Lstat(outDir); statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return 0, 0
	}

	protectedSet := make(map[string]struct{}, len(protected))
	for _, path := range protected {
		if abs, err := filepath.Abs(path); err == nil {
			protectedSet[abs] = struct{}{}
		}
	}
	candidates, total := collectPartialCandidates(outDir)
	remaining := len(candidates)
	if total <= maxBytes && remaining <= DefaultRetainedPartialCount {
		return 0, 0
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].info.ModTime().Equal(candidates[j].info.ModTime()) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].info.ModTime().Before(candidates[j].info.ModTime())
	})
	for _, candidate := range candidates {
		if total <= maxBytes && remaining <= DefaultRetainedPartialCount {
			break
		}
		if abs, err := filepath.Abs(candidate.path); err == nil {
			if _, ok := protectedSet[abs]; ok {
				continue
			}
		}
		if stagingPartIsActive(candidate.path) {
			continue
		}
		var removeErr error
		if candidate.staging {
			dir, openErr := OpenStagingDirectory(filepath.Dir(candidate.path))
			if openErr != nil {
				continue
			}
			removeErr = dir.Remove(filepath.Base(candidate.path))
			_ = dir.Close()
		} else {
			removeErr = os.Remove(candidate.path)
		}
		if removeErr != nil {
			continue
		}
		removed++
		freed += candidate.info.Size()
		total -= candidate.info.Size()
		remaining--
		if !candidate.artifact {
			if candidate.staging {
				dir, openErr := OpenStagingDirectory(filepath.Dir(candidate.path))
				if openErr == nil {
					_ = dir.Remove(filepath.Base(candidate.path) + stagingActivitySuffix)
					_ = dir.Remove(filepath.Base(candidate.path) + ".meta")
					_ = dir.Close()
				}
			} else {
				_ = releaseStagingActivityUnlocked(candidate.path)
				_ = RemoveStagingManifest(candidate.path)
			}
		}
	}
	return removed, freed
}

func collectPartialCandidates(outDir string) ([]partialCandidate, int64) {
	var candidates []partialCandidate
	var total int64
	add := func(dir *StagingDirectory, baseDir, name string, staging bool) {
		if !isPartialName(name) {
			return
		}
		path := filepath.Join(baseDir, name)
		if staging {
			if stagingPartIsActiveInDir(dir, name) {
				return
			}
		} else if stagingPartIsActive(path) {
			return
		}
		if !staging && !hasStagingManifest(path) {
			// Direct partials are retained unless a valid Tantu manifest
			// identifies them. This prevents cleanup from deleting unrelated
			// user files that happen to use a .part suffix.
			return
		}
		var info os.FileInfo
		var err error
		if staging {
			info, err = dir.Lstat(name)
		} else {
			info, err = os.Lstat(path)
		}
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		candidates = append(candidates, partialCandidate{path: path, info: info, staging: staging})
		total += info.Size()
	}
	addArtifact := func(dir *StagingDirectory, name string) {
		if !isActivityMarkerName(name) && !isManifestTempName(name) {
			return
		}
		partPath := stagingArtifactPartPath(dir.Path(), name)
		if partPath == "" {
			return
		}
		partName := filepath.Base(partPath)
		if _, err := dir.Lstat(partName); err == nil {
			return
		} else if !os.IsNotExist(err) {
			return
		}
		path := filepath.Join(dir.Path(), name)
		info, err := dir.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) <= DefaultStagingMaxAge {
			return
		}
		candidates = append(candidates, partialCandidate{path: path, info: info, artifact: true, staging: true})
		total += info.Size()
	}

	if stagingDir, entries, ok := readPrivateStagingEntries(outDir); ok {
		defer stagingDir.Close()
		for _, entry := range entries {
			add(stagingDir, stagingDir.Path(), entry.Name(), true)
			addArtifact(stagingDir, entry.Name())
		}
	}
	if entries, err := os.ReadDir(outDir); err == nil {
		for _, entry := range entries {
			if entry.Name() == StagingDirName {
				continue
			}
			add(nil, outDir, entry.Name(), false)
		}
	}
	return candidates, total
}

func isActivityMarkerName(name string) bool {
	if !strings.HasSuffix(name, stagingActivitySuffix) {
		return false
	}
	return isPartialName(strings.TrimSuffix(name, stagingActivitySuffix))
}

func isManifestTempName(name string) bool {
	if !strings.HasPrefix(name, ".") {
		return false
	}
	base := strings.TrimPrefix(name, ".")
	marker := strings.Index(base, ".meta.tmp-")
	return marker > 0 && isPartialName(base[:marker])
}

func stagingArtifactPartPath(dir, name string) string {
	if isActivityMarkerName(name) {
		return filepath.Join(dir, strings.TrimSuffix(name, stagingActivitySuffix))
	}
	if isManifestTempName(name) {
		base := strings.TrimPrefix(name, ".")
		marker := strings.Index(base, ".meta.tmp-")
		if marker > 0 {
			return filepath.Join(dir, base[:marker])
		}
	}
	return ""
}

func isPartialName(name string) bool {
	if strings.HasSuffix(name, ".part") {
		return true
	}
	marker := strings.LastIndex(name, ".part-")
	if marker <= 0 {
		return false
	}
	suffix := name[marker+len(".part-"):]
	if suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
