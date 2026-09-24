package transport

import (
	"context"
	"errors"
	"testing"
)

type stubTransport struct{ dialErr error }

func (s *stubTransport) Listen(_ string) (Listener, error) { return nil, errors.New("no listener") }
func (s *stubTransport) Dial(_ string) (Conn, error) {
	if s.dialErr != nil {
		return nil, s.dialErr
	}
	return nil, errors.New("stub dial")
}

// A fingerprint requirement on a transport without pinning must fail closed,
// never silently downgrade to an unauthenticated dial.
func TestDialPinned_RequiresPinningCapability(t *testing.T) {
	stub := &stubTransport{}
	if _, err := DialPinned(stub, "127.0.0.1:9877", "aabbcc"); err == nil {
		t.Fatal("DialPinned with fingerprint on unpinned transport succeeded, want error")
	}
	if _, err := DialPinnedContext(context.Background(), stub, "127.0.0.1:9877", "aabbcc"); err == nil {
		t.Fatal("DialPinnedContext with fingerprint on unpinned transport succeeded, want error")
	}
	// Empty fingerprint preserves the legacy loopback/test behavior.
	if _, err := DialPinned(stub, "127.0.0.1:9877", ""); err == nil || err.Error() != "stub dial" {
		t.Fatalf("DialPinned without fingerprint = %v, want stub passthrough", err)
	}
}
