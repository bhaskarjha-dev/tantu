package main

import (
	"path/filepath"
	"reflect"
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
