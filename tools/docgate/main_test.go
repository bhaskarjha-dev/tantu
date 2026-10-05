package main

// Self-verification for the documentation gate.
//
// This file exists because of a recorded failure in this repository: the
// encoding gate shipped unable to detect its own damage, and a gate that always
// reports green is worse than no gate, because it converts "the docs were
// checked" into an assumption. Every predicate below is therefore exercised
// against deliberately broken input and must fail, and the tests also pin the
// behaviour that makes the gate honest in the first place: it reports failure
// rather than silence when its own inputs are missing or unparseable.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFiles builds a file set from a map of path to body.
func fakeFiles(bodies map[string]string) map[string]file {
	out := make(map[string]file, len(bodies))
	for path, body := range bodies {
		out[path] = file{rel: path, body: body}
	}
	return out
}

// TestCheckConstantFailsOnAChangedValue is the red proof for the numeric claims:
// a limit that doubles in the code must turn the gate red even though the
// documentation still says the old number.
func TestCheckConstantFailsOnAChangedValue(t *testing.T) {
	claim := constantClaim{
		Symbol: "test.MaxBytes",
		File:   "pkg/a.go",
		Decl:   `MaxBytes\s+int64\s*=\s*((?:\d+\s*(?:\*|<<)\s*)*\d+)`,
		Value:  5 * 1024 * 1024 * 1024,
		Docs:   []string{"README.md"},
		Phrase: "up to 5GB",
	}
	files := fakeFiles(map[string]string{
		"pkg/a.go":  "const MaxBytes int64 = 5 * 1024 * 1024 * 1024",
		"README.md": "Sends up to 5GB.",
	})
	if f, ok := checkConstant(claim, files); !ok {
		t.Fatalf("a matching code and document were reported as drift: %s", f.problem)
	}

	// The code moves; the doc does not.
	files["pkg/a.go"] = file{rel: "pkg/a.go", body: "const MaxBytes int64 = 10 * 1024 * 1024 * 1024"}
	f, ok := checkConstant(claim, files)
	if ok {
		t.Fatal("the limit doubled in the code and the gate still passed")
	}
	if !strings.Contains(f.problem, "10") || !strings.Contains(f.problem, "5368709120") {
		t.Errorf("the failure does not name both values, so a reader cannot tell which moved: %q", f.problem)
	}

	// The doc moves; the code does not.
	files["pkg/a.go"] = file{rel: "pkg/a.go", body: "const MaxBytes int64 = 5 * 1024 * 1024 * 1024"}
	files["README.md"] = file{rel: "README.md", body: "Sends up to 10GB."}
	if _, ok := checkConstant(claim, files); ok {
		t.Fatal("the documented ceiling changed and the gate still passed")
	}
}

// TestCheckConstantFailsOnARewrittenDeclaration covers the silent case: a
// declaration rewritten into a form the gate cannot evaluate must be reported,
// not quietly skipped. A pattern that stops matching is the most likely way for
// this gate to rot into a no-op.
func TestCheckConstantFailsOnARewrittenDeclaration(t *testing.T) {
	claim := constantClaim{
		Symbol: "test.MaxBytes",
		File:   "pkg/a.go",
		Decl:   `MaxBytes\s+int64\s*=\s*((?:\d+\s*(?:\*|<<)\s*)*\d+)`,
		Value:  1024,
	}
	for name, body := range map[string]string{
		"renamed identifier":     "const MaxSize int64 = 5 * 1024 * 1024 * 1024\n",
		"expression is a call":   "const MaxBytes int64 = someHelper(5)",
		"expression uses a name": "const MaxBytes int64 = kib * 1024",
		"declaration moved type": "const MaxBytes = 5 * 1024 * 1024 * 1024",
	} {
		t.Run(name, func(t *testing.T) {
			files := fakeFiles(map[string]string{"pkg/a.go": body, "README.md": ""})
			if _, ok := checkConstant(claim, files); ok {
				t.Fatalf("an unresolvable declaration passed silently:\n%s", body)
			}
		})
	}

	// A file that is not there at all.
	if _, ok := checkConstant(claim, fakeFiles(map[string]string{"README.md": ""})); ok {
		t.Fatal("a missing source file passed silently")
	}
}

// TestEvalIntExpression covers the arithmetic, including the form the first
// draft of this gate got wrong: capturing only the first factor.
func TestEvalIntExpression(t *testing.T) {
	cases := []struct {
		expr string
		want int64
		ok   bool
	}{
		{"5 * 1024 * 1024 * 1024", 5 << 30, true},
		{"10 * 1024 * 1024", 10 << 20, true},
		{"2000", 2000, true},
		{"8 * 1024 * 1024 * 1024", 8 << 30, true},
		{"1 << 30", 1 << 30, true},
		{"1<<30", 1 << 30, true},
		{"5*gib", 0, false},
		{"", 0, false},
		{"5 *", 0, false},
		{"* 5", 0, false},
		{"someCall()", 0, false},
	}
	for _, tc := range cases {
		got, ok := evalIntExpression(tc.expr)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("evalIntExpression(%q) = %d, %v; want %d, %v", tc.expr, got, ok, tc.want, tc.ok)
		}
	}
}

// TestCheckPhraseFailsInBothDirections proves a renamed identifier is caught
// even though no number changed.
func TestCheckPhraseFailsInBothDirections(t *testing.T) {
	claim := phraseClaim{
		Doc:     "README.md",
		Phrases: []string{"127.0.0.1:9876"},
		File:    "internal/hub/hub.go",
		Symbols: []string{"DefaultWebAddr"},
		Note:    "the dashboard address",
	}
	good := fakeFiles(map[string]string{
		"README.md":           "binds 127.0.0.1:9876",
		"internal/hub/hub.go": "const DefaultWebAddr = \"x\"\n",
	})
	if _, ok := checkPhrase(claim, good); !ok {
		t.Fatal("a matching phrase and symbol were reported as drift")
	}

	// The identifier is renamed.
	bad := fakeFiles(map[string]string{
		"README.md":           "binds 127.0.0.1:9876",
		"internal/hub/hub.go": "const LoopbackAddr = \"x\"\n",
	})
	if _, ok := checkPhrase(claim, bad); ok {
		t.Fatal("the documented claim points at an identifier that no longer exists and the gate passed")
	}

	// The documentation stops stating it.
	missing := fakeFiles(map[string]string{
		"README.md":           "binds a loopback port",
		"internal/hub/hub.go": "const DefaultWebAddr = \"x\"\n",
	})
	if _, ok := checkPhrase(claim, missing); ok {
		t.Fatal("the document no longer states the claim and the gate passed")
	}

	// A missing source file.
	if _, ok := checkPhrase(claim, fakeFiles(map[string]string{"README.md": "127.0.0.1:9876"})); ok {
		t.Fatal("a missing source file passed silently")
	}
}

// TestCheckDocumentedFlagsFailsBothWays is the gate on the thing users type.
func TestCheckDocumentedFlagsFailsBothWays(t *testing.T) {
	claim := documentedFlag{Flag: "--flatten", File: "cmd/send.go", Decl: `"flatten"`, Doc: "README.md"}
	good := fakeFiles(map[string]string{
		"cmd/send.go": `fs.Bool("flatten", false, "")`,
		"README.md":   "use --flatten",
	})
	if _, ok := checkDocumentedFlags([]documentedFlag{claim}, good); !ok {
		t.Fatal("a real documented flag was reported as drift")
	}

	renamed := fakeFiles(map[string]string{
		"cmd/send.go": `fs.Bool("flat", false, "")`,
		"README.md":   "use --flatten",
	})
	if _, ok := checkDocumentedFlags([]documentedFlag{claim}, renamed); ok {
		t.Fatal("the documentation tells a user to type a flag that was renamed, and the gate passed")
	}

	undocumented := fakeFiles(map[string]string{
		"cmd/send.go": `fs.Bool("flatten", false, "")`,
		"README.md":   "send a directory",
	})
	if _, ok := checkDocumentedFlags([]documentedFlag{claim}, undocumented); ok {
		t.Fatal("a real flag is documented nowhere and the gate passed")
	}
}

// TestSubcommandNameParsing covers the shapes the README table actually uses,
// including the two that broke the first draft of this parser.
func TestSubcommandNameParsing(t *testing.T) {
	cases := []struct {
		cell   string
		want   string
		wantOK bool
	}{
		{"`tantu`", "", true},
		{"`tantu hub`", "hub", true},
		{"`tantu send <content>`", "send", true},
		{"`tantu open <url>`", "open", true},
		{"`tantu wrap -- <cmd>`", "wrap", true},
		{"`tantu transfer clear --yes`", "transfer", true},
		{"`tantu transfers`", "transfers", true},
		{"`node`", "", false},
		{"", "", false},
		{"`tantu --json`", "", false},
		{"`tantu -v`", "", false},
	}
	for _, tc := range cases {
		got, ok := subcommandName(tc.cell)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("subcommandName(%q) = %q, %v; want %q, %v", tc.cell, got, ok, tc.want, tc.wantOK)
		}
	}
}

// TestIsTableSeparator covers the markdown rule, which the first parser read as
// a command row.
func TestIsTableSeparator(t *testing.T) {
	for _, line := range []string{"|---|---|", "|:---|:---|", "| --- | --- |", "|:---:|:---|"} {
		if !isTableSeparator(line) {
			t.Errorf("isTableSeparator(%q) = false", line)
		}
	}
	for _, line := range []string{"| `tantu hub` | x |", "|", "||", "| a | - |", ""} {
		if isTableSeparator(line) {
			t.Errorf("isTableSeparator(%q) = true", line)
		}
	}
}

// TestHelpCommandsIgnoresUsageBlock pins the section boundary. Reading the Usage
// block as a command list makes the gate demand documentation for usage text,
// which is how a parser like this starts reporting nonsense.
func TestHelpCommandsIgnoresUsageBlock(t *testing.T) {
	help := `tantu - Zero-configuration encrypted peer-to-peer thread

Usage:
  tantu                         (starts the Unified Symmetric Hub)
  tantu <command> [arguments]
  tantu [flags] <url>           (implicit open for $BROWSER compatibility)
Commands:
  hub           Start the Unified Symmetric Hub (default when run with no arguments)
  open <url>    Send an OAuth authorization URL through the bridge
  version       Print tantu version
Flags:
  -v, --version Print version and exit
`
	commands := helpCommands(help)
	for _, want := range []string{"hub", "open", "version"} {
		if !commands[want] {
			t.Errorf("helpCommands did not find %q: %v", want, commands)
		}
	}
	if commands["tantu"] {
		t.Error("helpCommands read the bare `tantu` usage line as a command")
	}
	if commands["--version"] {
		t.Error("helpCommands read a flag as a command")
	}
	if commands["Print"] {
		t.Error("helpCommands kept parsing past the Commands section")
	}
}

// TestRunRefusesToPassOnAnEmptyRepository is the anti-false-green gate. A gate
// pointed at a directory with nothing in it must fail, not succeed quietly.
func TestRunRefusesToPassOnAnEmptyRepository(t *testing.T) {
	empty := t.TempDir()
	if err := run([]string{empty}); err == nil {
		t.Fatal("docgate reported success against an empty directory")
	} else if !strings.Contains(err.Error(), "nothing to check") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
}

// TestRunOverTheRealRepositoryIsGreen is the positive control: the gate must
// pass on the repository as it stands, or every negative test above is
// meaningless.
func TestRunOverTheRealRepositoryIsGreen(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Skipf("cannot locate the repository root from %s: %v", mustGetwd(), err)
	}
	if err := run([]string{root}); err != nil {
		t.Fatalf("docgate reported drift in the repository as committed: %v", err)
	}
}

// TestEveryConstantClaimIsWellFormed keeps the claim table itself honest: a
// pattern that cannot compile, or a document that does not exist, would
// otherwise fail at run time with a confusing message.
func TestEveryConstantClaimIsWellFormed(t *testing.T) {
	if len(constantClaims) < 5 {
		t.Fatalf("only %d constant claim(s); the table has shrunk and the gate is checking less than it claims", len(constantClaims))
	}
	seen := map[string]bool{}
	for _, c := range constantClaims {
		if c.Symbol == "" || c.File == "" || c.Decl == "" || c.Phrase == "" || len(c.Docs) == 0 {
			t.Errorf("incomplete claim: %+v", c)
			continue
		}
		if seen[c.Symbol] {
			t.Errorf("duplicate claim for %s", c.Symbol)
		}
		seen[c.Symbol] = true
		if c.Value <= 0 {
			t.Errorf("%s claims a non-positive value %d", c.Symbol, c.Value)
		}
	}
	for _, p := range phraseClaims {
		if len(p.Phrases) == 0 || len(p.Symbols) == 0 || p.File == "" || p.Doc == "" {
			t.Errorf("incomplete phrase claim: %+v", p)
		}
	}
	for _, f := range documentedFlags {
		if !strings.HasPrefix(f.Flag, "--") {
			t.Errorf("documented flag %q is not spelled as a long flag", f.Flag)
		}
	}
}

// TestClaimFilesExist makes a missing or renamed source file a test failure
// rather than a run-time surprise on CI.
func TestClaimFilesExist(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Skipf("cannot locate the repository root: %v", err)
	}
	files, err := loadFiles(root)
	if err != nil {
		t.Fatalf("loadFiles: %v", err)
	}
	for _, c := range constantClaims {
		if _, ok := files[c.File]; !ok {
			t.Errorf("constant claim %s names a file that does not exist: %s", c.Symbol, c.File)
		}
		for _, doc := range c.Docs {
			if _, ok := files[doc]; !ok {
				t.Errorf("constant claim %s names a document that does not exist: %s", c.Symbol, doc)
			}
		}
	}
	for _, p := range phraseClaims {
		for _, path := range []string{p.File, p.Doc} {
			if _, ok := files[path]; !ok {
				t.Errorf("phrase claim %q names a file that does not exist: %s", p.Note, path)
			}
		}
	}
	for _, f := range documentedFlags {
		for _, path := range []string{f.File, f.Doc} {
			if _, ok := files[path]; !ok {
				t.Errorf("flag claim %s names a file that does not exist: %s", f.Flag, path)
			}
		}
	}
	for _, v := range documentedEnvVars {
		if _, ok := files[v.File]; !ok {
			t.Errorf("environment claim %s names a file that does not exist: %s", v.Name, v.File)
		}
	}
}

// TestLoadFilesSkipsGeneratedTrees keeps the gate from reading a build output
// directory and reporting drift against binaries that are not tracked.
func TestLoadFilesSkipsGeneratedTrees(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"internal/hub", "dist", "temp", "tools/uxtest/node_modules"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "x.go"), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := loadFiles(root)
	if err != nil {
		t.Fatalf("loadFiles: %v", err)
	}
	if _, ok := files["internal/hub/x.go"]; !ok {
		t.Error("a source file under internal/ was skipped")
	}
	for _, skipped := range []string{"dist/x.go", "temp/x.go", "tools/uxtest/node_modules/x.go"} {
		if _, ok := files[skipped]; ok {
			t.Errorf("loadFiles read %s, which is generated", skipped)
		}
	}
}

// findRepoRoot walks up from the working directory to the module root.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 10; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}

func mustGetwd() string {
	dir, _ := os.Getwd()
	return dir
}
