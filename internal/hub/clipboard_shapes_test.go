package hub

// Gate for limitation 2.4, wiring half.
//
// The browser harness proves that extractPastedFiles handles every clipboard
// shape a browser can produce, and that a real ClipboardEvent stages a preview.
// Neither proves the paste handler *calls* it -- and red proof showed exactly
// that gap: reverting the handler back to reading clipboardData.files directly
// left all 75 browser checks green, because the shape checks invoke the helper
// rather than the handler that uses it.
//
// The gap cannot be closed in a browser either. An items-only clipboard cannot
// be constructed from page script: adding a file to DataTransfer.items populates
// DataTransfer.files as well, so any real DataTransfer a page can build has both
// shapes. That is the same reason the original defect was invisible -- the shape
// that breaks is the one no page script can produce.
//
// So this is a structural assertion on the served page, and it is deliberately
// narrow: it locates the paste listener and asserts what it reads. The runtime
// behaviour of the extraction is gated in the harness, where it can actually run.

import (
	"strings"
	"testing"
)

// pasteHandlerBody returns the source of the dashboard's paste listener, or
// fails if the listener has moved -- a moved listener is not a reason to
// silently pass, it is a reason to update this gate.
func pasteHandlerBody(t *testing.T) string {
	t.Helper()
	code := stripDashboardComments(dashboardHTML)
	start := strings.Index(code, "document.addEventListener('paste'")
	if start < 0 {
		t.Fatal("the dashboard no longer registers a paste listener; point this gate at its new location")
	}
	// A generous but bounded window: long enough for the handler body, short
	// enough that a later unrelated listener cannot be mistaken for this one.
	const window = 700
	end := start + window
	if end > len(code) {
		end = len(code)
	}
	return code[start:end]
}

func TestPasteHandlerReadsEveryClipboardShape(t *testing.T) {
	handler := pasteHandlerBody(t)

	// The failure this exists for: reading only clipboardData.files, which is
	// empty on the engines that expose a pasted screenshot through items. The
	// symptom was a keystroke that appeared to be swallowed.
	if !strings.Contains(handler, "extractPastedFiles(e.clipboardData)") {
		t.Errorf("the paste handler does not use extractPastedFiles, so the clipboard shapes it "+
			"handles never reach the upload path.\nhandler:\n%s", handler)
	}
	if strings.Contains(handler, "e.clipboardData.files") {
		t.Errorf("the paste handler reads clipboardData.files directly. That is the shape which is "+
			"empty on the engines this fix exists for; both shapes must go through the helper.\nhandler:\n%s",
			handler)
	}
}

// TestClipboardShapeHelperCoversBothClipboardSurfaces guards the helper against
// being narrowed back to one shape, which is the regression the wiring gate above
// cannot see: the helper could keep both branches while the handler stops using
// it, or the handler could use it while the helper drops a branch.
func TestClipboardShapeHelperCoversBothClipboardSurfaces(t *testing.T) {
	code := stripDashboardComments(dashboardHTML)
	start := strings.Index(code, "function extractPastedFiles(")
	if start < 0 {
		t.Fatal("extractPastedFiles is gone; the paste handler calls it, so its absence is a build-time break")
	}
	end := start + 900
	if end > len(code) {
		end = len(code)
	}
	helper := code[start:end]

	for _, want := range []string{"clipboardData.files", "clipboardData.items", "getAsFile", "kind !== 'file'"} {
		if !strings.Contains(helper, want) {
			t.Errorf("extractPastedFiles no longer handles %q, so that clipboard shape is dropped.\nhelper:\n%s", want, helper)
		}
	}
	// Text pastes must still fall through to the focused field, or pasting into
	// the snippet textarea would be swallowed by the image path.
	if !strings.Contains(helper, "return fromItems") && !strings.Contains(helper, "return fromItems;") {
		t.Errorf("extractPastedFiles no longer returns the items it collected.\nhelper:\n%s", helper)
	}
}
