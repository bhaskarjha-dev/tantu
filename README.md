# Tantu

[![CI](https://github.com/bhaskarjha-dev/tantu/actions/workflows/ci.yml/badge.svg)](https://github.com/bhaskarjha-dev/tantu/actions/workflows/ci.yml)

A zero-configuration, cross-platform developer utility that stretches an **encrypted peer-to-peer thread** between your machines — carrying OAuth authentication flows, files, text, and tokens across machine boundaries.

Run `./tantu` on both machines. That's it.

> *"That thread (tantu) by which this world, the next world, and all beings are strung together..."*  
> — **Bṛhadāraṇyaka Upaniṣad, 3.7.1**

---

## The Name: Why Tantu? (तन्तु)

**Tantu** (Sanskrit: **तन्तु**, pronounced */ˈtʌn.tuː/* or *TAN-too*) originates from the classical Indo-European verbal root **√tan** (*tanoti* — to stretch, to extend, to spin a continuous filament) augmented by the action suffix **-tu**.

In ancient literature and loomcraft, a *tantu* is specifically the **warp thread** — the longitudinal filament stretched taut between two fixed beams, upon which the entire tapestry is woven.

### The Structural Isomorphism

Tantu is not named decoratively; the name is an exact structural mirror of the tool's computational architecture:

| Loom Archetype | Tantu Computational Reality |
|:---|:---|
| **Stretched Taut** | An encrypted, mutual TLS 1.3 channel stretched between two machines on port `9877`. |
| **Persistent Conduit** | The warp stays in place while transient weft threads pass through — the Hub daemon runs continuously while OAuth flows, files, and text snippets stream across. |
| **Invisible Substrate** | In the final cloth, you see the pattern, not the individual warp — developers experience seamless workflows (*"the browser opened"*, *"the 4GB file arrived"*), unaware of the underlying wire. |
| **Scales to Fabric** | Multiple threads interlock into a web (*tantujāla*, तन्तुजाल) — scaling naturally from a point-to-point thread to an authenticated multi-peer mesh. |

### Defeating *Vikṣepa* (विक्षेप — Workspace Scatter)

Modern engineering environments suffer from **Vikṣepa** — mental and computational fragmentation. A developer’s workspace is scattered across laptops, cloud devboxes, remote GPUs, and WSL containers:
- Remote CLIs attempt to open OAuth redirects on `localhost` where no graphical browser lives.
- Transferring API tokens, error traces, or builds forces engineers into insecure channels (chat apps, cloud storage, ephemeral pastebins).

Tantu eliminates this friction. It stretches an invisible, encrypted thread across your machine boundaries, unifying a fractured workspace with ambient, quiet confidence.

---

## What Tantu Solves

**1. The Remote OAuth Trap**
You're running a CLI on a remote dev server (`gcloud`, `gh`, `az`, or any OAuth app) that opens a browser for authentication. The redirect targets `localhost`, which fails because your browser is on your laptop. Tantu intercepts the OAuth URL, opens it on the right machine, and forwards the callback back — transparently.

**2. Cross-Machine Sharing Friction**
Moving tokens, error logs, code snippets, and large files between machines means insecure pastebins, chat apps, or cloud storage. Tantu provides encrypted, resumable, direct peer-to-peer transfer up to 5GB.

---

## Architecture

```
       MACHINE 1 (Laptop)                              MACHINE 2 (Dev Server)
 ┌─────────────────────────────────────┐    ┌─────────────────────────────────────┐
 │            tantu hub                │    │            tantu hub                │
 │                                     │    │                                     │
 │  • Cockpit: hotkeys [o,s,t,c,p,v,q] │    │  • Cockpit: hotkeys [o,s,t,c,p,v,q] │
 │  • Web Dashboard: localhost:9876    │    │  • Web Dashboard: localhost:9876    │
 │    ├─ QuickDrop (up to 5GB)         │    │    ├─ QuickDrop (up to 5GB)         │
 │    ├─ OAuth Relay & Bookmarklet     │    │    ├─ OAuth Relay & Bookmarklet     │
 │    ├─ Peers & Pairing Wizard        │    │    ├─ Peers & Pairing Wizard        │
 │    └─ Live Activity Logs (SSE)      │    │    └─ Live Activity Logs (SSE)      │
 │                                     │    │                                     │
 │  • CLI: tantu send / wrap / open    │    │  • CLI: tantu send / wrap / open    │
 │                                     │    │                                     │
 │       Multiplexed Port :9877        │    │       Multiplexed Port :9877        │
 └─────────────────────────────────────┘    └─────────────────────────────────────┘
                   ▲                                              ▲
                   ╚═══════════ Encrypted mTLS Thread ════════════╝
                            (Direct LAN / SSH / Loopback)
```

Both machines run identical symmetric hubs. No server/client distinction. No cloud dependency.

---

## Features

- **Zero-Argument Startup** — `./tantu` boots the complete hub, self-heals identity, opens cockpit
- **Zero-Config LAN Discovery** — mDNS (`224.0.0.251:5353`) + UDP broadcast (`port 9879`) find nearby hubs automatically
- **Resumable QuickDrop** — interrupted multi-GB transfers resume from last valid 1MB chunk; SHA-256 verified; cross-platform path & Windows DOS device sanitization
- **Smart Default LAN Transport** — binds to `0.0.0.0:9877` by default for instant local discovery, pairing, and transfers without `--transport` or `--peer` flags
- **Single-Page Web Dashboard** (`http://localhost:9876`) — dark-mode browser UI with 4 workspaces:
  - **QuickDrop:** Drag-and-drop send (up to 5GB), text snippets, live received items feed with 1-click clipboard copying and folder opening
  - **OAuth Relay:** 1-click draggable bookmarklet, manual URL submission
  - **Peers & Network:** Discovered nearby hubs (1-click pairing), in-band pairing wizard, paired peer cards (alias, default toggle, unpair)
  - **Activity Logs:** Live filtered log explorer with JSON export and Server-Sent Events (SSE)
- **Interactive Developer Cockpit** — terminal dashboard with streaming logs, discovery badge, and hotkeys:
  - `[o]` Open Web Dashboard · `[s]` Send file · `[t]` Send text · `[c]` Clear
  - `[p]` Peer switcher · `[v]` Toggle verbose · `[q]` Graceful shutdown
- **Smart IPC Delegation** — CLI commands auto-detect and reuse a running hub via local REST probe
- **In-Band Pairing** — cryptographic handshake directly over port 9877 with SAS visual verification (works seamlessly while Hub is running)
- **Zero App Modification** — works transparently with any OAuth 2.0 PKCE application
- **Direct Disk Streaming** — constant RAM footprint for multi-GB transfers (1MB chunked streams)
- **Dual-Socket Isolation & Anti-CSRF** — Web UI binds to `127.0.0.1:9876` with strict Origin validation; wire traffic on encrypted `9877`
- **Cross-Platform** — Linux, macOS, Windows (with Git Bash / PowerShell path normalization)

---

## Quickstart

### 1. Start the Hub on Both Machines

```bash
# On your laptop (Machine 1) and dev server (Machine 2):
./tantu
```

The hub boots instantly — generates cryptographic identity on first run, opens the terminal cockpit, and starts the web dashboard at `http://localhost:9876`.

### 2. Pair Your Machines (One Time)

Pairing establishes mutual cryptographic trust. Choose whichever method is easiest:

- **Option A: 1-Click via Web Dashboard (Recommended)**  
  Press `[o]` in the terminal to open `http://localhost:9876`. Navigate to the **Peers & Network** tab, locate the discovered machine under **⚡ Discovered Nearby Hubs**, and click **Pair**.

- **Option B: Interactive CLI Discovery**  
  Run on either machine:
  ```bash
  ./tantu pair
  ```
  Tantu lists all discovered hubs on your LAN:
  ```text
  Discovered nearby hubs on LAN:
    [1] devbox (192.168.0.244:9877, SAS: 0949b2)
  Select peer [1-1] or enter custom IP (or press Enter to view my pairing code): 1
  ```
  Type `1` and press Enter.

- **Option C: Direct IP Flag**  
  ```bash
  ./tantu pair --peer=<REMOTE_MACHINE_IP>:9877
  ```

**Verify SAS Code:** Both machines display a 6-character Short Authentication String (e.g. `0949b2`). Confirm they match on both screens. Done — they are cryptographically bonded for life!

### 3. Use It

**Send a file:**
```bash
tantu send report.pdf
```

**Send text, API tokens, or logs:**
```bash
tantu send "sk-abc123-secret-token"
# Or pipe from stdin:
cat error.log | tantu send -
```

**Transparent OAuth (any CLI tool):**
```bash
tantu wrap -- gcloud auth login
tantu wrap -- gh auth login
tantu wrap -- az login
```

**Or use the Web Dashboard** — drag and drop files up to 5GB, paste text, click the OAuth bookmarklet.

---

## CLI Reference

| Command | Description |
|:---|:---|
| `tantu` | Start the Symmetric Hub (default) |
| `tantu hub` | Explicit hub start |
| `tantu pair` | Pair with a remote machine (mTLS + SAS verification) |
| `tantu unpair` | Remove a paired peer |
| `tantu status` | Show local identity and paired peers |
| `tantu send <content>` | Send text, files, or images to a paired machine |
| `tantu receive` | Receive content from a paired machine |
| `tantu drop` | Start a local Web UI for drag-and-drop sharing |
| `tantu wrap -- <cmd>` | Execute a command with BROWSER set to tantu |
| `tantu open <url>` | Send an OAuth URL through the thread |
| `tantu serve` | Start the A-side bridge listener |
| `tantu node` | Start unified peer node (OAuth + QuickDrop) |
| `tantu relay` | Start local HTTP relay with Web UI |

---

## Security Model

- **Mutual TLS (mTLS)** with ECDSA P-256 certificate pinning — no CA dependency
- **SAS (Short Authentication String)** verification during pairing — MITM-proof
- **PKCE (RFC 7636)** — authorization codes are useless without the code verifier
- **Dual-socket isolation & Anti-CSRF** — loopback-only web UI with strict Origin/Referer validation, encrypted-only wire traffic
- **Hardened filename sanitization** — defense against path traversal, NTFS ADS, and Windows DOS reserved devices
- **Zero cloud dependency** — all traffic is direct peer-to-peer

See [docs/THREAT-MODEL.md](docs/THREAT-MODEL.md) for the full threat analysis.

---

## Technical Details

- **Language:** Go (zero external runtime dependencies)
- **Transport layer:** Pluggable — LAN (mTLS), SSH, Loopback
- **Wire protocol:** Length-prefixed JSON envelopes with type discrimination
- **Discovery:** mDNS + UDP subnet broadcast (dual-engine, zero-dependency)
- **File transfer:** 1MB chunked streaming with resumable `.part` files and SHA-256 integrity
- **Identity storage:** `~/.config/tantu/` (Linux/macOS) or `%APPDATA%\tantu\` (Windows)

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full system architecture.

---

## Build from Source

```bash
go build -o tantu ./cmd/tantu
```

---

## What's Ahead

Tantu's encrypted thread currently connects machines on a local network. The architecture is designed to extend further:

- **Internet (WAN) Transport** — E2EE relay with STUN hole-punching for cross-network pairing
- **Multi-file / Directory Drops** — recursive folder transfer with structure preservation
- **OS-Native Notifications** — desktop alerts for incoming drops and OAuth requests
- **Pluggable Protocol Extensions** — clipboard sync, terminal sharing, and beyond

---

## License

MIT
