# Tantu UX Status — v6.0 Plan Tracking

> **Status:** Living implementation record (agent-maintained)
> **Last Updated:** 2026-10-25
> **Plan:** v6.0. Decision IDs resolve in [`docs/DECISIONS.md`](DECISIONS.md);
> the original plan document is under `temp/` and is gitignored.
>
> **Review note (2026-10-05):** no E4/E5 evidence was produced, and the ladder
> below is unchanged on that axis. Three batches landed since the last review
> and each is recorded under its own heading rather than folded into the P0/P1
> rows: Z32 (one notification surface, real empty states), Z33 (discovery
> health), Z34 (directories arrive as directories). Claims in this file that are
> not re-verified on each audit should be treated as unconfirmed — see
> `docs/KNOWN-LIMITATIONS.md` §"How to use this register".

This file maps every P0/P1 item to its implementation state and evidence
level. It exists so a "UX-complete" claim can never outrun its evidence.
Anything requiring representative users, physical devices, or assistive
technology sessions is marked as such — an automated agent cannot produce
E4–E5 evidence, and this file does not pretend otherwise.

Evidence ladder (from the plan): E0 hypothesis · E1 code inspected ·
E2 automated test · E3 browser/runtime validated · E4 representative user ·
E5 cross-platform/accessibility/failure evidence.

## P0 — primary-journey completeness

| Item | State | Evidence | Notes |
|---|---|---|---|
| Visible destination before send | Done | E2–E3 | Dashboard always-visible summary; cockpit prints destination; send/transfer outputs name it. Loopback self-sends say "local Hub". Send/relay **button labels** now also name the resolved destination (Z15). |
| Clipboard image preview/confirmation | Done | E2–E3 | Staged preview (name/size/type/destination, ≤8 MB thumbnail) for picker, drag/drop, and paste. Expert immediate-send is explicit opt-in. Marker tests forbid auto-send patterns. **Z15: the thumbnail actually rendered** — CSP did not authorize the page-created `blob:` URL, so the image was blocked and permanently invisible; fixed and covered by a test that parses the served policy. |
| State-aware first-run next action | Done | E2 | `tantu doctor`, `status` next-action line, dashboard next-action banner. No dead ends in CLI flows. |
| Honest transfer lifecycle | Done | E2–E3 | Operation states (completed/retryable/terminal/cancelled/duplicate-risk); completion requires remote ack + SHA match. Live loopback self-delivery verified. **Z15: both composer paths now report the outcome on the tab the user clicked from** — previously a text send and an over-limit rejection were visible only in Live Logs. |
| Error data-safety/retry-safety fields | Done | E2–E3 | Canonical contract on Hub API and `send --json`: code, plain message, retry/duplicate/data flags, one next action, diagnostic ID. |
| Lost-ACK and duplicate-risk state | Done | E2 | First-class `duplicate_risk`; `transfer retry` refused; exit code 3. No blind retry anywhere. |
| Keyboard-complete primary journey | Partial | E2 | Full keyboard paths, focus-visible styles, modal Escape/trap/restore, single-pointer alternatives for drag. Screen-reader sessions (E4) not run — see gaps. **Z15: tab bar is a single tab stop with arrow-key navigation; the scrolling log console is focusable and named; closing the pairing dialog never strands focus on `<body>`.** **Batch S: the node's two pages are now in scope and were not keyboard-complete.** The QuickDrop page's drop zone was a `div` with a click handler — no `role`, no `tabindex`, no key handler — beside a `display:none` file input, so **the one control the page exists for could not be reached with a keyboard at all**. It is now `role="button"`, `tabindex="0"`, activated by Enter and Space, and the browser suite walks the real tab order and requires every visible control to appear in it. The node's fields carry real labels (a placeholder is an instruction, not a name), its status lines are live regions, and its empty state is a drawn object rather than a paragraph of body text. The design system now has a **component layer** (`internal/webui/components.css`), served at one `{{DESIGN_SYSTEM}}` marker, so a page cannot get the tokens without the components — which is how the node pages ended up using `.empty-state` with no definition of it anywhere they could reach. Still no E4 screen-reader session. |
| Visual hierarchy and information architecture | Partial | Code + rendered measurement | **Batch T: every surface was captured in both themes at desktop and phone widths and read for what it is trying to be.** The send surface opened with TWO full-width bands — a downloads path FIRST, then the destination — so the prime position went to a file path and the one value a reader must find at a glance was a 15px line in a 1176px row. They are ONE band now: destination left at heading size, downloads path right at path size. The two columns ended 200px apart, leaving a hole in the middle of the primary screen; the received card now runs its column's full height and the list scrolls inside it. The drop zone said "Delivered to this Hub" while the header said "Sending to Devbox (workstation)" — two destinations on one screen, one of them wrong; the drop zone now says how the bytes travel and nothing about where they go. A peer's name shared a row with its actions, and since one peer was the default and the others were not, the row's shape changed with its button count and the name was truncated to fit — the name is how two machines are told apart, so it now has the whole row and wraps, and the fingerprint (64 hex characters) is a button that copies it. The relay tab's recommended path was a paragraph containing three instructions; it is three numbered steps with the drag target in its own column. The live log console was capped at 30rem inside a page that kept 40% of itself empty; it now fills the page. **Two screens of one product called the same value two names** — the identity SAS was "SAS Verification Code" on one and "This machine's identity SAS … not the pairing code" on another; it is "Identity SAS" everywhere. The design system gained a **spacing scale** (the rhythm was consistent but written out as a literal in every rule, so nothing could be changed as a system) and the component layer uses it. |
| WCAG 2.2 AA audit | Partial | Code-level requirements implemented (names, live regions, focus, motion, drag alternative); formal audit with AT sessions not run. **Z15: measured contrast for both themes is now green across the audited surfaces (was four sub-AA elements, three of them below 1.4:1), the page has a real heading outline plus a skip link and navigation landmark, and the chatty upload live region was removed in favour of `aria-valuetext`. Z63 widened "the audited surfaces" from a hand-picked list of 16 selectors to **every rendered text node — 181 per theme, verified on Chromium and Firefox**: that found six light-theme and three dark-theme failures the list never covered, including white on the primary action button at 3.68:1. Still no E4/E5 evidence.** **Batch S: the audit now covers the node's two pages, which it had never covered, and the "one type scale" claim was not true.** Measured on the running dashboard: the text inputs and the **tab bar** resolved to **Arial** while the body, the card titles, the buttons and the textarea used the product's stack — a form control does not inherit `font-family` unless it is told to, so the navigation was set in a different typeface from the page it navigates. The design system now says it in one rule (`input, textarea, select, button { font-family: inherit }`), and the browser suite requires every control on every node page to resolve to the page's own typeface. The node's pages now carry labels, live regions and a keyboard-reachable drop zone, and the suite measures all of that on the **rendered** page rather than in the source. **Batch T: the destination was the one value the product is most careful about and it was set at the size of body text** — the page carried `#destinationSummary { font-size: var(--fs-md) }` and an ID rule outranked the design system's class, so the value looked right in a diff and was 15px on the screen. The rule is gone and the destination is 16px, measured on the rendered page. Still no E4/E5 evidence — no assistive-technology session has been run against any surface in this product. |
| Focus-visible/unobscured behavior | Done | E2–E3 | `:focus-visible` rings, scroll-margin under the sticky header, reduced-motion handling. **Z15: `scroll-margin-top` is now derived from the measured sticky chrome (was 84 px against a real 129 px), and the autofocused relay button gets a `:focus` ring** — a programmatic focus never matched `:focus-visible`, so the popup's first focused state had no ring. |
| Unified plain-language vocabulary | Done | E1–E3 | "Trusted" (never "ready/online" unprobed); "File sent to X · verified"; "File may already be saved". **Z15: internal state names are no longer rendered** — Transfers and Authorizations use one shared label map, an unrecognised state degrades to "Unrecognised state" rather than leaking the identifier, duplicate risk reads as a warning, and records carry a date (they are kept 30 days). |
| No silent wrong-peer routing | Done | E2 | Empty/multi-peer resolution errors instead of picking; unresolvable echoes; destination revalidated at dial; cockpit resolution helper tested. |
| Refresh/restart/reconnect reconciliation | Done | E2 | SSE `Last-Event-ID` replay; snapshot reload; durable `transfers.json` (last 50, 30 days) with restart-restore test; corrupt-file fail-open test; 24 h sliding dashboard sessions (renewed on use, still rotated on Hub restart). |
| Redacted diagnostic path | Done | E2–E3 | Server-side URL/secret redaction; metadata-only history and bundle; `doctor --bundle-path`. No payload/secret in logs, errors, or history (tested). |
| `tantu doctor` contract | Done | E2–E3 | Human + `--json` + bundle; exit 0/1; live-smoke verified against a real Hub. |
| Canonical operation state in Hub and CLI | Done | E2–E3 | Hub API + `transfers`/`transfer list` (+ offline fallback) + `status --json` + `send --json`. |

## P1 — coherent product

| Item | State | Notes |
|---|---|---|
| Transfer history and bounded persistence | Done | `transfers.json`, 50/30-day bounds, atomic writes, offline CLI fallback, explicit deletion (`transfer clear`, dashboard Clear). |
| Completion tombstone/idempotency | Done | Receiver tombstone store (bounded LRU 1024, 24 h, durable file, fail-closed mismatch): same-key redelivery streams to discard, verifies the digest, and re-acknowledges without staging, publishing, or duplicate inbox/history. Sender key control via `send --idempotency-key` (validated, forwarded through delegation); `transfer retry` teaches key-reuse instead of blind retry. |
| Retry/resume actions | Partial | Retry-as-new-transfer is always available (`send` again); in-place retry refused by design. Same-DropID resume stays library-only. |
| CLI/dashboard state parity | Done | Shared taxonomy (`hub.ClassifyTransferError`), shared destination language, `send --json` and `open --json` with matching exit-code discipline. |
| Standalone migration/deprecation labels | Done | `serve`/`node` print Hub pointers; usage labels advanced/compatibility; `drop`/`relay` already carried Hub tips. |
| OAuth state machine | Done (faithful subset) | Sender-side submitted → waiting_callback → complete/failed/cancelled with redacted codes; browser-opened instant lives in A-side logs by architecture. Durable ledger + API + dashboard card + bundle; design for full versioning in `docs/SPEC-WIRE-VERSIONING.md`. |
| Browser/platform clipboard matrix | Partial | Paste-event path + picker fallback implemented and marked; matrix measurements need real browsers/platforms. |
| Failure-injection suite | Partial | Offline, quota-shape, checksum-shape, lost-ACK-shape, receiver-rejection (Unix-gated), disk-full taxonomy + write-failure E2E, corrupt-history, restart-during-life covered; true OS disk-full condition not injected. |
| Performance measurement harness | Done (code-level) | `internal/drop/throughput_bench_test.go` (1 MiB E2E, stdlib-only) with recorded baselines; no CI schedule yet. |
| User research baseline | Not started (human-only) | Ready-to-run materials in `docs/RESEARCH-PACKET.md`. |
| Redacted support bundle | Done | `doctor --bundle-path`, redaction tests (offline + live). |
| Upgrade/rollback UX | Done | `transfers.json` documented forward-compatible in `docs/RELEASE.md`; active-transfer guidance unchanged. |

## P2 — after the core is proven

**Shipped:** multi-file-as-separate transfers, and then **directory structure
preservation** (D-28). `tantu send <directory>` sends each file as an
independent transfer with its own operation ID, idempotency key, and outcome,
bounded at 2,000 files / 5 GiB, with symlinks refused and per-file reporting
naming each file by its **path in the tree** — a list of leaf names cannot tell
two files called `notes.txt` apart. A partial batch exits non-zero and names the
files that did not arrive; a batch that delivered nothing says so rather than
claiming the rest arrived. The receiver rebuilds the tree under the sent
directory's name; `--flatten` restores the folded-name expansion.

Still not started, per the plan's own stop rule (D-12, D-20): forwarding,
notifications, QR, extension, pause/resume, WAN transport, signed artifacts.

## Z32 — one notification surface, and empty states that act

Every failure path in the dashboard called `alert()`. That is a modal,
unthemed, page-blocking, OS-owned interruption, and it is invisible to a screen
reader as anything but an interruption — twenty-odd call sites across alias
edits, folder opens, downloads-folder changes, log export, both Clear buttons
and both unpair confirmations. The acceptance harness had been *stubbing*
`window.alert/prompt/confirm` during its action sweep, which is exactly why
several audits passed over them.

Now: `notify()` renders a themed, non-modal toast in one of two live regions
(polite for progress, assertive for failure), carries the product's own "Next:"
vocabulary, persists error toasts until dismissed, and pauses its timer on hover
or focus. One accessible dialog replaces `confirm`/`prompt` — Escape cancels, Tab
is trapped, focus moves to the safe control and returns to the opener. All four
empty states offer a real action, and the server-side first paint matches the
JavaScript re-render so the affordance cannot vanish on the next poll.

Evidence: five Go gates plus eight measured browser checks; the harness now
**fails the run** on any native dialog. Closes limitations 4.1 and 4.3, and 4.2
partly (the single-pointer alternative is now visible next to the drag-only
bookmarklet).

## Z33 — discovery that says when it is broken

Every discovery failure signal was discarded: broadcast write errors ignored, the
mDNS bind error dropped, a receive loop that gave up after 50 errors simply
returning. The product's answer to "discovery is broken" and "there is no second
machine" was the identical empty Nearby list.

`Engine.Health()` now counts sends, receives, errors and socket state, and
renders one sentence naming the condition; the Hub logs *transitions* rather
than samples; `/api/status` carries the block and the Peers tab renders it. The
calibration matters as much as the mechanism: a blocked mDNS socket on a network
where subnet broadcast works is normal, and is reported as "broadcast only", not
as degraded — a warning that is always there is a warning nobody reads. Closes
limitation 3.13. Limitation 3.14 (IPv4-only) is unchanged in substance but is now
*stated* by the health surface rather than implied.

## Z34 — a sent directory arrives as a directory

See `docs/DEV-RECORD.md` (Batch Z34) and `docs/ARCHITECTURE.md` §3.5a. The
product-level finding is worth repeating here because it is the kind of defect
only running the product finds: the collision disambiguator was appended to the
whole destination path instead of its leaf, so re-sending a directory made every
file fail with the delivered file turned into its own parent directory. No
publication-level test could see it, because those tests are handed an
already-chosen destination. The gate now drives the Hub instead, and is
red-proven by reintroducing the one-line bug.

## Z15 — frontend audit: what changed and what did not

A plan-driven audit of every user-facing element produced a report and a
prioritized list; the approved items shipped in four verified slices. Full
findings, rulings, and evidence are in `docs/DEV-RECORD.md` (Batch Z15).

**Fixed (P0):** blocked preview thumbnail (CSP `blob:`); silent text-composer
success and silent over-limit rejection; pairing approval rebuilt every poll and
destroying keyboard focus; four sub-AA contrast elements.

**Calibrated by the maintainer, deliberately not done:** header/tab chrome
re-theming (kept dark in both themes on purpose — the chrome owns its own
colours instead, and Z63 finally made that true rather than merely intended: the
tab labels, header button and wordmark had all been inheriting body colours and
measured 1.23:1 on the dark chrome in the light theme); a full 44px floor
(targeted bumps plus spacing, per the plan); wholesale replacement of the
remaining `prompt()` dialogs (the alias prompt is left native as an acceptable
low-risk surface).

**Touch targets, clarified (Z64):** the blanket 44px floor stays declined, but it
was doing quiet work as the only stated bar. There is a lower one that was never
explicitly waived — **WCAG 2.2 SC 2.5.8 Target Size (Minimum), AA, 24×24** — and
the harness now enforces it at 360px and 640px, measuring the *hit area* rather
than the control (a checkbox wrapped in a `<label>` is as large as its label).
Two controls were below it: the header peer switcher at 144×20, and the expert
immediate-send checkbox, whose label turned out to be 574×**17** — a wrapping
label expands width and leaves the height at the line box. Both fixed.
Lighthouse's default accessibility set does not include 2.5.8, which is why the
page scored 1.0 with both of them present.

**Still open, recorded not improvised:** the `alert()` dialogs on peer
actions; empty states without a next action; the bookmarklet's drag-only
affordance. Staleness is now declared (Z18) — when the Hub stops answering the
page says so and labels the destination as last known rather than presenting
dead data as live. `E4`/`E5` accessibility evidence cannot be produced in this
environment and is claimed nowhere.

## Where the plan stands

`docs/UX-PLAN-MAP.md` maps the v6.0 plan to shipped state item by item: P0
13/14, P1 8 done / 4 partial / 1 not started, P2 now 2 shipped (multi-file
sends, then directory structure preservation) with the remainder still gated by
the stop rule, release
gates A/B/E/F green and C/D red, 8 of 10 stop-rule conditions met, and which of
the nineteen §21 artifacts exist. `docs/KNOWN-LIMITATIONS.md` holds every
admitted limitation with severity and where it is visible;
`docs/SURFACE-MATRIX.md` holds the standalone migration matrix.

## Z16 — multi-peer evidence, and what it does not prove

Every browser run in Z15 used a **peerless** Hub, so the multi-peer layout had
no runtime evidence. Seeding trusted peers into the real store and re-running
found four defects the empty state cannot reach: peer badges at 1.67:1 on a
light card, warning chips at 3.9:1, horizontal overflow at 360px on every tab
from two independent causes, and a duplicate `max-width` that stretched the
header peer select. Details in `docs/DEV-RECORD.md` (Batch Z16).

**Evidence level is unchanged and deliberately so.** Seeding proves the layout
renders and behaves; it does not prove a pairing handshake, a cross-machine
transfer, or reachable/unreachable transitions, because no second machine was
ever dialed. Claiming more would repeat exactly the error this batch corrected.

## Batch K — the dashboard's visual system

The page was re-skinned. One measure shared by the header, the tab bar
and the content, so their edges line up; a segmented tab bar on a dark
track, sized to its tabs rather than to the page; a drawn icon set in
place of the colour emoji, which rendered as clip-art against a
monochrome UI and differed per platform; a type scale in place of seven
ad-hoc sizes, which is why the hierarchy had read as flat; cards with a
hairline, a layered shadow and titles that read as titles; empty states
that are cards with a heading and a button rather than centred
paragraphs; a drop zone that is a surface rather than a dashed box; and
a page with a pool of light at the top.

Two things in it matter beyond the appearance. The icons are CSS masks
painted by a pseudo-element rather than `<svg>` elements, because the
acceptance suite measures a tab label's rendered line count over the
button's contents, and an `<svg>` as a direct child of the tab button
reports two lines and fails that gate. And a broken mask is invisible
and silent — nothing else in the suite would notice a page whose icons
had all vanished — so three checks were added: every icon class has a
mask that applies, every icon has a box to draw in, and every mask
resolves to a drawing that paints. All three are red-proven.

The same work turned up a hole in an existing gate: the check that a
card title is not a `<div>` matched only the exact class list, so a card
title carrying an icon's class passed it. It is now a prefix, and the
red proof is that same rewritten card title.

Evidence: the acceptance suite green on all three engines, and the page
looked at in both themes, at 1600 px and 360 px, on every tab.
**This is not E4 or E5 evidence.** The contrast, overflow, target-size
and tab-label gates are machine-checked; whether the result reads as good
is a judgement, and the ladder above is unchanged on that axis.

## Batch L — the surfaces a user actually meets

Batch K dressed the page. Batch L dresses the parts of it that carry
information, and it exists because Batch K was not enough: it was
designed against empty states, which are the easiest thing in the
world to make look good and the least of what a user sees.

`tools/uxtest/populated.mjs` now starts a Hub with a seeded peer
store, drives real traffic through the real controls, and captures
every tab in both themes at 1600 px and 360 px. The first pictures of
this product that are not mostly empty were not good: a failed send
was one wall of red text with the next action in the same red at the
same weight, the diagnostic id read like part of the complaint, the
page said "Destination:" twice in the same card, a page with four
peers had four identically loud red Unpair buttons on it, the peer
rows had four different shapes, the fingerprint was cut to a column of
dots, and the log's level replaced the log's domain so a reader could
not tell which part of the product had failed.

All of that is now one component and one set of surfaces: a message
with a title, a detail, a next action on its own line and a
monospaced chip for the id; peer rows that keep their shape whatever
the name; a quiet destructive action, because every destructive action
in the product confirms first; a destination stated once; an identity
panel that shows the fingerprint in full; records that carry their
state on an edge; and a log that shows domain and level and wraps
with a hanging indent.

Two checks changed and are not weaker: the destination check asserts a
label and a value instead of a duplicated prefix, and a Go gate's
markers follow the copy rather than the mechanism. One check did not
change: the toast's next action is still "Next: ", because the
product's language is "Next: <step>" everywhere and a label is not a
place to be quietly different.

Evidence: the acceptance suite green on all three engines, the
contrast sweep now measuring 161 rendered text nodes per theme with no
failure, and the page looked at populated in both themes at 1600 px
and 360 px. **This is not E4 or E5 evidence.** The ladder above is
unchanged on that axis.

## Batch M — three surfaces, and a focus ring this work broke

The progress bar is the only thing on the screen that says a transfer
is happening, and it was eight pixels of gradient on a translucent
track with the file name somewhere else on the page. It has a track you
can see now. The received item was the one row in the product with no
edge while every row around it grew one, and a LAN peer found by the
beacon was drawn in a hand-rolled box that matched nothing else; both
are the shape of their neighbours now.

The third part of this batch is the one worth reading. Restyling the
fields, this work added a second focus ring to the text inputs and the
textarea — a 2px accent outline at a 1px offset — on the belief that
the fields had no visible focus indicator of their own. They did: a
shared rule gives every focusable control a 2px solid accent outline,
and the focused field's border turns accent-coloured as well. The new
rule was a second, differently-styled ring on the same control, and
the ring a reader saw was the wrong one of the two.

The belief behind it was never measured — it was arithmetic about a
colour nobody had rendered — and it was wrong.

The repository's two focus gates could not see it: the fields *did*
have a ring, and the checkbox was untouched. The new gate
(`TestAControlHasOnlyOneFocusRing`) says the rule the page actually
follows: a control type may be named by at most one **general**
`:focus-visible` rule. A rule that also names a class is a deliberate
context override — the header and the tab bar are chrome, and their
rings are the chrome accent — and those are the design working. Two
general rules for one type mean one is dead and the other is what a
reader sees. It compares the outline *and* its offset, and it is
red-proven.

The one surface this batch did not touch is the terminal cockpit, and
the reason is recorded rather than worked around: it draws itself
with ANSI and box-drawing characters and only runs on a terminal, so
capturing it needs a pseudo-terminal this environment does not have.
Its box geometry has gates; its appearance does not, and is not
claimed.

**This is not E4 or E5 evidence.** The ladder above is unchanged on
that axis.

## Batch O — the peer switcher, at a height the engine chose

The header's peer switcher is the control that says who you are about
to send to. Two things about it were wrong, and neither was visible in
the source.

Its **height was the browser's decision**. A native `<select>` is sized
by the engine unless the stylesheet says otherwise, and the engines
disagreed: on the same page, at 360px, with peers seeded, chromium
rendered the control 24px tall and webkit 31px. The header has a
110px budget, so webkit's header came to 114px and failed a check
chromium passed. Seven pixels of the narrowest layout, chosen by
software nobody in this repository controls. The height is now stated
— 24px, the WCAG 2.2 SC 2.5.8 minimum target size the rule already
claimed with `min-height` — so the control's size is the product's
decision and both engines agree: header 107px in each.

Its **text was unreadable in the light theme**. The switcher was
painted in `--chrome-text`, a chrome colour, on the peer pill — which
is a card surface, and white in the light theme. Light blue on white
measured **2.11:1**. It was the one word in the header nobody could
read, and the contrast sweep never measured it, in either theme, in any
run: the sweep walks elements that own a text node, and a `<select>`
owns none, because its text lives in an `<option>` inside the closed
popup. The sweep now measures controls that paint their own text, and
the switcher's colour on a card surface is `--text` — 16.00:1 in the
light theme, 15.15:1 in the dark one.

The same blindness had **every placeholder in the product** behind it.
The stylesheet had no `::placeholder` rule at all, so each placeholder
was painted in the browser's own default: `rgb(117,117,117)`, which is
**3.99:1** on the dark field surface and **4.07:1** on the light one.
In an empty field the placeholder is the only text on the screen — the
instruction the user needs in order to do the thing — and it was the
one part of the field they could not read. Placeholders now use
`--text-muted` with `opacity: 1` stated: **5.94:1** dark, **5.29:1**
light.

The gate that matters here is about the *cause*, not the symptom. The
header-height check catches the consequence in whichever engine
happens to be running — which is why it caught this on webkit and not
on chromium. The new check reads the stylesheet's own `height`
declarations for the switcher and compares them with the height the
browser actually used, so it fails the moment the declaration is
removed, in every engine. It is skipped, never silently passed, in the
peerless run where the control does not exist.

**This is not E4 or E5 evidence.** The ladder above is unchanged on
that axis.

## Batch P — the card header that could not fit its own title

CI was red on Batch O, and the red was not Batch O's. It was a card
header whose title and actions could not share a line — "Recent
Authorizations" beside "Refresh" and "Clear" — and the actions were
pushed past the edge of the screen on the CI runner's font, two pixels
at 360px.

The interesting part is what the same rule did on a developer's
machine: nothing visible, because the row *shrank the title* instead.
"Recent Authorizations" wrapped onto two lines with the buttons
squeezed against them. Same broken rule, and only one of its two
outcomes happens to overflow the screen — so the local runs had nothing
to report and the runner had the whole story.

Eight card headers existed, each written as an inline style, four with
`flex-wrap` and four without. They are one class now — `.card-head`,
which wraps — and the inline styles are gone.

The gate measures the card header's own content rather than the
document's width, because the document's width is the sum of everything
on the page, and it forces the condition by giving the headers a
deliberately wide font instead of waiting for a runner's font. Deleting
the one line `flex-wrap: wrap;` fails **sixteen of twenty**
combinations, with the native font, on a developer's machine: the Relay
header ran 40.9px past its own box at 360px and 80.9px at 320px.

And the width nobody had measured: the harness looked at 360px and
640px, never at 320px, and at 320px the document overflowed on four of
the five tabs on every machine. The harness measures 320px now, on every
tab — and the first run at that width found two more rows that could
not fit and had nowhere to go: the destination pill, capped at 15rem
(240px) on a 175px row, and the peer's three actions, 287px side by
side with `flex: none`. The pill's cap is now `min(15rem, 100%)` and
the action row is shrinkable. The probe's own text measurement was
wrong as well: it reported a clipped peer name as 276px of overflow
when the span was showing an ellipsis, because a range's rectangle is
the layout extent of the text rather than the painted one.

**This is not E4 or E5 evidence.** The ladder above is unchanged on
that axis.

## Batch Q — the pages nobody had looked at

The dashboard has been reviewed since Batch K. The rest of the
product's pages had not: the OAuth relay confirmation popup, the
dead-end page beside it, and the two one-pagers a browser is shown
after a relay succeeds or fails.

They were four hand-styled pages, each with its own copy of the
design system's colours written as hex. The popup referenced **zero**
of the dashboard's 99 custom properties — every hex it used happened
to appear in the dashboard's stylesheet, so the two pages agreed by
coincidence and would have drifted the moment a token moved. They
carried the same emoji (a padlock and a check mark) that Batch K
replaced everywhere else with a drawn icon set. And the whole family
was invisible to the acceptance suite: the harness checked the
bookmarklet *box* on the dashboard, and the popup itself was covered
by a Go test that dumped the DOM out of the system Chrome and looked
for three substrings.

The tokens, the type scale and the icon set now live in one embedded
stylesheet that every page is served, so the popup cannot drift from
the dashboard. The relay pages share one shell, with prefixed class
names. The pages that were Go string literals are files now, and the
embedded-bytes test covers all of them. The emoji are the product's
own drawn glyphs.

The popup and the dead-end page are measured by the acceptance suite
on the same engines as the dashboard: the contrast sweep in both
themes, the 24×24 target floor, nothing overflowing at 360px or
320px, the icons being drawn rather than typed, the autofocused Relay
button having a visible focus ring, and the page drawing its colours
from the shared design system. Restoring the emoji and removing the
theme marker fails four of those gates — and the last one reports
white text on a white button at **1:1**.

**This is not E4 or E5 evidence.** The ladder above is unchanged on
that axis.

## Batch R — the node's own pages, and the one that could not be reached

The Hub's pages were reviewed in Batches K to Q. The product has two
more pages, and they belong to the *other* side of the product: the
machine that receives. `tantu drop` serves a QuickDrop page and
`tantu relay` serves a relay page, and neither had been looked at.

Both carried a **third palette** — their own `:root` block, a third
set of values in a product whose other pages used the design system —
and a copy of the same layout, and emoji. The tokens, the type scale
and the icon set now live in `internal/webui`, which the Hub and the
node's commands both draw from, and the node's two pages share one
shell.

**The relay page could not be reached.** `tantu relay` prints a
one-time link; the link carries the one-time value in the URL
fragment, and the server only hands out a session cookie, so the page
has to exchange it. The QuickDrop page does; the relay page never did.
The route had been registered since the page was put behind a session
and nothing ever called it. A person who opened the printed link was
told to open the printed link. The exchange now happens on the
landing page, so the page behind the gate stays gated and no
capability is minted for an unauthenticated caller.

**The icons were blocked by the page's own policy.** The node's
servers set `default-src 'self'` with no `img-src`, and every glyph in
the shared icon set is a data: URI used as a CSS mask — so the pages
rendered with their layout intact and **no icons at all**, and the
only evidence was a console message about a Content Security Policy
violation.

**The placeholders were back to the browser's default** at 3.99:1,
because the rule Batch O added was in the dashboard's stylesheet
rather than in the design system. It is in the design system now.

The acceptance suite measures both node pages on the same gates as
every other page: the design system's colours, no emoji, the icons
drawn and **nothing blocked from loading**, the contrast sweep in
both themes, and nothing overflowing at 360px or 320px.

**This is not E4 or E5 evidence.** The ladder above is unchanged on
that axis.

## Explicit "not now" adherence

Cloud telemetry, hosted accounts, auto-send, silent retry, hidden default
destination, framework migration, fake history, multi-destination broadcast:
none introduced. Verified by marker tests (no auto-send patterns) and review.

## Stop-rule assessment (plan §20)

1. P0 definition-of-done: green except keyboard/screen-reader E4 evidence.
2. P1 gates: green or explicitly accepted above with rationale.
3. No known wrong-peer, silent-success, or unsafe-retry issue: green.
4. Primary journey validated with real users + accessibility testing: **red**
   — cannot be produced in this environment; claimed nowhere in product copy.
5. Platform evidence matches claims: partial — Windows runtime-verified;
   Linux/macOS compile-verified (`docs/RELEASE.md` scopes this honestly).
6. Limitations visible in product and docs: green (stale labels, session
   notes, this file, `docs/RELEASE.md` limits).
7. Additional work is P2 differentiation: green.
8. No hidden P0/P1 behind "future work": green (deferrals named with owners
   implicit: maintainer).
9. Operation model versioned/compatible: green (`transfers.json` v1 envelope;
   no wire change).
10. One mental model across surfaces: green (shared taxonomy and vocabulary).

**Verdict:** the product is not claimed "UX-complete for the primary
journey" (gates 4–5 are red). Everything automatable is done;
the remaining program is human validation, not code.
