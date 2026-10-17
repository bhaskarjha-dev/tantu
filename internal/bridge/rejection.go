package bridge

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
)

// Why a callback was refused, in terms the person staring at the browser can act
// on.
//
// This exists because of a field report. On 2026-10-17 a real sign-in failed and
// the browser showed:
//
//	invalid OAuth callback
//
// Which is what four different checks return, for four different reasons, with
// four different remedies. The specific reason went to the Hub's log -- and to a
// log tab the person had no reason to know existed. So the diagnostic ended in a
// dead end at exactly the moment it was most needed, which is the one thing this
// repository's own rule forbids.
//
// No test asserted anything about that page. `invalid OAuth callback` and
// `Rejected callback request` appeared in zero tests.
//
// The split here is deliberate:
//
//   - Detail is for the log. It may name headers, methods and paths.
//   - The rendered page is derived from Kind alone, and carries no state value,
//     no query value and no Detail text. state is the flow's own CSRF secret, so
//     it is never rendered; and the page is served to a loopback listener whose
//     only reachable client is a real top-level navigation, so naming which check
//     failed tells a legitimate user what to do and tells an attacker nothing
//     they could not learn from a blank page.
type rejectionKind int

const (
	rejectNone rejectionKind = iota
	// rejectMethod: the sign-in returned as something other than a GET or POST.
	rejectMethod
	// rejectNotLoopback: the callback was addressed to another machine.
	rejectNotLoopback
	// rejectNotNavigation: a page fetched the callback URL instead of the
	// browser redirecting the tab to it. Real, and it has a real remedy: the
	// application is doing its redirect in script rather than as a navigation.
	rejectNotNavigation
	// rejectSubresource: the callback was loaded as an image, script or frame.
	rejectSubresource
	// rejectOriginUnknown: the callback carried an Origin and Tantu had nothing
	// to compare it against, so it refused rather than guess.
	rejectOriginUnknown
	// rejectOriginMismatch: the callback came from a different site than the one
	// the sign-in started on.
	rejectOriginMismatch
	// rejectPath: the callback arrived on a path the sign-in registered.
	rejectPath
	// rejectState: the callback's state does not match the sign-in being waited
	// on -- typically a restarted, superseded or already-cancelled attempt.
	rejectState
	// rejectBodyUnreadable and rejectBodyTooLarge: nothing the user did.
	rejectBodyUnreadable
	rejectBodyTooLarge
	// rejectDuplicate: the callback already arrived once and was relayed.
	rejectDuplicate
)

// callbackRejection carries a log-safe reason and the kind that drives the page.
type callbackRejection struct {
	Kind   rejectionKind
	Detail string
}

func (r *callbackRejection) Error() string { return r.Detail }

// reject builds a rejection whose Detail is safe for the log and never rendered.
func reject(kind rejectionKind, format string, args ...any) error {
	return &callbackRejection{Kind: kind, Detail: fmt.Sprintf(format, args...)}
}

// rejectionKindOf extracts the kind from an error, defaulting to rejectNone so a
// rejection from a check that predates this classification still gets a page
// rather than a blank one.
func rejectionKindOf(err error) rejectionKind {
	var r *callbackRejection
	if errors.As(err, &r) {
		return r.Kind
	}
	return rejectNone
}

// rejectionAdvice is what the user is told, and what they should do next.
//
// Every kind needs all three fields non-empty. A gate enforces that, because an
// advice entry with an empty NextAction is the dead end rebuilt one layer down.
type rejectionAdvice struct {
	Title  string
	Detail string
	Next   string
	Status int
}

var rejectionAdviceTable = map[rejectionKind]rejectionAdvice{
	rejectNone: {
		Title:  "Tantu could not accept this sign-in",
		Detail: "The sign-in redirect arrived, but Tantu did not recognise it as the callback it was waiting for.",
		Next:   "Start the sign-in again from the application, and if it fails again check Live Logs in the Tantu dashboard for the technical reason.",
		Status: http.StatusBadRequest,
	},
	rejectMethod: {
		Title:  "This sign-in returned in a form Tantu does not accept",
		Detail: "The sign-in redirected the browser using something other than a normal page navigation.",
		Next:   "Start the sign-in again. If it keeps failing, the application may need a browser that supports the redirect it uses.",
		Status: http.StatusBadRequest,
	},
	rejectNotLoopback: {
		Title:  "This callback was addressed to another machine",
		Detail: "The sign-in redirected to a host that is not this computer, so the callback cannot belong to the sign-in Tantu is relaying.",
		Next:   "Check that the application registered a loopback redirect address such as http://localhost, then start the sign-in again.",
		Status: http.StatusBadRequest,
	},
	rejectNotNavigation: {
		Title:  "The sign-in was fetched rather than followed",
		Detail: "Something on the sign-in page asked for the callback address directly, instead of the browser following a redirect to it.",
		Next:   "Start the sign-in again and let the browser follow the redirect to completion. If the application always does this, it cannot be relayed this way.",
		Status: http.StatusBadRequest,
	},
	rejectSubresource: {
		Title:  "The callback was loaded as page content, not as a page",
		Detail: "The callback address was requested as an image, script or stylesheet rather than opened as a page.",
		Next:   "Start the sign-in again and let the browser open the callback address as a page.",
		Status: http.StatusBadRequest,
	},
	rejectOriginUnknown: {
		Title:  "Tantu could not verify where this sign-in came from",
		Detail: "The callback identified the site that sent it, but Tantu does not know which site the sign-in started on, so it refused rather than assume.",
		Next:   "Start the sign-in again. If it keeps failing, send the Live Logs entry for this callback to whoever maintains the application.",
		Status: http.StatusBadRequest,
	},
	rejectOriginMismatch: {
		Title:  "This sign-in came from a different website",
		Detail: "The callback says it came from one site, but the sign-in Tantu started was on another.",
		Next:   "Start the sign-in again, making sure you begin it from the application's own sign-in link rather than a copy.",
		Status: http.StatusBadRequest,
	},
	rejectPath: {
		Title:  "The callback arrived on an address this sign-in did not register",
		Detail: "The sign-in registered a callback address with a specific path, and the callback arrived somewhere else.",
		Next:   "Start the sign-in again. If the application keeps calling back on a different path, it needs to register the address it actually uses.",
		Status: http.StatusBadRequest,
	},
	rejectState: {
		Title:  "This sign-in is no longer waiting for a callback",
		Detail: "The callback belongs to a sign-in that has already finished, was cancelled, or was replaced by a newer one.",
		Next:   "Start the sign-in again from the application. This usually follows a second sign-in attempt, or a sign-in that was cancelled.",
		Status: http.StatusBadRequest,
	},
	rejectBodyUnreadable: {
		Title:  "The callback could not be read",
		Detail: "The callback arrived but its contents could not be read, so nothing could be forwarded to the application.",
		Next:   "Start the sign-in again. If it keeps failing, check Live Logs for the technical reason.",
		Status: http.StatusBadRequest,
	},
	rejectBodyTooLarge: {
		Title:  "The callback was too large to forward",
		Detail: "The callback carried more data than Tantu will forward to the application.",
		Next:   "Start the sign-in again, and if it keeps failing report it: the application appears to be returning a very large response.",
		Status: http.StatusRequestEntityTooLarge,
	},
	rejectDuplicate: {
		Title:  "This callback has already been forwarded",
		Detail: "Tantu already forwarded this sign-in's callback to the application, so there is nothing left to do here.",
		Next:   "Return to the application. If it is still waiting, start the sign-in again.",
		Status: http.StatusConflict,
	},
}

// rejectionKindName gives a kind a stable name, for gates and logs.
func rejectionKindName(kind rejectionKind) (string, bool) {
	switch kind {
	case rejectNone:
		return "rejectNone", true
	case rejectMethod:
		return "rejectMethod", true
	case rejectNotLoopback:
		return "rejectNotLoopback", true
	case rejectNotNavigation:
		return "rejectNotNavigation", true
	case rejectSubresource:
		return "rejectSubresource", true
	case rejectOriginUnknown:
		return "rejectOriginUnknown", true
	case rejectOriginMismatch:
		return "rejectOriginMismatch", true
	case rejectPath:
		return "rejectPath", true
	case rejectState:
		return "rejectState", true
	case rejectBodyUnreadable:
		return "rejectBodyUnreadable", true
	case rejectBodyTooLarge:
		return "rejectBodyTooLarge", true
	case rejectDuplicate:
		return "rejectDuplicate", true
	}
	return "", false
}

// adviceFor returns the page for a kind, never a zero value.
func adviceFor(kind rejectionKind) rejectionAdvice {
	if a, ok := rejectionAdviceTable[kind]; ok {
		return a
	}
	return rejectionAdviceTable[rejectNone]
}

// renderCallbackRejection writes the page a person sees when a callback is
// refused, and returns the advice so the caller can log it consistently.
//
// The HTML is assembled here rather than templated: it is a dozen lines, it has
// no user-supplied content in it, and every value is a compile-time constant from
// the table above. Nothing from the request reaches it.
func renderCallbackRejection(w http.ResponseWriter, kind rejectionKind) rejectionAdvice {
	a := adviceFor(kind)
	var b strings.Builder
	b.WriteString("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	b.WriteString("<title>" + html.EscapeString(a.Title) + "</title><style>")
	b.WriteString("body{font:16px/1.5 system-ui,-apple-system,Segoe UI,sans-serif;margin:0;")
	b.WriteString("background:#0f1117;color:#e6e6e6;display:flex;min-height:100vh;")
	b.WriteString("align-items:center;justify-content:center}")
	b.WriteString("main{max-width:34rem;padding:2rem}")
	b.WriteString("h1{font-size:1.35rem;margin:0 0 .75rem;line-height:1.3}")
	b.WriteString("p{margin:0 0 1rem;color:#b9c0cc}")
	b.WriteString(".next{border-left:3px solid #6aa9ff;padding:.5rem 0 .5rem .85rem;color:#e6e6e6}")
	b.WriteString(".who{color:#8b93a1;font-size:.85rem;margin-top:2rem}")
	b.WriteString("</style></head><body><main>")
	b.WriteString("<h1>" + html.EscapeString(a.Title) + "</h1>")
	b.WriteString("<p>" + html.EscapeString(a.Detail) + "</p>")
	b.WriteString("<p class=\"next\">" + html.EscapeString(a.Next) + "</p>")
	b.WriteString("<p class=\"who\">This page is served by Tantu, the machine that relayed this sign-in. " +
		"No sign-in information is shown here.</p>")
	b.WriteString("</main></body></html>")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(a.Status)
	_, _ = w.Write([]byte(b.String()))
	return a
}
