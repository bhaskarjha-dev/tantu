package main

// A sign-in waiting on a browser used to be invisible from the terminal: a
// CLI that never returned looked exactly like a hung process, and the peer
// kept holding a loopback callback port the whole time. These tests pin the
// selection and rendering rules behind the cockpit's [l] command.

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
)

// Only a plain in-range index may select. Releasing the wrong sign-in tears
// down a login the user still wanted, which is worse than releasing none.
func TestSelectActiveSignInIsStrict(t *testing.T) {
	active := []hub.ActiveRelay{
		{OperationID: "op-oldest", Peer: "devbox", State: "relaying", AgeSeconds: 90},
		{OperationID: "op-newer", Peer: "laptop", State: "relaying", AgeSeconds: 5},
	}

	cases := []struct {
		choice string
		wantOK bool
		wantID string
		reason string
	}{
		{"1", true, "op-oldest", "the first listed sign-in selects"},
		{"2", true, "op-newer", "the second listed sign-in selects"},
		{" 2 ", true, "op-newer", "surrounding space is tolerated"},
		{"", false, "", "empty means keep waiting"},
		{"   ", false, "", "whitespace means keep waiting"},
		{"0", false, "", "zero is not a 1-based index"},
		{"3", false, "", "out of range must not select"},
		{"-1", false, "", "a sign is not an index"},
		{"+1", false, "", "a leading sign is not an index"},
		{"1abc", false, "", "trailing junk must not select"},
		{"abc", false, "", "text must not select"},
	}
	for _, tc := range cases {
		got, ok := selectActiveSignIn(active, tc.choice)
		if ok != tc.wantOK {
			t.Errorf("selectActiveSignIn(%q) ok = %v, want %v (%s)", tc.choice, ok, tc.wantOK, tc.reason)
			continue
		}
		if ok && got.OperationID != tc.wantID {
			t.Errorf("selectActiveSignIn(%q) selected %q, want %q", tc.choice, got.OperationID, tc.wantID)
		}
	}

	// An empty list must never select, whatever is typed.
	if _, ok := selectActiveSignIn(nil, "1"); ok {
		t.Error("an empty list must not allow a selection")
	}
}

// The listing must distinguish a relay that has reached the peer from one
// still starting. Telling the user a starting relay is "on the wire" would
// send them to cancel a login the peer does not even know about yet.
func TestDescribeActiveSignInDistinguishesStarting(t *testing.T) {
	relaying := describeActiveSignIn(hub.ActiveRelay{Peer: "devbox", State: "relaying", AgeSeconds: 90})
	starting := describeActiveSignIn(hub.ActiveRelay{Peer: "devbox", State: "starting", Age: 200 * time.Millisecond})
	resolving := describeActiveSignIn(hub.ActiveRelay{State: "relaying", AgeSeconds: 5})

	if !strings.Contains(relaying, "devbox") {
		t.Errorf("the destination must be named, got %q", relaying)
	}
	if !strings.Contains(relaying, "1m30s") {
		t.Errorf("the age must be shown so the user can judge how long it has waited, got %q", relaying)
	}
	if !strings.Contains(starting, "not yet on the wire") {
		t.Errorf("a starting relay must say so rather than claiming to be in flight, got %q", starting)
	}
	if relaying == starting {
		t.Error("relaying and starting must render differently")
	}
	// A relay with no resolved peer must still render something meaningful.
	if !strings.Contains(resolving, "resolving") {
		t.Errorf("an unresolved relay must say it is resolving, got %q", resolving)
	}
}

// The banner advertises the key that answers "is it stuck?", so the help line
// and the unknown-key message cannot drift from the banner.
func TestCockpitShortcutHelpAdvertisesSignIns(t *testing.T) {
	help := cockpitShortcutHelp()
	if !strings.Contains(help, "[l]") {
		t.Errorf("the shortcut help must advertise the sign-in key, got %q", help)
	}
	if !strings.Contains(help, "sign-ins") {
		t.Errorf("the sign-in key must be labelled, not just present, got %q", help)
	}
	// The previously advertised keys must survive the addition.
	for _, key := range []string{"[o]", "[s]", "[t]", "[c]", "[p]", "[v]", "[q]"} {
		if !strings.Contains(help, key) {
			t.Errorf("shortcut %s disappeared from the help: %q", key, help)
		}
	}
}

// With nothing in flight the command must say so plainly and must not offer a
// selection the user cannot use.
func TestPrintActiveSignInsWithNoneOpen(t *testing.T) {
	out := captureStdout(t, func() {
		printActiveSignIns(context.Background(), nil, nil)
	})
	if !strings.Contains(out, "No sign-in is in progress") {
		t.Errorf("with nothing open the cockpit must say so plainly, got %q", out)
	}
	if strings.Contains(out, "[1-") {
		t.Errorf("an empty list must not offer a selection, got %q", out)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}
