# Dashboard acceptance harness

A real-browser acceptance pass for the Hub dashboard, driven by Playwright
across **three engines: chromium, firefox, webkit**.

This exists because **a marker test cannot catch the defects that actually
matter here.** A substring assertion for `blob:` passes whether or not the
browser renders the image. Nothing in a Go test notices that a live region
announces twice a second, that a 3-second poll replaces a button under the
user's focus, or that a page is unreadable at 1.2:1 contrast in the light
theme. Those were all real bugs in this repository, and all three were found
here rather than by a test.

Running three engines is not decoration: the third one earned its seat by
surfacing a measurement defect neither of the first two could (see *What it
asserts*).

## Running it

```sh
cd tools/uxtest && npm ci                      # once
npx playwright install chromium firefox webkit # once (per platform)

node tools/uxtest/run.mjs                     # chromium, peerless Hub
node tools/uxtest/run.mjs --peers             # three seeded trusted peers
node tools/uxtest/run.mjs --browser=firefox
node tools/uxtest/run.mjs --browser=webkit
```

The engine can also come from `TANTU_TEST_BROWSER`. Every run needs ports
**18976** and **19877** to be free. Another Tantu running with default ports
does not conflict; a second copy of this harness does. A Hub that cannot start
is reported with its own output rather than as a bare `fetch failed`, so a port
clash is distinguishable from a slow machine.

The harness's own Hub is built as **`tantu-uxtest`**, not `tantu`, on purpose.
Restarting your own Hub with `Stop-Process -Name tantu` used to kill the Hub
under test mid-run, and the failure did not look like what it was: four scattered
dashboard checks failed with a staleness sentinel, a still-visible banner and
`ERR_CONNECTION_REFUSED`, which reads as product flakiness rather than "your Hub
was killed". A distinct process name means that command cannot reach it.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Every check passed. |
| `1` | A check genuinely failed. **This is a product result.** |
| `3` | The harness itself failed — build, browser launch, or a Hub that never became ready. |
| `4` | **Not a product result.** The Hub under test exited mid-run, so every later check failed for want of a server. Re-run on a quiet machine. |

Code `4` exists because a red run caused by a dead Hub is otherwise
indistinguishable from a real regression, and the pass count printed alongside it
(`47/51`) invites exactly that misreading. Every check that failed after the Hub
died is tagged `[HUB DIED: …]`, and the last Hub output is printed.

| Variable | Purpose |
|---|---|
| `TANTU_TEST_BROWSER` | Engine to drive: `chromium` (default), `firefox`, `webkit` |
| `TANTU_TEST_CHROME` | Literal path to a Chrome/Edge binary standing in for Playwright's chromium (e.g. a machine that cannot download Playwright's build). Ignored for the other engines. |

Requirements: **Node 18+** (CI uses 20) and the pinned Playwright from
`tools/uxtest/package.json`. Playwright downloads the browser builds itself —
there is no system Chrome to hunt for.

## What it does *not* require

- It is **not** needed to build, test, or run Tantu. The Go suite remains
  self-contained: `go test ./...` does not invoke this, and the shipped binary
  gains no dependency — Playwright is dev-only tooling, pinned in
  `tools/uxtest/package.json` and justified like any other dependency.
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
- **Computed contrast in both OS themes, for every rendered target.** The sweep
  visits all five tabs in both themes and measures only elements that actually
  generate boxes: an element inside an inactive tab paints nothing, so there is
  no pixel to contrast — and WebKit demonstrably leaves computed colors inside
  hidden subtrees stale across a `prefers-color-scheme` flip, resolving them
  when the subtree renders, so measuring a hidden element reads a value no user
  can ever see. Two coverage checks keep that honesty from becoming an
  escape hatch: both themes must measure the *same* rendered set (a theme flip
  must not change what is on screen), and the count must not fall below the
  full complement — so a sweep that silently measures nothing fails instead of
  passing.
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

Every run, on every push, is mandatory in CI: the `dashboard-acceptance` job
sweeps all three engines in one gate and annotates each engine's tail
separately, so "chromium failed, so firefox was never looked at" never
happens.

Screenshots and a JSON report are written under a temporary directory that is
removed on exit.

## Evidence, honestly scoped

This harness raises evidence to **E3** (browser/runtime validated) for the paths
it drives, now across three engine implementations. It cannot produce:

- **E4** — representative-user testing.
- **E5** — screen-reader and assistive-technology sessions, high-contrast mode,
  and **Safari-the-application on macOS**: Playwright's WebKit is the engine,
  not the app, and its `emulateMedia` is not a user switching the OS
  appearance — a real macOS theme flip with the dashboard open remains
  untested, as do real mobile and embedded browsers.

A green run here means "measured in three real browser engines", not
"validated with users". `docs/UX-STATUS.md` tracks the difference, and the
plan's release gates D and C stay red until that evidence exists.
