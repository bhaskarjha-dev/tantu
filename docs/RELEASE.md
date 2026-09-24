# Tantu — Release, Install, Upgrade, Rollback

## Versioning

- Versions are `vMAJOR.MINOR.PATCH` git tags; goreleaser builds on tag push.
- The version is embedded at link time (`-X main.version` and
  `-X .../internal/hub.HubVersion`); `tantu version` and the dashboard
  report it. A local `go build` without ldflags reports the `1.0.0` default —
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
3. Run `tantu hub --headless` once (or `tantu` for the cockpit); identity and
   peer state are created under `~/.config/tantu/` (Linux/macOS) or
   `%APPDATA%\tantu\` (Windows) with 0600/0700 permissions.

## Upgrade

1. Stop the running Hub (Ctrl-C / SIGTERM, or exit the cockpit; the Hub runs
   in the foreground — there is no `hub stop` subcommand).
2. Replace the binary, keeping the previous one as `tantu.prev` until the new
   version is verified.
3. Start the new binary. `peers.json`/`identity.json` are forward-compatible
   (JSON; unknown fields are ignored), and a stale `hub.json` from the old
   process is taken over, not trusted blindly (PID/start-time probe).

## Rollback

Restore `tantu.prev` over the binary and restart. Peer/identity state written
by the newer version remains readable (same JSON schema, no migrations), so
rollback is a binary swap plus restart. In-flight transfers are not resumed
across a restart (DropIDs are random per attempt); re-send them. Stale
`.part` files are reclaimed automatically at startup (24 h sweep).

## Known release limitations

- No signed artifacts yet (see above).
- `-race` testing is not part of CI here (no C toolchain in some
  environments); concurrency is covered by `-count=2` reruns of the
  lifecycle-heavy packages plus stress-style unit tests.
- macOS/Linux binaries are compile-verified; runtime verification matrix is
  tracked in `docs/DEV-RECORD.md`.
