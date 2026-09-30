# Dashboard acceptance harness

A real-browser acceptance pass for the Hub dashboard, driven over the Chrome
DevTools Protocol.

This exists because **a marker test cannot catch the defects that actually
matter here.** A substring assertion for `blob:` passes whether or not the
browser renders the image. Nothing in a Go test notices that a live region
announces twice a second, that a 3-second poll replaces a button under the
user's focus, or that a page is unreadable at 1.2:1 contrast in the light
theme. Those were all real bugs in this repository, and all three were found
here rather than by a test.

## Running it

```sh
node tools/uxtest/run.mjs            # peerless Hub
node tools/uxtest/run.mjs --peers    # three seeded trusted peers
```

It needs ports **18976**, **19877** and **19470** to be free. Another Tantu
running with default ports does not conflict; a second copy of this harness
does. A Hub that cannot start is reported with its own output rather than as a
bare `fetch failed`, so a port clash is distinguishable from a slow machine.

Optional environment variables:

| Variable | Purpose |
|---|---|
| `TANTU_TEST_CHROME` | Path to a Chrome/Edge binary, if it is not in a standard location |

Requirements: **Node 18+** and **a Chrome or Edge binary**. That is all.

## What it does *not* require

- No `npm install`. There are **no dependencies**; it uses only Node's built-in
  `fetch`, `WebSocket`, `child_process`, and `fs`.
- It is **not** needed to build, test, or run Tantu. The Go suite remains
  self-contained: `go test ./...` does not invoke this, and the product ships
  zero runtime dependencies as always.
- It never touches your real config directory, peers, or identity. It builds
  into a temporary directory, starts a Hub on `127.0.0.1:18976` with an
  isolated store, and deletes everything afterwards.

## What it asserts

- The one-time bootstrap token is exchanged and then stripped from the URL.
- **The staged send preview actually renders.** The clipboard-image confirmation
  was blocked by a Content Security Policy that did not authorize the
  page-created `blob:` URL; this is the check that would have caught it.
- **The text composer reports its outcome on the tab the user clicked from**,
  for success, honest failure, and the over-limit rejection, keeping the draft
  when it fails.
- Structure: one `h1`, no `div` card titles, a navigation landmark, a skip link,
  and a measured `--chrome-h`.
- **The pairing approval keeps keyboard focus across status polls** and is an
  announced status region with full-size targets.
- **Staleness is declared** when the Hub stops answering, the destination is
  labelled rather than hidden, and a retry recovers cleanly.
- **Computed contrast in both OS themes** for the surfaces that carry meaning.
- 200% zoom, a 360px viewport on every tab, and reduced motion with every
  animation and transition neutralized.
- **Every `data-action` control runs when clicked.** The harness synthesises a
  button for each action the page can render — including ones only live data
  produces, such as *Cancel sign-in* — clicks it, and fails if the handler
  throws or leaves an unhandled rejection. Dialogs, the OS shell, the
  clipboard, file choosers and downloads are neutralised first, while every
  other call reaches the real Hub, so argument extraction and dispatch are
  exercised for real.

  This is the check that catches the class of defect nothing else can: a
  delegated handler that reads its element from the wrong place throws before
  it does anything, so there is no message, no state change, and no other
  assertion that goes red. "Cancel sign-in" read its operation id from `this`,
  which in a listener bound to `document` is `document`, and therefore threw
  `TypeError: this.getAttribute is not a function` on every click — while all
  the other checks here passed.

- Zero unexpected console errors.

Screenshots and a JSON report are written under a temporary directory that is
removed on exit. If Windows still holds a handle on the Chrome profile, the
harness says so and prints the path rather than leaving it behind quietly.

## Evidence, honestly scoped

This harness raises evidence to **E3** (browser/runtime validated) for the paths
it drives. It cannot produce:

- **E4** — representative-user testing.
- **E5** — screen-reader and assistive-technology sessions, high-contrast mode,
  Safari, or Firefox.

A green run here means "measured in a real browser", not "validated with users".
`docs/UX-STATUS.md` tracks the difference, and the plan's release gates D and C
stay red until that evidence exists.
