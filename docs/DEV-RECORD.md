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

Severity: P0 critical/exploitable remotely · P1 exploitable by paired/local attacker or data-loss · P2 hardening/correctness with realistic trigger · P3 minor/compat/doc.

## Decisions and assumptions

- D-01: Portable Go 1.26.3 pinned to match go.mod (not latest 1.27) for reproducible builds.
- D-02: `DialPinned` strictness errors only when expectedFP != "" AND transport lacks pinning — all current callers pass "" for ssh/loopback (verified send/relay/open/drop/hub), so behavior-preserving + fail-closed for future misuse.
- D-03: unpair `--fingerprint` minimum 6 chars aligns with `ResolvePeer` Tier-1 prefix rule (store.go:432); `--name` widened to Alias + case-insensitive to match ResolvePeer tiers 3-4.
- D-04: B-side header filter mirrors A-side `callbackHeaders` allowlist (Accept, Accept-Language, Content-Type, X-Requested-With) — single source `allowedCallbackRelayHeader` in session.go; Content-Length/Host/Cookie/Authorization can never be injected.
- D-05: Standalone relay/drop token issues (I-16, I-17) left OPEN pending design decision: migrating standalone servers to one-time tickets is a breaking workflow change; documenting + scoping first, fix next cycle if evidence supports.
- D-06: No commits pushed (per mandate: never push without explicit request). Local commits only.

## Validation matrix

- [x] go build ./... (baseline + after each batch)
- [x] go vet ./... (baseline + batch A)
- [x] go test -count=1 ./... (baseline + batch A, all pass)
- [ ] go test -race ./... — BLOCKED: no C compiler in environment
  (`-race requires cgo`), no gcc/cc/clang. Mitigation: -count=2 rerun of
  concurrency-heavy packages (pending). Never claim race safety.
- [ ] cross-compile GOOS=linux/darwin (pending)
- [x] new regression tests per fix (batch A: headers, staging, codec,
  SAS ambiguity, cert binding, address validation, perm repair (Unix),
  beacon validation, transport deadline fixture)
- [ ] release artifact smoke test (pending, end of engagement)

## Open questions

- Q-01: Should standalone `serve`/`relay`/`node` commands be retired in favor of Hub delegation, or migrated to one-time tickets? (I-16, I-17)
- Q-02: winget Go 1.27 install may have completed in background — verify it didn't mutate system PATH unexpectedly.

## Next actions

1. [x] Implement Batch A fixes (I-01..I-15) with regression tests.
2. [x] Run build/vet/full tests (all green; race blocked, see above).
3. [x] Triage Batch B (I-16..I-18, I-25, I-26): standalone token model, upload cap, docs.
4. [x] Cross-platform compile checks (linux/amd64, darwin/arm64 OK).
5. [x] Commit Batch A locally (1361d60, no push).
6. Adversarial re-review pass + fresh discovery (second-order effects of Batch A/B).
7. Release artifact build + smoke test.
8. Final report.
