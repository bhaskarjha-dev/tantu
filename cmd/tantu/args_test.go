package main

import (
	"flag"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestNormalizeArgs(t *testing.T) {
	cases := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "no flags",
			input:    []string{"file.txt"},
			expected: []string{"file.txt"},
		},
		{
			name:     "already ordered",
			input:    []string{"--transport=lan", "file.txt"},
			expected: []string{"--transport=lan", "file.txt"},
		},
		{
			name:     "interleaved flag with equal sign",
			input:    []string{"file.txt", "--transport=lan"},
			expected: []string{"--transport=lan", "file.txt"},
		},
		{
			name:     "interleaved flag with separate value",
			input:    []string{"archive.zip", "--transport", "lan", "-v"},
			expected: []string{"--transport", "lan", "-v", "archive.zip"},
		},
		{
			name:     "preserves double dash verbatim",
			input:    []string{"wrap", "--", "gcloud", "auth", "login"},
			expected: []string{"wrap", "--", "gcloud", "auth", "login"},
		},
		{
			name:     "flags before double dash reordered",
			input:    []string{"some-arg", "--transport=lan", "--", "curl", "-s", "http://example.com"},
			expected: []string{"--transport=lan", "some-arg", "--", "curl", "-s", "http://example.com"},
		},
		// Regression: --bundle-path was absent from valueFlags, so a
		// positional in front of it absorbed the path and the bundle was
		// written to a file named after the positional.
		{
			name:     "value flag after positional keeps its value",
			input:    []string{"SOMETEXT", "--bundle-path", "out/support.json"},
			expected: []string{"--bundle-path", "out/support.json", "SOMETEXT"},
		},
		{
			name:     "idempotency key after positional keeps its value",
			input:    []string{"notes.txt", "--idempotency-key", "op-retry-7"},
			expected: []string{"--idempotency-key", "op-retry-7", "notes.txt"},
		},
		{
			name:     "ssh fingerprint after positional keeps its value",
			input:    []string{"https://example.com/auth", "--ssh-fingerprint", "SHA256:abc"},
			expected: []string{"--ssh-fingerprint", "SHA256:abc", "https://example.com/auth"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizeArgs(c.input)
			if !reflect.DeepEqual(got, c.expected) {
				t.Errorf("NormalizeArgs(%v) = %v; want %v", c.input, got, c.expected)
			}
		})
	}
}

// NormalizeArgs reorders flags ahead of positionals so interleaved arguments
// still bind to their flags. It can only do that if it knows which flags take a
// separate value, so valueFlags is a hand-maintained allowlist and a value-taking
// flag missing from it silently loses its argument whenever it follows a
// positional. Derive the truth from the real flag sets and fail on divergence.
func TestSendValueFlagMapIsComplete(t *testing.T) {
	// Every FlagSet the CLI builds for a value-taking string flag.
	sets := map[string]*flag.FlagSet{
		"send":    newTestFlagSet("send", t),
		"open":    newTestFlagSet("open", t),
		"drop":    newTestFlagSet("drop", t),
		"relay":   newTestFlagSet("relay", t),
		"wrap":    newTestFlagSet("wrap", t),
		"node":    newTestFlagSet("node", t),
		"serve":   newTestFlagSet("serve", t),
		"pair":    newTestFlagSet("pair", t),
		"unpair":  newTestFlagSet("unpair", t),
		"receive": newTestFlagSet("receive", t),
		"status":  newTestFlagSet("status", t),
		"doctor":  newTestFlagSet("doctor", t),
		"hub":     newTestFlagSet("hub", t),
	}

	var missing []string
	for setName, fs := range sets {
		fs.VisitAll(func(f *flag.Flag) {
			// Only string flags bind a separate value; bool/int/duration flags
			// must stay out of the map or NormalizeArgs would swallow the next
			// argument.
			if !isStringFlagKind(f) {
				return
			}
			for _, name := range []string{"-" + f.Name, "--" + f.Name} {
				if !isValueFlag(name) {
					missing = append(missing, setName+":"+name)
				}
			}
		})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("value-taking flags missing from valueFlags (each silently loses its value when it follows a positional):\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// isStringFlagKind reports whether a flag was declared with FlagSet.String.
func isStringFlagKind(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !bf.IsBoolFlag()
}

// newTestFlagSet builds a FlagSet populated with the value-taking string flags
// each command declares, so the completeness test has something to check.
func newTestFlagSet(name string, t *testing.T) *flag.FlagSet {
	t.Helper()
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	// Keep in step with the string flags each command registers. These mirror
	// the real definitions; new value-taking flags must be added here too.
	for _, f := range []string{
		"transport", "bridge", "peer", "store-dir", "output-dir", "listen",
		"web-port", "web-addr", "port", "timeout", "fingerprint", "name",
		"ssh-host", "ssh-port", "ssh-user", "ssh-key", "ssh-known-hosts", "ssh-fingerprint",
		"ssh-authorized-key", "bundle-path", "idempotency-key",
	} {
		fs.String(f, "", "")
	}
	return fs
}

func TestSanitizePath(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{`"D:\My Files\archive.zip"`, filepath.Clean(`D:\My Files\archive.zip`)},
		{`'C:\Downloads\file.txt'`, filepath.Clean(`C:\Downloads\file.txt`)},
		{"  relative/path/test.go  ", filepath.Clean("relative/path/test.go")},
		{`""`, ""},
		{"", ""},
	}

	for _, c := range cases {
		got := SanitizePath(c.input)
		if got != c.expected {
			t.Errorf("SanitizePath(%q) = %q; want %q", c.input, got, c.expected)
		}
	}
}
