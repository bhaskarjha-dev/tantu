package main

import (
	"fmt"
	"regexp"
	"strings"
)

// checkLimitationsRegister verifies the shape of docs/KNOWN-LIMITATIONS.md.
//
// It exists because that file is the one place this repository requires a release
// gate to be able to see, and because a row inserted without a trailing newline
// once merged two rows into one and deleted a **closed** limitation from the
// register with nothing reporting it, across two batches. The register's own
// header says "do not delete it", which a tool that cannot read its rows cannot
// help with.
//
// Three properties, because they fail differently:
//
//  1. no line carries two row ids  -- the bug that actually happened
//  2. row ids are unique across the document, so a closure cannot be confused
//     with a reopening
//
// A third property -- "a row carries at least three separators" -- was written and
// then removed. It could not be made to fail: a truncated final cell is not
// detectable by counting separators, and the break written for it was a no-op.
// A check that cannot fail is decorative, so it is not here.
//
// Structural, and structural on purpose. This is a shape property of a markdown
// table: the input is the file's own text, so reading it is the complete check
// rather than a sample of it.
func checkLimitationsRegister(files map[string]file) (failure, bool) {
	const path = "docs/KNOWN-LIMITATIONS.md"
	src, ok := files[path]
	if !ok {
		// Absent means skip, not fail. Returning ok=false here reported a failure
		// with an empty message -- a green-to-red with nothing to read -- which is
		// the failure mode this whole tool exists to prevent.
		return failure{}, true
	}

	leadingRow := regexp.MustCompile(`^\|\s*(\d+\.\d+)\s*\|`)
	anyRowID := regexp.MustCompile(`\|\s*(\d+\.\d+)\s*\|`)

	seen := map[string]string{}
	bad := 0
	rows := 0

	for i, line := range strings.Split(src.body, "\n") {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}

		// Property 1 first: a merged line must be reported as merged, not parsed as
		// one row with a corrupted cell. The run that motivated this produced
		// "has read it. || 4.1 | ~~Some peer actions..." on a single line.
		ids := anyRowID.FindAllStringSubmatch(trimmed, -1)
		if len(ids) > 1 {
			bad++
			if bad <= 5 {
				fmt.Printf("%s:%d: one line carries %d row ids (%s); rows have merged and a "+
					"limitation has been deleted from the register\n", path, lineNo, len(ids),
					joinRowIDs(ids))
			}
			continue
		}

		m := leadingRow.FindStringSubmatch(trimmed)
		if m == nil {
			continue // prose, a sub-row, or a header separator
		}
		rows++
		id := m[1]

		// Property 2.
		if where, dup := seen[id]; dup {
			bad++
			if bad <= 5 {
				fmt.Printf("%s:%d: row %s already appears at %s; ids must be unique, or a "+
					"closure cannot be told from a reopening\n", path, lineNo, id, where)
			}
			continue
		}
		seen[id] = fmt.Sprintf("line %d", lineNo)

	}

	if rows == 0 {
		// A gate that finds nothing because it is looking in the wrong place is
		// green for the worst reason. Same reasoning as the fuzz execs floor.
		fmt.Printf("%s: no numbered rows found; the checker has gone stale and is "+
			"measuring nothing\n", path)
		return failf(path, "no numbered rows found; the checker has gone stale and is measuring nothing"), false
	}

	if bad > 5 {
		fmt.Printf("%s: %d further row-shape problems not shown\n", path, bad-5)
	}
	if bad > 0 {
		return failf(path, "%d row-shape problems across %d numbered rows", bad, rows), false
	}
	return failure{}, true
}

func joinRowIDs(matches [][]string) string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return strings.Join(out, ", ")
}
