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
5 GiB / 10 MiB text caps, no-overwrite publication from verified open descriptors, `.part` staging.
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
| I-19 | bridge | P3 | DOCUMENTED | Unconstrained legacy flows accept any path/state — documented as compat in THREAT-MODEL T2 (real provider flows constrained; localhost + single-attempt still apply) |
| I-20 | bridge | P3 | FIXED | `ExtractCallbackPort` silent 80/443 default removed — missing port is now an explicit error (the 443 branch was unreachable; :80 binds fail obscurely). Tests updated to expect rejection |
| I-21 | protocol | P3 | DOCUMENTED | No `Type` allowlist on decode — deliberate for forward compat (dispatchers enforce); rationale recorded in ARCHITECTURE §Wire Framing |
| I-22 | pairing | P3 | OPEN | SAS is 24-bit interactive-compare only, no commitment (cert.go:139) — accepted design, `--auto-pair` explicitly opt-in |
| I-23 | discovery | P3 | FIXED | Self-filter SAS clause hid legit peers on 24-bit collision — SAS clause now applies only to ephemeral FP-less engines; FP comparison is exact |
| I-24 | cli/send | P3 | FIXED | `send/open/drop/relay --transport=ssh` expose `--ssh-known-hosts` + `--ssh-fingerprint` (SHA256 pin); malformed pins fail fast at construction; live pin test against mock SSH server |
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

## Batch J — performance baseline (2026-09-24, measured local binary)

- Cold start (fresh store incl. ECDSA P-256 identity + cert generation) to
  dashboard 200: ~693 ms on this Windows machine.
- Idle footprint after 10 s (headless Hub, no peers/transfers): VmRSS
  ~14 MB. No idle hot loops by design (3 s status poll is client-driven;
  discovery beacon every 3 s; SSE long-lived but quiet).
- Unauthenticated `GET /api/status` → 401 live-verified in the same run.
- No limits were changed on this evidence: 14 MB idle / sub-second start
  needs no tuning. Large-file behavior remains streaming-bounded (2 MiB
  chunks, 32 MiB RAM spill per upload × 4 slots).

## Batch K — text guard, paste focus, log-content audit (2026-09-24)

- `sendTextDrop` fails fast client-side at the same 10 MiB UTF-8 byte limit
  the server enforces (TextEncoder byte count, not UTF-16 length), with a
  "send as a file instead" message — no wasted multi-MB rejected upload.
- Paste-to-upload now moves keyboard/screen-reader focus to the drop zone
  (`tabindex="-1"`, focus never opens the file dialog); outcome still
  announced via the aria-live status region.
- Server-side log-content audit: Hub logs metadata only (file names, byte
  counts, peer names, SHA-256 short) — snippet/file/clipboard contents never
  enter logs (hub.go drop actions). Combined with the client (lengths only),
  the "no sensitive content in logs" claim is now evidence-backed end to end.
- `confirmUnpair` verified: native `confirm()` gate with revocation warning
  before POST — destructive-action protection present, no change.
- Marker test extended; JS `node --check` clean; full suite 12/12 green.

## Batch M — product completion round (2026-09-24)

- M1 version provenance: plain `go build` binaries report `1.0.0-dev+<sha>[.dirty]`
  via `debug.ReadBuildInfo` VCS fallback (ldflags-stamped releases untouched).
  Live-verified in smoke binary.
- M2 discovery self-filter (I-23 FIXED): SAS clause FP-less-engines-only +
  `TestEngine_SelfFilterSASCollision`.
- M3 received-file serving: `GET /api/drop/file?id=&mode=` (ID-addressed,
  OutputDir containment, regular-file-only, 8 MB raster-only inline with fixed
  content-type map + sandbox CSP, everything else forced attachment). Dashboard
  renders image previews (`onerror` fallback) + per-file Download links.
  Tests: inline/download/svg/outside-dir/text-kind/unknown/symlink/unauth +
  `RecentDropsBuffer.Find` + markers.
- M4 real-browser validation (headless Chrome + dependency-free CDP harness in
  `temp`, documented here): zero console errors, native-button tabs,
  keyboard focus, 4/4 labelled inputs, paste-to-upload without exceptions,
  preview/download markup live, screenshot-reviewed. Findings fixed in the
  same round: stale-session banner (`sessionBanner` + probe), input labels +
  keyboard-operable drop zone (`role=button`, Enter/Space), inline SVG
  favicon (killed `/favicon.ico` 404).
- M5 SSH pinning UX (I-24 FIXED): `--ssh-known-hosts` + `--ssh-fingerprint`
  on send/open/drop/relay; malformed pins fail fast in `NewSSHTransport`;
  `TestSSHTransport_HostKeyFingerprintPin` (correct pin dials, wrong pin
  fails) + README Security Model bullet.
- M6 limit unification: new `drop.DefaultMaxTextSize` single source (Hub
  dispatcher, OnMeta, dashboard handler, receiver default); MaxBytesReader
  upload cap uses `drop.DefaultMaxDropSize`; JS mirror commented.
- M7 startup fail-fast: `Hub.ensureOutputDir` (phase 3b, before listeners) —
  proven by live repro (Hub previously started fine with a file as
  `--output-dir`); `TestHub_StartRejectsInvalidOutputDir`.
- M8 explicit callback ports (I-20 FIXED) + T2 legacy-flow compat note
  (I-19 DOCUMENTED).
- Validation: build + vet + full suite green; dashboard JS `node --check`
  clean (first-script-block extraction); linux/amd64 + darwin/arm64
  cross-compile green at commit time.

## Batch L — module hygiene (2026-09-24)
- `go mod tidy` was NOT clean: `golang.org/x/crypto` was marked
  `// indirect` while directly imported, and `golang.org/x/term` hashes were
  missing from `go.sum` (needed by the build — tidy downloaded v0.45.0).
  Committed the tidy result (direct require + sum entries); full suite
  re-verified green after. Release builds (`goreleaser` runs tidy as a
  before-hook) would have produced exactly this diff — now no delta.
- `gofmt -l` flags ~26 files repo-wide, verified as PURE CRLF↔LF noise:
  every +/- diff pair is balanced (whole-file EOL normalization), including
  untouched files (status.go, unpair.go, 20+ test files). CI runs only
  build/vet/test (race on linux/mac) — no fmt gate. No action taken;
  converting line endings repo-wide would pollute blame for zero behavior
  gain.
- CI note: `.github/workflows/ci.yml` runs `go test -race` on
  ubuntu/macos — the race evidence this environment cannot produce will be
  generated by CI on push (push itself still requires explicit user approval).

## Batch N — throughput, authed browser flow, changelog (2026-09-24)

- Throughput evidence (100 MB loopback self-send via dashboard upload API):
  8 s wall (~12.5 MB/s incl. multipart parse, wire re-chunk, 2x disk write,
  2x SHA-256), byte-identical SHA-256 before/after. No limits changed:
  streaming design holds at 100 MB; artifacts removed afterward.
- In-browser authenticated API (new `authed-check.mjs` CDP script):
  IPC-header `/api/status` (identity present) + `/api/drop/recent` (array)
  both 200 in real Chrome, zero console errors. Complements the
  cookie-session unit tests (different credential path, same middleware).
- `CHANGELOG.md` created (Unreleased + prior-development summary; ledger
  stays in DEV-RECORD, procedures in RELEASE.md).
- I-21 DOCUMENTED: no decode-time type allowlist is deliberate
  forward-compat; rationale added to ARCHITECTURE wire-framing note.
- RELEASE.md version paragraph synced with M1 VCS fallback.
- Validation: build + vet + full suite green (below); JS `node --check`
  clean; linux/darwin cross-compile green.

## Batch O — diagnostics, unification, wrap audit (2026-09-24)

- `status` upgraded to a diagnostic command: version (VCS-aware), store path,
  Hub liveness with dashboard URL + transport, then identity/peers.
  Live-verified fresh and against a running Hub. Q-01-adjacent: stale
  hub.json probes read as "not running".
- Pairing payload types unified: `internal/protocol/pairing.go` removed
  (its `PairDecisionPayload` had zero users); Hub display decode uses
  `pairing.PairHelloPayload` (validated single source). Pending-pairing
  names verified escaped in dashboard render + control-char-free at the
  pairing layer — no stored XSS.
- X-CLI-Version now observed: Hub middleware warn-logs CLI/Hub skew on
  delegated calls (`TestWebDashboard_CLIVersionSkewLogged`); mixed versions
  stay best-effort per RELEASE.md.
- `wrap` forwards `--ssh-known-hosts`/`--ssh-fingerprint` into the child
  BROWSER command (previously silently dropped the pin). node/receive/serve
  SSH constructions are listen-side (no remote host-key verification
  applies) — verified and left unchanged.
- Q-02 resolved: background winget install completed (Go 1.27.0 in
  `C:\Program Files\Go`, on system PATH). Session unaffected — every
  command here pins the 1.26.3 toolchain matching `go.mod`; `go vet`
  also passes under it implicitly via cross-checks. No action.
- Validation: build + vet + full suite green (above).

## Batch O/P — diagnostics, unification, skew, recovery wiring (2026-09-24)

- `status` is now a diagnostic command (version, store path, Hub liveness +
  dashboard URL, identity, peers); live-verified fresh and against a hub.
- Payload types unified: `internal/protocol/pairing.go` deleted;
  Hub display decode uses validated `pairing.PairHelloPayload`. Pending
  pairing names verified escaped end to end (dashboard `escapeHTML` +
  pairing-layer control-char rejection) — no stored XSS.
- Version skew observed both directions: Hub middleware warn-logs
  X-CLI-Version mismatches (tested); CLI `noteHubVersionSkew` warns on
  send/open/status delegation (probe `Version` was already populated).
- `wrap` forwards the new SSH pinning flags to the child BROWSER command;
  node/receive/serve SSH constructions verified listen-side (no change).
- `TestHub_StartSweepsStalePartials`: startup sweep wiring proven (aged
  `.part` removed, fresh kept) — recovery path is real, not just a helper.
- Q-02 resolved: winget Go 1.27 completed system-wide; session still pins
  portable 1.26.3 per `go.mod`. No action.
- Validation: build + vet + full suite green (above).

## Batch P — fuzz round 2, EOL verification, history note (2026-09-24)

- New `FuzzValidateCallbackRelay` (bridge header caps/dedup); re-ran all four
  targets 20s+: decode, beacon, metadata, callback-relay — zero crashes.
- gofmt/EOL verified by byte inspection (`od`): blobs are LF,
  `core.autocrlf=true` checks out CRLF working files repo-wide (pre-existing
  convention, CI has no fmt gate). My new files are gofmt-clean; edited files
  introduce no new finding category. Batch L no-action decision stands.
- History note: commit `21bcb2e` (batch O, first half — status diagnostics,
  wrap pinning, payload unification, Hub skew log) landed from parallel
  prior-agent work; this session's `98464fd` layers the second half
  (noteHubVersionSkew + call sites, sweep wiring test, record) with zero
  overlap (verified additive via inter-commit diff). No duplication.
- Cockpit empty-peer path re-audited (`handlePeerCommand` guides to
  `tantu pair`) — no change. Pending-pairing names verified escaped end to
  end — no stored XSS.
- Validation: full suite green at Batch O/P commit; fuzz clean (above).

## Batch Q — restart-during-upload lifecycle proof (2026-09-24)

- `TestWebDashboard_RestartDuringUploadsReleasesSlots`: all 4 upload slots
  held by stalled uploads → 5th fails fast 503 (no queueing); `Stop`
  mid-upload returns cleanly; in-flight uploads terminate (3x repeat, no
  flakes); restart succeeds and the full self-delivery pipeline returns 200
  (proves cross-generation slot release — a leak would 503 forever).
- Validation: full suite green (above).

## Batch R — cockpit selection strictness (2026-09-24)

- Batch A fixed lax `Sscanf("%d")` peer selection in `pair.go` but missed
  three identical copies in the cockpit (file-send, text-send, and active
  peer selectors): all now use strict `strconv.Atoi`. Repo-wide grep
  confirms no remaining behavioral `Sscanf` on user input (one ignored-error
  parse of locally-generated listener ports in `handshake.go`, left as is).
- Validation: build + vet + full suite green (above).

## Batch S — cockpit selection helper, release mechanics proof (2026-09-24)

- `parsePeerSelection` extracted (single strict implementation for all three
  cockpit selectors) + `cockpit_test.go` table test (`1abc` must not select,
  out-of-range falls through to resolver, empty/nil safe).
- Release mechanics proven without goreleaser: `-ldflags "-X main.version=
  -X .../hub.HubVersion"` stamps `tantu version` (`v9.9.9-test` verified),
  `sha256sum` checksums verified, stamped linux/amd64 cross-build verified.
  Matches `.goreleaser.yaml` ldflags (see RELEASE.md).
- Validation: Batch R full suite green; this batch targeted cmd tests green
  (full suite at commit).

## Batch T — adversarial round-3 fixes (2026-09-24)

Independent review of batches M–S returned 11 findings; all addressed:
- #1 discovery self-filter test was vacuous (bypassed listenLoop via
  recordNode). Filter extracted to testable `isSelfBeacon` (now EqualFold
  for hex FP); unit tests + real-UDP end-to-end test
  (`TestEngine_SelfFilterEndToEndUDP`) prove the ingest path.
- #2 `serveReceivedFile` now EvalSymlinks both sides + SameFile
  descriptor check; honest residual comment; symlinked-dir test added.
- #3 `POST /api/config` validates via shared `validateOutputDir`
  (extracted from `ensureOutputDir`); orphan-on-change documented;
  bad-dir test asserts 400 + no state change.
- #4 skew comparison canonicalized (`hub.CanonicalVersion` strips
  `.dirty`); Hub warnings deduped per version per run; CLI call-site set
  documented (send/open/status).
- #5 fingerprint pins fully validated at construction (SHA256 scheme +
  unpadded-base64 32-byte body); pin test extended with malformed bodies.
  (Caught during implementation: OpenSSH prints unpadded base64 —
  validator accepts both padded and raw.)
- #6 CSP comment corrected (sandbox constrains navigation, not `<img>`).
- #8 compile-adjacent assertion `standaloneTextDropLimit ==
  drop.DefaultMaxTextSize` in cmd tests.
- #9 `parsePeerSelection`/`pair.go` reject `+1`-style non-plain indices
  (digit-only), keeping those names resolvable.
- #10 session banner moved above tab panes (global); `:focus-visible`
  styles for tabs/drop-zone; `sr-only` class + labelled dynamic peer
  select; README platform support scoped (Windows runtime-verified,
  Linux/macOS compile-verified).
- Validation: build + vet + full suite + JS `node --check` green;
  linux/darwin cross-compile green; real-Chrome harness re-run
  ALL_BROWSER_CHECKS_PASSED after the banner move.

## Batch U — changelog sync, stress + fuzz re-run (2026-09-24)

- CHANGELOG Unreleased synced with batches O–T (status diagnostics,
  skew warnings, SSH pinning UX, selection strictness, output-dir
  fail-fast, header canonicalization).
- `go test -count=2 ./internal/hub/ ./internal/bridge/` green (lifecycle
  rerun post-batches T).
- Fuzz re-run 20s: FuzzValidateCallbackRelay, FuzzDecode — zero crashes,
  no new corpus files (nothing to commit from fuzzing).

## Batch V — stdout resume-corruption fix + standalone live-only trust (2026-09-24)

- REAL BUG (found by live two-store repro, not review): standalone
  `receive`/`node` returned `os.Stdout` as the text-drop writer. The
  receiver treats a seekable destination's offset as resume bytes, so any
  text smaller than a redirected stdout's accumulated offset failed with
  "invalid existing byte count for resumption" (e.g. `tantu receive >
  recv.log` broke all small texts). Fix: `unseekableWriter`/`stdoutTextWriter`
  in hardening.go + unit test. Live-verified: redirected-log receiver now
  delivers text.
- Standalone long-lived listeners (`serve`/`node`/`receive` LAN) dropped
  their start-time trust snapshots for live-only `IsTrusted` (same class
  as Hub C-01). Live-proven on `receive --loop`: paired send delivered;
  after `unpair`, send fails with `tls: bad certificate` and nothing is
  received (3-line log forensics confirmed no leak; earlier grep hit was
  the pre-unpair delivery).
- Validation: build + vet + full suite green (above).

## Batch W — live pairing ceremony proof (2026-09-24)

- Full legacy pairing ceremony exercised live with two temp stores:
  initiator (`pair --port 19892`, piped `y`) + responder (`pair
  --peer=127.0.0.1:19892`, piped `y`). Both sides displayed matching
  SAS codes (02b655/71d020), both confirmed, mutual `status` shows 1
  peer each with correct SAS. Operational address correctly recorded as
  `:9877` (not the pairing port). Temp artifacts removed.

## Batch X — changelog sync, final validation (2026-09-24)

- CHANGELOG Unreleased covers batches V/W (stdout fix, listener revocation).
- Final gates: build + vet + full suite (12/12) + linux/darwin
  cross-compile green. Fuzz (4 targets) and stress (-count=2) green
  earlier this session; no parser changes since.

## Batch Y — first-use recovery, transfer integrity, and release gates (2026-09-24)

### Findings and decisions

- **Y-01 / headless authentication:** `tantu hub --headless` printed a bare
  dashboard address even though every API operation requires a session or IPC
  capability. Headless operators had no cockpit from which to obtain the
  one-time bootstrap fragment. The startup banner and new `tantu dashboard`
  recovery command now print/mint authenticated links. Each Hub listener
  generation rotates its IPC/relay capabilities, and explicit dashboard-link
  requests rotate the bootstrap value.
- **Y-02 / dashboard trust boundary:** A bare/stale dashboard URL caused a
  page to issue a burst of predictable 401s. The root response now supplies a
  server-side session hint; unauthenticated pages stop before protected API
  calls and show recovery guidance. The Hub script uses a per-response CSP
  nonce and delegated DOM events. The bookmarklet's dynamic `javascript:`
  navigation is authorized by an exact CSP hash plus `unsafe-hashes`, avoiding
  a blanket `unsafe-inline` script policy while preserving the core OAuth
  journey. Inline CSS and legacy standalone pages remain explicitly scoped.
- **Y-03 / resumable-file identity:** Receiver staging now uses the private
  `.tantu-staging` directory consistently and anchors operations to verified
  directory handles. Every partial gets a private manifest binding DropID,
  kind, name, size, MIME type, chunk size, and a 64 KiB head hash. Hub and
  standalone receivers reuse only matching, head-hash-validated partials;
  missing legacy manifests cause a safe fresh transfer and mismatched or
  malformed manifests fail closed. Sidecars are removed on successful
  publication and swept with stale partials; completion retries remain
  at-least-once if the final acknowledgement is lost.
- **Y-04 / aggregate resource bounds:** Each long-lived Hub and standalone
  receive process shares a `TransferQuota`: four active transfers / 8 GiB and
  two / 6 GiB per peer by default. Unknown-size streams reserve one chunk and
  grow atomically as data arrives, so they cannot bypass the budget or consume
  the full budget merely by opening an empty session. Separate processes
  sharing one output directory are not a single global quota.
- **Y-05 / cross-process state:** Peer and identity load-modify-write
  transactions now use private directory locks across processes. First-run
  identity generation is atomic. Active peer selection is persisted in
  `active.json`, validated against the live store, and restored on Hub restart.
- **Y-06 / outbound revocation:** LAN `tantu open` now uses the live peer trust
  callback, closing the stale-snapshot gap already fixed in other long-lived
  and outbound paths.
- **Y-07 / observability recovery:** SSE frames now carry IDs and replay the
  bounded ring after reconnect via `Last-Event-ID`; slow-client loss during a
  live connection remains an explicit bounded-buffer tradeoff.
- **Y-08 / accurate UX:** Dashboard labels distinguish paired/trusted state
  from live connectivity, discovery SAS values are labeled unverified, pairing
  offers an actionable `tantu pair` discovery command, tabs/modal are keyboard
  and screen-reader friendly, and `send` rejects directories/empty text rather
  than silently changing meaning.

### Validation evidence

- `go build ./...`, `go vet ./...`, and `go test -count=1 ./...` pass after the
  batch; lifecycle/concurrency packages also pass with `-count=2`.
- Real Chromium validation: bare dashboard produced no console/API error burst;
  a fresh `tantu dashboard` link bootstrapped successfully; delegated tab,
  modal, and bookmarklet interactions worked; the bookmarklet opened its
  one-use relay popup under the nonce/hash CSP with zero console errors.
- Live loopback QuickDrop self-delivery succeeded through the authenticated
  dashboard API; the published file was byte-visible and its manifest was
  removed only after publication.
- CI now verifies modules, pins Go `1.26.3`, runs repeated Windows tests, and
  release pins GoReleaser `v2.18.2`; documentation records the remaining
  platform/interoperability limits rather than overstating release readiness.

### Residual risks / next actions

- Windows ACL enforcement, mixed-version wire negotiation, strict duplicate
  JSON-field rejection, complete SSH deployment UX, and legacy standalone
  inline-script/token migration remain tracked follow-up work.
- Local Windows race execution is still dependent on a complete C toolchain;
  CI remains the authoritative race gate. Do not claim race safety from the
  local `-count=2` runs.

## Final validation and review closeout (2026-09-24)

- `gofmt` was applied to the structurally edited LF Go files. The remaining
  `gofmt -l` entries are pre-existing formatting/CRLF conventions (including
  lifecycle tests and untouched files); no new semantic formatting delta was
  found in the changed LF files. `git diff --check` passes.
- `go build ./...`, `go vet ./...`, and `go test -count=1 ./...` pass across
  all 12 packages after the final edits.
- `go test -count=2 ./internal/hub ./internal/bridge ./internal/pairing
  ./internal/drop ./cmd/tantu` passes. A repeated-run failure exposed a test
  cleanup race in `TestHub_StartSweepsStalePartials`; the test now waits for
  `Hub.Stop`, and the targeted lifecycle cases pass at `-count=5`.
- `GOOS=linux/amd64` and `GOOS=darwin/arm64` cross-compilation with
  `CGO_ENABLED=0` pass. `CGO_ENABLED=1 go test -race ./...` is blocked locally
  because no `gcc`/C toolchain is installed; CI's Linux/macOS race jobs remain
  the authoritative race gate. No race-safety claim is made.
- Final review also synchronized capability reads with Hub-generation
  rotation, made active-peer removal/re-pairing safe across processes, hardened
  zero-value/overflow quota accounting, and made the dashboard recovery client
  reject malformed runtime URLs before opening a browser.
- Temporary live-review processes and artifacts were removed. No commit or
  push was performed.

## Post-review remediation (2026-09-24)

An independent adversarial review identified gaps in the first Batch Y pass.
The following were verified and addressed before the final validation rerun:

- **R-01 / live LAN revocation:** `LANTransport` now treats a non-nil
  `IsTrusted` callback as authoritative; a stale fingerprint snapshot cannot
  OR its way past an unpair. Built-in long-lived and outbound callers no
  longer pass redundant snapshots.
- **R-02 / identity initialization:** all pairing entry points now call the
  cross-process `LoadOrCreateIdentity` transaction, including the standalone
  `tantu pair` initiator.
- **R-03 / retained disk budget:** failed Tantu-owned partials are trimmed to an
  8 GiB per-output-directory retained budget (and 1,024 partials) after
  failures and at startup, with active paths protected. Private numbered
  partials and manifest-marked direct legacy `.part`/`.part-N` files are
  eligible; unmarked direct files are preserved for manual review.
- **R-04 / staging cleanup:** a newly-created Hub partial is removed with its
  sidecar if manifest publication fails.
- **R-05 / SSE ordering and amplification:** event ID allocation, ring append,
  and broadcast are serialized; reconnect DROP events use an in-flight/queued
  reload guard instead of one recent-items request per replayed event.
- **R-06 / active-peer consistency:** `DialPeer`, relay coordination, and
  status use the live-validated active-peer accessor; active-peer mutations
  are serialized and stale selections are cleared safely.
- **R-07 / documentation hygiene:** the audit no longer describes manifests
  as future work, and the unrelated end-of-file test formatting change was
  removed.

Targeted transport, pairing, drop, Hub, and CLI tests pass after these changes.
The complete post-review matrix also passes: `go build ./...`, `go vet ./...`,
`go test -count=1 ./...` (all 12 packages), repeated `-count=2` lifecycle and
concurrency packages, Linux/amd64 and Darwin/arm64 cross-compilation, and
`git diff --check`; the extracted dashboard JavaScript also passes
`node --check`. Local `-race` remains blocked by the missing `gcc` C toolchain;
CI Linux/macOS race jobs remain authoritative. No commit or push was
performed.

## Post-review staging follow-up (2026-09-24)

A second independent review found that the first cleanup pass could still miss
private `.part-N` files, treat arbitrary user `.part` files as Tantu data, and
race maintenance with active receivers. The follow-up remediation:

- **R-08 / numbered private partials:** staging sweep, sidecar cleanup, and
  retained-budget accounting now recognize both `.part` and `.part-N` forms in
  the private staging directory, with regression coverage.
- **R-09 / standalone resume:** metadata-bearing standalone transfers now use a
  DropID-keyed private partial, validate the durable manifest and head hash,
  position the writer at the validated chunk boundary, and fail closed on
  mismatches. An unmarked private partial is not reused; normal stale/budget
  maintenance reclaims that Tantu-owned orphan, while direct legacy files
  remain manual-review items.
- **R-10 / active ownership:** each built-in receiver creates a cross-process
  `.active` marker before opening a partial; maintenance skips marked files,
  and success/failure paths release the marker. Creation/registration is also
  serialized with the in-process maintenance lock.
- **R-11 / user-file safety:** automatic direct-legacy cleanup now requires a
  valid Tantu manifest. Unmarked `.part`/`.part-N` files are never deleted by
  age or budget maintenance, avoiding destructive behavior in a shared working
  directory. Private staging remains Tantu-owned and is still swept/bounded.
- **R-12 / symlink containment:** maintenance refuses to traverse a symlink
  planted at `.tantu-staging`, preventing cleanup from touching files outside
  the configured output directory.

The remaining staging caveats are explicit: a pre-manifest process from an older
mixed-version installation has no activity marker, unmarked direct legacy files
require operator review, and the manifest binds only metadata plus a 64 KiB
head hash rather than the full payload or sender identity. Current receiver
paths create owner-checked markers before opening bytes; marker/maintenance
transitions share a cross-process lock, private operations use verified
directory handles, and crash-orphaned markers/temp manifests are reclaimed
after the 24-hour stale window.

## Post-staging validation (2026-09-24)

- `go mod verify`, `go build ./...`, `go vet ./...`, and
  `go test -count=1 ./...` pass across all 12 packages.
- `go test -count=2 ./internal/hub ./internal/bridge ./internal/pairing
  ./internal/drop ./cmd/tantu` passes after the staging, resume, activity-marker,
  and legacy-safety changes.
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...` and
  `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...` pass.
- `git diff --check` passes, and the changed LF Go files checked with `gofmt -l`
  are clean; repository-wide pre-existing CRLF/format entries remain. The
  working tree remains intentionally uncommitted; no push was performed.
- Local `go test -race` remains unavailable because this Windows environment
  has no C compiler; CI Linux/macOS race jobs remain the authoritative gate.

## Runtime-lock concurrency closeout (2026-09-24)

- `withRuntimeLock` now serializes in-process runtime metadata transactions
  with `runtimeLockMu`, while retaining the directory lock for cross-process
  coordination. On Windows, an existing lock directory plus an access-denied
  `Mkdir` result is treated as contention; unrelated permission failures still
  fail immediately.
- The concurrent runtime-writer regression passes at `-count=20`; the runtime
  ownership/concurrency cases also pass at `-count=5`.
- The post-fix validation used the pinned Go 1.26.3 toolchain: `go mod verify`,
  build, vet, full test, repeated package test, Linux/amd64 and Darwin/arm64
  cross-build, and `git diff --check` are green. The newly edited runtime file
  and new LF Go files are `gofmt`-clean;
  the two reported `gofmt -l` entries are pre-existing test-file formatting
  conventions. This does not establish race safety; local `-race` remains
  blocked by the missing C toolchain and CI remains authoritative.

## Independent staging audit remediation (2026-09-24)

The second read-only audit identified ownership, replacement-race, and Windows
hardlink gaps. The following were addressed before the final matrix:

- **R-13 / duplicate attempt ownership:** the dispatcher assigns an opaque
  per-attempt token to local metadata callbacks. Hub and Node now scope
  reservation, activity, completion, and cleanup state to that token; rejected
  or pre-reservation attempts cannot tear down an active transfer with the
  same DropID.
- **R-14 / root-anchored staging and publication:** private staging and
  manifest operations use verified `os.Root` handles. Final publication copies
  from the verified open source descriptor into a root-relative, no-overwrite
  destination, and source cleanup is identity-checked. A staging pathname
  replacement can no longer redirect maintenance or payload writes outside the
  opened directory on supported platforms.
- **R-15 / cross-process activity ownership:** marker acquisition, touch, and
  release use the same cross-process maintenance lock; release closures verify
  marker identity so an old owner cannot remove a replacement marker. Orphan
  `.active` and manifest-temp artifacts are swept and included in retained-file
  accounting.
- **R-16 / Windows hardlinks:** resume checks query the open Windows handle's
  link count and fail closed if it cannot be queried; the former Windows no-op
  and test skip were removed.
- **R-17 / validation and cleanup safety:** malformed/foreign manifests no
  longer authorize direct-file deletion, Hub failure cleanup uses the output
  directory captured by the transfer, and Hub resume no longer treats general
  permission/open failures as safe-to-replace races.
- **R-18 / durability and filename boundaries:** file receivers sync each
  non-empty chunk before its offset can be reused, and cross-platform filename
  sanitization replaces Win32-invalid characters. Full-content/sender identity,
  power-loss guarantees, completion idempotency, and cross-process quota
  coordination remain explicit follow-up boundaries rather than implied claims.

### Post-audit validation (2026-09-24)

- Pinned Go 1.26.3 `go mod verify`, `go build ./...`, `go vet ./...`, and
  `go test ./...` pass across all 12 packages.
- `go test -count=2 ./internal/hub ./internal/bridge ./internal/pairing
  ./internal/drop ./cmd/tantu` passes; duplicate-DropID ownership,
  root-anchored staging, owner-safe marker release, orphan-artifact cleanup,
  invalid-manifest preservation, and true resume-offset tests pass repeatedly.
- `CGO_ENABLED=0` Linux/amd64 and Darwin/arm64 cross-builds pass. `git diff
  --check` passes; targeted changed LF Go files are `gofmt`-clean, with only
  the previously noted repository formatting conventions remaining.
- The attempted Windows hardlink test is now enabled (the skip was removed);
  the local Windows runtime test suite passes. A local `-race` run still stops
  before compilation because `gcc` is unavailable, so CI Linux/macOS race
  jobs remain authoritative and no race-safety claim is made.

## Batch Z — UX upgrade vertical slice 1 (2026-09-25, EVIDENCE-BASED)

Plan v6.0 (decisions recorded in `docs/DECISIONS.md`) was treated as strategy,
not a literal patch. Each P0 was interrogated for wire-compat, privacy,
and fake-history risk before implementation; the slice below is the
dependency-ordered minimum that makes every send visible, intentional,
verifiable, and recoverable without a framework migration or new transport.

### What changed

- **Canonical sender truth:** `internal/hub/operations.go` adds a bounded
  (50) session-local outbound ledger with logical operation IDs, DropIDs,
  destination snapshot, terminal states
  (completed/retryable/terminal/cancelled/duplicate-risk), and explicit
  retry/duplicate/data safety plus one next action. No payload bytes, text,
  tokens, or URLs are stored.
- **Honest upload contract:** `POST /api/drop/upload` (text + file) now
  generates per-attempt DropIDs, tracks negotiation via OnAck, classifies
  failures (dial/integrity/quota/drop_complete/drop_data/timeout/cancel),
  records every dialled attempt, and returns operation_id, destination,
  verified, code, plain_message, retry_safe, duplicate_risk, data_safe,
  next_action, and diagnostic_id. Validation/slot rejections return the same
  contract without ledger spam. `GET /api/transfers/recent` exposes the
  ledger (capability-protected).
- **Dashboard safe-send:** clipboard/drag/picker files stage for preview
  (name/size/type/destination, image thumbnail <=8 MB via ObjectURL, revoked
  after) with explicit Send/Cancel and a visible expert immediate-send
  opt-in. Destination is always visible; header no longer claims unprobed
  reachability ("Trusted"); next-action banner, Transfers tab, reduced-motion
  and scroll-margin fixes included. Success/failure messages name the
  destination, operation ID, and recovery.
- **CLI recovery:** new `tantu doctor` (human + `--json`, exit 0/1, no
  secrets), `tantu status --json` (same contract), `tantu transfer list` /
  `tantu transfers` (session ledger, `--json`), and an explicit refusal of
  blind `transfer retry` after unknown outcomes.

### Decisions

- Session-local (not yet durable) ledger with explicit restart-clears
  labeling, per plan Q5 (persist after contract exists). Refresh/reconnect
  within a Hub generation reconciles via snapshot; Hub restart starts a new
  generation rather than lying about history.
- No wire change: DropID stays per-attempt random; operation ID is
  sender-local. Mixed-version policy unchanged (upgrade both).
- Preview-by-default for all file sources (picker/drag/paste), not only
  clipboard, per the risk-based confirmation table; expert skip is visible
  and never hides destination.

### Validation

- `go build ./...`, `go vet ./...`, `go test -count=1 ./...` green (12 pkgs).
- New tests: ledger bound, error taxonomy (dial/integrity/drop_complete/
  drop_data), upload error/success contracts with privacy (no payload echo),
  transfers auth, doctor fresh/live/transfer-fetch CLI tests, dashboard
  markers for preview/destination/transfers and absence of auto-send patterns.
- Dashboard JS passes `node --check` (with `{{SESSION_READY}}` stubbed).
- `git diff --check` clean; changed Go files `gofmt`-clean. Local `-race`
  still blocked (no gcc); CI remains authoritative. No commit or push.

## Batch Z2 — durable history, support bundle, a11y gaps (2026-09-25)

Continuation of the v6.0 vertical slices. The remaining P0 honesty gap was
Hub restart wiping sender truth (Journey 10); the remaining P0 diagnostic
gap was no redacted bundle path; remaining a11y gaps were modal focus trap
and cockpit destination silence.

### What changed

- **Durable ledger:** `transfers.json` (v1 envelope, 0600, atomic
  temp+fsync+rename with Windows retry) in the store dir, last 50 / 30 days,
  metadata-only by construction. Saved synchronously on every record
  (best-effort; never fails the upload); loaded and validated on Hub Start
  (missing = clean first run, corrupt = warn + start empty). `Add` also
  prunes by age so a months-long Hub generation cannot accumulate stale ops.
- **Offline CLI truth:** `tantu transfers` / `transfer list` falls back to
  last-saved history with an explicit stale label when the Hub is stopped;
  errors only when neither live nor saved history exists.
- **Support bundle:** `tantu doctor --bundle-path <file>` writes owner-only
  JSON (health report, live-or-saved transfers, redacted Hub logs, redaction
  note). Never contains payload bytes, snippets, tokens, keys, or full
  sensitive URLs. JSON mode keeps stdout pure (bundle path to stderr).
- **Focus trap:** pairing dialog cycles Tab/Shift-Tab within the modal;
  Escape and focus-restore behavior unchanged.
- **Cockpit destinations:** `cockpitDestinationName` resolves single, active,
  or explicit targets without silent substitution (unresolvable echoes);
  file and text sends print `Destination: X` before transmitting.

### Decisions

- Single-writer file (the Hub) + atomic rename: readers never see partial
  content, no cross-process lock needed (one Hub owns a store dir via
  hub.json). CLI reads the same file offline.
- 30-day / 50-record bound matches the dashboard Transfers copy and CLI
  note; configurable retention deferred (no demand signal yet).
- `transfer retry` remains refused (at-least-once duplicate risk without
  idempotency); `send --json` deferred to a later CLI-parity slice.

### Validation

- `go build`, `go vet`, `go test -count=1 ./...` green (12 pkgs);
  linux/amd64 + darwin/arm64 cross-build clean; dashboard JS `node --check`
  clean; `git diff --check` and `gofmt` clean.
- New tests: age prune, persist round-trip (privacy: no payload in file),
  restart-restore via two Hub generations, corrupt-starts-empty, saved-file
  fallback read, offline + live bundle redaction (no key/token/capability
  material, no payload echo), cockpit destination resolution, modal-trap
  marker. No commit or push.

## Batch Z3 — send contract, deletion, labels, status (2026-09-25)

Final automatable slice of the v6.0 plan. Items were dependency-ordered:
status next-action, `send --json` + exit codes, transfer-history deletion,
standalone labels, release/threat docs, and an honest plan-tracking record.

### What changed

- **`tantu status` next action:** human output ends with one recommended
  next action (identity → pair; no peers → pair; Hub stopped → start;
  else send a test). No-peers path prints it before returning.
- **`tantu send --json`:** machine-readable contract (operation ID,
  destination, verification, safety flags; never payload) with exit codes
  0/1/2/3. Direct sends assign and report their wire DropID
  (`drop.NewDropID`); delegation parses the Hub success body and preserves
  the Hub error contract via typed `hubError` (human `Error()` format
  unchanged). Direct failures reuse the single taxonomy
  (`hub.ClassifyTransferError`, newly exported) with OnAck negotiation
  tracking; duplicate-risk exits 3 with an inbox check.
- **Deletion:** `DELETE /api/transfers/recent` (capability-protected,
  returns removed count) + dashboard Clear button with consequence
  confirmation + `tantu transfer clear --yes` (live API when the Hub runs,
  saved-file removal when stopped; refuses without `--yes`).
- **Standalone labels:** `serve`/`node` print Hub pointers; usage marks
  node/serve/relay/drop advanced or compatibility (`drop`/`relay` already
  carried Hub tips).
- **Docs:** `transfers.json` recorded forward-compatible in `docs/RELEASE.md`
  (upgrade + rollback); threat-model assets/SR12/trust table include it;
  new `docs/UX-STATUS.md` maps every P0/P1 to state + evidence level with a
  stop-rule assessment that leaves gates 4–5 red (human validation
  unproducible here) — no UX-complete claim is made.

### Decisions

- Human send output preserved byte-for-byte except additive ASCII
  Destination/Operation-ID lines (no emoji-line edits, avoiding the
  encoding fragility seen in earlier attempts).
- `send --json` prints result JSON to stdout in all cases (success and
  failure) for automation; validation maps to exit 2, duplicate-risk to 3.
- Loopback self-send destination corrected to "local Hub" (was "default
  peer"); old records keep their recorded values — history is never
  rewritten.

### Validation

- Live smoke (real binaries, temp store, artifacts removed): delegated
  `--json` success with Hub operation ID; cross-binary-restart history
  restore (2 ops); validation exit 2; unresolvable-peer exit 2; offline
  stale fallback; direct dial-refused exit 1 with full contract; offline
  doctor + bundle write.
- Flaky Windows TempDir cleanup in Hub-starting CLI tests fixed by waiting
  for `Hub.Stop()` (same remedy as the earlier hub lifecycle case);
  5/5 reruns green. Cockpit test Hub now uses an isolated output dir.
- `go build`, `go vet`, `go test -count=1 ./...` green (12 pkgs);
  `-count=2` lifecycle rerun green; linux/amd64 + darwin/arm64 clean;
  dashboard JS `node --check` clean; `gofmt`/`git diff --check` clean.
  Local `-race` still blocked (no gcc); CI remains authoritative. No commit
  or push.

## Batch Z4 — relay history, benchmark, fuzz, designs (2026-09-25)

Final deferred-backend slice plus measurement and human-program materials.
Pushed batches Z–Z3 to origin/main first (remote CI race jobs pending).

### What changed

- **Relay attempt ledger** (`internal/hub/relay_history.go`): sender-side
  submitted → waiting_callback → complete/failed/cancelled with target
  peer, origin host only, redacted code, and next action. Only the bare
  host is ever stored (the sanitizer preserves single-use query values, so
  full sanitized URLs are unpersistable by construction); failures keep a
  category, never raw bridge errors. Durable `authorizations.json` (last
  50, 30 days, same atomic/0600 pattern), `GET /api/relay/recent`,
  dashboard Recent Authorizations card, and bundle inclusion (live or
  saved-file). Recording hooks cover validation, resolution, dial, and
  B-side phases; coalesced follower requests share the leader's attempt.
- **Benchmark harness** (`internal/drop/throughput_bench_test.go`):
  1 MiB loopback E2E (framing, chunk, staging sync, 2× SHA-256), stdlib
  only. Local baseline (i9-13950HX, Windows): ~8.5 ms/op, ~123 MB/s,
  ~12.6 MB/op, 235 allocs/op.
- **Fuzz round 3:** all four targets 20 s each — decode ~6.0M execs,
  beacon ~7.3M, metadata ~5.5M, callback-relay ~2.7M — zero crashes, no new
  corpus files.
- **Docs:** `docs/SPEC-WIRE-VERSIONING.md` (additive envelope version +
  idempotency-key + receiver tombstone proposal with open decisions and
  rollout), `docs/RESEARCH-PACKET.md` (consent, hypotheses, 28 grouped
  tasks, cohorts/sample floor, thresholds, ledger template, matrices).
  UX-STATUS: OAuth row done, tombstone deferred-with-design, perf harness
  done, research packet linked; ARCHITECTURE relay tab and CHANGELOG synced.

### Validation

- New tests: ledger bound, origin redaction (host-only incl. adversarial
  query), persist round-trip (no query material), invalid-URL API path
  (failed/invalid_input, secret assert), relay/recent auth gate, dashboard
  markers, live bundle authorizations (failed/host-only/no leak).
- `go build`, `go vet`, full suite green; JS `node --check` clean;
  cross-compile clean. Pushed Z–Z3 before starting this batch. Committed as
  `1fb209c feat(oauth)` + `9844c5a docs(ux)` and pushed.

## Batch Z5 — wire phase 1, output-dir note (2026-09-25)

First implementable slice of `docs/SPEC-WIRE-VERSIONING.md`: additive-only
wire fields with zero behavior change, keeping the tombstone and retry UX
behind the spec's open design decisions.

### What changed

- Envelopes carry `v: 1` stamped at the single encode choke point
  (`protocol.ProtocolVersion`); legacy frames without `v` decode to V == 0;
  explicit versions are never clobbered.
- `drop_send` carries a validated `idempotency_key` (identifier alphabet,
  ≤128 bytes, fail-closed rejection). Hub uploads set key = operation ID;
  direct CLI sends set key = DropID. Receivers validate only.
- Dashboard downloads-directory change now states that previously received
  files stay in their former location (alert + log, matching the existing
  dialog style and the documented orphan-by-design semantics).

### Validation

- New tests: version emitted/legacy-decode/explicit-preserved; key
  round-trip and rejection over a live E2E pair; dashboard marker for the
  old-file explanation.
- Full suite, vet, JS check, and cross-compile green (below). Committed as
  feat(wire) + docs(wire) and pushed.

## Batch Z6 — open --json and relay attempt IDs (2026-09-25)

Parity slice for the relay path, mirroring the send contract: every command
with a JSON mode reports operation identity, destination, and recovery.

### What changed

- `relayOAuth` returns its recorded attempt ID ("" for coalesced follower
  requests, which run no flow of their own). Relay API responses (POST open
  and legacy JSON) carry `operation_id`, `destination`, and the ledger's
  `next_action` additively; HTML bookmarklet responses unchanged.
- `DelegateOpen` returns a parsed result plus typed `RelayAPIError`
  (historical human format preserved); existing mock-server tests pass
  unmodified in behavior.
- `tantu open --json` with exit codes 0/1/2 (no duplicate-risk code: relay
  performs no publication), shared `displayDestination` helper now used by
  both send and open direct paths, and destination print on direct success.
- Dashboard relay outcomes name the destination and link failures to Recent
  Authorizations with the recorded ID and next action.

### Validation

- New tests: relay error/result parsing, failure-JSON mapping, destination
  helper, relay response contract (ID matches the ledger entry).
- Live smoke (real Hub binary, artifacts removed): delegated `open --json`
  failure returns operation ID + destination + next action with exit 1;
  missing-URL validation exits 2.
- Full suite, vet, JS check, cross-compile green (below). Committed as
  `e5a78d1 feat(relay)` + `bf38d66 docs(relay)` and pushed.

## Batch Z7 — relay deletion, rejection evidence, tombstone collision (2026-09-25)

### Mid-session collision (resolved without loss)

While this batch was uncommitted, a parallel agent landed `797a924
feat(drop): idempotency tombstone with sender key control` on top of the
same base — full SPEC phase-2 implementation (receiver tombstone store,
`send --idempotency-key`, key-reuse teaching in the retry refusal),
touching 14 overlapping files. Discovery: `git status` showed foreign
staged deletions. Response, in order: no commits; verified my work intact
in the worktree; `git reset` to safe the index; `git stash push -u`;
verified their tree green; reviewed their diff; restored their versions of
all files outside my 7-file scope; re-applied my changes onto their tree;
full re-verification. Nothing of either party was destroyed; their commit
was local-only (never pushed), so no history rewrite was needed. Lesson:
parallel sessions on one checkout need branch-per-agent discipline.

### Review of 797a924 (adversarial read)

- Ordering correct: validate → tombstone lookup (fail-closed mismatch
  before quota) → quota → discard sink → SHA gate → record-on-success.
- Tombstones live in the private staging dir (0600/atomic), same posture
  as manifests — no new exposure class. Duplicate path skips OnMeta, so no
  staging/buffer/reservation leaks; quota lease held during re-stream is
  released by defer.
- Inline key validation refactored into shared `drop.ValidTombstoneKey`,
  guarded for empty (legacy keyless sends unaffected).
- Sender key control is coherent end to end (flag → delegation → Hub
  acceptance → receiver). Retry refusal preserved with key-reuse teaching.
- No secret logging introduced (keys are random identifiers; digests only).

### What changed (this batch, rebased)

- `DELETE /api/relay/recent` (capability-protected, removed count),
  dashboard Clear button with consequence confirmation, and `transfer clear`
  extended to both ledgers (live API when the Hub runs, saved files when
  stopped; still requires `--yes`).
- Live receiver-rejection test: read-only output dir (Unix-gated, root
  skip) proves the terminal, non-duplicate-risk failure path end to end
  with ledger agreement. Skips honestly on Windows ACLs.

### Validation

- New tests: relay DELETE contract (auth gate, removed count, empty
  reread), CLI relay-file clear, read-only rejection (skipped on Windows;
  CI Unix executes it), dashboard clear markers.
- Full suite, vet, JS check, cross-compile green (below). Committed after
  the rebase; pushed with the batch.

## Batch Z9 — permanent bookmarklet (2026-09-25)
User-reported defect: the dashboard bookmarklet baked in a one-use ticket,
so the second click (or any click after 10 minutes or a Hub restart) died
with "relay token or dashboard session required" until the bookmark was
re-dragged. A bookmark that works once is not a bookmark.

### What changed

- The bookmark carries no credential and never expires. Unauthenticated
  clicks render a confirmation interstitial (safe origin host, destination
  lookup, explicit Relay button); the relay itself still needs the click
  plus a valid session at POST time. Burned-ticket links degrade to the
  same page with an expiry note instead of a 403.
- Removed the Hub one-use ticket machinery (`relayTickets` map, mint and
  consume paths). The per-process token fallback and IPC-header paths relay
  immediately, unchanged. The session cookie is still never accepted as GET
  relay authorization (existing test now pins render-without-relay).
- The interstitial shell is fully static (no user data embedded — the page
  reads its own address bar), proven byte-identical across secrets.

### Validation

- New tests: interstitial render/expiry-note/redaction/static-shell,
  evolved cookie-only assertions, ticket-free bookmark markers.
- Real headless-Chrome run (isolated profile, virtual-time budget): shell
  renders, recovery state shown without a session, no secret in DOM, zero
  console errors.
- Full suite, vet, JS check, cross-compile green (below). Committed as
  `8bf8555 feat(oauth)` + `1edb30d docs(oauth)` and pushed.

## Batch Z8 — tombstone docs accuracy and failure taxonomy (2026-09-25)

Audit follow-through: the parallel tombstone feature shipped without
user-facing documentation, and disk-full paths lacked tests.

### What changed

- CHANGELOG tombstone entry corrected (lookup shipped, not future) with
  `--idempotency-key` retry semantics; README resumable-transfer line
  documents duplicate-safe retry.
- Disk-full taxonomy tests (write/sync/read failure strings classify as
  interrupted, safe retry, no duplicate risk) plus a drop-layer write-failure
  E2E. Incidental finding, kept as designed: the receiver returns the raw
  local error while the sender observes the messaged failure relayed through
  the completion frame; the test asserts both sides of that contract.

### Validation

- New tests listed above, green. Full suite, vet, JS check, cross-compile
  green (below). Committed as `96fea9f test(failures)` + `63b6cc7
  docs(tombstone)` and pushed.

## Batch Z10 — repeat logins are never coalesced (2026-09-26)

User-reported defect with logs: after one successful OAuth login, logging
out and logging in again replayed the previous session — "Duplicate OAuth
request suppressed" with success but no browser opening, so the new login
could never complete. Root cause: session keys normalized away
per-attempt values (state/PKCE/nonce), so a new login mapped onto the
previous login's 2-minute replay entry. The code even documented the flaw
(bside.go: the key "normalizes away per-attempt OAuth values such as
state") while offering no way to start a distinct flow.

### What changed

- Session keys now bind the exact request URL (canonical, all values,
  sha256 — no secret retention) alongside the normalized flow identity, in
  both the bridge session manager and the Hub relay coordinator. Identical
  retries still coalesce in-flight and replay post-success; a new login
  always owns a fresh session and opens its own browser flow.
- Stale comments corrected (bside/aside/coordinator test); `DeriveOAuthFlowID`
  itself unchanged (tested contract holds).

### Validation

- New failing-first test proving the report (same log line, then green
  after the fix): sequential post-logout login plus concurrent distinct
  logins open 4 browser flows; Hub key unit test (equal/reordered vs
  distinct).
- Pre-existing coalescing tests (identical URLs) pass unmodified.
- Full suite, vet, JS check, cross-compile green (below). Committed as
  `1b42151 fix(oauth)` + `69c1f1e docs(oauth)` and pushed.

## Batch Z11 — relay confirmation autofocus (2026-09-25)

Follow-up to a usability challenge on Z9: the interstitial's second click
felt redundant next to the bookmark click. Auto-submitting was rejected on
a concrete threat analysis (a bare navigation — phishing link, restored
tab — would fire logins on the paired machine with no gesture; the click
is the proof of intent, and it cannot move receiver-side because initiation
carries the intent). Kept the click; removed the friction around it: the
Relay button autofocuses once actionable (click → Enter), and the popup
still auto-closes on success.

### Validation

- Serving-path assertion for the focus call; full hub/cmd suites green;
  isolated-worktree check; cross-compile clean. Committed and pushed.
- Live artifact check caught a stale-Hub-on-9876 smoke artifact (previous
  test Hub still releasing the port; new instance fell back silently while
  the probe hit the old one) — reran clean against a fresh instance.

## Batch Z14 — interstitial session watch (2026-09-26)

Follow-up to the "session not established" dead end: the popup showed
recovery guidance but sat disabled until the bookmark was retried.

### What changed

- The interstitial polls `/api/status` every 3 s and enables itself the
  moment a session exists (e.g. the dashboard was just opened in another
  tab): destination rendered, button enabled and focused once, announcement
  emitted once via the live region. Polling never steals focus or repeats
  itself; POST 401 returns to watching; the timer stops on success.
- Readiness keys on HTTP success, not payload shape: an authenticated Hub
  with zero peers answers `peers: null`, which an earlier draft mistook
  for "no session" — caught by live CDP probing against a peerless Hub
  before any commit. New users and loopback testers would have been the
  victims.
- Notable environment finding: a second live agent session is active in
  this environment (`tantu-ux-z14`); all smoke work kept to unique ports,
  paths, and process names, and foreign processes were never touched.

### Validation

- Static markers for the watch behavior; server contract unchanged.
- Live real-Chrome arc: recovery state → bootstrap in-profile → enabled
  button with destination and one-time announcement, stable across polls,
  zero console errors; enabled-state screenshot reviewed.
- Full suite, vet, JS check, cross-compile green (below). No commit or
  push yet.

## Batch Z12 — premium visual refresh (2026-09-25)

Design-token foundation (radius, elevation, type, motion), OS-driven light
theme alongside the dark theme, and component polish across the dashboard,
interstitial, legacy relay pages, and pairing dialog — with no framework,
no external assets, and no weakened guarantee.

### Review findings fixed from screenshots

- User-content areas kept hardcoded near-black backgrounds, unreadable in
  light mode (textarea, received items) — now theme-variable driven. The
  log console stays deliberately dark in both themes (terminal convention).
- Pairing dialog colors were inline and theme-blind — converted to classes.
- `go vet` caught bare `%` sequences in restyled Fprintf templates before
  they could render as runtime garbage.
- An early screenshot round probed the user's own Hub on the shared port
  (stale-process collision); all evidence since is port-pinned and
  version-tag verified. The modal "invisibility" scare was harness error
  (clicked inside a hidden tab); the dialog correctly lives in its tab.

### Validation

- Marker tests extended (theme query, chips, ETA, modal, focus); real
  headless-Chrome runs in both themes for dashboard, interstitial, and the
  opened modal with zero console errors.
- Full suite, vet, JS check, cross-compile green (below). Committed after
  verification; pushed with the batch.

## Batch Z13 — IA audit fixes (2026-09-25)

Decision-by-decision audit of every placement (Plan-mode review): kept the
combined QuickDrop composer and the separate Transfers tab, and fixed what
failed the five user questions.

### What changed

- Shared send-destination header above the composer grid: one visible
  control now governs file, image, clipboard, and text sends (the text
  composer previously inherited an off-screen selection).
- Banner severity order: pairing approval above next-action guidance.
- "Received Items & History" renamed to "Received Items" with an explicit
  session-local note (inbound is memory-only by privacy design; the
  alternative — persisting snippets — was rejected, recorded here).
- Expert mode labeled per-session. Pairing dialog kept in its tab (a move
  to body level was evaluated and rejected as risk without product value);
  `openPairModal` now ensures its tab is visible first.
- Inbox per-item delete deferred with rationale: entry-only removal strands
  files, file removal needs OS-trash semantics — a real design decision,
  not a gap to improvise.

### Validation

- Marker for the shared header; full hub/cmd suites green; re-shot
  dashboard in both themes against a port-pinned Hub (prior round probed
  the user's own Hub on the shared port — evidence hygiene noted).
- Committed after verification; pushed with the batch.

## Batch Z15 - full browser frontend audit and remediation (2026-09-27)

Plan-driven audit of the entire browser frontend (header, all five tabs,
banners, composer, preview, progress, empty/loading/error states, the
interstitial, and the legacy relay pages), element by element, judged against
the five user questions and the three honesty rules. Phase 1 produced a
report plus a prioritized list; Phase 2 implemented the approved items in four
verified slices. Evidence level moved from E1/E3 to E2/E3 across the board.

### Phase 1 findings that mattered most

- CSP authorized only `'self' data:` for images, but the send preview stages
  the chosen file as a page-created `blob:` URL. The browser blocked it, so
  the clipboard-image confirmation showed a permanently broken thumbnail
  (`naturalWidth: 0`, `opacity: 0`) and every paste/drop/selection logged a CSP
  violation. The product's flagship disclosure-safety feature was confirming a
  picture the user could not see.
- The text composer reported nothing on the tab the user clicked from. A
  successful send and an over-limit rejection were both visible only in Live
  Logs, making both indistinguishable from a dead button.
- `loadPendingPairings` reassigned its container on every 3 s status poll,
  replacing the Approve button and throwing focus to `<body>`. The one action
  that grants permanent mutual trust was unusable by keyboard.
- Three light-theme contrast failures (next-action banner 1.20:1, log console
  1.26:1, downloads path 2.27:1) plus a 1.69:1 version tag.
- The log console scrolled 2798 px of content in a 378 px viewport with no
  `tabindex` and no role: unreachable and unscrollable by keyboard.
- No headings at all in the SPA (11 `.card-title` divs, 0 `h1`/`h2`).
- 21 blocking `alert`/`confirm`/`prompt` dialogs; all peer-management actions
  were dialog-only.
- Records showed a bare clock time despite a 30-day retention window.
- `scroll-margin-top: 84px` against a measured 129 px of sticky chrome.

### Maintainer rulings applied

Dark header/tab chrome kept in light mode deliberately; contrast fixed on what
sits on it instead of re-theming. Next-action banner demoted to P1. Tab
arrow-keys P1-low. Dialog replacement sliced to approve-confirm,
unpair-consequences, and logs-clear-confirm; the alias prompt deliberately
left on a native dialog as an acceptable low-risk surface. 44px enforced
targeted, spacing accepted elsewhere. Inbox delete, full AA, and density
judgment remain recorded deferrals.

### What changed

Slice 1 (CSP + a11y mechanics): `blob:` authorized with a test that parses the
served policy and matches it against the scheme the page actually assigns.
Pairing banner guarded by a request-set signature, made an announced status
region, rebuilt with theme-aware classes and 46px targets. Inbox preserves
focus across live re-renders (matched by action, label, and ordinal, because
`data-value` is a per-render sequence number) and announces arrivals once.
Dialog focus restoration no longer accepts `<body>` as a target. Real heading
outline, skip link, navigation landmark restored around the tablist, roving
tabindex with Arrow/Home/End, log console given `tabindex=0` + `role="log"` +
`aria-live="off"`, upload live region removed in favour of `aria-valuetext`,
`--chrome-h` measured rather than guessed.

Slice 2 (comprehension + contrast): text composer reports sending, success,
and failure inline with the draft preserved and verification conditional on
the server response; over-limit rejection visible and explicit that nothing
was sent. Shared `OPERATION_STATE_LABELS` map replaces raw taxonomy, unknown
states degrade to "Unrecognised state", `duplicate_risk` styled as a warning.
Dated records (today/yesterday/date). Buttons name the resolved destination.
Transport named instead of an unconditional mTLS claim. Loopback self-send no
longer labelled "Unauthenticated Peer". Contrast corrected to >=4.5:1 for the
banner, downloads path, SAS value, active pill, success chip, and version tag.
Runnable pair command built from the already-polled listening address.
Consequence-stating confirmations for approve, unpair, and log clear; all three
Clear buttons marked destructive.

Slice 4 (interstitial + legacy): language, viewport, main landmark, h1, 44px
buttons, corrected contrast, visible initial focus (`:focus` added because
programmatic autofocus does not match `:focus-visible`), success moved into a
live region outside the hidden view, 5xx no longer misreported as a missing
session, Cancel has a no-close fallback, and the legacy error page is no longer
a dead end.

### Evidence and method notes

- Built a dependency-free CDP acceptance harness (63 assertions) covering both
  OS themes, dashboard, interstitial, opened modal, 200% zoom, 360 px, reduced
  motion, keyboard order, and computed contrast, with zero console errors.
- The harness refused to trust itself three times and each time it was right:
  a colour-string/array bug in the contrast compositor; a check that compared
  the Relay label against the card instead of its own fill; and a
  focus-visibility assertion that could not pass in headless Chrome because
  the document is never focused. Fixed with `Emulation.setFocusEmulationEnabled`.
- Two harness-authoring bugs worth recording: a PowerShell helper named `Git`
  silently shadowed the `git` executable (names are case-insensitive), and
  `git apply` into a CRLF checkout mixed line endings so `gofmt` flagged every
  file. Verification now copies files byte-for-byte and scopes `gofmt` to
  touched files.
- Evidence hygiene: all probes ran against an isolated Hub on 127.0.0.1:18976
  with a version tag asserted to contain HEAD. The maintainer's own Hub on
  9876 was never probed or screenshotted. One self-inflicted incident is
  recorded in Phase 1: two early `tantu dashboard` invocations without
  `--print` opened localhost dashboard tabs in the system browser, one of
  which reached the maintainer's own Hub. Harmless (localhost, one-time link,
  never opened afterwards) but corrected to `--store-dir ... --print` for all
  later runs.
- A parallel session committed `1df03da` (interstitial session watch) into the
  same checkout mid-batch. It touches only the interstitial script; the CSP
  edit is elsewhere, so there was no conflict, and each slice was verified
  against a worktree at current HEAD.

### Validation

- `go build ./...`, `go vet ./...`, `go test -count=1 ./...` green.
- `gofmt` clean on both touched files (`internal/hub/operations_test.go` is
  gofmt-dirty at HEAD and was not touched by this batch).
- `node --check` clean on the extracted dashboard script with
  `{{SESSION_READY}}` stubbed; exactly one `</script>`; no backticks inside the
  Go raw string; the interstitial shell still embeds no user data and remains
  byte-identical across secrets.
- linux/amd64 and darwin/arm64 cross-compile clean; `git diff --check` clean.
- Each slice verified in an isolated git worktree before its commit. Committed
  as `d4767a6`, `6e568cd`, `1b6735b`, `6e36010`. Not pushed.

## Batch Z16 - multi-peer runtime evidence (2026-09-27)

Follow-up to Z15, and an admission about it: every browser run in Z15 used a
**peerless** loopback Hub. The audit inspected the multi-peer layout
statically and the acceptance pass exercised the empty state, so both agreed
with each other and both were looking at the same one code path. The header
peer select, the destination pills, the peers list, and the Default/Active
badges had no runtime evidence at all.

### Method

Seeded three trusted peers directly into an isolated store's `peers.json`, the
same JSON the store itself writes, so `/api/status` returns genuine peer data
and the real render path runs. SAS values are derived server-side from the
fingerprint, so they are real rather than invented. The set deliberately
includes a default peer, a peer with a 62-character hostname, and a
**one-character fingerprint**, which is the shape most likely to produce a
broken display string. Peers are trusted but unreachable, so sends fail
honestly.

### Defects found (all invisible to the peerless run)

- The Default and Active badges reused the log console's fixed dark palette.
  As text on a light card that measured **1.67:1**. The console palette is
  now scoped to `.log-console`, where the surface is deliberately dark in both
  themes, and standalone badges follow the theme.
- Warning chips, which carry retryable and duplicate-risk state, measured
  **3.9:1** in the light theme. Deepened to the same hue, readable value.
- The page overflowed horizontally at 360px on **every** tab, from two
  independent causes: the header `<select>` is sized by its longest option and
  neither the pill nor the label span wrapping it could shrink; and `.grid-2`
  items default to `min-width: auto`, so one wide descendant forced the track
  past its container.
- A duplicate `max-width: 100%` in the peer select rule silently overrode its
  16rem cap, stretching the control across the whole header. A marker now
  fails if the cap is shadowed again.

### Two of my own test assumptions were wrong, not the product

- The text-send assertion assumed success. A seeded peer is trusted but
  unreachable, so the send fails - and the product reports it inline with the
  next action, which is exactly the defect Z15 fixed. The assertion was
  corrected to require that the outcome is reported **either way**, and that
  the draft is cleared only on success.
- The narrow-viewport check ran against whatever tab happened to be active, so
  a zero-width measurement passed **vacuously** while a different tab was in
  fact overflowing. It now sweeps every tab. Content inside a deliberately
  scrollable or clipping container is excluded, since that cannot widen the
  page; the document-level scroll width remains the authoritative assertion.

### Harness hardening

- The version guard compared against an empty HEAD without noticing, so a
  stale Hub was measured and its result reported as a layout failure. This is
  the second time a stale process or a silent-guard produced a plausible wrong
  answer; it now fails loudly when HEAD cannot be resolved, and cleanup matches
  the Hub by command line rather than a pid file that had gone stale and let an
  old process hold the port.
- A multi-peer run now records its peer count and mode in the report, and
  reports skipped multi-peer assertions as a note, so a peerless pass can
  never be read as multi-peer coverage.

### Validation

- 83/83 assertions with three peers, 68/68 peerless, both OS themes, zero
  unexpected console errors. Screenshots reviewed for the Peers tab, the
  multi-peer composer, and 360px.
- New `TestWebDashboard_MultiPeerSurfaceMarkers` pins the scoped tag palette,
  the warning value, the shrink allowances, and the select cap, and fails on
  each regression.
- Full suite, vet, JS check, cross-compile green in an isolated worktree.
  Committed as `6e12faa`. Not pushed.

### Still unevidenced

A second *live* machine. Seeding proves the multi-peer layout renders and
behaves; it does not prove a real pairing handshake, a real cross-machine
transfer, or the reachable/unreachable state transitions, because nothing was
ever actually dialed. That remains a physical-two-machine task.

## Batch Z17 — 24-hour sliding dashboard sessions (2026-09-27)

User-reported friction: bookmark popups died with "session not established"
after any idle gap over 30 minutes, because sessions expired absolutely —
even mid-use. The popup recovery loop (press `o`, poll enables Relay) already
worked; the TTL was simply harsher than the threat model requires.

### What changed

- `dashboardSessionTTL = 24h`, renewed on every authenticated use
  (`touchDashboardSession`, split out for time-driven tests). Active
  dashboards never expire mid-use; idle gaps under a day just work.
- Cookie `MaxAge` matched to the same TTL (was a hardcoded 1800).
- Untouched on purpose: restart rotation, HttpOnly/Strict, exact-authority
  checks, 32-session cap with expiry pruning. Within the same-user loopback
  boundary, TTL length is the weak control; the strong ones are unchanged.

### Validation

- New time-driven tests (fresh/renew/expire/prune, TTL contract); existing
  bootstrap/rotation tests green.
- Full suite, vet, cross-compile green (below). No commit or push yet.

## Batch Z18 - staleness, a committed harness, and the plan-map artifacts (2026-09-27)

Closed the last honesty gap from the audit, put the acceptance harness in the
repository, and produced three of the plan's §21 artifacts.

### Staleness: the last honesty gap

When the Hub stopped answering, the dashboard kept showing the peer list, the
destination, the SAS, and the identity as if they were current. A dead Hub left
a "Trusted" pill and a destination that read as live, which is exactly the
"never imply unprobed state" rule the product otherwise holds.

- Two consecutive failed polls (not one, so a dropped request does not flash a
  warning) raise a banner: the Hub is not reachable, everything below is the
  last known state, and the time it was last confirmed.
- The destination is **labelled** "last known", never hidden. Hiding it would be
  a worse lie than showing a stale one.
- A Retry action forces an immediate poll instead of waiting out the interval.
- A 401 from the status endpoint now raises the session banner. A Hub restart
  drops the in-memory session, which is a different problem from being
  unreachable; previously the banner was only evaluated at page load, so a
  mid-session restart produced no explanation at all.

**Two bugs the browser pass caught in this change itself.** The retry handler
cleared the failure count *before* polling, so the success path's transition
check never fired and the banner stayed up after a successful recovery. And
because the peer DOM is only re-rendered when the peer set changes, recovering
from a disconnection left the header stuck on "Disconnected" even though the
Hub was back and the peers were identical. Both are fixed and both are pinned
by markers.

Fifteen assertions, run against a real Hub: live state, an unreachable Hub
(status endpoint blocked at the network layer rather than simulated), recovery
through the in-page retry, and a real Hub process kill and restart. The
unreachable case deliberately does **not** claim a lost session, because the
session's state genuinely cannot be known while the Hub is down.

### Incident: a bulk overwrite of the working tree

Mid-implementation, four repository files were overwritten in a single bulk
operation at one identical timestamp, reverting them toward a pre-work state
and destroying the in-flight staleness edits. The reflog was clean, the stash
was empty, and the content matched no git ref, so it was a file-level restore
rather than a git operation. Committed work was never at risk; only uncommitted
work was lost.

Paused and asked rather than force-restoring, per the precedent recorded in an
earlier batch where parallel sessions on one checkout nearly destroyed the
other party's work. On approval, restored from HEAD, confirmed byte-identical
blobs, and cleared a stale stat-cache that was reporting the files as modified.
A second session (`dd71bde`/`5481dde`, sliding dashboard sessions) landed
during the same window; the full suite was re-run green with both sets of
changes before continuing.

### The harness is now in the repository

`tools/uxtest/run.mjs` is dependency-free Node over the DevTools Protocol. It
is what found the blocked preview image, the pairing focus destruction, the
chatty live region, the light-theme contrast failures, the multi-peer badge
colours, and the sticky-header overlap. Leaving it in a temporary directory is
the same mistake the first of those bugs was: the next change reaches for a
marker test because that is all that is available.

It adds nothing to the product runtime, needs no `npm install`, and is not
required to build, test, or run Tantu. It refuses to trust a Hub it did not
just build, because a stale process holding the port once produced a plausible
but entirely wrong layout result. Twenty-nine assertions, green in both
peerless and three-peer modes.

### §21 artifacts

- `docs/KNOWN-LIMITATIONS.md` - every admitted limitation in one register, with
  severity, status, and where it is visible. This is what the stop rule's
  condition 6 actually requires.
- `docs/SURFACE-MATRIX.md` - the standalone migration matrix: audience,
  canonical status, and Hub relationship for every retained surface, plus the
  rules that keep them converged.
- `docs/UX-PLAN-MAP.md` - the plan-to-state map: every P0/P1/P2 item, phase,
  release gate, stop-rule condition, and §21 artifact, marked done, partial, or
  not started. Four §21 artifacts remain genuinely absent and are named as
  such rather than quietly skipped.

### Validation

- `go build ./...`, `go vet ./...`, `go test -count=1 ./...` green.
- `node --check` clean on the harness; 29/29 harness assertions in both modes;
  15/15 staleness assertions.
- Cross-compile, gofmt on touched files, and `git diff --check` clean; each
  commit verified in an isolated worktree.
- Committed as `c4d1ecd`, `4103d38`, and the documentation commit. Not pushed.

## Batch Z19 — spent-link guidance, rotating links, honest bind errors (2026-09-27)

User report with logs: a dashboard opened from `o` showed the session
banner with its bootstrap fragment still in the URL (proof the exchange
never succeeded — a success clears it), both `o` presses printed the
identical link (proof nothing was ever consumed), and a relay failed with
a callback-port bind error mislabeled "in use".

### What changed

- The bootstrap exchange now reports WHY it failed: spent/rotated link
  (press `o` for a fresh one, never reuse old links), unreachable Hub
  (proxy/VPN triage), or full sessions. Previously one generic banner sent
  users looping on the wrong remedy — including reloading a spent link,
  which can never succeed.
- Cockpit `o` mints via `NewDashboardURL` (rotating), matching its own doc
  comment and the `tantu dashboard` command: the terminal always shows the
  current valid link.
- A-side bind failures distinguish EACCES/EPERM (OS policy forbids the
  port — e.g. Windows Hyper-V exclusions, with the `netsh` check named)
  from genuinely in-use ports. Extracted as a tested helper; the occupied-
  port regression test still pins the old message for the true collision.

### Validation

- New unit tests (bind mapping across errno shapes; time-driven session
  tests from Z17); full suite, vet, JS check, cross-compile green (below).
  Committed as `53d0ed1 fix(oauth)` + `fc4e9fb docs(oauth)` and pushed.

## Batch Z20 — spent fragments stop replaying failure (2026-09-27)

User report, diagnosed from evidence: a dashboard tab showing the session
banner with its `#tantu_bootstrap=` fragment still in the URL. A success
clears the fragment, so its presence proves the exchange never succeeded —
and since the exchange re-reads the fragment on every load, each reload
(including Ctrl+Shift+R) deterministically replays the same failure. Not a
cache problem; nothing cached is involved. The same mechanism explains an
incognito/normal split: whichever tab loads a one-time link first consumes
it, and every other tab holding that fragment fails forever.

### What changed

- Failed exchanges now strip the dead fragment, so reloads and duplicated
  tabs cannot replay a doomed token. The targeted guidance (Z19) already
  names the remedy.
- Real-browser proof: a spent link renders the self-explanatory guidance
  with zero console errors.

### Validation

- New headless-Chrome assertion on the spent-link guidance; full suite,
  vet, JS check, cross-compile green (below). No commit or push yet.

---

## Batch Z21 - documentation audit: stated properties versus verified ones (2026-09-29)

### Why this batch exists

Three separate defects found in one session shared a shape: a **stated**
property with nothing checking it.

1. The UTF-8 encoding gate reported the tree clean while 27 mis-decoded sites
   were committed, from the first commit onward.
2. `internal/pairing/cert.go` documented that the SAS word list "excludes
   i, l, o, 0, 1" and that a confusable test existed. Neither was true: 305 of
   512 words contain those letters, and no such test ran.
3. `AGENTS.md` listed "Zero External Dependencies" as an architecture
   invariant. The module requires `golang.org/x/crypto` and
   `golang.org/x/sys`.

In each case the code was correct and the *description* was not, and nothing
in the build or the test suite noticed. That is the failure mode worth naming:
a claim that no check can falsify stops being a claim and becomes decoration.

### The dependency claim, decided properly

The old wording was not merely inaccurate, it was misleading in a specific
direction. Tantu exists to hold someone's OAuth tokens, so a reader deciding
whether to trust it is being offered a *count* in place of evidence.
`x/crypto/ssh` is the largest and least-reviewed code in the build; the honest
position is not "we have no dependencies" but "here is the entire surface, it
is pinned, and here is why each piece is here."

Decided:

- The genuine invariant is **no C libraries, no runtime install** — one static
  binary that runs on a machine with no toolchain. That maps directly onto the
  product promise: every install step is a place the tool can fail on a machine
  that is already broken.
- The restated invariant is a **pinned, enumerable dependency surface**.
- `docs/DEPENDENCIES.md` records the inventory, the import sites, the costs,
  and four conditions for adding a module.

Recorded cost: **clipboard access is blocked on macOS.** Every mature Go
clipboard binding is cgo-backed, and AppKit bindings need cgo, which breaks
the static build. The `purego` route is possible but is a large, fragile
dependency. This is the constraint's first demonstrated price, and it is
written down rather than rediscovered later.

### Rejected

- **Rewriting the word list to avoid i/l/o/0/1.** 305 of 512 entries would
  change, and dropping most of the vocabulary is a worse outcome than the
  confusion those letters cause, which is already handled by the words being
  real words. The comment was corrected instead of the list.
- **Forbidding all edit-distance-1 word pairs.** A 512-word English list
  cannot: the real list has 772 such pairs, and "bear"/"beat" are ordinary
  words. Asserting zero would have demanded a worse list. The check pins what
  the list actually achieves - zero prefix pairs against ~900,000 expected by
  chance - and bounds substitution pairs at 900 so the list cannot silently
  degrade.
- **Deleting the sentence rather than correcting it.** A stale claim removed
  silently cannot be distinguished from a claim never made.

### Validation

- `docs/DEPENDENCIES.md` created; `AGENTS.md` invariant restated with the
  reasoning inline; `README.md` throughput figure replaced with the
  cross-machine measurement and its method.
- Stale claims corrected: `ARCHITECTURE.md` cockpit hotkeys (added `[l]`) and
  its "zero external dependencies" line; `UX-STATUS.md` and `UX-PLAN-MAP.md`
  P2 sections (multi-file shipped); `SURFACE-MATRIX.md` batch row and two new
  convergence rules; `EVIDENCE-TWO-MACHINE.md` directory row.
- New limitation 5.5 records the systemic issue: nothing checks doc claims
  against code. The two instances that could be made self-verifying now are;
  the general case is open.
- `gofmt`, `go build ./...`, `go vet ./...`, `go run ./tools/encgate`,
  `staticcheck`, and `go test ./...` all green.

## Batch Z22 - CI forensics: fourteen red runs, three root causes (2026-09-30)

### Why this batch exists

Two documentation claims were falsified by looking at the artifact instead of
the prose. `docs/KNOWN-LIMITATIONS.md` §1.6 said CI was "unobserved", and
several DEV-RECORD entries (Batch Y, the runtime-lock closeout, Batch Z, Batch
Z4) plus `docs/AUDIT.md` said local `-race` was blocked for lack of gcc. Both
were false on this machine:

- **CI is publicly observable.** `bhaskarjha-dev/tantu` is a public
  repository, so the unauthenticated GitHub API returns every run, job, step
  conclusion, and check annotation. Only the raw job *logs* are gated (403,
  "must have admin rights"), which is enough to see *where* a job died but not
  the stack trace inside it. The last green run was **#5, 2026-09-25**;
  runs **#6 through #19 all failed**.
- **`-race` runs locally.** With `C:\Users\ai2\scoop\apps\mingw\current\bin`
  (mingw-w64 gcc 16.2.0) on `PATH` and `CGO_ENABLED=1`, the full
  `go test -race -count=1 ./...` completes green across all 13 packages.

Fifteen consecutive red runs with nobody reading the result is the same
failure shape as Batch Z21: a gate whose output no one consumes is not a gate.

### The three root causes, isolated by timing

Job durations and annotations from the public API separated the failures into
three independent defects:

1. **govulncheck, fails at ~6 s in `Quality gates`.** Reproduced exactly by
   re-running the gate under CI's own toolchain:
   `GOTOOLCHAIN=go1.26.3 govulncheck ./...` → *9 reachable standard library
   vulnerabilities*, exit 3 (GO-2026-6218, -6090, -6089, -5972, -5856,
   -5039, -5037, -5026, -4970; fixed across 1.26.4-1.26.6), while the local
   1.27 toolchain reports 0. The repository's code was never at fault:
   `govulncheck` judges the stdlib against the *toolchain that compiles it*,
   so pinning Go 1.26.3 in the workflow is what failed the gate. Under
   `GOTOOLCHAIN=go1.27.1` the same command reports *No vulnerabilities
   found*, exit 0. **Decision: pin `1.27.1` exactly** (both workflows, all
   four occurrences) rather than the floating `stable` alias, because this
   repository pins GoReleaser `v2.18.2` and staticcheck `v0.8.1` for the same
   reason - a floating version turns "my build broke" into archaeology. The
   cost of pinning is that the pin must be *bumped on purpose*, which is why
   the workflow comment now says so and the bump is recorded here.
2. **`TestRelayCoordinator_LeaderSurvivesCallerDisconnect` fails only under
   `-race`, at ~55 s on Ubuntu and ~35 s on macOS.** The test closed
   `flowStarted` from a watcher goroutine launched *before* the `Do`
   goroutine, so `<-flowStarted` could return before `c.Do` had run at all;
   `cancel()` then won the race, `Do` returned at its `ctx.Err()` check
   (line 184) and never inserted into `c.active` (line 208), and the
   assertion saw `0 open relays, want 1`. Reproduced locally at 6 failures in
   25 `-race` runs. The signal now closes **inside the leader callback**, one
   statement after `call.register` — a point `Do` only reaches with the relay
   already registered — and the test then failed 0 times in 60 runs. This is
   a test defect, not a product defect: `Do` inserts before it invokes `fn`,
   so the ordering the callback establishes is airtight by construction.
3. **The dashboard harness exited 3 at ~8 s.** CI was still running the old
   harness, whose fixed 4-second sleep plus `fetch` added up to exactly the
   observed duration; on a runner where the Hub takes longer than 4 s to
   listen, the probe fails and every check reports `fetch failed`. The
   bounded readiness probe with captured hub logs (Batch Z21's successor,
   in `tools/uxtest/run.mjs`) is the fix and is in this working tree.

### Decisions

- **`dashboard-acceptance` keeps `continue-on-error: true` for now.** The
  harness has never once completed on that runner, so there is no evidence
  about Chrome-for-Testing there either way, and a job that fails for
  infrastructure reasons is a gate people learn to ignore. The criterion for
  flipping it is stated in the workflow comment: one observed green Linux run.
  The finding it exists to catch is *not* left unguarded in the meantime -
  `TestEveryDataActionHasAHandler` enforces the action-wiring half of the
  sweep on every push and cannot vary by environment.
- **Local race evidence stops being CI-exclusive.** Earlier entries in this
  record and in `docs/AUDIT.md` described `-race` as unavailable locally;
  both were corrected rather than left to stand, and the corrected wording
  names the condition (a C toolchain on `PATH`) instead of asserting a
  platform property.
- **No product code changed for CI.** The relay fix is confined to a test,
  the pin to workflow YAML. A red pipeline got exactly as much change as it
  needed, so the diff stays reviewable against the cause.

### Validation

- `GOTOOLCHAIN=go1.26.3 go run golang.org/x/vuln/cmd/govulncheck@latest ./...`
  → 9 findings, exit 3 (CI reproduced); `GOTOOLCHAIN=go1.27.1` → 0
  findings, exit 0 (CI fixed).
- `go test -count=1 -race ./...` with mingw gcc on PATH: all 13 packages
  pass. The relay test alone: 0 failures in 60 `-race` runs (6 in 25 before).
- `gofmt -l .`, `go build ./...`, `go vet ./...`, `staticcheck@v0.8.1`,
  `go run ./tools/encgate`, `go test -count=1 ./...`, coverage floor
  (`cmd/tantu` 19.5% against the floor of 18), and `node tools/uxtest/run.mjs`
  at 32/32 all green.

## Batch Z23 - what the annotations found: four tests that had never passed (2026-09-30)

### Why this batch exists

Batch Z22 fixed the three causes that were known about and stopped there, on
the assumption that the `-race` failures were the relay test. Run #20 proved
that wrong: Quality gates and Windows went green, Ubuntu and macOS did not,
and the failure was still only an exit code. The fix in this batch came first
and the diagnosis followed from it: **a failure that cannot be read cannot be
fixed**, so the test and harness steps now re-emit their output as check
annotations, which the unauthenticated API returns even though job logs do not.

Run #22 then named four failing tests, identical on Ubuntu and macOS. None of
them had ever passed in CI: `upload_failure_test.go` landed 2026-09-26 and
`publish_atomic_test.go` 2026-09-28, both *after* the last green run (#5,
2026-09-25). They had been red and unreadable ever since.

### The four, and what each one was actually about

1. **`TestFinalizeIncomingPartConsumesStagingName`,
   `TestFinalizeIncomingPartDoesNotOverwriteExistingFile`,
   `TestFinalizeIncomingPartRejectsDigestMismatch`** all failed in
   `publishIncomingFile`, which had two return values: `finalName`
   (`filepath.Base`) after a successful rename, `candidate` (full path) after a
   copy. The rename is the normal path on macOS and Linux; the copy is what
   Windows takes, because a file that is still open cannot be renamed there.
   So the function returned a bare file name on the platforms people use, and
   the digest check that guards the copy was skipped by the rename entirely.
2. **`TestWebDashboard_UploadReadOnlyOutputDir`** chmod'd the output
   directory and then posted a *text* drop. Text drops are buffered in memory
   and never touch the output directory, so the receiver had nothing to
   refuse: the test asserted 500 and got 200. Its second assertion — a
   terminal ledger state — could not have passed either, because
   `classifyRejection` reports receiver rejections as retry-safe and
   `classify_test.go` pins that.
3. The Hub had the same two defects as the CLI — `publishPartFile` returned a
   bare name on both paths, and verified the digest only where it copied —
   with **no failing test at all**, because every test that inspects an inbox
   item constructs it with an absolute `SavedPath` by hand.

The interesting one is the Hub's. A bare `saved_path` makes
`serveReceivedFile` resolve against the Hub's working directory, fail its
containment check, and 404 — correct behaviour for an escape attempt, wrong
for a normal receive. Inbox preview, the download link, and `tantu drop`'s
`/api/download` could not serve a file anyone had actually received, on any
platform, and nothing failed because the tests supplied paths the product
never produces.

### Decisions

- **Verify before publishing, not after.** The digest is now re-read through
  the receiver's own descriptor before either path can expose the delivered
  name. The alternative — rename first, hash, undo if it mismatches — puts
  unverified bytes under a delivered name for the length of that window,
  which is the property the atomic-publication work existed to prevent. The
  cost is one sequential read of bytes just written and no write, so peak
  disk stays at N; the comment on `PublishFromStaging` was reworded because
  "no I/O proportional to the payload" was no longer true of publication as a
  whole.
- **Fix the test, not the classifier.** A read-only output directory is
  indeed not worth retrying, but `classifyRejection`'s retry-safe default is
  pinned by `classify_test.go` and changing wire-visible classification for a
  string match ("permission denied") is a decision, not a correction. The test
  now asserts what the code promises — `receiver_rejected`, retry-safe, no
  duplicate risk, data safe, nothing published — and the classification
  question stays open rather than being settled silently inside a test fix.
- **Make the contract test runnable everywhere.** The permission-bit variant
  is Unix-only, so its assertions had never executed on a machine anyone
  could debug them on — the first rewrite of this batch inverted `retry_safe`
  and was only caught by the next CI run. A second variant blocks
  `.tantu-staging` with a regular file, which every platform refuses the same
  way, so the shared assertions run on Windows too and were verified locally
  before the push.
- **Add the test the product was missing, not only the one that failed.**
  `TestHub_ReceivedFileSavedPathIsAbsoluteAndServes` sends a real file through
  the loopback, then asserts the recorded path resolves and that
  `/api/drop/file` serves the bytes; `TestFinalizePartFileVerifiesDigestAndReturnsFullPath`
  covers the Hub's copy of the publication code, which was untested for both
  of these properties.

### Validation

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `staticcheck@v0.8.1`,
  `go run ./tools/encgate`, `go test -count=1 ./...`, and
  `go test -race -count=1 ./...` across all 13 packages, all on Go 1.27.1.
- `node tools/uxtest/run.mjs` 32/32, run once with a literal Chrome path and
  once with a glob, which is what CI uses.
- CI run #24 (2026-09-30, `cf7d6c7`) was the first fully green run since #5:
  Quality gates, `Test (ubuntu-latest)`, `Test (macos-latest)`,
  `Test (windows-latest)` and `Dashboard acceptance (browser)` all success.
  Runs #22 and #23 had already gone green for four of the five jobs, and #23's
  annotations are what identified the one assertion still wrong — which is
  also why `dashboard-acceptance` stopped carrying `continue-on-error`: its
  stated condition, one observed green Linux run, had been met three times.
- The annotation plumbing itself was tested in isolation against a failing
  run, a clean run, and a non-zero exit with no `--- FAIL` line — the shape
  where a bare `grep` under `set -eo pipefail` would abort the step and drop
  the detail it was written to produce.

## Batch Z24 — running the release pipeline instead of reading it (2026-09-30)

### Why this batch exists

With CI green end to end, the largest remaining "never executed" surface was
`.github/workflows/release.yml`: `git tag -l` is empty, so the release job has
never run once, while `docs/RELEASE.md` already describes install and upgrade
procedures for artifacts that do not exist. A release workflow that has never
run is a release workflow whose first run *is* the release. The only honest
way to close that was to execute the pipeline locally with the exact pinned
tools.

### What executing it found

1. **`release.yml` would have failed its first tag push.** GoReleaser's
   `sboms` step shells out to `syft` and the workflow never installs it:
   `goreleaser release` built all six binaries and archived all six, then
   exited 1 with `exec: "syft": executable file not found in %PATH%`. Every
   artifact had been produced and none of them would have been published.
2. **`goreleaser check` exits 2 on the config as committed**, because
   `archives.format` and `archives.format_overrides.format` are deprecated in
   v2 (`format` was replaced by `formats`). `release` only warns today, which
   is the interesting part: the gate fails *now* and the build fails at the
   next major, so the config was one version away from breaking silently.
3. **`docs/RELEASE.md` carried three stale claims** — Go `1.26.3` as the
   CI/release toolchain, `govulncheck` unobserved in CI, local Windows
   `-race` evidence limited to `-count=2` — each one documentation describing
   a repository that no longer exists.

### Decisions

- **Provision syft pinned and checksum-verified, not `curl | sh`.** The
  workflow downloads `syft_1.52.0_linux_amd64.tar.gz` plus its published
  `checksums.txt`, verifies with `sha256sum -c`, extracts to `~/.local/bin`
  and adds it to `GITHUB_PATH`. That step fetches and executes code from
  outside go.mod's pinned surface, so it gets the same treatment a release
  artifact would: a floating install script inside a release job is precisely
  the hole `docs/DEPENDENCIES.md` exists to prevent. The script was executed
  locally in git bash against the Linux asset — checksum `OK`, binary
  extracted, PATH line written — before it was committed, because a release
  step that has only ever been read is the same mistake as a release job that
  has only ever been read.
- **Migrate the config rather than silence the check.** `goreleaser check`
  exiting non-zero is a gate like any other; the fix is the property rename,
  not a skip.
- **Enforce tidiness in CI instead of repairing it at release time.** The
  config's before-hook runs `go mod tidy`, which would rewrite `go.mod` and
  `go.sum` during a tag build — releasing code that differs from the tag. The
  quality job now runs `go mod tidy` followed by `git diff --exit-code go.mod
  go.sum`, making tidiness a property of the commit. The hook is a no-op on a
  tidy tree (confirmed: the snapshot run left the tree clean).
- **Verify the artifacts, not just the exit code.** A pipeline that exits 0
  and produces an empty archive is still broken. The snapshot run was checked
  for all 6 archives, 6 SBOMs and `checksums.txt`; every one of the 12 hashes
  was re-computed against the file; the linux archive was listed for
  `LICENSE`, `README.md` and a single `tantu`; an SBOM was opened and shown to
  be SPDX-2.3 carrying `golang.org/x/crypto@v0.56.0`; and the built binary
  reported `v0.0.0-SNAPSHOT-05d7678`, proving the `-X main.version` ldflags.

### Still unproven

The publishing half: creating the GitHub release, attaching the artifacts and
fetching them back. That requires a tag, and pushing one tells users a version
exists — a product decision rather than an engineering one — so row 1.5 stays
open for exactly that, and no further.

### Validation

- `goreleaser check` → 0 (was 2) and `goreleaser release --snapshot --clean`
  → 0 (was 1), using GoReleaser v2.18.2 and syft 1.52.0, the versions the
  workflow pins.
- The syft install script executed verbatim in git bash against the Linux
  asset.
- `go build ./...`, `go vet ./...`, `gofmt -l .`, `staticcheck@v0.8.1`,
  `go run ./tools/encgate`, `go test -count=1 ./...` and
  `node tools/uxtest/run.mjs` at 32/32, green after the config and doc edits.
