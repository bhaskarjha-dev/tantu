# Tantu — Release, Install, Upgrade, Rollback

## Versioning

- Versions are `vMAJOR.MINOR.PATCH` git tags; goreleaser builds on tag push.
- **The first tag will be `v0.1.0`.** Nothing in this repository creates tags
  — pushing one is a person's decision, and until it happens the publishing
  half of the pipeline (create the GitHub release, attach the artifacts, fetch
  them back) has never run. That is KNOWN-LIMITATIONS 1.5, and `v0.1.0` is
  the version meant to close it.
- Every tool in the pipeline is pinned, and none should be floated for a
  release: Go `1.27.1` in both workflows (a security decision as much as a
  reproducibility one — `govulncheck` judges the standard library against the
  toolchain that builds it, and 1.26.3 carried 9 reachable stdlib
  vulnerabilities), GoReleaser `v2.18.2`, syft `1.52.0`, staticcheck
  `v0.8.1`. `go.mod` declares language version `1.26.3`; that is the language
  the module requires, not the toolchain CI builds with.
- The version is embedded at link time (`-X main.version` and
  `-X .../internal/hub.HubVersion`); `tantu version` and the dashboard
  report it. A local `go build` without ldflags reports
  `1.0.0-dev+<short-sha>[.dirty]` (VCS fallback via `debug.ReadBuildInfo`) —
  that value means "unreleased dev build", not a real release.
- Envelopes carry an additive `v` field, stamped with `protocol.ProtocolVersion`
  (currently 1) at the encode choke point. Peers that predate versioning ignore
  it, so emitting it is compatible in both directions; see
  `docs/SPEC-WIRE-VERSIONING.md`. Unknown message types still fail closed
  (logged, connection dropped), and a beacon from a release that predates the
  identity-free beacon format is rejected outright rather than accepted with its
  SAS in hand.
- **Discovery beacons are not compatible with pre-`d7de281` releases.** Those
  broadcasts carried a SAS and certificate fingerprint; a current release drops
  any beacon that still sets those fields, so two machines on the same LAN will
  not discover each other unless they run the same release. Pairing itself is
  unaffected. This is a deliberate consequence of removing the cleartext SAS
  broadcast, and it is the one place the release boundary is a hard break rather
  than a graceful degradation.
- **`bridge_cancel` is additive: a current machine can talk to an older one, but
  a cancel will not be understood.** The sign-in path is unchanged, so a
  pre-cancel release still relays logins normally. What a current release cannot
  do is *stop* one: the older machine ignores the unknown type and holds its
  callback listener until its own timeout. The UI reports this honestly as "not
  confirmed" rather than as a failure, and says the other machine may release on
  its own. Given that mixed-version operation is unsupported, this only matters
  during the window where one machine has been upgraded and the other has not.
- Mixed-version operation is unsupported - upgrade both machines.

## Artifacts

Per release (see `.goreleaser.yaml`): 6 archives
(linux/darwin/windows × amd64/arm64, CGO_ENABLED=0), `checksums.txt`, and an
SBOM per archive. There is currently **no artifact signing** (no cosign
configuration — signing keys must never live in this repo). Until signing
lands, verify downloads against `checksums.txt` from the GitHub release page.

The pipeline is verified rather than assumed: on 2026-09-30 `goreleaser check`
and `goreleaser release --snapshot --clean` were run with the pinned
GoReleaser and syft, producing all 6 archives, 6 SPDX-2.3 SBOMs and
`checksums.txt` — every hash re-checked, the archives confirmed to hold the
single binary plus README and LICENSE, an SBOM inspected for its pinned
modules, and `tantu version` reporting the link-time-injected version. What
that run cannot exercise is the publishing half: creating the GitHub release
and fetching the artifacts back from it, which a tag push does.

## Supply chain

The full dependency inventory, the reasoning behind the `CGO_ENABLED=0`
static-build invariant, and the conditions for adding a module are in
`docs/DEPENDENCIES.md`. Summary: two pinned pure-Go `golang.org/x` modules
(`crypto` for SSH, `sys` for Windows hardlink counting), no C libraries, no
runtime install.

An SBOM ships with every archive. `govulncheck` runs in the quality job on
every push, judged against the pinned toolchain, and reports 0 findings as of
2026-09-30 (the runs from #24 on are green). Re-running it locally before a
release is still cheap:

```sh
GOTOOLCHAIN=go1.27.1 go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

## Install

1. Download the archive for your OS/arch and verify its checksum.
2. Extract the single `tantu` binary to a directory on PATH.
3. Run `tantu hub --headless` once (or `tantu` for the cockpit); identity,
   peer, and active-peer state are created under `~/.config/tantu/`
   (Linux/macOS) or `%APPDATA%\tantu\` (Windows) with 0600/0700 permissions.
   The headless banner prints a one-time dashboard URL; use `tantu dashboard`
   from another terminal to mint a fresh authenticated link.

## Upgrade

1. Stop the running Hub (Ctrl-C / SIGTERM, or exit the cockpit; the Hub runs
   in the foreground — there is no `hub stop` subcommand).
2. Replace the binary, keeping the previous one as `tantu.prev` until the new
   version is verified.
3. Start the new binary. `peers.json`/`identity.json`/`active.json`/`transfers.json`
   are forward-compatible (JSON; unknown fields are ignored), and a stale
   `hub.json` from the old process is taken over, not trusted blindly
   (PID/start-time probe).

## Rollback

Restore `tantu.prev` over the binary and restart. Peer/identity/transfer state
written by the newer version remains readable (same JSON schema, no
migrations; an older binary simply ignores `transfers.json`), so
rollback is a binary swap plus restart. Built-in one-shot transfers use a new
DropID per attempt and are not resumed across a restart; library callers that
deliberately reuse a DropID may resume a matching private partial. Stale
Tantu-owned private or manifest-marked `.part` files are reclaimed
automatically at startup (24 h sweep); unmarked direct legacy files are
preserved for manual review.

## Known release limitations

- No signed artifacts yet (see above).
- `-race` runs on Linux/macOS in CI, and on Windows locally once a C toolchain
  is on `PATH` (verified 2026-09-30 with mingw-w64 gcc 16.2.0: all 13
  packages green). On a stock Windows install without gcc the local evidence
  is repeated non-race runs (`-count=2`) plus CI's `-race` coverage elsewhere.
- Resume manifests bind metadata and a 64 KiB head hash, not the full payload
  or sender identity; completion after a lost acknowledgement is at-least-once.
- The shipped archive has never been executed on macOS or Linux: those OSes
  are verified by the full Go suite in CI on every push (`-race` on both, and
  the browser harness on Linux), not by running the artifact a user would
  download. The runtime verification matrix is tracked in
  `docs/DEV-RECORD.md`.
