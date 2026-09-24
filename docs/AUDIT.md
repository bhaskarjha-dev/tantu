# Tantu Project Audit

**Audit date:** 2026-09-24
**Scope:** all Go packages, CLI commands, embedded web surfaces, protocol framing, tests, CI/release configuration, and the incident report in `temp/repeat-bug.md`.

## Executive assessment

Tantu has a useful set of primitives — symmetric nodes, certificate pinning, a framed protocol, resumable transfers, and a local dashboard — but the implementation did not initially uphold several guarantees claimed by the README, architecture document, and threat model. The most serious risks were in the privileged local control plane and lifecycle code, not in the basic idea of the product.

The highest-impact issues found were:

1. OAuth retries were not idempotent. Every retry created another callback listener and browser launch, producing the repeat/port-collision storm recorded in `temp/repeat-bug.md`.
2. OAuth callback URLs and query values were written to terminal output and the event ring buffer. The incident report contains values that look like real authorization codes; those values must be treated as compromised and revoked where applicable.
3. The dashboard and standalone UIs allowed untrusted data to reach inline JavaScript or insufficiently protected local control endpoints, creating a path into a loopback origin that can control peers, files, pairing, and browser actions.
4. The legacy relay was a state-changing GET with wildcard CORS and no CSRF capability.
5. LAN accept loops could be stopped or starved by rejected or slow TLS handshakes; blocked receives ignored context cancellation.
6. The Hub disabled its advertised 5 GiB quota, had restart/shutdown races, and could publish unrelated files during finalization. Its runtime handoff and listener lifecycle also lacked generation and lease checks.
7. SSH server mode did not require an independent client credential, and SSH clients did not verify host keys by default.
8. Pairing accepted a certificate from JSON without binding it to the authenticated TLS peer; web pairing could auto-accept without a user comparing SAS values.

## Incident: repeated OAuth browser launches

### Root cause

`HandleBSide` generated a fresh request ID for every invocation. `HandleASide` treated every incoming `bridge_request` as a new session, bound the requested callback port again, and called `OpenBrowser` again. A retry therefore had no relationship to the original logical authorization transaction. The only correlation fields (`RequestID`) were not used by the retry path, and neither side consistently validated them.

The callback listener also accepted any path and the callback logger recorded the complete request URI. Once a listener was occupied, later retries generated the exact “callback port is in use” errors visible in the incident report. Context deadlines did not interrupt blocked `Receive` calls, so failed sessions could remain active for the full timeout.

### Remediation implemented

- Added a shared `OAuthSessionManager` for each dispatcher/listener. It keys a logical request by authenticated peer, a stable authorization-flow identity, and the callback destination when no stable flow identity is available.
- Concurrent retries wait for the original result instead of opening another page. Completed retries receive `BridgeAck.Replay` and return success without another callback flow. Successful results are replayable for a bounded window and the replay table is capped.
- Kept meaningful authorization parameters in the flow identity while excluding changing `state`, PKCE challenges, nonces, and loopback callback ports. An explicit application `FlowID` is also bound to the authorization URL so it cannot coalesce unrelated requests.
- Added Hub-level single-flight/replay coordination so duplicate `/api/relay/open` and `/relay` requests do not create duplicate wire sessions. Resolved peer fingerprints are part of the key.
- Added request-ID validation for acknowledgements, relays, and completions, plus URL/FlowID size limits.
- Added context-aware receive helpers and a real session deadline to the bridge and QuickDrop paths.
- Added a one-time fragment bootstrap and HttpOnly dashboard session; sensitive GETs, downloads, SSE, and mutations now require a session or IPC capability. Runtime relay bookmarks use one-use tickets rather than an HTML-embedded reusable token.
- Validated callback path/state for loopback redirects and supported external trampoline state URLs. B-side independently validates the relayed path/state before delivery.
- Disabled redirect following on the B-side and never automatically replays GET or POST OAuth callbacks: a lost response must become a new user-visible flow rather than a second delivery of a one-time code.
- Callback logs and event metadata redact query values. Browser-launch logs contain only a redacted URL, and bridge errors crossing standalone logger boundaries are redacted as well.
- Callback listener shutdown now observes session cancellation and force-closes active handlers after a bounded graceful shutdown.
- Hub lifecycle state is generation-safe: readiness is published only after an HTTP health probe, restart cannot overlap shutdown, SSE/upload handlers are force-closed, and runtime metadata is published only after readiness.
- Runtime discovery probes now require the per-process capability and match PID/start time; runtime files use bounded reads, a cross-process lock, live-owner checks, and ownership-safe removal.
- Dynamic-port pairing advertises the actual listener port, rejected web pairing returns an explicit rejection, and pairing decisions are single-terminal-state operations.

A regression test covers simultaneous duplicate connections, a completed retry, a later retry, stable FlowID normalization, meaningful-parameter separation, and a single browser launch.

## Findings and disposition

### Critical / high priority

| ID | Finding | Disposition |
|---|---|---|
| SEC-01 | OAuth callback query values, including apparent authorization codes, were logged and retained. | Fixed in bridge/Hub logging boundaries; incident artifact must be handled as a secret. |
| SEC-02 | Duplicate OAuth requests caused repeated browser launches and callback-port collisions. | Fixed with session managers, replay acknowledgements, Hub/standalone single-flight, bounded replay state, and context cancellation. |
| SEC-03 | Untrusted text and peer/discovery metadata reached inline JavaScript and `innerHTML`. | Dynamic Hub and standalone handlers now use context-aware encoding/text nodes; remaining CSP work is tracked below. |
| SEC-04 | `/relay` was a state-changing GET with wildcard CORS. | Wildcard CORS removed; actual relay requests require an IPC capability, a one-use ticket, or the explicitly retained legacy capability. |
| SEC-05 | TLS/SSH handshake failures could terminate a dispatcher or block accepts. | Transport handshakes now have deadlines, candidate tracking, classification, and continue-on-reject behavior. LAN handshakes are concurrent and bounded; fatal listener errors are returned and close drains queued connections. |
| SEC-06 | Context timeouts did not interrupt blocked bridge/drop receives. | Bridge, pairing, QuickDrop, LAN, and SSH paths now use deadlines or context-triggered connection closure. |
| SEC-07 | SSH server mode accepted unauthenticated clients; clients ignored host keys. | SSH servers require an independent authorized key by default; legacy host-key authorization is explicit. Clients use known-hosts or an explicit pin by default. CLI SSH UX and multi-key deployment remain release work. |
| SEC-08 | Pairing JSON certificate was not bound to the TLS peer certificate. | In-band and legacy pairing compare the payload fingerprint with the authenticated TLS leaf when available. |
| SEC-09 | Web pairing could accept without a human SAS comparison. | Both outbound and inbound web handshakes require approval by default; `AutoAcceptPairing` is an explicit compatibility opt-in. Rejection is reported as rejection, and dynamic listener ports are preserved. |
| DO-01 | Hub configured unlimited QuickDrop sizes despite a documented 5 GiB limit. | Hub, standalone receive, node, and Web UI paths enforce file/text limits and bounded histories/sessions. |
| DO-02 | File finalization removed an existing destination before rename and could report false success. | Receiver paths publish from verified open descriptors through root-anchored, no-overwrite exclusive copies before acknowledging success; source cleanup retains its manifest on failure. Retries after a lost success acknowledgement remain at-least-once and may require duplicate cleanup. |
| FS-01 | `.part` resumption was keyed only by filename and could combine unrelated files. | Drop IDs are constrained to safe identifiers, valid private manifests bind metadata and a 64 KiB head hash, mismatches fail closed, and Tantu-owned retained partials are bounded by a separate disk budget. Unmarked direct legacy files are preserved for manual review. |
| LIFE-01 | `Stop` could leave `Start` blocked; restarting a Hub could panic on a closed readiness channel. | Hub now owns run cancellation, resets readiness on subsequent starts, bounds shutdown, and removes runtime metadata only when owned. |
| IPC-01 | `hub.json` was trusted without ownership validation and delegated HTTP clients had no timeout. | Runtime metadata is private/atomic/ownership-checked and cross-process locked; authenticated probes match the recorded PID/start time; loopback authorities/origins are exact; per-process IPC tokens protect reads and mutations, with a one-time browser session for the UI. OS-specific ACLs remain. |

### Medium priority

- Protocol frames are capped at 4 MiB, and control frames have a 64 KiB typed limit; aggregate transfer reservations are bounded per receiver process and per peer, Tantu-owned failed partials are bounded by a separate retained-disk budget, verified staging roots and activity/process locks protect active partials across cooperating Tantu processes, and malformed-frame concurrency is bounded by the dispatcher/session caps. Unmarked direct legacy `.part` files are intentionally preserved for manual review.
- `Envelope` schemas still need stricter duplicate-field validation and an explicit version-negotiation design for mixed releases.
- Peer-store and identity writes are unique-temp/atomic, private, and serialized across processes; Windows ACL enforcement and crash-recovery verification remain platform work.
- Active peer selection is persisted in `active.json` and validated against the live peer store; default/active routing now survives a controlled Hub restart.
- Discovery beacons are unauthenticated hints. Version/port/identity fields, node-table caps, source rate limiting, and roaming cooldown/probing are implemented; discovery must never be treated as authorization.
- SSE now replays ring-buffer events after a browser reconnect using `Last-Event-ID`; slow clients can still miss events while connected, and delivery remains bounded to 32 subscribers with non-blocking broadcasts.
- The Hub dashboard now uses a per-response CSP nonce and delegated DOM events instead of inline JavaScript handlers; inline CSS and the legacy standalone pages remain compatibility-scoped follow-up work.
- Standalone relay/drop servers now enforce loopback/origin checks, bounded bodies/history/sessions, and a per-process mutation token. Browser-level checks cover the Hub session/recovery flow; CSP and token migration for legacy pages remain follow-up work.
- SSH known-host bootstrap, multiple authorized-key files, and independent Hub outbound SSH client configuration still need a deliberate UX/configuration design.
- Private resume manifests, per-process aggregate transfer quotas, root-anchored staging, and explicit browser-level security tests are implemented for the Hub and standalone receivers. Full-content/sender identity proof, power-loss guarantees, at-least-once completion idempotency, cross-process quota coordination, and mixed-version interoperability remain open.

## Test and tooling assessment

The original suite was primarily happy-path testing. It had useful coverage for normal OAuth, file transfer, resumption, LAN/SSH/loopback transports, pairing, and the dashboard, but little adversarial or lifecycle coverage.

Added regression coverage includes:

- duplicate OAuth retry coalescing, bounded replay, stable FlowID normalization, meaningful-parameter separation, callback path/state validation, form-post handling, redacted errors/logs, and no automatic callback replay;
- malformed/oversized/short protocol frames and blocked receive cancellation;
- concurrent/bounded LAN handshakes, fatal-accept propagation, queued-connection close/drain, pinned LAN dials, SSH banner-exchange deadlines, authentication, and host-key mismatch;
- certificate/JSON pairing mismatch and two-sided web pairing approval;
- invalid/hostile DropID, kind, name, size, and chunk metadata; short writes; finalization failure; no-overwrite publication; unsafe filename handling; duplicate-DropID ownership; root-anchored staging replacement; hardlink checks; invalid-manifest preservation; and orphan activity/temp cleanup;
- Hub start/stop/restart, generation-safe readiness, active SSE shutdown, runtime ownership/live leases, authenticated PID/start-time probes, exact-origin/authority checks, one-time dashboard sessions, protected GETs, relay tickets, dynamic ports, and malicious/stale `hub.json`;
- hostile text/peer/discovery strings in browser DOM contexts, standalone mutation-token checks, bounded histories, and relay single-flight cancellation.

Recommended local commands:

```text
go version
gofmt -w <changed files>
go build ./...
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
```

The local Windows toolchain cannot run the race runtime (`-race` requires a complete cgo installation); CI runs race tests on Linux/macOS, while Windows uses repeated non-race tests. The release job pins Go and GoReleaser versions rather than using floating `latest` values.

## Release recommendation

Do not describe the current build as “MITM-proof,” “zero external exposure,” or “strictly 5 GiB” until the remaining platform/interoperability items are closed. The repeat-OAuth regression, callback redaction/correlation, authenticated local control plane, runtime ownership/lease checks, generation-safe lifecycle, dynamic-port pairing, rejected-pairing reporting, pinned LAN dials, explicit SSH authentication, fatal-listener handling, pre-ack file publication, durable partial manifests, aggregate transfer quotas, cross-process peer/identity locks, headless dashboard recovery, Hub script CSP, and SSE reconnect replay are fixed and tested. Remaining release blockers for untrusted-network use are complete SSH deployment UX, OS ACL/PID hardening, mixed-version probe compatibility, strict duplicate-field schema validation, and formal acceptance or migration of legacy inline-script pages; SSE slow-client loss remains a documented bounded-buffer tradeoff rather than a replay gap.
