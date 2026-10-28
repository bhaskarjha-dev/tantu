package main

// Red proofs for the repository inventory gate.
//
// The rule this repository keeps: *a new gate must be able to fail.* A
// gate that has never been seen to fail is a gate that has never been
// tested, and a gate that cannot fail is worse than no gate -- it reports
// green forever.
//
// Each test below drives the check over a throwaway repository with the
// defect written into it, and passes only when the check complains.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// inventoryProblems runs the inventory check over a repository and returns
// the problems it found.
//
// The check reads git's index and the inventory document, so it is
// exercised the way it runs: over a real repository.
func inventoryProblems(t *testing.T, dir string) []string {
	t.Helper()
	files, err := loadFiles(dir)
	if err != nil {
		t.Fatalf("load files: %v", err)
	}
	problems := checkRepoInventory(dir, files)
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, p.problem)
	}
	return out
}

// writeInventory writes an inventory document into a throwaway repository.
func writeInventory(t *testing.T, dir, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(repoInventoryDoc))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for the inventory: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write the inventory: %v", err)
	}
}

// bodyOf reads the inventory out of a throwaway repository.
func bodyOf(t *testing.T, dir string) string {
	t.Helper()
	files, err := loadFiles(dir)
	if err != nil {
		t.Fatalf("load files: %v", err)
	}
	doc, ok := files[repoInventoryDoc]
	if !ok {
		t.Fatal("the inventory document is missing from the throwaway repository")
	}
	return doc.body
}

// trackedOf reads the tracked file list out of a throwaway repository.
func trackedOf(t *testing.T, dir string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("read the tracked file list: %v", err)
	}
	return strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
}

// The classifying rows of a throwaway inventory, and its summary.
//
// The sections matter: a row under "## Root" names a file at the root, and
// a row under "## `cmd/tantu/`" names one in that directory. That is how a
// file name is resolved when two files in the repository share one -- and
// this repository has three called main.go.
func fixtureInventory(summaryRows string, extra string) string {
	return "# Inventory\n\n" +
		"The repository is 2 tracked files.\n\n" +
		"## Root\n\n" +
		"| `go.mod` | **EXTREMELY IMPORTANT** | the build |\n\n" +
		"## `cmd/tantu/`\n\n" +
		"| `main.go` | **EXTREMELY IMPORTANT** | the entry point |\n\n" +
		extra +
		summaryRows
}

// A file that is tracked and not classified is the defect this gate
// exists for: a new file appears, nobody says what it is for, and the
// inventory quietly stops describing the repository.
func TestRepoInventoryReportsAnUnclassifiedFile(t *testing.T) {
	dir := initRepo(t, "go.mod", "cmd/tantu/main.go")
	// go.mod is deliberately NOT classified: it is the file that appeared
	// and that nobody has said what it is for.
	writeInventory(t, dir, "# Inventory\n\n"+
		"The repository is 2 tracked files.\n\n"+
		"## `cmd/tantu/`\n\n"+
		"| `main.go` | **EXTREMELY IMPORTANT** | the entry point |\n\n"+
		"| `Root` | 1 | 1 | 0 | 0 | 0 |\n"+
		"| `cmd/tantu/` | 1 | 1 | 0 | 0 | 0 |\n"+
		"| **Total** | **2** | **2** | **0** | **0** | **0** |\n")

	problems := inventoryProblems(t, dir)
	if len(problems) == 0 {
		t.Fatal("a tracked file that the inventory does not classify was accepted")
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "go.mod") {
		t.Errorf("the failure does not name the unclassified file: %s", joined)
	}
}

// The other direction of drift: the table classifies a file that has since
// been renamed or deleted. A table that describes a file which is not
// there is a claim about nothing.
func TestRepoInventoryReportsAFileThatNoLongerExists(t *testing.T) {
	dir := initRepo(t, "go.mod")
	writeInventory(t, dir, "# Inventory\n\n"+
		"The repository is 1 tracked files.\n\n"+
		"## Root\n\n"+
		"| `go.mod` | **EXTREMELY IMPORTANT** | the build |\n"+
		"| `cmd/tantu/gone.go` | **EXTREMELY IMPORTANT** | it used to be here |\n"+
		"| `Root` | 1 | 1 | 0 | 0 | 0 |\n")

	problems := inventoryProblems(t, dir)
	if len(problems) == 0 {
		t.Fatal("the inventory was accepted while classifying a file that does not exist")
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "gone.go") {
		t.Errorf("the failure does not name the file that no longer exists: %s", joined)
	}
}

// A complete and current inventory passes, so the rule cannot be quietly
// satisfied by failing everything.
func TestRepoInventoryAcceptsACompleteInventory(t *testing.T) {
	dir := initRepo(t, "go.mod", "cmd/tantu/main.go")
	writeInventory(t, dir, fixtureInventory(
		"| `Root` | 1 | 1 | 0 | 0 | 0 |\n"+
			"| `cmd/tantu/` | 1 | 1 | 0 | 0 | 0 |\n"+
			"| **Total** | **2** | **2** | **0** | **0** | **0** |\n",
		""))

	if problems := inventoryProblems(t, dir); len(problems) > 0 {
		t.Fatalf("a complete inventory was reported:\n  %v", problems)
	}
}

// The inventory's own prose names things that are NOT tracked source
// paths -- the build output it tells you to delete, a glob describing a
// set of files, a directory of dependencies. None of those may be treated
// as a claim about a file in the repository.
func TestRepoInventoryIgnoresGlobsAndUntrackedArtefacts(t *testing.T) {
	dir := initRepo(t, "go.mod")
	writeInventory(t, dir, "# Inventory\n\n"+
		"The repository is 1 tracked files.\n\n"+
		"## Root\n\n"+
		"| `go.mod` | **EXTREMELY IMPORTANT** | the build |\n"+
		"| `Root` | 1 | 1 | 0 | 0 | 0 |\n"+
		"| **Total** | **1** | **1** | **0** | **0** | **0** |\n\n"+
		"A glob is not a path: `tools/*/main_test.go`.\n"+
		"The useless things: `dist/`, `temp/`, `tools/uxtest/node_modules/`, `docgate.exe`.\n")

	if problems := inventoryProblems(t, dir); len(problems) > 0 {
		t.Fatalf("the check reported globs and untracked artefacts as claims about files:\n  %v", problems)
	}
}

// A gate that cannot read its own input must say so rather than pass.
// This is the rule the encoding gate broke by shipping unable to detect
// its own damage.
func TestRepoInventoryRefusesToPassWhenGitCannotBeRead(t *testing.T) {
	empty := t.TempDir() // not a repository
	problems := checkRepoInventory(empty, map[string]file{})
	if len(problems) == 0 {
		t.Fatal("the check reported success outside a git repository, having examined nothing")
	}
	if !strings.Contains(problems[0].problem, "must not pass without running") {
		t.Errorf("the failure does not say it examined nothing: %s", problems[0].problem)
	}
}

// The inventory document itself must exist. A missing document is not a
// passing state; it is an unchecked claim.
func TestRepoInventoryReportsAMissingDocument(t *testing.T) {
	dir := initRepo(t, "go.mod")
	problems := inventoryProblems(t, dir)
	if len(problems) == 0 {
		t.Fatal("the check passed with no inventory document to check")
	}
	if !strings.Contains(problems[0], "must not pass without running") {
		t.Errorf("the failure does not say it examined nothing: %s", problems[0])
	}
}

// The real repository must pass, so the rule cannot be quietly satisfied
// by an inventory that names nothing.
func TestRepoInventoryAcceptsThisRepository(t *testing.T) {
	dir, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find the repository root: %v", err)
	}
	files, err := loadFiles(dir)
	if err != nil {
		t.Fatalf("load files: %v", err)
	}
	if problems := checkRepoInventory(dir, files); len(problems) > 0 {
		t.Fatalf("this repository's inventory does not match the repository:\n  %v", problems)
	}
}

// The number in the opening paragraph must be the real one.
//
// It is the first number a reader sees, and it is the one that goes
// stale first: every file added makes it wrong, and nothing about a
// wrong number in a paragraph looks wrong.
func TestTheInventoryProseCountIsChecked(t *testing.T) {
	problems := checkInventoryProseCount("The repository is 290 tracked files.", 298)
	if len(problems) == 0 {
		t.Fatal("a wrong count in the opening paragraph was accepted")
	}
	if !strings.Contains(problems[0].problem, "290") {
		t.Errorf("the failure does not name the wrong number: %s", problems[0].problem)
	}
	if problems := checkInventoryProseCount("The repository is 298 tracked files.", 298); len(problems) > 0 {
		t.Fatalf("a right count was reported:\n  %v", problems)
	}
	// A document that does not state the count at all is an unchecked
	// claim, not a passing one.
	if problems := checkInventoryProseCount("# Inventory\n", 298); len(problems) == 0 {
		t.Fatal("a document that states no count was accepted")
	}
}

// A class spelled two ways is a class nobody can count.
//
// The first version of this repository's inventory wrote "**EXTREMELY**"
// in some tables and "**EXTREMELY IMPORTANT**" in others, and the
// counting script could not tell which was which. The gate caught it, so
// the vocabulary is now closed.
func TestTheInventoryClassVocabularyIsChecked(t *testing.T) {
	problems := checkInventoryVocabulary("# Inventory\n\n| `go.mod` | **EXTREMELY** | the build |\n")
	if len(problems) == 0 {
		t.Fatal("a class spelled as a shorthand was accepted")
	}
	if !strings.Contains(problems[0].problem, "EXTREMELY") {
		t.Errorf("the failure does not name the offending class: %s", problems[0].problem)
	}
	// The four real names are accepted, and an ordinary bold phrase is not
	// mistaken for a class.
	clean := "# Inventory\n\n| `go.mod` | **EXTREMELY IMPORTANT** | the build |\n" +
		"| `go.sum` | **VERY IMPORTANT** | the build |\n" +
		"| `LICENSE` | **IMPORTANT** | the licence |\n" +
		"| `dist/` | **USELESS** | build output |\n" +
		"| `a` | **What breaks without it** | a column header |\n"
	if problems := checkInventoryVocabulary(clean); len(problems) > 0 {
		t.Fatalf("the four real class names were rejected:\n  %v", problems)
	}
}

// The summary table must be what the rows beneath it say.
//
// The counts in the summary were written by hand, and the hand-written
// total was 297 while the repository held 298.
func TestTheInventorySummaryIsCheckedAgainstItsOwnRows(t *testing.T) {
	dir := initRepo(t, "go.mod", "cmd/tantu/main.go")

	// A summary that agrees with its rows passes.
	writeInventory(t, dir, fixtureInventory(
		"| `Root` | 1 | 1 | 0 | 0 | 0 |\n"+
			"| `cmd/tantu/` | 1 | 1 | 0 | 0 | 0 |\n"+
			"| **Total** | **2** | **2** | **0** | **0** | **0** |\n",
		""))
	if problems := checkInventorySummary(bodyOf(t, dir), trackedOf(t, dir)); len(problems) > 0 {
		t.Fatalf("a summary that matches its rows was reported:\n  %v", problems)
	}

	// A summary that does not is reported.
	writeInventory(t, dir, fixtureInventory(
		"| `Root` | 1 | 0 | 0 | 1 | 0 |\n"+
			"| `cmd/tantu/` | 1 | 1 | 0 | 0 | 0 |\n"+
			"| **Total** | **2** | **1** | **0** | **1** | **0** |\n",
		""))
	problems := checkInventorySummary(bodyOf(t, dir), trackedOf(t, dir))
	if len(problems) == 0 {
		t.Fatal("a summary that disagreed with its own rows was accepted")
	}
	if !strings.Contains(problems[0].problem, "Root") {
		t.Errorf("the failure does not name the row that drifted: %s", problems[0].problem)
	}
}

// A total that is hand-edited is a total that is wrong the moment one row
// changes.
func TestTheInventoryTotalMustBeTheSumOfItsRows(t *testing.T) {
	dir := initRepo(t, "go.mod", "cmd/tantu/main.go")
	writeInventory(t, dir, fixtureInventory(
		"| `Root` | 1 | 1 | 0 | 0 | 0 |\n"+
			"| `cmd/tantu/` | 1 | 1 | 0 | 0 | 0 |\n"+
			"| **Total** | **5** | **2** | **0** | **0** | **0** |\n",
		""))

	problems := checkInventorySummary(bodyOf(t, dir), trackedOf(t, dir))
	if len(problems) == 0 {
		t.Fatal("a total that was not the sum of its rows was accepted")
	}
	if !strings.Contains(problems[0].problem, "total row") {
		t.Errorf("the failure does not say the total is wrong: %s", problems[0].problem)
	}
}
