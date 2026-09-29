# Two-machine evidence run (2026-09-29)

> **What this is:** a recorded run against a second, physically distinct
> machine. It closes the "no second machine was ever dialled" gap that
> `docs/KNOWN-LIMITATIONS.md` §2.6 carried as open.
>
> **What it is not:** a substitute for E4 (representative users) or E5
> (assistive technology). Nothing here was observed on the peer's own screen.

## The setup

| | |
|---|---|
| Local machine | `DESKTOP-SUD78TU`, `192.168.0.244`, MAC `28-00-AF-57-A5-F3` |
| Peer | `192.168.0.178:9877`, MAC `ac-91-a1-5c-44-d8` |
| Build | `tantu v1.0.0-dev+ebcee7f` (dev build, unstaged tree) |
| Transport | LAN (mTLS), peer trusted in the store, SAS `cc88f1` |

The peer is a **different host**: a different IP and a different MAC on the
same subnet, and the peer's certificate fingerprint (`cc88f1…`) differs from
this machine's identity (`0949b2…`). Nothing here is a loopback self-send, which
is what every previous automated run used.

## What ran, and what came back

| # | Scenario | Result |
|---|---|---|
| 1 | Text snippet across machines | completed, `verified: true` |
| 2 | 8 MiB file across machines | completed, `verified: true`, **0.53 s (15.0 MiB/s)** |
| 3 | Same `--idempotency-key`, different content | refused; nothing saved |
| 4 | Same key, same content | suppressed duplicate, reported as already saved |
| 5 | Send to an unpaired address (`192.168.0.251:9877`) | refused before sending: "not a trusted paired peer" |
| 6 | `tantu send <directory>` (before multi-file support) | refused: "is a directory; archive it before sending", exit 2 |
| 7 | `tantu doctor` against the live peer | healthy, all checks ok |

Measurement for #2: 8 MiB of pseudo-random bytes, SHA-256
`7444487280e26a34f33de76c9de2f922f1548061815803502bcd8bbf7ec2533a`, transferred
in 0.53 s. The sender's stated digest matched the receiver's, which is what
"verified" means here — the claim is end-to-end integrity, not just that bytes
moved.

## The defect this run found

Scenario 3 is the one that mattered. Reusing an idempotency key with different
content is refused by the receiver, which is correct behaviour. What the sender
did with that refusal was not:

```text
code: unknown_outcome
plain_message: "Transfer may have completed but confirmation was lost.
                The file may already be saved."
retry_safe: false   duplicate_risk: true
```

Every byte had been streamed, so the classifier's byte-count rule fired and
overrode everything it knew. The user was told to go and check the receiver's
inbox for a file the receiver had just stated it did not write, and was told
retrying was unsafe.

The cause was structural: a receiver's refusal and a lost acknowledgement both
arrived as plain strings, so the only way to tell them apart was to match on
wording. Rejections are now a typed error (`drop.RejectionError`), classified
before any byte-count inference:

```text
code: idempotency_key_conflict
message: "The receiver already completed a different transfer under this
          idempotency key, so this one was refused. Nothing new was saved."
retry_safe: true    data_safe: true
next_action: "Send again with a new idempotency key, or resend the original
              content to reuse the existing one. Nothing needs cleaning up."
```

The durable ledger shows the same input before and after, which is the clearest
record of the change:

```text
06:09:10  text (24 bytes) -> ai  terminal_failure          <- before
06:10:24  text (24 bytes) -> ai  retryable_failure [safe to retry]   <- after
```

A genuinely lost acknowledgement still reports `unknown_outcome`; that test is
pinned separately so the fix could not quietly turn a real unknown into a false
reassurance.

## What this still does not prove

Being explicit, because the previous version of this claim was wrong in the
other direction — it said a second machine had *never* been dialled when the
limitations register could not tell the difference between "not attempted" and
"not recorded".

- **Nothing was observed on the peer's own dashboard.** Delivery is proven from
  the sender's side by the receiver's acknowledgement and matching digest. The
  receiving inbox, its rendering, and its download path are unverified here.
- **The pairing ceremony was not exercised.** The peer was already trusted. SAS
  comparison, inbound approval, rejection, and the timeout path are still
  covered only by seeded-store and in-process tests.
- **The OAuth relay was not run across machines** in this session. It works
  against the loopback peer in the test suite; a real remote sign-in is
  unverified.
- **One direction, one peer, one network.** No roaming, no sleep/wake, no
  Wi-Fi-to-Ethernet handover, no behaviour when the peer disappears mid-transfer
  on real hardware.
- **The peer runs a release that may differ.** If it is an older build, a
  behavioural difference would be indistinguishable from a product defect.
  This run cannot tell.

## Reproducing it

Any paired second machine on the same LAN will do:

```bash
tantu send "hello"                                   # expect: completed, verified
dd if=/dev/urandom of=./8mib.bin bs=1M count=8
tantu send ./8mib.bin                                # expect: completed, verified
tantu send "other" --idempotency-key k1              # expect: completed
tantu send "other" --idempotency-key k1              # expect: idempotency_key_conflict
```

The interesting assertion is the last one. If it reports `unknown_outcome`, the
typed-rejection fix has regressed.
