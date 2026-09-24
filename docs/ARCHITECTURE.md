# Tantu System Architecture

> **Status:** Active Reference Architecture  
> **Last Updated:** 2026-09-24

---

## 1. Executive Summary

`tantu` is a cross-platform, peer-to-peer developer utility that establishes an encrypted, mutually authenticated bidirectional mesh between machines (e.g., personal laptops, remote development boxes, cloud VMs, and WSL2 environments).

It delivers two primary capabilities over a single unified transport:
1. **Zero-Modification OAuth Callback Forwarding:** Transparently proxies browser-based OAuth 2.0 / PKCE authentication across machines, resolving the classic `localhost` redirect mismatch ([RFC 8252](https://datatracker.ietf.org/doc/html/rfc8252)).
2. **QuickDrop Cross-Machine Sharing:** Securely streams text snippets, tokens, and files/images of arbitrary size (up to 5GB) between paired machines with zero cloud dependencies.

`tantu` operates as a **Multi-Peer Mesh with Dynamic Port Resilience and Zero-Trust Identity Attribution**:
* **Dynamic Port Resilience:** Eliminates port collision crashes with an auto-decrement fallback loop (`9876 → 9875 → 9874 → 9873`) and dynamic wire port binding.
* **Daemon State Discovery (`hub.json`):** An atomic runtime descriptor enables port-agnostic CLI IPC delegation and stale process recovery.
* **Cryptographic Identity Attribution:** Extracts verified TLS 1.3 client leaf certificates to guarantee 100% cryptographic provenance on all inbound drops and OAuth sessions.
* **Multi-Peer Mesh & 5-Tier Fuzzy Resolver:** Supports multiple paired nodes with custom aliases, default routing, and dynamic IP self-healing upon roaming.
* **Progressive UI/UX Parity:** Zero extra friction for single-peer setups, paired with seamless mesh controls in both the Interactive Terminal Cockpit and Single-Page Web Dashboard.

### 1.1 The Invariant: The Tantu Metaphor

The system derives its name from Sanskrit **तन्तु** (*tantu*, from verbal root **√tan** — *to stretch, to extend, to spin*), designating the **warp thread** of a loom. A warp thread is:
1. **Stretched taut** between fixed anchor points (the persistent mTLS channel on port `9877`).
2. **Ambient & persistent** while transient flows pass through (OAuth redirects, QuickDrop streams).
3. **The invisible foundation** enabling complex patterns to be woven across machine boundaries.
4. **Naturally scalable** from a single thread to an interconnected fabric (*tantujāla*, तन्तुजाल mesh).

---

## 2. Core Architectural Philosophy: Symmetric Hub & Dual-Socket Model

`tantu` operates under an entirely symmetric architecture. Any node can initiate an OAuth flow, receive a callback, send a file, or accept a transfer.

```
               NODE 1 (Workstation)                          NODE 2 (Dev Server / Cloud VM)
      ┌────────────────────────────────────┐        ┌────────────────────────────────────┐
      │             tantu hub              │        │             tantu hub              │
      │  • Identity: Keypair / Cert / SAS  │        │  • Identity: Keypair / Cert / SAS  │
      │  • Cockpit ([o,s,t,c,p,v,q])       │        │  • Cockpit ([o,s,t,c,p,v,q])       │
      │  • Multi-Peer Mesh State Machine   │        │  • Multi-Peer Mesh State Machine   │
      │                                    │        │                                    │
      │  ╔══════════════════════════════╗  │        │  ╔══════════════════════════════╗  │
      │  ║   LOOPBACK SOCKET (:9876*)   ║  │        │  ║   LOOPBACK SOCKET (:9876*)   ║  │
      │  ║  * Auto-decrement fallback   ║  │        │  ║  * Auto-decrement fallback   ║  │
      │  ║  • Web Dashboard (4 tabs)    ║  │        │  ║  • Web Dashboard (4 tabs)    ║  │
      │  ║  • 1-Click Bookmarklet Relay ║  │        │  ║  • 1-Click Bookmarklet Relay ║  │
      │  ║  • Server-Sent Events (SSE)  ║  │        │  ║  • Server-Sent Events (SSE)  ║  │
      │  ║  • Local REST IPC Delegation ║  │        │  ║  • Local REST IPC Delegation ║  │
      │  ╚══════════════════════════════╝  │        │  ╚══════════════════════════════╝  │
      │                                    │        │                                    │
      │  ┌──────────────┐┌──────────────┐  │        │  ┌──────────────┐┌──────────────┐  │
      │  │ OAuth Svc    ││ QuickDrop    │  │        │  │ OAuth Svc    ││ QuickDrop    │  │
      │  │ (Client/Svr) ││ (Send/Recv)  │  │        │  │ (Client/Svr) ││ (Send/Recv)  │  │
      │  └──────┬───────┘└──────┬───────┘  │        │  └──────┬───────┘└──────┬───────┘  │
      │         │               │          │        │         │               │          │
      │         └───────┬───────┘          │        │         └───────┬───────┘          │
      │                 ▼                  │        │                 ▼                  │
      │      Multiplexed Wire Dispatcher   │        │      Multiplexed Wire Dispatcher   │
      │       ╔══════════════════════╗     │        │       ╔══════════════════════╗     │
      │       ║  WIRE SOCKET (:9877) ║     │        │       ║  WIRE SOCKET (:9877) ║     │
      │       ╚══════════════════════╝     │        │       ╚══════════════════════╝     │
      └─────────────────┬──────────────────┘        └─────────────────┬──────────────────┘
                        │                                             │
                        │    ╔═══════════════════════════════════╗    │
                        │    ║     PLUGGABLE ENCRYPTED PIPE      ║    │
                        └═══►║                                   ║◄═══┘
                             ║  Direct LAN mTLS (:9877)          ║
                             ║  Native SSH / TCP Loopback        ║
                             ╚═══════════════════════════════════╝
```

### Invariants of the Symmetric Hub:
* **Role Symmetry:** Every node is both a client and a server. No specialized coordinator node is required.
* **Dual-Socket Network Isolation:**
  - **Loopback Socket (`127.0.0.1:9876*`):** Exclusively binds to localhost with automatic fallback (`9876 → 9875 → 9874 → 9873`). Hosts the Single-Page Web Dashboard, Bookmarklet HTTP Relay, Server-Sent Events (SSE) bus, and Local REST IPC server. Never exposed to external network interfaces.
  - **Wire Socket (`0.0.0.0:9877`):** Listens on the network interface for mTLS / SSH / Loopback traffic. Handled by a multiplexed protocol dispatcher that routes incoming connections dynamically.
* **Dynamic State Discovery (`hub.json`):** Written atomically through a cross-process lock after the HTTP listener is ready. The descriptor records the PID, start time, actual Web/Wire endpoints, transport, and a per-process IPC capability; CLI subcommands use an authenticated PID/start-time probe rather than trusting the file or a status-shaped local response.
* **Smart Default Routing:** Outbound drops automatically resolve to the designated `Default` or `Active` peer when unspecified, eliminating flag fatigue.

---

## 3. Subsystem Breakdown

### 3.1 Protocol Wire Framing (`internal/protocol/`)

All communications over a `transport.Conn` utilize length-prefixed JSON envelopes:
```
[ 4-Byte Big-Endian Payload Length (N) ] [ N-Byte JSON Envelope ]
```

#### Envelope Schema:
```json
{
  "type": "message_type_string",
  "payload": { ... }
}
```

#### Message Catalog:

| Category | Type Constant | Direction | Description |
|---|---|---|---|
| **Control** | `heartbeat` | Bi-directional | Keepalive signal to maintain stateful NAT/firewall bindings |
| **OAuth** | `bridge_request` | Client → Server | Requests remote browser to open URL and listen on `callback_port` |
| **OAuth** | `bridge_ack` | Server → Client | Confirms browser launch and active callback listener |
| **OAuth** | `callback_relay`| Server → Client | Relays intercepted HTTP request (headers, query, auth code) |
| **OAuth** | `bridge_complete`| Client → Server | Confirms token exchange complete; authorizes browser success render |
| **QuickDrop** | `drop_send` | Sender → Receiver| Announces drop session (kind, name, size, MIME type, chunk size) |
| **QuickDrop** | `drop_ack` | Receiver → Sender| Approves or rejects drop (e.g. quota, file permission) |
| **QuickDrop** | `drop_data` | Sender → Receiver| Binary payload chunk (1MB default chunking, base64-encoded in JSON) |
| **QuickDrop** | `drop_complete`| Receiver → Sender| Confirms byte-for-byte receipt and file integrity |
| **Pairing** | `pair_hello` | Initiator ↔ Responder| In-band pairing invitation/response with ephemeral SAS, cert, and listen_port |
| **Pairing** | `pair_decision` | Either → Other | In-band confirmation of SAS visual verification (`accepted: true/false`) |

> **Wire Framing & Compatibility:** All protocol communication uses standard 4-byte big-endian length-prefixed frames. The pairing subsystem transparently unmarshals both flat JSON (`{"cert_pem": ...}`) and envelope-nested JSON (`{"type": "pair_hello", "payload": ...}`), ensuring cross-version client compatibility.

> **No decode-time type allowlist (deliberate):** the frame decoder accepts any
> non-empty `type` and enforces only size budgets (4 MiB frames, 64 KiB control
> frames). Type authorization happens at dispatch: the multiplexer routes only
> known types and drops unknown ones (logged, connection closed), and session
> handlers reject unexpected types explicitly. A decoder allowlist would turn
> every future message type into a wire break against older peers without
> adding any security — unknown types already receive no handler and no
> trust — so forward compatibility is preserved at the layer that can
> actually judge a type.

---

### 3.2 Pluggable Transport Layer (`internal/transport/`)

The transport layer exposes a minimal, testable interface with cryptographic identity introspection:
```go
type Transport interface {
    Dial(addr string) (Conn, error)
    Listen(addr string) (Listener, error)
}

type Conn interface {
    Send(msgType string, payload any) error
    Receive() (*protocol.Envelope, error)
    Close() error
    LocalAddr() net.Addr
    RemoteAddr() net.Addr
}

// PeerIdentity is an optional interface implemented by transport connections
// that carry cryptographic peer identity (e.g. mutual TLS).
type PeerIdentity interface {
    PeerFingerprint() string
}

// Deadliner is an optional interface implemented by transport connections
// that support I/O deadlines.
type Deadliner interface {
    SetDeadline(t time.Time) error
    SetReadDeadline(t time.Time) error
    SetWriteDeadline(t time.Time) error
}
```

#### Supported Transports:
1. **Direct LAN mTLS (`lan.go`):** Mutual TLS 1.3 over raw TCP using self-signed ECDSA P-256 certificates with certificate fingerprint pinning on port 9877. Extracts client leaf certificate for zero-trust caller provenance. Features dynamic `IsTrusted` callback in `LANTransportConfig` allowing runtime peer validation from `peers.json` without requiring a daemon restart. Configured with `AllowPairing: true` so incoming unauthenticated TLS clients can complete the TLS handshake and proceed to application-level in-band pairing (`pair_hello`), while non-pairing operations strictly enforce cryptographic peer pinning.
2. **Loopback (`loopback.go`):** TCP loopback on `127.0.0.1` for WSL, Docker, and integration test harnesses (fully implements `Deadliner`).
3. **Native SSH (`ssh.go`):** SSH client/server tunnels using `golang.org/x/crypto/ssh` for cloud servers with existing SSH keys.
4. **Internet WAN Relay (Spec: `docs/SPEC-INTERNET-TRANSPORT.md`):** Zero-trust encrypted relay transport designed for internet-wide traversal without port forwarding.

#### Port Preservation Invariant:
* Address sanitizers (`EnsurePeerLANPort`) strictly preserve user-specified custom ports (e.g., `192.168.1.50:9878`) rather than forcibly overwriting them with default `9877`.

---

### 3.3 Multi-Peer Mesh & Zero-Trust Identity (`internal/pairing/`)

Nodes establish mutual trust without an external Certificate Authority (CA):
* Each node auto-generates an ECDSA P-256 private key and self-signed X.509 certificate on initial launch, persisted in `identity.json`.
* **Short Authentication String (SAS):** The SHA-256 digest of both public certificates is computed and formatted into a 6-character hex code (`pairing.SASCode(fp)`). Users visually verify this code on both screens to defeat Man-in-the-Middle (MitM) attacks.
* **Multi-Peer Store (`peers.json`):** Persists an array of paired nodes with metadata:
  ```json
  {
    "peers": [
      {
        "fingerprint": "a1b2c3d4...",
        "alias": "macbook-pro",
        "name": "air-laptop",
        "address": "192.168.1.100:9877",
        "is_default": true,
        "paired_at": "2026-09-10T20:00:00Z",
        "last_seen": "2026-09-10T21:30:00Z"
      }
    ]
  }
  ```
* **5-Tier Deterministic Fuzzy Resolver (`ResolvePeer(query)`):**
  1. Exact SHA-256 fingerprint match.
  2. SAS 6-character visual code match.
  3. Case-insensitive alias match.
  4. Exact hostname / IP address match.
  5. Substring / Prefix match (minimum 4 characters).
* **Dynamic IP Self-Healing:** When an authenticated peer connects from a new IP (e.g., after DHCP lease renewal or roaming between Wi-Fi networks), the Hub automatically updates the peer's stored address upon receipt of valid mTLS frames.

---

### 3.4 Unified Multiplexed Dispatcher (`internal/bridge/`)

Incoming TCP connections on port `9877` undergo protocol negotiation at the application level:
```
Connection Accepted on 0.0.0.0:9877 (mTLS Handshake Verified)
                     │
             conn.PeerIdentity()
        (Extract Leaf Cert Fingerprint)
                     │
             30s Handshake Deadline
                     │
              Read 1st Envelope
                     │
       ┌─────────────┼─────────────┐
       ▼             ▼             ▼
 [bridge_req]   [drop_send]   [pair_hello]
       │             │             │
       ▼             ▼             ▼
  HandleASide    ReceiveDrop   HandleInboundPairing
```
* **Infallible Zero-Trust Sender Attribution:** In `ReceiveDrop`, the incoming connection's leaf certificate fingerprint (`conn.PeerIdentity()`) is extracted directly and matched against `peers.json`. If matched, `PeerName` is assigned to the peer's verified alias/display name. If unauthenticated, it is marked `(unauthenticated)`. Handlers never trust unauthenticated envelope fields for sender identity, eliminating heuristic spoofing.
* **Handshake Deadline Enforcement:** Dispatcher applies a 30-second read deadline via `Deadliner` during initial envelope inspection, preventing slowloris or unauthenticated connection starvation. Once classified, the deadline is cleared for persistent streams.
* **Prefetched Connection Wrapping:** Using `PrefetchedConn`, the initial envelope is inspected without consuming or losing byte framing for OAuth and QuickDrop. For in-band pairing (`pair_hello`), the dispatcher routes the underlying transport connection directly to `HandleInboundPairing` to ensure clean bi-directional frame exchange.
* **Active Connection Registry:** Dispatcher tracks all live connections with thread-safe synchronization, closing them immediately when the Hub context shuts down.

---

### 3.5 Single-Page Web Dashboard & Observability Engine (`internal/hub/`)

The embedded HTTP server on `127.0.0.1:9876*` provides a dark-mode Web UI with zero external dependencies:
1. **Header Peer Indicator Pill:** Global visual badge indicating active/default peer with quick-selection dropdown.
2. **QuickDrop Tab:** Responsive dual-pane layout featuring drag-and-drop send (up to 5GB), text snippets, peer destination selector (Default / Specific Peer), and a live **Received Items Feed** (inbox) with dynamic peer badges, one-click clipboard copying, direct URL opening, and canonical download location controls (`~/Downloads/tantu`).
3. **OAuth Relay Tab:** 1-click draggable bookmarklet (`javascript:...`) and manual authorization URL submission form (`POST /api/relay/open`).
4. **Peers & Network Tab:** Complete multi-peer management cards with inline alias editing (`POST /api/peers/alias`), default peer toggle (`POST /api/peers/default`), unpair removal (`POST /api/peers/remove`), active peer selection (`POST /api/peers/active`), nearby LAN hubs feed (`GET /api/discovery/peers`), and 1-click visual In-Band Pairing Wizard (`POST /api/pair/initiate`).
5. **Activity Logs Tab:** Live diagnostic explorer with domain filtering pills (`ALL`, `OAUTH`, `DROP`, `PEER`, `NET`, `DEBUG`, `ERROR`), real-time query search bar, structured metadata inspection (`<details>`), and one-click JSON export (`/api/logs`).
6. **Event Bus & RingBuffer:** Circular 200-event in-memory buffer streaming structured JSON events via Server-Sent Events (`/api/events`).
7. **Authenticated Local Control Plane (`securityMiddleware`):** Validates the exact loopback authority (including port), Origin, and Referer, requires an IPC capability or one-time browser session for every sensitive `/api/*` read and mutation, disables caching, caps concurrent multipart uploads (4, with 503 + Retry-After), and uses a one-use relay ticket for the legacy bookmarklet. The long-lived IPC token is never embedded in dashboard HTML. (Scope note: this describes the Hub dashboard. The legacy standalone `tantu relay` page renders per-request one-time tickets with a per-process token fallback; the legacy standalone `tantu drop` page uses a per-process header token documented under THREAT-MODEL SR16.)

---

### 3.6 Smart Local IPC Delegation & In-Band Pairing (`internal/hub/ipc.go`)

To avoid port conflicts and eliminate duplicate daemon instances, CLI commands check for an active Hub:
1. Reads `hub.json` from `pairing.DefaultStoreDir()` to identify the active daemon PID, start time, Web endpoint, and IPC capability.
2. Performs a fast 200ms authenticated probe to `http://127.0.0.1:<web_port>/api/probe`, rejecting stale/replaced descriptors and endpoint mismatches.
3. If running, commands delegate work to the Hub via `POST /api/drop/upload` (for drops) or `POST /api/relay/open` (for URLs), carrying the `--peer` flag and capability when the destination matches the runtime descriptor.
4. If not running, commands transparently fall back to standalone execution.
5. **Seamless In-Band Pairing on Port 9877:** Because the Hub's LAN transport is configured with `AllowPairing: true` and the Dispatcher multiplexes `pair_hello`, incoming pairing requests from remote machines or the Web Dashboard connect directly to port 9877 without requiring an alternate port.
6. **Dedicated Initiator Fallback (`cmd/tantu/pair.go`):** When `tantu pair` is run in initiator mode without arguments and user requests manual pairing code display while the Hub is active on port 9877, it automatically starts a dedicated pairing listener on port 9878 (`pairingPort == 9877 -> 9878`) as an isolated standalone fallback.

---

### 3.7 Interactive Terminal Cockpit & Shell Ergonomics (`cmd/tantu/`)

* **Developer Cockpit:** Displays a clean status banner with streaming colorized event logs and non-blocking single-key hotkeys:
  - `[o]` Open Web Dashboard in default browser
  - `[s]` Send file prompt (with progressive peer destination selector if >1 peer)
  - `[t]` Send text snippet or token to peer (with progressive peer destination selector if >1 peer)
  - `[c]` Clear screen and redraw banner
  - `[p]` Interactive peer switcher & status (toggle active target on the fly)
  - `[v]` Toggle verbose / debug logging
  - `[q]` Graceful shutdown
* **Progressive Disclosure:** In a single-peer mesh, `[s]` and `[t]` send directly with zero extra prompts. In a multi-peer mesh, an intuitive destination prompt appears (`Send to [1: devbox, 2: macbook, or Enter for Active]: `).
* **Headless Server Mode:** Running `tantu --server` (or `-s`) disables terminal interactive input for systemd, Docker, or background daemon operation.
* **Windows Shell Resilience:** Flag normalizer handles unescaped backslashes, stripped quotes from Windows drag-and-drop file paths, and interleaved flags.

---

## 4. Security Architecture & Threat Mitigations

1. **Loopback-Only Web & IPC Surface with Anti-CSRF:** The Web Dashboard, REST IPC, and SSE stream bind strictly to `127.0.0.1` (with dynamic fallback). External network interfaces cannot reach the HTTP API. `securityMiddleware` validates the exact authority, Origin, and Referer, and requires a session/capability for sensitive reads and mutations; wildcard CORS and unauthenticated status/data reads are not used.
2. **Dual-Socket Network Isolation:** Public/LAN traffic is strictly restricted to port 9877, requiring length-prefixed TLS/SSH frames with pinned cryptographic certificates.
3. **Mutual TLS with Fingerprint Pinning:** LAN connections reject any TLS client or server certificate whose SHA-256 fingerprint is not explicitly stored in `peers.json`.
4. **Cryptographic Leaf Certificate Identity Attribution:** Provenance on inbound drops and OAuth requests is derived directly from verified TLS 1.3 client certificates (`sha256(leaf.Raw)`). Handlers are completely immune to application-layer envelope spoofing.
5. **No Credential Caching:** `tantu` never stores, inspects, or logs OAuth refresh tokens, access tokens, or client secrets. It operates strictly as an ephemeral transport pipe.
6. **Frame Size Limits & Handshake Deadlines:** General frames are capped at 4 MiB and typed control frames at 64 KiB; pairing connections use the smaller cap before allocation. Inbound connections enforce a 30-second read deadline during protocol negotiation via `Deadliner`. File data is chunked (1MB) and streamed directly to disk; aggregate transfer quotas remain future work.
7. **Exact Port Matching:** Eliminates dynamic port fallbacks on the OAuth callback listener, preserving strict redirect URI compliance with Google, GitHub, and Azure identity providers.
8. **Filesystem Isolation:** Identity keys (`identity.json`), peer credentials (`peers.json`), and daemon state descriptors (`hub.json`) use private file modes and atomic/locked publication in the platform-standard config directory. Windows ACL enforcement and cross-process peer-store locking remain deployment requirements.
9. **Cross-Platform Filename Sanitization (`SanitizeDropFilename`):** All received filenames are aggressively sanitized: path traversal characters (`..`, `\`, `/`) are stripped, NTFS Alternate Data Streams (`:`) are removed, Windows reserved DOS devices (`CON`, `PRN`, `AUX`, `NUL`, `COM1-9`, `LPT1-9`) are prefixed with `drop_`, control characters are eliminated, and files are stored strictly inside the sandboxed download folder with timestamp deduplication.
