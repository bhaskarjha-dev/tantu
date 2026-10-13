package hub

// Gate: a failure message must agree with the safety flags returned beside it.
//
// ClassifyTransferError and classifyRejection are the only place Tantu decides
// what a user is told about a failed transfer, and they already have thorough
// tests for the *flags*: 34 assertions across classify_test.go and
// operations_test.go cover retry_safe, duplicate_risk and data_safe.
//
// What none of them cover is the *wording*. Those are separate decisions made in
// the same tuple, and only one of them is gated. So this passes today:
//
//	"confirmation_timeout",
//	    "Transfer timed out. Please try again.",          // reworded
//	    "Please retry.",
//	    false, true, true                                   // flags untouched
//
// Every existing assertion still passes -- retry_safe is false, duplicate_risk
// is true -- and the user is now told to retry a transfer whose bytes the
// receiver may already hold. The one property that makes a timeout message
// trustworthy is that it admits the outcome is unknown.
//
// That is the same defect class this review has now hit repeatedly: a gate that
// confirms a value is present rather than confirming it is what the user is
// told. Here the gated value (the flag) and the ungated value (the sentence)
// are the same fact, stated twice.
//
// This gate reads the tuples out of the source and holds the two halves
// against each other, in both directions:
//
//   - duplicate_risk set      => the message must admit the file may be there
//   - duplicate_risk clear    => the message must NOT claim it might be
//   - retry_safe clear        => the next action must not say "safe to retry"
//   - retry_safe and duplicate_risk together => always a contradiction
//   - one code, one wording   => stage-specific and generic branches that
//     share a code must not drift apart
//
// It deliberately does NOT cross-check data_safe against the wording, and that
// omission is the result of the gate failing when written the obvious way. The
// first version asserted that data_safe means "nothing was saved on the
// receiver", and it failed on exactly the branches that matter most:
//
//	cancelled_after_send, data_safe=true,
//	    "Transfer cancelled after sending. The file may already be saved on the receiver."
//
// which is correct behaviour being reported as a defect. data_safe means the
// sender's own data is not compromised -- the source file and the user's draft
// survive -- not that the receiver holds nothing. For cancelled_after_send the
// bytes left intact and the only unknown is whether they landed, so the flag is
// true. Reading it as "nothing was saved" would have made the gate demand a
// reassurance the product is honestly unable to give.
//
// It enumerates the tuples from the source rather than from a hand-written
// table, so a branch added later is covered without anyone remembering to add
// it -- which is the whole reason the hand-written version of this gate would
// have rotted.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// tupleRe matches one `return "code", "plain", "next", a, b, c` as written in
// the two classifiers. Messages contain apostrophes but no escaped quotes, so a
// plain literal run is enough.
var tupleRe = regexp.MustCompile(
	`return\s+"([a-z_]+)",\s*\n\s*"([^"]*)",\s*\n\s*"([^"]*)",\s*\n\s*(true|false),\s*(true|false),\s*(true|false)`)

type failureTuple struct {
	fn            string
	code          string
	plain         string
	next          string
	retrySafe     bool
	duplicateRisk bool
	dataSafe      bool
}

func readClassifierTuples(t *testing.T) []failureTuple {
	t.Helper()
	src, err := os.ReadFile("operations.go")
	if err != nil {
		t.Fatalf("cannot read operations.go, so this gate has no input and must not pass: %v", err)
	}
	s := string(src)
	// Split at the second classifier so each tuple knows which function it came
	// from. Without that, the two legitimately-divergent shared codes look like
	// drift.
	splitAt := strings.Index(s, "func classifyRejection(")
	if splitAt < 0 {
		t.Fatalf("classifyRejection is gone from operations.go; this gate can no longer tell the two " +
			"classifiers apart and must not pass on a partial view")
	}
	parts := []struct{ fn, body string }{
		{"ClassifyTransferError", s[:splitAt]},
		{"classifyRejection", s[splitAt:]},
	}
	var out []failureTuple
	for _, p := range parts {
		for _, m := range tupleRe.FindAllStringSubmatch(p.body, -1) {
			out = append(out, failureTuple{
				fn:            p.fn,
				code:          m[1],
				plain:         m[2],
				next:          m[3],
				retrySafe:     m[4] == "true",
				duplicateRisk: m[5] == "true",
				dataSafe:      m[6] == "true",
			})
		}
	}
	// A gate that finds nothing has silently stopped gating. 19 tuples today:
	// 14 in ClassifyTransferError, 5 in classifyRejection.
	if len(out) < 19 {
		t.Fatalf("found only %d failure tuple(s) in operations.go, expected at least 19 -- "+
			"the classifiers have been restructured or this regex no longer matches, and a gate "+
			"that quietly stops reading its input must not report success", len(out))
	}
	// One code, one wording -- *within* a classifier. Repeated codes inside a
	// single function are legitimate: a stage-specific branch and a generic one
	// deliberately share an outcome, which is how a mid-stream reset avoids
	// being mistaken for a pre-send dial error. What is not legitimate is the
	// same code carrying two different sentences in the same place, so which one
	// a user sees depends on which branch happened to catch their failure.
	//
	// Across the two classifiers, divergence is allowed only where the receiver
	// itself stated the reason, because hearing a refusal is strictly more
	// information than matching a string on the sender. Those two codes are the
	// whole of the justified set, and a new one has to be argued for here
	// rather than appearing by accident.
	justifiedCrossFunction := map[string]string{
		"integrity_rejected": "sender infers a checksum failure from a string; the receiver states it",
		"receiver_busy":      "sender infers 503/quota from a string; the receiver says it is full",
	}
	byCode := map[string]failureTuple{}
	for _, f := range out {
		prev, ok := byCode[f.code]
		if !ok {
			byCode[f.code] = f
			continue
		}
		if prev.plain == f.plain && prev.next == f.next && prev.duplicateRisk == f.duplicateRisk {
			continue
		}
		if prev.fn != f.fn {
			if _, allowed := justifiedCrossFunction[f.code]; !allowed {
				t.Errorf("code %q carries different wording in %s and %s with no stated reason:\n"+
					"  %s: %q\n  %s: %q\nA code shared by both classifiers may only diverge where the "+
					"receiver itself stated the cause. Add it to justifiedCrossFunction with the reason "+
					"if that is genuinely what is happening here.",
					f.code, prev.fn, f.fn, prev.fn, prev.plain, f.fn, f.plain)
			}
			continue
		}
		t.Errorf("code %q is returned twice from %s with different messages, so which one a user "+
			"sees depends on which branch caught their failure:\n  %q\n  %q",
			f.code, f.fn, prev.plain, f.plain)
	}
	if len(byCode) < 14 {
		t.Fatalf("found only %d distinct failure code(s), expected at least 14 -- the classifiers "+
			"have been restructured or this regex no longer matches, and a gate that quietly stops "+
			"reading its input must not report success", len(byCode))
	}
	return out
}

// mustAdmitPossibility matches the phrases that tell a user their file may
// already be on the receiver.
var mustAdmitPossibility = regexp.MustCompile(`(?i)may already (be )?saved|may have (already )?saved|possibly (already )?saved`)

// mustDenyPossibility matches phrases that wrongly claim a file might be there
// when the sender knows it is not.
var mustDenyPossibility = regexp.MustCompile(`(?i)may already be saved|may already have been saved`)

func TestFailureMessageAgreesWithItsSafetyFlags(t *testing.T) {
	for _, f := range readClassifierTuples(t) {
		t.Run(f.code, func(t *testing.T) {
			switch {
			case f.duplicateRisk && !mustAdmitPossibility.MatchString(f.plain):
				t.Errorf("%s sets duplicate_risk, but its message never admits the file may already "+
					"be on the receiver:\n  %q\nThe user is told nothing about the one risk that flag "+
					"exists to communicate, and will retry into a duplicate.", f.code, f.plain)

			case !f.duplicateRisk && mustDenyPossibility.MatchString(f.plain):
				t.Errorf("%s clears duplicate_risk, but its message says the file may already be "+
					"saved:\n  %q\nThat sends the user to check an inbox for something this outcome "+
					"knows did not happen.", f.code, f.plain)

			case f.retrySafe && f.duplicateRisk:
				t.Errorf("%s claims the retry is safe *and* that a duplicate is possible. Those "+
					"contradict: either the outcome is known and retrying is free, or it is not and "+
					"retrying can duplicate.", f.code)
			}

			// The guidance has to come *before* the retry. Telling a user to
			// retry first and check afterwards is the ordering mistake that
			// turns a duplicate risk into a duplicate.
			if f.duplicateRisk && !strings.Contains(strings.ToLower(f.next), "before retrying") &&
				!strings.Contains(strings.ToLower(f.next), "check the receiver") {
				t.Errorf("%s sets duplicate_risk, but its next action does not tell the user to "+
					"check the receiver first:\n  %q\nThe guidance must come before the retry, not "+
					"after it.", f.code, f.next)
			}

			if !f.retrySafe && strings.Contains(strings.ToLower(f.next), "safe to retry") {
				t.Errorf("%s marks the retry unsafe, but its next action tells the user it is safe "+
					"to retry:\n  %q\nThe flag and the sentence disagree about the same fact.", f.code, f.next)
			}
		})
	}
}

// TestEveryFailureModeIsReachable guards the other half: a tuple that can never
// be returned is a message nobody will ever read, and the wording gate above
// would happily pass on it. Each code is matched to an input that has to reach
// it, so a branch that became unreachable -- or whose trigger was renamed --
// fails here instead of sitting there looking correct.
//
// This is a constructed-input gate and it is honest about that: reaching
// confirmation_timeout for real needs a receiver that accepts every byte and
// then goes silent, which means a second Hub. What is being tested is the
// classifier's mapping, and for a pure mapping the constructed input *is* the
// domain -- there is no running system whose behaviour is being asserted.
func TestEveryFailureModeIsReachable(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range readClassifierTuples(t) {
		seen[f.code] = true
	}

	for _, tc := range []struct {
		code string
		// err, bytesSent, size, cancelled
		err       string
		bytesSent int64
		size      int64
		cancelled bool
		wantBytes bool
	}{
		{"cancelled_after_send", "context canceled", 100, 100, true, false},
		{"cancelled", "context canceled", 40, 100, true, false},
		{"confirmation_timeout", "i/o timeout", 100, 100, false, false},
		{"transfer_timeout", "deadline exceeded", 40, 100, false, false},
		{"receiver_busy", "receiver said quota exceeded", 0, 100, false, false},
		{"integrity_rejected", "final size mismatch", 0, 100, false, false},
		{"negotiation_failed", "invalid resume offset", 0, 100, false, false},
		{"unknown_outcome", "receive drop_complete", 100, 100, false, false},
		{"transfer_interrupted", "write payload failed", 40, 100, false, false},
		{"destination_unreachable", "dial tcp: connection refused", 0, 100, false, false},
		{"transfer_failed", "something entirely unforeseen", 0, 100, false, false},
	} {
		if !seen[tc.code] {
			t.Errorf("no failure tuple in operations.go carries code %q, so the expectation in this "+
				"gate is stale. Either the branch was renamed or removed.", tc.code)
			continue
		}
		t.Run(tc.code, func(t *testing.T) {
			got, _, _, _, _, _ := ClassifyTransferError(errString(tc.err), tc.bytesSent, tc.size, tc.cancelled)
			if got != tc.code {
				t.Errorf("input %q (%d/%d bytes, cancelled=%v) classified as %q, want %q",
					tc.err, tc.bytesSent, tc.size, tc.cancelled, got, tc.code)
			}
		})
	}

	// classifyRejection is reached by type, not by wording, so it needs a real
	// RejectionError rather than an error string.
	for _, tc := range []struct{ code, reason string }{
		{"idempotency_key_conflict", "idempotency key reused"},
		{"integrity_rejected", "checksum mismatch"},
		{"receiver_busy", "no space left on device"},
		{"untrusted_peer", "not paired or trusted"},
		{"receiver_rejected", "policy says no"},
	} {
		if !seen[tc.code] {
			t.Errorf("no failure tuple in operations.go carries code %q, so the rejection "+
				"expectation in this gate is stale", tc.code)
			continue
		}
		t.Run("rejection/"+tc.code, func(t *testing.T) {
			got, _, _, _, _, _ := classifyRejection(tc.reason)
			if got != tc.code {
				t.Errorf("rejection %q classified as %q, want %q", tc.reason, got, tc.code)
			}
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }
