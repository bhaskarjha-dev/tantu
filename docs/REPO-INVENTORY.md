# Repository Inventory

> **Status:** living document. **Last reviewed:** 2026-10-26.
> **Scope:** every tracked path in this repository, and the untracked artefacts
> that were on disk when this was written.

The repository is 298 tracked files. A person arriving today cannot tell
which of them are load-bearing, which are supporting cast, and which are
cruft -- and the cost of guessing wrong is not a broken build. It is a
**quietly wrong product**: a removed test, a dropped error contract, a
design token quietly re-inlined, and nothing goes red.

This document is the map. It answers one question per file: *what breaks
if this goes away?*

It is not a style guide and it is not a wish list. The classes below are
claims about consequence, and the last section says how the claim is
enforced.

---

## The four classes

| Class | Meaning | Test |
|---|---|---|
| **EXTREMELY IMPORTANT** | Losing it breaks a promise the product makes to the people who use it — the wire format, the trust model, the data path, the encrypted channel, the design system every surface draws from, the build, the gates, the licence, or the rules the work is judged against. | The product stops doing the thing it exists to do, or stops being buildable, or stops being honest about what it does. |
| **VERY IMPORTANT** | Losing it breaks a documented capability, or a whole class of evidence about the product — but the product still builds, still runs, and still behaves correctly on the paths it still covers. | A capability disappears, or a claim stops being provable, and nobody can find out from the code alone. |
| **IMPORTANT** | Losing it degrades quality, coverage, or maintainability. The product still builds, still runs, and still behaves the same. | The repository knows less about itself, or can prove less about itself, and the next change is riskier for it. |
| **USELESS** | Losing it changes nothing that matters. | Nothing. |

**The honest answer about "useless": nothing tracked in this repository is
useless.** That is not a compliment -- it is a finding, and it is stated
plainly rather than manufactured into four buckets to fill a table. Every
tracked file earns its place. The genuinely useless things in this
repository are on disk and *untracked*, and they are listed in the last
section.

---

## Summary

The counts are computed from the tables below, not estimated: every
tracked path is matched to the row that classifies it, and a path that no
row classifies fails the gate rather than being counted as nothing.

| Area | Files | Extremely | Very | Important | Useless |
|---|---:|---:|---:|---:|---:|
| Root | 11 | 4 | 2 | 5 | 0 |
| `.github/workflows/` | 2 | 2 | 0 | 0 | 0 |
| `cmd/tantu/` | 54 | 14 | 8 | 32 | 0 |
| `internal/bridge/` | 35 | 7 | 0 | 28 | 0 |
| `internal/bridge/testdata/` | 3 | 0 | 3 | 0 | 0 |
| `internal/browser/` | 2 | 0 | 1 | 1 | 0 |
| `internal/discovery/` | 8 | 0 | 2 | 6 | 0 |
| `internal/drop/` | 28 | 11 | 0 | 17 | 0 |
| `internal/hub/` | 68 | 11 | 2 | 55 | 0 |
| `internal/pairing/` | 13 | 4 | 0 | 9 | 0 |
| `internal/protocol/` | 5 | 2 | 0 | 3 | 0 |
| `internal/testutil/` | 9 | 0 | 0 | 9 | 0 |
| `internal/transport/` | 9 | 4 | 0 | 5 | 0 |
| `internal/webui/` | 7 | 3 | 1 | 3 | 0 |
| `tools/docgate/` | 6 | 1 | 1 | 4 | 0 |
| `tools/encgate/` | 3 | 1 | 0 | 2 | 0 |
| `tools/uxtest/` | 14 | 6 | 2 | 6 | 0 |
| `tools/wfcheck/` | 5 | 0 | 1 | 4 | 0 |
| `docs/` | 16 | 3 | 8 | 5 | 0 |
| **Total** | **298** | **73** | **31** | **194** | **0** |

The root table also names seven **directories** as extremely important —
`internal/protocol/`, `internal/pairing/`, `internal/drop/`,
`internal/transport/`, `internal/bridge/`, `internal/hub/`, `cmd/tantu/`
and `internal/webui/`. Those are prose, not rows: each directory's files
are counted in its own table below, so the total counts every file once.

---

## Root

| File | Class | What breaks without it |
|---|---|---|
| `go.mod` | **EXTREMELY IMPORTANT** | The build. It pins the module path and the entire dependency surface — two modules, both reviewed by hand. |
| `go.sum` | **EXTREMELY IMPORTANT** | The build's integrity check. Without it, `go build` refuses to run. |
| `.github/workflows/ci.yml` | **EXTREMELY IMPORTANT** | Every gate the repository trusts. It is the definition of "green" for anything in this repo. |
| `.github/workflows/release.yml` | **EXTREMELY IMPORTANT** | Signed, keyless release artifacts and the SBOM. Losing it means nothing can be published safely. |
| `AGENTS.md` | **EXTREMELY IMPORTANT** | The rules the work is judged against: the invariants, the conventions, and the demand that a claim about behaviour is a claim that something runs. |
| `LICENSE` | **EXTREMELY IMPORTANT** | The legal right to distribute the binary. |
| `internal/protocol/` | **EXTREMELY IMPORTANT** | See below. |
| `internal/pairing/` | **EXTREMELY IMPORTANT** | See below. |
| `internal/drop/` | **EXTREMELY IMPORTANT** | See below. |
| `internal/transport/` | **EXTREMELY IMPORTANT** | See below. |
| `internal/bridge/` | **EXTREMELY IMPORTANT** | See below. |
| `internal/hub/` | **EXTREMELY IMPORTANT** | See below. |
| `cmd/tantu/` | **EXTREMELY IMPORTANT** | See below. |
| `internal/webui/` | **EXTREMELY IMPORTANT** | See below. |
| `README.md` | **VERY IMPORTANT** | The front door. It is how anyone learns the product exists and how to pair two machines. |
| `SECURITY.md` | **VERY IMPORTANT** | How a vulnerability gets reported. Losing it means a real report goes to the wrong place or nowhere. |
| `CHANGELOG.md` | **IMPORTANT** | The user-facing summary of what changed and why. Not load-bearing at runtime; losing it makes upgrades guesswork. |
| `.gitattributes` | **IMPORTANT** | Line-ending normalisation. Without it, Windows checkouts rewrite LF to CRLF, `gofmt -l` reports false positives locally while CI sees clean files — **destroying the signal of any format gate**. |
| `.gitignore` | **IMPORTANT** | Keeps build output, scratch files and the working tree clean. |
| `.goreleaser.yaml` | **IMPORTANT** | Release packaging and the version stamp. The build still works without it; the release does not. |
| `staticcheck.conf` | **IMPORTANT** | The static analyser's configuration. Without it, staticcheck either fails or stops checking the things it was pointed at. |

> The seven package directories listed as **EXTREMELY IMPORTANT** in the root
> table are counted once, here, and again in their own tables below. The
> root table's count is of *distinct top-level entries*.

---

## `.github/workflows/`

| File | Class | What breaks without it |
|---|---|---|
| `ci.yml` | **EXTREMELY IMPORTANT** | Every gate: format, vet, staticcheck, tests, the three tool gates, and the browser suite on three engines. |
| `release.yml` | **EXTREMELY IMPORTANT** | The signed release. |

---

## `internal/protocol/` — the wire format

Both ends of every transfer must agree on this, byte for byte. A change
here is a change to every installed version at once.

| File | Class | What breaks without it |
|---|---|---|
| `protocol.go` | **EXTREMELY IMPORTANT** | The envelope: type discrimination, framing, the rules a message must obey. |
| `codec.go` | **EXTREMELY IMPORTANT** | The encoding. Without it there is no wire. |
| `protocol_test.go` | **IMPORTANT** | Proof that the protocol behaves as specified. |
| `decoder_contract_test.go` | **IMPORTANT** | Proof that the decoder rejects what it must reject — the property that keeps a malformed message from becoming a crash. |
| `version_test.go` | **IMPORTANT** | Proof that version skew is *detected*, which is what lets the CLI say "your peer is older" instead of failing mysteriously. |

---

## `internal/pairing/` — the trust model

The one part of the product where being wrong destroys a person's
security permanently. Nothing here may be "simplified".

| File | Class | What breaks without it |
|---|---|---|
| `handshake.go` | **EXTREMELY IMPORTANT** | The in-band pairing handshake: transcript SAS, nonce exchange, the proof that both sides saw the same thing. |
| `cert.go` | **EXTREMELY IMPORTANT** | Certificate generation, the SAS word list, the short authentication string. |
| `pair.go` | **EXTREMELY IMPORTANT** | The pairing API the CLI and the Hub both drive. |
| `store.go` | **EXTREMELY IMPORTANT** | The trusted-peer store: who is trusted, with which key, and how that survives a restart. |
| `pair_test.go` | **IMPORTANT** | Proof the handshake completes. |
| `handshake_test.go` | **IMPORTANT** | Proof the handshake rejects what it must. |
| `cert_test.go` | **IMPORTANT** | Proof the certificate and SAS rules hold. |
| `sas_test.go` | **IMPORTANT** | Proof the SAS carries enough entropy (54 bits, 6 words) and is uniform. |
| `store_test.go` | **IMPORTANT** | Proof the store survives corruption, partial writes and concurrent access. |
| `identity_binding_test.go` | **IMPORTANT** | Proof an identity is bound to its key. |
| `has_peer_at_address_test.go` | **IMPORTANT** | Proof the store does not confuse "a peer at this address" with "this peer". |
| `store_backup_test.go` | **IMPORTANT** | Proof a backup restores. |
| `store_fault_test.go` | **IMPORTANT** | Proof the store fails safely when the disk does. |

---

## `internal/drop/` — the data path

Where bytes become files. Durability, idempotency and path safety live
here, and every one of those is a promise the product makes.

| File | Class | What breaks without it |
|---|---|---|
| `types.go` | **EXTREMELY IMPORTANT** | The vocabulary of a drop: identity, size, digest, state. |
| `manifest.go` | **EXTREMELY IMPORTANT** | The manifest that describes a batch. |
| `receiver.go` | **EXTREMELY IMPORTANT** | Receiving, staging and publishing. |
| `staging.go` | **EXTREMELY IMPORTANT** | Staging and atomic publication — the reason a crashed transfer leaves a resumable partial rather than a truncated file under a delivered name. |
| `sender.go` | **EXTREMELY IMPORTANT** | Sending. |
| `digest.go` | **EXTREMELY IMPORTANT** | The digest contract. `verified: true` means something because of this. |
| `quota.go` | **EXTREMELY IMPORTANT** | The size limit the UI quotes (5GB) is enforced here. |
| `resume.go` | **EXTREMELY IMPORTANT** | Resuming a partial transfer. |
| `tombstone.go` | **EXTREMELY IMPORTANT** | The record that a drop id was used, so a retry cannot duplicate it. |
| `staging_lockholder_windows.go` | **EXTREMELY IMPORTANT** | The lock that keeps two processes from publishing the same path on Windows. |
| `staging_lockholder_other.go` | **EXTREMELY IMPORTANT** | The same lock everywhere else. |
| `drop_test.go` | **IMPORTANT** | Proof of the core behaviour. |
| `e2e_test.go` | **IMPORTANT** | Proof it works end to end. |
| `staging_test.go` | **IMPORTANT** | Proof of staging and atomicity. |
| `publish_test.go` | **IMPORTANT** | Proof of publication. |
| `security_test.go` | **IMPORTANT** | Proof of path safety: a delivered name cannot escape the output directory. |
| `relpath_test.go` | **IMPORTANT** | Proof of relative-path resolution. |
| `resume_test.go` | **IMPORTANT** | Proof of resumption. |
| `quota_test.go` | **IMPORTANT** | Proof of the limit. |
| `tombstone_test.go` | **IMPORTANT** | Proof the duplicate guard works. |
| `staging_lock_test.go` | **IMPORTANT** | Proof the lock works. |
| `digest` / `payload_digest_contract_test.go` | **IMPORTANT** | Proof the digest is computed the same way on both ends. |
| `dropid_test.go` | **IMPORTANT** | Proof of drop identity. |
| `duplicate_report_test.go` | **IMPORTANT** | Proof that a suppressed duplicate is *reported* as suppressed rather than as sent. |
| `rejection_report_test.go` | **IMPORTANT** | Proof that a rejection says what to do next. |
| `maintenance_contract_test.go` | **IMPORTANT** | Proof of the maintenance sweep's contract. |
| `write_failure_test.go` | **IMPORTANT** | Proof of behaviour when the disk refuses. |
| `throughput_bench_test.go` | **IMPORTANT** | The measurement behind the performance budgets. |

---

## `internal/transport/` — the encrypted channel

| File | Class | What breaks without it |
|---|---|---|
| `transport.go` | **EXTREMELY IMPORTANT** | The `Transport` interface every implementation conforms to. |
| `lan.go` | **EXTREMELY IMPORTANT** | The LAN transport: mutual TLS, certificate pinning, address learning. |
| `loopback.go` | **EXTREMELY IMPORTANT** | The loopback transport: the one-machine mode every test and the first five minutes use. |
| `ssh.go` | **EXTREMELY IMPORTANT** | The SSH transport. |
| `lan_test.go` | **IMPORTANT** | Proof of the LAN path, including certificate pinning. |
| `loopback_test.go` | **IMPORTANT** | Proof of the loopback path. |
| `ssh_test.go` | **IMPORTANT** | Proof of the SSH path. |
| `transport_test.go` | **IMPORTANT** | Proof the implementations agree. |
| `ssh_deadline_test.go` | **IMPORTANT** | Proof an SSH dial cannot hang forever. |

---

## `internal/bridge/` — the OAuth relay's trust boundary

The bridge is where a sign-in page's URL travels between two machines.
It is the part of the product that handles credentials, so its rules are
the strictest in the repository.

| File | Class | What breaks without it |
|---|---|---|
| `dispatcher.go` | **EXTREMELY IMPORTANT** | The callback dispatcher: which listener owns which callback. |
| `session.go` | **EXTREMELY IMPORTANT** | A relay session's lifecycle. |
| `session_registry.go` | **EXTREMELY IMPORTANT** | The registry of live sessions and their callback expectations. |
| `rejection.go` | **EXTREMELY IMPORTANT** | The classified rejection: twelve cases, each naming what happened and what to do. |
| `aside.go` | **EXTREMELY IMPORTANT** | The side that receives the callback. |
| `bside.go` | **EXTREMELY IMPORTANT** | The side that opened the relay. |
| `cancel.go` | **EXTREMELY IMPORTANT** | Abandoning a relay without leaking a callback port. |
| `heartbeat_client.go` | **IMPORTANT** | Keeping a relay session alive. |
| `cancel_client.go` | **IMPORTANT** | Asking the far side to abandon a relay. |
| `callback_expectation_test.go` | **IMPORTANT** | Proof that a callback is only accepted from the URL that asked for it. |
| `e2e_test.go`, `e2e_cancel_test.go`, `e2e_ssh_test.go`, `e2e_lan_test.go`, `e2e_multiplex_test.go`, `e2e_form_post_test.go`, `e2e_unstated_path_test.go` | **IMPORTANT** | The end-to-end proofs of the relay across both transports and every callback shape. |
| `aside_test.go`, `bside_test.go`, `session_test.go`, `session_registry_test.go`, `session_registry_window_test.go` | **IMPORTANT** | Proof of each side's behaviour and of the session window. |
| `rejection_test.go` | **IMPORTANT** | Proof that the rejections are classified, not generic. |
| `callback_csrf_test.go` | **IMPORTANT** | Proof that the callback is not forgeable cross-origin. |
| `headers_test.go` | **IMPORTANT** | Proof of the security headers. |
| `fuzz_callback_expectation_test.go` | **IMPORTANT** | The fuzz target; its breaks are the proof that the callback matcher cannot be confused. |
| `heartbeat_client_test.go` | **IMPORTANT** | Proof of the heartbeat. |
| `bind_error_test.go` | **IMPORTANT** | Proof that a port collision is reported as one. |
| `loopback_agreement_test.go` | **IMPORTANT** | Proof that both ends agree on the loopback contract. |
| `dispatcher_test.go` | **IMPORTANT** | Proof that the dispatcher routes each callback to the session that asked for it. |
| `shutdown_drain_test.go` | **IMPORTANT** | Proof that an in-flight transfer gets its drain window and is not killed at ninety percent. |
| `refused_never_on_wire_test.go` | **IMPORTANT** | Proof that a refusal is never serialised onto the wire. |
| `session_replay_test.go` | **IMPORTANT** | Proof that a session cannot be replayed. |
| `unbound_warning_test.go` | **IMPORTANT** | Proof of the unbound-callback warning. |
| `rejection_pages_export_test.go` | **IMPORTANT** | Proof that the rejection pages export. |
| `internal/bridge/testdata/fuzz/FuzzCallbackExpectation/3c06a70ed9c13ea5`, `internal/bridge/testdata/fuzz/FuzzCallbackExpectation/69a5b36abb77d1f9`, `internal/bridge/testdata/fuzz/FuzzCallbackExpectation/d7a41ebd21b98124` | **VERY IMPORTANT** | The fuzz corpus — three seeds, each a callback URL the matcher must classify correctly. Without the seeds the fuzzer starts from nothing and the known-bad inputs are never re-checked. These three files are the *only* things in the repository whose deletion is silent: the fuzz test still passes, having simply never been shown the cases it was written for. |

---

## `internal/hub/` — the product itself

The largest package, and the one the dashboard, the CLI and every
background process live in.

| File | Class | What breaks without it |
|---|---|---|
| `hub.go` | **EXTREMELY IMPORTANT** | The Hub: lifecycle, listeners, sessions, the peer model. |
| `web_dashboard.go` | **EXTREMELY IMPORTANT** | The dashboard server and every API under it. |
| `dashboard.html` | **EXTREMELY IMPORTANT** | The dashboard itself — 5,889 lines carrying the whole interface, the design system, and the behaviour. |
| `ipc.go` | **EXTREMELY IMPORTANT** | The local IPC surface the CLI uses to talk to a running Hub. |
| `operations.go` | **EXTREMELY IMPORTANT** | The operation model: what an operation is, and how it ends. |
| `relay_coordinator.go` | **EXTREMELY IMPORTANT** | Driving a relay from end to end. |
| `relay_history.go` | **EXTREMELY IMPORTANT** | The authorization history the dashboard shows. |
| `logger.go` | **EXTREMELY IMPORTANT** | The live log the dashboard reads. |
| `peer_health.go` | **EXTREMELY IMPORTANT** | Whether a peer is answering, and saying so honestly. |
| `discovery_health.go` | **EXTREMELY IMPORTANT** | Whether discovery is working, and saying so honestly. |
| `relpath.go` | **EXTREMELY IMPORTANT** | Safe path resolution for received files. |
| `unpair_notify.go` | **IMPORTANT** | Telling the other machine it was unpaired. |
| `dashboard` HTML companions: `relay_interstitial.html`, `relay_blocked.html` | **VERY IMPORTANT** | The relay confirmation popup and the dead end behind it. |
| `dashboard_ux_test.go` | **IMPORTANT** | The gates on what the dashboard *is*: one band, a heading-sized destination, an untruncated peer name, a procedure as a list. |
| `served_page_test.go` | **IMPORTANT** | The helper that makes every dashboard gate read the page the browser receives rather than the file on disk. |
| `accessibility_name_test.go` | **IMPORTANT** | Proof that every control has an accessible name. |
| `focus_ring_test.go` | **IMPORTANT** | Proof that every focusable control has a visible focus ring. |
| `failure_wording_test.go` | **IMPORTANT** | Proof that failures say what to do next. |
| `clipboard_shapes_test.go` | **IMPORTANT** | Proof that the clipboard path has its shapes. |
| `probe_fields_test.go` | **IMPORTANT** | Proof of the fields a probe reports. |
| `activity_log_test.go` | **IMPORTANT** | Proof of the activity log's shape. |
| `logger_test.go` | **IMPORTANT** | Proof of the live log's shape and its domain classification. |
| `classify_test.go` | **IMPORTANT** | Proof that a transfer error is classified into the user-language vocabulary, and that the classification is the single source the surfaces share. |
| `dashboard_notify_test.go` | **IMPORTANT** | Proof of the notification path. |
| `dashboard_session_test.go`, `dashboard_session_cookie_test.go`, `dashboard_authz_test.go` | **IMPORTANT** | Proof that the dashboard is authorised and that its session is a cookie. |
| `embedded_html_test.go` | **IMPORTANT** | Proof the embedded pages are served. |
| `web_dashboard_test.go` | **IMPORTANT** | The large gate file over the dashboard's markup and behaviour. |
| `hub_test.go` | **IMPORTANT** | Proof of the Hub's core behaviour. |
| `peer_health_test.go`, `discovery_health_test.go` | **IMPORTANT** | Proof that health is reported honestly. |
| `paired_transfer_e2e_test.go` | **IMPORTANT** | Two real Hubs, one real payload. |
| `reconnection_test.go` | **IMPORTANT** | Proof that a moved peer is found again. |
| `ipc_test.go` | **IMPORTANT** | Proof of the IPC surface. |
| `operations_test.go` | **IMPORTANT** | Proof of the operation model. |
| `relay_coordinator_test.go`, `relay_cancel_test.go`, `relay_history_test.go`, `relay_browser_test.go`, `relay_clear_test.go` | **IMPORTANT** | Proof of the relay's paths. |
| `relpath_test.go`, `relpath_e2e_test.go`, `received_file_path_test.go`, `filename_bound_test.go` | **IMPORTANT** | Proof of path safety. |
| `port_fallback_test.go`, `single_instance_test.go`, `ready_race_test.go`, `hub_stop_test.go`, `hub_stop_run_test.go`, `background_group_test.go` | **IMPORTANT** | Proof of lifecycle behaviour. |
| `perf_budget_test.go`, `progress_test.go`, `upload_failure_test.go` | **IMPORTANT** | Proof of the performance and progress contracts. |
| `staging_test.go`, `session_recovery_test.go`, `recovery_runtime_test.go`, `reconcile_lifecycle_test.go`, `drop_resume_tamper_test.go` | **IMPORTANT** | Proof of recovery. |
| `address_learning_test.go`, `discovered_peers_test.go`, `peer_store_fault_test.go`, `delegation_test.go`, `hub_delegate_test.go` | **IMPORTANT** | Proof of peer resolution and delegation. |
| `unpair_notify_test.go` | **IMPORTANT** | Proof of the unpair notice. |
| `staging_linkcount_windows.go`, `staging_linkcount_unix.go`, `staging_linkcount_other.go` | **IMPORTANT** | The Hub's own hardlink count, per platform. The staging path uses it to notice that a file it was writing has been replaced underneath it. |
| `failure` / `clipboard` / `probe` companion tests | **IMPORTANT** | Proof of each contract. |

---

## `internal/drop/`, `internal/transport/`, `internal/protocol/` — covered above.

---

## `internal/webui/` — the design system

Every surface in the product draws from this directory. Nothing here is
product logic; all of it is the vocabulary the product is built from,
and a surface that does not get it looks like a different product.

| File | Class | What breaks without it |
|---|---|---|
| `theme.css` | **EXTREMELY IMPORTANT** | The tokens, the type scale, the spacing scale and the icon set. Every colour and every measurement in the product comes from here. |
| `components.css` | **EXTREMELY IMPORTANT** | The component layer: the button vocabulary, the empty state, the message, the destination bar, the focus ring, the typeface. Without it, the node's pages render an empty state as a paragraph of body text. |
| `webui.go` | **EXTREMELY IMPORTANT** | The embed point. The design system is served at one marker, so a page cannot get the tokens without the components. |
| `relay_shell.css` | **VERY IMPORTANT** | The popup's own shape — its density and its focus behaviour — built from the shared primitives. |
| `relay_interstitial` HTML companions: `relay_ok.html`, `relay_failed.html` | **IMPORTANT** | The two one-pagers a browser is shown after a relay. |
| `webui_test.go` | **IMPORTANT** | Proof that no page carries its own palette, and that every page includes the design system exactly once. |

---

## `internal/discovery/` — convenience

Discovery finds a peer on the LAN so you do not have to type its
address. **Pairing by address always works**, so this is a convenience
and not a dependency — which is exactly why its class is not higher.

| File | Class | What breaks without it |
|---|---|---|
| `discovery.go` | **VERY IMPORTANT** | mDNS and broadcast discovery. |
| `health.go` | **VERY IMPORTANT** | Whether discovery is working, and saying so honestly. |
| `discovery_test.go`, `health_test.go`, `binds_test.go`, `health_sentence_test.go`, `shutdown_coherence_test.go`, `unannounceable_test.go` | **IMPORTANT** | Proofs of the discovery behaviour and of the sentences it reports. |

---

## `internal/browser/` — opening the dashboard

| File | Class | What breaks without it |
|---|---|---|
| `browser.go` | **VERY IMPORTANT** | Opening the dashboard in the user's browser. Without it the product works and the user just has to paste a URL. |
| `browser_test.go` | **IMPORTANT** | Proof of the sanitised launch. |

---

## `internal/testutil/` — shared fixtures

| File | Class | What breaks without it |
|---|---|---|
| `internal/testutil/sshserver.go`, `internal/testutil/ports.go`, `internal/testutil/e2e_test.go` | **IMPORTANT** | A real SSH server and free-port allocation for the tests that need one. |
| `internal/testutil/oauthclient/client.go`, `internal/testutil/oauthclient/client_test.go`, `internal/testutil/oauthclient/doc.go` | **IMPORTANT** | A real OAuth client for the relay tests. |
| `internal/testutil/oauthserver/server.go`, `internal/testutil/oauthserver/server_test.go`, `internal/testutil/oauthserver/doc.go` | **IMPORTANT** | A real OAuth server for the relay tests. |

---

## `cmd/tantu/` — the command surface

| File | Class | What breaks without it |
|---|---|---|
| `main.go` | **EXTREMELY IMPORTANT** | The entry point: command dispatch, the version, and the single-instance guard. |
| `args.go` | **EXTREMELY IMPORTANT** | The argument contract every command shares. |
| `hub.go` | **EXTREMELY IMPORTANT** | `tantu hub` — the product. |
| `send.go` | **EXTREMELY IMPORTANT** | `tantu send` — the primary journey's CLI half. |
| `pair.go` | **EXTREMELY IMPORTANT** | `tantu pair` — the trust ceremony's CLI half. |
| `unpair.go` | **EXTREMELY IMPORTANT** | `tantu unpair` — revoking trust. |
| `status.go` | **EXTREMELY IMPORTANT** | `tantu status` — the one command that tells you what state the machine is in. |
| `doctor.go` | **EXTREMELY IMPORTANT** | `tantu doctor` — the command that names the next action. |
| `receive.go` | **EXTREMELY IMPORTANT** | `tantu receive`. |
| `transfers.go` | **EXTREMELY IMPORTANT** | `tantu transfers`. |
| `open.go` | **EXTREMELY IMPORTANT** | `tantu open` — opening a received file in the user's editor. |
| `cockpit.go` | **EXTREMELY IMPORTANT** | The terminal UI: 1,121 lines that draw the whole product in a terminal. |
| `dashboard.go` | **EXTREMELY IMPORTANT** | `tantu dashboard` — the one-time link that opens the dashboard. |
| `hardening.go` | **EXTREMELY IMPORTANT** | The hardening the paths and the atomicity depend on. |
| `multifile.go`, `multifile_send.go` | **VERY IMPORTANT** | Multi-file sends, with the batch semantics rule 6 of the surface matrix demands. |
| `hub_delegate.go` | **VERY IMPORTANT** | Delegating a command to a running Hub. |
| `drop.go`, `relay.go`, `node.go`, `serve.go`, `wrap.go` | **VERY IMPORTANT** | The compatibility surfaces: the legacy web sender, the legacy ticket listener, the combined receiver, the specialised listener, the CLI wrapper. |
| `nodepage.css`, `nodepage.go` | **IMPORTANT** | The shell the node's two pages share. |
| `staging_linkcount_windows.go`, `staging_linkcount_unix.go`, `staging_linkcount_other.go` | **IMPORTANT** | Counting a file's links, per platform — the hardlink count the staging path uses to notice a replaced file. |
| `cockpit_test.go`, `cockpit_box_test.go`, `cockpit_reach_test.go`, `cockpit_snippet_test.go`, `cockpit_signin_test.go` | **IMPORTANT** | The cockpit's geometry, its reachability and its snippets. |
| `nodepage_test.go` | **IMPORTANT** | The node pages' keyboard reachability, labels, live regions, typeface and palette. |
| `pair_sas_test.go`, `pairingport_test.go`, `peer_resolution_test.go` | **IMPORTANT** | Proof of the CLI's trust and peer-resolution behaviour. |
| `doctor_test.go`, `doctor_reach_test.go`, `doctor_store_fault_test.go` | **IMPORTANT** | Proof that the doctor tells the truth, including when the store is broken. |
| `perf_budget_test.go` | **IMPORTANT** | The CLI's performance budget. |
| `hardening_test.go` | **IMPORTANT** | Proof of the hardening. |
| `multifile_test.go`, `multifile_relpath_test.go`, `multifile_batch_test.go` | **IMPORTANT** | Proof of the batch semantics. |
| `transfer_dispatch_test.go`, `transfer_clear_test.go`, `send_json_test.go`, `send_path_test.go` | **IMPORTANT** | Proof of the send and transfer commands. |
| `open_json_test.go`, `unpair_notify_test.go`, `hub_delegate_test.go`, `publish_atomic_test.go`, `present_started_hub_test.go`, `args_test.go` | **IMPORTANT** | Proof of the command contracts. |

---

## `tools/` — the gates and the harness

These are dev-only. None of them ships in the binary. All of them decide
whether a change is allowed to land.

### `tools/uxtest/` — the browser acceptance suite

| File | Class | What breaks without it |
|---|---|---|
| `run.mjs` | **EXTREMELY IMPORTANT** | The only gate that sees a rendered page. It measures contrast in both themes, the real keyboard tab order, 200% zoom, a narrow viewport, reduced motion, and the console error log — on three engines. |
| `selfcheck-settle.cjs`, `selfcheck-engine-failure.cjs`, `selfcheck-narrow-viewport-button.cjs`, `selfcheck-narrow-viewport-card-head.cjs`, `selfcheck-overflow-probe.cjs` | **EXTREMELY IMPORTANT** | The red proofs. Each drives a real browser to hold the harness to account, so a detector that has quietly stopped matching fails *itself*. Deleting them leaves a harness that cannot be shown to be able to fail. |
| `settle.cjs`, `engine-failure.cjs` | **VERY IMPORTANT** | The conditions those self-checks hold to account, kept in their own modules so they can be tested rather than trusted. |
| `README.md` | **IMPORTANT** | How to run the suite and what each check means. It is the second `README.md` in this repository — the other is at the root — which is why the gate resolves a file name inside a section's own directory rather than by name alone. |
| `populated.mjs` | **IMPORTANT** | Seeds realistic state and captures every tab. This is how the surfaces get *looked at* rather than only measured. |
| `nodepages.mjs` | **IMPORTANT** | The same for the node's own pages, which had no capture tool at all. |
| `shot.mjs` | **IMPORTANT** | A one-shot capture of a single view. |
| `package.json`, `package-lock.json` | **IMPORTANT** | The pinned Playwright version. The lockfile is what makes the harness's behaviour reproducible across machines. |

### `tools/docgate/`, `tools/encgate/`, `tools/wfcheck/`

| File | Class | What breaks without it |
|---|---|---|
| `tools/docgate/main.go` | **EXTREMELY IMPORTANT** | The gate that keeps the documentation matching the code: the CLI surface, the documented flags, the environment variables, the KNOWN-LIMITATIONS row shape, and every CI gate. |
| `tools/docgate/limitations.go` | **VERY IMPORTANT** | The row-shape and severity rules of the register. |
| `tools/docgate/main_test.go`, `tools/docgate/limitations_test.go` | **IMPORTANT** | Proof that the gate catches what it claims to. |
| `tools/docgate/inventory_test.go` | **IMPORTANT** | The red proofs of the inventory gate itself: a tracked file that is not classified, a table that names a file which is not there, a glob mistaken for a path, and a repository the gate cannot read. Without them the inventory could be quietly satisfied by classifying nothing. |
| `tools/docgate/inventory.go` | **IMPORTANT** | The inventory gate itself: every tracked path classified, every named path still present, the class vocabulary spelled one way, and the summary table recomputed from the tables beneath it. |
| `tools/encgate/main.go` | **EXTREMELY IMPORTANT** | The gate that no tracked text file has encoding damage. A mojibake'd source file is a defect that every other gate reads as valid. |
| `tools/encgate/main_test.go`, `tools/encgate/damage_shapes_test.go` | **IMPORTANT** | Proof the gate detects each shape of damage. |
| `tools/wfcheck/main.go` | **VERY IMPORTANT** | Parses the shell inside the workflows. `wfcheck` only ever read them as text, and a malformed workflow was reported as clean. |
| `tools/wfcheck/main_test.go`, `tools/wfcheck/pipefail_test.go`, `tools/wfcheck/browser_matrix_test.go`, `tools/wfcheck/dashboard_case_test.go` | **IMPORTANT** | Proof that the workflow checker sees what it claims to. |

---

## `docs/` — the record

The documentation is not decoration here. It is where the product's
*claims* live, and several of the gates exist only to keep the claims
true.

| File | Class | What breaks without it |
|---|---|---|
| `THREAT-MODEL.md` | **EXTREMELY IMPORTANT** | The threat model. It is the reason the design decisions are the design decisions, and the only way to check a new feature against them. |
| `KNOWN-LIMITATIONS.md` | **EXTREMELY IMPORTANT** | The register that stops the repository lying about itself. Referenced 73 times; `docgate` enforces its shape. |
| `ARCHITECTURE.md` | **EXTREMELY IMPORTANT** | The map. Referenced 32 times. |
| `DEV-RECORD.md` | **VERY IMPORTANT** | The evidence trail: every batch, every red proof, every rejected hypothesis. 10,013 lines. Not needed at runtime and irreplaceable when a claim is questioned. |
| `RELEASE.md` | **VERY IMPORTANT** | Install, upgrade and rollback. Referenced 38 times, including by the release workflow. |
| `DEPENDENCIES.md` | **VERY IMPORTANT** | The inventory and the reasoning behind every pinned dependency. Referenced 23 times. |
| `DECISIONS.md` | **VERY IMPORTANT** | The decision log: what was chosen, and why the alternative lost. |
| `UX-STATUS.md` | **VERY IMPORTANT** | The living UX record. Referenced 13 times. |
| `EVIDENCE-TWO-MACHINE.md` | **VERY IMPORTANT** | The two-machine procedure. The only document that says how to prove the product works between two laptops. |
| `SPEC-WIRE-VERSIONING.md` | **VERY IMPORTANT** | Wire compatibility rules. Referenced 11 times. |
| `SURFACE-MATRIX.md` | **VERY IMPORTANT** | The rules that keep the surfaces converged. |
| `AUDIT.md` | **IMPORTANT** | A point-in-time audit. Its value is the snapshot; it does not need to be current to be worth keeping. |
| `UX-PLAN-MAP.md` | **IMPORTANT** | A map of the UX plan's structure. |
| `RESEARCH-PACKET.md` | **IMPORTANT** | The research questions the product's next claims should be tested against. |
| `REVIEW-REJECTION-PAGES.md` | **IMPORTANT** | A focused review of the rejection pages. |
| `REPO-INVENTORY.md` | **IMPORTANT** | This document. It is a claim about the repository, and it is the one claim here that decays on its own -- so `tools/docgate` checks it both ways, and the check is what keeps the rest of this table worth reading. |

---

## The genuinely useless things (none of them tracked)

These were on disk when this document was written. **None is in git**, and
none should be.

| Artefact | Class | What it is | What to do |
|---|---|---|---|
| `docgate.exe`, `encgate.exe` (repository root) | **USELESS** | Stray binaries from a `go build` without `-o`. 6.7 MB of nothing. | **Deleted on 2026-10-26.** `go build ./...` and `go run ./tools/encgate` are how these are meant to be produced. |
| `temp/` | **USELESS** *as a location* | A scratch directory holding two working documents — one of them a 79 KB upgrade plan, which is real work but does not belong in the repository. | Move the documents somewhere deliberate, or keep them; delete the directory. It is gitignored either way. |
| `dist/` | **USELESS** *as a location* | Build output: the binaries and the two-laptop test kit. | Keep it if you are about to test on two laptops; it regenerates in seconds. It is gitignored. |
| `tools/uxtest/node_modules/` | **USELESS** *as a location* | 100 MB of Playwright. | Never commit it. The lockfile pins the version; `npm ci` restores it. |

The distinction matters: **"useless" describes a file's *contribution*, not
its origin.** `temp/tantu-ultimate-ux-upgrade-plan.md` is a 79 KB plan
with real content and the wrong location; `docgate.exe` is a binary with
no content and no location worth keeping. Both are outside the
repository's history, and neither is in the classified tables above,
because the tables classify *tracked* files.

---

## What breaks first, in order

If something has to be deleted, this is the order in which the damage
stays survivable.

1. `docs/AUDIT.md`, `docs/UX-PLAN-MAP.md`, `docs/RESEARCH-PACKET.md`,
   `docs/REVIEW-REJECTION-PAGES.md` — snapshots and background. Nothing
   breaks; the repository knows less.
2. The `*_test.go` files — nothing breaks *today*; the next change
   becomes riskier, and the defect class they guard comes back.
3. `tools/*/main_test.go` — the gates keep working but lose their red
   proofs, which is worse: a gate that has never been seen to fail is a
   gate that has never been tested.
4. `internal/webui/components.css` — **not survivable.** The node's
   pages lose their empty state, their buttons and their focus ring, and
   nothing goes red.
5. `internal/protocol/` — **not survivable.** Every installed version
   stops agreeing with every other.
6. `internal/pairing/` — **not survivable.** Trust becomes a claim
   rather than a mechanism.
7. `.github/workflows/` — **not survivable.** Nothing is checked any
   more, and it looks exactly like everything is fine.

---

## How this document is kept honest

The inventory is a claim about the repository, and a claim about a
repository drifts the moment somebody adds a file. So it is gated.

`tools/docgate` checks five things about this document:

1. **Every tracked path is classified.** A new file that nobody has
   said what it is for fails the gate.
2. **Every path the inventory names still exists.** A file that has
   been renamed or deleted fails the gate, because a table that
   describes a file which is not there is a claim about nothing.
3. **The class vocabulary is exactly the four names.** The first
   version of this document spelled the classes `**EXTREMELY**` and
   `**VERY**` in some tables and `**EXTREMELY IMPORTANT**` in others.
   A class spelled two ways is a class nobody can count.
4. **The summary table is recomputed from the tables beneath it.** The
   counts in the summary are not trusted; they are derived from the
   rows and compared. A summary that has drifted from its own rows is
   the first thing a reader looks at, and the one thing that reading
   does not check.
5. **The number in the opening paragraph is the real one.** It is a
   claim in a sentence, and a sentence is the one place a gate cannot
   reach by counting rows -- and it is the number that goes stale
   first.

The check is deliberately mechanical. It cannot say whether a class is
the *right* class -- that is judgement, and it stays with a reviewer.
What it can say is that the table is a complete and current picture of
what is in the repository, which is the property that decays on its own.

The gate has a red proof of its own, in
`tools/docgate/inventory_test.go`: a tracked file that is not classified,
a table that names a file which is not there, a glob mistaken for a
path, a repository it cannot read, and a missing document. Each of
those must make the gate complain.

**A note on how a file name is resolved.** A row under
`internal/drop/` that says `staging.go` means
`internal/drop/staging.go`. That resolution is not a convenience: this
repository has three files called `main.go`, two called `hub.go` and
three called `e2e_test.go`, so a file name on its own does not say which
file it means -- and a table that cannot say which file it means is a
table nobody can check.
