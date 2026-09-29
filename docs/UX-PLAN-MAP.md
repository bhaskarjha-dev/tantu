# UX Plan Map

> **Status:** living map. **Last reviewed:** 2026-09-29.
> **Plan:** v6.0. Decision IDs (D-01 … D-27) resolve in
> [`docs/DECISIONS.md`](DECISIONS.md), which is the tracked, durable copy.
> The original plan document lives under `temp/` and is gitignored, so it is
> not available to anyone cloning this repository.
> **Purpose:** one answer to "for each thing the plan asks for, is it done,
> deferred, or not started?" — so the state is derivable rather than
> remembered, and so an "UX-complete" claim can never outrun its evidence.

Tracking detail lives in `docs/UX-STATUS.md`; engineering detail in
`docs/DEV-RECORD.md`; admitted gaps in `docs/KNOWN-LIMITATIONS.md`. This file
is the index between the plan and those three.

---

## 1. Backlog (§14)

### P0 — must address before "UX-complete"

| Item | State | Evidence |
|---|---|---|
| Visible destination before send | **Done** | Always-visible summary; buttons name the resolved destination |
| Clipboard image preview/confirmation | **Done** | Renders (CSP unblocked, Z15); ≤8 MB thumbnail; expert opt-in is explicit |
| State-aware first-run next action | **Done** | `tantu doctor`; `status` next-action line; dashboard next-action banner |
| Honest transfer lifecycle | **Done** | File **and** text composers both report inline (Z15) |
| Error data-safety / retry-safety fields | **Done** | Canonical contract on the Hub API and `send --json` |
| Lost-ACK and duplicate-risk state | **Done** | First-class; `transfer retry` refused; no blind retry anywhere |
| Keyboard-complete primary journey | **Partial** | All paths work and are runtime-verified; **no AT session** (E4/E5) |
| Focus-visible / unobscured | **Done** | `--chrome-h` measured; `:focus` ring on the autofocused control |
| Unified plain-language vocabulary | **Done** | One state map; no raw taxonomy reaches the DOM |
| No silent wrong-peer routing | **Done** | Unresolvable rather than guessed; destination revalidated at dial |
| Refresh / restart / reconnect reconciliation | **Done** | SSE `Last-Event-ID` replay; durable `transfers.json`; staleness declared (Z18) |
| Redacted diagnostic path | **Done** | Server-side redaction; metadata-only bundle; `doctor --bundle-path` |
| `tantu doctor` contract | **Done** | Human + `--json`, exit 0/1, live-verified |
| Canonical operation state in Hub and CLI | **Done** | Shared taxonomy; `send --json`, `open --json` |

**13 of 14 done.** The exception is blocked on evidence this environment
cannot produce, and is claimed nowhere in product copy.

### P1 — required for a coherent product

| Item | State |
|---|---|
| Transfer history and bounded persistence | **Done** (last 50 / 30 days, atomic, explicit deletion) |
| Completion tombstone / idempotency | **Done** (receiver tombstone, bounded LRU, fail-closed) |
| Retry / resume actions | **Partial** — in-place retry refused by design; same-DropID resume is library-only |
| CLI / dashboard state parity | **Done** (shared taxonomy and destination language) |
| Standalone surface migration / deprecation labels | **Done** — see `docs/SURFACE-MATRIX.md` |
| OAuth state machine | **Done** (faithful subset) |
| Browser / platform clipboard matrix | **Partial** — path and fallback implemented; per-browser measurement not done |
| WCAG 2.2 AA audit | **Partial** — code-level complete and contrast-measured; no AT sessions |
| Failure-injection suite | **Partial** — offline, quota, checksum, lost-ACK, disk-full taxonomy; true OS disk-full not injected |
| Performance measurement harness | **Done** (code-level; baseline in DEV-RECORD Batch J; not gated in CI) |
| User research baseline | **Not started** — materials ready in `docs/RESEARCH-PACKET.md`; human-only |
| Redacted support bundle | **Done** |
| Upgrade / rollback UX | **Done** (`docs/RELEASE.md`) |

### P2 — after the core is proven

**One shipped:** multi-file-as-separate transfers (`tantu send <directory>`).
It was the highest-value item on this list — the most common reason a
developer points a file-send at a folder — and it is bounded, honest about
partial delivery, and reuses the existing single-file wire path with no
protocol change. Recording the exception matters more than the exception: the
stop rule (D-12) gates P2 behind a proven core, and the core was proven before
this was started.

Still **not started**: forwarding, desktop notifications, QR pairing, browser
extension, OS clipboard-image CLI, true pause/resume, advanced history search,
WAN transport, signed artifacts, and directory *structure* preservation (the
sender folds paths into filenames; it does not rebuild the tree on the
receiver). Gated by D-12 as before.

### Explicit "not now" (§14)

All honoured, none introduced: cloud telemetry, hosted accounts, automatic
peer-discovery authorization, silent auto-send, silent retry after ambiguous
acknowledgement, hidden default destination, framework migration, fake history,
multi-destination broadcast.

## 2. Phases (§13)

| Phase | State | Note |
|---|---|---|
| 0 — Evidence, inventory, decisions | **Partial** | Contracts and ledger shipped; several §21 artifacts still absent |
| 1 — Truthful first five minutes | **Mostly** | §4.1 navigation deliberately not adopted (Z13); everything else present |
| 2 — Safe send composer | **Done** | Z15 closed the last gap |
| 3 — Trustworthy transfer lifecycle | **Done** | |
| 4 — Surface convergence | **Mostly** | See `docs/SURFACE-MATRIX.md` |
| 5 — Accessibility, release-grade | **Partial** | Everything automatable done; E4/E5 outstanding |
| 6 — Differentiators | **Not started** | Correct, per D-12 |

## 3. Release gates (§16)

| Gate | State | Blocker |
|---|---|---|
| A — Truth | **Green** | — |
| B — Recovery | **Green** | Some native dialogs remain (noted, not gating) |
| C — Accessibility | **Red** | No screen-reader evidence |
| D — Evidence | **Red** | No representative users; macOS/Linux compile-only |
| E — Surface convergence | **Green** | — |
| F — Privacy | **Green** | — |

## 4. Stop rule (§20)

**8 of 10 conditions met.** Not met:

- **#4** primary journey validated with real users and accessibility testing.
- **#5** supported platforms have evidence matching their claims (Windows only).

Therefore the product is **not** described as "UX-complete for the primary
journey" anywhere in product copy, and must not be until those two close.

## 5. Required artifacts (§21)

The plan names nineteen. Current state:

| Artifact | Where it lives |
|---|---|
| `current-state-inventory` | `docs/AUDIT.md` + `docs/UX-STATUS.md` |
| `journey-map` | **absent** — journeys are described in the plan, not mapped to shipped surfaces |
| `error-matrix` | Canonical contract in code (Hub API + `ClassifyTransferError`) |
| `state-model` | `internal/hub/operations.go`, `docs/SPEC-WIRE-VERSIONING.md` |
| `operation-idempotency-contract` | Code + `docs/ARCHITECTURE.md` |
| `peer-destination-contract` | Code + `docs/ARCHITECTURE.md` |
| `transfer-idempotency-contract` | Code (receiver tombstone) |
| `content-voice-guide` | **absent** — vocabulary is enforced in code and tests, not written down |
| `accessibility-test-plan` | **absent** — `tools/uxtest/README.md` covers the automated half only |
| `research-consent-and-redaction` | `docs/RESEARCH-PACKET.md` |
| `performance-budgets` | Plan §12 + DEV-RECORD Batch J baseline; **not enforced** |
| `platform-support-matrix` | `docs/RELEASE.md` (partial) + `docs/KNOWN-LIMITATIONS.md` §1 |
| `clipboard-compatibility-matrix` | **absent** |
| `standalone-migration-matrix` | `docs/SURFACE-MATRIX.md` |
| `surface-parity-fixtures` | Shared Go tests across `cmd/tantu` and `internal/hub`; **no dedicated fixture set** |
| `release-gates` | This file §3 |
| `decision-log` | `docs/DEV-RECORD.md` + plan §18 (D-01…D-25) |
| `evidence-index` | `docs/DEV-RECORD.md` |
| `known-limitations-register` | `docs/KNOWN-LIMITATIONS.md` |

**Four remain genuinely absent:** `journey-map`, `content-voice-guide`,
`accessibility-test-plan`, `clipboard-compatibility-matrix`, plus
`surface-parity-fixtures` as a named artifact.

## 6. Journeys (§5) — highest level

| Journey | State |
|---|---|
| 1 Install / first launch | **Mostly** — `doctor` + next action; no guided first-run surface |
| 2 Pairing | **Mostly** — SAS, explicit approve, unpair consequences; two-machine path unproven |
| 3 Choosing a destination | **Done** (rendered and switchable; reachability never claimed) |
| 4 Sending text | **Done** (Z15 added the missing outcome surface) |
| 5 Sending an image | **Done** (Z15 unblocked the preview) |
| 6 Sending files | **Done** |
| 7 Receiving | **Partial** — preview/download/open/copy present; no delete, no forwarding, no search |
| 8 OAuth relay | **Done** (interstitial completed in Z15) |
| 9 Error and recovery | **Done** for the implemented failures; taxonomy tested |
| 10 Refresh / restart / reconnect | **Done** incl. staleness (Z18) |
| 11 Upgrade / rollback | **Partial** — documented, no in-product updater (correctly) |
| 12 Diagnostics / support | **Done** (redacted bundle) |
| 13 Multi-peer operation | **Partial** — layout verified; no live second machine |
| 14 Shared-directory / platform safety | **Partial** — documented; no in-product warning surface |

## 7. What would move the needle

In leverage order:

1. **A second real machine.** Moves journeys 2, 13, and stop-rule #5.
2. **Assistive-technology sessions.** Moves Gate C and stop-rule #4 — the only
   path to an "UX-complete" claim.
3. **A moderated user study.** Moves Gate D and stop-rule #4.
4. **The four absent §21 artifacts.** Cheap, and they stop this file from
   being the only map.
