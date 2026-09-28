# Security Policy

## Reporting a vulnerability

Please **do not open a public issue** for a security problem.

Report privately through GitHub's "Report a vulnerability" button on the
Security tab of this repository, which opens a private advisory visible only to
the maintainer.

Please include:

- what an attacker can achieve, and what they need in order to do it
- the Tantu version (`tantu version` reports the build and commit)
- your platform and whether the Hub was running headless, with a cockpit, or in
  a container
- reproduction steps, ideally a `tantu send`/`open`/`pair` transcript
- whether any real credential, token, or file was exposed

## What to expect

- An acknowledgement within a few days, from a human, not an auto-responder.
- An assessment of severity and an indication of whether a fix is in progress.
- A fix release, and credit in `CHANGELOG.md` unless you prefer otherwise.
- Disclosure is coordinated with you. There is no bounty programme.

## Scope

In scope:

- anything in `internal/protocol`, `internal/transport`, `internal/pairing`,
  `internal/bridge`, or `internal/drop` that lets an untrusted party cross a
  trust boundary it should not
- the loopback control plane in `internal/hub` — authentication, CSRF, path
  handling, or the local HTTP surfaces
- anything that lets one paired peer read, write, or impersonate another
- a local origin that an ordinary web page can drive
- secret material: keys, tokens, authorization codes, or PKCE verifiers
  reaching a log, a file, a URL, or another machine

Out of scope:

- **anything reachable only by code already running as your user.** The Hub and
  its CLI hold your identity key, your peer store, and a loopback control
  surface. Any process that can read your user files can already read those. This
  boundary is stated explicitly in `docs/THREAT-MODEL.md` (T10) rather than
  treated as a vulnerability. Reporting it will get a kind reply and no fix.
- Denial of service by a process that can already open 20 connections to
  127.0.0.1.
- Findings that depend on a modified build.
- Missing hardening with no demonstrated impact. These are tracked in
  `docs/KNOWN-LIMITATIONS.md`, which records severity and status for each.

## Deployment notes that affect your risk

- The Web UI binds loopback only. Treat any tool that proxies or forwards a
  local port to a network interface as removing that boundary.
- Mixed-version operation between two machines is unsupported. Upgrade both.
- Windows relies on directory permissions; there is no ACL hardening yet. On a
  shared or multi-user Windows host, prefer `%APPDATA%\tantu` being readable
  only by your account.
- `tantu hub --auto-pair` accepts an incoming pairing without human approval.
  It is off by default and removes the only MITM-resistant step in pairing. Do
  not enable it on a network you do not control.
