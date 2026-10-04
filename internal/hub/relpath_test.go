package hub

// Gates for directory-structure preservation.
//
// The feature is small; the risk is not. A peer-supplied relative path is the
// first thing in this product that lets a remote machine choose a *place* on
// disk rather than a single file name, so every check below is about where a
// write can land, not about whether the tree looks right.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

// dropOpenRoot is a short alias for the anchored-handle constructor, so the
// tests read as "open the output directory" rather than as a staging-area
// detail. It is a function rather than an inline call because each test also
// needs the closer, and a helper that returns both is easier to use correctly.
func dropOpenRoot(dir string) (*drop.StagingDirectory, error) {
	return drop.OpenStagingDirectory(dir)
}

// TestSanitizeRelPathRefusesEveryEscape is the primary gate. Each case is a way
// out of the output directory or a way to make two distinct files share one
// name. Every one must be refused, not rewritten: a sanitiser that silently
// produced a different name would publish a file the sender never agreed to.
func TestSanitizeRelPathRefusesEveryEscape(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"parent traversal", "../etc/passwd"},
		{"nested parent traversal", "a/../../b"},
		{"trailing parent traversal", "a/.."},
		{"absolute unix", "/etc/passwd"},
		{"absolute windows drive", `C:\Windows\System32\drivers\etc\hosts`},
		{"drive relative", "C:temp/x"},
		{"unc", `\\server\share\x`},
		{"leading slash after trim", "  /etc/shadow"},
		{"current directory segment", "./a"},
		{"current directory alone", "."},
		{"parent alone", ".."},
		{"empty segment", "a//b"},
		{"trailing separator", "a/b/"},
		{"control character", "a/\x01b"},
		{"newline", "a\nb"},
		{"null byte", "a\x00b"},
		{"colon (alternate data stream)", "notes.txt:hidden"},
		{"question mark", "a?b"},
		{"asterisk", "a*b"},
		{"angle brackets", "<script>"},
		{"pipe", "a|b"},
		{"quote", `a"b`},
		{"less than", "a<b"},
		{"trailing dot", "readme."},
		{"trailing space", "readme "},
		{"leading space", " readme"},
		{"reserved device con", "CON"},
		{"reserved device con with extension", "con.txt"},
		{"reserved device nul", "a/nul"},
		{"reserved device com1", "COM1"},
		{"reserved device lpt9", "lpt9"},
		{"component too long", strings.Repeat("x", MaxRelPathComponent+1)},
		{"whole path too long", strings.Repeat("ab/", MaxRelPathLength) + "cd"},
		{"too deep", strings.TrimSuffix(strings.Repeat("d/", MaxRelPathDepth+2), "/")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			components, err := SanitizeRelPath(tc.in)
			if err == nil {
				t.Fatalf("SanitizeRelPath(%q) accepted it as %v", tc.in, components)
			}
			if len(components) != 0 {
				t.Errorf("a refused path still produced components: %v", components)
			}
			// The reason must be actionable: a bare "invalid" sends the user
			// looking for a different command.
			if !strings.Contains(err.Error(), " ") {
				t.Errorf("refusal %q carries no explanation", err.Error())
			}
		})
	}
}

// TestSanitizeRelPathAcceptsRealTrees is the other half. A gate that only
// refuses would pass on a receiver that cannot send anything at all.
func TestSanitizeRelPathAcceptsRealTrees(t *testing.T) {
	cases := []struct {
		in    string
		parts []string
	}{
		{"notes.txt", []string{"notes.txt"}},
		{"docs/api/readme.md", []string{"docs", "api", "readme.md"}},
		{`docs\api\readme.md`, []string{"docs", "api", "readme.md"}},
		{"my project (2024)/src/main.go", []string{"my project (2024)", "src", "main.go"}},
		{"unicode/\u00fcber/caf\u00e9.txt", []string{"unicode", "\u00fcber", "caf\u00e9.txt"}},
		{"dotfiles/.gitignore", []string{"dotfiles", ".gitignore"}},
		{"a/...hidden", []string{"a", "...hidden"}},
		{"a/.b", []string{"a", ".b"}},
		{"emoji/\U0001f9f0.bin", []string{"emoji", "\U0001f9f0.bin"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			parts, err := SanitizeRelPath(tc.in)
			if err != nil {
				t.Fatalf("SanitizeRelPath(%q): %v", tc.in, err)
			}
			if len(parts) != len(tc.parts) {
				t.Fatalf("got %v, want %v", parts, tc.parts)
			}
			for i := range parts {
				if parts[i] != tc.parts[i] {
					t.Errorf("component %d = %q, want %q", i, parts[i], tc.parts[i])
				}
			}
		})
	}
}

// TestSanitizeRelPathEmptyIsNotAnError pins the additive contract: every
// single-file transfer that has ever been sent carries no relative path, and
// that must remain a normal, successful case rather than a refusal.
func TestSanitizeRelPathEmptyIsNotAnError(t *testing.T) {
	for _, in := range []string{"", "   ", "\t"} {
		parts, err := SanitizeRelPath(in)
		if err != nil {
			t.Errorf("SanitizeRelPath(%q): %v", in, err)
		}
		if len(parts) != 0 {
			t.Errorf("SanitizeRelPath(%q) produced %v", in, parts)
		}
		if JoinRelComponents(parts) != "" {
			t.Error("an empty path joined to something")
		}
	}
}

// TestEnsureRelDirsRefusesAPlantedSymlink is the gate for the attack this
// feature makes possible: a directory that already exists inside the output
// directory as a link elsewhere. Without this check every file in that branch
// would be written wherever the link points.
//
// The decision logic is checked against a fake root so it runs everywhere;
// TestEnsureRelDirsRefusesARealPlantedSymlink then repeats it against a real
// filesystem link for the platforms whose OS allows an unprivileged test to
// create one. Splitting them matters: Windows without developer mode cannot
// create a symlink, so a single test would leave the single most important
// property in this batch unevidenced on two of three CI platforms.
func TestEnsureRelDirsRefusesAPlantedSymlink(t *testing.T) {
	sep := string(filepath.Separator)
	root := &fakeRelRoot{
		entries: map[string]fakeRelEntry{
			"pkg":                         {mode: os.ModeSymlink | os.ModeDir},
			sep + "project":               {mode: os.ModeDir},
			sep + "project" + sep + "src": {mode: os.ModeDir},
		},
	}
	// A link at the first level of the requested path must stop the walk
	// immediately, before any later component is touched.
	err := EnsureRelDirs(root, filepath.Join("pkg", "util", "notes.txt"))
	if err == nil {
		t.Fatal("EnsureRelDirs accepted a symlinked directory component")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("refusal does not name the cause: %v", err)
	}
	if _, inspected := root.touched["pkg"]; !inspected {
		t.Error("the symlinked component was never inspected, so the refusal came from somewhere else")
	}
	for name := range root.touched {
		if strings.Contains(name, "util") {
			t.Errorf("the walk continued past the symlink and touched %q", name)
		}
	}
	// The same walk with a real directory in that place must succeed and create
	// only the missing component, so the gate cannot pass by refusing everything.
	clean := &fakeRelRoot{entries: map[string]fakeRelEntry{
		"project":                     {mode: os.ModeDir},
		sep + "project" + sep + "src": {mode: os.ModeDir},
	}}
	if err := EnsureRelDirs(clean, filepath.Join("project", "src", "deep", "notes.txt")); err != nil {
		t.Fatalf("EnsureRelDirs refused a real tree: %v", err)
	}
	if _, created := clean.created[filepath.Join("project", "src", "deep")]; !created {
		t.Errorf("the missing component was not created: %v", clean.created)
	}
}

// TestEnsureRelDirsRefusesARealPlantedSymlink repeats the symlink refusal
// against an actual link on disk, and asserts the decisive consequence: nothing
// was written through it.
func TestEnsureRelDirsRefusesARealPlantedSymlink(t *testing.T) {
	out := t.TempDir()
	outside := t.TempDir()
	canary := filepath.Join(outside, "canary.txt")
	if err := os.WriteFile(canary, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A link at the first level of the tree, pointing outside the output dir.
	if err := os.Symlink(outside, filepath.Join(out, "pkg")); err != nil {
		t.Skipf("this platform will not create an unprivileged symlink (%v); the decision logic is covered by TestEnsureRelDirsRefusesAPlantedSymlink", err)
	}

	root, err := dropOpenRoot(out)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	err = EnsureRelDirs(root, filepath.Join("pkg", "util", "notes.txt"))
	if err == nil {
		t.Fatal("EnsureRelDirs accepted a symlinked directory component")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("refusal does not name the cause: %v", err)
	}
	// The decisive assertion: nothing was written through the link.
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != "canary.txt" {
		t.Errorf("the link target was modified: %v", entries)
	}
}

// fakeRelEntry is one pre-existing path in a fakeRelRoot.
type fakeRelEntry struct {
	mode os.FileMode
}

// fakeRelRoot is a scripted relRoot. It exists so the symlink and
// not-a-directory decisions can be asserted deterministically on every
// platform, including the ones where a test process may not create a real link.
type fakeRelRoot struct {
	entries  map[string]fakeRelEntry
	created  map[string]struct{}
	touched  map[string]struct{}
	mkdirErr error
}

func (f *fakeRelRoot) ensureMaps() {
	if f.created == nil {
		f.created = map[string]struct{}{}
	}
	if f.touched == nil {
		f.touched = map[string]struct{}{}
	}
}

func (f *fakeRelRoot) Lstat(name string) (os.FileInfo, error) {
	f.ensureMaps()
	f.touched[name] = struct{}{}
	if entry, ok := f.entries[name]; ok {
		return fakeRelInfo{name: filepath.Base(name), mode: entry.mode}, nil
	}
	return nil, &os.PathError{Op: "lstat", Path: name, Err: os.ErrNotExist}
}

func (f *fakeRelRoot) Mkdir(name string, _ os.FileMode) error {
	f.ensureMaps()
	if f.mkdirErr != nil {
		return f.mkdirErr
	}
	if _, ok := f.entries[name]; ok {
		return &os.PathError{Op: "mkdir", Path: name, Err: os.ErrExist}
	}
	f.created[name] = struct{}{}
	if f.entries == nil {
		f.entries = map[string]fakeRelEntry{}
	}
	f.entries[name] = fakeRelEntry{mode: os.ModeDir}
	return nil
}

// fakeRelInfo is a minimal os.FileInfo for the fake root.
type fakeRelInfo struct {
	name string
	mode os.FileMode
}

func (i fakeRelInfo) Name() string       { return i.name }
func (i fakeRelInfo) Size() int64        { return 0 }
func (i fakeRelInfo) Mode() os.FileMode  { return i.mode }
func (i fakeRelInfo) ModTime() time.Time { return time.Time{} }
func (i fakeRelInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fakeRelInfo) Sys() any           { return nil }

// TestEnsureRelDirsRefusesAFileInTheWay closes the other shape of the same
// attack: a plain file already occupying a name the tree needs. Publishing
// through it is impossible, and silently replacing it is not this product's
// behaviour.
func TestEnsureRelDirsRefusesAFileInTheWay(t *testing.T) {
	out := t.TempDir()
	blocker := filepath.Join(out, "pkg")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := dropOpenRoot(out)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	if err := EnsureRelDirs(root, filepath.Join("pkg", "notes.txt")); err == nil {
		t.Fatal("EnsureRelDirs accepted a file where a directory was required")
	}
	if data, readErr := os.ReadFile(blocker); readErr != nil || string(data) != "not a directory" {
		t.Errorf("the blocking file was modified: %q err=%v", data, readErr)
	}
}

// TestEnsureRelDirsCreatesAndIsIdempotent proves the positive path: the tree
// exists afterwards, with modes that keep it private, and a second call for a
// sibling file does not fail or disturb it.
func TestEnsureRelDirsCreatesAndIsIdempotent(t *testing.T) {
	out := t.TempDir()
	root, err := dropOpenRoot(out)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	rel := filepath.Join("project", "docs", "api", "readme.md")
	if err := EnsureRelDirs(root, rel); err != nil {
		t.Fatalf("EnsureRelDirs: %v", err)
	}
	if err := EnsureRelDirs(root, filepath.Join("project", "docs", "api", "notes.md")); err != nil {
		t.Fatalf("second EnsureRelDirs: %v", err)
	}
	info, err := os.Stat(filepath.Join(out, "project", "docs", "api"))
	if err != nil || !info.IsDir() {
		t.Fatalf("tree not created: %v %v", info, err)
	}
	// A directory holding a peer's file names should not be group- or
	// world-readable. Unix only: Windows reports 0777 for every directory
	// because its ACL model has no equivalent bit, so the assertion would be
	// meaningless there rather than passing or failing honestly.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("created directory is mode %o; a tree of received files should not be group- or world-accessible", perm)
		}
	}
	// A path with no directory component is a no-op, not an error.
	if err := EnsureRelDirs(root, "notes.md"); err != nil {
		t.Errorf("EnsureRelDirs on a bare file name: %v", err)
	}
}

// TestPublishNestedKeepsEverythingInsideTheOutputDirectory drives the real
// publication function with a nested destination and a digest, which is the
// closest a unit test gets to the end-to-end behaviour.
func TestPublishNestedKeepsEverythingInsideTheOutputDirectory(t *testing.T) {
	out := t.TempDir()
	payload := []byte("nested payload")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	staging := filepath.Join(out, ".tantu-staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(staging, "abc123.part")
	if err := os.WriteFile(part, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenFile(part, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	dest := filepath.Join(out, "project", "docs", "readme.md")
	published, err := finalizePartFile(source, part, dest, out, digest, int64(len(payload)))
	if err != nil {
		t.Fatalf("finalizePartFile: %v", err)
	}
	if published != dest {
		t.Errorf("published %q, want %q", published, dest)
	}
	got, err := os.ReadFile(published)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("published content %q err=%v", got, err)
	}
	// Whichever publication path ran, neither leaves a second copy of the
	// payload behind: a rename moves the partial and a copy removes it.
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("the staging partial survived publication: %v", err)
	}
	// The private staging area must not be left inside the reconstructed tree.
	if _, err := os.Stat(filepath.Join(out, "project", ".tantu-staging")); !os.IsNotExist(err) {
		t.Errorf("a staging directory was created inside the delivered tree: %v", err)
	}
}

// TestNestedRenamePathIsReachable proves the fast path exists and is not dead
// code, by releasing the descriptor before the rename - the only situation in
// which Windows allows it. Without this, "the copy fallback saved us" would
// leave the rename path permanently unexercised on two of three platforms.
func TestNestedRenamePathIsReachable(t *testing.T) {
	out := t.TempDir()
	payload := []byte("rename path")
	staging := filepath.Join(out, ".tantu-staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(staging, "renameme.part")
	if err := os.WriteFile(part, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := dropOpenRoot(out)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	relDest := filepath.Join("project", "docs", "readme.md")
	if err := EnsureRelDirs(root, relDest); err != nil {
		t.Fatalf("EnsureRelDirs: %v", err)
	}
	if err := root.Rename(filepath.Join(".tantu-staging", "renameme.part"), relDest); err != nil {
		t.Fatalf("root-anchored rename into a created tree failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, relDest))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("content after rename: %q err=%v", got, err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("the source survived the rename: %v", err)
	}
}

// TestPublishNestedRefusesADestinationOutsideTheRoot is the containment gate for
// publication itself, not just for path validation. Even if every validator
// above were bypassed, the last line of defence is that a destination which
// does not resolve inside the output directory is refused rather than written.
func TestPublishNestedRefusesADestinationOutsideTheRoot(t *testing.T) {
	out := t.TempDir()
	outside := t.TempDir()
	payload := []byte("escape attempt")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	staging := filepath.Join(out, ".tantu-staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(staging, "escape.part")
	if err := os.WriteFile(part, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenFile(part, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	dest := filepath.Join(outside, "written-anyway.txt")
	if _, _, err := publishPartFile(source, part, dest, out, digest, int64(len(payload))); err == nil {
		t.Fatal("publishPartFile wrote outside the output directory")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("a refused publication still created the file: %v", err)
	}
}

// TestPublishNestedNeverOverwrites pins the collision rule for the new path: a
// second send of the same directory produces a second tree rather than
// replacing files the user already has.
func TestPublishNestedNeverOverwrites(t *testing.T) {
	out := t.TempDir()
	payload := []byte("first")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	publish := func(body string) string {
		t.Helper()
		staging := filepath.Join(out, ".tantu-staging")
		if err := os.MkdirAll(staging, 0o700); err != nil {
			t.Fatal(err)
		}
		part := filepath.Join(staging, "collide.part")
		if err := os.WriteFile(part, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		source, err := os.OpenFile(part, os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		dest := filepath.Join(out, "project", "notes.txt")
		published, err := finalizePartFile(source, part, dest, out, digest, int64(len(body)))
		if err != nil {
			t.Fatalf("finalizePartFile: %v", err)
		}
		return published
	}

	first := publish("first")
	second := publish("first")
	if first == second {
		t.Fatalf("a second send replaced the first: both published to %q", first)
	}
	if data, err := os.ReadFile(first); err != nil || string(data) != "first" {
		t.Errorf("the first file changed: %q err=%v", data, err)
	}
	if !strings.Contains(filepath.Base(second), "(") {
		t.Errorf("the disambiguated name %q does not follow the documented shape", filepath.Base(second))
	}
}

// TestPublishFlatPathIsUnchanged guards the blast radius. A single-file send
// must not start being routed through the nested publisher: that path carries
// essentially every transfer the product performs, and it should keep the
// exact code it has always run.
func TestPublishFlatPathIsUnchanged(t *testing.T) {
	out := t.TempDir()
	payload := []byte("flat")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	staging := filepath.Join(out, ".tantu-staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(staging, "flat.part")
	if err := os.WriteFile(part, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenFile(part, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	dest := filepath.Join(out, "notes.txt")
	published, err := finalizePartFile(source, part, dest, out, digest, int64(len(payload)))
	if err != nil {
		t.Fatalf("finalizePartFile: %v", err)
	}
	if published != dest {
		t.Errorf("published %q, want %q", published, dest)
	}
	if _, err := os.Stat(filepath.Join(out, ".tantu-staging", "flat.part")); !os.IsNotExist(err) {
		t.Errorf("the staging partial survived: %v", err)
	}
	// Routing: a flat destination must not be sent through the nested
	// publisher, which creates directories and addresses a different root.
	if _, err := finalizePartFile(source, part, filepath.Join(out, "..", "escaped.txt"), out, digest, int64(len(payload))); err == nil {
		t.Error("a destination outside the output directory was published")
	}
}
