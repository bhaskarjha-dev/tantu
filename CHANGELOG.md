# Tantu Changelog

User-facing summary of what changed and why. The authoritative engineering
record (issue ledger, evidence, rejected hypotheses) lives in
`docs/DEV-RECORD.md`. See `docs/RELEASE.md` for install, upgrade, and
rollback procedures.

## Unreleased

### Added
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
- OAuth relay attempts recorded with target peer, origin host only, state,
  and next action (`GET /api/relay/recent`, dashboard card, support bundle;
  durable last 50 / 30 days; never URLs, codes, or tokens).
- Wire versioning phase 1: envelopes carry `v: 1` and drop metadata carries
  a validated `idempotency_key` (Hub: operation ID; CLI: DropID); receivers
  validate and otherwise ignore both, preserving legacy interop. Tombstone
  lookup remains future work per `docs/SPEC-WIRE-VERSIONING.md`.
- Changing the dashboard downloads directory now states that previously
  received files stay in their former location.

### Changed
- The Hub dashboard now receives a per-response CSP nonce and uses delegated
  DOM events instead of inline JavaScript handlers; stale unauthenticated tabs
  show recovery guidance without firing a burst of 401 probes.
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
