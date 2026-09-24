# Tantu — Durable Development Record

Maintained by the autonomous engineering agent. Updated each cycle.
Location: `docs/DEV-RECORD.md` (committed; `temp/` is gitignored and unsuitable
for durable records).
Baseline established 2026-09-24 (commit 2be64da, branch main, tree clean, 9 commits ahead of origin/main).

## Toolchain note

No Go toolchain was installed in this environment. Portable Go 1.26.3 was
downloaded (dl.google.com, 74 MB zip, verified size) and extracted with unzip
to `C:\Users\ai2\go-dist\go` (PowerShell Expand-Archive stalled mid-extract and
produced a corrupt GOROOT; re-extract from scratch fixed it). All `go` commands
in this session must prefix `PATH=/c/Users/ai2/go-dist/go/bin:$PATH`.
`winget install GoLang.Go` was attempted first but stalled (left running in
background; harmless if it completes later — prefer the pinned 1.26.3 toolchain
matching `go.mod`).

## Baseline (2026-09-24, commit 2be64da)

- `go build ./...` — PASS
- `go vet ./...` — PASS
- `go test -count=1 ./...` — PASS (all 12 packages)
- `go test -race ./...` — not yet run (pending)

## Architecture understanding

Symmetric Hub: both machines run identical code. Dual sockets: Web UI/IPC on
loopback-only 127.0.0.1:9876 (fallback 9875..9873, enforced in NewHub +
securityMiddleware + IPC validators); encrypted wire on :9877 (mTLS LAN, SSH,
or loopback). Pluggable `transport.Transport` (LAN/mTLS with FP pinning, SSH
with known_hosts/key pinning, loopback plain-TCP). Wire frames are
`protocol.Envelope` (4-byte BE length + JSON, 4 MiB max, 64 KiB control cap,
sticky-terminal decoder). OAuth bridge: B-side sends BridgeRequest, A-side binds
exact 127.0.0.1 callback port, relays callback, retries coalesced via
OAuthSessionManager (2 min replay). QuickDrop: 2 MiB chunks, resume via
chunk-aligned ReceivedBytes + 64 KiB head-hash, SHA-256 full-file integrity,
5 GiB / 10 MiB text caps, no-overwrite atomic publish, `.part` staging.
Identity: ECDSA P-256 self-signed 10y cert, FP = hex(SHA-256 DER), 6-hex SAS,
peers.json + identity.json 0600 with atomic rename. Discovery: plaintext
`AUTHDISC:` beacons over broadcast + mDNS-port UDP, 3 s interval, 15 s TTL,
256-node LRU. Dashboard auth: one-time bootstrap fragment → HttpOnly
SameSite=Strict session cookie (30 min, 32 max) or IPC token header; strict
Host/Origin/Referer loopback+port checks; relay tickets one-use 10 min.
Standalone legacy commands (serve/relay/node/wrap) predate the Hub and do NOT
share its session/token model (reusable per-process tokens in HTML/bookmarklet).

## Issue ledger

| ID | Area | Severity | Status | Summary |
|----|------|----------|--------|---------|
| I-01 | bridge | P1 | FIXED | B-side forwarded unfiltered `relay.Headers` into localhost app request (bside.go:388) — header injection (Cookie/Authorization) by malicious peer |
| I-02 | hub/drop | P1 | FIXED | Hub staging open used O_TRUNC without O_EXCL on predictable peer-controlled path (hub.go:1225,1268) — local symlink TOCTOU truncation |
| I-03 | protocol | P2 | FIXED | Encoder enforced only 4 MiB, not 64 KiB control cap (codec.go:80) — sender learns failure only after receiver closes |
| I-04 | transport | P2 | FIXED | `DialPinned(Context)` silently fell back to unpinned Dial for transports without pinning (transport.go:138) |
| I-05 | pairing | P2 | FIXED | Legacy initiator used `context.Background()` with deadline cleared during SAS prompt (pair.go:277,355) — unbounded hang |
| I-06 | pairing/store | P2 | FIXED | `ResolvePeer` SAS tier returned first match, no ambiguity error (store.go:449) despite 24-bit SAS |
| I-07 | pairing/store | P2 | FIXED | `AddPeer` never validated fp↔cert binding, fp charset, or address shape (store.go:227) |
| I-08 | pairing/store | P2 | FIXED | Store dir/file modes never repaired on load (store.go:54) — pre-existing lax perms persist |
| I-09 | cli/unpair | P2 | FIXED | `--fingerprint` accepted 1-char prefix, single match deleted without confirm (unpair.go:118); `--name` case-sensitive, alias ignored |
| I-10 | cli/pair | P3 | FIXED | Peer selection `Sscanf("%d")` accepted trailing junk like "1abc" (pair.go:132) |
| I-11 | cli/send | P2 | FIXED | argv text path had no size cap while stdin capped at 10 MiB (send.go:138) |
| I-12 | cli/send | P2 | FIXED | `send lan` snapshotted TrustedFingerprints without live `IsTrusted` (send.go:277) — stale trust TOCTOU |
| I-13 | discovery | P2 | FIXED | Beacons accepted any FP/SAS charset/length only (discovery.go:293) — spoof/flood poisoning, no hex check |
| I-14 | cli/open | P3 | FIXED | stdin `bufio.Scanner` 64 KiB line cap vs 64 KiB URL limit (open.go:52) — near-limit URLs fail via stdin only |
| I-15 | bridge | P3 | FIXED | `isLoopbackHost` (aside.go:117) allowed only 3 hosts while hardening allowed full 127/8 — `127.0.0.2` inconsistency |
| I-16 | standalone | P2 | FIXED | `relay.go` reusable per-process token in `GET /relay?token=` URL + bookmarklet; now renders per-request one-time tickets (single-use, 10-min TTL, 128 cap) for bookmarklet + form (header-preferred), per-process token kept as legacy fallback for old renders |
| I-17 | standalone | P2 | SCOPED | `drop.go` reusable `TANTU_LOCAL_TOKEN` embedded in HTML/JS; retained with documented rationale (header-only, never in URLs; no-store page; Origin/Referer/Host enforced; same-user boundary) + SR16 scoped exception |
| I-18 | hub | P3 | FIXED | No concurrent-upload cap on `POST /api/drop/upload` — now capped at 4 with 503 + Retry-After before any body buffering |
| I-19 | bridge | P3 | OPEN | Unconstrained legacy flows accept any path/state (aside.go:108, bside.go:200) — compat behavior, document |
| I-20 | bridge | P3 | OPEN | `ExtractCallbackPort` silent 80/443 default (bside.go:112) contradicts "exact port, no fallback" docs |
| I-21 | protocol | P3 | OPEN | No `Type` allowlist / max type length on decode (codec.go) — mitigated by dispatcher filtering + size budget |
| I-22 | pairing | P3 | OPEN | SAS is 24-bit interactive-compare only, no commitment (cert.go:139) — accepted design, `--auto-pair` explicitly opt-in |
| I-23 | discovery | P3 | OPEN | Self-filter drops beacons matching own SAS with different FP (discovery.go:271) — 24-bit collision hides legit peer |
| I-24 | cli/send | P3 | OPEN | `send --transport=ssh` exposes no host-key pinning flags (send.go:250) — fails closed to ~/.ssh/known_hosts, no pinning UX |
| I-25 | docs | P3 | FIXED | README/THREAT-MODEL overgeneralized Hub guarantees to standalone commands — SR16 scoped exception, T3 LAN-scoped trust, T6 upload cap, ARCHITECTURE scope note, README Hub qualifier |
| I-26 | hub | P3 | FIXED | `openDirectoryInOS` dash-prefixed dir flag parsing — resolved to absolute path before exec |
| I-27 | ssh | P3 | OPEN | Private-PEM accepted as server authorized key (ssh.go:147) — conflates auth domains for CLI convenience |

## Round-2 findings (adversarial re-review + fresh discovery, 2026-09-24)

| ID | Area | Severity | Status | Summary |
|----|------|----------|--------|---------|
| C-01 | hub/transport | P2→P3 | HARDENED | Start-time trust snapshot + live check (OR) meant `unpair` relied on the dispatcher app gate alone. Investigated with a live revocation test: the dispatcher gate was always live, so no data path existed — kept live-only transport config as defense-in-depth + added `TestHub_UnpairRevokesDropAccess` end-to-end regression |
| C-02 | standalone relay | P2 | FIXED | Single shared ticket per render (form use burned the bookmarklet); tickets now minted separately per surface; URL-presence validated before ticket consumption so bad requests never burn tickets |
| C-03 | hub/drop | P1 | FIXED | Hardlink-planted partials pass SameFile resume checks → victim truncation. Resume now rejects multi-linked files (Unix link-count, build-tagged; documented no-op elsewhere); staging dir must Lstat as a real directory (Hub + standalone) |
| C-04 | bridge | P2 | FIXED | Duplicate-case header keys let validator and deliverer disagree; Content-Type/Host lookup now deterministic canonical (sorted, first-wins); B-side loopback checks widened to 127/8 symmetric with A-side; relay header map capped (64 entries, 16 KiB) |
| C-05 | pairing | P2 | FIXED | `ctx.Err()` checked only after the blocking SAS prompt — all four pairing prompts (legacy initiator/responder, in-band both) now use `confirmWithContext` (prompt in goroutine, select on ctx) |
| C-06 | pairing/store | P3 | FIXED | Address validation rejects URL/scheme/userinfo/whitespace hosts and non-numeric/out-of-range ports; `UpdatePeerAddress("")` clears symmetrically with `AddPeer` |
| C-07 | pairing/store | P2 | FIXED | Cross-tier collisions (SAS of A == fp-prefix of B) error instead of shadowing; empty query with >1 peers and no default errors instead of silently picking first |
| C-08 | hub | P3 | FIXED | Text uploads (16 slots) and outbound pair attempts (4 slots) capped with 503, same pattern as multipart uploads |
| C-09 | bridge | P2 | FIXED | CallbackRelay header map unbounded inside the 4 MiB frame exemption — capped at 64 headers / 16 KiB total in `validateCallbackRelay` |
| C-10 | transport | P3 | FIXED | Strict-DialPinned contract now pinned by unit test (fp on unpinned transport errors; empty fp dials) |
| F-01 | hub/bridge | P2 | FIXED | Post-success replay returned false success without delivery (Hub relayCoordinator 45s + bridge Replay ack). Coordinator replay removed (in-flight coalescing kept) + regression tests; B-side Replay now warn-logs "no callback delivered for this request" |
| F-02 | discovery | P3 | FIXED | `allowBeaconSource` limiter map unbounded under source-IP rotation — unconditional oldest-eviction past cap, mirroring `recordNode` |
| F-03 | hub/drop/cli | P2 | FIXED | Aborted transfers leave staging orphans forever (DropIDs random per attempt, never resumed). New `drop.SweepStalePartials` (24h default) wired into Hub start + `drop`/`node`/`receive` startup + unit test |
| F-04 | hub | P3 | FIXED | Same as C-08 (pair slots) |
| F-05 | cli | P3 | FIXED | Cockpit `promptSendText` uncapped ingest (now 10MB) + `RenderSnippetCard` terminal bomb (now 4KB truncation notice) |
| F-06 | cli | P3 | FIXED | `tantu drop` long-poll relied on lossy 32-slot wakeup channel — poll now scans queue for unserved items (delivered-set), eviction prunes the set |
| F-07 | cli | P3 | FIXED | `drop`/`relay` LAN transports lacked live `IsTrusted` (added, matching `send`/`node`/`receive`) |
| F-08 | hub | P3 | ACCEPTED | Roaming probes are paired-gated + mTLS-pinned + 30s/FP-throttled; residual LAN port-scan oracle via unreadable logs is negligible |
| F-09 | all parsers | — | DONE | Fuzz targets added for frame decode, beacon validation, drop metadata; 25s each, millions of execs, no crashes |

Severity: P0 critical/exploitable remotely · P1 exploitable by paired/local attacker or data-loss · P2 hardening/correctness with realistic trigger · P3 minor/compat/doc.

## Decisions and assumptions

- D-01: Portable Go 1.26.3 pinned to match go.mod (not latest 1.27) for reproducible builds.
- D-02: `DialPinned` strictness errors only when expectedFP != "" AND transport lacks pinning — all current callers pass "" for ssh/loopback (verified send/relay/open/drop/hub), so behavior-preserving + fail-closed for future misuse.
- D-03: unpair `--fingerprint` minimum 6 chars aligns with `ResolvePeer` Tier-1 prefix rule (store.go:432); `--name` widened to Alias + case-insensitive to match ResolvePeer tiers 3-4.
- D-04: B-side header filter mirrors A-side `callbackHeaders` allowlist (Accept, Accept-Language, Content-Type, X-Requested-With) — single source `allowedCallbackRelayHeader` in session.go; Content-Length/Host/Cookie/Authorization can never be injected.
- D-05: Standalone relay/drop token issues: relay migrated to per-request one-time tickets with legacy fallback (mirrors Hub); drop keeps its header-only per-process token with a documented SR16 exception (header tokens never enter URLs/history/logs; page is no-store; Origin/Referer/Host still enforced).
- D-06: No commits pushed (per mandate: never push without explicit request). Local commits only.
- D-07: Adversarial finding "stale trust survives unpair" (C-01) investigated with a live two-connection test: rejected as a vulnerability — the dispatcher application gate was always live (`IsPeerTrusted` reads the store per connection), so no data path existed. Live-only transport config kept as defense-in-depth cleanup.
- D-08: Post-success replay removed (not "fixed to redeliver"): request keys normalize away per-attempt OAuth values, so any replay of a past success is semantically false. Duplicates redo the flow (correct); in-flight coalescing (the actual browser-storm fix) is retained everywhere.
- D-09: `temp/` is gitignored — the durable record lives at `docs/DEV-RECORD.md` (committed) instead.

## Validation matrix

- [x] go build ./... (baseline + after each batch)
- [x] go vet ./... (baseline + each batch)
- [x] go test -count=1 ./... (baseline + batches A–D, all pass)
- [x] go test -count=2 ./internal/hub/ ./internal/bridge/ (lifecycle/concurrency rerun, pass)
- [ ] go test -race ./... — BLOCKED: no C compiler in environment
  (`-race requires cgo`), no gcc/cc/clang. Mitigation: -count=2 rerun of
  concurrency-heavy packages (done, pass). Never claim race safety.
- [x] cross-compile GOOS=linux/amd64, darwin/arm64 (pass; windows vet pass)
- [x] new regression tests per fix (batches A–D; see ledger)
- [x] fuzz: FuzzDecode, FuzzValidBeacon, FuzzValidateDropMetadata — 25s each,
  millions of execs, no crashes
- [x] release artifact smoke test: binary builds, version ok, headless Hub
  start/healthz/401-without-cap/IPC-authed status+recent/multipart+text
  uploads end-to-end (loopback self-delivery)/relay-403-without-ticket/
  dashboard-200/stale-hub.json takeover by next Hub. Stop()/restart covered
  by unit tests; smoke temp files removed.

## Open questions

- Q-01: Should standalone `serve`/`relay`/`node` commands be retired in favor of Hub delegation, or migrated to one-time tickets? (I-16, I-17)
- Q-02: winget Go 1.27 install may have completed in background — verify it didn't mutate system PATH unexpectedly.

## Next actions

1. [x] Implement Batch A fixes (I-01..I-15) with regression tests.
2. [x] Run build/vet/full tests (all green; race blocked, see above).
3. [x] Triage Batch B (I-16..I-18, I-25, I-26): standalone token model, upload cap, docs.
4. [x] Cross-platform compile checks (linux/amd64, darwin/arm64 OK).
5. [x] Commit Batch A locally (1361d60, no push).
6. [x] Batch B committed (9efd35a, no push).
7. [x] Adversarial re-review + fresh discovery → Batches C/D implemented.
8. [x] Fuzzing (3 targets, clean) + release smoke test.
9. Commit Batches C/D locally (no push); final report.
10. [x] Batch E (42799e7): Hub.Err startup-failure signal, Stop force-close
    (no running wedge), OAuth in-flight cap 128, discovery transient-error
    backoff, cockpit ctx+timeout plumbing, Hub.Timeout() — full suite green.
11. [x] Batch F (eb1ea1b): dashboard XHR real upload progress + cancel,
    GB/TB formatBytes, focus-preserving peer render, ARIA live regions —
    JS node --check clean, full suite green.

## Batch E/F validation (2026-09-24, commits 42799e7, eb1ea1b)

- `go build ./...` PASS, `go vet ./...` PASS,
  `go test -count=1 ./...` PASS (all 12 packages).
- New tests: TestHub_ErrReportsStartupFailure, TestHub_ErrNilOnSuccess,
  TestHub_TimeoutDefaultAndConfigured, TestOAuthSessionManager_ActiveSessionCap,
  TestWebDashboard_UploadProgressAndPeerRenderMarkers.
- `go test -race ./...` still blocked (no C compiler); `-count=1` full suite
  is the standing mitigation. Race safety never claimed.

## Batch G — adversarial review outcomes (2026-09-24, EVIDENCE-BASED)

Three hypothesized vulnerabilities were investigated and REJECTED with code
evidence (no fix needed — the worried-about behavior does not exist):

- G-01 SSH silent-trust: `AllowInsecureHostKey: true` appears ONLY in
  `*_test.go` files. All 8 production SSH client configs
  (send/open/drop/relay/node/serve/receive + hub) leave the zero value
  (false) → known_hosts verification, fail-closed. I-24 (no pinning UX)
  stays OPEN and accurately scoped.
- G-02 DNS rebinding (`127.0.0.1.nip.io`, resolving names): all Host/Origin
  checks (`splitAuthority`, `sameLoopbackAuthority`,
  `requestHostIsLoopbackHeader`, bridge `isLoopbackHost`) are literal string
  matches against `127.0.0.1`/`localhost`/`::1` (plus 127/8 range on the
  bridge A-side) — no DNS resolution anywhere, so resolution-based bypass
  is impossible.
- G-03 `/api/events` auth bypass: `securityMiddleware` gates ALL `/api/*`
  uniformly (Host + Origin/Referer + session/IPC capability) before mux
  routing; `/api/events` has no exemption. Covered by
  TestWebDashboard_EventsSSE.
- G-04 wire version negotiation: no `Version` field on envelopes (only
  beacons carry `v: 1`, mismatches dropped). ACCEPTED design: symmetric-hub
  same-release pairing is the supported topology; unknown types fail closed
  (dispatcher `default` logs + drops; strict `expected %q` errors in
  aside/receiver). Documented in `docs/RELEASE.md` (mixed-version
  unsupported — upgrade both machines) instead of a wire change with
  compat cost and no demonstrated interop failure.
- G-05 release supply chain: `.goreleaser.yaml` verified (6 archives,
  CGO_ENABLED=0, ldflags version injection, checksums.txt). Added SBOM
  generation (no secrets required); cosign signing deliberately absent
  (keys must never live in repo) — recorded as a known limitation with
  checksums.txt verification guidance. New `docs/RELEASE.md` (install /
  upgrade / rollback; every claim verified: no `hub stop` subcommand, 24 h
  part sweep, JSON forward-compat, stale-hub.json takeover).

## Batch G implementation (clipboard uploads, tab-switch fix)

- Dashboard paste-to-upload: a document-level `paste` listener sends a
  clipboard image through the authenticated XHR upload path (switches to the
  QuickDrop tab first); text pastes fall through to the focused field. Only
  file name + size are logged — no clipboard content in logs/URLs.
- Fixed a latent crash the feature exposed: `switchTab` relied on the
  implicit click `event` (`event.currentTarget`), which throws when called
  programmatically (no `classList` on `document`). It now falls back to
  matching the tab button by target id. Click behavior unchanged.
- Marker test extended (`clipboardData`, `paste` listener); JS re-validated
  with `node --check`; full suite green (see batch H validation on commit).

## Batch H validation (2026-09-24)

- `go build` + `go vet` + `go test -count=1 ./...` (12 pkgs) PASS.
- Cross-compile `linux/amd64` + `darwin/arm64` PASS (post batches E–G).
- `go test -count=2 ./internal/hub/ ./internal/bridge/` PASS (lifecycle rerun).
- Fuzz re-run 20s each, all clean: FuzzDecode (~7.0M execs), FuzzValidBeacon
  (~8.1M), FuzzValidateDropMetadata (~8.7M), zero crashes.
- Release smoke (local `go build` binary, v1.0.0 dev default): headless Hub
  start, dashboard 200 with all batch-F/G markers live
  (XHR progress, cancel, signature guard, paste, aria-live), upload 401
  without capability, healthz 200. Smoke artifacts removed.
- `context.Background()` audit (cmd + hub): all remaining uses are
  legitimate roots (signal.NotifyContext, shutdown-after-cancel, nil-parent
  guards) — no detached user-operation contexts. No change.
- README QuickDrop line synced (paste + progress + cancel).
- Cockpit stdin note: the hotkey goroutine blocks in `scanner.Scan()` and
  cannot observe ctx cancellation until stdin EOF — process-lifetime scoped,
  no Hub lifecycle impact; accepted without change.

## Batch I — SSH server review + Q-01 decision (2026-09-24)

- SSH server posture VERIFIED GOOD (no change): `MaxAuthTries: 3`,
  `NoClientAuth: false` with public-key-only auth (no password callback
  exists — password auth impossible), channel type restricted to `"tantu"`
  (`session`/`direct-tcpip` rejected), global + per-channel requests
  discarded, pre-auth handshakes bounded by `MaxConcurrentHandshakes` slots.
  Multi-channel exhaustion requires an already-authenticated
  (authorized_keys) peer — inside the trust boundary, equivalent to opening
  many TCP connections on any transport. Accepted without change. I-27
  (private-PEM as authorized key) stays OPEN as documented compat.
- Q-01 RESOLVED (retain standalone commands): `serve`/`relay`/`node`/`drop`
  stay. Relay already migrated to per-request tickets; drop's header-only
  per-process token is inside the same-user boundary with SR16 rationale
  (never in URLs, no-store page, exact-authority + Origin/Referer still
  enforced). Migrating standalone drop to the Hub session model would be
  major surgery for negligible gain within that boundary. Revisit only if
  maintenance burden grows.
- `go test -race` re-checked: still no C compiler (gcc/cc/clang absent) —
  remains blocked with `-count=2` mitigation. Race safety never claimed.
