package drop

// Wire-level gates for RelPath.
//
// The Hub re-validates every component before touching the filesystem, but the
// wire layer has its own job: refuse a hostile path at acknowledgement, before
// the payload is staged. A path that reaches a staging write and is refused
// afterwards has already cost the user a full transfer's worth of disk, so the
// cheap structural checks belong here - and they must not reject anything a
// real sender can produce.

import (
	"strings"
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
)

// TestValidateDropMetadataRelPath covers the boundary between "relative" and
// "not": every case here is a path that could name a place outside a directory.
func TestValidateDropMetadataRelPath(t *testing.T) {
	refused := []struct {
		name string
		rel  string
	}{
		{"absolute unix", "/etc/passwd"},
		{"absolute windows", `\Windows\win.ini`},
		{"drive letter", "C:/Windows/win.ini"},
		{"parent segment", "a/../b"},
		{"leading parent", "../escape"},
		{"trailing parent", "a/.."},
		{"current directory segment", "a/./b"},
		{"empty segment", "a//b"},
		{"only dots", ".."},
		{"control character", "a/\x07b"},
		{"null byte", "a\x00b"},
		{"too long", strings.Repeat("a", MaxRelPathLength+1)},
		{"too deep", strings.TrimSuffix(strings.Repeat("d/", MaxRelPathDepth+1), "/")},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			meta := DropSend{DropID: "rel1", Kind: DropKindFile, Name: "a.txt", Size: 1, RelPath: tc.rel}
			if err := validateDropMetadata(meta); err == nil {
				t.Fatalf("validateDropMetadata accepted rel_path %q", tc.rel)
			}
		})
	}

	accepted := []struct {
		name string
		rel  string
	}{
		{"empty (every single-file send)", ""},
		{"simple", "a.txt"},
		{"nested", "project/docs/api/readme.md"},
		{"windows separators from a windows sender", `project\docs\readme.md`},
		{"dotfile", "project/.gitignore"},
		{"spaces and parentheses", "my project (2024)/notes.txt"},
		{"non-ascii", "projekt/übersicht/café.txt"},
		{"at the depth bound", strings.TrimSuffix(strings.Repeat("d/", MaxRelPathDepth-1), "/") + "/f.txt"},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			meta := DropSend{DropID: "rel1", Kind: DropKindFile, Name: "a.txt", Size: 1, RelPath: tc.rel}
			if err := validateDropMetadata(meta); err != nil {
				t.Fatalf("validateDropMetadata refused a legitimate rel_path %q: %v", tc.rel, err)
			}
		})
	}
}

// TestRelPathIsOmittedForSingleFileSends is the additive-contract gate. A peer
// that predates this field must receive byte-identical metadata for a
// single-file send, and the only way to know that is to check the encoded form.
func TestRelPathIsOmittedForSingleFileSends(t *testing.T) {
	meta := DropSend{DropID: "d1", Kind: DropKindFile, Name: "notes.txt", Size: 10}
	env, err := protocol.NewEnvelope(TypeDropSend, meta)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if strings.Contains(string(env.Payload), "rel_path") {
		t.Errorf("a single-file send carries rel_path on the wire: %s", env.Payload)
	}
	withPath := DropSend{DropID: "d1", Kind: DropKindFile, Name: "notes.txt", Size: 10, RelPath: "project/notes.txt"}
	env, err = protocol.NewEnvelope(TypeDropSend, withPath)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if !strings.Contains(string(env.Payload), `"rel_path":"project/notes.txt"`) {
		t.Errorf("a directory send does not carry rel_path: %s", env.Payload)
	}
}

// TestRelPathRoundTripsThroughTheEnvelope proves the field survives the only
// encoding path either side uses.
func TestRelPathRoundTripsThroughTheEnvelope(t *testing.T) {
	original := DropSend{DropID: "d1", Kind: DropKindFile, Name: "notes.txt", Size: 10, RelPath: "project/docs/notes.txt"}
	env, err := protocol.NewEnvelope(TypeDropSend, original)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	var decoded DropSend
	if err := env.DecodePayload(&decoded); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if decoded.RelPath != original.RelPath {
		t.Errorf("rel_path = %q, want %q", decoded.RelPath, original.RelPath)
	}
}
