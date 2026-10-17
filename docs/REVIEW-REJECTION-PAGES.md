# Reviewing the OAuth callback rejection pages

A procedure, so that the evidence closing [KNOWN-LIMITATIONS 4.99](KNOWN-LIMITATIONS.md)
is something anyone can repeat, and so the next person does not have to ask what
was done.

This is the procedure. If you have run it, the results go in the row's **Evidence**
column.

---

## What this closes, and what it does not

| Row | Claim | Closed by this |
|---|---|---|
| **4.99** | Nobody outside the author has read the twelve rejection pages, and no assistive technology has been through them. | **Yes.** One other person reads all twelve under a screen reader. |
| **2.2** | No assistive-technology evidence at all: no screen-reader session, no high-contrast mode, no switch input. | **No.** This is one screen reader over twelve static pages. High-contrast and switch input are untouched, and 2.2 stays open. |

Being precise about this matters more than closing a row quickly: 4.99 is a claim
about twelve pages of error copy, and this procedure answers exactly that.

---

## Already done, so you do not repeat it

- Every page is **rendered by the product's own `renderCallbackRejection`** — the
  same function the browser receives. Not a transcription.
- Machine-checked: no internal check text (`Sec-Fetch`, "does not match"), no
  `state` value, no authorization code, and the two answers are distinguishable
  (gates in `internal/bridge/rejection_test.go`).
- Each page carries `<html lang>`, a `<main>` landmark, an `<h1>`, a document
  `<title>` and a viewport meta.
- All twelve are 36–55 words, which is **14–21 seconds** at a comfortable
  listening pace.

So the questions below are the ones no assertion answers.

---

## Setup

```powershell
cd D:\dev\pro\tantu
$env:TANTU_EXPORT_REJECTION_PAGES = "$PWD\rejection-pages.html"
go test ./internal/bridge/ -run TestExportRejectionPages -v
```

Open `rejection-pages.html`. Turn on Narrator: **Win + Ctrl + Enter**. If it is not
already installed, Windows offers to add it when you press that.

Read the twelve frames in order. Do not read the surrounding page first — the
surrounding text is the harness's commentary, not the product's.

---

## The pass, per page

Four questions. Answer all four for each of the twelve. A page passes only if all
four hold.

### 1. Does the headline alone tell you the situation?

Press the reading-order keys (`H` then `H` in Narrator) and listen to only the
heading. Without the body, do you know:

- something went wrong with this sign-in, and
- roughly what kind of thing went wrong?

**Fails if** the heading is vague ("Something went wrong"), or is about Tantu
rather than about the user's sign-in.

### 2. Is the next action unambiguous, and one action?

Listen to the whole page. Is there exactly **one** thing to do, and could you do
it without reading anything else?

**Fails if** the page offers two remedies, or describes a condition ("if the
application keeps calling back on a different path…") without saying what *you*
should do now.

### 3. Does any word stop you?

Note any word you had to look up, or had to work out from context.

Candidate already flagged, not yet judged: **"callback"** appears in ten of the
twelve pages, and **"loopback"** and **"localhost"** in the `rejectNotLoopback`
page. Whether those need replacing with plainer words is a judgement, not a
measurement — a developer who knows the product will not notice, which is the point
of asking someone who does not.

### 4. Is the reassurance line helping or being in the way?

Every page ends with:

> This page is served by Tantu, the machine that relayed this sign-in. No
> sign-in information is shown here.

The first sentence is reassurance; the second is a claim about the product's
behaviour. **Listen to whether you would want either of them at this moment.** A
user whose sign-in just failed is looking for what to do, and this is the last
thing they hear.

**Fails if** it reads as boilerplate, or as Tantu talking about itself rather than
helping.

### One structural question, asked once rather than per page

The next action is marked visually — a 3px blue left border on `.next`. In the
DOM it is simply the third paragraph. **Nothing marks it in the audio stream.**

So: when you hear the three paragraphs with no visual cue, is the next action
still obviously the next action? If it is not, that is a finding about the
**markup**, not the copy — the fix is a structural cue that survives being heard,
not a rewording.

---

## What counts as a finding

- A page that fails any of the four questions.
- A word that stopped you, with the page number.
- The structural question, answered either way.

Report what is wrong, not that something felt wrong. "Page 8's next action is a
diagnosis, not an instruction — it tells me the application is misconfigured and
gives me nothing to do" is a finding. "Page 8 was confusing" is not.

---

## Recording the result

Replace this with what you actually observed, and paste it into the **Evidence**
column of row 4.99:

```
Reviewed 2026-10-17 by <name>, Windows <version>, Narrator <version>,
all twelve pages.

Pass/fail: <N of 12 passed>
Findings:  <numbered list, each with the page number and what is wrong>
Words that needed looking up: <list, or "none">
Structural: <whether the next action is identifiable without the visual cue>
```

A **clean run is a legitimate result** and closes 4.99 on its own. Record the
reader, the date and the tool version either way — "reviewed and clean" is
evidence, and it is the only kind that is hard to reconstruct later.

---

## Afterwards

- **Findings** become either a fix in `internal/bridge/rejection.go` (with a red
  proof on `internal/bridge/rejection_test.go`, as every other change here has) or
  a new row in the register. Not a commit message.
- **2.2 stays open.** High-contrast and switch input are still unevidenced, and a
  screen reader over static pages is not the "no assistive-technology evidence"
  that 2.2 claims is missing.