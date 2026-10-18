# Reviewing the Tantu sign-in error pages

**Who this is for:** someone who uses Tantu but does not write software.

**What you are doing:** reading twelve small web pages — the ones a person sees
when a sign-in is refused — and telling me whether they make sense. That is the
whole job.

**How long it takes:** about 20 minutes, most of it listening.

**What you need:** nothing to install. Everything this needs is already on your
computer. (I checked.)

---

## Part 1 — Why this matters

When a sign-in fails, Tantu shows a page explaining what went wrong. I wrote
those pages. I have read them. **You have not**, and neither has anyone else.

That is a real problem, not a formality. I can tell you every page is complete
and leaks no secrets — those are automated checks, and they pass. What no
automated check can tell me is whether the words actually help someone who is
mid-failure and frustrated.

You are the only person who can answer that. A developer reading their own
product stops noticing things a stranger would not.

---

## Part 2 — Before you start

You do **not** need to install anything. I confirmed all three of these are
already on your machine:

| Thing | Where it is | Why you need it |
|---|---|---|
| Go | `C:\Program Files\Go\bin\go.exe` | Produces the twelve pages |
| Narrator | `C:\Windows\System32\Narrator.exe` | Reads pages aloud |
| Chrome or Edge | already installed | Opens the page |

---

## Part 3 — Make the twelve pages

### Step 1 — open PowerShell

Press the **Windows key**, type `powershell`, press **Enter**.

A black window with a blinking cursor opens. That is PowerShell. It is the place
you type commands.

If a blue window titled "Administrator" or a tabbed one opens instead, that is
fine too — just make sure the text says `PowerShell`.

### Step 2 — move into the Tantu folder

Type this and press Enter:

```powershell
cd D:\dev\pro\tantu
```

**What that means:** "change directory" — go to this folder. Everything after this
happens inside it.

**How to tell it worked:** the prompt now starts with `PS D:\dev\pro\tantu>`.

### Step 3 — tell PowerShell where to put the file

Copy this line exactly, including the `$`, the quotes and the slashes:

```powershell
$env:TANTU_EXPORT_REJECTION_PAGES = "$PWD\rejection-pages.html"
```

**What that means:** it sets a short-lived note, "put the output at
rejection-pages.html in this folder". It lasts only until you close the window.

**Nothing will be printed.** That is correct. No output means it worked.

### Step 4 — run the command that makes the pages

```powershell
go test ./internal/bridge/ -run TestExportRejectionPages -v
```

**What that means:** `go` is the Go tool that Tantu is built with. This asks it to
run one specific small program that draws all twelve pages.

**This is what success looks like.** I ran it and copied the real output here:

```
=== RUN   TestExportRejectionPages
    rejection_pages_export_test.go:124: wrote 12 rejection pages to D:\dev\pro\tantu\rejection-pages.html
--- PASS: TestExportRejectionPages (0.00s)
PASS
ok      github.com/bhaskarjha-dev/tantu/internal/bridge    4.070s
```

**How to tell it worked:** the words `wrote 12 rejection pages`, and a line that
says `ok` rather than `FAIL`.

#### If it says `go` is not recognised

That means this PowerShell window cannot find the Go tool. Fix it by giving it
the full path. Type this instead of the step 4 command:

```powershell
& "C:\Program Files\Go\bin\go.exe" test ./internal/bridge/ -run TestExportRejectionPages -v
```

(The `&` at the start is what tells PowerShell to run the thing you just named.)

**If you see any red error instead**, stop there and send me the red text, exactly
as it appears. Do not carry on — the later steps depend on this one working.

---

## Part 4 — Turn on Narrator

Narrator is Windows' built-in screen reader: it reads what is on screen out loud.

**To turn it on:** press **Windows key + Ctrl + Enter** together.

**To turn it off:** press the same three keys again.

If that does not work, click the Start button, then **Settings → Accessibility →
Narrator**, and switch it **On**. Turning it on this way will also make the
shortcut work.

**I am not going to guess at the navigation keys for you**, because getting them
wrong is the fastest way to make this frustrating, and a wrong key reads as "this
does not work". Here is where Windows lists the real ones, with the real names
next to them:

> Settings → Accessibility → Narrator → **Keyboard shortcuts and narrator
> settings** → *General* tab

Open that window before you start, and keep it beside your browser. It will name
the keys for reading from the top of the page, reading from where you are, and
moving forward one item — which is all you need.

**If you only try one thing:** **Ctrl + Alt + →**. It moves to the next piece of
text. If what you hear changes as you press it, you are ready to start.

---

## Part 5 — What to do

The page you opened has twelve sections. Each shows one error page inside a box,
plus a note saying what would have to happen for a person to see it.

Work down through them, one at a time. For **each** page, answer four questions.

> **How to tell the pages apart:** each one has a heading above the box, like
> `rejectState` or `rejectPath`. That is Tantu's internal name for the problem,
> not part of the page itself. Use it to tell me which one you are talking about
> — otherwise "page 3" will not mean the same thing to us.

### Question 1 — Does the headline alone tell you what happened?

Navigate to just the big bold heading (the first thing in each box) and read only
that, nothing else.

Without reading the rest — do you know that **a sign-in failed**, and roughly
**why**?

> **A page fails this if** the headline is vague, like "Something went wrong", or
> is about Tantu instead of about your sign-in.

### Question 2 — Is there one clear next thing to do?

Read the whole page.

Is there **exactly one** action, and could you take it without needing to read
anything else first?

> **A page fails this if** it gives you two options, or only tells you what the
> problem is without telling you what to do about it.

### Question 3 — Did any word stop you?

Note any word you had to stop and think about, or had to look up.

Two I already suspect, which I want **your** judgement on rather than mine:

- **"callback"** — this word appears on ten of the twelve pages. Do you know what
  it means? If not, should it be a plainer word?
- **"loopback"** and **"localhost"** — on one page.

Write down which words stopped you and which page they were on.

### Question 4 — Does the last line help?

Every page ends with:

> "This page is served by Tantu, the machine that relayed this sign-in. No
> sign-in information is shown here."

That is the **last thing you hear** after your sign-in has just failed.

> **Listen to whether you would want that sentence at that moment** — or whether
> it is filler. Your answer either way is useful; I genuinely do not know.

### One extra thing — asked once, not twelve times

On each page, the "what to do" line has a **blue line down its left edge**. That
line is visual only.

Now listen to a page with your eyes closed, or looking away.

> **Could you still tell which part was the thing to do?** If not, that is a real
> finding about the page — and it is about how the page is *built*, not about the
> wording. Tell me and I will fix the structure.

---

## Part 6 — Write down what you found

A **clean run is a perfectly good result.** If everything passes, that closes this
item. You do not have to find something to have done it properly.

Copy this into a message to me and fill it in:

```
Reviewed: 2026-10-18
By:       <your name>
System:   <the Windows version, e.g. Windows 11>
Narrator: <the version it says on the welcome screen, or "unknown">

Pages passed:  <n> of 12

Page 1:  pass / fail — <what was wrong, if anything>
Page 2:  pass / fail — ...
Page 3:  pass / fail — ...
... through page 12

Words that stopped me:
  <word> — page <n>

The blue-line "next action" was identifiable without seeing it:
  yes / no — <what happened>

The closing sentence ("This page is served by Tantu..."):
  helpful / filler / in the way — <why>
```

**Report what is wrong, not that something felt off.**

- Good: *"Page 8's next action tells me the app is misconfigured but gives me
  nothing to do about it."*
- Not useful: *"Page 8 was confusing."*

---

## Part 7 — If you would rather not use Narrator

You can still read the twelve pages with your eyes and tell me what you think.
That is genuinely useful.

But it is **not the same evidence**, and I want to be straight about the
difference:

| What you did | What it closes |
|---|---|
| Read them with your eyes | The "nobody but the author has read them" half |
| Read them with Narrator | That half **and** the assistive-technology half |

Both help. Only the second closes the item completely.

---

## Part 8 — Afterwards

- If you find a **word problem**, I change the wording and re-check it.
- If you find a **structural problem** — like the blue line having no spoken
  equivalent — I change how the page is built, not what it says.
- If you find something I disagree with, I will say so and explain why. If I am
  wrong, you will hear that too.

**One thing this does not do:** it does not close a different, larger item in our
list about accessibility, which also needs high-contrast mode and switch-access
testing. This is one screen reader over twelve small pages. That bigger item stays
open, and it should.