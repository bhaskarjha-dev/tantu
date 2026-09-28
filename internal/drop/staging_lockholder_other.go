//go:build !windows

package drop

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// lockHolderAlive reports whether the process that took a lock is still running.
// Signal 0 performs the existence and permission checks without delivering
// anything.
func lockHolderAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		return true
	}
	// The process exists but belongs to another user, which still counts as
	// alive.
	return errors.Is(err, os.ErrPermission)
}

// lockHolderPID reads the recorded owner pid from a lock directory.
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
