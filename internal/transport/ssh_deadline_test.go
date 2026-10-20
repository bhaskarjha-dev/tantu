package transport

import (
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeSSHChannel supplies the ssh.Channel surface for deadline tests. Only
// Close is ever reached; the promoted interface methods must never be called,
// which is why the embedded interface stays nil.
type fakeSSHChannel struct {
	ssh.Channel
}

func (f *fakeSSHChannel) Close() error { return nil }

// Setting a deadline on a closed connection must report the close, matching
// net.Conn: silently returning nil arms timers against a dead session and
// tells the caller the connection is still usable.
func TestSSHConn_SetDeadlineAfterCloseIsRejected(t *testing.T) {
	conn := newSSHConnWithTimeouts(&fakeSSHChannel{}, nil, nil, nil, 0, 0)
	if !conn.DeadlineSupported() {
		t.Fatal("sshConn with a channel must report deadline support")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	if err := conn.SetDeadline(deadline); err == nil {
		t.Error("SetDeadline on a closed SSH connection returned nil, want an error")
	}
	if err := conn.SetReadDeadline(deadline); err == nil {
		t.Error("SetReadDeadline on a closed SSH connection returned nil, want an error")
	}
	if err := conn.SetWriteDeadline(deadline); err == nil {
		t.Error("SetWriteDeadline on a closed SSH connection returned nil, want an error")
	}
	// The same closed check, on the fourth method that consults it.
	//
	// This assertion could not be made before: the test checked the method
	// only BEFORE Close, and checked only the setters after it, so a
	// DeadlineSupported that kept reporting "deadlines supported" for a
	// closed connection passed. Two call sites use it to choose between the
	// native deadline path and the goroutine fallback, so a closed SSH
	// connection took the native path and reported the setter's error instead
	// of the context's. See docs/KNOWN-LIMITATIONS.md row 3.32.
	if conn.DeadlineSupported() {
		t.Error("DeadlineSupported on a closed SSH connection returned true, want false: the " +
			"callers use it to choose between the native deadline path and the goroutine fallback")
	}
}
