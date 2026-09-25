# Wire Versioning & Idempotent Retry — Design Proposal

> **Status:** Phase 1 implemented (emit + validate); phases 2–3 pending
> design decisions below. Until the tombstone lands, retry-after-unknown-
> outcome stays at-least-once with explicit duplicate-risk UX; `transfer
> retry` stays refused.
> **Date:** 2026-09-25

## 1. Problem

The wire protocol has no version field: pairing assumes both machines run
the same release, beacons carry `v: 1` (mismatches ignored), and unknown
message types fail closed (logged, connection dropped). Consequences:

- A retried transfer cannot be distinguished from a new one, so a lost
  `drop_complete` forces the honest but annoying duplicate-risk state.
- Any protocol extension risks a flag day: old peers fail closed with no
  actionable message.

## 2. Goals / non-goals

Goals: additive-only changes old peers ignore; a receiver-side completion
tombstone enabling at-most-once publication; feature gating by presence;
clear mixed-version errors. Non-goals: WAN transport, a general capability
framework, changing the same-release supported topology.

## 3. Proposal

1. **Envelope `v` (int, omit-empty).** Absent means 0 (current peers).
   `encoding/json` ignores unknown fields on decode, and no validator
   rejects them, so emitting `v: 1` is backward compatible in both
   directions: old peers ignore it, new peers read it.
2. **`idempotency_key` in `drop_send`.** Sender-generated, unique per
   logical operation (reuse the operation-ID generator semantics). Old
   receivers ignore it (legacy at-least-once path, current UX preserved).
3. **Receiver completion tombstone.** Bounded LRU (1,024 entries) mapping
   idempotency key → `{sha256, bytes, published_name, at}`. On `drop_send`
   with a known key and matching metadata: skip staging/publication and
   re-acknowledge success with the recorded digest. Same key with
   mismatched metadata: reject (fail closed, never mix bytes). Unknown key:
   normal path. Tombstone durability is the open decision (§5).
4. **Version-skew errors.** When a feature requires a peer version the other
   side lacks (e.g. idempotent retry against a v0 receiver), fail fast with
   a named code (`incompatible_peer`) plus the existing upgrade guidance,
   reusing the CLI/Hub skew warnings.

## 4. Why this shape

- Additive fields preserve the deliberate no-allowlist decoding rule
  (forward compat documented in ARCHITECTURE): every future message type
  would otherwise be a wire break.
- Tombstone on the receiver (not the sender) is the only side that knows
  whether publication happened — the exact uncertainty duplicate-risk
  reports today.
- Bounding (count + age) mirrors existing budgets (quotas, 50-op ledgers,
  8 GiB partial budget) instead of inventing unbounded state.

## 5. Open decisions

1. Tombstone durability: memory-only (lost on restart, like SSE buffer) vs
   persisted beside `transfers.json`. Recommendation: persisted, same
   atomic/0600 pattern — crash recovery is the point.
2. Retention: 24 h to match the partial sweep, or 30 d to match history?
   Recommendation: 24 h; idempotency windows longer than a day invite
   surprising re-acks.
3. Mixed-version support window: same-release-only stays, or N/N-1
   best-effort after this lands? Recommendation: keep same-release as the
   supported topology; versioning only improves error quality.

## 6. Rollout (each phase independently testable)

1. Emit `v: 1` + `idempotency_key`; receivers ignore. Compat matrix:
   new↔new, new↔old, old↔new transfer tests.
   **Implemented 2026-09-25:** envelope `V` stamped at the encode choke
   point (`protocol.ProtocolVersion`), `idempotency_key` emitted by Hub
   uploads (key = operation ID) and direct CLI sends (key = DropID),
   receiver-side alphabet/length validation with fail-closed rejection.
   Tombstone lookup explicitly not yet performed.
2. Tombstone + re-ack path with duplicate-DropID ownership tests extended
   to same-key redelivery (no second publication, identical digest).
3. Idempotent retry UX: `transfer retry <id>` allowed only for tombstoned
   operations; everything else keeps the current refusal. Duplicate-risk
   state remains for the legacy path.

## 7. Risks

Replay within the trust boundary (paired peers only; tombstone re-ack
returns the recorded digest, never new bytes) · disk (bounded, budgeted) ·
old-peer interop (additive; fail-closed with named errors, never silent).
