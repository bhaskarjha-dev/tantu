# Tantu Changelog

User-facing summary of what changed and why. The authoritative engineering
record (issue ledger, evidence, rejected hypotheses) lives in
`docs/DEV-RECORD.md`. See `docs/RELEASE.md` for install, upgrade, and
rollback procedures.

## Unreleased

### Added
- `docs/DEPENDENCIES.md` — the complete dependency inventory (two pinned
  `golang.org/x` modules), the reasoning behind the `CGO_ENABLED=0`
  static-build invariant, the demonstrated cost of that constraint, and four
  conditions any new module must satisfy.
- `docs/DECISIONS.md` — the v6.0 decision record (D-01 … D-27), extracted so
  that decision IDs cited by three tracked documents resolve for anyone
  cloning the repository. The original plan lives under gitignored `temp/`.
- **Multi-file sends.** `tantu send <directory>` now sends every file under a
  directory as an independent transfer, following the recorded decision D-13
  ("multi-select becomes multiple logical transfers"). It is not archived: each
  file gets its own operation ID, its own idempotency key, and its own outcome,
  so 40 of 50 arriving is reported as 40 of 50 rather than as one failure
  nobody can reason about. Files are named from their path relative to the
  directory (`docs/api/reference.md` arrives as `docs-api-reference.md`), so
  two files with the same basename in different subdirectories do not collide
  on the receiver's flat downloads folder. Symlinks are refused rather than
  followed or silently skipped, because a link could otherwise pull in files
  outside the directory — and a silently shortened batch is indistinguishable
  from a complete one. Expansion is bounded at 2,000 files and 5 GiB total, and
  fails at the point of the mistake rather than deep into a run.
  `tantu send <dir> --json` emits a batch contract (`status`, per-file
  `files[]` with the same safety vocabulary as a single send, `total`,
  `succeeded`, `failed`, `next_action`), and the exit code is the worst outcome
  in the batch — a partial send is never reported as success.
- **Cockpit sign-in visibility.** Pressing `[l]` lists in-flight OAuth
  sign-ins with their destination, state, and age, and offers to release one.
  The banner also shows a standing warning while any sign-in is open. An
  in-flight sign-in was previously invisible from the terminal, so a CLI that
  never returned looked exactly like a hung process — and because the peer
  holds a loopback callback port for the duration, and an application's
  redirect port is normally fixed, an abandoned sign-in blocked the retry that
  would have followed.
- The cockpit's file prompt now accepts a directory and streams it the same
  way `tantu send` does, rather than telling the user to archive it first.
- `tantu dashboard` and a capability-protected `POST /api/dashboard-url` for
  fresh authenticated dashboard links in headless/recovery workflows.
- Durable private staging manifests bind resumable partials to transfer
  metadata and a head hash; mismatched partials fail closed.
- Default aggregate QuickDrop transfer quotas bound long-lived Hub and
  standalone receivers, including unknown-size streams.
- Cross-process identity/peer mutation locks and persisted active-peer state.
- SSE reconnects replay buffered events with `Last-Event-ID`.
- Headless startup now prints the one-time authenticated dashboard URL.
- Received-file serving on the Hub dashboard: inline image previews (8 MB
  raster-only, sandboxed) and per-file downloads for QuickDrop inbox items.
- `status` now reports version, store path, Hub liveness with dashboard URL,
  identity, and peers — a real diagnostic command.
- CLI warns on Hub/CLI version skew during delegation; Hub logs it deduped.
- SSH host-key verification UX: `--ssh-known-hosts` and `--ssh-fingerprint`
  (`SHA256:` pin) on `send`, `open`, `drop`, and `relay` (forwarded through
  `wrap`).
- Dashboard stale-session banner: an explicit reconnect notice instead of
  silent 401 failures when opened without a session (stale bookmark/second
  tab).
- Dev-build version provenance: plain `go build` binaries report
  `1.0.0-dev+<sha>[.dirty]` instead of a bare `1.0.0`.
- Clipboard image paste-to-upload on the dashboard; real XHR upload progress
  with cancel; GB/TB sizes; ARIA live regions; keyboard-operable drop zone;
  labelled inputs; inline SVG favicon.
- Sender-side transfer operations: every Hub upload returns an operation ID
  with explicit retry/duplicate/data safety and one next action
  (`GET /api/transfers/recent`, `tantu transfers`, `tantu transfer list`).
  History is durable (last 50, 30 days, `transfers.json`) and survives Hub
  restart; the CLI falls back to last-saved history when the Hub is stopped.
- `tantu doctor` diagnostics with plain-language checks and a safe next
  action (human and `--json`); `tantu status --json` shares the contract.
- A dependency-free browser acceptance harness for the dashboard
  (`node tools/uxtest/run.mjs`, optional `--peers`). It needs Node and a
  Chrome/Edge binary, adds nothing to the product's runtime, and is not
  required to build, test, or run Tantu.
- Dashboard safe-send composer: file/image preview with destination,
  size, and type before Send, explicit expert immediate-send opt-in,
  always-visible destination, next-action banner, and a Transfers tab backed
  by durable sender truth (last 50, 30 days).
- `tantu doctor --bundle-path` redacted support bundle (health report,
  transfer metadata, Hub logs; metadata only, owner-only file).
- `tantu send --json` machine-readable contract (operation ID, destination,
  verification, safety flags; never payload) with distinguished exit codes
  (0 success, 1 failure, 2 invalid input, 3 duplicate-risk). Direct sends
  assign and report their wire DropID; delegation surfaces the Hub's
  operation truth including duplicate-risk errors.
- `tantu transfer clear --yes` and dashboard Clear buttons delete
  sender-side transfer and authorization metadata (received files untouched)
  via an authenticated API or, when the Hub is stopped, the saved files
  directly.
- `tantu open --json` machine-readable contract (operation ID, destination;
  never the URL) with exit codes 0/1/2; relay API responses now carry the
  recorded attempt ID, destination, and next action, and the dashboard names
  the destination in relay outcomes.
- `tantu status` ends with one recommended next action; standalone
  `serve`/`node`/`relay`/`drop` surfaces label themselves advanced or
  compatibility and point at the Hub.
- Pairing dialog traps Tab focus while open; cockpit file/text sends print
  the resolved destination before transmitting.
- Dashboard sessions last 24 hours sliding from last use (renewed on every
  authenticated call) instead of expiring absolutely after 30 minutes, so
  bookmark popups survive idle gaps under a day; Hub restarts still rotate
  all sessions.
- Spent bootstrap links explain themselves: the dashboard names the failure
  (used link vs unreachable Hub vs full sessions) instead of one generic
  banner, failed exchanges drop the dead fragment so reloads and duplicated
  tabs cannot replay them, and every `o` press prints a freshly minted link.
- Callback-port bind failures distinguish OS-forbidden ports from in-use
  ports (notably Windows Hyper-V exclusions), with an actionable message.
- Premium visual refresh with zero dependencies: design-token system,
  OS-driven light/dark themes, purposeful motion with full reduced-motion
  support, upload speed and ETA, status chips, and a matching interstitial,
  relay pages, and pairing dialog.
- Information-architecture audit fixes: one shared send-destination header
  governing file and text sends, pairing approval banner above guidance,
  "Received Items" labeled session-local, and expert mode stated
  per-session.
- OAuth relay attempts recorded with target peer, origin host only, state,
  and next action (`GET /api/relay/recent`, dashboard card, support bundle;
  durable last 50 / 30 days; never URLs, codes, or tokens).
- The OAuth bookmarklet is permanent and ticket-free: unauthenticated clicks
  render a confirmation interstitial (safe origin, destination lookup,
  explicit auto-focused Relay button for Enter-to-confirm) instead of dying
  on an expired one-use ticket, and the relay still requires a click plus a
  valid session at POST time. The popup watches for a session and enables
  itself (once, with announcement) when one appears — no bookmark retry.
- Repeat logins are never coalesced: session keys bind the exact request
  URL, so a new login (fresh state/PKCE) always opens its own browser flow
  while byte-identical retries still share one.
- Wire versioning phase 1: envelopes carry `v: 1` and drop metadata carries
  a validated `idempotency_key` (Hub: operation ID, overridable per request;
  CLI: DropID, overridable via `--idempotency-key`).
- Receiver completion tombstones: same-key redelivery streams to discard,
  verifies the digest, and re-acknowledges without staging, publishing, or
  duplicate inbox/history (bounded LRU 1024, 24 h retention, durable file,
  fail-closed mismatch). Re-run a send with the same `--idempotency-key`
  for a duplicate-safe retry; `transfer retry` teaches this pattern.
- Changing the dashboard downloads directory now states that previously
  received files stay in their former location.

### Changed
- **A receiver that refuses a transfer is now reported as a known outcome.**
  Reusing an `--idempotency-key` with different content was refused by the
  receiver, but the sender matched on wording and, because every byte had
  already been streamed, reported it as "may have completed, the file may
  already be saved" and marked it unsafe to retry. That sent users to check a
  receiver inbox for a file the receiver had just said it did not write.
  Rejections are now a typed error, classified before any byte-count guessing:
  the console shows `idempotency_key_conflict` (or `integrity_rejected` /
  `receiver_busy`) with a safe-to-retry flag and an actionable next step. A
  genuinely lost acknowledgement is still reported as an unknown outcome.
- **Live Activity Logs now records the primary journey.** A verified send left
  no entry at all, so the tab — a headline surface, also part of the support
  bundle — stayed empty through a working transfer. Successful sends, failed
  sends (with the plain classification, never a raw transport string), relay
  completion, and Hub startup are all logged now.
- The Hub logs its own configuration once at startup (version, transport, P2P
  and dashboard addresses, downloads directory), so the log has useful content
  the moment it is opened instead of only after something happens.
- The Hub dashboard now receives a per-response CSP nonce and uses delegated
  DOM events instead of inline JavaScript handlers; stale unauthenticated tabs
  show recovery guidance without firing a burst of 401 probes.
- Dashboard transfer and authorization records now read in plain language
  ("File saved on peer", "Not delivered", "File may already be saved") instead
  of raw internal state names, carry a date as well as a time (they are kept
  for 30 days), and render duplicate risk as a warning rather than a failure.
  The send and relay buttons name the resolved destination, the drop zone
  names the transport actually in use instead of claiming mTLS
  unconditionally, and a local loopback self-send is no longer labelled
  "Unauthenticated Peer" in the inbox.
- Approving an inbound pairing request and unpairing a peer now confirm what
  they change and whether it is reversible; clearing the log feed confirms
  that the Hub's own log and the other ledgers are untouched.
- The OAuth relay confirmation popup declares a language and viewport, gains a
  main landmark and heading, meets 44px targets, and reports a server fault as
  a server fault rather than as a missing dashboard session.
- Peer status language now distinguishes paired/trusted state from live
  connectivity, and discovery SAS values are explicitly labeled unverified.
  The dashboard header no longer claims reachability it has not probed
  ("Trusted" instead of "ready/online"); the send destination stays visible
  for single-peer setups, and clipboard/file sends stage for preview and
  confirmation instead of auto-sending.
- `tantu send` failures now exit 2 for invalid input and 3 for unknown
  outcomes with duplicate risk (previously all failures exited 1), in both
  human and `--json` modes; success output additionally names the
  destination and operation ID.
- OAuth `redirect_uri` without an explicit loopback port is now rejected
  (previously silently defaulted to 80/443).
- Peer resolution no longer silently picks: cross-tier SAS/fingerprint
  collisions error, and an empty query with multiple peers and no default
  errors instead of taking the first peer.
- Post-success OAuth deduplication replays were removed: a duplicate request
  after completion runs the flow again (correct) instead of reporting a
  delivery that never happened for that request. In-flight coalescing is
  unchanged.
- Standalone `relay` pages mint per-request one-time tickets (single-use,
  10-minute TTL) for the bookmarklet and the manual form; the per-process
  token remains as a legacy fallback.

### Fixed
- **"Cancel sign-in" was a dead button on every Hub.** The delegated click
  listener is bound to `document`, so inside it `this` is the document, but
  the handler read the operation id with `this.getAttribute(...)`. Every
  click threw `Uncaught TypeError: this.getAttribute is not a function`,
  the in-flight sign-in stayed stuck, and the user got no feedback — while
  the acceptance harness still reported 29/29 passing, because nothing in
  the suite ever invoked a delegated action. The handler now reads from the
  clicked `target`, and the defect class is closed by two independent
  gates: a static test (`internal/hub/delegation_test.go`) that fails if any
  delegated handler reads state from `this` and verifies all 32
  `data-action` values have a handler, and a behavioral sweep in
  `tools/uxtest/run.mjs` that clicks every action in a real browser (32/32;
  with the bug reintroduced it fails 30/32 and names the dead action).
- **`tantu transfer list --json`, `transfer clear --yes`, and
  `transfer list --store-dir DIR` all failed with
  `Unknown transfer subcommand: --json`.** `NormalizeArgs` hoists flags
  ahead of positionals, but `runTransfer` dispatched on the first token of
  the reordered rest — so every documented flag-bearing transfer command
  misdispatched. Dispatch now separates leading flags from the subcommand
  (`splitLeadingFlags` / `transferDispatch`), covered by an 18-subtest
  regression test in `cmd/tantu/transfer_dispatch_test.go`.
- The UX acceptance harness now starts deterministically and fails with a
  reason: a bounded readiness probe replaces the fixed 4-second sleep, hub
  stdout/stderr is captured and printed on failure (so a port clash reads as
  a port clash instead of `fetch failed`), navigation waits for the location
  hash to settle, and free required ports are documented in
  `tools/uxtest/README.md`.
- **CI had been red for fourteen consecutive runs (runs #6–#19), for three
  independent reasons, all now fixed.** (1) The workflows pinned Go `1.26.3`,
  and `govulncheck` scans the standard library *that toolchain builds*: 1.26.3
  carries 9 reachable stdlib vulnerabilities (GO-2026-6218 … GO-2026-4970,
  fixed across 1.26.4–1.26.6), so the quality gate exited 3 on a perfectly
  clean tree. Both workflows now pin `1.27.1`, verified locally with
  `GOTOOLCHAIN=go1.27.1 govulncheck ./...` → *No vulnerabilities found*
  (exit 0), against `GOTOOLCHAIN=go1.26.3` → 9 findings (exit 3).
  (2) `TestRelayCoordinator_LeaderSurvivesCallerDisconnect` had a scheduling
  race of its own: it signalled `flowStarted` from a watcher goroutine
  launched *before* the `Do` goroutine, so the signal could arrive before
  `Do` ran, `cancel()` then won the race, and `Do` returned at its context
  check without ever inserting the relay — `0 open relays, want 1`. The
  signal now closes inside the leader callback, after `call.register`, which
  `Do` reaches only once the relay is already in `c.active`. It failed 6 of
  25 runs under `-race` before the change and 0 of 60 after. (3) The browser
  harness exited 3 in CI on the fixed 4-second startup timer described above.
  CI is publicly observable through the unauthenticated GitHub API, which is
  how these were found: every run, job, and check annotation is readable,
  only the raw logs need admin rights. Run #24 (2026-09-30) was the first
  fully green run since #5 — quality gates, all three OS test jobs and the
  browser acceptance job together — and with three consecutive green runs
  behind it, `dashboard-acceptance` no longer carries `continue-on-error`:
  the browser harness is a blocking gate again.
- The race detector now runs locally on Windows too: with a C toolchain on
  `PATH` (mingw-w64 gcc 16.2.0) `go test -race -count=1 ./...` completes
  green across all 13 packages, so the Linux/macOS-only coverage that the
  docs described as CI-exclusive no longer has to wait for a push.
- **Every received file's saved path was a bare file name, so the Hub could
  not serve a file anyone actually received.** The Hub's `publishPartFile`
  returned `filepath.Base(name)` on the rename path and `publishViaTemp`
  returned the bare name on the copy path — so the inbox recorded
  `photo.png` rather than its path. `serveReceivedFile` resolves
  `saved_path` with `filepath.Abs` before its containment check, and a bare
  name resolves against the *Hub's working directory*: the check failed
  closed (right for an escape attempt, wrong for a normal receive), and
  inline preview, the download link, and the standalone `drop` server's
  `/api/download` all 404'd for every received file on every platform. The
  receipts printed by `tantu receive` and `node` lost the directory as well,
  and `publishIncomingFile` in the CLI had the same split, so only the copy
  path was ever correct, and only where the copy path runs. Both paths now
  return the full published path. The defect had no failing test because
  every test that touched this code injected an absolute `SavedPath` by hand;
  `TestHub_ReceivedFileSavedPathIsAbsoluteAndServes` now sends a real file
  through the loopback and asserts that the recorded path resolves to the
  received bytes and that `/api/drop/file` serves them.
- **A published file's digest was checked only on the copy path.** The
  preferred path is a same-filesystem rename, which moves bytes without
  reading them, so on macOS and Linux neither `finalizeIncomingPart` nor
  `finalizePartFile` compared the staged bytes against the digest they
  reported as verified — `TestFinalizeIncomingPartRejectsDigestMismatch`
  failed there for precisely that reason while Windows passed, because a file
  that is still open cannot be renamed there and so always takes the copy
  path. The staged bytes are now re-read through the receiver's own descriptor
  and verified before either path can expose the delivered name
  (`drop.VerifyStagedDigest`): one sequential read, no write, peak disk still
  N, and no published name ever appears with content that has not been
  checked. `TestFinalizePartFileVerifiesDigestAndReturnsFullPath` covers the
  Hub's copy of this code.
- **The test guarding receiver rejection never exercised one.**
  `TestWebDashboard_UploadReadOnlyOutputDir` chmod'd the output directory and
  then sent a *text* drop — a payload that lives in an in-memory buffer and
  never touches the output directory — so it asserted 500 while receiving 200
  on Linux and macOS, and it also asserted a terminal ledger state that the
  classifier reports as retryable by contract. It now sends a file, which is
  the branch that creates `.tantu-staging` inside the output directory and
  publishes into it, and it asserts what the classifier really produces for a
  rejection: `receiver_rejected`, retry-safe, no duplicate risk, data safe,
  one ledger record, and no published file. A second variant blocks
  `.tantu-staging` with a regular file instead of permission bits, so the same
  contract runs on Windows too — where the permission variant is skipped, and
  where those assertions had therefore never executed on any machine anyone
  could debug them on.
- **A directory send ignored `--peer` and delivered to the active peer
  instead.** On the delegation path the explicit target was dropped, so a batch
  aimed at one machine arrived on another and reported success. The
  destination is now carried through every delegated upload.
- A batch that delivered nothing exited 0 in human mode (the JSON path was
  correct), which would have let a script read a total failure as a completed
  send.
- A batch where nothing arrived printed "re-send the failed files, the rest
  arrived" — reassuring the user that files were on the peer when none were.
- **Log messages in the OAuth bridge and multiplexer were shipping as
  mojibake, and some had lost their entire text.** Every `logf` call in
  `internal/bridge/aside.go` and `internal/bridge/dispatcher.go` had its
  status emoji mis-decoded, and the damage had also swallowed the message
  bodies — the receiver's interstitial heading read `✅` as a broken glyph,
  and eleven log lines were reduced to a damaged symbol plus arguments that
  no longer matched any format verb. The damage was present in the very
  first commit. All messages, emoji, and format strings are restored.
- **The UTF-8 encoding gate no longer passes on the damage it exists to catch.**
  The check looked for a lead character followed by a byte in `0x80-0xBF`, but
  a Windows-1252 mis-decode of the characters that dominate prose — em dash,
  curly quotes, ellipsis, emoji — substitutes a character from the `U+2000`
  block instead, so it never fired. Sixteen real damage sites were committed
  and the gate reported the tree clean. It now reverses the mis-decode and
  accepts a run only when the recoded bytes are valid UTF-8 *and* differ from
  what is in the file, so damaged text is caught and legitimate text (an accented
  letter followed by a real em dash) is not. The damaged prose, comments, and a
  log-message emoji were repaired.
- **The encoding gate now catches the damage it previously could not.** Two of
  the three damage shapes in the tree were invisible to it: a mis-decode whose
  bytes are not valid UTF-8 and so cannot be reversed, and an invisible soft
  hyphen. The gate now reverses a candidate run and accepts it only when the
  result is valid UTF-8 *and* differs, flags invisible format characters, and
  detects a bare cluster of high-Latin characters that is not part of a word —
  without flagging real accented words, `×`, `÷`, `·`, `§`, or intact emoji.
- **`AGENTS.md` claimed "Zero External Dependencies" as an architecture
  invariant.** The module requires `golang.org/x/crypto` (SSH) and
  `golang.org/x/sys` (Windows hardlink counting). The wording was worse than
  the facts: it invited a reader to treat a count as a security property, in a
  product whose job is holding OAuth tokens and private keys. The invariant is
  restated as *no C libraries, no runtime install* — which is true and
  verifiable — plus a pinned, enumerable dependency surface.
- **The threat model had no supply-chain entry**, despite the product holding
  an identity key and the build importing an entire SSH implementation.
  Added as T12, including the point that the dependency *count* is not the
  mitigation and what actually is.
- Documentation drift corrected: stale cockpit hotkeys and a "zero external
  dependencies" line in `ARCHITECTURE.md`; P2 sections in `UX-STATUS.md` and
  `UX-PLAN-MAP.md` still saying multi-file send was not started; a throughput
  figure in the README that predated the cross-machine measurement; and
  `AUDIT.md` citing an incident report that was removed because it contained
  credential-shaped data.
- **The SAS word list's comment claimed two guarantees it did not have.** It
  stated that the list excluded `i`, `l`, `o`, `0` and `1` — 305 of the 512
  words contain one of them — and that a confusable check existed, which no
  test performed. The comment now describes the list accurately, and a
  mechanical confusable check was added and calibrated against measurements
  of the real list: zero prefix pairs (where ~900,000 are expected by chance,
  so the property is deliberate and worth pinning) and 772 single-substitution
  pairs, which is ordinary for English and now bounded so the list cannot
  silently become impossible to compare by ear.
- **The CI staticcheck gate can now run at all.** The pinned version
  (2025.1.1) could not read the toolchain's export data and failed on every
  package with an internal error, so the step had never produced a meaningful
  result. It is pinned to a working version, and the real findings it surfaced
  are fixed: a test that compared a value with itself and could never fail,
  and five dead helpers left behind by earlier refactors (including two
  superseded origin validators in the security middleware).
- A receiver rejection that happened after the whole payload arrived reported
  no byte count, so the sender could not say how far the refused attempt got.
- The dashboard send preview renders again. The chosen file is staged as a
  page-created object URL, which the Content Security Policy did not
  authorize, so the browser blocked the image, the confirmation card showed a
  permanently broken thumbnail, and every paste, drop, and file selection
  logged a CSP violation. Blob URLs are same-origin and scoped to the page,
  so no other guarantee changed.
- Sending a text snippet now reports what happened on the tab you clicked
  from. A successful send and an over-limit rejection were previously visible
  only in the Live Logs tab, so both looked like a dead button; failures keep
  the draft, and "verified" appears only when the receiver confirmed it.
- An incoming pairing request is no longer rebuilt on every status poll, which
  threw keyboard focus onto the page body every three seconds and made the
  Approve button - the one action that grants permanent trust - impossible to
  reach with a keyboard or screen reader. The banner is now announced, and its
  buttons are theme-aware and full-size.
- The dashboard has a real heading outline (one page heading, a heading per
  card), a skip link, and a navigation landmark that the tablist previously
  replaced. The tab bar is a single tab stop with arrow-key navigation, the
  scrolling log console is reachable and named by keyboard, and closing the
  pairing dialog returns focus to the control that opened it.
- Upload progress is no longer announced continuously: the progress bar already
  exposes its value, so the mirroring live region is gone and the byte and
  remaining-time detail is available on demand instead of roughly 1.3 times a
  second for the whole transfer.
- Legibility in both OS themes: the always-on next-action guidance, the
  downloads path, the SAS value, the active filter pill, and the success chip
  now meet the 4.5:1 contrast minimum, and the version tag no longer inherits
  a light-theme colour onto the dark header.
- A completed OAuth relay is announced to screen readers (the success text was
  written into a region that had just been hidden), the confirmation popup's
  first focused control now shows a focus ring, and its Cancel button says so
  when a window cannot close itself. The legacy relay error page is no longer a
  dead end: it explains the reason, states that nothing was relayed, and links
  back to the dashboard.
- The downloads path is no longer clipped at narrow widths, and a focused
  control is no longer parked underneath the sticky header and tab bar.
- Multi-peer dashboard layout. The Default and Active badges on a peer row
  carried the log console's fixed dark palette, which measured 1.67:1 as text
  on a light card, and warning chips measured 3.9:1; both now follow the theme,
  while the console keeps its own palette. The header peer selector no longer
  stretches across the header or widens the page when a peer has a long name,
  and the two-column layouts no longer force horizontal scrolling on a narrow
  window. A single peer, a long peer name, and a one-character fingerprint are
  all now covered by the browser acceptance run.
- The dashboard now says when it is showing stale data. When the Hub stops
  answering, a banner states that everything below is the last known state and
  when it was last confirmed, the destination is labelled "last known" rather
  than hidden, and a Retry action recovers without waiting for the next poll. A
  Hub restart now also raises the session banner, so a mid-session restart no
  longer leaves an unexplained dashboard.
- Concurrent QuickDrop attempts with a reused DropID can no longer make one
  attempt clean up or cancel another attempt's receiver state.
- Transfer failures after the final byte no longer read as ordinary errors:
  lost confirmations report possible duplicates with an inbox check instead
  of a blind retry.
- Private staging operations now use verified directory handles, a
  cross-process maintenance lock, and owner-checked activity-marker release;
  crash-orphaned markers and manifest temporaries are swept and budgeted.
- File finalization copies from the verified open descriptor, Windows resume
  checks the open handle's hard-link count, invalid manifest sidecars cannot
  authorize cleanup, and Hub failure cleanup uses the transfer's captured
  output directory.
- Runtime metadata transactions now serialize concurrent in-process writers
  and tolerate Windows lock-directory contention without transient
  access-denied failures.
- Live LAN trust callbacks are now authoritative; start-time fingerprint
  snapshots can no longer keep an unpaired peer authorized in long-lived
  listeners.
- Pairing entry points now use the cross-process atomic identity load/create
  transaction.
- Failed partials are bounded by a retained-partial disk budget, private
  numbered partials are swept/budgeted, and only manifest-marked direct legacy
  `.part` files are cleaned; unmarked user files are preserved for manual
  review. Hub manifest-write failures clean up newly created staging files.
- SSE event IDs are serialized with ring insertion, and reconnect DROP events
  use an in-flight guard to avoid request/memory amplification.
- Standalone and Hub partial files now use the private `.tantu-staging` area,
  so stale-part cleanup and manifest cleanup share one recovery path.
- `open` LAN OAuth now honors live peer revocation just like QuickDrop and
  relay commands.
- B-side now forwards only allowlisted OAuth callback headers (Accept,
  Accept-Language, Content-Type, X-Requested-With) with deterministic
  duplicate resolution; relay header volume capped; B-side loopback checks
  symmetric with A-side (full 127/8).
- Peer selection prompts (pairing, cockpit) require plain-digit indices —
  trailing junk can no longer misselect.
- Text drops no longer fail when the receiver's stdout is redirected:
  the stdout offset was mistaken for resume bytes.
- Unpairing revokes immediately on all long-lived listeners (Hub, `serve`,
  `node`, `receive`) — removed peers previously stayed trusted until
  restart on standalone listeners.
- Hub validates its output directory at startup and on config change
  instead of failing every transfer later.
- Hub staging hardened against symlink/hardlink planting (exclusive creation,
  identity + link-count verification, staging-dir checks); stale partials
  older than 24 h are swept at startup.
- Legacy pairing prompts bounded by context (all four flows); SAS
  ambiguities rejected; peer certificate/address validation on store writes.
- Hub validates its output directory at startup instead of failing every
  transfer later; `Stop` can no longer wedge on "already running".
- Discovery beacons require well-formed identities; SAS self-filter no
  longer hides colliding peers; rate-limiter map hard-bounded.
- Concurrent multipart uploads (4), text uploads (16), and outbound pairing
  attempts (4) capped with 503 + Retry-After.

### Security notes
- Resume identity is metadata plus a 64 KiB head hash, not a full-content or
  sender-fingerprint proof; built-in one-shot sends use a new DropID per
  attempt. Completion is at-least-once if the final acknowledgement is lost.
- No wire-protocol break: all changes are compatible between peers running
  this revision. Mixed-version operation remains unsupported — upgrade both
  machines (`docs/RELEASE.md`).
- `go test -race` evidence is produced by CI (Linux/macOS); local Windows
  runs use `-count=2` lifecycle reruns instead (no C toolchain here).

## Prior development

Before this changelog existed, the history below was the record (newest
first). Each entry names the area hardened; details are in `docs/DEV-RECORD.md`
and `docs/AUDIT.md`:

- Hub control-plane auth (loopback enforcement, capability/session model,
  one-use relay tickets), lifecycle and restart races.
- Standalone services and Hub delegation hardening.
- QuickDrop bounded publication (no overwrite, atomic staging).
- mTLS/SSH/discovery lifecycle hardening.
- Pairing TLS-identity binding, dynamic-port preservation.
- OAuth retry coalescing, callback redaction.
- Protocol envelope validation, control-frame budgets.
