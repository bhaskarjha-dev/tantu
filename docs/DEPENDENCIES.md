# Dependency Surface

> **Purpose:** one place that states what Tantu depends on, why, and what it
> would cost to change. This document replaces a "Zero External Dependencies"
> claim in `AGENTS.md` that was not accurate.
>
> **Verified against:** `go.mod`, the import graph, and the published build
> matrix. Review when a dependency is added or removed.

## The current surface

Two direct module requirements. Both are `golang.org/x`, both pure Go, both
pinned. That is the whole list.

| Module | Version | Used for | Import sites |
|---|---|---|---|
| `golang.org/x/crypto` | v0.56.0 | SSH transport (client and test server) | `internal/transport/ssh.go`, `internal/testutil/sshserver.go`, `internal/bridge/e2e_ssh_test.go`, `internal/transport/ssh_test.go` |
| `golang.org/x/sys` | v0.47.0 | Windows hardlink count on staged partials | `cmd/tantu/staging_linkcount_windows.go`, `internal/hub/staging_linkcount_windows.go` |

`golang.org/x/net`, `golang.org/x/term`, and `golang.org/x/text` appear in
`go list -m all` as indirect requirements **of x/crypto's own go.mod**. They
contribute no packages to the Tantu build. The only x/crypto packages that
actually compile in are `ssh`, `ssh/knownhosts`, and their internals
(`chacha20`, `curve25519`, `poly1305`, `blowfish`, `cryptobyte`,
`bcrypt_pbkdf`).

Check it yourself:

```sh
go list -m all                      # 2 direct + 3 indirect-of-x/crypto
go list -deps ./cmd/tantu | grep golang.org/x
```

## What the accurate claim is

> **No C libraries, no runtime install.** Tantu builds to a single static
> binary with `CGO_ENABLED=0`, installable on a machine with no toolchain and
> no package manager. It depends on two pinned, pure-Go `golang.org/x` modules.

That is checkable. The previous wording — "zero external dependencies" — was
not, and was misleading in a specific and unhelpful direction.

## Why "zero" was the wrong framing

The count was never the property that mattered. Three things were:

1. **Install must not be a failure mode.** Tantu exists for machines where
   something else is already broken — a remote devbox with a dead SSH session
   and no permission to install packages. Every install step is a place the
   tool can fail before it helps. `CGO_ENABLED=0` and a static binary are the
   direct expression of that, and they are real.

2. **The surface must be enumerable.** Two modules, pinned, with every import
   site listed above, is auditable. That is a stronger and more useful
   property than a number.

3. **The crypto must not be ours.** The one place where writing something
   yourself would be catastrophic is cryptography, and the honest response is
   to *use* `x/crypto` rather than to avoid it. `x/crypto/ssh` is the largest
   and least-reviewed code in this build, and it is also the correct choice:
   a hand-rolled SSH implementation would be smaller and far more dangerous.

Calling the surface "zero" invited a reader to treat a **count** as a
security property — in a product whose entire job is holding someone's OAuth
tokens and private keys. It did not reduce the attack surface. It only made
the real surface harder to see, which is the opposite of what a trust
document should do.

## What it costs

Recorded here so the trade-off is explicit rather than folklore.

**A demonstrated cost: clipboard access is blocked on macOS.** Every mature Go
clipboard binding is cgo-backed. macOS pasteboard access essentially requires
AppKit bindings, which in Go means cgo, which breaks the static build. The
`purego` route (dlopen AppKit, no cgo) is possible but is a large, fragile
dependency — a poor trade against the product's stated values. Net result:
clipboard features would ship for Windows and Linux only, or would require
revisiting the `CGO_ENABLED=0` decision, which is a larger change than the
feature.

**A subtler cost:** a number-shaped rule invites satisfying the number rather
than reasoning about the dependency. "Don't add dependencies" can become a
reason to hand-roll something, or to decline a feature, when the honest answer
might be "this one is worth it."

## Conditions for adding a dependency

A new non-stdlib module requires a note in `docs/DEV-RECORD.md` stating:

1. **What it is used for**, and the exact import sites.
2. **Why the standard library genuinely cannot serve.** "It would be easier"
   is not sufficient.
3. **What it costs** — build size, platform coverage, review burden, licence,
   and whether it is cgo (which would break the static-build invariant).
4. **Why not to write it here**, if the answer is plausibly "we could."

Adding one is allowed. Adding one *silently* is what this document exists to
prevent.

## Vulnerability scanning

`govulncheck` is configured in CI (`.github/workflows/ci.yml`) and reports
vulnerabilities reachable from this module's own code. It has not yet been
observed executing in CI — tracked as `docs/KNOWN-LIMITATIONS.md` §1.6. Run
locally with:

```sh
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

The two current modules are Google-maintained, widely deployed, and
transitively depended on by a large fraction of the Go ecosystem, so they
receive attention proportional to that usage. That is a reason for
comfort, not a substitute for scanning.
