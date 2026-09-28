package hub

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A received name is bounded at 4096 bytes upstream, which was sized to stop
// metadata becoming a memory sink and not to fit a filesystem. A long name
// therefore passed acknowledgement, the whole payload was staged, and the " (n)"
// disambiguator applied at publication pushed the result past the 255-byte
// NAME_MAX shared by ext4, APFS, and NTFS — failing only after the transfer had
// consumed the disk it was going to write.
func TestSanitizeDropFilenameBoundsLength(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"plain long name", strings.Repeat("a", 400) + ".txt"},
		{"no extension", strings.Repeat("b", 500)},
		{"long extension", strings.Repeat("c", 300) + "." + strings.Repeat("d", 60)},
		{"everything long", strings.Repeat("e", 1000)},
		{"multibyte", strings.Repeat("é", 300) + ".dat"},
		{"emoji", strings.Repeat("🔐", 200) + ".png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeDropFilename(tc.input)
			if len(got) > maxSanitizedDropNameBytes {
				t.Errorf("sanitized name is %d bytes, want at most %d: %q", len(got), maxSanitizedDropNameBytes, got)
			}
			if got == "" {
				t.Fatal("sanitizer must never return an empty name")
			}
			// A name that is not valid UTF-8 would be written as a replacement
			// sequence, which is a different name than the peer sent.
			if !utf8.ValidString(got) {
				t.Errorf("sanitized name is not valid UTF-8: %q", got)
			}
			// Enough headroom must remain for the collision suffix publication
			// appends, otherwise the bound only moves the failure.
			if len(got) > 0 && len(got)+len(" (9999)") > 255 {
				t.Errorf("no room left for a collision suffix: %d bytes", len(got))
			}
		})
	}
}

// Truncation must not mangle the extension away or leave a trailing separator
// that the filesystem rejects.
func TestSanitizeDropFilenameTruncationKeepsExtensionAndNoTrailingDot(t *testing.T) {
	got := SanitizeDropFilename(strings.Repeat("x", 400) + ".json")
	if !strings.HasSuffix(got, ".json") {
		t.Errorf("truncation must preserve the extension, got %q", got[len(got)-10:])
	}
	if strings.HasSuffix(got, ".") || strings.HasSuffix(got, " ") {
		t.Errorf("truncation must not leave a trailing dot or space: %q", got)
	}
}

// Names that already fit must be untouched, so the bound cannot change existing
// behaviour.
func TestSanitizeDropFilenameLeavesShortNamesAlone(t *testing.T) {
	for _, name := range []string{"report.pdf", "notes.txt", "archive.tar.gz", "a", "x.png"} {
		if got := SanitizeDropFilename(name); got != name {
			t.Errorf("short name changed: %q -> %q", name, got)
		}
	}
}
