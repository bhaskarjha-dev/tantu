# Tantu — Known Limitations Register

> **Status:** living register. **Last reviewed:** 2026-09-27.
> **Purpose:** one place where every admitted limitation is written down, so a
> release gate cannot quietly drift and so "we always knew" is checkable.

This register exists because the plan's stop rule (v6.0 §20, condition 6)
requires that "remaining limitations are visible in the product and
documentation". A limitation tracked only in a commit message, a chat, or an
agent's memory is not visible. If something here becomes untrue, either fix it
or move it to *Resolved* with the evidence that closed it — do not delete it.

Severity: **Blocker** (a claim is false or a journey is unsafe) ·
**Gap** (a promised capability is unproven) · **Limit** (a known narrowing).

---

## 1. Platform and runtime

| # | Limitation | Severity | Status | Where it is visible |
|---|---|---|---|---|
| 1.1 | **Linux and macOS are compile-verified only.** No runtime verification on either. | Gap | Open | `docs/RELEASE.md` "Known release limitations" |
| 1.2 | Windows is the only runtime-verified platform. | Limit | Open | `docs/RELEASE.md` |
| 1.3 | `-race` runs on Linux/macOS in CI; local Windows race execution needs a full C toolchain, so local evidence is `-count=2` lifecycle reruns. | Limit | Accepted | `docs/RELEASE.md` |
| 1.4 | Artifacts are **not signed**. No cosign configuration, and signing keys must never live in this repo. | Gap | Open | `docs/RELEASE.md`, `CHANGELOG.md` Security notes |
| 1.5 | **No release has ever been published** (`git tag -l` is empty). The goreleaser pipeline, SBOM generation, cross-compilation for darwin/linux arm64, and ldflags version stamping have never executed end to end. `docs/RELEASE.md` describes install and upgrade procedures for artifacts that do not exist yet. | Blocker for "installable" | Open | `docs/RELEASE.md` |
| 1.6 | **The quality gates added after the audit have never run on real CI.** The local Windows toolchain cannot run `-race`; `staticcheck`, `govulncheck`, the coverage floor, and the browser acceptance job are configured but unobserved. | Gap | Open | `.github/workflows/ci.yml` |

## 2. Evidence

| # | Limitation | Severity | Status | Where it is visible |
|---|---|---|---|---|
| 2.1 | **No representative-user (E4) evidence.** No moderated study has been run. | Blocker for the "UX-complete" claim | Open | `docs/UX-STATUS.md`, stop-rule #4 |
| 2.2 | **No assistive-technology (E5) evidence.** No screen-reader session, no high-contrast mode, no switch input. | Blocker for Gate C | Open | `docs/UX-STATUS.md`, Gate C |
| 2.3 | The browser acceptance harness covers **Chrome/Edge on Windows only.** Safari, Firefox, and every non-Windows browser are unverified. | Gap | Open | `tools/uxtest/README.md` |
| 2.4 | **Clipboard-image success is unmeasured per browser/platform.** The paste-event path and the file-picker fallback are implemented; which browsers expose which clipboard shapes is not measured. | Gap | Open | `docs/UX-STATUS.md` (Browser/platform clipboard matrix) |
| 2.5 | No **performance budget** in the plan's §12 sense is enforced in CI. A throughput baseline exists in DEV-RECORD Batch J; the user-perceived budgets are not gated. | Gap | Open | `docs/DEV-RECORD.md` Batch J |
| 2.6 | **A second live machine was never dialled.** Multi-peer layout, SAS derivation, and row rendering are verified from a seeded store. A real pairing handshake, a real cross-machine transfer, and reachable/unreachable transitions are unproven. | Gap | Open | `docs/DEV-RECORD.md` Batch Z16 |

## 3. Product behaviour

| # | Limitation | Severity | Status | Where it is visible |
|---|---|---|---|---|
| 3.1 | **In-place transfer retry is refused by design.** `transfer retry` teaches idempotency-key reuse instead. Same-DropID resume is library-only. | Limit | By design (D-07) | `docs/UX-STATUS.md` (Retry/resume actions) |
| 3.2 | **Directories are rejected** by send; multi-file selection is not yet separate logical transfers. | Limit | Deferred (P2, D-13) | `docs/UX-STATUS.md` P2 |
| 3.3 | **Inbox items cannot be deleted from the dashboard.** Entry-only removal strands files; file removal needs OS-trash semantics. | Limit | Deferred with rationale | `docs/DEV-RECORD.md` Batch Z13 |
| 3.4 | **Dashboard session state does not survive a Hub restart.** Sessions live in Hub memory (now 24 h sliding while the Hub lives). A restart is reported as a lost session with instructions to reopen. | Limit | By design | Dashboard session banner |
| 3.5 | A Hub **restart drops the session**, so an open dashboard tab must be reopened; there is no silent re-establishment. | Limit | By design | Dashboard session banner |
| 3.6 | **No browser availability fallback in the dashboard itself.** The recovery path is `tantu dashboard` from a terminal, not an in-page action. | Limit | Open | `docs/UX-STATUS.md` |
| 3.7 | Mixed-version operation is **unsupported** by design; pairing assumes the same release on both machines. | Limit | By design | `docs/RELEASE.md` |
| 3.8 | **A local process can still inject an OAuth callback.** The browser-CSRF defence reads `Sec-Fetch-Mode`, `Sec-Fetch-Dest`, and `Origin`, which a web page cannot forge — but a process on the same machine can simply omit them, and they are deliberately tolerated when absent so CLI OAuth flows keep working. Blocking that would mean requiring a header that non-browser clients do not send. This sits inside the documented same-user trust boundary (see THREAT-MODEL T10) and is not a remote attack path. | Limit | By design | `internal/bridge/aside.go` |
| 3.9 | **An OAuth flow with no `redirect_uri` and no `state` has no redirect binding.** `callbackExpectation` reports it as `unbound`, and the browser checks still apply, but the flow is weaker than one that carries a `state` and is not separately surfaced to the user at the call site. | Limit | Open | `internal/bridge/aside.go` |
| 3.10 | **Abandoning a sign-in closes the listener, not the browser tab.** `bridge_cancel` releases the session, its bound loopback callback port, and the wire wait immediately, and a new attempt supersedes a stale session on the same port. The authorization page already open in the browser is not closed: no supported signal distinguishes a tab the user still wants from one they abandoned, and closing tabs a script did not open is unreliable across browsers. The user closes that tab themselves. | Limit | Open, by design | `internal/bridge/session_registry.go` |
| 3.11 | **A cancel against a peer running a release without `bridge_cancel` cannot be confirmed.** The send is attempted and the reply is treated as "not released" rather than as an error, so the UI says the other machine may still release on its own timeout. Given 3.7, this only arises across a mixed-version pair. | Limit | Open, by design | `internal/bridge/cancel_client.go` |
| 3.12 | **The cockpit has no sign-in count or cancel key.** Cancellation is available from the dashboard (`/api/relay/active`) and the relay popup's Cancel button, but the terminal cockpit shows neither a count of in-flight sign-ins nor a way to stop one. | Gap | Open | `cmd/tantu/cockpit.go` |

## 4. Interface

| # | Limitation | Severity | Status | Where it is visible |
|---|---|---|---|---|
| 4.1 | **Some peer actions still use blocking native dialogs** (`alert`/`prompt`) — chiefly alias editing. Approve-pairing, unpair, and log-clear were converted. | Limit | Open, deliberately deferred | `internal/hub/web_dashboard.go` |
| 4.2 | **The bookmarklet is drag-only** as an affordance. The manual URL forwarder is the single-pointer alternative, but the two are not linked from each other. | Gap | Open | Dashboard, OAuth Relay tab |
| 4.3 | **Several empty states offer no next action** (Transfers, Authorizations, Peers). The plan's §4.10 asks for one. | Gap | Open | Dashboard |
| 4.4 | The plan's §4.1 target navigation (**Today / Send / Transfers / Peers & Trust** + secondary) was **not adopted**; the dashboard keeps five purpose-named tabs. | Limit | Decided (Z13 audit) | `docs/DEV-RECORD.md` Batch Z13 |
| 4.5 | The log console is **deliberately dark in both OS themes** (terminal convention) while the surrounding chrome follows the theme. | Limit | Decided, documented | `internal/hub/web_dashboard.go` |
| 4.6 | The header and tab-bar chrome stay **dark in the light theme on purpose**; contrast is fixed on what sits on it rather than by re-theming. | Limit | Decided | Same |
| 4.7 | No **dashboards beyond the five tabs** — no settings surface, no history search, no filtering on transfers or authorizations. | Gap | Deferred (P2) | `docs/UX-STATUS.md` P2 |

## 5. Privacy and data

| # | Limitation | Severity | Status | Where it is visible |
|---|---|---|---|---|
| 5.1 | **Received items are session-local and in memory only.** They are cleared on Hub restart and are not persisted. Inbound persistence was evaluated and rejected for privacy. | Limit | By design | Dashboard inbox note |
| 5.2 | Transfer history is **metadata-only and bounded** (last 50, 30 days). No payload bytes, clipboard contents, text snippets, or full OAuth URLs are ever stored. | Limit | By design (D-14) | `docs/ARCHITECTURE.md` |
| 5.3 | The client-side 10 MiB text limit **mirrors** the server default rather than querying it. A non-default server limit would make the early rejection wrong (the server still enforces the truth). | Limit | Accepted | `internal/hub/web_dashboard.go` |
| 5.4 | The SAS word list has **no automated confusable-pair check.** Words are unique, 4-6 lowercase ASCII letters, and drawn from a 512-entry list, but a mechanical "these two words look alike" test is not implemented, so a future edit could introduce a visually similar pair and weaken a spoken comparison. | Limit | Open | `internal/pairing/cert.go` |

## 6. Resolved

Recorded so the register shows movement, not just accumulation.

| Limitation | Resolved by | Evidence |
|---|---|---|
| Send preview never rendered (CSP blocked `blob:`) | Z15 | `TestWebDashboard_SendPreviewImageSchemeIsAuthorized`, harness decode check || Text send reported nothing on the composer's own tab | Z15 | `TestWebDashboard_TextComposerReportsOutcomeInline` |
| Pairing approval rebuilt every 3 s, destroying keyboard focus | Z15 | `TestWebDashboard_AccessibilityMechanicsMarkers`, harness focus-hold check |
| Four sub-4.5:1 contrast elements, three below 1.4:1 | Z15 | Harness computed-contrast sweep, both themes |
| Multi-peer badge colours and 360 px overflow | Z16 | `TestWebDashboard_MultiPeerSurfaceMarkers` |
| Dead Hub left live-looking data on screen | Z18 | `TestWebDashboard_StalenessIsDeclared` |
| `tantu send` shipped a mistyped path as a text snippet and reported success | audit 2026-09-28 | `TestSendPathHintClassification`, plus the four mistyped-path shapes rejected end to end |
| A crash mid-publish left a truncated file under a delivered name | audit 2026-09-28 | `TestPublishStagedFileIsAtomicAndVisibleOnlyWhenComplete`, `TestSweepPublicationTempsReclaimsOnlyStaleTemporaries` |
| The "verified" digest described a different byte stream than the file published | audit 2026-09-28 | `TestFinalizeIncomingPartRejectsDigestMismatch`, `TestFinalizeIncomingPartRejectsSizeMismatch` |
| `tantu drop` served its own API credential in unauthenticated HTML | audit 2026-09-28 | `TestLocalSessionIsOneUseAndPageCarriesNoCredential` |
| The pairing SAS was 24 bits, static, and broadcast in cleartext on the LAN | audit 2026-09-28 | `TestTranscriptSASIsBoundToTheSession`, `TestBeaconPayloadCarriesNoIdentity` |
| A web page could inject an OAuth code into a local login | audit 2026-09-28 | `TestCallbackRejectsPageInjectedFetch`, `TestUnboundFlowStillRequiresNavigation` |
| A suppressed duplicate was reported as a verified success | audit 2026-09-28 | `TestE2E_SuppressedDuplicateReachesTheSender` |
| The relay popup's Cancel only closed the window, and the abandoned sign-in kept its loopback callback port — so a retry failed with "port in use" and only a process restart recovered | audit 2026-09-28 | `TestE2E_CancelReleasesCallbackPortImmediately`, `TestE2E_RetryOnSamePortSupersedesAbandonedSession`, `TestSessionRegistry_CancelOnPortReleasesStaleSession` |
| An in-flight sign-in was invisible on every surface, so "why is my login stuck" had no answer | audit 2026-09-28 | `TestWebDashboard_RelayActiveListsInFlightRelay`, `TestHub_SharesSessionRegistryWithDispatcher` |

---

## How to use this register

1. **Before calling anything "UX-complete"**, read §1 and §2. Any open Blocker
   or Gap there means the claim is not available.
2. **When adding a limitation**, add it here in the same change that introduces
   it, and make it visible in the product or docs too. A limitation recorded
   only here has not satisfied the stop rule.
3. **When fixing one**, move the row to §6 with the evidence that closed it.
   Deleting a row loses the reason a future change might reintroduce it.
4. **Re-review at each release.** The date at the top is the last review; if it
   is stale, treat every row as unconfirmed.
