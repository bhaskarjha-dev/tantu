//go:build windows

package drop

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// lockHolderAlive reports whether the process that took a lock is still running.
// On Windows os.Process.Signal is not implemented, so this opens the process
// directly: OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION succeeds only
// for a live process.
func lockHolderAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const (
		processQueryLimitedInformation = 0x1000
		errorAccessDenied              = syscall.Errno(5)
	)
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		// The process exists but belongs to another account, which still
		// counts as alive.
		return err == errorAccessDenied
	}
	_ = syscall.CloseHandle(handle)
	return true
}

// lockHolderPID reads the recorded owner pid from a lock directory. A missing or
// unreadable heartbeat means an unknown holder, which is reported as alive so
// the wait times out rather than stealing a lock whose owner we cannot check.
func lockHolderPID(lockPath string) (int, bool) {
	raw, err := os.ReadFile(filepath.Join(lockPath, stagingProcessLockHeartbeat))
	if err != nil {
		return 0, false
	}
	if len(raw) > 64 {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
