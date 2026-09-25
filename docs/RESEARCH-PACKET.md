# Tantu Human-Validation Packet

> **Status:** Ready-to-run materials. No sessions have been run; see
> `docs/UX-STATUS.md` for the evidence state.
> **Source:** plan §11 (research program), §15 (acceptance library).

An automated agent cannot produce E4–E5 evidence. This packet exists so the
human program starts executing instead of being designed. Every session must
leave a filled evidence ledger (below) — percentages without sample size,
profile, task context, and baseline are meaningless.

## 1. Consent and redaction (before every session)

- [ ] Explain what is recorded (notes, screen, video — each separately).
- [ ] Obtain explicit consent for each artifact type.
- [ ] Confirm deletion schedule for recordings and notes.
- [ ] Never capture: clipboard bytes, text snippets, payload bytes, full
      OAuth URLs, tokens, private keys, unredacted local paths.
- [ ] Store artifacts outside the product repo unless explicitly approved.
- [ ] Record participant profile only as needed for analysis.

## 2. Hypotheses under test

| ID | Hypothesis | Validation |
|---|---|---|
| H-01 | Visible destination reduces wrong-peer sends | Moderated multi-peer task |
| H-02 | Clipboard preview reduces accidental disclosure without materially slowing experts | Within-session comparison |
| H-03 | State-aware first-run reduces time to first transfer | Baseline vs redesigned flow |
| H-04 | Data-safety/retry-safety fields improve recovery choice | Failure-injection tasks |
| H-05 | One vocabulary reduces dashboard/CLI confusion | Cross-surface comparative tasks |
| H-06 | Completion tombstone removes duplicate-risk ambiguity | Retry-scenario tests (post-versioning) |
| H-07 | Expert immediate-send preserves power-user speed | Expert timing + error rate |
| H-08 | Bounded history is useful without privacy concern | Metadata-retention interviews |

## 3. Task scripts (grouped)

First run: install/start · recover dashboard link · pair · verify SAS ·
explain trust vs connectivity. Sending: text · paste-and-send image ·
file · cancel. Receiving: preview · download · open folder. Recovery:
offline peer · quota · checksum · lost ACK · browser unavailable · stale
dashboard tab · refresh during transfer · restart during transfer ·
destination change during preparation · renamed output directory.
Cross-surface: same logical operation in CLI and dashboard · second peer
and destination switch. Access: keyboard-only paste/picker · screen-reader
progress and completion · 200% zoom · high contrast · reduced motion.

## 4. Cohorts and minimum sample

First-time developers · experienced CLI users · Windows/macOS/Linux users ·
remote/SSH users · multi-peer users · keyboard-only users · screen-reader
users (desktop + mobile AT where available) · high-contrast/reduced-motion
users · failure-recovery and slow-network/large-file users · no-browser
users · shared-directory users. Minimum before any UX-complete claim: at
least 5 first-time + 5 power users and documented accessibility sessions
per supported platform where available; report limitations, never invent
statistical certainty.

## 5. Starting success thresholds

90% unaided first transfer · 95% correct destination identification · zero
wrong-peer sends in moderated tests · 90% safe recovery choice · 90%
paired-vs-online explanation · 95% keyboard-only completion · 90%
screen-reader completion · duplicate incidents trending to zero · every
release-blocking UX issue backed by a test or research artifact.

## 6. Evidence ledger (one per journey)

```text
Journey / Current behavior / Evidence level (E0–E5) / Known failure modes /
Observed confusion / Recommended change / Acceptance test / Release status /
Owner / Last reviewed
```

## 7. Platform and failure matrices (pointers)

Browsers/platforms/zoom/contrast/motion/keyboard/AT/slow-network plus
clipboard source variants (OS screenshot, copied file, browser copy,
no-payload, text+image): plan §15.2. Failure fixtures (offline, quota,
permission, disk-full, checksum, lost ACK, duplicate DropID, refresh, SSE
gap, restart, renamed output, replaced staging, expired session, no
browser, incompatible version, hostile names/URLs, long/unicode names,
empty/oversized text, large preview): plan §15.3.
