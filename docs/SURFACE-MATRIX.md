# Surface Migration Matrix

> **Status:** living matrix. **Last reviewed:** 2026-10-22.
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
| `tantu send <dir>` | Power | First-class adapter | Canonical (batch) | Must match | Delegated to Hub | Retain; a directory is a **batch of independent transfers**, not an archive. Per-file operation IDs and outcomes; a partial batch exits non-zero and names the files that did not arrive. |
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
6. **A batch reports as a batch.** A multi-file send is several logical
   transfers, so no surface may collapse them into a single success flag. The
   counts, the per-file outcomes, and the process exit code must all agree —
   and the worst outcome in the batch wins, because "49 of 50, exit 0" is
   indistinguishable from success to any caller. This is the batch form of
   rule 3: partial is not success.
7. **An explicit destination is never dropped.** A batch and a single send
   must both carry `--peer` all the way to the peer. A batch that silently
   routed to the active peer would deliver to a machine the user did not name
   while reporting success — the worst outcome this product can produce.
8. **One owner per component, and every surface gets it.** A surface may
   carry its own *shape* — the QuickDrop card, the relay popup, the
   dashboard's tabs — but the components a surface is built from
   (the button vocabulary, the empty state, the visually-hidden utility,
   the focus ring, the typeface) are owned by the design system and
   served to every page at one marker. The failure this rule exists to
   prevent is concrete and measured: the node's pages used the class
   `.empty-state`, and the only definition of it in the product lived in
   the dashboard's stylesheet, which those pages never load — so their
   empty state rendered as an unstyled paragraph of body text. A surface
   that redefines a component is not "styled differently"; it is a
   second owner, and the two drift.
9. **The value the reader most needs takes the leading position, and it
   is stated once.** Rule 2 says a destination is never chosen silently.
   This says how the reader finds it. On the send surface the
   destination is the one value that must be findable at a glance, so it
   leads — it is the first thing in the first band, at the size of a
   heading, and the rest of the band is secondary. It is stated **once**,
   in one treatment: the send surface used to name it in the band, in the
   drop zone's sub-line, and in the send button's label, in three
   different treatments, and the band and the sub-line disagreed with
   each other about where a send would go. Repeating a value is not the
   same as making it clear, and two statements of it are two chances to
   contradict each other.
10. **A surface states the shape of what it is, not just what it does.**
    A procedure is a list, not a paragraph: the relay tab's recommended
    path was described in prose containing three instructions, and a
    reader following prose has to find the steps inside it. A name is not
    truncated away: a peer's name is how two machines are told apart, so
    it wraps rather than shrinking to fit the buttons beside it. And an
    empty state is drawn as an object, because "nothing here yet" in the
    same weight as everything else reads as an oversight rather than as a
    state.

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
