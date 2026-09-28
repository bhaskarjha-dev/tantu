# Wire Versioning & Idempotent Retry — Design Proposal

> **Status:** Phases 1–2 implemented (emit + validate + receiver tombstone);
> phase 3 (idempotent retry UX) partially addressed: `transfer retry` still
> refuses blind retry but now teaches duplicate-safe key-reuse, since the Hub
> keeps no payload to resend.
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

## 5. Open decisions (resolved 2026-09-25; recommendations adopted)

1. Tombstone durability: persisted beside transfer history (atomic/0600) —
   crash recovery is the point. Memory-only stays available via an empty
   path for tests and short-lived receivers.
2. Retention: 24 h to match the partial sweep.
3. Mixed-version support window: same-release-only stays the supported
   topology; versioning improves error quality.

## 6. Rollout (each phase independently testable)

1. Emit `v: 1` + `idempotency_key`; receivers ignore. Compat matrix:
   new↔new, new↔old, old↔new transfer tests.
   **Implemented 2026-09-25:** envelope `V` stamped at the encode choke
   point (`protocol.ProtocolVersion`), `idempotency_key` emitted by Hub
   uploads (key = operation ID) and direct CLI sends (key = DropID),
   receiver-side alphabet/length validation with fail-closed rejection
   (refactored to the shared `drop.ValidTombstoneKey` helper).
2. Tombstone + re-ack path with duplicate-DropID ownership tests extended
   to same-key redelivery (no second publication, identical digest).
   **Implemented 2026-09-25** (`internal/drop/tombstone.go`): bounded LRU
   1024, 24 h retention, durable file, fail-closed mismatch; same-key
   redelivery streams to discard, verifies the digest, and re-acknowledges
   without staging, publishing, or duplicate inbox/history.
3. Idempotent retry UX: `transfer retry <id>` allowed only for tombstoned
   operations; everything else keeps the current refusal. Duplicate-risk
   state remains for the legacy path.
   **Partially addressed 2026-09-25:** retry stays refused (the Hub keeps
   no payload to resend) but now teaches duplicate-safe key-reuse with
   `--idempotency-key` instead of only refusing.

### 3a. A new message type, and the one place it is not merely additive
   **Added 2026-09-28:** `bridge_cancel` is a new message type, not a new
   field, and it is the first case where "old peers ignore it" is not
   sufficient. The sign-in path is untouched, so a pre-cancel peer still
   relays logins; what it cannot do is *stop* one. Because the type is
   unknown to it, it fails closed and keeps its callback listener until its
   own timeout, and the cancelling side cannot distinguish that from a slow
   peer.

   Two decisions follow, and both are deliberate:

   - The reply reuses `bridge_cancel` rather than introducing an ack type.
     A cancelling client always gets a frame it can parse; a peer that does
     not know the type times out on a small bound instead of mis-reading an
     unrelated payload.
   - An unconfirmed cancel is reported as **unconfirmed**, not as a failure.
     The sign-in may well have completed or timed out between the user
     deciding and the message arriving, and a hard error would tell the user
     their cancel failed when the thing they wanted stopped has already
     stopped. The UI names the residual honestly: the other machine may
     release on its own.

   This is the same-release-only topology (5.3) showing through an additive
   change. It is documented in `KNOWN-LIMITATIONS.md` 3.11 rather than papered
   over, because the alternative - silently reporting success - is the failure
   this whole mechanism was added to remove.

## 7. Risks

Replay within the trust boundary (paired peers only; tombstone re-ack
returns the recorded digest, never new bytes) · disk (bounded, budgeted) ·
old-peer interop (additive; fail-closed with named errors, never silent).
