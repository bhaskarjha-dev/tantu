# Tantu Changelog

User-facing summary of what changed and why. The authoritative engineering
record (issue ledger, evidence, rejected hypotheses) lives in
`docs/DEV-RECORD.md`. See `docs/RELEASE.md` for install, upgrade, and
rollback procedures.

## Unreleased

### Added
- Received-file serving on the Hub dashboard: inline image previews (8 MB
  raster-only, sandboxed) and per-file downloads for QuickDrop inbox items.
- Dashboard stale-session banner: an explicit reconnect notice instead of
  silent 401 failures when opened without a session (stale bookmark/second
  tab).
- SSH host-key verification UX: `--ssh-known-hosts` and `--ssh-fingerprint`
  (`SHA256:` pin) on `send`, `open`, `drop`, and `relay`.
- Dev-build version provenance: plain `go build` binaries report
  `1.0.0-dev+<sha>[.dirty]` instead of a bare `1.0.0`.
- Clipboard image paste-to-upload on the dashboard; real XHR upload progress
  with cancel; GB/TB sizes; ARIA live regions; keyboard-operable drop zone;
  labelled inputs; inline SVG favicon.

### Changed
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
- B-side now forwards only allowlisted OAuth callback headers (Accept,
  Accept-Language, Content-Type, X-Requested-With) with deterministic
  duplicate resolution; relay header volume capped.
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
