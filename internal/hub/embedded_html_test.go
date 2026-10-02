package hub

import (
	"crypto/sha256"
	"os"
	"strings"
	"testing"
)

// The dashboard pages live in real HTML files compiled into the binary with
// //go:embed. That is only an improvement over the old Go raw strings if the
// served bytes and the file on disk stay the same bytes: if the directive
// ever points at the wrong file, or the variable is replaced with an
// in-source literal, whichever copy a test happens to read still passes on
// its own. This compares both copies directly, so a miswired embed fails
// here instead of shipping a stale page, and logs the digest so a served
// page can be identified after the fact.
func TestEmbeddedHTMLMatchesSourceFile(t *testing.T) {
	cases := []struct {
		name string
		file string
		got  string
	}{
		{"dashboard", "dashboard.html", dashboardHTML},
		{"relay interstitial", "relay_interstitial.html", relayInterstitialHTML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatalf("reading %s: %v", tc.file, err)
			}
			if string(want) != tc.got {
				t.Fatalf("%s and the embedded variable diverge (file=%d bytes, embedded=%d bytes): the //go:embed directive points elsewhere, or the variable was edited in source",
					tc.file, len(want), len(tc.got))
			}
			if !strings.HasPrefix(tc.got, "<!DOCTYPE html>") || !strings.HasSuffix(tc.got, "</html>") {
				t.Fatalf("%s is not a complete HTML document (prefix/suffix check failed)", tc.file)
			}
			t.Logf("sha256=%x len=%d", sha256.Sum256([]byte(tc.got)), len(tc.got))
		})
	}
}
