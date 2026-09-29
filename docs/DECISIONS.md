# Product Decisions (v6.0)

> **Status:** the durable decision record for the v6.0 product plan.
> **Origin:** extracted from `temp/tantu-ultimate-ux-upgrade-plan.md` (v6.0).
>
> **Why this file exists:** the plan lives under `temp/`, which is gitignored.
> That is right for a working scratch document and wrong for a record that
> three tracked documents cite and that the whole stop rule is written
> against. `docs/UX-PLAN-MAP.md`, `docs/UX-STATUS.md`, and `docs/DEV-RECORD.md`
> all reference decision IDs (D-01 … D-25) that a reader cloning this
> repository could not resolve. This file is the resolvable copy.
>
> Each decision is a constraint, not a preference: a change that contradicts
> one must say so explicitly and record why, in `docs/DEV-RECORD.md`.

## The decisions

| ID | Decision | Type | Consequence |
|----|----------|------|-------------|
| D-01 | Hub dashboard is the primary normal-user surface | Recommended | Build normal UX around Hub state |
| D-02 | CLI/cockpit are first-class power surfaces | Recommended | Same state/terminology required |
| D-03 | Standalone commands are compatibility/advanced pending migration | Recommended | No divergent UX models |
| D-04 | Clipboard image preview/confirmation by default | Recommended | One-click mode is explicit opt-in |
| D-05 | WCAG 2.2 AA is a release target | Recommended | Accessibility becomes a release gate |
| D-06 | One canonical state/event model | Required | Backend contract precedes UI polish |
| D-07 | Completion idempotency is required before retry UX claims | Required | No blind retry button |
| D-08 | WAN transport is excluded from this phase | Recommended | Focus on LAN/SSH/loopback coherence |
| D-09 | No lifecycle command promise before service semantics | Required | Avoid misleading hub stop/start UX |
| D-10 | Real-user validation is required for an "UX-complete" claim | Required | Evidence, not opinion, defines completion |
| D-11 | Preserve the embedded zero-runtime architecture initially | Recommended | Avoid framework migration without evidence |
| D-12 | No new differentiators before the core transfer/recovery flow is solid | Required | Keep scope disciplined |
| D-13 | Single-file sends first; multi-select becomes multiple logical transfers | Recommended | Avoid silent archive behavior |
| D-14 | Metadata history is bounded and payload-free | Recommended | Useful history without secret retention |
| D-15 | Original clipboard image bytes are preserved; no silent metadata stripping | Recommended | Truthful payload behavior |
| D-16 | Every user-visible state has a technical evidence level | Recommended | Prevents unsupported UX-complete claims |
| D-17 | Research is decision-oriented, not open-ended discovery | Recommended | Each study answers a product decision |
| D-18 | Four primary workspaces plus secondary diagnostics/settings in the first release | Recommended | Avoid navigation overload |
| D-19 | Snapshot-plus-events is the canonical UI consistency model | Recommended | Refresh/reconnect reconciles truth |
| D-20 | "Ultimate" has a stop rule: no new P0/P1 gaps, evidence gates green, remaining work is P2 | Required | Prevents endless redesign |
| D-21 | Risk-based confirmation replaces blanket confirmation | Recommended | Protect high-risk actions without frustrating experts |
| D-22 | Unknown outcome is a first-class state | Required | Never convert uncertainty into failure or success |
| D-23 | Surface parity is tested by shared fixtures | Required | Prevents dashboard/CLI semantic drift |
| D-24 | The support bundle is a first-class product surface | Required | Users need safe help, not only logs |
| D-25 | History is not shipped before backend truth | Required | Avoid fake UX and duplicate-risk lies |

## Decisions added after the plan

The plan predates two changes that altered its premises. Both are recorded
here so the record is complete.

| ID | Decision | Rationale |
|----|----------|-----------|
| D-26 | **A receiver rejection is a known outcome, not an unknown one.** Rejections are a typed error, classified before any byte-count inference. | A refusal with a stated reason and a fully streamed payload used to be reported as "may have completed, the file may already be saved" — sending users to check an inbox for a file the receiver had said it did not write. D-22 applied to a case where the outcome was in fact known. |
| D-27 | **The dependency surface is pinned and enumerable, not "zero".** Adding a module requires a written justification. | "Zero external dependencies" was untrue (`x/crypto`, `x/sys`) and invited treating a count as a security property. D-11 is preserved — no C libraries, no runtime install — while the count-as-security framing is rejected. See `docs/DEPENDENCIES.md`. |

## Superseded or narrowed

| Original | Status | Note |
|---|---|---|
| D-18 (four primary workspaces) | **Not adopted** | The dashboard keeps five purpose-named tabs (QuickDrop, OAuth Relay, Peers & Network, Transfers, Live Logs). Recorded as a deliberate divergence in `docs/UX-PLAN-MAP.md` §1, not an oversight. |
| D-13 (single-file sends) | **Shipped** | Multi-file send implemented as specified: independent per-file transfers, no archiving. |
