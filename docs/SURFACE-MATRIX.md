# Surface Migration Matrix

> **Status:** living matrix. **Last reviewed:** 2026-09-27.
> **Purpose:** the plan (v6.0 §2.1, §9.7) requires that no retained surface
> teaches a second, contradictory product model. This file is where each
> surface's audience, canonical status, and relationship to the Hub is written
> down, so "no surface remains undocumented" (Phase 0 exit criterion) is
> checkable rather than asserted.

The Hub dashboard is the primary normal-user surface (D-01). CLI and cockpit
are first-class power surfaces (D-02). Everything else is compatibility or
advanced (D-03), and none of it may diverge in vocabulary or destination
semantics.

---

## Retained surfaces

| Surface | Audience | Canonical status | Data model | Retry semantics | Auth model | Migration intent |
|---|---|---|---|---|---|---|
| `tantu hub` (dashboard) | Normal | **Primary** | Canonical | Canonical | Dashboard session / IPC | Retain; the reference implementation |
| Cockpit | Power | First-class adapter | Canonical | Must match | Local IPC | Retain; print the resolved destination before transmitting |
| `tantu send` | Power | First-class adapter | Canonical | Must match | Delegated to Hub | Retain; destination and operation ID always named |
| `tantu receive` | Normal/advanced | Advanced | Must converge | Must match | Wire | Retain; progress and recovery language is thinner than the dashboard's |
| `tantu open` | Power | First-class adapter | Canonical | Must match | Delegated | Retain |
| `tantu status` / `doctor` | Power + first-run | **First-class diagnostic** | Canonical | n/a | Local | Retain; `status` answers state, `doctor` diagnoses and proposes repair |
| `tantu transfer(s)` | Power | First-class adapter | Canonical (metadata only) | Must match | Delegated | Retain |
| `tantu pair` / `unpair` | Power | First-class adapter | Canonical | n/a | Local | Retain; trust changes are explicit |
| `tantu wrap` | Power | First-class adapter | Canonical | n/a | Local | Retain |

## Compatibility / advanced surfaces

These are retained, labelled, and pointed at the Hub. They are **not** the
normal path and must not become one silently.

| Surface | Audience | Canonical status | Relationship to the Hub | Label printed |
|---|---|---|---|---|
| `tantu drop` | Legacy / web | Compatibility | Web sender with a legacy token; relays through the Hub | Hub pointer + tip |
| `tantu relay` | Advanced | Compatibility | Legacy ticket listener; relays through the Hub | Hub pointer + tip |
| `tantu node` | Advanced | Advanced | Combined receiver, more technical than the normal path | Advanced/compatibility label |
| `tantu serve` | Advanced | Compatibility | Specialized compatibility listener, different terminology | Compatibility label |

## Rules that keep the surfaces converged

1. **One vocabulary.** Peer trust state, transfer state, and authorization
   state use the same user language everywhere. The mapping lives in the Hub
   (`hub.ClassifyTransferError`) and the dashboard, not restated per surface.
2. **No silent destination choice.** A surface with more than one peer either
   resolves unambiguously or errors. Empty queries with multiple peers and no
   default are an error, never "take the first".
3. **No contradictory retry semantics.** A lost acknowledgement is reported as
   possible duplicate risk on every surface; no surface offers a blind retry.
4. **Version skew is visible.** CLI/Hub version mismatch is surfaced, not
   silently tolerated. Mixed-version operation is unsupported by design; both
   machines should run the same release.
5. **Every surface points at the Hub.** A user on any compatibility surface is
   told the primary path exists, so no surface becomes a dead end.

## Known divergences still open

| # | Divergence | Severity | Note |
|---|---|---|---|
| 1 | `tantu receive` has thinner progress/recovery language than the dashboard. | Limit | Acceptable for an advanced surface, but it is a real difference |
| 2 | `tantu drop` keeps a legacy token model. | Gap | Intended to converge; not yet removed |
| 3 | Terminal width adaptation of the state vocabulary is not systematic. | Limit | Same names, different line breaks; no semantic difference |

## Decisions

- **D-03** — standalone commands are compatibility/advanced pending migration.
  Recommended and adopted: no surface keeps a divergent UX model.
- **No surface has been removed.** The plan anticipated deprecation timelines;
  none has been scheduled because no replacement gap is proven.
- **Migration direction** (Phase 0 exit criterion): decided — compatibility
  surfaces stay, labelled, pointing at the Hub. Not a future intention.
