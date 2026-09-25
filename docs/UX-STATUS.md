# Tantu UX Status — v6.0 Plan Tracking

> **Status:** Living implementation record (agent-maintained)
> **Last Updated:** 2026-09-25
> **Plan:** `temp/tantu-ultimate-ux-upgrade-plan.md` (v6.0)

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
| Visible destination before send | Done | E2–E3 | Dashboard always-visible summary; cockpit prints destination; send/transfer outputs name it. Loopback self-sends say "local Hub". |
| Clipboard image preview/confirmation | Done | E2–E3 | Staged preview (name/size/type/destination, ≤8 MB thumbnail) for picker, drag/drop, and paste. Expert immediate-send is explicit opt-in. Marker tests forbid auto-send patterns. |
| State-aware first-run next action | Done | E2 | `tantu doctor`, `status` next-action line, dashboard next-action banner. No dead ends in CLI flows. |
| Honest transfer lifecycle | Done | E2–E3 | Operation states (completed/retryable/terminal/cancelled/duplicate-risk); completion requires remote ack + SHA match. Live loopback self-delivery verified. |
| Error data-safety/retry-safety fields | Done | E2–E3 | Canonical contract on Hub API and `send --json`: code, plain message, retry/duplicate/data flags, one next action, diagnostic ID. |
| Lost-ACK and duplicate-risk state | Done | E2 | First-class `duplicate_risk`; `transfer retry` refused; exit code 3. No blind retry anywhere. |
| Keyboard-complete primary journey | Partial | E2 | Full keyboard paths, focus-visible styles, modal Escape/trap/restore, single-pointer alternatives for drag. Screen-reader sessions (E4) not run — see gaps. |
| Focus-visible/unobscured behavior | Done | E2 | `:focus-visible` rings, scroll-margin under the sticky header, reduced-motion handling. |
| Unified plain-language vocabulary | Done | E1–E2 | "Trusted" (never "ready/online" unprobed); "File sent to X · verified"; "File may already be saved". |
| No silent wrong-peer routing | Done | E2 | Empty/multi-peer resolution errors instead of picking; unresolvable echoes; destination revalidated at dial; cockpit resolution helper tested. |
| Refresh/restart/reconnect reconciliation | Done | E2 | SSE `Last-Event-ID` replay; snapshot reload; durable `transfers.json` (last 50, 30 days) with restart-restore test; corrupt-file fail-open test. |
| Redacted diagnostic path | Done | E2–E3 | Server-side URL/secret redaction; metadata-only history and bundle; `doctor --bundle-path`. No payload/secret in logs, errors, or history (tested). |
| `tantu doctor` contract | Done | E2–E3 | Human + `--json` + bundle; exit 0/1; live-smoke verified against a real Hub. |
| Canonical operation state in Hub and CLI | Done | E2–E3 | Hub API + `transfers`/`transfer list` (+ offline fallback) + `status --json` + `send --json`. |

## P1 — coherent product

| Item | State | Notes |
|---|---|---|
| Transfer history and bounded persistence | Done | `transfers.json`, 50/30-day bounds, atomic writes, offline CLI fallback, explicit deletion (`transfer clear`, dashboard Clear). |
| Completion tombstone/idempotency | Deferred | Requires wire change (no version field; mixed-version unsupported). Mitigation: duplicate-risk state + refused blind retry. See gaps. |
| Retry/resume actions | Partial | Retry-as-new-transfer is always available (`send` again); in-place retry refused by design. Same-DropID resume stays library-only. |
| CLI/dashboard state parity | Done | Shared taxonomy (`hub.ClassifyTransferError`), shared destination language, `send --json`. |
| Standalone migration/deprecation labels | Done | `serve`/`node` print Hub pointers; usage labels advanced/compatibility; `drop`/`relay` already carried Hub tips. |
| OAuth state machine | Deferred | Relay errors stay generic by privacy design; full authorization-state surfacing needs relay-coordinator work. See gaps. |
| Browser/platform clipboard matrix | Partial | Paste-event path + picker fallback implemented and marked; matrix measurements need real browsers/platforms. |
| WCAG 2.2 AA audit | Partial | Code-level requirements implemented (names, live regions, focus, motion, drag alternative); formal audit with AT sessions not run. |
| Failure-injection suite | Partial | Offline, quota-shape, checksum-shape, lost-ACK-shape, corrupt-history, restart-during-life covered; disk-full/permission-injection not covered. |
| Performance measurement harness | Partial | Prior baselines recorded (cold start ~693 ms, idle ~14 MB, 100 MB ~12.5 MB/s); no standing harness. |
| User research baseline | Not started | Requires human participants by definition. |
| Redacted support bundle | Done | `doctor --bundle-path`, redaction tests (offline + live). |
| Upgrade/rollback UX | Done | `transfers.json` documented forward-compatible in `docs/RELEASE.md`; active-transfer guidance unchanged. |

## P2 — after the core is proven

Not started, per the plan's own stop rule (D-12, D-20): multi-file-as-separate-transfers policy documented in send (directories rejected); forwarding, notifications, QR, extension, pause/resume, WAN transport untouched.

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
