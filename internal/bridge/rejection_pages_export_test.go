package bridge

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Renders every callback rejection page into one HTML file, so a person can read
// them without causing a sign-in to fail.
//
// Why this exists. 4.99 records that nobody outside this session has read the
// twelve pages, and the obvious objection is the right one: **if authentication
// succeeds, how would anyone ever see them?** The honest answer is that most of
// them cannot be reached by any action a user can take. Eight of the twelve are
// raised by a check on the incoming request -- an unexpected method, a
// non-loopback Host, a fetch instead of a navigation, a subresource destination,
// an unexpected Origin, a path the sign-in never registered, a state that no
// longer matches -- and each requires the application, the network or the browser
// to misbehave in a specific way. Three more are the caller's own: an unreadable
// body, an oversized one, and a callback that arrives twice.
//
// Only two are reachable by a person on purpose: pressing the callback URL twice,
// and completing a sign-in that was cancelled or superseded. The user's own relay
// history from the 2026-10-17 field report shows two `cancelled` attempts, so a
// completed cancelled attempt would have shown one of these pages without anyone
// arranging it.
//
// That leaves ten pages no person would ever see in normal use, which is not a
// reason to leave them unreviewed. This exports all twelve from the product's own
// renderer -- not a copy, the same renderCallbackRejection the browser gets -- into
// a single file, so the copy can be read by someone who is not the author.
//
//	$env:TANTU_EXPORT_REJECTION_PAGES = "$PWD\rejection-pages.html"
//	go test ./internal/bridge/ -run TestExportRejectionPages -v
//
// Skipped when the variable is unset, so it costs nothing on an ordinary run.
//
// What this does NOT do, and 4.99 still records: reading a page in a browser is
// not a screen-reader pass (2.2), and one reader is still one reader.
func TestExportRejectionPages(t *testing.T) {
	out := strings.TrimSpace(os.Getenv("TANTU_EXPORT_REJECTION_PAGES"))
	if out == "" {
		t.Skip("set TANTU_EXPORT_REJECTION_PAGES to a file path to export the pages")
	}

	// What a person would have to do to see each one, and whether that is
	// something a person can do at all.
	type trigger struct {
		how    string
		byHand bool
	}
	triggers := map[rejectionKind]trigger{
		rejectNone:           {"Anything Tantu cannot classify. Should be unreachable now that every check returns a typed rejection.", false},
		rejectMethod:         {"The application redirects with something other than a page navigation. Needs an application that does this.", false},
		rejectNotLoopback:    {"The sign-in redirects to a host that is not this machine. Needs a misconfigured application.", false},
		rejectNotNavigation:  {"The sign-in page fetches the callback address instead of the browser following a redirect to it. Needs an application that redirects in script.", false},
		rejectSubresource:    {"The callback address is requested as an image, script or stylesheet. Needs an application that does this.", false},
		rejectOriginUnknown:  {"The callback carries an Origin and the sign-in's origin is unknown to Tantu. **Unreachable since 2026-10-20**: the A-side validates the authorization URL to have a scheme and a host before it computes the expectation, so the origin is always known and an unknown origin can no longer reach this page. The check is kept as a defence in depth, and is still gated by a unit test.", false},
		rejectOriginMismatch: {"The callback comes from a different site than the sign-in started on. Needs a cross-site redirect chain.", false},
		rejectPath:           {"The callback arrives on a path the sign-in did not register. Needs an application whose listener and registration disagree.", false},
		rejectState:          {"A sign-in is cancelled or superseded, then completed. **A person can do this**: start a sign-in, cancel it, then finish it in the browser.", true},
		rejectBodyUnreadable: {"The callback body cannot be read. Needs the connection to break mid-request.", false},
		rejectBodyTooLarge:   {"The callback carries more than 1 MiB. Needs an application returning a very large response.", false},
		rejectDuplicate:      {"The callback URL is opened twice. **A person can do this**: reload the callback page.", true},
	}

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<title>Tantu &mdash; callback rejection pages</title><style>`)
	b.WriteString(`body{font:16px/1.55 system-ui,-apple-system,Segoe UI,sans-serif;margin:0;background:#0f1117;color:#e6e6e6}`)
	b.WriteString(`main{max-width:60rem;margin:0 auto;padding:2.5rem 1.5rem 5rem}`)
	b.WriteString(`h1{font-size:1.6rem;margin:0 0 .4rem}h2{font-size:1.1rem;margin:3rem 0 .6rem;color:#9fb4d0}`)
	b.WriteString(`.meta{color:#8b93a1;font-size:.9rem;margin:0 0 1.2rem}`)
	b.WriteString(`.byhand{color:#7fd08a}.bycode{color:#d0a07f}`)
	b.WriteString(`iframe{border:1px solid #2a2f3a;border-radius:8px;background:#0f1117;width:34rem;height:15rem}`)
	b.WriteString(`.row{display:flex;gap:1.5rem;align-items:flex-start;flex-wrap:wrap;margin:0 0 1rem;padding:1rem;border:1px solid #232833;border-radius:8px}`)
	b.WriteString(`.how{flex:1 1 18rem}.frame{flex:0 0 auto}`)
	b.WriteString(`code{background:#1a1f28;padding:.1rem .35rem;border-radius:4px;font-size:.85em}`)
	b.WriteString(`</style></head><body><main>`)

	b.WriteString(`<h1>Callback rejection pages</h1>`)
	fmt.Fprintf(&b, `<p class="meta">Rendered %s from <code>renderCallbackRejection</code>, the same renderer the browser receives &mdash; not a copy.</p>`,
		time.Now().UTC().Format("2006-01-02 15:04:05Z"))
	b.WriteString(`<p class="meta">These pages appear only when a sign-in is <em>refused</em>. A working sign-in never shows one, ` +
		`which is why only some of them can be reached by hand: the trigger below says which.</p>`)

	byHand := 0
	for k := rejectNone; k <= rejectDuplicate; k++ {
		name, _ := rejectionKindName(k)
		tr := triggers[k]
		status := `<span class="bycode">not reachable by hand</span>`
		if tr.byHand {
			byHand++
			status = `<span class="byhand">reachable by hand</span>`
		}
		fmt.Fprintf(&b, `<h2>%s</h2><div class="row"><div class="how">%s<br><span class="meta">%s &middot; %s &middot; HTTP %d</span></div>`,
			name, tr.how, status, name, adviceFor(k).Status)

		rec := httptest.NewRecorder()
		renderCallbackRejection(rec, k)
		fmt.Fprintf(&b, `<div class="frame"><iframe title="%s" srcdoc="%s"></iframe></div></div>`,
			name, htmlAttrEscape(rec.Body.String()))
	}

	fmt.Fprintf(&b, `<p class="meta">%d of 12 can be reached by an action a person takes. The rest need the application, `+
		`the network or the browser to misbehave in a specific way, so the only evidence they have is constructed, not observed.</p>`,
		byHand)
	b.WriteString(`</main></body></html>`)

	if dir := filepath.Dir(out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", out, err)
	}
	t.Logf("wrote %d rejection pages to %s", 12, out)
}

// htmlAttrEscape makes the page safe inside an iframe's srcdoc attribute.
func htmlAttrEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"\"", "&quot;",
		"<", "&lt;",
		">", "&gt;",
		"\n", " ",
	)
	return r.Replace(s)
}
