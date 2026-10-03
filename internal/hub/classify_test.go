package hub

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
)

func TestClassifyTransferError_DiskFullIsInterrupted(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"write", errors.New("write payload: no space left on device")},
		{"sync", errors.New("sync payload chunk: no space left on device")},
		{"read", errors.New("read payload: input/output error")},
	}
	for _, tc := range cases {
		code, _, _, retrySafe, duplicateRisk, _ := ClassifyTransferError(tc.err, 0, 100, false)
		if code != "transfer_interrupted" {
			t.Errorf("%s: code = %q, want transfer_interrupted", tc.name, code)
		}
		if !retrySafe || duplicateRisk {
			t.Errorf("%s: flags wrong: retry=%v dup=%v", tc.name, retrySafe, duplicateRisk)
		}
	}
}

// A receiver that states a refusal has answered. The user-facing result must
// say the file was not saved and that a retry is safe - not warn of a possible
// duplicate, which is what the byte-count heuristic used to produce once every
// byte had been streamed.
func TestClassifyTransferError_ReceiverRejectionIsKnown(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{
			name:   "idempotency key conflict",
			reason: "content does not match the recorded completion for this idempotency key",
			want:   "idempotency_key_conflict",
		},
		{
			name:   "integrity",
			reason: "SHA-256 checksum mismatch: expected abc, got def",
			want:   "integrity_rejected",
		},
		{
			name:   "busy",
			reason: "receiver busy: too many concurrent transfers",
			want:   "receiver_busy",
		},
		{
			name:   "unclassified",
			reason: "refused for an unstated reason",
			want:   "receiver_rejected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Every byte was streamed, which is precisely the condition that
			// used to force the "unknown outcome" verdict.
			err := &drop.RejectionError{Reason: tc.reason, BytesReceived: 100}
			code, plain, next, retrySafe, duplicateRisk, dataSafe := ClassifyTransferError(err, 100, 100, false)
			if code != tc.want {
				t.Errorf("code = %q, want %q", code, tc.want)
			}
			if duplicateRisk {
				t.Error("an explicit rejection must never claim a possible duplicate")
			}
			if !retrySafe {
				t.Error("nothing was published, so a retry must be reported as safe")
			}
			if !dataSafe {
				t.Error("data safety must be preserved")
			}
			if plain == "" || next == "" {
				t.Error("every rejection needs a plain message and one next action")
			}
			if state := operationStateForFailure(retrySafe, duplicateRisk, false); state != OperationStateRetryableFailure {
				t.Errorf("state = %q, want %q", state, OperationStateRetryableFailure)
			}
		})
	}
}

// The converse must also hold: a connection that dies after the payload is
// genuinely unresolved, and must keep being reported that way. Fixing the
// rejection case is only honest if the unknown case is preserved.
func TestClassifyTransferError_LostAcknowledgementStaysUnknown(t *testing.T) {
	lost := errors.New("receive drop_complete: connection reset by peer")
	code, plain, _, retrySafe, duplicateRisk, _ := ClassifyTransferError(lost, 100, 100, false)
	if code != "unknown_outcome" {
		t.Errorf("code = %q, want unknown_outcome", code)
	}
	if !duplicateRisk || retrySafe {
		t.Errorf("a lost acknowledgement must keep its duplicate risk: retry=%v dup=%v", retrySafe, duplicateRisk)
	}
	if want := "may already be saved"; !strings.Contains(plain, want) {
		t.Errorf("message must still warn the file may exist, got %q", plain)
	}
}

// A receiver that no longer trusts the sender says so with the dispatcher's
// exact words: "unauthorized: peer certificate not paired or trusted". This is
// what an unpaired peer hears when it tries to send after the other machine
// removed it, and it is the one refusal the sender can actually act on — the
// generic "receiver refused" message hid the only useful fact, that the pair
// relationship is gone and re-pairing is the fix.
func TestClassifyTransferError_UntrustedPeerExplainsUnpair(t *testing.T) {
	reason := "unauthorized: peer certificate not paired or trusted"
	err := &drop.RejectionError{Reason: reason, BytesReceived: 100}
	code, plain, next, retrySafe, duplicateRisk, dataSafe := ClassifyTransferError(err, 100, 100, false)
	if code != "untrusted_peer" {
		t.Fatalf("code = %q, want untrusted_peer (the receiver stated the sender is not trusted)", code)
	}
	if !strings.Contains(strings.ToLower(plain), "no longer trust") {
		t.Errorf("message must name the real cause (no longer trusted), got %q", plain)
	}
	if !strings.Contains(strings.ToLower(next), "pair again") {
		t.Errorf("next action must say to pair again, got %q", next)
	}
	if !retrySafe || duplicateRisk || !dataSafe {
		t.Errorf("flags wrong: retry=%v dup=%v dataSafe=%v (nothing was saved)", retrySafe, duplicateRisk, dataSafe)
	}
}

// Wrapped rejections must still be recognised: the error crosses several
// layers on its way to the classifier, and losing the type on the way would
// silently restore the old misreport.
func TestClassifyTransferError_WrappedRejectionStillClassified(t *testing.T) {
	inner := &drop.RejectionError{Reason: "content does not match the recorded completion for this idempotency key", BytesReceived: 7}
	wrapped := fmt.Errorf("send drop failed: %w", inner)
	code, _, _, retrySafe, duplicateRisk, _ := ClassifyTransferError(wrapped, 7, 7, false)
	if code != "idempotency_key_conflict" {
		t.Errorf("code = %q, want idempotency_key_conflict", code)
	}
	if duplicateRisk || !retrySafe {
		t.Errorf("wrapped rejection flags wrong: retry=%v dup=%v", retrySafe, duplicateRisk)
	}
}
