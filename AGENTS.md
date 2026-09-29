# Tantu — Development Rules

## Always-Active Rules

- Only modify files that are relevant to the current task
- Read actual files before editing — never code from memory
- Run `go build ./...` and `go test ./...` after making changes
- Never hardcode API keys or credentials — use environment variables
- Keep all existing tests passing when modifying code

## Architecture Invariants

- **Symmetric Hub** — both machines run identical code; no server/client distinction
- **Dual-Socket Isolation** — Web UI on `127.0.0.1:9876` (loopback only); wire traffic on `:9877` (encrypted only)
- **Pluggable Transport** — all transport implementations conform to `transport.Transport` interface
- **No C libraries, no runtime install** — `CGO_ENABLED=0`; a build is one
  static binary that runs on a machine with no toolchain and no package
  manager. This is a genuine invariant because it maps directly onto the
  product promise: Tantu exists for machines where something else is already
  broken, so every install step is a place the tool can fail.
- **Pinned, enumerable dependency surface** — every non-stdlib import is
  listed in `go.mod`, pinned, and justified in `docs/DEPENDENCIES.md`.

  This replaces an earlier "Zero External Dependencies" invariant, which was
  not accurate: the module requires `golang.org/x/crypto` (SSH) and
  `golang.org/x/sys` (Windows hardlink counting). The old wording was worse
  than the facts in one specific way — it invited a reader to treat a
  *count* as a security property, in a product whose whole job is holding
  someone's credentials. `x/crypto/ssh` is the largest and least-reviewed
  code in the build; calling the surface "zero" did not reduce that, it only
  made it harder to see. See `docs/DEPENDENCIES.md` for the current inventory,
  the reasoning, and the conditions for adding one.

## Code Conventions

- Standard Go project layout (`cmd/`, `internal/`)
- Standard `testing` package only — no external test frameworks
- All wire messages are `protocol.Envelope` types with explicit type discrimination
- Config directory: `~/.config/tantu/` (Linux/macOS) or `%APPDATA%\tantu\` (Windows)

## Package Structure

```
cmd/tantu/          CLI entrypoint and subcommand handlers
internal/bridge/    OAuth callback dispatcher (multiplexed)
internal/browser/   Cross-platform URL launcher with sanitization
internal/discovery/ Zero-config LAN peer discovery (mDNS + broadcast)
internal/drop/      QuickDrop file/text transfer (chunked, resumable, SHA-256)
internal/hub/       Symmetric Hub lifecycle, Web Dashboard, IPC, Cockpit
internal/pairing/   Cryptographic identity, SAS verification, peer store
internal/protocol/  Wire protocol envelope encoding/decoding
internal/testutil/  Test helpers (SSH server, OAuth mocks)
internal/transport/ Pluggable transport layer (LAN/mTLS, SSH, loopback)
```
