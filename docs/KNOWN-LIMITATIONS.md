# Tantu — Known Limitations Register

> **Status:** living register. **Last reviewed:** 2026-09-29.
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
| 1.5 | **No release has ever been published** (`git tag -l` is empty). All six published targets were cross-compiled successfully on 2026-09-29 (linux/darwin/windows × amd64/arm64, CGO_ENABLED=0, release ldflags), so the build matrix is proven; the goreleaser pipeline, SBOM generation, and the release artifacts themselves have still never executed end to end. `docs/RELEASE.md` describes install and upgrade procedures for artifacts that do not exist yet. | Blocker for "installable" | Partly closed 2026-09-29 | `docs/RELEASE.md` |
| 1.6 | **The quality gates are now exercised locally, but CI itself is still unobserved.** `staticcheck` and the encoding gate have been run to completion on this machine and pass; `-race` still cannot run locally on Windows, and `govulncheck`, the coverage floor, and the browser acceptance job have not been executed here. | Gap | Partly closed 2026-09-29 | `.github/workflows/ci.yml` |

## 2. Evidence

| # | Limitation | Severity | Status | Where it is visible |
|---|---|---|---|---|
| 2.1 | **No representative-user (E4) evidence.** No moderated study has been run. | Blocker for the "UX-complete" claim | Open | `docs/UX-STATUS.md`, stop-rule #4 |
| 2.2 | **No assistive-technology (E5) evidence.** No screen-reader session, no high-contrast mode, no switch input. | Blocker for Gate C | Open | `docs/UX-STATUS.md`, Gate C |
| 2.3 | The browser acceptance harness covers **Chrome/Edge on Windows only.** Safari, Firefox, and every non-Windows browser are unverified. | Gap | Open | `tools/uxtest/README.md` |
| 2.4 | **Clipboard-image success is unmeasured per browser/platform.** The paste-event path and the file-picker fallback are implemented; which browsers expose which clipboard shapes is not measured. | Gap | Open | `docs/UX-STATUS.md` (Browser/platform clipboard matrix) |
| 2.5 | No **performance budget** in the plan's §12 sense is enforced in CI. A throughput baseline exists in DEV-RECORD Batch J; the user-perceived budgets are not gated. | Gap | Open | `docs/DEV-RECORD.md` Batch J |
| 2.6 | **A second live machine has now been dialled.** A real peer at `192.168.0.178:9877` (a distinct host: different IP and MAC) accepted verified text and 8 MiB file transfers at 15.0 MiB/s, refused a reused idempotency key, and returned a hard failure for an unpaired address. Pairing ceremony, discovery, and inbound approval are still unexercised, and evidence is one-directional: nothing has been observed on the peer's own dashboard. | Gap (partly closed) | Partly closed 2026-09-29 | `docs/EVIDENCE-TWO-MACHINE.md` |

## 3. Product behaviour

| # | Limitation | Severity | Status | Where it is visible |
|---|---|---|---|---|
| 3.1 | **In-place transfer retry is refused by design.** `transfer retry` teaches idempotency-key reuse instead. Same-DropID resume is library-only. | Limit | By design (D-07) | `docs/UX-STATUS.md` (Retry/resume actions) |
| 3.2 | **Directories are now sent as multiple independent transfers** (D-13), bounded at 2,000 files / 5 GiB, with symlinks refused and per-file outcomes. Structure is **not** preserved on the receiver: `a/b.txt` arrives as `a-b.txt` in a flat folder, so a two-file tree with colliding basenames is disambiguated by path, not by directory. A whole directory arriving as an archive remains unsupported. | Limit | Partly closed 2026-09-29 | `tantu send <dir>` |
| 3.3 | **Inbox items cannot be deleted from the dashboard.** Entry-only removal strands files; file removal needs OS-trash semantics. | Limit | Deferred with rationale | `docs/DEV-RECORD.md` Batch Z13 |
| 3.4 | **Dashboard session state does not survive a Hub restart.** Sessions live in Hub memory (now 24 h sliding while the Hub lives). A restart is reported as a lost session with instructions to reopen. | Limit | By design | Dashboard session banner |
| 3.5 | A Hub **restart drops the session**, so an open dashboard tab must be reopened; there is no silent re-establishment. | Limit | By design | Dashboard session banner |
| 3.6 | **No browser availability fallback in the dashboard itself.** The recovery path is `tantu dashboard` from a terminal, not an in-page action. | Limit | Open | `docs/UX-STATUS.md` |
| 3.7 | Mixed-version operation is **unsupported** by design; pairing assumes the same release on both machines. | Limit | By design | `docs/RELEASE.md` |
| 3.8 | **A local process can still inject an OAuth callback.** The browser-CSRF defence reads `Sec-Fetch-Mode`, `Sec-Fetch-Dest`, and `Origin`, which a web page cannot forge — but a process on the same machine can simply omit them, and they are deliberately tolerated when absent so CLI OAuth flows keep working. Blocking that would mean requiring a header that non-browser clients do not send. This sits inside the documented same-user trust boundary (see THREAT-MODEL T10) and is not a remote attack path. | Limit | By design | `internal/bridge/aside.go` |
| 3.9 | **An OAuth flow with no `redirect_uri` and no `state` has no redirect binding.** `callbackExpectation` reports it as `unbound`, and the browser checks still apply, but the flow is weaker than one that carries a `state` and is not separately surfaced to the user at the call site. | Limit | Open | `internal/bridge/aside.go` |
| 3.10 | **Abandoning a sign-in closes the listener, not the browser tab.** `bridge_cancel` releases the session, its bound loopback callback port, and the wire wait immediately, and a new attempt supersedes a stale session on the same port. The authorization page already open in the browser is not closed: no supported signal distinguishes a tab the user still wants from one they abandoned, and closing tabs a script did not open is unreliable across browsers. The user closes that tab themselves. | Limit | Open, by design | `internal/bridge/session_registry.go` |
| 3.11 | **A cancel against a peer running a release without `bridge_cancel` cannot be confirmed.** The send is attempted and the reply is treated as "not released" rather than as an error, so the UI says the other machine may still release on its own timeout. Given 3.7, this only arises across a mixed-version pair. | Limit | Open, by design | `internal/bridge/cancel_client.go` |
| 3.12 | The cockpit now shows in-flight sign-ins and can release one. The banner carries a standing warning while any sign-in is open, and `[l]` lists destination, state, and age before offering to cancel. A relay still in `starting` is labelled distinctly from one already on the wire, and a mis-typed selection is refused rather than guessed — releasing the wrong login is worse than releasing none. | Gap | Closed 2026-09-29 | `cmd/tantu/cockpit.go` |

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
| 5.4 | The SAS word list now has an **automated confusable-pair check, bounded by what the list can actually achieve.** Measured on the real 512-word list: **zero** prefix pairs where ~900,000 are expected by chance (a deliberate property, now asserted exactly), and 772 single-substitution pairs, which is ordinary for English words ("bear"/"beat") and bounded at 900 so the list cannot silently become uncomparable. The check is mechanically decidable only — rhyme and homophony still need a human. | Limit | Partly closed 2026-09-29 | `internal/pairing/sas_test.go` |
| 5.5 | **Documentation claims are not systematically verified against code.** Three separate false claims were found in one session (the encoding gate that could not detect its own damage, the SAS comment describing a test that did not exist, "zero external dependencies"). Each was a *stated* property with nothing checking it. The encoding gate is now self-verifying and the dependency surface is enumerated in `docs/DEPENDENCIES.md`, but there is no general check that a doc claim matches the code it describes. | Gap | Open | this document, §6 |

## 6. Resolved

Recorded so the register shows movement, not just accumulation.

| Limitation | Resolved by | Evidence |
|---|---|---|
| A refused send was reported as "may have completed, the file may already be saved", sending users to verify a file the receiver had just said it did not write | audit 2026-09-29 | `TestClassifyTransferError_ReceiverRejectionIsKnown`, `TestE2E_IdempotencyKeyConflictIsAKnownRejection` |
| Live Activity Logs stayed empty through a verified send | audit 2026-09-29 | `TestWebDashboard_SuccessfulSendIsVisibleInActivityLog`, `TestWebDashboard_StartupIsVisibleInActivityLog` |
| The UTF-8 gate reported the tree clean while 27 committed sites were mis-decoded across 6 files | audit 2026-09-29 | `TestCountMojibakeCatchesTheShippedDamage`, `TestCountMojibakeRunsCatchesUnreversibleDamage`, `TestCountFormatCharsCatchesInvisibleDamage` |
| Every OAuth-bridge and multiplexer log message shipped as mojibake, and eleven had lost their entire text and format verbs | audit 2026-09-29 | `go vet ./...` (format-verb check), `tools/encgate` |
| A test compared `relayRequestKey(...)` with itself and could never fail | audit 2026-09-29 | `TestRelayRequestKey_DistinguishesAttempts` |
| The CI staticcheck pin could not read the toolchain's export data, so the step could never pass | audit 2026-09-29 | `staticcheck.conf`, `go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...` exits 0 |
| The SAS word list's own comment claimed it excluded `i l o 0 1` and that a confusable test existed; neither was true (305 of 512 words contain those letters) | audit 2026-09-29 | `TestSASWordListHasNoMechanicalConfusables`, comment corrected in `cert.go` |
| `AGENTS.md` claimed "Zero External Dependencies" while the module requires `x/crypto` and `x/sys`; the wording invited treating a count as a security property | docs audit 2026-09-29 | `docs/DEPENDENCIES.md` (inventory + conditions), invariant restated as "no C libraries, no runtime install" |
| A directory send dropped `--peer` on the delegation path and delivered to the active peer while reporting success | 2026-09-29 | `tantu send <dir> --peer <untrusted>` returns `destination_unreachable` for every file |
| A batch that delivered nothing exited 0 in human mode, and printed "the rest arrived" | 2026-09-29 | `TestBatchExitCodeIsWorstOutcome`, `TestBatchStatusReflectsOutcomes` |
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
