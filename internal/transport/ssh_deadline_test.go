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
}
