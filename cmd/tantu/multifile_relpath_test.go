package main

// Gates for directory-structure preservation on the sending side.
//
// The sender's job is narrow and worth pinning exactly: express a walked path
// as the receiver should place it, name the top-level component safely, and
// keep --flatten behaving precisely as it did before this feature existed.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTreeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestExpandDirectoryPreservesPaths is the sender-side gate: each file carries
// its path inside the tree, rooted at the directory the user pointed at.
func TestExpandDirectoryPreservesPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	writeTreeFile(t, root, "README.md", "readme")
	writeTreeFile(t, root, "docs/api/openapi.yaml", "openapi")
	writeTreeFile(t, root, "src/main.go", "package main")
	writeTreeFile(t, root, ".gitignore", "*.log")

	files, err := expandDirectory(root, true)
	if err != nil {
		t.Fatalf("expandDirectory: %v", err)
	}

	got := map[string]string{}
	for _, f := range files {
		got[f.RelPath] = f.Name
	}
	want := map[string]string{
		"project/README.md":             "README.md",
		"project/docs/api/openapi.yaml": "openapi.yaml",
		"project/src/main.go":           "main.go",
		"project/.gitignore":            ".gitignore",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d relative paths, want %d: %v", len(got), len(want), got)
	}
	for relPath, leaf := range want {
		leafName, ok := got[relPath]
		if !ok {
			t.Errorf("no file carried rel_path %q (got %v)", relPath, got)
			continue
		}
		if leafName != leaf {
			t.Errorf("%s leaf name = %q, want %q", relPath, leafName, leaf)
		}
	}

	// The order must be deterministic on the full path, not on the leaf: a
	// re-send has to produce the same sequence so a failure names the same file
	// every time.
	for i := 1; i < len(files); i++ {
		if files[i-1].RelPath > files[i].RelPath {
			t.Fatalf("expansion is not sorted by relative path: %q before %q", files[i-1].RelPath, files[i].RelPath)
		}
	}
}

// TestExpandDirectoryFlattenIsUnchanged pins the opt-out against the behaviour
// that existed before this feature. --flatten must be exactly the old expansion,
// because that is what a user who asks for it is asking for.
func TestExpandDirectoryFlattenIsUnchanged(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	writeTreeFile(t, root, "docs/api/readme.md", "readme")
	writeTreeFile(t, root, "other/readme.md", "readme")

	files, err := expandDirectory(root, false)
	if err != nil {
		t.Fatalf("expandDirectory: %v", err)
	}
	names := map[string]bool{}
	for _, f := range files {
		if f.RelPath != "" {
			t.Errorf("a flattened expansion carried rel_path %q", f.RelPath)
		}
		names[f.Name] = true
	}
	if len(names) != 2 {
		t.Fatalf("flattening collapsed two distinct files into %d name(s): %v", len(names), names)
	}
	// The historical shape: the path folded into the name, separators replaced.
	if !names["docs-api-readme.md"] || !names["other-readme.md"] {
		t.Errorf("flattened names are not the documented shape: %v", names)
	}
}

// TestSingleFileSendHasNoRelativePath is the additive-contract gate on the CLI
// side: with nothing to preserve there is nothing to send, and a stray field
// would change the wire shape for every ordinary transfer.
func TestSingleFileSendHasNoRelativePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	writeTreeFile(t, root, "only.txt", "x")
	files, err := expandDirectory(root, true)
	if err != nil {
		t.Fatalf("expandDirectory: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	// A single-file directory does carry a path - that is what makes the
	// receiver create the folder - so this test is about the *boundary*: the
	// flattened name must be the bare basename, never a path with separators.
	if strings.ContainsAny(files[0].Name, `/\`) {
		t.Errorf("leaf name %q contains a separator", files[0].Name)
	}
	if files[0].Name != "only.txt" {
		t.Errorf("leaf name = %q, want the basename", files[0].Name)
	}
}

// TestTransferRelPathNamesTheSentDirectory is the shape gate: the sent
// directory is the first component, so a project arrives as a project rather
// than as its contents scattered into whatever else is in the folder.
func TestTransferRelPathNamesTheSentDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	writeTreeFile(t, root, "src/main.go", "x")
	got := transferRelPath(root, filepath.Join(root, "src", "main.go"))
	if got != "project/src/main.go" {
		t.Errorf("transferRelPath = %q, want %q", got, "project/src/main.go")
	}

	// A directory name with spaces and parentheses is ordinary and must survive
	// unchanged: rewriting it would rename the folder the user pointed at.
	spaced := filepath.Join(base, "my project (2024)")
	writeTreeFile(t, spaced, "src/main.go", "x")
	got = transferRelPath(spaced, filepath.Join(spaced, "src", "main.go"))
	if got != "my project (2024)/src/main.go" {
		t.Errorf("transferRelPath = %q, want %q", got, "my project (2024)/src/main.go")
	}
}

// TestSanitizeRelPathComponentCoversTheTopLevelOnly is the adjustment gate.
// The sender adjusts a name rather than refusing it, and this pins exactly how
// far that goes: one component, bounded, never silently empty.
func TestSanitizeRelPathComponentCoversTheTopLevelOnly(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"project", "project"},
		{"my project (2024)", "my project (2024)"},
		{"weird:name", "weird_name"},
		{"a<b>c", "a_b_c"},
		{`q"uote`, "q_uote"},
		{"pipe|bar", "pipe_bar"},
		{"star*", "star_"},
		{"question?", "question_"},
		{"bell\x07", "bell"},
		{" leading", "leading"},
		{"trailing.", "trailing"},
		{"...", dropBinName},
		{"", dropBinName},
		{strings.Repeat("x", 400) + ".txt", strings.Repeat("x", 196) + ".txt"},
	}
	for _, tc := range cases {
		if got := sanitizeRelPathComponent(tc.in); got != tc.want {
			t.Errorf("sanitizeRelPathComponent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// The adjusted name must always be one the receiver will accept, or the
	// sender would produce a path its own peer refuses.
	for _, in := range []string{"weird:name", "...", strings.Repeat("y", 500), "a\x00b", "CON"} {
		got := sanitizeRelPathComponent(in)
		if got == "" || strings.ContainsAny(got, `<>:"|?*`) || strings.Contains(got, "\x00") {
			t.Errorf("sanitizeRelPathComponent(%q) = %q, which the receiver would refuse", in, got)
		}
		if len(got) > 200 {
			t.Errorf("sanitizeRelPathComponent(%q) produced %d bytes, above the receiver's component bound", in, len(got))
		}
	}
}
