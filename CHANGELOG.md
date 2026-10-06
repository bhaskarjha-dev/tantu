# Tantu Changelog

User-facing summary of what changed and why. The authoritative engineering
record (issue ledger, evidence, rejected hypotheses) lives in
`docs/DEV-RECORD.md`. See `docs/RELEASE.md` for install, upgrade, and
rollback procedures.

## Unreleased

### Added
- **Release artifacts are now signed, keyless.** Every archive, every SBOM and
  `checksums.txt` gets a detached cosign signature and the Fulcio certificate
  that vouches for the signing identity, so a downloader can prove an artifact
  came from this repository's release workflow without trusting the file they
  just fetched. Keyless is deliberate: a signing key must never live in this
  repository. `docs/RELEASE.md` has the verification recipe, and it pins the
  expected workflow identity and OIDC issuer — a checksum alone only proves a
  file matches `checksums.txt`, not that `checksums.txt` is ours. cosign is
  pinned to **2.6.1**: cosign 3.x deprecates the two output flags GoReleaser's
  signing integration uses and writes a single bundle instead, which
  `goreleaser check` does not catch. That was found by running cosign 3.0.4,
  not by reading the config.

- **A sent directory arrives as a directory.** `tantu send ./project/` used to
  fold every path into the filename, so a project landed in one flat folder as
  `project-docs-api-readme.md` and two files that differed only by directory
  became indistinguishable. Each file now carries its path inside the tree and
  the receiver rebuilds it under the sent directory's name. Per-file progress
  and failure lines show the path rather than the bare leaf, because a list of
  leaf names cannot tell two `notes.txt` files apart. Sending the same
  directory again produces a second tree (`README (1).md`) rather than merging.
  `tantu send <dir> --flatten` restores the previous behaviour exactly.
- **The dashboard reports discovery's health.** An empty "Nearby" list used to
  mean either "no second machine on this network" or "discovery is blocked
  here", and nothing on screen distinguished them — every failure signal inside
  the engine had been discarded. The Peers tab now shows what discovery is
  actually doing and, when it is not doing it, why: multicast or subnet
  broadcast (or both), how many hubs are in reach, and the last socket error.
  The Hub logs discovery *transitions* — beacons not leaving this machine, and
  (once the watcher is running, see Fixed below) degraded and recovered — so Live
  Logs has the reason without a wall of repetition. A blocked multicast socket on
  a network where broadcast works is reported as "broadcast only", not as a fault:
  a warning that is always there is a warning nobody reads. Discovery is still
  IPv4-only, and now says so.
- **A real notification surface, and empty states that act.** The dashboard no
  longer calls `alert()`, `confirm()` or `prompt()` anywhere. Every outcome it
  cannot put next to its own control arrives as a themed, non-modal
  notification announced through a live region, carrying the same single "Next:"
  step the CLI uses; failures persist until dismissed and pause while you are
  reading or operating them, and they never take focus. Every confirmation and
  prompt is one real dialog: Escape cancels, Tab stays inside, and focus
  returns to the control you opened it from. All four empty states — received
  items, authorizations, transfers, peers — now offer an action instead of
  describing a dead end.
- **A gate for documented claims.** `tools/docgate` checks the class of
  documentation claim that is mechanically decidable, and refuses to report
  success if its own inputs are missing or unparseable: the CLI surface in both
  directions (a documented command that no longer exists, and a command that
  exists and is not documented), the flags the docs tell a user to type, eight
  numeric limits against the constants that enforce them, the ports, the config
  directory, and the environment variables. It does not decide whether a
  sentence is true — that stays with a reviewer. It found four real drifts the
  moment it ran honestly: `tantu version` missing from the CLI reference table,
  the 10 MiB text ceiling stated nowhere in the architecture doc, `tantu send
  --text` documented nowhere, and the `X-Tantu-IPC-Token` header named nowhere.
  It is trusted only because it is itself tested against deliberately broken
  input, which immediately found two real bugs in the gate and one design flaw
  that made a check impossible to test.
- `docs/DEPENDENCIES.md` — the complete dependency inventory (two pinned
  `golang.org/x` modules), the reasoning behind the `CGO_ENABLED=0`
  static-build invariant, the demonstrated cost of that constraint, and four
  conditions any new module must satisfy.
- `docs/DECISIONS.md` — the v6.0 decision record (D-01 … D-30), extracted so
  that decision IDs cited by three tracked documents resolve for anyone
  cloning the repository. The original plan lives under gitignored `temp/`.

### Changed
- **`tantu pair` no longer reuses the port your Hub already owns, or hangs
  silently when it does.** The collision guard compared the requested port against
  the literal default `9877` rather than against the port the running Hub
  actually holds, so a Hub on a configured `p2p_port` — or a second Hub, which is
  exactly what the loopback transport is for — got no adjustment. Observed with a
  Hub on `19702`: the responder announced "Waiting for peer connection on
  0.0.0.0:19702..." and then printed nothing at all, while the other machine's
  connection reached the Hub's p2p listener and came back as `wsarecv: An existing
  connection was forcibly closed by the remote host`. It now moves to the next port
  and says so. `/api/probe` reports `p2p_addr` so the CLI can know which port is
  taken; the endpoint still omits identity, peer and content data.
- **The browser acceptance harness now tells you when a red run means nothing.**
  When the Hub it tests dies mid-run — and it could, because it was built as
  `tantu.exe`, so restarting your own Hub with `Stop-Process -Name tantu` killed
  it — the run reported four scattered check failures and a summary reading
  "47/51 checks passed". That is indistinguishable from a regression, and it was
  misdiagnosed twice while chasing it. The harness binary is now `tantu-uxtest`,
  so that command cannot reach it, and Hub death is reported as such: every
  later failure is tagged and the harness exits `4`, which CI surfaces as "this
  run proves nothing either way" rather than a product failure. Codes are
  documented in `tools/uxtest/README.md`.
- **A busy port no longer costs you discovery on both paths.** Discovery binds
  two independent transports — subnet broadcast on `9879` and mDNS on `5353` —
  and a failure in one used to disable the other, because the broadcast bind
  happened first and aborted startup outright. Anything holding `9879` silenced
  mDNS too, even though it was free. Every bind is now optional: the engine
  binds what it can, says why the rest failed, and keeps going, so a hub with a
  taken broadcast port still finds peers over mDNS. Beacons are also only sent
  over transports that are actually open, since a UDP write to a port nobody is
  listening on reports success while delivering nothing — which made a hub
  look healthy while being invisible. This also unblocks the dashboard's
  "discovery cannot reach this network" warning and the Hub's degraded/recovered
  log lines, which described a state the product could not actually reach.
- **The dashboard no longer shows a broken nav bar, two padlocks, filler copy, or
  duplicate buttons.** All four were visible in normal use while every automated
  check passed. The section tabs wrapped every label onto a second line — five
  tabs occupied the space of ten, because a flex item shrinks below its content
  width by default and the tab bar already had the horizontal scroll that was
  meant to handle narrow widths. The browser tab showed two padlocks, since the
  favicon is a lock and the page title was also a lock. The next-action banner
  always showed a sentence that restated the page ("Choose text, an image, or a
  file above…"); it now appears only when it has something to steer, which is the
  answer to whether that copy was useful: it was not, so it is gone rather than
  moved behind an icon. Received Items, Recent Authorizations and Transfers each
  had a "Refresh" in their empty state *and* in their own card header above it;
  each empty state now offers exactly one real next action.
- **The Hub now actually watches discovery health.** The watcher that reports
  discovery transitions existed, was documented as running, and was called by
  nothing — staticcheck found it on CI's first run after the discovery-health
  work was pushed. It is now started for the life of the Hub, sharing the
  discovery engine's context. It also reported the wrong moment even once
  running: it only announced discovery that was *already* broken before its first
  ten-second sample, so a Hub that started healthy and later lost discovery said
  nothing — the one case the feature exists for. It now reports the transition
  into the fault whenever it happens, stays quiet while a fault persists, and
  announces recovery. `KNOWN-LIMITATIONS.md` 3.17 records what still cannot fire:
  a failed broadcast bind aborts engine startup rather than degrading it, so the
  "cannot reach this network" state remains unreachable in production and is
  recorded as an open gap rather than presented as working.
- Directory sends are larger on the wire: each file's `drop_send` carries an
  optional `rel_path`. It is absent for every single-file send and for text, so
  ordinary transfers are byte-identical to before, and a peer that does not
  understand the field still publishes a directory send flat.
- `tantu send --flatten` is new, and is the opt-out rather than the default:
  "send this folder" means the folder.
- The browser acceptance harness now **fails a run** if the browser reports any
  native dialog. It previously stubbed `window.alert/prompt/confirm` during its
  action sweep, which is why the alert sites above survived several audits.
- The acceptance harness's preview-thumbnail check now waits for the fade to
  settle instead of sampling once at a fixed moment. The transition is 250 ms,
  so a single sample could not legitimately catch it mid-fade — but a cold
  browser's blob decode finished late twice, and the race cost two runs
  (Batch Z31 and this one) for a check that was measuring the wrong instant.
- `docs/DECISIONS.md` — the v6.0 decision record (D-01 … D-30), extracted so
- **The plan's user-perceived performance budgets are enforced in CI.** All
  §12.1 rows that a peerless test rig can honestly measure now have gates: 7
  Go gates (dashboard document serve, action acknowledgement with a real disk
  write, peer-state refresh, SSE delivery, history byte bound, `tantu status`,
  `tantu doctor` local work — every number the plan's, asserted on p95 with
  14–440× headroom) and 4 browser gates (first useful render under 1 s,
  paste/drop preview feedback under 100 ms, no UI freeze over 100 ms,
  reconnect reconciliation under 2 s). Upload-progress cadence and cancel
  acknowledgement stay ungated — they need a live peer (`docs/KNOWN-LIMITATIONS.md`
  2.5).
- **The pairing conversation is now visible on the machine that starts it.**
  The Pair dialog shows the session code to compare and this machine's own
  Approve/Reject for the whole handshake, with a live "confirmed here,
  waiting for the other device" state — previously the code and the approval
  row lived only in the banner *behind* the dialog's overlay, so the initiator
  watched "Connecting..." for 60 seconds with nothing to compare and no button
  to press, and every attempt timed out. The pending banner also says which
  direction a request came from ("You started this pairing…" vs "Compare this
  code with the code shown on the remote screen"), and the confirm dialog
  names the address for outgoing requests instead of "Remote Device".
- **Unpair now tells the other machine.** Removing a paired peer (dashboard
  Unpair or `tantu unpair`) sends a best-effort, certificate-authenticated
  notice over the encrypted channel, so the removed side clears it from its
  paired list within milliseconds instead of staying paired forever. If the
  notice cannot be delivered (peer offline), the removal still stands and the
  peer's next send is refused with an honest "the receiver no longer trusts
  this machine" explanation instead of a generic error. The unpair
  confirmation says exactly this before you commit.
- **A gate for documented claims.** `tools/docgate` checks the class of
  documentation claim that is mechanically decidable, and refuses to report
  success if its own inputs are missing or unparseable: the CLI surface in both
  directions (a documented command that no longer exists, and a command that
  exists and is not documented), the flags the docs tell a user to type, eight
  numeric limits against the constants that enforce them, the ports, the config
  directory, and the environment variables. It does not decide whether a
  sentence is true — that stays with a reviewer. It found four real drifts the
  moment it ran honestly: `tantu version` missing from the CLI reference table,
  the 10 MiB text ceiling stated nowhere in the architecture doc, `tantu send
  --text` documented nowhere, and the `X-Tantu-IPC-Token` header named nowhere.
  It is trusted only because it is itself tested against deliberately broken
  input, which immediately found two real bugs in the gate and one design flaw
  that made a check impossible to test.
- `docs/DEPENDENCIES.md` — the complete dependency inventory (two pinned
  `golang.org/x` modules), the reasoning behind the `CGO_ENABLED=0`
  static-build invariant, the demonstrated cost of that constraint, and four
  conditions any new module must satisfy.
- **Multi-file sends.** `tantu send <directory>` sends every file under a
  directory as an independent transfer, following the recorded decision D-13
  ("multi-select becomes multiple logical transfers"). It is not archived: each
  file gets its own operation ID, its own idempotency key, and its own outcome,
  so 40 of 50 arriving is reported as 40 of 50 rather than as one failure
  nobody can reason about. Symlinks are refused rather than
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
- **A returning Hub is noticed in about a second, not up to three.** While the
  dashboard can't reach the Hub it polls every second instead of every three,
  so the reconnecting machine's state reconciles inside the plan's 2-second
  budget (§12.1); the moment the Hub answers again the cadence relaxes back to
  the quiet 3-second poll. The gate measures recovery with **no manual retry
  click** — releasing the block, the staleness banner must clear on its own
  (measured 527–670 ms across chromium, firefox and webkit).
- **The dashboard acceptance harness runs on Playwright across three engines.**
  `tools/uxtest` now drives chromium, firefox and webkit (Playwright 1.63.0,
  pinned, dev-only) instead of raw CDP against a system Chrome, and CI's
  `dashboard-acceptance` sweeps all three in one gate — each engine annotated
  separately, every engine run even when an earlier one fails. The third
  engine earned its seat immediately: webkit revealed that the contrast sweep
  was measuring elements in *inactive tabs* — which generate no boxes and
  therefore have no pixels to contrast — and that WebKit leaves computed
  colors inside such hidden subtrees stale across a `prefers-color-scheme`
  flip (resolving them when the subtree renders), so the check could report a
  failure for a value no user can ever see. The sweep now measures only
  rendered elements, in both themes, and two new coverage checks keep that
  honest: both themes must measure the same rendered set, and the count has a
  floor, so a sweep that silently measures nothing fails instead of passing.
  Red-proven after the change: an injected light-theme contrast defect and the
  historical `this`-delegation bug both fail the run by name (32/36).
- **The dashboard source is a real HTML file now.** The Hub's two pages —
  `dashboard.html` (3,355 lines) and `relay_interstitial.html` (172 lines) —
  moved out of Go raw strings in `web_dashboard.go` (5,265 → 1,750 lines)
  and are compiled into the binary with `//go:embed`, so the static-binary
  invariant is unchanged. The benefit is everything a raw string denies:
  syntax highlighting, linting, per-hunk diffs and review of 3,500 lines of
  HTML/CSS/JS that no tool could previously see, and JavaScript template
  literals become usable (a backtick is the one character a Go raw string
  can never contain — the constraint is named in DEV-RECORD Z15's notes).
  Serving is provably byte-identical: both embedded digests match the
  pre-extraction literals (`d395e796…`, `2b96aa75…`), logged by
  `TestEmbeddedHTMLMatchesSourceFile`, which was red-proven both ways — a
  mis-pointed directive fails the divergence check with both byte counts,
  and a corrupted file fails the completeness check. The acceptance harness
  still passes 32/32 against the extracted source.
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
- **Two Hubs in one browser no longer log each other out.** Dashboard session
  cookies were named identically regardless of port, and browsers key cookies
  by name/domain/path but never by port — so a second Hub's login REPLACED the
  first Hub's session value, and that dashboard then failed every poll with
  "Dashboard session not established" while its Hub was perfectly healthy (a
  permanent dead state, found by driving a live two-Hub pairing run). Session
  cookies are now scoped to their Hub's web port; a gate models the shared
  browser jar across two Hubs and requires both to keep working after both
  logins.
- **Rejection messages now say who rejected.** The dashboard used to answer
  every failed pairing with "Pairing was rejected by the remote device" —
  even when *this* machine had not answered yet, had rejected it locally, or
  the handshake had simply timed out after 60 seconds. Outcomes are now
  distinguished: "You rejected this pairing", "The other device rejected the
  pairing, or stopped responding", and a timeout that says so, with
  unreachable-Hub and handshake-timeout errors mapped to plain language
  instead of raw `context deadline exceeded` text.
- **The identity SAS is no longer labeled as the pairing code.** The Pair
  dialog showed this machine's identity fingerprint SAS under the heading
  "Local SAS Verification Code" — inviting a comparison against the other
  screen's session words that can never match. It is now labeled "This
  machine's identity SAS — shown on other devices after pairing; NOT the
  pairing code".
- **Sending to a machine that unpaired you explains itself.** The transfer
  classifier had no branch for the refusal reason the pairing layer actually
  produces (`unauthorized: peer certificate not paired or trusted`), so the
  user got a generic "receiver rejected" with no cause and no next step. It
  now reports that the receiver no longer trusts this machine and that
  pairing again is the fix. (`TestClassifyTransferError_UntrustedPeerExplainsUnpair`,
  red against the previous classifier.)
- **The relay-listing integration test no longer races a phase the product
  deliberately exposes.** `TestWebDashboard_RelayActiveListsInFlightRelay`
  asserted an operation id on the *first* entry its poll saw, but an
  in-flight relay is listed from the moment its flow begins — in a
  `starting` phase with no attempt id yet (still cancellable by flow key) —
  and the id is published only after the coordinator's first hub lock
  acquisition. CI run #35 (macOS arm64) landed a GET inside exactly that
  window and failed a correct product. The test now polls past `starting`
  to the id-bearing phase, leak-checks every observation in either phase,
  keeps the pre-existing skip for environments where no relay reaches the
  wire, and asserts the `relaying` state; the id-bearing snapshot itself
  stays pinned by the coordinator unit test.
- **A symlinked output directory was accepted at configuration time and then
  failed every transfer at the final publish step.** Validation used
  `os.Stat`, which follows links, while publication and the maintenance
  sweeps inspect the directory with `os.Lstat` and reject a link — so a Hub
  configured with a linked output directory accepted the whole transfer,
  streamed the payload, and failed at the last step with a message about a
  "staging path", while stale-partial reclamation silently no-opped with no
  recorded error. Startup and `POST /api/config` now refuse a symlinked or
  junctioned output directory up front, with a message naming the problem,
  so the failure happens where it can still be acted on.
  (`TestValidateOutputDirRejectsSymlinkedDirectory`, red against the
  previous code.)
- **`TestHub_DropResumption` no longer races the RecentDrops buffer against
  file publication.** The test waited for the delivered file to reach full
  length and then sampled the buffer once, but the buffer is appended by the
  `OnDropReceived` callback, which runs *after* publication, the completion
  tombstone, and the wire complete — so a full file on disk can precede the
  buffer entry by milliseconds. That single read landed in the window on CI
  (run #32: `expected RecentDrops to contain item, got 0`) and reproduced
  locally within 200 runs, making the test a coin-flip gate rather than a
  check. It now polls the buffer with a bounded deadline, the same pattern
  the neighboring receive tests already use. Product ordering is unchanged
  and intentional: a drop is durable and acknowledged before it becomes a
  dashboard "recent drop".
- **Three discovery tests asserted that the machine was silent, and CI run
  #37 failed a correct product for hearing a neighbour.** Every discovery
  engine joins the same mDNS group by design, and CI runs package test
  binaries in parallel, so a concurrent test's beacons arrive on the shared
  listeners and are recorded — correctly. `TestEngine_SelfFiltering`,
  `TestEngine_MalformedPacket` and `TestEngine_SelfFilterEndToEndUDP`
  required the node table to be *empty* (or exactly one entry) and so failed
  on any foreign beacon, with `runnervm8df0l` — the runner's own hostname,
  advertised by a `cmd/tantu` lan-hub test — reported as "found self". They
  now assert the actual contract: our own name is never recorded (with a
  replayed own-nonce beacon at the listener, so the check cannot pass
  because nothing arrived), malformed packets produce no *invalid
  coordinates* (out-of-range port, empty or control-bearing name), and a
  foreign probe beacon must still be recorded, so a deaf or over-filtering
  engine cannot pass. Red-proven under local packet injection in both
  directions: the old assertions fail on foreign nodes, the new ones fail
  when `isSelfBeacon` or `validBeacon` is disabled. No product code
  changed.
- **The SSH listener wedged instead of failing when its accept loop died.**
  A listener-level `Accept` error was published once into the results queue
  and `serve()` returned: the first caller saw the error, every later
  `Accept` blocked forever on an empty queue, and the bound TCP socket was
  never released — the port stayed held with nothing accepting on it, while
  the LAN listener (the reference implementation) records a terminal error,
  closes the socket, and returns that error from every subsequent call.
  `Close()` also never drained the queue, so a connection accepted
  concurrently with shutdown outlived the listener. The SSH listener now
  mirrors the LAN contract (`terminalErr` + `serveDone` + wait-then-drain in
  `Close`), with `TestSSHListener_FatalAcceptErrorIsRepeated` and
  `TestSSHListener_CloseDrainsQueuedConnections` red against the previous
  code (blocked second Accept; leaked queue entry). The LAN gate's own read
  assertion was strengthened in the same commit: it counted a deadline
  timeout as a closed pipe, so it passed even when `Close` leaked.
- **Setting a deadline on a closed SSH connection reported success.** All
  three `Set*Deadline` methods checked for closure via `c.channel == nil`,
  which `Close` never sets, so they returned nil and armed timers against a
  dead session — `net.Conn` returns an error there.
  `TestSSHConn_SetDeadlineAfterCloseIsRejected` was red against the old code
  on all three methods. The interface docs now state what was previously
  written nowhere: an SSH deadline expiry *closes* the connection (a blocked
  `ssh.Channel` read cannot be interrupted otherwise) while LAN/loopback fail
  the pending I/O and keep the connection, and fingerprint formats differ by
  transport (LAN's 64-hex SHA-256 vs SSH's `SHA256:<base64>`).
- **Paired-hub detection in discovery had been dead since the cleartext SAS
  was removed.** `discovery.DiscoveredNode` kept `SAS` and `Fingerprint`
  fields that `recordNode` forced to `""`, so every consumer correlating via
  `store.GetPeer(node.Fingerprint)` queried the empty string and always
  missed: the dashboard's Nearby list called every already-paired hub "not
  yet verified" and offered it for re-pairing, `tantu pair` listed
  already-paired hubs as candidates on both paths, the cockpit repeated the
  filter, the log printed `(SAS: )` with an empty code, `discSeen`
  throttled every node through one key, and the discovery-driven roaming
  probe was unreachable code. The identity fields are now *deleted* rather
  than kept empty — the compiler refuses any future consumer from claiming
  identity discovery cannot provide — and correlation is an address-host
  match (`pairing.PeerStore.HasPeerAtAddress`) documented as display-only,
  never authorization. The roaming probe is removed rather than revived:
  without identity there is no safe way to tell which stored peer a beacon
  from a *new* address belongs to, and the authenticated self-healing path
  (which pins the fingerprint before rewriting an address) was never dead.
  Gates: `TestPeerStoreHasPeerAtAddress`, `TestBuildDiscoveredPeers` (which
  also pins the response shape: no `sas`/`fingerprint` keys survive).
- **A legacy beacon whose `sas`/`fp` keys were present but *empty* was
  accepted** — exactly what pre-`d7de281` peers emitted, since they declared
  those fields without `omitempty` (verified against local git history).
  `validBeacon` now rejects on *presence* (`*string` fields), not on a
  non-empty value. The E2E test that claimed to catch legacy beacons was
  also restructured: it sent a valid beacon milliseconds before the legacy
  one, so the 50 ms per-source rate limiter dropped the legacy packet and
  the test passed with the validation deleted.
  `TestEngine_RejectsLegacyBeaconEndToEnd` now sends the legacy packet
  *first* — only validation can be what refuses it — and then requires a
  well-formed peer from the same source to still be discovered, so it cannot
  pass vacuously. Both it and `TestValidBeaconRejectsPresentButEmptyLegacyFields`
  were red before the fix.
- **One LAN host could evict every legitimate peer from discovery.** The
  256-slot table is keyed by `ip:port`, so a single source claiming rotating
  ports manufactured distinct keys and the oldest-first eviction removed
  everyone else — `TestRecordNodeBoundsPerSourceHost` observed 256/256 slots
  held by one host with the legitimate peer gone. New keys from a host
  already at the 8-slot per-source cap are now refused (existing entries
  keep refreshing).
- **`tantu pair`'s discovery scan failed silently.** `_ = eng.Start(...)`
  discarded the error, so a machine where the UDP socket could not bind
  scanned nothing for two seconds and printed "done." — indistinguishable
  from an empty LAN. The failure is now reported with an instruction to
  enter an address manually, and the listing no longer prints an empty
  `(SAS: )`.
- **Three stale claims corrected while fixing the above.**
  `ARCHITECTURE.md` §4.3 said LAN connections reject certificates not
  stored in `peers.json`, but the hub's wire listener completes the TLS
  handshake for unknown certificates by design (`AllowPairing: true`) and
  authorizes at the application layer — which THREAT-MODEL already
  documented. `AUDIT.md` described roaming cooldown/probing that no longer
  exists, and `transport.go` named roaming probes as `DialPinnedContext`'s
  caller. THREAT-MODEL T6 now records the SSH pre-auth handshake bound (64,
  excess rejected rather than queued) and the LAN listener's missing
  aggregate handshake cap.
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
- **The release pipeline would have failed on its own first tag push.**
  `release.yml` never installed `syft`, which GoReleaser's `sboms` step shells
  out to: a tag build would have compiled and archived all six targets and
  then exited 1 with `exec: "syft": executable file not found`. The config
  also used `archives.format`, which `goreleaser check` exits 2 on as
  deprecated. Both defects were found by *running* the pipeline instead of
  reading it — the pinned GoReleaser `v2.18.2` and a checksum-verified syft
  `1.52.0`: `goreleaser check` now exits 0 and
  `goreleaser release --snapshot --clean` completes, producing all 6 archives,
  6 SPDX-2.3 SBOMs and `checksums.txt` with every hash re-verified, the
  archives confirmed to hold the single binary plus README and LICENSE, and
  `tantu version` reporting the link-time-injected version. CI now also fails
  when `go mod tidy` would change `go.mod`/`go.sum`: the release's before-hook
  repairs a non-tidy tree *during* the build, which would ship code that
  differs from the tag.
- **`docs/RELEASE.md` carried three claims that had stopped being true.** It
  named Go `1.26.3` as the CI/release toolchain (both workflows pin
  `1.27.1`, a security decision), said `govulncheck` had never been observed
  running in CI (the quality job runs it green on every push), and said local
  Windows `-race` evidence was only `-count=2` reruns (it runs with a C
  toolchain as of 2026-09-30). A release document that misstates the
  toolchain is worse than a missing one, because it is what someone reads
  while preparing to ship.
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
- **Directory sends introduce the first peer-chosen location on disk.** A
  sender-supplied relative path is validated in three layers and every failure
  is a refusal rather than a rewrite: the wire layer rejects absolute paths,
  drive prefixes, control characters and empty / `.` / `..` segments *at
  acknowledgement*, before any payload is staged; the receiver then rejects
  Windows-illegal characters, trailing dots or spaces, reserved DOS device
  names, NTFS alternate streams and over-long or over-deep components; and
  parent directories are created through a root handle anchored on the output
  directory, with every component required to be a real directory rather than a
  symlink. Rewriting instead of refusing was rejected deliberately: turning
  `../../etc/passwd` into `etc-passwd` would publish a file the sender never
  agreed to, and on Windows a trailing dot is stripped rather than rejected, so
  a rewrite would merge two transfers the user meant to keep apart.
- Re-sending a directory now produces a second tree with `(1)` disambiguators
  rather than failing. The first implementation appended the disambiguator to
  the whole destination path, which turned the delivered file into its own
  parent directory and made **every** file in a re-sent directory fail; it was
  found by running the real binary, not by testing, and the gate that now
  covers it drives the Hub rather than the publication helper.
- Security & integrity audit (batch Z29): dashboard route/authorization,
  pairing identity binding, and QuickDrop resume integrity were each traced
  to the implementing line and now have enumerated, red-proven gates — every
  registered route outside the documented public allowlist must answer 401
  unauthenticated, a substituted pairing certificate or transport
  fingerprint must be refused, and planted staging bytes must never appear
  in a delivered file. No vulnerability was found in those chains; five
  comments that claimed properties the code did not implement were
  corrected (`docs/DEV-RECORD.md` Batch Z29 records what was checked,
  rejected, and why).
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
