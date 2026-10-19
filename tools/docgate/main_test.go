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
	"os/exec"
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

// TestCheckForbiddenFailsOnAReintroducedInstruction is the gate on the thing
// a document must stop saying.
//
// The pairing instructions once told a user to compare a 6-character hex
// value -- the certificate fingerprint's first six digits, which
// pairing.SASCode's own doc forbids as a security decision. No phrase check
// could have caught that: the README did mention the SAS, correctly, in its
// security section. The wrong sentence was a second sentence about the same
// concept, and only a check on the forbidden sentence can see it.
func TestCheckForbiddenFailsOnAReintroducedInstruction(t *testing.T) {
	claim := forbiddenPhrase{
		Doc:     "README.md",
		Phrases: []string{"6-character Short Authentication String"},
		Note:    "the hex SAS instruction",
	}
	good := fakeFiles(map[string]string{
		"README.md": "Compare the code on this screen with the code on the other machine's screen.",
	})
	if _, ok := checkForbidden(claim, good); !ok {
		t.Fatal("a document that no longer states the forbidden instruction was reported as drift")
	}

	bad := fakeFiles(map[string]string{
		"README.md": "Both machines display a 6-character Short Authentication String (e.g. 0949b2).",
	})
	if _, ok := checkForbidden(claim, bad); ok {
		t.Fatal("the document still states the forbidden instruction and the gate passed")
	}

	// A missing document must not pass silently.
	if _, ok := checkForbidden(claim, fakeFiles(map[string]string{})); ok {
		t.Fatal("a missing document passed silently")
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
	for _, p := range forbiddenPhrases {
		if _, ok := files[p.Doc]; !ok {
			t.Errorf("forbidden-phrase claim %q names a document that does not exist: %s", p.Note, p.Doc)
		}
		if len(p.Phrases) == 0 {
			t.Errorf("forbidden-phrase claim %q lists no phrase, so it can never fail", p.Note)
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

// ---- tracked artefacts ------------------------------------------------------

// initRepo creates a throwaway git repository containing the given paths and
// returns its root. The artefact check reads git's index rather than the working
// tree, so testing it any other way would test a different thing.
func initRepo(t *testing.T, paths ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", p, err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git is unavailable or refused in this sandbox (%v: %s); the check cannot be exercised",
				err, out)
		}
	}
	return dir
}

func artefactProblems(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, f := range checkTrackedArtefacts(dir) {
		out = append(out, f.doc+": "+f.problem)
	}
	return out
}

// The red proof for the rule that caught a real artefact: `t.err`, a committed
// dump of a Go test binary's usage message, was in the repository root and was
// none of the eleven files that belong there.
func TestTrackedArtefactsRejectsAStrayFileInTheRepositoryRoot(t *testing.T) {
	dir := initRepo(t, "go.mod", "README.md", "t.err")
	problems := artefactProblems(t, dir)
	if len(problems) == 0 {
		t.Fatal("a stray `t.err` in the repository root was accepted. This is the exact artefact that " +
			"was committed by accident: a fuzz command's stderr, redirected to a file, added wholesale")
	}
	if !strings.Contains(problems[0], "t.err") {
		t.Errorf("the problem does not name the offending file: %s", problems[0])
	}
	// It must also say what to do, because a bare "no" sends the reader to the
	// project history to work out the intended layout.
	if !strings.Contains(problems[0], "cmd/") || !strings.Contains(problems[0], "docs/") {
		t.Errorf("the problem does not say where source and documents belong: %s", problems[0])
	}
}

// The suffix rule is separate from the root rule, and reaches anywhere in the
// tree: `rejection-pages.html` was written into the repository root by
// TestExportRejectionPages and committed before .gitignore learned about it.
func TestTrackedArtefactsRejectsGeneratedSuffixesAnywhere(t *testing.T) {
	dir := initRepo(t, "go.mod", "docs/a.md", "internal/hub/coverage.out")
	problems := artefactProblems(t, dir)
	if len(problems) == 0 {
		t.Fatal("a tracked coverage.out was accepted; captured output is not source wherever it sits")
	}
	if !strings.Contains(problems[0], "internal/hub/coverage.out") {
		t.Errorf("the problem does not name the offending file: %s", problems[0])
	}
}

// The gate must not report success on a repository that is simply clean.
func TestTrackedArtefactsAcceptsACleanRepository(t *testing.T) {
	dir := initRepo(t,
		".gitignore", ".gitattributes", ".goreleaser.yaml",
		"AGENTS.md", "CHANGELOG.md", "LICENSE", "README.md", "SECURITY.md",
		"go.mod", "go.sum", "staticcheck.conf",
		"cmd/tantu/main.go", "internal/hub/hub.go", "docs/ARCHITECTURE.md",
		".github/workflows/ci.yml",
	)
	if problems := artefactProblems(t, dir); len(problems) > 0 {
		t.Fatalf("a clean repository was reported:\n  %v", problems)
	}
}

// A file that has been removed from the working tree but is still in the index
// is exactly the state a `git rm --cached` leaves behind, and it is still in
// every clone. The check reads the index on purpose, so this must hold.
func TestTrackedArtefactsReportsAFileRemovedOnlyFromTheWorkingTree(t *testing.T) {
	dir := initRepo(t, "go.mod", "stray.log")
	if err := os.Remove(filepath.Join(dir, "stray.log")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if problems := artefactProblems(t, dir); len(problems) == 0 {
		t.Fatal("a file deleted from the working tree but still in the index was accepted")
	}
}

// A gate that cannot read its own input must say so rather than pass. This is
// the rule that the encoding gate broke by shipping unable to detect its own
// damage, and it is asserted here for the artefact check specifically.
func TestTrackedArtefactsRefusesToPassWhenGitCannotBeRead(t *testing.T) {
	empty := t.TempDir() // not a repository
	problems := checkTrackedArtefacts(empty)
	if len(problems) == 0 {
		t.Fatal("the check reported success outside a git repository, having examined nothing")
	}
	if !strings.Contains(problems[0].problem, "must not pass without running") {
		t.Errorf("the failure does not say it examined nothing: %s", problems[0].problem)
	}
}

// The real repository must pass, so the rule cannot be quietly satisfied by
// shrinking the allowlist until it stops meaning anything.
func TestTrackedArtefactsAcceptsThisRepository(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Skipf("no module root above %s", mustGetwd())
	}
	if problems := artefactProblems(t, root); len(problems) > 0 {
		t.Fatalf("this repository has tracked artefacts:\n  %v", problems)
	}
}

// ---- documented local gates -------------------------------------------------

func documentedGateProblems(t *testing.T, readme, ci string) []string {
	t.Helper()
	files := map[string]file{
		"README.md":                {rel: "README.md", body: readme},
		".github/workflows/ci.yml": {rel: ".github/workflows/ci.yml", body: ci},
	}
	var out []string
	for _, f := range checkDocumentedGates(files) {
		out = append(out, f.doc+": "+f.problem)
	}
	return out
}

const readmeWithGates = `# Tantu

### Quality gates you can run locally

` + "```bash" + `
go test ./...
go run ./tools/encgate
go run ./tools/docgate
go run ./tools/wfcheck
` + "```" + `

## Build from Source
`

// The red proof for a real omission: wfcheck was in the CI quality job and in
// AGENTS.md, and not in the README's list, so the gate that stops release.yml
// breaking at tag time had no documented route to being run before a push.
func TestDocumentedGatesReportsAGateCIRunsButReadmeOmits(t *testing.T) {
	readme := "# Tantu\n\n### Quality gates you can run locally\n\n" +
		"```bash\ngo test ./...\ngo run ./tools/encgate\ngo run ./tools/docgate\n```\n\n## Next\n"
	ci := "jobs:\n  quality:\n    steps:\n      - run: go run ./tools/encgate\n" +
		"      - run: go run ./tools/docgate\n      - run: go run ./tools/wfcheck\n"
	problems := documentedGateProblems(t, readme, ci)
	if len(problems) == 0 {
		t.Fatal("a gate that CI runs and the README does not list was accepted")
	}
	if !strings.Contains(problems[0], "wfcheck") {
		t.Errorf("the problem does not name the gate: %s", problems[0])
	}
}

func TestDocumentedGatesAcceptsWhenEveryCIGateIsListed(t *testing.T) {
	ci := "jobs:\n  quality:\n    steps:\n      - run: go run ./tools/encgate\n" +
		"      - run: go run ./tools/docgate\n      - run: go run ./tools/wfcheck\n"
	if problems := documentedGateProblems(t, readmeWithGates, ci); len(problems) > 0 {
		t.Fatalf("a README that lists every gate was reported:\n  %v", problems)
	}
}

// The list must come from the workflow, not from a literal written here. A
// second list is the failure that produced this check: the two drifted, and the
// one that went stale was the one that was supposed to be checked.
func TestDocumentedGatesFollowsTheWorkflowRatherThanAHardcodedList(t *testing.T) {
	ci := "jobs:\n  quality:\n    steps:\n      - run: go run ./tools/encgate\n" +
		"      - run: go run ./tools/docgate\n      - run: go run ./tools/wfcheck\n" +
		"      - run: go run ./tools/newgate\n"
	problems := documentedGateProblems(t, readmeWithGates, ci)
	if len(problems) == 0 {
		t.Fatal("a gate added to CI and absent from the README was accepted; the check is reading a " +
			"hardcoded list rather than the workflow")
	}
	if !strings.Contains(problems[0], "newgate") {
		t.Errorf("the problem names %q rather than the newly added gate", problems[0])
	}
}

// Mentioning the tool somewhere else in the README does not count: the section
// has to be the one a contributor reads to decide what to run.
func TestDocumentedGatesRequiresTheGateInTheLocalSection(t *testing.T) {
	readme := "# Tantu\n\nSomewhere below, in a build recipe:\n\ngo run ./tools/encgate\ngo run ./tools/docgate\n" +
		"go run ./tools/wfcheck\n\n### Quality gates you can run locally\n\n```bash\ngo test ./...\n" +
		"go run ./tools/encgate\ngo run ./tools/docgate\n```\n\n## Next\n"
	ci := "jobs:\n  quality:\n    steps:\n      - run: go run ./tools/encgate\n" +
		"      - run: go run ./tools/docgate\n      - run: go run ./tools/wfcheck\n"
	if problems := documentedGateProblems(t, readme, ci); len(problems) == 0 {
		t.Fatal("a README naming the gate only outside the local-gates section was accepted")
	}
}

func TestDocumentedGatesRefusesToPassWithoutItsInputs(t *testing.T) {
	t.Run("no CI workflow", func(t *testing.T) {
		files := map[string]file{"README.md": {rel: "README.md", body: readmeWithGates}}
		problems := checkDocumentedGates(files)
		if len(problems) == 0 {
			t.Fatal("the check passed with no workflow to read")
		}
		if !strings.Contains(problems[0].problem, "must not pass without running") {
			t.Errorf("the failure does not say it examined nothing: %s", problems[0].problem)
		}
	})
	t.Run("no README", func(t *testing.T) {
		files := map[string]file{".github/workflows/ci.yml": {rel: ".github/workflows/ci.yml", body: "run: go run ./tools/wfcheck\n"}}
		if problems := checkDocumentedGates(files); len(problems) == 0 {
			t.Fatal("the check passed with no README to read")
		}
	})
	t.Run("no local-gates section", func(t *testing.T) {
		files := map[string]file{
			"README.md":                {rel: "README.md", body: "# Tantu\n\nNo section here.\n"},
			".github/workflows/ci.yml": {rel: ".github/workflows/ci.yml", body: "run: go run ./tools/wfcheck\n"},
		}
		if problems := checkDocumentedGates(files); len(problems) == 0 {
			t.Fatal("the check passed with no section for a contributor to read")
		}
	})
	t.Run("a workflow with no gates in it", func(t *testing.T) {
		files := map[string]file{
			"README.md":                {rel: "README.md", body: readmeWithGates},
			".github/workflows/ci.yml": {rel: ".github/workflows/ci.yml", body: "jobs:\n  build:\n    steps: []\n"},
		}
		if problems := checkDocumentedGates(files); len(problems) == 0 {
			t.Fatal("the check passed having extracted no gates at all")
		}
	})
}

func TestDocumentedGatesAcceptsThisRepository(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Skipf("no module root above %s", mustGetwd())
	}
	var problems []string
	for _, f := range checkDocumentedGates(map[string]file{
		"README.md":                {rel: "README.md", body: mustRead(t, filepath.Join(root, "README.md"))},
		".github/workflows/ci.yml": {rel: ".github/workflows/ci.yml", body: mustRead(t, filepath.Join(root, ".github", "workflows", "ci.yml"))},
	}) {
		problems = append(problems, f.doc+": "+f.problem)
	}
	if len(problems) > 0 {
		t.Fatalf("this repository does not document every gate CI runs:\n  %v", problems)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
