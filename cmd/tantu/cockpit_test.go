
package main

import (
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

func TestParsePeerSelection(t *testing.T) {
	peers := []pairing.Peer{
		{Fingerprint: "fp-aaa", Name: "a"},
		{Fingerprint: "fp-bbb", Name: "b"},
	}
	cases := []struct {
		choice string
		want   string
	}{
		{"1", "fp-aaa"},
		{"2", "fp-bbb"},
		{" 2 ", "fp-bbb"},
		{"+1", "fp-aaa"}, // Atoi accepts a leading +; harmless explicit index
		{"0", "0"},       // out of range -> raw query for the resolver
		{"3", "3"},
		{"1abc", "1abc"}, // trailing junk must NOT select peer 1
		{"abc", "abc"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := parsePeerSelection(peers, tc.choice); got != tc.want {
			t.Errorf("parsePeerSelection(%q) = %q, want %q", tc.choice, got, tc.want)
		}
	}
	if got := parsePeerSelection(nil, "1"); got != "" {
		t.Errorf("parsePeerSelection(nil, 1) = %q, want empty", got)
	}
}
