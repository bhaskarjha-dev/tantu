package main

import (
	"strings"
	"testing"
)

// Self-tests for the workflow gate.
//
// encgate and docgate are both tested against deliberately broken input, which is
// the lesson they exist to teach: a gate that cannot detect its own failure mode
// is worse than no gate. wfcheck was the one tool here without that, and it
// immediately justified the gap -- its job-shape check flagged every legitimate
// job name on the first run, because the condition included the job-name line
// itself. Nothing local had caught that either; the red proof did.

// A workflow shaped the way every one in this repository actually is.
const goodWorkflow = `name: CI
on:
  push:
    branches:
      - '**'
jobs:
  quality:
    name: Quality gates
    runs-on: ubuntu-latest
    steps:
      - name: gofmt
        run: |
          gofmt -l .
  release-artifact:
    name: Release artifact
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - name: Build
        run: |
          go build ./...
`

func TestJobShapeAcceptsAWellFormedWorkflow(t *testing.T) {
	if problems := checkJobShape(goodWorkflow); len(problems) != 0 {
		t.Errorf("a correct workflow was reported as broken:\n  %v", problems)
	}
}

func TestJobShapeCatchesAMisIndentedJob(t *testing.T) {
	// The exact mistake: a job written one level too deep, so its name becomes a
	// field of the job above it and the workflow stops loading entirely. This is
	// well-formed YAML, which is why a parser would not have caught it.
	broken := `jobs:
  quality:
    name: Quality gates
    runs-on: ubuntu-latest
    steps:
      - run: |
          go vet ./...
    fuzz:
      name: Fuzz
      runs-on: ubuntu-latest
      steps:
        - run: |
            go test -fuzz=Foo
  test:
    name: Test
    runs-on: ubuntu-latest
    steps:
      - run: |
          go test ./...
`
	problems := checkJobShape(broken)
	if len(problems) != 1 {
		t.Fatalf("expected exactly one problem, got %d: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "fuzz") {
		t.Errorf("the problem does not name the offending job: %s", problems[0])
	}
	if !strings.Contains(problems[0], "145") && !strings.Contains(problems[0], "line ") {
		t.Errorf("the problem does not carry a line number: %s", problems[0])
	}
	// It must not also condemn the legitimate jobs around it.
	for _, p := range problems {
		if strings.Contains(p, `"test"`) || strings.Contains(p, `"quality"`) {
			t.Errorf("a legitimate job was reported: %s", p)
		}
	}
}

func TestJobShapeReportsAMissingJobsKey(t *testing.T) {
	problems := checkJobShape("name: CI\non:\n  push:\n    branches:\n      - '**'\n")
	if len(problems) != 1 {
		t.Fatalf("a workflow with no jobs: key should report one problem, got %v", problems)
	}
	if !strings.Contains(problems[0], "jobs") {
		t.Errorf("the problem does not mention the missing key: %s", problems[0])
	}
}

func TestJobShapeIgnoresCommentsAndBlankLines(t *testing.T) {
	withNoise := "# leading comment\nname: CI\n\n" + goodWorkflow[len("name: CI\n"):]
	if problems := checkJobShape(withNoise); len(problems) != 0 {
		t.Errorf("comments and blank lines changed the result:\n  %v", problems)
	}
}

// Every job key the gate permits must actually be permitted, so that adding a
// supported key to a real workflow does not report a problem.
func TestJobShapeAllowsEveryDeclaredJobKey(t *testing.T) {
	for k := range jobKeys {
		src := "jobs:\n  one:\n    " + k + ": x\n  two:\n    runs-on: ubuntu-latest\n    steps: []\n"
		if problems := checkJobShape(src); len(problems) != 0 {
			t.Errorf("declared job key %q was rejected:\n  %v", k, problems)
		}
	}
}

// The gate must not be able to report success by inspecting nothing. An empty
// document has no jobs, and that is a problem rather than a pass.
func TestJobShapeRefusesAnEmptyDocument(t *testing.T) {
	if problems := checkJobShape(""); len(problems) != 1 {
		t.Errorf("an empty document should report one problem, got %v", problems)
	}
}

func TestBlockExtractionStillCrossChecks(t *testing.T) {
	// The cross-check that keeps `blocks` honest: the number of parsed blocks
	// must equal the number of `run: |` keys in the file.
	bs := blocks(goodWorkflow)
	if len(bs) != 2 {
		t.Fatalf("parsed %d blocks from a document with 2 `run: |` keys", len(bs))
	}
}
