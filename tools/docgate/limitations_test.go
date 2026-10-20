package main

// The register's row-shape check, self-verified.
//
// The register is a markdown TABLE, and a table row is split on every pipe
// that is not escaped. A row whose prose contains a literal pipe -- a code
// span quoting `run: |`, say -- therefore becomes a row with an extra
// column, and the register's last column stops being the last column.
//
// That happened in row 1.8, and nothing reported it: the text reads
// correctly, and only the rendering is wrong. The property that would have
// caught it had been written and then REMOVED, on the grounds that a MINIMUM
// count "could not be made to fail". An EXACT count can, because the table's
// header says how many cells a row has.
//
// Every case below is exercised against deliberately broken input and must
// fail, because a gate that always reports green is worse than no gate.

import (
	"strings"
	"testing"
)

const registerHeader = "| # | Limitation | Severity | Status | Where it is visible |\n" +
	"|---|---|---|---|---|\n"

// registerBody builds a register with the given numbered rows.
func registerBody(rows ...string) string {
	return registerHeader + strings.Join(rows, "\n") + "\n"
}

func TestRegisterCheckAcceptsWellFormedRows(t *testing.T) {
	files := fakeFiles(map[string]string{
		"docs/KNOWN-LIMITATIONS.md": registerBody(
			"| 1.1 | **A closed limitation.** | Gap | Closed | `docs/RELEASE.md` |",
			"| 1.2 | **An open one.** | Limit | Open | `internal/hub/hub.go` |",
		),
	})
	if f, ok := checkLimitationsRegister(files); !ok {
		t.Fatalf("a well-formed register was reported as malformed: %v", f)
	}
}

// TestRegisterCheckFailsOnAnUnescapedPipe is the red proof for the property
// that was removed and brought back.
//
// The row below quotes `run: |` without escaping the pipe, which is exactly
// what row 1.8 did.
func TestRegisterCheckFailsOnAnUnescapedPipe(t *testing.T) {
	files := fakeFiles(map[string]string{
		"docs/KNOWN-LIMITATIONS.md": registerBody(
			"| 1.8 | **The gate scans `run: |` blocks.** | Gap | Closed | `a.go` |",
		),
	})
	if f, ok := checkLimitationsRegister(files); ok {
		t.Fatal("a row with an unescaped pipe was accepted: the renderer splits it into an extra " +
			"column, and the register's last column stops being the last column")
	} else if !strings.Contains(f.problem, "row-shape problems") {
		t.Errorf("the failure does not say what is wrong with the row: %v", f)
	}
}

// TestRegisterCheckAcceptsAnEscapedPipe is the other direction: the fix is to
// escape the pipe, and the check must accept the escaped form.
func TestRegisterCheckAcceptsAnEscapedPipe(t *testing.T) {
	files := fakeFiles(map[string]string{
		"docs/KNOWN-LIMITATIONS.md": registerBody(
			"| 1.8 | **The gate scans `run: \\|` blocks.** | Gap | Closed | `a.go` |",
		),
	})
	if f, ok := checkLimitationsRegister(files); !ok {
		t.Fatalf("a row with an escaped pipe was reported as malformed: %v", f)
	}
}

// TestRegisterCheckStillFailsOnAMergedRow guards the property the new one
// sits next to: a check that catches the new class must not have lost the old
// one.
func TestRegisterCheckStillFailsOnAMergedRow(t *testing.T) {
	files := fakeFiles(map[string]string{
		"docs/KNOWN-LIMITATIONS.md": registerBody(
			"| 1.1 | **A closed limitation.** | Gap | Closed | `a.go` || 4.1 | ~~Another~~ | Gap | Closed | `b.go` |",
		),
	})
	if f, ok := checkLimitationsRegister(files); ok {
		t.Fatal("two rows merged into one line were accepted, which is the failure that deleted a " +
			"closed limitation from the register across two batches")
	} else if !strings.Contains(f.problem, "row-shape") && !strings.Contains(f.problem, "no numbered rows") {
		t.Errorf("the failure does not say what is wrong: %v", f)
	}
}

// TestRegisterCheckStillFailsOnADuplicateID is the other neighbour: a
// closure must be tellable from a reopening.
func TestRegisterCheckStillFailsOnADuplicateID(t *testing.T) {
	files := fakeFiles(map[string]string{
		"docs/KNOWN-LIMITATIONS.md": registerBody(
			"| 1.1 | **A closed limitation.** | Gap | Closed | `a.go` |",
			"| 1.1 | **The same id again.** | Gap | Open | `b.go` |",
		),
	})
	if _, ok := checkLimitationsRegister(files); ok {
		t.Fatal("a duplicated row id was accepted, so a closure cannot be told from a reopening")
	}
}

// TestRegisterCheckRefusesToPassWithoutRows is the property that makes a
// green mean something: a gate that finds nothing because it is looking in
// the wrong place is green for the worst reason.
func TestRegisterCheckRefusesToPassWithoutRows(t *testing.T) {
	files := fakeFiles(map[string]string{
		"docs/KNOWN-LIMITATIONS.md": "there are no rows here at all\n",
	})
	if f, ok := checkLimitationsRegister(files); ok {
		t.Fatalf("a register with no rows was reported as well formed: %v", f)
	}
}

// TestCountUnescapedPipes is the unit under the property.
//
// The cases are rows with THREE cells, so a pipe inside a cell's prose is an
// EXTRA separator: "| a | b |" has three, and "| a `run: |` b | c |" has
// four, which is one too many for the table.
func TestCountUnescapedPipes(t *testing.T) {
	for _, tc := range []struct {
		line string
		want int
		why  string
	}{
		{line: "| a | b |", want: 3, why: "three separators make four cells"},
		{line: "| a `run: |` b | c |", want: 4, why: "an unescaped pipe in a cell adds a separator"},
		{line: "| a `run: \\|` b | c |", want: 3, why: "an escaped pipe does not add a separator"},
		{line: "| a `run: \\\\|` b | c |", want: 4, why: "two backslashes make the pipe unescaped again"},
	} {
		if got := countUnescapedPipes(tc.line); got != tc.want {
			t.Errorf("countUnescapedPipes(%q) = %d, want %d: %s", tc.line, got, tc.want, tc.why)
		}
	}
}
