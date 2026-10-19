// Command docgate fails when a documented claim no longer matches the code it
// describes.
//
// This exists because of a recorded pattern, not a hypothetical one. Three
// separate false claims were found in one session (the encoding gate that could
// not detect its own damage, a comment describing a test that did not exist,
// "zero external dependencies"). Each was a *stated* property with nothing
// checking it, and the register's own entry for this gap says so: "there is no
// general check that a doc claim matches the code it describes".
//
// The scope here is deliberately the class of claim that is mechanically
// decidable, because a gate that guesses at prose becomes a gate people turn off:
//
//   - the CLI surface: every subcommand the binary advertises is documented,
//     and every documented subcommand exists;
//   - the flags the documentation tells a user to type actually exist;
//   - the numbers. A limit stated as "up to 5GB" is checked against the constant
//     that enforces it, in both directions - the code must still hold that value,
//     and the documentation must still state it;
//   - the ports, the config directory, and the environment variables.
//
// What this cannot check, and does not pretend to: whether a sentence is true,
// whether a caveat is still the right caveat, whether an example transcript
// matches current output. Those are judgement, and they stay with a reviewer.
//
// Self-verification is the load-bearing part. An earlier gate in this repository
// shipped unable to detect its own damage, which is worse than no gate: it
// reported green forever. So every predicate here is exercised in
// docgate_test.go against deliberately broken input and must fail, and the gate
// refuses to report success if it has nothing to compare.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run performs every check over the repository root.
func run(args []string) error {
	root := "."
	if len(args) > 0 {
		root = args[0]
	}
	files, err := loadFiles(root)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("docgate: found no files under %s; refusing to report success with nothing to check", root)
	}

	failures := 0
	report := func(failure failure) {
		failures++
		fmt.Fprintf(os.Stderr, "::error title=documentation drift::%s: %s\n", failure.doc, failure.problem)
	}

	for _, c := range constantClaims {
		if f, ok := checkConstant(c, files); !ok {
			report(f)
		}
	}
	for _, c := range phraseClaims {
		if f, ok := checkPhrase(c, files); !ok {
			report(f)
		}
	}
	for _, c := range forbiddenPhrases {
		if f, ok := checkForbidden(c, files); !ok {
			report(f)
		}
	}
	if f, ok := checkCommandSurface(files, root); !ok {
		report(f)
	}
	if f, ok := checkDocumentedFlags(documentedFlags, files); !ok {
		report(f)
	}
	if f, ok := checkLimitationsRegister(files); !ok {
		report(f)
	}
	if f, ok := checkEnvironmentVariables(files); !ok {
		report(f)
	}
	for _, f := range checkTrackedArtefacts(root) {
		report(f)
	}
	for _, f := range checkDocumentedGates(files) {
		report(f)
	}

	if failures > 0 {
		return fmt.Errorf("docgate: %d documented claim(s) no longer match the code", failures)
	}
	fmt.Printf("docgate: %d constant claim(s), %d documented phrase(s), %d forbidden phrase(s), the CLI command surface, the KNOWN-LIMITATIONS row shape, %d documented flag(s), %d environment variable(s) and every CI quality gate documented locally all match the code; %d tracked path(s) contain no generated artefacts\n",
		len(constantClaims), len(phraseClaims), len(forbiddenPhrases), len(documentedFlags), len(documentedEnvVars), countTrackedFiles(root))
	return nil
}

// countTrackedFiles reports the number of tracked paths, for the summary line.
// A failure here is reported by checkTrackedArtefacts already, so this only
// degrades the number to zero rather than hiding anything.
func countTrackedFiles(root string) int {
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, p := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if p != "" {
			n++
		}
	}
	return n
}

// file is one loaded tracked text file.
type file struct {
	rel  string
	body string
}

func loadFiles(root string) (map[string]file, error) {
	out := map[string]file{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "temp":
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".go", ".md", ".html", ".mjs", ".yml", ".yaml":
		default:
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "../") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[rel] = file{rel: rel, body: string(data)}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("docgate: walk %s: %w", root, err)
	}
	return out, nil
}

type failure struct {
	doc     string
	problem string
}

func failf(doc, format string, args ...any) failure {
	return failure{doc: doc, problem: fmt.Sprintf(format, args...)}
}

// ---- constant claims -------------------------------------------------------

// constantClaim ties a number the documentation states to the Go constant that
// enforces it. Both directions matter: the code must still hold the value, and
// the documentation must still state it. A limit that silently doubled, or a
// README that still advertises the old ceiling, are both drift.
type constantClaim struct {
	// Symbol is the Go identifier, used in messages.
	Symbol string
	// File is the source file the declaration must be in.
	File string
	// Decl matches the declaration and captures every integer factor of its
	// right-hand side, so `5 * 1024 * 1024 * 1024` resolves to 5 GiB rather
	// than to the 5 that happens to be written first.
	Decl string
	// Value is the resolved numeric value the product of those factors must be.
	Value int64
	// Docs are the files that must state this number, and Phrase is the text
	// they must contain. Every Docs entry must contain Phrase.
	Docs   []string
	Phrase string
}

const (
	gib = 1 << 30
	mib = 1 << 20
)

// constantClaims is the checked set. Each entry was added because the number is
// stated in user-facing prose, which is exactly where it silently rots.
var constantClaims = []constantClaim{
	{
		Symbol: "drop.DefaultMaxDropSize",
		File:   "internal/drop/receiver.go",
		Decl:   `DefaultMaxDropSize\s+int64\s*=\s*((?:\d+\s*(?:\*|<<)\s*)*\d+)`,
		Value:  5 * gib,
		Docs:   []string{"README.md", "docs/ARCHITECTURE.md"},
		Phrase: "up to 5GB",
	},
	{
		Symbol: "drop.DefaultMaxTextSize",
		File:   "internal/drop/receiver.go",
		Decl:   `DefaultMaxTextSize\s+int64\s*=\s*((?:\d+\s*(?:\*|<<)\s*)*\d+)`,
		Value:  10 * mib,
		Docs:   []string{"docs/ARCHITECTURE.md"},
		Phrase: "10 MiB",
	},
	{
		Symbol: "maxExpandedFiles",
		File:   "cmd/tantu/multifile.go",
		Decl:   `maxExpandedFiles\s*=\s*(\d+)`,
		Value:  2000,
		Docs:   []string{"docs/KNOWN-LIMITATIONS.md"},
		Phrase: "2,000 files",
	},
	{
		Symbol: "maxExpandedTotalBytes",
		File:   "cmd/tantu/multifile.go",
		Decl:   `maxExpandedTotalBytes\s+int64\s*=\s*((?:\d+\s*(?:\*|<<)\s*)*\d+)`,
		Value:  5 * gib,
		Docs:   []string{"docs/KNOWN-LIMITATIONS.md"},
		Phrase: "5 GiB",
	},
	{
		Symbol: "drop.DefaultRetainedPartialBudget",
		File:   "internal/drop/staging.go",
		Decl:   `DefaultRetainedPartialBudget\s+int64\s*=\s*((?:\d+\s*(?:\*|<<)\s*)*\d+)`,
		Value:  8 * gib,
		Docs:   []string{"README.md"},
		Phrase: "8 GiB",
	},
	{
		Symbol: "drop.DefaultRetainedPartialCount",
		File:   "internal/drop/staging.go",
		Decl:   `DefaultRetainedPartialCount\s*=\s*(\d+)`,
		Value:  1024,
		Docs:   []string{"README.md"},
		Phrase: "1,024",
	},
	{
		Symbol: "hub.MaxRelPathLength",
		File:   "internal/hub/relpath.go",
		Decl:   `MaxRelPathLength\s*=\s*(\d+)`,
		Value:  1024,
		Docs:   []string{"docs/SPEC-WIRE-VERSIONING.md"},
		Phrase: "bounded length and depth",
	},
	{
		Symbol: "drop.MaxRelPathDepth",
		File:   "internal/drop/types.go",
		Decl:   `MaxRelPathDepth\s*=\s*(\d+)`,
		Value:  24,
		Docs:   []string{"docs/ARCHITECTURE.md"},
		Phrase: "over-deep",
	},
}

// checkConstant resolves a claim against the loaded files.
func checkConstant(c constantClaim, files map[string]file) (failure, bool) {
	src, ok := files[c.File]
	if !ok {
		return failf(c.File, "%s: the file this constant is declared in is missing, so the claim cannot be checked", c.Symbol), false
	}
	re, err := regexp.Compile(c.Decl)
	if err != nil {
		return failf(c.File, "%s: the gate's own pattern is invalid (%v); fix the gate", c.Symbol, err), false
	}
	m := re.FindStringSubmatch(src.body)
	if m == nil {
		return failf(c.File, "%s: no declaration matching the gate's pattern was found, so the documented number cannot be verified", c.Symbol), false
	}
	got, ok := evalIntExpression(m[1])
	if !ok {
		return failf(c.File, "%s: the gate cannot resolve the declared expression %q; teach it that form rather than letting it pass", c.Symbol, m[1]), false
	}
	if got != c.Value {
		return failf(c.File, "%s is %d but the documentation states %d; update the code or the documentation, not this gate", c.Symbol, got, c.Value), false
	}
	for _, doc := range c.Docs {
		d, present := files[doc]
		if !present {
			return failf(doc, "%s is checked against this document but the document is missing", c.Symbol), false
		}
		if !strings.Contains(d.body, c.Phrase) {
			return failf(doc, "%s is %d but this document no longer states %q", c.Symbol, c.Value, c.Phrase), false
		}
	}
	return failure{}, true
}

// evalIntExpression multiplies the integer factors of a declaration's
// right-hand side. It accepts only `a * b * c` and `a << b`, which is what the
// size constants in this repository are written as; anything else is reported
// as unresolvable rather than guessed at, so a rewritten declaration turns the
// gate red instead of silently checking nothing.
func evalIntExpression(expr string) (int64, bool) {
	normalized := strings.ReplaceAll(expr, "<<", "*2^")
	normalized = strings.ReplaceAll(normalized, " ", "")
	parts := strings.Split(normalized, "*")
	total := int64(1)
	for _, part := range parts {
		if part == "" {
			return 0, false
		}
		// A shift arrives as the marker followed by the shift count, so the
		// count is what follows the marker.
		if shift, ok := strings.CutPrefix(part, "2^"); ok {
			n, err := strconv.Atoi(shift)
			if err != nil || n < 0 || n > 62 {
				return 0, false
			}
			total *= 1 << uint(n)
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, false
		}
		total *= int64(n)
	}
	return total, true
}

// ---- phrase claims ---------------------------------------------------------

// phraseClaim is a documented phrase that must stay anchored in the code it
// describes. Weaker than a constant check, and honestly labelled: it catches a
// renamed identifier that leaves the documentation pointing at nothing.
type phraseClaim struct {
	// Doc is the file stating the claim.
	Doc string
	// Phrases must all appear in Doc.
	Phrases []string
	// File is the source the claim is about.
	File string
	// Symbols must all appear in File.
	Symbols []string
	// Note is the one-line explanation printed on failure.
	Note string
}

var phraseClaims = []phraseClaim{
	{
		Doc:     "README.md",
		Phrases: []string{"~/.config/tantu/", "%APPDATA%\\tantu\\"},
		File:    "internal/pairing/store.go",
		Symbols: []string{"tantu", "APPDATA", ".config"},
		Note:    "the configuration directory the README names",
	},
	{
		Doc:     "docs/ARCHITECTURE.md",
		Phrases: []string{"127.0.0.1:9876"},
		File:    "internal/hub/hub.go",
		Symbols: []string{`DefaultWebAddr    = "127.0.0.1:9876"`, "startPort := 9876"},
		Note:    "the dashboard address and the port it starts from",
	},
	{
		Doc:     "README.md",
		Phrases: []string{"224.0.0.251:5353", "9879"},
		File:    "internal/discovery/discovery.go",
		Symbols: []string{"224.0.0.251", "5353", "DefaultBroadcastPort = 9879"},
		Note:    "the discovery multicast group and broadcast port",
	},
	{
		Doc:     "README.md",
		Phrases: []string{"--flatten"},
		File:    "cmd/tantu/send.go",
		Symbols: []string{"flattenFlag"},
		Note:    "the flag the README tells users to type for a flat directory send",
	},
	{
		// The README's discovery listing must match what the CLI prints, and
		// what it prints carries no SAS: discovery broadcasts no identity, so
		// any code shown there would be fabricated.
		Doc:     "README.md",
		Phrases: []string{"(192.168.0.244:9877, not yet paired)"},
		File:    "cmd/tantu/pair.go",
		Symbols: []string{"(%s, not yet paired)"},
		Note:    "the discovery listing the README shows, which must carry no SAS",
	},
	{
		// The pairing instructions must tell the user to compare against the
		// OTHER machine's screen. The code is one value shown on two screens;
		// a document that says otherwise is teaching a same-screen comparison.
		Doc:     "README.md",
		Phrases: []string{"Compare the code on this screen with the code on the **other machine's** screen"},
		File:    "cmd/tantu/pair.go",
		Symbols: []string{"Compare this code with the code shown on the remote screen"},
		Note:    "the pairing-code comparison the README describes, which the CLI's prompt implements",
	},
}

func checkPhrase(c phraseClaim, files map[string]file) (failure, bool) {
	doc, ok := files[c.Doc]
	if !ok {
		return failf(c.Doc, "this document is missing but is the subject of a phrase check"), false
	}
	for _, p := range c.Phrases {
		if !strings.Contains(doc.body, p) {
			return failf(c.Doc, "%q is no longer stated here (%s)", p, c.Note), false
		}
	}
	src, ok := files[c.File]
	if !ok {
		return failf(c.File, "%s: the source this document describes is missing", c.Note), false
	}
	for _, s := range c.Symbols {
		if !strings.Contains(src.body, s) {
			return failf(c.File, "%s: the documented claim (%s) points at %q, which no longer exists here", c.Note, strings.Join(c.Phrases, ", "), s), false
		}
	}
	return failure{}, true
}

// ---- forbidden phrases ------------------------------------------------------

// forbiddenPhrase is a statement a document must NOT make.
//
// A phrase check proves a document still says what it should. This proves it
// has stopped saying what it must not. The pairing instructions once told a
// user to compare "a 6-character Short Authentication String", which is the
// certificate fingerprint's first six hex digits -- a value
// pairing.SASCode's own doc forbids as a security decision, because it is 24
// bits, static and not bound to a session. No amount of "the README mentions
// the SAS" would have caught that, because it did mention it, correctly
// labelled as the thing it must not be confused with.
type forbiddenPhrase struct {
	// Doc is the file that must not contain the phrase.
	Doc string
	// Phrases must all be absent from Doc.
	Phrases []string
	// Note is the one-line explanation printed on failure.
	Note string
}

var forbiddenPhrases = []forbiddenPhrase{
	{
		Doc:     "README.md",
		Phrases: []string{"6-character Short Authentication String", "display a 6-character"},
		Note: "instructions to compare a hex value the product forbids as a security decision; " +
			"the pairing code is a session-bound word code",
	},
}

func checkForbidden(c forbiddenPhrase, files map[string]file) (failure, bool) {
	doc, ok := files[c.Doc]
	if !ok {
		return failf(c.Doc, "this document is missing but is the subject of a forbidden-phrase check"), false
	}
	for _, p := range c.Phrases {
		if strings.Contains(doc.body, p) {
			return failf(c.Doc, "%q is still stated here (%s)", p, c.Note), false
		}
	}
	return failure{}, true
}

// ---- the CLI surface -------------------------------------------------------

// subcommandName extracts the subcommand from the first cell of a CLI Reference
// row. The cell is prose-shaped - "`tantu send <content>`", "`tantu wrap -- <cmd>`",
// "`tantu transfer clear --yes`" - so the command word is taken as the first
// token after `tantu`, and anything after it (arguments, flags) is not part of
// the identity being compared.
//
// A bare `tantu` cell documents the default behaviour (run with no arguments),
// which is not a subcommand and does not appear in `--help` as one; it returns
// the empty name and is checked separately.
func subcommandName(cell string) (string, bool) {
	cell = strings.TrimSpace(cell)
	cell = strings.Trim(cell, "`")
	if !strings.HasPrefix(cell, "tantu") {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(cell, "tantu"))
	if rest == "" {
		return "", true
	}
	first := strings.Fields(rest)[0]
	if first == "" || !subcommandWord.MatchString(first) {
		return "", false
	}
	return first, true
}

// subcommandWord matches a CLI subcommand name.
var subcommandWord = regexp.MustCompile(`^[a-z][a-z-]*$`)

// advertisedCommand matches a subcommand line in `tantu --help`.
var advertisedCommand = regexp.MustCompile(`(?m)^\s{2,}([a-z][a-z-]*)\s+\S`)

// checkCommandSurface is bidirectional on purpose. A documented command that was
// removed sends a user to a usage error; a command that exists and is not
// documented is invisible to the person deciding whether they can do something.
func checkCommandSurface(files map[string]file, root string) (failure, bool) {
	readme, ok := files["README.md"]
	if !ok {
		return failf("README.md", "missing, so the documented CLI surface cannot be checked"), false
	}
	help := helpText(root)
	if strings.TrimSpace(help) == "" {
		return failf("cmd/tantu", "`tantu --help` produced no output; the command surface cannot be verified and the gate will not report success"), false
	}

	advertised := helpCommands(help)
	if len(advertised) < 10 {
		return failf("cmd/tantu", "only %d subcommand(s) parsed from `tantu --help`; the gate's parser is wrong and would pass vacuously", len(advertised)), false
	}

	documented := map[string]bool{}
	documentsDefault := false
	inTable := false
	for _, line := range strings.Split(readme.body, "\n") {
		if strings.HasPrefix(line, "| Command |") {
			inTable = true
			continue
		}
		if inTable && strings.HasPrefix(line, "|---") {
			continue
		}
		if inTable && !strings.HasPrefix(line, "|") {
			inTable = false
			continue
		}
		if !inTable {
			continue
		}
		if isTableSeparator(line) {
			continue
		}
		fields := strings.Split(strings.Trim(line, "|"), "|")
		if len(fields) < 2 {
			return failf("README.md", "the CLI Reference row %q has no description column, so the gate cannot tell it apart from a malformed row", strings.TrimSpace(line)), false
		}
		name, ok := subcommandName(fields[0])
		if !ok {
			return failf("README.md", "the CLI Reference row %q does not name a `tantu <subcommand>`, so the gate cannot tell what it documents", strings.TrimSpace(line)), false
		}
		if name == "" {
			// The default (no-arguments) entry. Running `tantu` with no
			// arguments starts the Hub, so the row is real; it is simply not a
			// subcommand and `--help` does not list it as one.
			documentsDefault = true
			continue
		}
		documented[name] = true
	}

	var undocumented, phantom []string
	for name := range advertised {
		if !documented[name] {
			undocumented = append(undocumented, name)
		}
	}
	for name := range documented {
		if !advertised[name] {
			phantom = append(phantom, name)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(phantom)
	if len(undocumented) > 0 {
		return failf("README.md", "%d subcommand(s) exist but are not in the CLI Reference table: %s", len(undocumented), strings.Join(undocumented, ", ")), false
	}
	if len(phantom) > 0 {
		return failf("README.md", "%d documented subcommand(s) no longer exist: %s", len(phantom), strings.Join(phantom, ", ")), false
	}
	if !documentsDefault {
		return failf("README.md", "the CLI Reference table has no row for the bare `tantu` command, but running tantu with no arguments starts the Hub"), false
	}
	return failure{}, true
}

// isTableSeparator reports whether a markdown table row is the |---|---| rule
// rather than content. Matching only a literal "|---" prefix misses the common
// "|---|---|" form, and treating the rule as a command row is how a table parser
// starts reporting garbage.
func isTableSeparator(line string) bool {
	trimmed := strings.Trim(strings.TrimSpace(line), "|")
	if trimmed == "" {
		return false
	}
	for _, cell := range strings.Split(trimmed, "|") {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			return false
		}
		for _, r := range cell {
			if r != '-' && r != ':' && r != ' ' {
				return false
			}
		}
	}
	return true
}

// helpCommands extracts the subcommand names from `tantu --help`.
//
// Only the Commands section is parsed. The Usage block above it also contains
// `tantu` and `tantu <command> [arguments]`, which are invocation shapes rather
// than commands, and reading them as commands makes the gate demand a
// documentation row for a row of usage text.
func helpCommands(help string) map[string]bool {
	commands := map[string]bool{}
	inCommands := false
	for _, line := range strings.Split(help, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "Commands:":
			inCommands = true
			continue
		case inCommands && (strings.HasPrefix(trimmed, "Flags:") || trimmed == ""):
			inCommands = false
			continue
		}
		if !inCommands {
			continue
		}
		if m := advertisedCommand.FindStringSubmatch(line); m != nil {
			commands[m[1]] = true
		}
	}
	return commands
}

// helpText runs `tantu --help` from the repository root.
//
// The working directory matters: `go run ./cmd/tantu` is resolved relative to
// it, and the gate is also exercised from `tools/docgate` by its own tests. A
// failure here is reported rather than ignored - a gate that silently skips its
// own most dynamic check stops checking anything the day the build breaks.
func helpText(root string) string {
	cmd := exec.Command("go", "run", "./cmd/tantu", "--help")
	cmd.Dir = root
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// ---- documented flags ------------------------------------------------------

// documentedFlag ties a flag the documentation tells a user to type to the file
// that declares it.
type documentedFlag struct {
	Flag string
	File string
	Decl string
	Doc  string
}

var documentedFlags = []documentedFlag{
	{Flag: "--flatten", File: "cmd/tantu/send.go", Decl: `"flatten"`, Doc: "README.md"},
	{Flag: "--idempotency-key", File: "cmd/tantu/send.go", Decl: `"idempotency-key"`, Doc: "README.md"},
	{Flag: "--json", File: "cmd/tantu/send.go", Decl: `"json"`, Doc: "docs/KNOWN-LIMITATIONS.md"},
	{Flag: "--peer", File: "cmd/tantu/send.go", Decl: `"peer"`, Doc: "README.md"},
	{Flag: "--text", File: "cmd/tantu/send.go", Decl: `"text"`, Doc: "README.md"},
	{Flag: "--ssh-fingerprint", File: "cmd/tantu/send.go", Decl: `"ssh-fingerprint"`, Doc: "README.md"},
	{Flag: "--print", File: "cmd/tantu/dashboard.go", Decl: `"print"`, Doc: "docs/DEV-RECORD.md"},
	{Flag: "--headless", File: "cmd/tantu/hub.go", Decl: `"headless"`, Doc: "README.md"},
	{Flag: "--server", File: "cmd/tantu/hub.go", Decl: `"server"`, Doc: "docs/ARCHITECTURE.md"},
	{Flag: "--auto-pair", File: "cmd/tantu/hub.go", Decl: `"auto-pair"`, Doc: "SECURITY.md"},
	{Flag: "--bundle-path", File: "cmd/tantu/doctor.go", Decl: `"bundle-path"`, Doc: "docs/UX-PLAN-MAP.md"},
}

// checkDocumentedFlags takes the table as an argument rather than reading the
// package-level one, so the predicate can be exercised against fabricated input.
// A checker that can only be run against the repository it checks cannot be
// shown to fail, and an unfailable checker is the failure mode this whole tool
// exists to prevent.
func checkDocumentedFlags(claims []documentedFlag, files map[string]file) (failure, bool) {
	for _, f := range claims {
		src, ok := files[f.File]
		if !ok {
			return failf(f.File, "%s is checked but the file declaring it is missing", f.Flag), false
		}
		if !strings.Contains(src.body, f.Decl) {
			return failf(f.File, "%s is documented but no flag named %s is declared here", f.Flag, f.Decl), false
		}
		doc, ok := files[f.Doc]
		if !ok {
			return failf(f.Doc, "%s is checked against this document but the document is missing", f.Flag), false
		}
		if !strings.Contains(doc.body, f.Flag) {
			return failf(f.Doc, "%s is a real flag but is no longer mentioned here", f.Flag), false
		}
	}
	return failure{}, true
}

// ---- environment variables -------------------------------------------------

// documentedEnvVars are the environment variables the documentation names, tied
// to the source that reads them. A renamed variable is invisible at runtime -
// the code simply falls back to its default - which is precisely why it has to
// be checked here.
var documentedEnvVars = []struct {
	Name   string
	File   string
	Symbol string
}{
	{Name: "GITHUB_TOKEN", File: ".github/workflows/release.yml", Symbol: "GITHUB_TOKEN"},
	{Name: "X-Tantu-IPC-Token", File: "docs/ARCHITECTURE.md", Symbol: "X-Tantu-IPC-Token"},
	{Name: "COSIGN_VERSION", File: ".github/workflows/release.yml", Symbol: "COSIGN_VERSION"},
	{Name: "SYFT_VERSION", File: ".github/workflows/release.yml", Symbol: "SYFT_VERSION"},
}

func checkEnvironmentVariables(files map[string]file) (failure, bool) {
	for _, v := range documentedEnvVars {
		doc, ok := files[v.File]
		if !ok {
			return failf(v.File, "%s is checked but the document is missing", v.Name), false
		}
		if !strings.Contains(doc.body, v.Symbol) {
			return failf(v.File, "%s is no longer named here", v.Name), false
		}
	}
	return failure{}, true
}

// ---- tracked artefacts ------------------------------------------------------

// repositoryRootFiles is every file that belongs directly in the repository
// root. The rule is an allowlist rather than a denylist of extensions, because
// the failure it catches has no extension signature: `t.err` is a two-kilobyte
// dump of a Go test binary's usage message, committed by accident when a fuzz
// command's stderr was redirected to it.
//
// The allowlist is the stronger form for a structural reason. The root of a Go
// repository has a small, knowable set of legitimate contents -- module files,
// licence, the entry documents, the CI and release configuration. A new source
// file belongs under cmd/ or internal/, and a new document under docs/. So
// anything else at the root is a decision someone should have to make
// deliberately, and the gate is that decision being asked for.
var repositoryRootFiles = map[string]string{
	".gitattributes":   "attributes",
	".gitignore":       "ignore rules",
	".goreleaser.yaml": "release configuration",
	"AGENTS.md":        "contributor rules",
	"CHANGELOG.md":     "release history",
	"LICENSE":          "licence",
	"README.md":        "entry document",
	"SECURITY.md":      "security policy",
	"go.mod":           "module definition",
	"go.sum":           "module checksums",
	"staticcheck.conf": "staticcheck configuration",
}

// generatedExtensions are suffixes that never belong in a source repository.
// Each one is here because this repository has produced one: `t.err` held a
// test binary's usage dump, and `rejection-pages.html` was written by
// TestExportRejectionPages and committed before .gitignore learned about it.
var generatedExtensions = map[string]string{
	".err":  "captured standard error",
	".out":  "captured output",
	".log":  "log capture",
	".prof": "profile output",
	".test": "compiled test binary",
	".tmp":  "temporary file",
	".bak":  "editor backup",
	".orig": "merge residue",
	".rej":  "merge residue",
}

// checkTrackedArtefacts reports tracked files that are build or capture output
// rather than source.
//
// It reads git's index rather than the working tree, because the question is
// what is *committed*. A generated file sitting untracked in a working
// directory is nobody's problem; the same file in the index is in every clone.
//
// git itself is the input, so a missing or failing git is reported rather than
// skipped. A check that quietly examines nothing is the failure this repository
// has now met in three separate tools, and the fourth is not a contribution.
func checkTrackedArtefacts(root string) []failure {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		return []failure{failf("(git)", "could not read the tracked file list (%v), so this check examined "+
			"nothing. It must not pass without running.", err)}
	}
	tracked := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(tracked) == 0 || (len(tracked) == 1 && tracked[0] == "") {
		return []failure{failf("(git)", "git reported no tracked files, so this check examined nothing. "+
			"It must not pass without running.")}
	}

	var problems []failure
	for _, rel := range tracked {
		if rel == "" {
			continue
		}
		if strings.Contains(rel, "/") {
			// Not at the root. Only the suffix rule applies, and only to the
			// suffixes below.
			if why, bad := generatedExtensions[strings.ToLower(filepath.Ext(rel))]; bad {
				problems = append(problems, failf(rel, "this is %s, not source. If it is generated, add it "+
					"to .gitignore and `git rm --cached` it; if it is deliberate, add it to generatedExtensions "+
					"in tools/docgate with the reason.", why))
			}
			continue
		}
		if _, known := repositoryRootFiles[rel]; !known {
			problems = append(problems, failf(rel, "this file sits in the repository root and is not one of "+
				"the %d files that belong there. Source belongs under cmd/ or internal/, documents under docs/. "+
				"If it is deliberate, add it to repositoryRootFiles in tools/docgate with its reason.",
				len(repositoryRootFiles)))
		}
	}
	return problems
}

// ---- the local gate list ----------------------------------------------------

// checkDocumentedGates verifies that every gate the CI quality job runs is
// listed in the README's "Quality gates you can run locally" section.
//
// The section exists to tell a contributor what to run before pushing. A gate
// that is added to CI and not to that list is a gate nobody runs locally, which
// is how a defect reaches a pull request that the author could have caught in
// thirty seconds. `wfcheck` was exactly that: added to the quality job, named in
// AGENTS.md, and absent from the README's list of four, so the one gate that
// keeps the release workflow from breaking at tag time had no documented route
// to being run at all.
//
// The list is derived from ci.yml rather than written out again here. A second
// literal list is precisely what drifted before, and it is why a version of this
// check that consulted one could have reported green while the thing it was
// checking was wrong.
func checkDocumentedGates(files map[string]file) []failure {
	ci, ok := files[".github/workflows/ci.yml"]
	if !ok {
		return []failure{failf(".github/workflows/ci.yml", "not found, so the documented-gate check "+
			"examined nothing. It must not pass without running.")}
	}
	readme, ok := files["README.md"]
	if !ok {
		return []failure{failf("README.md", "not found, so the documented-gate check examined nothing")}
	}

	gates := map[string]bool{}
	for _, m := range regexp.MustCompile(`go run \./(tools/([a-z]+))`).FindAllStringSubmatch(ci.body, -1) {
		gates["go run ./"+m[1]] = true
	}
	if len(gates) == 0 {
		return []failure{failf(".github/workflows/ci.yml", "no `go run ./tools/...` gate was found; the "+
			"extraction is broken, not the workflow")}
	}

	// Only the "run locally" section counts. README.md names the other tools
	// elsewhere -- a build recipe, the browser harness, the dependency
	// inventory -- and matching the whole file would make this vacuous.
	start := strings.Index(readme.body, "Quality gates you can run locally")
	if start < 0 {
		return []failure{failf("README.md", "no \"Quality gates you can run locally\" section, so there is "+
			"nothing for a contributor to run. The section is what tells someone what to run before pushing")}
	}
	section := readme.body[start:]
	if end := strings.Index(section, "\n## "); end > 0 {
		section = section[:end]
	}

	// Sorted so the message is stable from run to run.
	names := make([]string, 0, len(gates))
	for g := range gates {
		names = append(names, g)
	}
	sort.Strings(names)

	var problems []failure
	for _, g := range names {
		if !strings.Contains(section, g) {
			problems = append(problems, failf("README.md", "the CI quality job runs `%s` but the "+
				"\"Quality gates you can run locally\" section does not list it. A gate nobody is told "+
				"to run is a gate nobody runs", g))
		}
	}
	return problems
}
