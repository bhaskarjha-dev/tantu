package main

// The repository inventory gate.
//
// The inventory (docs/REPO-INVENTORY.md) claims a class and a consequence
// for every tracked path, and it carries a summary table of how many files
// fall into each class. Both are claims about the repository, and both
// decay on their own: a new file appears, a class is spelled two ways, a
// count is edited by hand.
//
// So both are checked, and the check is mechanical on purpose. It cannot
// say whether a class is the RIGHT class -- that is judgement, and it
// stays with a reviewer. What it can say is that the table is a complete
// and current picture of what is in the repository, which is the property
// that decays fastest.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// repoInventoryDoc is the document that classifies every tracked path.
const repoInventoryDoc = "docs/REPO-INVENTORY.md"

// inventoryClasses are the four classes, spelled exactly. A document that
// writes "**EXTREMELY**" instead of "**EXTREMELY IMPORTANT**" has not
// given a reader a class it can look up, and a counting script cannot
// count it -- which is how this repository ended up with a summary table
// that said one thing and its rows said another.
var inventoryClasses = []string{
	"EXTREMELY IMPORTANT",
	"VERY IMPORTANT",
	"IMPORTANT",
	"USELESS",
}

// inventoryArea is one row of the summary table and the paths it counts.
//
// The exclusion is a prefix rather than part of the pattern because Go's
// regexp engine has no negative lookahead: `internal/bridge/` and
// `internal/bridge/testdata/` are counted separately, and a pattern that
// tried to say "everything under bridge except testdata" would not
// compile.
type inventoryArea struct {
	name    string
	regex   string
	exclude string
}

var inventoryAreas = []inventoryArea{
	{"Root", `^[^/]+$`, ""},
	{".github/workflows/", `^\.github/workflows/`, ""},
	{"cmd/tantu/", `^cmd/tantu/`, ""},
	{"internal/bridge/", `^internal/bridge/`, "internal/bridge/testdata/"},
	{"internal/bridge/testdata/", `^internal/bridge/testdata/`, ""},
	{"internal/browser/", `^internal/browser/`, ""},
	{"internal/discovery/", `^internal/discovery/`, ""},
	{"internal/drop/", `^internal/drop/`, ""},
	{"internal/hub/", `^internal/hub/`, ""},
	{"internal/pairing/", `^internal/pairing/`, ""},
	{"internal/protocol/", `^internal/protocol/`, ""},
	{"internal/testutil/", `^internal/testutil/`, ""},
	{"internal/transport/", `^internal/transport/`, ""},
	{"internal/webui/", `^internal/webui/`, ""},
	{"tools/docgate/", `^tools/docgate/`, ""},
	{"tools/encgate/", `^tools/encgate/`, ""},
	{"tools/uxtest/", `^tools/uxtest/`, ""},
	{"tools/wfcheck/", `^tools/wfcheck/`, ""},
	{"docs/", `^docs/`, ""},
}

// checkRepoInventory keeps the repository inventory honest.
//
// Both directions are checked. Every tracked path must be classified, and
// every path the inventory names must still exist. A new file that is not
// classified fails the gate; a file that is deleted without being removed
// from the table fails the gate too.
func checkRepoInventory(root string, files map[string]file) []failure {
	doc, ok := files[repoInventoryDoc]
	if !ok {
		return []failure{failf(repoInventoryDoc,
			"the repository inventory is missing, so the claim that every tracked file has been "+
				"classified cannot be checked. It must not pass without running.")}
	}
	body := doc.body

	// git is the input, as it is for checkTrackedArtefacts: a missing or
	// failing git is reported rather than skipped, because a check that
	// quietly examines nothing is the failure this repository has now met
	// in three separate tools.
	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		return []failure{failf("(git)", "could not read the tracked file list (%v), so the inventory check "+
			"examined nothing. It must not pass without running.", err)}
	}
	tracked := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(tracked) == 0 || (len(tracked) == 1 && tracked[0] == "") {
		return []failure{failf("(git)", "git reported no tracked files, so the inventory check examined "+
			"nothing. It must not pass without running.")}
	}

	var problems []failure

	// Every tracked path must be classified. The resolution is the same
	// one the summary uses -- section directory first, then a unique leaf
	// -- so the two can never disagree about which file a name means.
	classes := inventoryClassMap(body, tracked)
	var unclassified []string
	for _, rel := range tracked {
		if rel == "" {
			continue
		}
		if _, ok := classes[rel]; !ok {
			unclassified = append(unclassified, rel)
		}
	}
	if len(unclassified) > 0 {
		problems = append(problems, failf(repoInventoryDoc,
			"%d tracked path(s) are not classified in the inventory: %s. Add them, or delete the file.",
			len(unclassified), strings.Join(unclassified, ", ")))
	}

	// And every path the inventory names must still exist. This catches the
	// other direction of drift: a table that classifies a file which has
	// since been renamed or deleted, which reads as a claim about a file
	// that is not there.
	for _, named := range inventoryPaths(body) {
		if named == repoInventoryDoc {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(named))); err != nil {
			problems = append(problems, failf(repoInventoryDoc,
				"the inventory classifies %q, which does not exist in the repository. A table that "+
					"describes a file which is not there is a claim about nothing.", named))
		}
	}

	// The class vocabulary must be exactly the four names.
	problems = append(problems, checkInventoryVocabulary(body)...)

	// The count in the opening paragraph must be the real one. It is the
	// first number a reader sees, and it is the one that goes stale
	// first: every file added makes it wrong, and nothing about a wrong
	// number in a paragraph looks wrong.
	problems = append(problems, checkInventoryProseCount(body, len(tracked))...)

	// And the summary table must be what the tables say it is.
	problems = append(problems, checkInventorySummary(body, tracked)...)

	return problems
}

// checkInventoryProseCount checks the number the document opens with.
//
// "The repository is 298 tracked files" is a claim about the repository
// written in a paragraph, and a paragraph is the one place a gate cannot
// reach by counting rows. It is also the number that goes stale first.
func checkInventoryProseCount(body string, tracked int) []failure {
	re := regexp.MustCompile(`(?i)the repository is (\d+) tracked files`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		return []failure{failf(repoInventoryDoc,
			"the inventory does not state how many tracked files the repository has, so the first "+
				"number a reader sees is not a claim the gate can check")}
	}
	if n := atoi(m[1]); n != tracked {
		return []failure{failf(repoInventoryDoc,
			"the inventory opens by saying the repository has %d tracked files; it has %d. A number "+
				"in a paragraph goes stale the moment a file is added, and nothing about a wrong "+
				"number in a paragraph looks wrong.", n, tracked)}
	}
	return nil
}

// checkInventoryVocabulary requires every class to be spelled one of the
// four ways.
//
// It is a small rule and it caught a real inconsistency the first time it
// ran: the root and package tables spelled the classes "**EXTREMELY**"
// and "**VERY**" while the summary and the definition used the full
// names. A reader could not tell whether those were the same classes,
// and no script could count them.
//
// The test is "is this an all-caps phrase, and is it one of the four?".
// An ordinary bold phrase in the prose is not all-caps, so it is left
// alone; "EXTREMELY" on its own is all-caps and is not one of the four,
// so it is reported.
func checkInventoryVocabulary(body string) []failure {
	// Code spans are not classes. This document explains the shorthand it
	// retired, and it has to be able to SAY "**EXTREMELY**" without the
	// gate reading its own explanation as a mistake.
	scanned := regexp.MustCompile("`[^`]*`").ReplaceAllString(body, "")
	classish := regexp.MustCompile(`\*\*([A-Z][A-Z ]*)\*\*`)
	var problems []failure
	for _, m := range classish.FindAllStringSubmatch(scanned, -1) {
		cand := strings.TrimSpace(m[1])
		if !isAllCaps(cand) {
			continue // an ordinary bold phrase, not a class
		}
		known := false
		for _, c := range inventoryClasses {
			if cand == c {
				known = true
				break
			}
		}
		if !known {
			problems = append(problems, failf(repoInventoryDoc,
				"the inventory uses the class %q, which is not one of the four classes it defines "+
					"(%s). A class spelled two ways is a class nobody can count.",
				cand, strings.Join(inventoryClasses, ", ")))
		}
	}
	return problems
}

// isAllCaps reports whether a phrase is written entirely in capitals --
// which is what a class name in this document looks like, and what an
// ordinary bold phrase does not.
func isAllCaps(s string) bool {
	letters := 0
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			letters++
		case r == ' ':
		default:
			return false
		}
	}
	return letters > 0
}

// checkInventorySummary recomputes the summary table from the inventory's
// own rows and compares it with what the table says.
//
// A summary table that has drifted from the rows beneath it is worse than
// no summary: it is the first thing a reader looks at, and it is the one
// thing that is not checked by reading.
func checkInventorySummary(body string, tracked []string) []failure {
	classes := inventoryClassMap(body, tracked)

	// What the tables say.
	type counts struct{ files, extreme, very, important, useless int }
	computed := map[string]counts{}
	for _, area := range inventoryAreas {
		re := regexp.MustCompile(area.regex)
		c := counts{}
		for _, rel := range tracked {
			if rel == "" || !re.MatchString(rel) {
				continue
			}
			if area.exclude != "" && strings.HasPrefix(rel, area.exclude) {
				continue
			}
			c.files++
			switch classes[rel] {
			case "EXTREMELY IMPORTANT":
				c.extreme++
			case "VERY IMPORTANT":
				c.very++
			case "IMPORTANT":
				c.important++
			case "USELESS":
				c.useless++
			}
		}
		computed[area.name] = c
	}

	// What the summary says. The row is:
	//   | `Area` | files | extremely | very | important | useless |
	// The area name may or may not be in backticks, and the numbers may
	// be bold, because the total row is bold.
	rowRe := regexp.MustCompile("^\\|\\s*`?([^`|]+)`?\\s*\\|\\s*\\**(\\d+)\\**\\s*\\|\\s*\\**(\\d+)\\**\\s*\\|\\s*\\**(\\d+)\\**\\s*\\|\\s*\\**(\\d+)\\**\\s*\\|\\s*\\**(\\d+)\\**\\s*\\|")
	var problems []failure
	seen := map[string]bool{}
	stated := map[string]counts{}
	for _, line := range strings.Split(body, "\n") {
		m := rowRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		name := strings.TrimSpace(strings.Trim(strings.TrimSpace(m[1]), "`*"))
		got := counts{
			files:     atoi(m[2]),
			extreme:   atoi(m[3]),
			very:      atoi(m[4]),
			important: atoi(m[5]),
			useless:   atoi(m[6]),
		}
		if name == "Total" {
			stated["Total"] = got
			continue
		}
		if _, ok := computed[name]; !ok {
			continue // a row of some other table
		}
		seen[name] = true
		want := computed[name]
		if got != want {
			problems = append(problems, failf(repoInventoryDoc,
				"the summary row for %q says %d file(s) (%d extremely, %d very, %d important, %d useless), "+
					"but the tables beneath it classify %d file(s) (%d extremely, %d very, %d important, "+
					"%d useless). A summary that has drifted from its own tables is the first thing a "+
					"reader looks at and the one thing reading does not check.",
				name, got.files, got.extreme, got.very, got.important, got.useless,
				want.files, want.extreme, want.very, want.important, want.useless))
		}
	}
	for _, area := range inventoryAreas {
		// An area with no tracked files needs no row: there is nothing
		// to count. An area with files and no row is a row of numbers
		// that nobody can check.
		if computed[area.name].files > 0 && !seen[area.name] {
			problems = append(problems, failf(repoInventoryDoc,
				"the summary table has no row for %q, so the %d file(s) in that area are not counted anywhere.",
				area.name, computed[area.name].files))
		}
	}

	// The total row must be the sum of the rows above it, and the number
	// of tracked files. A total that is hand-edited is a total that is
	// wrong the moment one row changes.
	var sum counts
	for _, area := range inventoryAreas {
		c := computed[area.name]
		sum.files += c.files
		sum.extreme += c.extreme
		sum.very += c.very
		sum.important += c.important
		sum.useless += c.useless
	}
	if total, ok := stated["Total"]; ok {
		if total != sum {
			problems = append(problems, failf(repoInventoryDoc,
				"the summary's total row says %d file(s) (%d extremely, %d very, %d important, %d useless), "+
					"but the rows above it add up to %d file(s) (%d extremely, %d very, %d important, %d useless).",
				total.files, total.extreme, total.very, total.important, total.useless,
				sum.files, sum.extreme, sum.very, sum.important, sum.useless))
		}
	} else {
		problems = append(problems, failf(repoInventoryDoc,
			"the summary table has no total row, so the whole table can be read without anything to check "+
				"it against"))
	}
	return problems
}

// inventoryClassMap works out which class the inventory gives each tracked
// path.
//
// It reads the document's own rows: a row that carries a class assigns that
// class to every tracked file it names. A file named only by its leaf name
// is resolved WITHIN ITS SECTION'S DIRECTORY, because a table under
// "internal/drop/" that says "staging.go" plainly means
// internal/drop/staging.go.
//
// That resolution is not a convenience. This repository has three files
// called main.go, two called hub.go and three called e2e_test.go, so a
// leaf name on its own does not say which file it means -- and a table
// that cannot say which file it means is a table nobody can check. The
// first version of this gate assumed the names were unique, and the
// assumption was wrong the moment it was run.
func inventoryClassMap(body string, tracked []string) map[string]string {
	out := map[string]string{}
	trackedSet := map[string]bool{}
	leafPaths := map[string][]string{}
	for _, rel := range tracked {
		if rel == "" {
			continue
		}
		trackedSet[rel] = true
		leaf := filepath.Base(rel)
		leafPaths[leaf] = append(leafPaths[leaf], rel)
	}

	// The class of a row: the last class token in it wins, because the
	// reason column may quote one.
	classRe := regexp.MustCompile(`\*\*(` + strings.Join(inventoryClasses, "|") + `)\*\*`)
	tokenRe := regexp.MustCompile("`([^`]+)`")
	headingRe := regexp.MustCompile(`^#{2,4}\s`)

	// The directory the current section is about. A heading names it in
	// backticks ("## `internal/drop/` -- the data path"); the root section
	// has no directory and its names are repository-root names.
	dir := ""
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if headingRe.MatchString(trimmed) {
			dir = ""
			for _, tok := range tokenRe.FindAllStringSubmatch(trimmed, -1) {
				if strings.HasSuffix(tok[1], "/") {
					dir = tok[1]
					break
				}
			}
			if strings.HasPrefix(trimmed, "## Root") {
				dir = ""
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		classes := classRe.FindAllStringSubmatch(trimmed, -1)
		if len(classes) == 0 {
			continue
		}
		cls := classes[len(classes)-1][1]
		for _, tok := range tokenRe.FindAllStringSubmatch(trimmed, -1) {
			name := tok[1]
			// A leaf name resolves INSIDE the section's directory
			// first. It has to: `README.md` is a tracked path in its
			// own right at the root, and `tools/uxtest/README.md` is a
			// second one. A row under tools/uxtest/ that says
			// "README.md" means the uxtest one, and a row under the
			// root section that says "README.md" means the root one.
			// Without the section first, the uxtest row was quietly
			// classifying the root file and the uxtest file was left
			// unclassified.
			if !strings.Contains(name, "/") && dir != "" {
				cand := dir + name
				if trackedSet[cand] {
					out[cand] = cls
					continue
				}
			}
			if trackedSet[name] {
				out[name] = cls
				continue
			}
			if strings.Contains(name, "/") {
				// A full path, or a path relative to the section's
				// directory.
				cand := name
				if !trackedSet[cand] && dir != "" {
					cand = dir + name
				}
				if trackedSet[cand] {
					out[cand] = cls
				}
				continue
			}
			// A leaf name in a section that does not say which
			// directory it is about: only a unique leaf will do.
			if len(leafPaths[name]) == 1 {
				out[leafPaths[name][0]] = cls
			}
		}
	}
	return out
}

// atoi parses a decimal integer, treating anything else as zero.
func atoi(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// inventoryPaths extracts the repository paths an inventory document names.
//
// Only tokens that look like tracked source paths are considered: a
// backticked token containing a directory separator and ending in one of
// this repository's source extensions. That keeps the check away from the
// document's own prose -- the untracked artefacts it names (`dist/`,
// `temp/`, `node_modules/`) and the binary it tells you to delete.
func inventoryPaths(body string) []string {
	sourceExts := map[string]bool{
		".go": true, ".md": true, ".css": true, ".html": true, ".js": true,
		".mjs": true, ".cjs": true, ".yaml": true, ".yml": true, ".json": true,
		".mod": true, ".sum": true, ".txt": true, ".conf": true,
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(body, -1) {
		tok := m[1]
		if !strings.Contains(tok, "/") {
			continue
		}
		// A glob is a statement about a set of files, not about one
		// path, and there is no single file to stat.
		if strings.ContainsAny(tok, "*?") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(tok))
		if !sourceExts[ext] {
			continue
		}
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}
