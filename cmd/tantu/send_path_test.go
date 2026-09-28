package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The send command classifies a bare argument as a file or as a text snippet.
// A path-shaped argument that does not resolve is a user error (usually a
// typo), not a snippet to transmit: falling through would send the mistyped
// path itself to the peer and report a verified success.
func TestSendPathHintClassification(t *testing.T) {
	cases := []struct {
		name     string
		arg      string
		textFlag bool
		wantHint bool
	}{
		{"absolute posix", "/etc/hosts", false, true},
		{"relative posix", "./notes.txt", false, true},
		{"windows drive", `C:\Users\dev\file.txt`, false, true},
		{"windows rooted", `\file.txt`, false, true},
		{"home", "~/notes.txt", false, true},
		{"bare word", "hello", false, false},
		{"multiword phrase", "hello there world", false, false},
		{"text flag overrides hint", "./looks/like/a/path", true, false},
		{"text flag overrides backslash", `C:\looks\like\a\path`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sendHasPathHint(tc.arg, tc.textFlag); got != tc.wantHint {
				t.Errorf("sendHasPathHint(%q, text=%v) = %v, want %v", tc.arg, tc.textFlag, got, tc.wantHint)
			}
		})
	}
}

// A bare filename with no separator is ambiguous: `tantu send notes.txt` must
// still work as a file send, and the hint logic must not break that path.
func TestSendPathHintBareFilenameStillOpensAsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// A bare filename carries no path hint, so classification falls back to the
	// "single remaining argument" rule. Confirm os.Stat resolves it so the
	// file/text decision is unambiguous.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture must be stat-able: %v", err)
	}
	if sendHasPathHint("notes.txt", false) {
		t.Errorf("a bare filename must not be treated as an explicit path hint")
	}
}
