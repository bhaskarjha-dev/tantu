package hub

import (
	"errors"
	"testing"
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
