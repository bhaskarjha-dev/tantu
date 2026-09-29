package main

// A batch that partly fails must never look like a success. These tests pin
// the aggregation rules, because "49 of 50, exit 0" is the single most
// dangerous thing a directory send could do: a script would treat fifty files
// as delivered when one never arrived.

import (
	"strings"
	"testing"
)

func TestBatchExitCodeIsWorstOutcome(t *testing.T) {
	cases := []struct {
		name  string
		files []sendBatchFile
		want  int
	}{
		{
			name:  "all succeeded",
			files: []sendBatchFile{{ExitCode: sendExitOK}, {ExitCode: sendExitOK}},
			want:  sendExitOK,
		},
		{
			name:  "one failure among successes is not success",
			files: []sendBatchFile{{ExitCode: sendExitOK}, {ExitCode: sendExitFailed}, {ExitCode: sendExitOK}},
			want:  sendExitFailed,
		},
		{
			name:  "all failed",
			files: []sendBatchFile{{ExitCode: sendExitFailed}, {ExitCode: sendExitFailed}},
			want:  sendExitFailed,
		},
		{
			name:  "a usage error does not mask a transfer failure",
			files: []sendBatchFile{{ExitCode: sendExitUsage}, {ExitCode: sendExitFailed}},
			want:  sendExitFailed,
		},
		{
			name:  "only a usage error",
			files: []sendBatchFile{{ExitCode: sendExitUsage}},
			want:  sendExitUsage,
		},
		{
			// An unknown outcome is the most severe case: retrying it could
			// put a second copy on the peer, so it must win outright.
			name:  "duplicate risk wins over any failure",
			files: []sendBatchFile{{ExitCode: sendExitFailed}, {ExitCode: sendExitOK, DuplicateRisk: true}},
			want:  sendExitDuplicateRisk,
		},
		{
			name:  "duplicate risk alone",
			files: []sendBatchFile{{ExitCode: sendExitOK, DuplicateRisk: true}},
			want:  sendExitDuplicateRisk,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := sendBatchJSONResult{Files: tc.files}
			if got := batchExitCode(res); got != tc.want {
				t.Errorf("batchExitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

// The status and the counts must agree with the per-file outcomes, because
// the batch contract exists so a caller can trust the summary.
func TestBatchStatusReflectsOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		succeeded   int
		failed      int
		ambiguous   int
		wantStatus  string
		wantFailed  int
		wantNextHas string
	}{
		{"clean run", 3, 0, 0, "success", 0, ""},
		// Nothing arrived, so the guidance must not claim the rest did.
		{"total failure", 0, 3, 0, "error", 3, "Nothing was sent"},
		{"partial success is not success", 2, 1, 0, "partial", 1, "other 2 arrived"},
		// Files confirmed delivered alongside one unresolved outcome: part
		// provably arrived, so this is partial rather than success, and the
		// unresolved file counts as failed because its delivery is unknown.
		{"unknown outcome among successes", 3, 0, 1, "partial", 1, "duplicate risk"},
		// Nothing is known to have arrived, so this is an error even though
		// no file reported a hard failure in the ordinary sense.
		{"only an unknown outcome", 0, 0, 1, "error", 1, "duplicate risk"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := make([]sendBatchFile, 0, tc.succeeded+tc.failed)
			for i := 0; i < tc.succeeded; i++ {
				files = append(files, sendBatchFile{ExitCode: sendExitOK, Name: "ok"})
			}
			for i := 0; i < tc.failed; i++ {
				files = append(files, sendBatchFile{ExitCode: sendExitFailed, Name: "bad"})
			}
			for i := 0; i < tc.ambiguous; i++ {
				// An unknown outcome is modelled the way the real sender
				// reports one: a failure carrying duplicate risk, not a
				// success. Whether the file is on the peer is precisely what
				// is not known.
				files = append(files, sendBatchFile{ExitCode: sendExitFailed, DuplicateRisk: true, Name: "unknown"})
			}

			res := aggregateBatch(files, sendBatchJSONResult{}, len(files))

			if res.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", res.Status, tc.wantStatus)
			}
			if res.Succeeded != tc.succeeded {
				t.Errorf("succeeded = %d, want %d", res.Succeeded, tc.succeeded)
			}
			if res.Failed != tc.wantFailed {
				t.Errorf("failed = %d, want %d", res.Failed, tc.wantFailed)
			}
			// The next action must never claim files arrived when none did:
			// "the rest arrived" after a total failure is the kind of
			// reassurance that sends a user looking in the wrong place.
			if tc.wantNextHas == "" {
				if res.NextAction != "" {
					t.Errorf("a clean run needs no next action, got %q", res.NextAction)
				}
			} else if !strings.Contains(res.NextAction, tc.wantNextHas) {
				t.Errorf("next action %q must mention %q", res.NextAction, tc.wantNextHas)
			}
		})
	}
}
