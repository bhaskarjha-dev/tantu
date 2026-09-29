package hub

// Live Activity Logs is advertised as a user-facing surface: a tab in the
// dashboard, a "Live" badge, and a section of the support bundle. It was empty
// through an entirely successful send, because only failures and a handful of
// lifecycle events were ever logged. A feature that shows nothing during normal
// use reads as a broken feature, and a user's first move when asking "did my
// transfer work?" is to open that tab.
//
// These tests pin the guarantee that ordinary successes and ordinary failures
// both leave a record, through the same API the dashboard calls.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func readActivityLog(t *testing.T, h *Hub) []LogEvent {
	t.Helper()
	resp, err := testGet(h.ipcToken, "http://"+h.WebAddr()+"/api/logs")
	if err != nil {
		t.Fatalf("GET /api/logs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/logs status = %d", resp.StatusCode)
	}
	var events []LogEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	return events
}

func messagesMatching(events []LogEvent, substr string) []LogEvent {
	var out []LogEvent
	for _, e := range events {
		if strings.Contains(e.Message, substr) {
			out = append(out, e)
		}
	}
	return out
}

func summarizeMessages(events []LogEvent) string {
	parts := make([]string, 0, len(events))
	for _, e := range events {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, " | ")
}

// A successful text send must be visible. This is the exact case that shipped
// with an empty console: a verified delivery the user could not see anywhere.
func TestWebDashboard_SuccessfulSendIsVisibleInActivityLog(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]any{"text": "observable", "name": "note.txt"})
	resp, err := testPost(h.ipcToken, "http://"+h.WebAddr()+"/api/drop/upload", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d", resp.StatusCode)
	}

	events := readActivityLog(t, h)
	hits := messagesMatching(events, "Sent text")
	if len(hits) == 0 {
		t.Fatalf("a successful send left no entry in the activity log; got %d events: %s",
			len(events), summarizeMessages(events))
	}
	// The entry must state verification, so the log cannot be read as
	// "it tried" when it means "it landed and was checked".
	if !strings.Contains(hits[0].Message, "verified") {
		t.Errorf("the success entry must state verification, got %q", hits[0].Message)
	}
}

// A started Hub records its own configuration, so the tab has content the
// moment it is opened rather than only after something happens.
func TestWebDashboard_StartupIsVisibleInActivityLog(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	events := readActivityLog(t, h)
	hits := messagesMatching(events, "ready")
	if len(hits) == 0 {
		t.Fatalf("Hub startup is not logged; got %d events: %s", len(events), summarizeMessages(events))
	}
	// It must carry the addresses a user needs in order to act on it.
	for _, want := range []string{h.WebAddr(), h.OutputDir()} {
		if !strings.Contains(hits[0].Message, want) {
			t.Errorf("startup entry should name %q, got %q", want, hits[0].Message)
		}
	}
}

// Reading the log must not disturb it: the dashboard hydrates from this
// endpoint and then streams, so a read that consumed the buffer would make
// every later assertion in this file pass or fail for the wrong reason.
func TestWebDashboard_ActivityLogRingIsStableAcrossReads(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	first := len(readActivityLog(t, h))
	second := len(readActivityLog(t, h))
	if first != second {
		t.Errorf("reading the activity log twice changed it: %d then %d", first, second)
	}
	if first == 0 {
		t.Error("a started Hub should log at least its own startup")
	}
}
