package bridge

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestBindListenErrorText(t *testing.T) {
	forbidden := &os.SyscallError{Syscall: "bind", Err: syscall.EACCES}
	if got := bindListenErrorText(58934, forbidden); !strings.Contains(got, "cannot be bound") || strings.Contains(got, "is in use") {
		t.Errorf("EACCES must not read as in-use, got %q", got)
	} else if !strings.Contains(got, "58934") || !strings.Contains(got, "excludedportrange") {
		t.Errorf("EACCES message must name the port and the remedy, got %q", got)
	}

	perm := &os.SyscallError{Syscall: "bind", Err: syscall.EPERM}
	if got := bindListenErrorText(1, perm); !strings.Contains(got, "cannot be bound") {
		t.Errorf("EPERM must read as forbidden, got %q", got)
	}

	plain := errors.New("listen tcp 127.0.0.1:58934: bind: address already in use")
	if got := bindListenErrorText(58934, plain); !strings.Contains(got, "is in use on this machine") {
		t.Errorf("ordinary bind failure must keep the in-use message, got %q", got)
	}
}
