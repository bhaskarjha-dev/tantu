package drop_test

import (
	"strings"
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

func TestNewDropID_UniqueAndValid(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		id := drop.NewDropID()
		if strings.TrimSpace(id) == "" {
			t.Fatal("NewDropID returned empty ID")
		}
		if len(id) > drop.MaxDropIDLength {
			t.Fatalf("DropID %q exceeds max length", id)
		}
		for _, r := range id {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				t.Fatalf("DropID %q contains invalid character %q", id, r)
			}
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate DropID %q in 100 samples", id)
		}
		seen[id] = struct{}{}
	}
}
