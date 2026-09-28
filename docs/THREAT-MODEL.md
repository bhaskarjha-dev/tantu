# Tantu: Threat Model

> **Status:** Active Reference Threat Model  
> **Last Updated:** 2026-09-24

---

## 1. System Boundary

`tantu` operates as an encrypted, mutually authenticated bidirectional mesh across machines (workstations, dev boxes, cloud VMs, WSL2).

```
┌─ Trust Boundary ────────────────────────────────────────────────────────────┐
│                                                                              │
│  Node 1 (Workstation)            mTLS 1.3 Pipe        Node 2 (Dev Server)    │
│  ┌────────────────────────┐  (ECDSA P-256 Pinning)    ┌───────────────────┐  │
│  │  tantu Hub             │◄─────────────────────────►│  tantu Hub        │  │
│  │  • Loopback (:9876*)   │                           │  • Loopback (:9876)│  │
│  │  • Wire (:9877)        │                           │  • Wire (:9877)   │  │
│  │  • Default/Active Target│                          │  • OAuth / Drop Svc│  │
│  └────────────────────────┘                           └───────────────────┘  │
│              ▲                                                   ▲           │
│              │ (Strict 127.0.0.1)                                │ (Loopback)│
│              ▼                                                   ▼           │
│         Web Browser                                        Local CLI / App   │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Assets to Protect
1. **OAuth Authorization Codes:** Transits the encrypted bridge during browser redirect callbacks.
2. **Access & Refresh Tokens:** Never transit the bridge — exchanged directly between application node and identity provider.
3. **PKCE Code Verifier:** Generated and kept exclusively on the initiating application node (RFC 7636).
4. **QuickDrop File & Snippet Payloads:** P2P file streams, code snippets, tokens, and binary assets (up to 5GB).
5. **Node Pairing Credentials:** ECDSA P-256 private keys (`identity.json`) and trusted peer certificates (`peers.json`).
6. **Daemon State Descriptor:** Ephemeral process coordinates (`hub.json`) for local REST IPC.
7. **Transfer History (`transfers.json`):** Sender-side metadata only (destinations, states, times) — no payload; same sensitivity as peer metadata, same private/atomic handling.

---

## 2. Threat Analysis

### T1: Authorization Code Interception on Bridge Transport
| Field | Detail |
|-------|--------|
| **Threat** | Attacker intercepts authorization code in transit between nodes |
| **Impact** | Medium — code alone is unusable without the PKCE `code_verifier` |
| **Mitigation** | TLS 1.3 / SSH encryption; PKCE renders intercepted authorization codes useless |
| **Residual Risk** | Low — requires both transport compromise and code_verifier exfiltration |

### T2: Rogue Bridge Node (Compromised Peer)
| Field | Detail |
|-------|--------|
| **Threat** | A rogue machine impersonates a paired peer to capture authorization codes or inject drops |
| **Impact** | High — unauthorized access to callbacks or arbitrary file transfers |
| **Mitigation** | Strict mutual TLS certificate fingerprint pinning (`peers.json`); out-of-band SAS verification during pairing |
| **Residual Risk** | Low — attacker cannot establish mTLS without possessing the pinned private key |
| **Compat note** | OAuth flows whose authorization URL carries no `redirect_uri` (legacy/synthetic callers) skip callback path/state binding and accept any localhost path. This preserves backward compatibility; real provider flows always carry `redirect_uri` and are fully constrained. Localhost delivery + single-attempt handoff still apply. |

### T3: Unauthorized Inbound Connection on Wire Socket (:9877)
| Field | Detail |
|-------|--------|
| **Threat** | Unpaired network entity attempts to connect to port 9877 |
| **Impact** | High — potential reconnaissance or exploit attempt |
| **Mitigation** | Defence in depth, in order:<br>1. Non-TLS traffic is rejected at the transport layer.<br>2. TLS 1.3 handshake completes for any client, so that zero-friction in-band pairing works (`AllowPairing: true`). The Dispatcher then applies a 30-second read deadline and inspects the first envelope.<br>3. Only `pair_hello` reaches pairing, and pairing requires interactive human approval with a session-bound SAS before any trust is written.<br>4. Every other operation — `drop_send`, `bridge_request` — is gated on `IsPeerTrusted` against `peers.json`. An unauthenticated request is rejected and cannot trigger a browser launch or a file write.<br>5. Loopback accepts any local connection (same-user boundary) and SSH requires key authentication; neither binds a non-loopback interface without a key check. |
| **Residual Risk** | **Low, not negligible.** Step 2 means an unpaired host can complete a TLS handshake and open one connection. It cannot reach any operation beyond `pair_hello`, and it is still subject to handshake and session caps, but "reaches the pairing entry point" is a real, deliberate property of the design rather than a non-event. |

### T4: Port Collision / Hijacking on Callback Listener
| Field | Detail |
|-------|--------|
| **Threat** | Another process on the browser machine binds the callback port before the bridge, capturing the auth code |
| **Impact** | Medium — auth code delivered to wrong process |
| **Mitigation** | Exact port matching; bridge verifies listener binding; binds to `127.0.0.1` exclusively; fails fast if port is in use |
| **Residual Risk** | Low — PKCE protects token exchange |

### T5: Phishing via Malicious URL Opening
| Field | Detail |
|-------|--------|
| **Threat** | Malicious peer sends phishing URL to browser node via `bridge_request` |
| **Impact** | High — user tricked into entering credentials |
| **Mitigation** | Cockpit and Web UI prominently display the target URL and origin peer; URL schema validation (HTTP/HTTPS only) |
| **Residual Risk** | **Medium, and higher than "Low".** `file://`, `javascript:`, and `data:` are rejected, but there is **no allowlist of authorization endpoints**: a paired peer can make the A-side open any http(s) URL, including an internal address or a credential-harvesting lookalike, in a browser session already authenticated to every provider. The peer may also bind a loopback port of its choosing and receive any callback landing there. This is reachable by an already-paired peer, so it is not a network attack, but the blast radius is the user's whole browser session. Tracked as an open limitation, not a closed one. |

### T6: Denial of Service / Resource Exhaustion
| Field | Detail |
|-------|--------|
| **Threat** | Flooding bridge with spurious requests, hanging connections, or massive payloads |
| **Impact** | Low to Medium — process resource exhaustion |
| **Mitigation** | 4 MiB general frame cap and 64 KiB typed control-frame cap (enforced on encode and decode); 5GB file transfer cap; 1MB streaming chunks directly to disk; Hub dashboard caps concurrent 5 GiB multipart uploads at 4 (excess fails fast with 503 + Retry-After); each long-lived receiver process reserves aggregate transfer capacity (4 sessions / 8 GiB, 2 / 6 GiB per peer by default), including unknown-size streams, and trims Tantu-owned failed partials to an 8 GiB / 1,024-file retained-partial budget; activity markers and a cross-process maintenance lock prevent cooperative Tantu processes from unlinking active partials; unmarked direct legacy `.part` files are preserved for manual review; 30-second handshake deadline on wire connections via `Deadliner`; active connection tracking with immediate teardown on shutdown |
| **Residual Risk** | Low |

### T7: Man-in-the-Middle on LAN During Initial Pairing
| Field | Detail |
|-------|--------|
| **Threat** | Active network attacker intercepts pairing handshake |
| **Impact** | High — malicious peer certificate accepted |
| **Mitigation** | Short Authentication String (SAS): a 54-bit word code derived by `pairing.TranscriptSAS` from a hash over the live pairing transcript — both certificate fingerprints plus both freshly generated nonces. Because the code is session-bound it differs on every attempt, and because discovery no longer broadcasts any identity it cannot be pre-computed from the network. The user compares the code on both screens before trust is recorded. |
| **Residual Risk** | **Low, and only if the user actually compares the code.** The value shown is 54 bits and unguessable, so an active MITM cannot match it without the user's participation — but a user who approves a prompt without reading it has accepted the peer. The code is not, and is not claimed to be, a defence against a user who does not verify. |

### T8: Stale Listener / Zombie Socket
| Field | Detail |
|-------|--------|
| **Threat** | Ephemeral callback listener remains open after auth completion, or after a user abandons a sign-in without completing it |
| **Impact** | **Medium, not Low.** Bound to loopback, so it is not remotely reachable — but a held port is not idle. An application's `redirect_uri` port is normally fixed, so an abandoned listener does not merely waste a socket: it **blocks the user's next attempt** with "port is in use", for the full session timeout. The only previous recovery was restarting the process, which on a headless node can mean losing the login session too |
| **Mitigation** | Four paths, in order of immediacy:<br>1. Automatic teardown immediately following `bridge_complete`.<br>2. A 5-minute timeout watchdog, distinguished from a user cancellation by cause so an abandoned sign-in is never reported as a failed login.<br>3. **`bridge_cancel`**: the peer sends it when the user cancels, and the session is released at once — listener closed, session de-registered, wire wait ended. It travels on its own connection, because the session's connection is busy waiting for the callback.<br>4. **Port supersession**: a new sign-in for a port an older in-flight session still holds releases the stale one first, so a retry always proceeds instead of failing to bind.<br>Cancellation is gated exactly like the operation it undoes: `IsPeerTrusted` rejects an unpaired peer before the handler runs, and the registry additionally refuses a peer that does not own the session. Release waits for the listener to be **closed**, not merely for cancellation to be requested, so a retry cannot race the teardown |
| **Residual Risk** | **Low.** The authorization page already open in the browser is not closed — no supported signal distinguishes a tab the user still wants from one they abandoned, so the user closes it themselves (KNOWN-LIMITATIONS 3.10). This is a cosmetic leftover, not a held port |

### T9: Cross-Peer Impersonation in Multi-Peer Mesh
| Field | Detail |
|-------|--------|
| **Threat** | Peer A claims an incoming QuickDrop or OAuth flow originated from Peer B |
| **Impact** | Medium — spoofed sender attribution in UI and inbox |
| **Mitigation** | **Infallible Zero-Trust Sender Attribution:** `hub.go` extracts the raw TLS 1.3 client leaf certificate directly from the underlying `transport.Conn.PeerIdentity()`. It matches `sha256(leaf.Raw)` against `peers.json`. Application-level payload fields (`FromPeer`) are ignored for cryptographic provenance. Connections without a verified leaf certificate are strictly marked `"Unauthenticated Peer"` with zero fallback heuristics |
| **Residual Risk** | Negligible — mathematically impossible to forge without peer's private key |

### T10: Local State File (`hub.json`) Tampering & IPC Hijacking
| Field | Detail |
|-------|--------|
| **Threat** | Unprivileged local process modifies `hub.json` to redirect CLI delegation to a malicious port |
| **Impact** | Medium — CLI commands (`send`, `open`) routed to attacker's process |
| **Mitigation** | `hub.json` is private/atomic and protected by a cross-process lock; each Hub listener generation rotates its IPC/relay capability; peer and identity mutations use separate cross-process locks around load-modify-write publication; delegation probes `/api/probe` with the capability and matches PID/start time and the recorded loopback endpoint before sending work. The capability is bound to the runtime endpoint to prevent forwarding it to an unrelated local listener. |
| **Residual Risk** | Low — requires local user account compromise |

### T11: Path Traversal & Arbitrary File Overwrite in QuickDrop
| Field | Detail |
|-------|--------|
| **Threat** | Peer sends filename containing path traversal sequences (e.g., `../../etc/passwd`), NTFS alternate data streams (`file:stream`), or Windows reserved DOS device names (`CON`, `PRN`, `AUX`, `NUL`, `COM1-9`, `LPT1-9`) |
| **Impact** | High — arbitrary file overwrite or filesystem lockup on receiver's machine |
| **Mitigation** | Receivers cleanse incoming filenames using `SanitizeDropFilename()`: normalizes slashes, extracts basename, strips NTFS ADS colons and Win32-invalid characters, strips ASCII control characters (0–31, 127), trims trailing dots/spaces, prepends `drop_` to Windows reserved DOS device names, and falls back to `drop.bin` for empty/invalid paths. Resumable partials are bound to a private manifest containing transfer metadata and a 64 KiB head hash; mismatched or malformed manifests are rejected rather than combined. Private staging operations use verified directory handles; files write strictly into the configured downloads directory and existing files receive numeric collision suffixes rather than being overwritten. |
| **Residual Risk** | Negligible |

### T12: External Network Access to Web Dashboard & REST IPC
| Field | Detail |
|-------|--------|
| **Threat** | External attacker on the same Wi-Fi/LAN queries `http://<ip>:9876/api/status` or attempts to trigger drops |
| **Impact** | High — unauthorized file access and remote command execution |
| **Mitigation** | Web Dashboard and REST IPC bind **strictly to `127.0.0.1`** (loopback only). They never bind to `0.0.0.0` or external network interfaces |
| **Residual Risk** | Negligible |

### T13: Cross-Origin API Abuse & CSRF via Web Browser
| Field | Detail |
|-------|--------|
| **Threat** | Malicious website visited by developer in their browser executes cross-origin `fetch()`/`XHR` calls to `http://127.0.0.1:9876/api/*` (e.g., `/api/open-folder`, `/api/config`, `/api/status`) or exploits wildcard CORS |
| **Impact** | High — drive-by configuration tampering, local directory opening, or sensitive mesh state leakage |
| **Mitigation** | Elimination of wildcard CORS; exact loopback authority/Origin/Referer validation; sensitive `/api/*` reads and mutations require an IPC capability or one-time HttpOnly dashboard session; the legacy GET relay requires an explicit capability, else renders a confirmation shell that authorizes nothing. |
| **Residual Risk** | Negligible |

---

## 3. Security Requirements

| # | Requirement | Mitigates |
|---|------------|-----------|
| **SR1** | All wire traffic must be encrypted via TLS 1.3 (ECDSA P-256) or SSH | T1, T2, T7 |
| **SR2** | Mutual authentication required: client and server certificates pinned in `peers.json` for operational flows; in-band pairing isolated to `pair_hello` with mandatory SAS verification | T2, T3 |
| **SR3** | Ephemeral callback payloads must never be stored, logged, or inspected | T1, T2 |
| **SR4** | Web Dashboard, REST IPC, and OAuth callback listeners must bind strictly to `127.0.0.1` | T4, T12 |
| **SR5** | Callback listeners must be torn down immediately after `bridge_complete`, after a user cancellation, or on timeout. A new sign-in that needs a callback port an older in-flight session still holds must release the stale session rather than fail to bind | T8 |
| **SR6** | URLs to be opened in browser must be validated (HTTP/HTTPS only) and displayed to user | T5 |
| **SR7** | Maximum session duration enforced (5 minutes default) | T6, T8 |
| **SR8** | Envelope payload size capped at 64KB for control messages; files chunked at 1MB | T6 |
| **SR9** | Pairing requires out-of-band visual verification of a session-bound SAS word code | T7 |
| **SR10**| PKCE `code_verifier` must never leave the initiating node | T1, T2 |
| **SR11**| Inbound sender identity must be cryptographically extracted from TLS client leaf cert | T9 |
| **SR12**| Runtime state descriptor (`hub.json`), key material, peer state, transfer history (`transfers.json`), and active-peer selection must use private permissions and ownership-safe publication; peer/identity mutations are serialized across processes; OS ACL enforcement is a deployment requirement | T10 |
| **SR13**| Received files must be sanitized via `SanitizeDropFilename` (traversal, NTFS ADS, DOS devices, control chars) and saved in sandboxed dir | T11 |
| **SR14**| Existing files must not be silently overwritten by incoming drops | T11 |
| **SR15**| Dynamic port fallbacks apply only to local loopback web/IPC, never to OAuth callbacks | T4 |
| **SR16**| Web Dashboard REST API (`/api/*`) must enforce exact-authority anti-CSRF validation and capability/session authorization; wildcard CORS and reusable HTML-embedded tokens are prohibited. Scoped exception: the legacy standalone `tantu drop` UI (not the Hub dashboard) embeds a per-process loopback CSRF token in its page for `/api/*` header auth. Rationale: that token never appears in URLs (no history/bookmark/log exposure), the page is served `no-store`, and every request still requires exact loopback authority plus Origin/Referer validation, so it is unusable cross-origin. Residual: any local process running as the same user can read the page and call the API — inside the same-user trust boundary (see T10/SR12). The standalone `tantu relay` page instead renders per-request one-time tickets (single-use, 10-minute TTL) with the per-process token only as a legacy fallback for previously rendered bookmarklets. | T13 |
| **SR17**| Inbound wire connections must enforce read deadlines during initial handshake via `Deadliner` | T6 |
| **SR18**| Long-lived receivers must bound aggregate transfer reservations, including unknown-size streams, and release reservations on every terminal path | T6 |
| **SR19**| A resumable partial must have a valid private manifest bound to its transfer metadata and 64 KiB head hash; mismatched or malformed manifests must never be resumed. This is prefix identity, not a full-content or sender-identity proof | T11 |
| **SR20**| Staging maintenance must use verified directory handles, must not remove active partials or unmarked user files, and direct legacy `.part` files without a valid Tantu manifest require manual review | T6, T11 |
| **SR21**| An OAuth callback listener must accept a request only as a top-level browser navigation: `Sec-Fetch-Mode` must be `navigate` and `Sec-Fetch-Dest` must not name a subresource when those headers are present, and a supplied `Origin` must match the authorization server's origin. These headers are unforgeable from a web page and are tolerated when absent so non-browser clients keep working | T1, T2 |
| **SR22**| Application `state` must be compared in constant time on both the A-side and the B-side, and a missing `state` must fail when the authorization request carried one | T1, T2 |
| **SR23**| Discovery beacons must carry no SAS and no certificate fingerprint, and a beacon that still carries either must be rejected outright | T7 |
| **SR24**| A verification code shown to a user must be derived from a session transcript and must not be persisted or broadcast | T7 |
| **SR25**| Releasing an in-flight sign-in must be authorized exactly like starting one: a `bridge_cancel` is refused from an unpaired peer, and refused from a paired peer that does not own the session. A cancel must not become a peer-wide teardown, and an unconfirmed cancel must be reported as unconfirmed rather than as a failure | T8 |
| **SR26**| An in-flight sign-in must be listable and releasable from a local surface. A sign-in nobody can see is a sign-in nobody can abandon except by restarting the process | T8 |

---

## 4. Trust Model Summary

| Entity | Trusted For | NOT Trusted For |
|--------|------------|-----------------|
| **Local Hub** | Managing loopback Web UI, local IPC, and mTLS dispatcher | Exposing services to external network |
| **Paired Peer** | Initiating OAuth requests, sending drops within quotas | Impersonating other peers, modifying PKCE |
| **Transport Layer** | Mutual encryption, certificate pinning, provenance extraction | Opaque payload contents |
| **Identity Provider** | Issuing OAuth tokens and validating PKCE | Inspecting local network topology |
| **Local Filesystem** | Storing `identity.json`, `peers.json`, `transfers.json`, and `hub.json` with private modes and ownership-safe writes | Public shared directories; Windows ACL enforcement remains platform-specific |

---

## 5. Security Posture Conclusion

`tantu` avoids the metadata exposure of a cloud relay and does not leave an
open forward on the remote host the way an ad-hoc `ssh -R` does. Within that
comparison the concrete protections are:

- **Zero-Trust Sender Provenance:** Every byte received is cryptographically bound to a verified TLS leaf certificate.
- **Defense in Depth via PKCE:** Intercepted authorization codes are mathematically useless without the local `code_verifier`.
- **Loopback & Browser-Access Control:** External network interfaces cannot access the Web Dashboard, REST IPC, or local callback listeners; exact-authority checks and capability/session authorization block ordinary cross-origin and tokenless local-browser access, and the bookmarklet confirmation shell authorizes nothing without a click and a valid session. Inline dashboard scripts and OS ACLs remain tracked hardening work.
- **Hardened Filesystem Defense:** Incoming files are sanitized against path traversal, NTFS ADS, Win32-invalid characters, and DOS reserved device conflicts; private staging operations are anchored to verified directory handles. Publication is atomic — the delivered name only ever appears complete — and the reported digest is computed over the bytes that were published. Windows ACL enforcement and power-loss guarantees remain deployment/platform boundaries.
- **Browser-CSRF Defense on the OAuth callback:** a callback is accepted only as a top-level navigation with an `Origin` matching the authorization server, which a web page cannot forge. A local process can still inject one by omitting those headers; that is the same-user boundary recorded in T10 and in `docs/KNOWN-LIMITATIONS.md` §3.8.
- **No Cloud Dependencies:** Traffic travels directly peer-to-peer across LAN or native SSH tunnels with zero third-party metadata leakage.
