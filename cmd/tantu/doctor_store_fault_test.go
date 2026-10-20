package main

// `tantu doctor` is the command a person runs when something is already
// broken, so it is the one place where "the peer store could not be read" and
// "you have no trusted peers" must not be the same line.
//
// The defect this holds: doctor reported "No trusted peers." for a store that
// failed to parse, with the next action "Run `tantu pair`". A user whose
// pairing record had been lost was told to pair again with a machine they had
// already paired, and the reason they could not was never mentioned.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDoctorReportsAnUnreadablePeerStoreRatherThanNoPeers is the gate.
func TestDoctorReportsAnUnreadablePeerStoreRatherThanNoPeers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "peers.json"), []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("write the corrupt peer store: %v", err)
	}

	report := buildHealthReport(dir, "127.0.0.1:1")
	check, found := doctorCheck(report, "peers")
	if !found {
		t.Fatalf("doctor emitted no peers check at all against a store it could not read: %+v", report.Checks)
	}
	if check.OK {
		t.Errorf("doctor reported the peers check OK against a store it could not read: %+v", check)
	}
	if !strings.Contains(strings.ToLower(check.Message), "could not be read") {
		t.Errorf("the peers check does not say the store could not be read: %q", check.Message)
	}
	if !strings.Contains(check.Message, "peers.json") {
		t.Errorf("the peers check does not name the file that is broken: %q", check.Message)
	}
	if strings.Contains(check.Message, "No trusted peers") {
		t.Errorf("the peers check still says the user has no trusted peers, which is a claim "+
			"about their trust decisions rather than about a file: %q", check.Message)
	}
}

// TestDoctorReportsNoFaultForAnEmptyPeerStore is the control.
func TestDoctorReportsNoFaultForAnEmptyPeerStore(t *testing.T) {
	dir := t.TempDir()
	report := buildHealthReport(dir, "127.0.0.1:1")
	check, found := doctorCheck(report, "peers")
	if !found {
		t.Fatalf("doctor emitted no peers check: %+v", report.Checks)
	}
	if strings.Contains(strings.ToLower(check.Message), "could not be read") {
		t.Errorf("doctor reported a fault against a store that is merely empty: %q", check.Message)
	}
}
