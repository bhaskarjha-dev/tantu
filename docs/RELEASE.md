# Tantu — Release, Install, Upgrade, Rollback

## Versioning

- Versions are `vMAJOR.MINOR.PATCH` git tags; goreleaser builds on tag push.
- CI/release use Go `1.26.3` (matching `go.mod`) and GoReleaser `v2.18.2`; do not
  float either tool version for a release.
- The version is embedded at link time (`-X main.version` and
  `-X .../internal/hub.HubVersion`); `tantu version` and the dashboard
  report it. A local `go build` without ldflags reports
  `1.0.0-dev+<short-sha>[.dirty]` (VCS fallback via `debug.ReadBuildInfo`) —
  that value means "unreleased dev build", not a real release.
- Wire protocol has no version field: pairing assumes both machines run the
  same release. Unknown message types fail closed (logged, connection
  dropped), and beacons carry `v: 1` (mismatches ignored). Mixed-version
  operation is unsupported — upgrade both machines.

## Artifacts

Per release (see `.goreleaser.yaml`): 6 archives
(linux/darwin/windows × amd64/arm64, CGO_ENABLED=0), `checksums.txt`, and an
SBOM per archive. There is currently **no artifact signing** (no cosign
configuration — signing keys must never live in this repo). Until signing
lands, verify downloads against `checksums.txt` from the GitHub release page.

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
- `-race` runs on Linux/macOS in CI; local Windows race execution depends on a
  complete C toolchain, so local evidence uses `-count=2` lifecycle reruns
  rather than claiming race safety.
- Resume manifests bind metadata and a 64 KiB head hash, not the full payload
  or sender identity; completion after a lost acknowledgement is at-least-once.
- macOS/Linux binaries are compile-verified; runtime verification matrix is
  tracked in `docs/DEV-RECORD.md`.
