# Tantu — Development Rules

## Always-Active Rules

- Only modify files that are relevant to the current task
- Read actual files before editing — never code from memory
- Run `go build ./...` and `go test ./...` after making changes
- Never hardcode API keys or credentials — use environment variables
- Keep all existing tests passing when modifying code
- **Verify a claim rather than asserting it.** Run the tool (`node
  tools/uxtest/run.mjs`, `goreleaser release --snapshot`, `go run ./tools/encgate`,
  `go run ./tools/docgate`), not just the code that claims to work. Several
  defects in this repository's history — a CSP-blocked image, a missing release
  tool, an unusable encoding gate, a cosign flag that silently does nothing —
  were invisible to reading and obvious to running.
- **Run the gates CI runs, not a subset you remember.** The quality job is
  `gofmt -l .`, `go vet ./...`,
  `go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...`, `go test`, `encgate`,
  `docgate`. On 2026-10-06 six commits were pushed on a report that every gate
  was green, and CI was red on the one gate that had never been run locally:
  staticcheck found a watcher function that nothing called, while both documents
  describing it claimed it ran. Read `.github/workflows/ci.yml` for the list
  rather than trusting this summary of it.
- **A documented claim about behaviour is a claim that something runs.** A
  function that exists, a comment that explains why it matters, and a document
  that describes its output are not evidence that any of them are connected.
  `watchDiscoveryHealth` had all three and was never called; every gate passed
  because each tested the pure decision function it calls. When a document says
  the product does something, the gate has to exercise the thing that does it,
  not just the part of it that is easy to reach.
- **A new gate must be able to fail.** Every checker here is exercised against
  deliberately broken input, and refuses to report success when its own inputs
  are missing or unparseable. A check that always passes is worse than no
  check.

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
internal/discovery/ Zero-config LAN peer discovery (mDNS + broadcast) + health
internal/drop/      QuickDrop file/text transfer (chunked, resumable, SHA-256)
internal/hub/       Symmetric Hub lifecycle, Web Dashboard, IPC, Cockpit
internal/pairing/   Cryptographic identity, SAS verification, peer store
internal/protocol/  Wire protocol envelope encoding/decoding
internal/testutil/  Test helpers (SSH server, OAuth mocks)
internal/transport/ Pluggable transport layer (LAN/mTLS, SSH, loopback)
tools/encgate/      Gate: no encoding damage in tracked text files
tools/docgate/      Gate: documented claims still match the code
tools/uxtest/       Browser acceptance harness (Playwright, 3 engines)
```
