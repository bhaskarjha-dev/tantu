# Tantu UX Status — v6.0 Plan Tracking

> **Status:** Living implementation record (agent-maintained)
> **Last Updated:** 2026-09-29
> **Plan:** v6.0. Decision IDs resolve in [`docs/DECISIONS.md`](DECISIONS.md);
> the original plan document is under `temp/` and is gitignored.
>
> **Review note (2026-09-29):** the P0/P1 evidence ladder below is unchanged
> and no E4/E5 evidence was produced. The P2 section was updated: multi-file
> sends shipped, and the cockpit gained in-flight sign-in visibility. Claims in
> this file that are not re-verified on each audit should be treated as
> unconfirmed — see `docs/KNOWN-LIMITATIONS.md` §"How to use this register".

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
| Keyboard-complete primary journey | Partial | E2 | Full keyboard paths, focus-visible styles, modal Escape/trap/restore, single-pointer alternatives for drag. Screen-reader sessions (E4) not run — see gaps. **Z15: tab bar is a single tab stop with arrow-key navigation; the scrolling log console is focusable and named; closing the pairing dialog never strands focus on `<body>`.** |
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
| WCAG 2.2 AA audit | Partial | Code-level requirements implemented (names, live regions, focus, motion, drag alternative); formal audit with AT sessions not run. **Z15: measured contrast for both themes is now green across the audited surfaces (was four sub-AA elements, three of them below 1.4:1), the page has a real heading outline plus a skip link and navigation landmark, and the chatty upload live region was removed in favour of `aria-valuetext`. Still no E4/E5 evidence.** |
| Failure-injection suite | Partial | Offline, quota-shape, checksum-shape, lost-ACK-shape, receiver-rejection (Unix-gated), disk-full taxonomy + write-failure E2E, corrupt-history, restart-during-life covered; true OS disk-full condition not injected. |
| Performance measurement harness | Done (code-level) | `internal/drop/throughput_bench_test.go` (1 MiB E2E, stdlib-only) with recorded baselines; no CI schedule yet. |
| User research baseline | Not started (human-only) | Ready-to-run materials in `docs/RESEARCH-PACKET.md`. |
| Redacted support bundle | Done | `doctor --bundle-path`, redaction tests (offline + live). |
| Upgrade/rollback UX | Done | `transfers.json` documented forward-compatible in `docs/RELEASE.md`; active-transfer guidance unchanged. |

## P2 — after the core is proven

**Shipped:** multi-file-as-separate transfers. `tantu send <directory>` sends
each file as an independent transfer with its own operation ID, idempotency
key, and outcome, bounded at 2,000 files / 5 GiB, with symlinks refused and
per-file reporting. A partial batch exits non-zero and names the files that did
not arrive; a batch that delivered nothing says so rather than claiming the
rest arrived. Structure is not recreated on the receiver — paths are folded
into filenames (`pkg/util/notes.txt` → `pkg-util-notes.txt`).

Still not started, per the plan's own stop rule (D-12, D-20): forwarding,
notifications, QR, extension, pause/resume, WAN transport, directory structure
preservation, signed artifacts.

## Z15 — frontend audit: what changed and what did not

A plan-driven audit of every user-facing element produced a report and a
prioritized list; the approved items shipped in four verified slices. Full
findings, rulings, and evidence are in `docs/DEV-RECORD.md` (Batch Z15).

**Fixed (P0):** blocked preview thumbnail (CSP `blob:`); silent text-composer
success and silent over-limit rejection; pairing approval rebuilt every poll and
destroying keyboard focus; four sub-AA contrast elements.

**Calibrated by the maintainer, deliberately not done:** header/tab chrome
re-theming (kept dark in both themes on purpose — contrast fixed on what sits
on it instead); a full 44px floor (targeted bumps plus spacing, per the plan);
wholesale replacement of the remaining `prompt()` dialogs (the alias prompt is
left native as an acceptable low-risk surface).

**Still open, recorded not improvised:** the `alert()` dialogs on peer
actions; empty states without a next action; the bookmarklet's drag-only
affordance. Staleness is now declared (Z18) — when the Hub stops answering the
page says so and labels the destination as last known rather than presenting
dead data as live. `E4`/`E5` accessibility evidence cannot be produced in this
environment and is claimed nowhere.

## Where the plan stands

`docs/UX-PLAN-MAP.md` maps the v6.0 plan to shipped state item by item: P0
13/14, P1 8 done / 4 partial / 1 not started, P2 now 1 shipped (multi-file
sends) with the remainder still gated by the stop rule, release
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
