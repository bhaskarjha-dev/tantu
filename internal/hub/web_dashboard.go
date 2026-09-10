package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>🔐 tantu hub</title>
<style>
  :root {
    --bg: #090b10;
    --card-bg: #131722;
    --card-hover: #181d2b;
    --border: #232a3b;
    --border-light: #323c52;
    --text: #e6edf3;
    --text-muted: #8b949e;
    --accent: #3b82f6;
    --accent-hover: #2563eb;
    --accent-gradient: linear-gradient(135deg, #3b82f6, #6366f1);
    --success: #10b981;
    --success-bg: rgba(16, 185, 129, 0.12);
    --warning: #f59e0b;
    --warning-bg: rgba(245, 158, 11, 0.12);
    --error: #ef4444;
    --error-bg: rgba(239, 68, 68, 0.12);
    --purple: #a855f7;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    background: var(--bg);
    color: var(--text);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    min-height: 100vh;
    display: flex;
    flex-direction: column;
  }
  header {
    background: rgba(19, 23, 34, 0.85);
    backdrop-filter: blur(12px);
    border-bottom: 1px solid var(--border);
    padding: 1rem 2rem;
    display: flex;
    justify-content: space-between;
    align-items: center;
    position: sticky;
    top: 0;
    z-index: 50;
  }
  .logo-group {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }
  .logo-title {
    font-size: 1.25rem;
    font-weight: 700;
    letter-spacing: -0.02em;
    background: var(--accent-gradient);
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
  }
  .version-tag {
    font-size: 0.75rem;
    color: var(--text-muted);
    background: rgba(255, 255, 255, 0.05);
    border: 1px solid var(--border);
    padding: 0.15rem 0.45rem;
    border-radius: 9999px;
  }
  .peer-status-pill {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    background: var(--card-bg);
    border: 1px solid var(--border);
    padding: 0.4rem 0.85rem;
    border-radius: 9999px;
    font-size: 0.85rem;
  }
  .status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--success);
    box-shadow: 0 0 8px var(--success);
  }
  .status-dot.offline {
    background: var(--warning);
    box-shadow: 0 0 8px var(--warning);
  }
  nav.tabs-nav {
    display: flex;
    gap: 0.5rem;
    padding: 1rem 2rem 0;
    border-bottom: 1px solid var(--border);
    background: rgba(19, 23, 34, 0.4);
  }
  .tab-btn {
    background: transparent;
    border: none;
    border-bottom: 2px solid transparent;
    color: var(--text-muted);
    font-size: 0.95rem;
    font-weight: 600;
    padding: 0.75rem 1.25rem;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    transition: all 0.2s;
  }
  .tab-btn:hover {
    color: var(--text);
  }
  .tab-btn.active {
    color: var(--accent);
    border-bottom-color: var(--accent);
  }
  main.tab-content {
    flex: 1;
    padding: 2rem;
    max-width: 1000px;
    margin: 0 auto;
    width: 100%;
  }
  .tab-pane {
    display: none;
  }
  .tab-pane.active {
    display: block;
    animation: fadeIn 0.2s ease-in-out;
  }
  @keyframes fadeIn {
    from { opacity: 0; transform: translateY(4px); }
    to { opacity: 1; transform: translateY(0); }
  }
  .card {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 1.5rem;
    margin-bottom: 1.5rem;
    box-shadow: 0 4px 20px rgba(0, 0, 0, 0.3);
  }
  .card-title {
    font-size: 1.1rem;
    font-weight: 600;
    margin-bottom: 1rem;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .drop-zone {
    border: 2px dashed var(--border-light);
    border-radius: 12px;
    padding: 2.5rem 1rem;
    text-align: center;
    background: rgba(255, 255, 255, 0.01);
    cursor: pointer;
    transition: all 0.2s;
  }
  .drop-zone.dragover {
    border-color: var(--accent);
    background: rgba(59, 130, 246, 0.08);
  }
  .drop-zone-icon {
    font-size: 2.5rem;
    margin-bottom: 0.75rem;
  }
  .drop-zone-text {
    font-size: 1rem;
    color: var(--text-muted);
  }
  .drop-zone-subtext {
    font-size: 0.8rem;
    color: var(--text-muted);
    margin-top: 0.35rem;
  }
  .progress-container {
    margin-top: 1rem;
    display: none;
  }
  .progress-bar {
    height: 8px;
    background: rgba(255, 255, 255, 0.08);
    border-radius: 9999px;
    overflow: hidden;
  }
  .progress-fill {
    height: 100%;
    width: 0%;
    background: var(--accent-gradient);
    transition: width 0.2s;
  }
  .progress-meta {
    display: flex;
    justify-content: space-between;
    font-size: 0.8rem;
    color: var(--text-muted);
    margin-top: 0.4rem;
  }
  .form-group {
    display: flex;
    gap: 0.75rem;
    margin-top: 1rem;
  }
  input[type="text"], textarea {
    width: 100%;
    background: #090b10;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.75rem 1rem;
    color: var(--text);
    font-size: 0.95rem;
    outline: none;
    transition: border-color 0.2s;
  }
  input[type="text"]:focus, textarea:focus {
    border-color: var(--accent);
  }
  textarea {
    min-height: 100px;
    resize: vertical;
    font-family: inherit;
  }
  .btn-primary {
    background: var(--accent-gradient);
    border: none;
    color: #fff;
    padding: 0.75rem 1.5rem;
    border-radius: 8px;
    font-weight: 600;
    font-size: 0.95rem;
    cursor: pointer;
    transition: opacity 0.2s;
    white-space: nowrap;
  }
  .btn-primary:hover {
    opacity: 0.9;
  }
  .btn-primary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .btn-sm {
    background: rgba(255, 255, 255, 0.06);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 0.35rem 0.75rem;
    border-radius: 6px;
    font-size: 0.8rem;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.2s;
  }
  .btn-sm:hover {
    background: rgba(255, 255, 255, 0.12);
    border-color: var(--border-light);
  }
  .received-item {
    background: #090b10;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.85rem;
    transition: border-color 0.2s;
  }
  .received-item:hover {
    border-color: var(--border-light);
  }
  .received-item-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: 0.8rem;
    color: var(--text-muted);
    margin-bottom: 0.5rem;
  }
  .received-item-content {
    background: #06070a;
    border: 1px solid rgba(255,255,255,0.05);
    border-radius: 6px;
    padding: 0.6rem 0.75rem;
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, monospace;
    font-size: 0.85rem;
    color: var(--text);
    white-space: pre-wrap;
    word-break: break-all;
    max-height: 160px;
    overflow-y: auto;
  }
  .received-item-actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }
  .bookmarklet-box {
    background: rgba(59, 130, 246, 0.06);
    border: 1px dashed var(--accent);
    border-radius: 12px;
    padding: 1.5rem;
    text-align: center;
    margin-bottom: 1.5rem;
  }
  .bookmarklet-btn {
    display: inline-block;
    background: var(--accent-gradient);
    color: #fff;
    text-decoration: none;
    font-weight: 600;
    padding: 0.75rem 1.5rem;
    border-radius: 9999px;
    box-shadow: 0 4px 14px rgba(59, 130, 246, 0.4);
    cursor: move;
  }
  .hint-text {
    font-size: 0.85rem;
    color: var(--text-muted);
    margin-top: 0.75rem;
  }
  .grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1.5rem;
  }
  @media (max-width: 768px) {
    .grid-2 { grid-template-columns: 1fr; }
    nav.tabs-nav { overflow-x: auto; }
  }
  .stat-row {
    display: flex;
    justify-content: space-between;
    padding: 0.6rem 0;
    border-bottom: 1px solid rgba(255, 255, 255, 0.05);
    font-size: 0.9rem;
  }
  .stat-label { color: var(--text-muted); }
  .stat-val { font-family: monospace; font-weight: 600; }
  .log-console {
    background: #06070a;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 1rem;
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 0.85rem;
    height: 380px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }
  .log-entry {
    line-height: 1.4;
    word-break: break-all;
  }
  .tag {
    display: inline-block;
    padding: 0.1rem 0.4rem;
    border-radius: 4px;
    font-size: 0.75rem;
    font-weight: 700;
    margin-right: 0.5rem;
  }
  .tag-oauth { background: rgba(59, 130, 246, 0.2); color: #60a5fa; }
  .tag-drop { background: var(--success-bg); color: #34d399; }
  .tag-peer { background: rgba(168, 85, 247, 0.2); color: #c084fc; }
  .tag-net { background: var(--warning-bg); color: #fbbf24; }
  .tag-sys { background: rgba(6, 182, 212, 0.2); color: #22d3ee; }
  .tag-debug { background: rgba(255, 255, 255, 0.08); color: #94a3b8; }
  .tag-error { background: var(--error-bg); color: #f87171; }
  .pill {
    background: rgba(255, 255, 255, 0.05);
    border: 1px solid var(--border);
    color: var(--text-muted);
    padding: 0.25rem 0.65rem;
    border-radius: 9999px;
    font-size: 0.75rem;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.2s;
  }
  .pill:hover {
    background: rgba(255, 255, 255, 0.1);
    color: var(--text);
  }
  .pill.active {
    background: var(--accent);
    border-color: var(--accent);
    color: #fff;
  }
  .status-banner {
    display: none;
    border-radius: 8px;
    padding: 0.85rem 1rem;
    margin-top: 1rem;
    font-size: 0.9rem;
  }
  .status-banner.success { display: block; background: var(--success-bg); border: 1px solid var(--success); color: #34d399; }
  .status-banner.error { display: block; background: var(--error-bg); border: 1px solid var(--error); color: #f87171; }
  .status-banner.relaying { display: block; background: var(--warning-bg); border: 1px solid var(--warning); color: #fbbf24; }
</style>
</head>
<body>
  <header>
    <div class="logo-group">
      <div class="logo-title">🔐 tantu</div>
      <span class="version-tag">v{{VERSION}}</span>
    </div>
    <div class="peer-status-pill">
      <div class="status-dot" id="peerDot"></div>
      <span id="peerLabel">Auto-detecting peer...</span>
    </div>
  </header>

  <nav class="tabs-nav">
    <button class="tab-btn active" onclick="switchTab('tab-drop')">📦 QuickDrop</button>
    <button class="tab-btn" onclick="switchTab('tab-relay')">⚡ OAuth Relay</button>
    <button class="tab-btn" onclick="switchTab('tab-peers')">🔗 Peers & Network</button>
    <button class="tab-btn" onclick="switchTab('tab-logs')">📋 Live Logs</button>
  </nav>

  <main class="tab-content">
    <!-- INBOUND PAIRING APPROVAL BANNER -->
    <div id="pendingPairingsBanner" style="display: none; margin-bottom: 1.5rem; background: rgba(245, 158, 11, 0.12); border: 1px solid #f59e0b; border-radius: 12px; padding: 1.25rem;">
      <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem;">
        <div style="display: flex; align-items: center; gap: 0.5rem;">
          <span style="font-size: 1.25rem;">🔐</span>
          <span style="font-weight: 600; color: #fbbf24; font-size: 1rem;">Incoming Pairing Request</span>
        </div>
        <span style="font-size: 0.75rem; background: rgba(245, 158, 11, 0.25); color: #fbbf24; padding: 0.2rem 0.6rem; border-radius: 9999px; font-weight: 500;">Action Required</span>
      </div>
      <div id="pendingPairingsList" style="display: flex; flex-direction: column; gap: 0.75rem;"></div>
    </div>

    <!-- TAB 1: QUICKDROP -->
    <div id="tab-drop" class="tab-pane active">
      <!-- Download Location Bar -->
      <div class="card" style="margin-bottom: 1.25rem; padding: 0.75rem 1.25rem;">
        <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 0.75rem;">
          <div style="display: flex; align-items: center; gap: 0.5rem; font-size: 0.9rem;">
            <span style="color: var(--text-muted);">📁 Downloads location:</span>
            <code id="downloadDirPath" style="color: #60a5fa; background: rgba(59,130,246,0.1); padding: 0.2rem 0.5rem; border-radius: 4px; font-size: 0.85rem;">...</code>
          </div>
          <div style="display: flex; gap: 0.5rem;">
            <button class="btn-sm" onclick="promptChangeDownloadDir()">✏️ Change</button>
            <button class="btn-sm" onclick="openDownloadFolder()">📂 Open Folder</button>
          </div>
        </div>
      </div>

      <!-- Responsive Dual-Pane Layout -->
      <div class="grid-2">
        <!-- Left Pane: Outbound Sending -->
        <div style="display: flex; flex-direction: column; gap: 1.25rem;">
          <div class="card">
            <div class="card-title">📤 Send File or Image (up to 5GB)</div>
            <div id="dropPeerSelector" style="display:none; margin-bottom: 0.75rem; align-items: center; gap: 0.5rem; flex-wrap: wrap;">
              <span style="font-size: 0.8rem; color: var(--text-muted); font-weight: 600;">Destination:</span>
              <div id="dropPeerPills" style="display: flex; gap: 0.4rem; flex-wrap: wrap;"></div>
            </div>
            <div class="drop-zone" id="dropZone" onclick="document.getElementById('fileInput').click()">
              <div class="drop-zone-icon">📁</div>
              <div class="drop-zone-text">Drag & drop files here, or click to browse</div>
              <div class="drop-zone-subtext">Direct peer-to-peer streaming via mTLS • zero intermediate servers</div>
            </div>
            <input type="file" id="fileInput" style="display: none;">
            
            <div class="progress-container" id="uploadProgress">
              <div class="progress-bar">
                <div class="progress-fill" id="progressFill"></div>
              </div>
              <div class="progress-meta">
                <span id="progressFile">Uploading...</span>
                <span id="progressPercent">0%</span>
              </div>
            </div>
            <div class="status-banner" id="dropStatus"></div>
          </div>

          <div class="card">
            <div class="card-title">📝 Quick Text & Snippet Sharing</div>
            <textarea id="textPayload" placeholder="Paste code snippet, auth tokens, commands, or notes to send immediately to the peer..."></textarea>
            <div style="display: flex; justify-content: flex-end; margin-top: 0.75rem;">
              <button class="btn-primary" id="btnSendText" onclick="sendTextDrop()">Send to Peer</button>
            </div>
          </div>
        </div>

        <!-- Right Pane: Received Items & History -->
        <div class="card" style="display: flex; flex-direction: column;">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem;">
            <div class="card-title" style="margin-bottom: 0;">📥 Received Items & History</div>
            <button class="btn-sm" onclick="loadRecentDrops()">🔄 Refresh</button>
          </div>
          <div id="receivedDropsList" style="flex: 1; overflow-y: auto; max-height: 580px; display: flex; flex-direction: column; gap: 0.75rem;">
            <p style="color: var(--text-muted); font-size: 0.85rem; text-align: center; padding: 2rem 0;">No received items yet.<br>Snippets and files sent by your peer appear here in real-time.</p>
          </div>
        </div>
      </div>
    </div>

    <!-- TAB 2: OAUTH RELAY -->
    <div id="tab-relay" class="tab-pane">
      <div class="bookmarklet-box">
        <a class="bookmarklet-btn" href="{{BOOKMARKLET_HREF}}">⚡ Tantu</a>
        <div class="hint-text">Drag this button to your browser Bookmarks Bar. When logging in on any OAuth tab, click it to relay authorization directly back to your dev terminal!</div>
      </div>

      <div class="card">
        <div class="card-title">⚡ Manual OAuth URL Forwarder</div>
        <form onsubmit="relayOAuth(event)" class="form-group">
          <input type="text" id="oauthUrlInput" placeholder="Paste OAuth URL (https://accounts.google.com/o/oauth2/...)" required>
          <button type="submit" class="btn-primary" id="btnRelay">Relay to Peer</button>
        </form>
        <div class="status-banner" id="relayStatus"></div>
      </div>
    </div>

    <!-- TAB 3: PEERS & NETWORK -->
    <div id="tab-peers" class="tab-pane">
      <!-- Discovered Nearby Hubs (Auto-detected via mDNS/LAN Beacon) -->
      <div id="discoveredHubsCard" class="card" style="display: none; margin-bottom: 1.5rem; border: 1px solid var(--accent); background: rgba(59, 130, 246, 0.05);">
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem;">
          <div class="card-title" style="margin-bottom: 0; color: var(--accent);">⚡ Discovered Nearby Hubs</div>
          <span style="font-size: 0.75rem; color: var(--text-muted); background: var(--surface-hover); padding: 0.2rem 0.5rem; border-radius: 4px;">Zero-Typing Discovery</span>
        </div>
        <div id="discoveredHubsList" style="display: flex; flex-direction: column; gap: 0.5rem;"></div>
      </div>

      <div class="grid-2">
        <div class="card">
          <div class="card-title">💻 Local Hub Identity</div>
          <div class="stat-row">
            <span class="stat-label">Transport</span>
            <span class="stat-val" id="idTransport">-</span>
          </div>
          <div class="stat-row">
            <span class="stat-label">SAS Verification Code</span>
            <span class="stat-val" id="idSAS" style="color: #60a5fa;">-</span>
          </div>
          <div class="stat-row">
            <span class="stat-label">P2P Listen Socket</span>
            <span class="stat-val" id="idP2P">-</span>
          </div>
          <div class="stat-row">
            <span class="stat-label">Web / IPC Socket</span>
            <span class="stat-val" id="idWeb">-</span>
          </div>
          <div class="stat-row">
            <span class="stat-label">TLS Fingerprint</span>
            <span class="stat-val" id="idFP" style="font-size: 0.75rem;">-</span>
          </div>
        </div>

        <div class="card">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem;">
            <div class="card-title" style="margin-bottom: 0;">👥 Connected Peers</div>
            <button class="btn-sm" onclick="openPairModal()">+ Pair New Device</button>
          </div>
          <div id="peersList">
            <p style="color: var(--text-muted); font-size: 0.9rem;">Loading peer status...</p>
          </div>
        </div>
      </div>

      <!-- PAIRING MODAL -->
      <div id="pairModal" class="modal-overlay" style="display:none; position:fixed; inset:0; background:rgba(0,0,0,0.75); backdrop-filter:blur(4px); z-index:9999; align-items:center; justify-content:center;">
        <div class="modal-box" style="background:#0f172a; border:1px solid #334155; border-radius:12px; width:90%; max-width:500px; padding:1.5rem; box-shadow:0 25px 50px -12px rgba(0,0,0,0.5);">
          <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:1rem;">
            <h3 style="margin:0; font-size:1.1rem; color:#f8fafc;">🔗 Pair New Device</h3>
            <button onclick="closePairModal()" style="background:none; border:none; color:#94a3b8; font-size:1.4rem; cursor:pointer; line-height:1;">&times;</button>
          </div>
          
          <p style="font-size:0.85rem; color:#94a3b8; margin-bottom:1.25rem;">
            Pairing establishes mutual zero-trust encryption (TLS + SAS authentication) between this machine and another tantu instance over in-band port 9877.
          </p>

          <div style="background:rgba(30,41,59,0.7); border:1px solid #334155; border-radius:8px; padding:1rem; margin-bottom:1.25rem; text-align:center;">
            <div style="font-size:0.75rem; text-transform:uppercase; letter-spacing:0.05em; color:#94a3b8; margin-bottom:0.25rem;">Local SAS Verification Code</div>
            <div id="pairModalSAS" style="font-family:monospace; font-size:1.5rem; font-weight:700; color:#60a5fa; letter-spacing:0.1em;">---</div>
          </div>

          <div style="margin-bottom:1.25rem;">
            <label style="display:block; font-size:0.8rem; font-weight:600; color:#cbd5e1; margin-bottom:0.5rem;">Option 1: Initiate from Remote Device (CLI)</label>
            <div style="display:flex; gap:0.5rem; align-items:center;">
              <code id="pairCmdText" style="flex:1; background:rgba(0,0,0,0.4); border:1px solid #334155; border-radius:6px; padding:0.5rem 0.75rem; font-size:0.8rem; color:#38bdf8; overflow-x:auto; white-space:nowrap;">tantu pair --peer=&lt;THIS_IP&gt;:9877</code>
              <button class="btn-sm" id="btnCopyPairCmd" onclick="copyPairCmd()">📋 Copy</button>
            </div>
          </div>

          <div style="margin-bottom:1.25rem;">
            <label style="display:block; font-size:0.8rem; font-weight:600; color:#cbd5e1; margin-bottom:0.5rem;">Option 2: Connect to Remote Peer Address</label>
            <div style="display:flex; gap:0.5rem; align-items:center;">
              <input type="text" id="pairRemoteAddrInput" placeholder="192.168.1.50:9877" style="flex:1; font-size:0.85rem; padding:0.5rem 0.75rem;">
              <button class="btn" id="btnPairConnect" onclick="initiateWebPairing()" style="white-space:nowrap; padding:0.5rem 1rem;">Pair Device</button>
            </div>
            <div id="pairStatusMsg" style="font-size:0.8rem; margin-top:0.5rem; min-height:1.2rem;"></div>
          </div>

          <div style="display:flex; justify-content:flex-end;">
            <button class="btn-sm" onclick="closePairModal()">Close</button>
          </div>
        </div>
      </div>
    </div>

    <!-- TAB 4: LIVE LOGS -->
    <div id="tab-logs" class="tab-pane">
      <div class="card">
        <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 0.75rem; margin-bottom: 1rem;">
          <div class="card-title" style="margin-bottom: 0;">📋 Live Diagnostics & Activity Feed</div>
          <div style="display: flex; gap: 0.5rem;">
            <button class="btn-sm" onclick="exportLogs()">📥 Export Logs</button>
            <button class="btn-sm" onclick="clearLogs()">Clear</button>
          </div>
        </div>

        <!-- Filter Pills & Search Bar -->
        <div style="display: flex; flex-direction: column; gap: 0.75rem; margin-bottom: 1rem;">
          <div style="display: flex; gap: 0.5rem; flex-wrap: wrap; align-items: center;">
            <span style="font-size: 0.8rem; color: var(--text-muted); margin-right: 0.25rem;">Filter:</span>
            <button class="pill active" data-filter="ALL" onclick="setLogFilter('ALL', this)">All</button>
            <button class="pill" data-filter="OAUTH" onclick="setLogFilter('OAUTH', this)">🔵 OAuth</button>
            <button class="pill" data-filter="DROP" onclick="setLogFilter('DROP', this)">🟢 QuickDrop</button>
            <button class="pill" data-filter="PEER" onclick="setLogFilter('PEER', this)">🟣 Peer</button>
            <button class="pill" data-filter="NET" onclick="setLogFilter('NET', this)">🟡 Network</button>
            <button class="pill" data-filter="DEBUG" onclick="setLogFilter('DEBUG', this)">🐛 Debug / Verbose</button>
            <button class="pill" data-filter="ERROR" onclick="setLogFilter('ERROR', this)">🔴 Errors</button>
          </div>

          <div style="position: relative;">
            <input type="text" id="logSearchInput" placeholder="🔍 Search live logs by text, URL, host, or request ID..." style="padding-left: 1rem; font-size: 0.85rem;" oninput="applyLogFilters()">
          </div>
        </div>

        <div class="log-console" id="logConsole">
          <div class="log-entry" data-domain="NET" data-level="INFO"><span style="color:#64748b; margin-right:0.5rem;">--:--:--</span><span class="tag tag-net">NET</span> Hub initialized and listening.</div>
        </div>
      </div>
    </div>
  </main>

  <script>
    function switchTab(tabId) {
      document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
      document.querySelectorAll('.tab-pane').forEach(p => p.classList.remove('active'));
      event.currentTarget.classList.add('active');
      document.getElementById(tabId).classList.add('active');
    }

    let currentFilter = 'ALL';

    function addLog(domain, msg, level, meta) {
      domain = (domain || 'SYS').toUpperCase();
      level = (level || 'INFO').toUpperCase();
      const console = document.getElementById('logConsole');
      const row = document.createElement('div');
      row.className = 'log-entry';
      row.setAttribute('data-domain', domain);
      row.setAttribute('data-level', level);

      const timeStr = new Date().toLocaleTimeString();
      let tagClass = 'tag-net';
      if (domain === 'OAUTH') tagClass = 'tag-oauth';
      else if (domain === 'DROP') tagClass = 'tag-drop';
      else if (domain === 'PEER') tagClass = 'tag-peer';
      else if (domain === 'SYS') tagClass = 'tag-sys';
      if (level === 'ERROR') tagClass = 'tag-error';
      else if (level === 'DEBUG') tagClass = 'tag-debug';

      let metaHTML = '';
      if (meta && Object.keys(meta).length > 0) {
        metaHTML = '<details style="margin-top:0.25rem; font-size:0.75rem; color:#94a3b8;"><summary style="cursor:pointer; color:#60a5fa;">inspect metadata</summary><pre style="background:rgba(0,0,0,0.3); padding:0.4rem; border-radius:4px; margin-top:0.25rem; overflow-x:auto;">' + escapeHTML(JSON.stringify(meta, null, 2)) + '</pre></details>';
      }

      row.innerHTML = '<span style="color:#64748b; margin-right:0.5rem;">' + timeStr + '</span><span class="tag ' + tagClass + '">' + domain + '</span> ' + escapeHTML(msg) + metaHTML;
      console.appendChild(row);

      if (console.children.length > 500) {
        console.removeChild(console.firstChild);
      }

      filterRow(row);
      console.scrollTop = console.scrollHeight;
    }

    function setLogFilter(filter, btn) {
      currentFilter = filter;
      document.querySelectorAll('.pill').forEach(p => p.classList.remove('active'));
      if (btn) btn.classList.add('active');
      applyLogFilters();
    }

    function applyLogFilters() {
      const search = (document.getElementById('logSearchInput').value || '').toLowerCase();
      const rows = document.querySelectorAll('#logConsole .log-entry');
      rows.forEach(row => {
        const rowDomain = row.getAttribute('data-domain') || '';
        const rowLevel = row.getAttribute('data-level') || '';
        const rowText = row.innerText.toLowerCase();

        let domainMatch = true;
        if (currentFilter === 'DEBUG') {
          domainMatch = (rowLevel === 'DEBUG');
        } else if (currentFilter === 'ERROR') {
          domainMatch = (rowLevel === 'ERROR');
        } else if (currentFilter !== 'ALL') {
          domainMatch = (rowDomain === currentFilter);
        }

        const searchMatch = !search || rowText.includes(search);
        row.style.display = (domainMatch && searchMatch) ? '' : 'none';
      });
    }

    function filterRow(row) {
      const search = (document.getElementById('logSearchInput').value || '').toLowerCase();
      const rowDomain = row.getAttribute('data-domain') || '';
      const rowLevel = row.getAttribute('data-level') || '';
      const rowText = row.innerText.toLowerCase();

      let domainMatch = true;
      if (currentFilter === 'DEBUG') {
        domainMatch = (rowLevel === 'DEBUG');
      } else if (currentFilter === 'ERROR') {
        domainMatch = (rowLevel === 'ERROR');
      } else if (currentFilter !== 'ALL') {
        domainMatch = (rowDomain === currentFilter);
      }

      const searchMatch = !search || rowText.includes(search);
      row.style.display = (domainMatch && searchMatch) ? '' : 'none';
    }

    async function exportLogs() {
      try {
        const res = await fetch('/api/logs');
        if (!res.ok) {
          alert('Failed to fetch logs: ' + res.statusText);
          return;
        }
        const data = await res.json();
        const jsonStr = JSON.stringify(data, null, 2);
        const blob = new Blob([jsonStr], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = 'tantu-logs.json';
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
      } catch (err) {
        alert('Error exporting logs: ' + err.message);
      }
    }

    async function loadInitialLogs() {
      try {
        const res = await fetch('/api/logs');
        if (!res.ok) return;
        const events = await res.json();
        if (Array.isArray(events) && events.length > 0) {
          document.getElementById('logConsole').innerHTML = '';
          events.forEach(ev => {
            const domain = ev.domain || ev.tag || 'SYS';
            addLog(domain, ev.message, ev.level, ev.metadata);
          });
        }
      } catch (_) {}
    }

    function clearLogs() {
      document.getElementById('logConsole').innerHTML = '';
    }

    let selectedPeerTarget = '';

    async function updateStatus() {
      try {
        const res = await fetch('/api/status');
        if (!res.ok) return;
        const data = await res.json();
        
        document.getElementById('idTransport').innerText = data.transport || 'loopback';
        document.getElementById('idP2P').innerText = data.p2p_addr || '127.0.0.1:9877';
        document.getElementById('idWeb').innerText = data.web_addr || '127.0.0.1:9876';
        if (data.identity) {
          document.getElementById('idSAS').innerText = data.identity.sas || '-';
          document.getElementById('idFP').innerText = data.identity.fingerprint ? data.identity.fingerprint.slice(0, 24) + '...' : '-';
        }

        const peers = data.peers || [];
        const peersList = document.getElementById('peersList');
        const dropSelector = document.getElementById('dropPeerSelector');
        const dropPills = document.getElementById('dropPeerPills');
        const peerDot = document.getElementById('peerDot');
        const peerLabel = document.getElementById('peerLabel');

        if (peers.length === 0) {
          selectedPeerTarget = '';
          peerLabel.innerText = 'Loopback Mode (No LAN Peers)';
          peerDot.className = 'status-dot offline';
          peersList.innerHTML = '<p style="color: var(--text-muted); font-size: 0.9rem;">No paired LAN peers yet. Run <code>tantu pair</code> to connect another machine.</p>';
          if (dropSelector) dropSelector.style.display = 'none';
        } else if (peers.length === 1) {
          const p = peers[0];
          selectedPeerTarget = p.fingerprint;
          peerLabel.innerText = (p.name || 'Peer') + ' [Online 🟢]';
          peerDot.className = 'status-dot';
          if (dropSelector) dropSelector.style.display = 'none';
          renderPeersList(peers);
        } else {
          // Multiple peers
          peerDot.className = 'status-dot';
          let activePeer = peers.find(p => p.active) || peers[0];
          if (!selectedPeerTarget) {
            selectedPeerTarget = activePeer.fingerprint;
          } else if (!peers.some(p => p.fingerprint === selectedPeerTarget)) {
            selectedPeerTarget = activePeer.fingerprint;
          }

          // Header dropdown
          const optionsHTML = peers.map(p => {
            const sel = (p.fingerprint === activePeer.fingerprint) ? 'selected' : '';
            const defBadge = p.is_default ? ' (Default)' : '';
            return '<option value="' + escapeHTML(p.fingerprint) + '" ' + sel + ' style="background:var(--card-bg); color:var(--text);">' + escapeHTML(p.name) + defBadge + '</option>';
          }).join('');
          peerLabel.innerHTML = '<select id="headerPeerSelect" onchange="switchActivePeer(this.value)" style="background:transparent; color:var(--text); border:none; outline:none; font-size:0.85rem; font-weight:600; cursor:pointer;">' + optionsHTML + '</select>';

          // Tab 1 destination pills
          if (dropSelector && dropPills) {
            dropSelector.style.display = 'flex';
            dropPills.innerHTML = peers.map(p => {
              const isSelected = (p.fingerprint === selectedPeerTarget);
              const pillClass = isSelected ? 'pill active' : 'pill';
              const defTag = p.is_default ? ' ⭐️' : '';
              return '<button type="button" class="' + pillClass + '" onclick="selectDropPeer(\'' + escapeHTML(p.fingerprint) + '\')">' + escapeHTML(p.name) + defTag + '</button>';
            }).join('');
          }

          renderPeersList(peers);
        }
      } catch (err) {
        document.getElementById('peerLabel').innerText = 'Disconnected';
        document.getElementById('peerDot').className = 'status-dot offline';
      }

      // Query discovered LAN peers
      try {
        const discRes = await fetch('/api/discovery/peers');
        if (discRes.ok) {
          const discPeers = await discRes.json();
          renderDiscoveredHubs(discPeers);
        }
      } catch (err) {
        // Discovery endpoint non-critical
      }

      // Check for incoming pairing approvals
      await loadPendingPairings();
    }

    async function loadPendingPairings() {
      try {
        const res = await fetch('/api/pair/pending');
        if (!res.ok) return;
        const list = await res.json();
        const banner = document.getElementById('pendingPairingsBanner');
        const container = document.getElementById('pendingPairingsList');
        if (!banner || !container) return;
        if (!list || list.length === 0) {
          banner.style.display = 'none';
          container.innerHTML = '';
          return;
        }
        banner.style.display = 'block';
        container.innerHTML = list.map(function(item) {
          var name = escapeHTML(item.peer_name || 'Nearby Device');
          var addr = escapeHTML(item.remote_addr);
          var sas = escapeHTML(item.peer_sas);
          var id = escapeHTML(item.id);
          return '<div style="display: flex; justify-content: space-between; align-items: center; background: rgba(0,0,0,0.3); padding: 0.75rem 1rem; border-radius: 8px; flex-wrap: wrap; gap: 0.75rem;">' +
            '<div>' +
              '<div style="font-weight: 600; color: #fff;">' + name + ' <span style="font-size: 0.8rem; color: #94a3b8; font-weight: 400;">(' + addr + ')</span></div>' +
              '<div style="font-size: 0.85rem; color: #cbd5e1; margin-top: 0.25rem;">Compare SAS Code with remote screen: <strong style="color: #38bdf8; font-family: monospace; font-size: 1.05rem; letter-spacing: 0.05em; background: rgba(56,189,248,0.15); padding: 0.15rem 0.5rem; border-radius: 4px;">' + sas + '</strong></div>' +
            '</div>' +
            '<div style="display: flex; gap: 0.5rem;">' +
              '<button class="btn-sm" style="background: #10b981; color: white; border: none; font-weight: 600; padding: 0.4rem 1rem; cursor: pointer; border-radius: 6px;" onclick="decidePairing(\'' + id + '\', true)">✓ Approve</button>' +
              '<button class="btn-sm" style="background: #ef4444; color: white; border: none; font-weight: 600; padding: 0.4rem 1rem; cursor: pointer; border-radius: 6px;" onclick="decidePairing(\'' + id + '\', false)">✕ Reject</button>' +
            '</div>' +
          '</div>';
        }).join('');
      } catch (err) {
        // Pending check non-critical
      }
    }

    async function decidePairing(id, accept) {
      try {
        const res = await fetch('/api/pair/decision', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id, accept })
        });
        if (res.ok) {
          await loadPendingPairings();
          await updateStatus();
        }
      } catch (err) {
        console.error('Pairing decision failed', err);
      }
    }

    function renderDiscoveredHubs(discovered) {
      const card = document.getElementById('discoveredHubsCard');
      const list = document.getElementById('discoveredHubsList');
      if (!card || !list) return;

      const unpaired = (discovered || []).filter(d => !d.is_paired);
      if (unpaired.length === 0) {
        card.style.display = 'none';
        return;
      }

      card.style.display = 'block';
      list.innerHTML = unpaired.map(function(d) {
        return '<div style="display: flex; justify-content: space-between; align-items: center; background: var(--surface); padding: 0.6rem 0.85rem; border-radius: 6px; border: 1px solid var(--border);">' +
          '<div>' +
            '<div style="font-weight: 600; font-size: 0.9rem; color: var(--text);">💻 ' + escapeHTML(d.name) + '</div>' +
            '<div style="font-size: 0.75rem; color: var(--text-muted); font-family: monospace;">' + escapeHTML(d.address) + ' · SAS: <span style="color: var(--accent); font-weight: 600;">' + escapeHTML(d.sas) + '</span></div>' +
          '</div>' +
          '<button class="btn btn-primary" style="padding: 0.3rem 0.75rem; font-size: 0.8rem;" onclick="prefillPairModal(\'' + escapeHTML(d.address) + '\', \'' + escapeHTML(d.sas) + '\')">⚡ Pair</button>' +
        '</div>';
      }).join('');
    }

    function prefillPairModal(addr, sas) {
      openPairModal();
      const input = document.getElementById('pairRemoteAddrInput') || document.getElementById('pairPeerAddress');
      if (input) {
        input.value = addr;
        input.focus();
      }
      if (sas) {
        const statusMsg = document.getElementById('pairStatusMsg');
        if (statusMsg) {
          statusMsg.innerHTML = '<span style="color:var(--accent);">Peer SAS: <strong>' + escapeHTML(sas) + '</strong></span>';
        }
      }
    }

    function selectDropPeer(fp) {
      selectedPeerTarget = fp;
      updateStatus();
    }

    async function switchActivePeer(fp) {
      try {
        const res = await fetch('/api/peers/active', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ peer: fp })
        });
        if (res.ok) {
          selectedPeerTarget = fp;
          updateStatus();
        } else {
          const d = await res.json();
          alert('Failed to switch active peer: ' + (d.message || res.statusText));
        }
      } catch (err) {
        alert('Error switching active peer: ' + err.message);
      }
    }

    function renderPeersList(peers) {
      const peersList = document.getElementById('peersList');
      peersList.innerHTML = peers.map(function(pr) {
        const defaultBadge = pr.is_default ? '<span class="tag tag-oauth">Default</span>' : '';
        const activeBadge = pr.active ? '<span class="tag tag-drop">Active</span>' : '';
        const defaultBtn = !pr.is_default ? '<button class="btn-sm" onclick="setDefaultPeer(\'' + escapeHTML(pr.fingerprint) + '\')">⭐️ Set Default</button>' : '';
        const fpShort = pr.fingerprint ? (pr.fingerprint.slice(0, 16) + '...') : '-';

        return '<div style="background: rgba(255,255,255,0.02); border:1px solid var(--border); border-radius:8px; padding:0.85rem 1rem; margin-bottom:0.75rem;">' +
          '<div style="display:flex; justify-content:space-between; align-items:center; flex-wrap:wrap; gap:0.5rem;">' +
            '<div style="display:flex; align-items:center; gap:0.5rem;">' +
              '<span style="font-weight:600; color:var(--text); font-size:1rem;">' + escapeHTML(pr.name || 'Unnamed Peer') + '</span>' +
              defaultBadge + activeBadge +
            '</div>' +
            '<div style="display:flex; gap:0.35rem;">' +
              '<button class="btn-sm" onclick="promptEditAlias(\'' + escapeHTML(pr.fingerprint) + '\', \'' + escapeHTML(pr.alias || pr.name || '') + '\')">✏️ Alias</button>' +
              defaultBtn +
              '<button class="btn-sm" style="color:#f87171; border-color:rgba(239,68,68,0.3);" onclick="confirmUnpair(\'' + escapeHTML(pr.fingerprint) + '\', \'' + escapeHTML(pr.name || '') + '\')">🗑️ Unpair</button>' +
            '</div>' +
          '</div>' +
          '<div style="font-size:0.8rem; color:var(--text-muted); margin-top:0.5rem; display:flex; gap:1rem; flex-wrap:wrap;">' +
            '<span>Address: <strong style="color:var(--text);">' + escapeHTML(pr.address) + '</strong></span>' +
            '<span>SAS: <strong style="color:#60a5fa;">' + escapeHTML(pr.sas) + '</strong></span>' +
            '<span>FP: <span style="font-family:monospace; font-size:0.75rem;">' + escapeHTML(fpShort) + '</span></span>' +
          '</div>' +
        '</div>';
      }).join('');
    }

    async function promptEditAlias(fp, currentAlias) {
      const newAlias = prompt('Enter friendly nickname / alias for this peer:', currentAlias || '');
      if (newAlias === null) return;
      try {
        const res = await fetch('/api/peers/alias', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ fingerprint: fp, alias: newAlias.trim() })
        });
        if (res.ok) {
          updateStatus();
        } else {
          const d = await res.json();
          alert('Failed to update alias: ' + (d.message || res.statusText));
        }
      } catch (err) {
        alert('Error updating alias: ' + err.message);
      }
    }

    async function setDefaultPeer(fp) {
      try {
        const res = await fetch('/api/peers/default', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ fingerprint: fp })
        });
        if (res.ok) {
          updateStatus();
        } else {
          const d = await res.json();
          alert('Failed to set default peer: ' + (d.message || res.statusText));
        }
      } catch (err) {
        alert('Error setting default peer: ' + err.message);
      }
    }

    async function confirmUnpair(fp, name) {
      if (!confirm('Are you sure you want to unpair from ' + (name || 'this peer') + '? This will revoke mutual zero-trust encryption.')) {
        return;
      }
      try {
        const res = await fetch('/api/peers/remove', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ fingerprint: fp })
        });
        if (res.ok) {
          updateStatus();
        } else {
          const d = await res.json();
          alert('Failed to unpair: ' + (d.message || res.statusText));
        }
      } catch (err) {
        alert('Error unpairing: ' + err.message);
      }
    }

    function openPairModal() {
      const sas = document.getElementById('idSAS').innerText || '---';
      document.getElementById('pairModalSAS').innerText = sas;
      const p2p = document.getElementById('idP2P').innerText || '9877';
      let port = '9877';
      if (p2p.includes(':')) {
        port = p2p.split(':').pop();
      }
      document.getElementById('pairCmdText').innerText = 'tantu pair --peer=<THIS_MACHINE_IP>:' + port;
      document.getElementById('pairStatusMsg').innerText = '';
      document.getElementById('pairModal').style.display = 'flex';
    }

    function closePairModal() {
      document.getElementById('pairModal').style.display = 'none';
    }

    async function copyPairCmd() {
      const text = document.getElementById('pairCmdText').innerText;
      const btn = document.getElementById('btnCopyPairCmd');
      try {
        if (navigator.clipboard && window.isSecureContext) {
          await navigator.clipboard.writeText(text);
        } else {
          const ta = document.createElement('textarea');
          ta.value = text;
          document.body.appendChild(ta);
          ta.select();
          document.execCommand('copy');
          document.body.removeChild(ta);
        }
        const oldText = btn.innerText;
        btn.innerText = '✅ Copied!';
        setTimeout(() => { btn.innerText = oldText; }, 2000);
      } catch (e) {
        alert('Failed to copy: ' + e.message);
      }
    }

    async function initiateWebPairing() {
      const input = document.getElementById('pairRemoteAddrInput');
      const addr = (input.value || '').trim();
      const statusMsg = document.getElementById('pairStatusMsg');
      const btn = document.getElementById('btnPairConnect');
      if (!addr) {
        statusMsg.style.color = '#f87171';
        statusMsg.innerText = 'Please enter a valid peer address (e.g. 192.168.1.50:9877).';
        return;
      }
      btn.disabled = true;
      btn.innerText = 'Pairing...';
      statusMsg.style.color = '#38bdf8';
      statusMsg.innerText = 'Connecting to ' + addr + ' for in-band pairing handshake...';

      try {
        const res = await fetch('/api/pair/initiate', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ peer_addr: addr })
        });
        const data = await res.json();
        if (res.ok) {
          statusMsg.style.color = '#4ade80';
          statusMsg.innerText = '✅ Successfully paired with ' + (data.peer_name || addr) + ' (SAS: ' + data.peer_sas + ')';
          input.value = '';
          updateStatus();
        } else {
          statusMsg.style.color = '#f87171';
          statusMsg.innerText = '❌ Pairing failed: ' + (data.error || res.statusText);
        }
      } catch (err) {
        statusMsg.style.color = '#f87171';
        statusMsg.innerText = '❌ Network error: ' + err.message;
      } finally {
        btn.disabled = false;
        btn.innerText = 'Pair Device';
      }
    }

    // Drag and Drop
    const dropZone = document.getElementById('dropZone');
    const fileInput = document.getElementById('fileInput');

    ['dragenter', 'dragover'].forEach(name => {
      dropZone.addEventListener(name, (e) => { e.preventDefault(); dropZone.classList.add('dragover'); });
    });
    ['dragleave', 'drop'].forEach(name => {
      dropZone.addEventListener(name, (e) => { e.preventDefault(); dropZone.classList.remove('dragover'); });
    });

    dropZone.addEventListener('drop', (e) => {
      if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
        uploadFile(e.dataTransfer.files[0]);
      }
    });
    fileInput.addEventListener('change', () => {
      if (fileInput.files && fileInput.files.length > 0) {
        uploadFile(fileInput.files[0]);
      }
    });

    async function uploadFile(file) {
      const progress = document.getElementById('uploadProgress');
      const fill = document.getElementById('progressFill');
      const pFile = document.getElementById('progressFile');
      const pPercent = document.getElementById('progressPercent');
      const status = document.getElementById('dropStatus');

      progress.style.display = 'block';
      fill.style.width = '20%';
      pFile.innerText = file.name + ' (' + (file.size / (1024*1024)).toFixed(1) + ' MB)';
      pPercent.innerText = 'Streaming...';
      status.style.display = 'none';

      addLog('DROP', 'Initiating file upload: ' + file.name + ' (' + file.size + ' bytes)');

      const fd = new FormData();
      fd.append('file', file);
      if (selectedPeerTarget) {
        fd.append('peer', selectedPeerTarget);
      }

      try {
        fill.style.width = '60%';
        const res = await fetch('/api/drop/upload', {
          method: 'POST',
          body: fd
        });
        fill.style.width = '100%';
        const data = await res.json();
        if (res.ok && data.status === 'success') {
          status.className = 'status-banner success';
          status.innerHTML = '✅ <strong>File transferred successfully to peer!</strong>';
          addLog('DROP', 'Completed transfer of ' + file.name);
        } else {
          status.className = 'status-banner error';
          status.innerHTML = '❌ <strong>Transfer failed:</strong> ' + (data.message || 'Unknown error');
          addLog('ERROR', 'Drop failed: ' + (data.message || 'Unknown error'));
        }
      } catch (err) {
        status.className = 'status-banner error';
        status.innerHTML = '❌ <strong>Connection error:</strong> ' + err.message;
        addLog('ERROR', 'Upload error: ' + err.message);
      } finally {
        setTimeout(() => { progress.style.display = 'none'; fill.style.width = '0%'; }, 2500);
      }
    }

    async function sendTextDrop() {
      const textarea = document.getElementById('textPayload');
      const text = textarea.value.trim();
      if (!text) return;

      const btn = document.getElementById('btnSendText');
      btn.disabled = true;
      addLog('DROP', 'Sending text snippet (' + text.length + ' chars)...');

      const payload = { text: text };
      if (selectedPeerTarget) {
        payload.peer = selectedPeerTarget;
      }

      try {
        const res = await fetch('/api/drop/upload', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });
        const data = await res.json();
        if (res.ok && data.status === 'success') {
          textarea.value = '';
          addLog('DROP', 'Text snippet sent successfully.');
        } else {
          addLog('ERROR', 'Failed to send text: ' + (data.message || 'Unknown error'));
        }
      } catch (err) {
        addLog('ERROR', 'Text send failed: ' + err.message);
      } finally {
        btn.disabled = false;
      }
    }

    async function relayOAuth(e) {
      e.preventDefault();
      const input = document.getElementById('oauthUrlInput');
      const btn = document.getElementById('btnRelay');
      const status = document.getElementById('relayStatus');
      const url = input.value.trim();
      if (!url) return;

      btn.disabled = true;
      status.className = 'status-banner relaying';
      status.innerHTML = '⏳ Relaying OAuth callback through bridge...';
      addLog('OAUTH', 'Forwarding URL: ' + url.slice(0, 60) + '...');

      const payload = { url: url };
      if (selectedPeerTarget) {
        payload.peer = selectedPeerTarget;
      }

      try {
        const res = await fetch('/api/relay/open', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });
        const data = await res.json();
        if (res.ok && data.status === 'success') {
          status.className = 'status-banner success';
          status.innerHTML = '✅ <strong>Authentication completed successfully!</strong>';
          input.value = '';
          addLog('OAUTH', 'OAuth flow completed successfully.');
        } else {
          status.className = 'status-banner error';
          status.innerHTML = '❌ <strong>Relay failed:</strong> ' + (data.message || 'Unknown error');
          addLog('ERROR', 'Relay failed: ' + (data.message || 'Unknown error'));
        }
      } catch (err) {
        status.className = 'status-banner error';
        status.innerHTML = '❌ <strong>Connection error:</strong> ' + err.message;
        addLog('ERROR', 'Relay error: ' + err.message);
      } finally {
        btn.disabled = false;
      }
    }

    function copySnippet(text, btn) {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(onSuccess, fallbackCopy);
      } else {
        fallbackCopy();
      }

      function fallbackCopy() {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        try {
          document.execCommand('copy');
          onSuccess();
        } catch (_) {}
        document.body.removeChild(ta);
      }

      function onSuccess() {
        const oldText = btn.innerHTML;
        btn.innerHTML = '✅ Copied!';
        btn.style.color = '#34d399';
        btn.style.borderColor = '#10b981';
        setTimeout(() => {
          btn.innerHTML = oldText;
          btn.style.color = '';
          btn.style.borderColor = '';
        }, 2000);
      }
    }

    function openURL(url) {
      window.open(url, '_blank');
    }

    async function openDownloadFolder() {
      try {
        const res = await fetch('/api/open-folder', { method: 'POST' });
        const data = await res.json();
        if (!res.ok || data.status !== 'success') {
          alert('Failed to open folder: ' + (data.message || 'unknown error'));
        }
      } catch (err) {
        alert('Error opening folder: ' + err.message);
      }
    }

    async function promptChangeDownloadDir() {
      const current = document.getElementById('downloadDirPath').innerText;
      const newDir = prompt('Enter new downloads directory path:', current);
      if (!newDir || newDir.trim() === '' || newDir.trim() === current) return;
      try {
        const res = await fetch('/api/config', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ output_dir: newDir.trim() })
        });
        const data = await res.json();
        if (res.ok && data.status === 'success') {
          document.getElementById('downloadDirPath').innerText = data.output_dir || newDir.trim();
        } else {
          alert('Failed to update directory: ' + (data.message || 'unknown error'));
        }
      } catch (err) {
        alert('Error updating directory: ' + err.message);
      }
    }

    async function loadConfig() {
      try {
        const res = await fetch('/api/config');
        if (res.ok) {
          const cfg = await res.json();
          if (cfg.output_dir) {
            document.getElementById('downloadDirPath').innerText = cfg.output_dir;
          }
        }
      } catch (_) {}
    }

    async function loadRecentDrops() {
      try {
        const res = await fetch('/api/drop/recent');
        if (!res.ok) return;
        const items = await res.json();
        renderRecentDrops(items);
      } catch (_) {}
    }

    function renderRecentDrops(items) {
      const list = document.getElementById('receivedDropsList');
      if (!items || items.length === 0) {
        list.innerHTML = '<p style="color: var(--text-muted); font-size: 0.85rem; text-align: center; padding: 2rem 0;">No received items yet.<br>Snippets and files sent by your peer appear here in real-time.</p>';
        return;
      }
      const reversed = items.slice().reverse();
      list.innerHTML = reversed.map(function(item) {
        const timeStr = item.timestamp ? new Date(item.timestamp).toLocaleTimeString() : '';
        const isFile = item.kind === 'file';
        const isURL = item.is_url;
        const icon = isFile ? '📁' : (isURL ? '🌐' : '📝');
        const title = isFile ? (item.name || 'Received File') : (isURL ? 'Web URL' : 'Text Snippet');
        const sizeStr = formatBytes(item.size);

        let contentHTML = '';
        if (isFile) {
          contentHTML = '<div class="received-item-content">Path: ' + escapeHTML(item.saved_path || item.name) + ' (' + sizeStr + ')</div>';
        } else {
          contentHTML = '<div class="received-item-content">' + escapeHTML(item.content || '') + '</div>';
        }

        let actionsHTML = '<div class="received-item-actions">';
        if (!isFile && item.content) {
          actionsHTML += '<button class="btn-sm" onclick="copySnippet(' + JSON.stringify(item.content) + ', this)">📋 Copy</button>';
          if (isURL || item.content.startsWith('http://') || item.content.startsWith('https://')) {
            const cleanURL = item.content.trim();
            actionsHTML += '<button class="btn-sm" onclick="openURL(' + JSON.stringify(cleanURL) + ')">🌐 Open in Browser</button>';
          }
        } else if (isFile && item.saved_path) {
          actionsHTML += '<button class="btn-sm" onclick="openDownloadFolder()">📂 Open in Folder</button>';
        }
        actionsHTML += '</div>';

        return '<div class="received-item">' +
          '<div class="received-item-header">' +
            '<span><strong>' + icon + ' ' + title + '</strong> from ' + escapeHTML(item.from_peer || 'Peer') + '</span>' +
            '<span>' + timeStr + ' &bull; ' + sizeStr + '</span>' +
          '</div>' +
          contentHTML +
          actionsHTML +
        '</div>';
      }).join('');
    }

    function formatBytes(bytes) {
      if (!bytes || bytes <= 0) return '0 B';
      if (bytes < 1024) return bytes + ' B';
      if (bytes < 1024*1024) return (bytes / 1024).toFixed(1) + ' KB';
      return (bytes / (1024*1024)).toFixed(1) + ' MB';
    }

    function escapeHTML(str) {
      return String(str).replace(/[&<>'"]/g, tag => ({
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        "'": '&#39;',
        '"': '&quot;'
      }[tag] || tag));
    }

    // Connect SSE for live logs
    if (window.EventSource) {
      const sse = new EventSource('/api/events');
      sse.onmessage = (e) => {
        try {
          const item = JSON.parse(e.data);
          const domain = item.domain || item.tag || 'SYS';
          addLog(domain, item.message || JSON.stringify(item), item.level, item.metadata);
          if (domain === 'DROP') {
            loadRecentDrops();
          }
        } catch (_) {
          addLog('SYS', e.data, 'INFO');
        }
      };
      sse.onerror = () => { /* reconnects automatically */ };
    }

    setInterval(updateStatus, 3000);
    updateStatus();
    loadConfig();
    loadRecentDrops();
    loadInitialLogs();
  </script>
</body>
</html>`

type relayResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func isAllowedOrigin(originHeader string) bool {
	if originHeader == "" {
		return true
	}
	u, err := url.Parse(originHeader)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func (h *Hub) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Protect all internal /api/ routes against unauthorized cross-origin access and CSRF
		if strings.HasPrefix(r.URL.Path, "/api/") {
			origin := r.Header.Get("Origin")
			if origin != "" && !isAllowedOrigin(origin) {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"status":  "error",
					"message": "forbidden cross-origin request",
				})
				return
			}
			referer := r.Header.Get("Referer")
			if referer != "" && !isAllowedOrigin(referer) {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"status":  "error",
					"message": "forbidden cross-origin request",
				})
				return
			}
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

type statusResponse struct {
	Status          string         `json:"status"`
	Version         string         `json:"version"`
	Identity        identityStatus `json:"identity"`
	Transport       string         `json:"transport"`
	P2PAddr         string         `json:"p2p_addr"`
	WebAddr         string         `json:"web_addr"`
	ActivePeer      string         `json:"active_peer,omitempty"`
	DiscoveredCount int            `json:"discovered_count"`
	DiscoveredPeers int            `json:"discovered_peers"`
	Peers           []peerStatus   `json:"peers"`
}

type discoveredPeerResponse struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	SAS         string `json:"sas"`
	Fingerprint string `json:"fingerprint"`
	IsPaired    bool   `json:"is_paired"`
}

type identityStatus struct {
	Fingerprint string `json:"fingerprint"`
	SAS         string `json:"sas"`
}

type peerStatus struct {
	Name        string `json:"name"`
	Alias       string `json:"alias,omitempty"`
	IsDefault   bool   `json:"is_default,omitempty"`
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
	SAS         string `json:"sas"`
	Active      bool   `json:"active"`
}

// registerDashboardRoutes configures all routes for the Single-Page Web Dashboard and APIs.
func (h *Hub) registerDashboardRoutes(mux *http.ServeMux) {
	// 1. GET / — Unified Dashboard SPA
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		webAddr := h.actualWeb
		if webAddr == "" {
			webAddr = h.cfg.WebAddr
		}
		bookmarkletJS := fmt.Sprintf("javascript:void(window.open('http://%s/relay?url='+encodeURIComponent(location.href),'_blank','width=550,height=380'))", webAddr)

		page := strings.ReplaceAll(dashboardHTML, "{{VERSION}}", HubVersion)
		page = strings.ReplaceAll(page, "{{BOOKMARKLET_HREF}}", bookmarkletJS)
		_, _ = w.Write([]byte(page))
	})

	// 2. GET /healthz
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// 3. GET /api/status
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		h.mu.RLock()
		activeFP := h.activePeer
		defer h.mu.RUnlock()

		var peers []peerStatus
		if h.store != nil {
			allPeers := h.store.ListPeers()
			for _, p := range allPeers {
				isActive := false
				if activeFP != "" && p.Fingerprint == activeFP {
					isActive = true
				} else if activeFP == "" && (p.IsDefault || len(allPeers) == 1) {
					isActive = true
				}
				peers = append(peers, peerStatus{
					Name:        p.DisplayName(),
					Alias:       p.Alias,
					IsDefault:   p.IsDefault,
					Address:     p.Address,
					Fingerprint: p.Fingerprint,
					SAS:         p.SAS(),
					Active:      isActive,
				})
			}
		}

		discCount := 0
		if engine := h.DiscoveryEngine(); engine != nil {
			discCount = len(engine.ListNodes())
		}

		resp := statusResponse{
			Status:          "online",
			Version:         HubVersion,
			Transport:       h.cfg.TransportType,
			P2PAddr:         "",
			WebAddr:         h.actualWeb,
			ActivePeer:      activeFP,
			DiscoveredCount: discCount,
			DiscoveredPeers: discCount,
			Peers:           peers,
		}
		if h.actualP2P != nil {
			resp.P2PAddr = h.actualP2P.String()
		}
		if h.identity != nil {
			resp.Identity = identityStatus{
				Fingerprint: h.identity.Fingerprint,
				SAS:         pairing.SASCode(h.identity.Fingerprint),
			}
		}

		writeJSON(w, http.StatusOK, resp)
	})

	// 4. GET /relay — Legacy bookmarklet handler
	mux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, relayResponse{
				Status:  "error",
				Message: "method not allowed, use GET",
			})
			return
		}

		respond := func(status int, resp relayResponse) {
			if strings.Contains(r.Header.Get("Accept"), "text/html") {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(status)
				if resp.Status == "success" {
					fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>✅ Auth Relayed</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;}.box{background:#131722;border:1px solid #232a3b;border-radius:12px;padding:2rem;max-width:450px;text-align:center;box-shadow:0 4px 20px rgba(0,0,0,0.5);}.icon{font-size:2.5rem;margin-bottom:0.75rem;}h2{color:#34d399;margin:0 0 0.5rem 0;}p{color:#8b949e;font-size:0.9rem;margin:0 0 1rem 0;}.hint{color:#64748b;font-size:0.8rem;}</style></head><body><div class="box"><div class="icon">✅</div><h2>Authentication Relayed!</h2><p>%s</p><div class="hint">This window will close automatically in 3 seconds...</div></div><script>setTimeout(function(){window.close();},3000);</script></body></html>`, resp.Message)
				} else {
					fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>❌ Relay Error</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;}.box{background:#131722;border:1px solid #232a3b;border-radius:12px;padding:2rem;max-width:450px;text-align:center;box-shadow:0 4px 20px rgba(0,0,0,0.5);}.icon{font-size:2.5rem;margin-bottom:0.75rem;}h2{color:#f87171;margin:0 0 0.5rem 0;}p{color:#8b949e;font-size:0.9rem;margin:0 0 1rem 0;}.hint{color:#64748b;font-size:0.8rem;}</style></head><body><div class="box"><div class="icon">❌</div><h2>Relay Error</h2><p>%s</p><div class="hint">Check terminal logs or verify your peer is connected.</div></div></body></html>`, resp.Message)
				}
				return
			}
			writeJSON(w, status, resp)
		}

		rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
		if rawURL == "" {
			respond(http.StatusBadRequest, relayResponse{
				Status:  "error",
				Message: "missing or empty url parameter",
			})
			return
		}

		oauthURL := browser.SanitizeURL(rawURL)
		conn, err := h.DialPeer("")
		if err != nil {
			respond(http.StatusBadGateway, relayResponse{
				Status:  "error",
				Message: fmt.Sprintf("failed to dial peer: %v", err),
			})
			return
		}
		defer conn.Close()

		relayCtx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
		defer cancel()

		bcfg := bridge.BSideConfig{Timeout: h.cfg.Timeout}
		if err := bridge.HandleBSide(relayCtx, conn, oauthURL, bcfg); err != nil {
			respond(http.StatusInternalServerError, relayResponse{
				Status:  "error",
				Message: fmt.Sprintf("bridge flow failed: %v", err),
			})
			return
		}

		respond(http.StatusOK, relayResponse{
			Status:  "success",
			Message: "Authentication completed successfully",
		})
	})

	// 5. POST /api/relay/open — Manual URL forwarder
	mux.HandleFunc("/api/relay/open", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, relayResponse{
				Status:  "error",
				Message: "method not allowed, use POST",
			})
			return
		}

		var req struct {
			URL  string `json:"url"`
			Peer string `json:"peer"`
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, relayResponse{Status: "error", Message: "invalid json body"})
				return
			}
		} else {
			req.URL = r.FormValue("url")
			req.Peer = r.FormValue("peer")
		}

		rawURL := strings.TrimSpace(req.URL)
		if rawURL == "" {
			writeJSON(w, http.StatusBadRequest, relayResponse{Status: "error", Message: "missing url field"})
			return
		}

		oauthURL := browser.SanitizeURL(rawURL)
		conn, err := h.DialPeer(req.Peer)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, relayResponse{
				Status:  "error",
				Message: fmt.Sprintf("failed to dial peer: %v", err),
			})
			return
		}
		defer conn.Close()

		relayCtx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
		defer cancel()

		bcfg := bridge.BSideConfig{Timeout: h.cfg.Timeout}
		if err := bridge.HandleBSide(relayCtx, conn, oauthURL, bcfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, relayResponse{
				Status:  "error",
				Message: fmt.Sprintf("bridge flow failed: %v", err),
			})
			return
		}

		writeJSON(w, http.StatusOK, relayResponse{
			Status:  "success",
			Message: "Authentication completed successfully",
		})
	})

	// 6. POST /api/drop/upload — File & Text QuickDrop transfer
	mux.HandleFunc("/api/drop/upload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}

		contentType := r.Header.Get("Content-Type")

		// JSON text drop
		if strings.HasPrefix(contentType, "application/json") {
			var textReq struct {
				Text string `json:"text"`
				Name string `json:"name"`
				Peer string `json:"peer"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 50*1024*1024)).Decode(&textReq); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json payload"})
				return
			}
			if textReq.Text == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "empty text payload"})
				return
			}

			conn, err := h.DialPeer(textReq.Peer)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"status": "error", "message": fmt.Sprintf("dial peer failed: %v", err)})
				return
			}
			defer conn.Close()

			meta := drop.DropSend{
				Kind: drop.DropKindText,
				Name: textReq.Name,
				Size: int64(len(textReq.Text)),
			}
			sendCtx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
			defer cancel()

			if err := drop.SendDrop(sendCtx, conn, meta, strings.NewReader(textReq.Text), drop.SendDropConfig{Timeout: h.cfg.Timeout}); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": fmt.Sprintf("send drop failed: %v", err)})
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"status":  "success",
				"message": "Text drop sent successfully",
				"size":    len(textReq.Text),
			})
			return
		}

		// Multipart file drop (up to 5GB)
		r.Body = http.MaxBytesReader(w, r.Body, 5*1024*1024*1024)
		if err := r.ParseMultipartForm(32 * 1024 * 1024); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": fmt.Sprintf("multipart parse error (max 5GB): %v", err)})
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()

		file, header, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "missing file field in form"})
			return
		}
		defer file.Close()

		targetPeer := r.FormValue("peer")
		conn, err := h.DialPeer(targetPeer)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"status": "error", "message": fmt.Sprintf("dial peer failed: %v", err)})
			return
		}
		defer conn.Close()

		fileName := filepath.Base(header.Filename)
		mimeType := mime.TypeByExtension(filepath.Ext(fileName))
		meta := drop.DropSend{
			Kind:     drop.DropKindFile,
			Name:     fileName,
			Size:     header.Size,
			MIMEType: mimeType,
		}

		sendCtx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
		defer cancel()

		if err := drop.SendDrop(sendCtx, conn, meta, file, drop.SendDropConfig{Timeout: h.cfg.Timeout}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": fmt.Sprintf("drop file transfer failed: %v", err)})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "success",
			"message": "File drop sent successfully",
			"name":    fileName,
			"size":    header.Size,
		})
	})

	// 7. GET /api/logs — Historical RingBuffer hydration
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		if h.logger != nil {
			h.logger.HandleLogs(w, r)
		} else {
			writeJSON(w, http.StatusOK, []any{})
		}
	})

	// 8. GET /api/events — SSE Stream
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		if h.logger != nil {
			h.logger.HandleEvents(w, r)
		} else {
			http.Error(w, "logger unavailable", http.StatusServiceUnavailable)
		}
	})

	// 9. GET /api/drop/recent — Returns list of recent received drops
	mux.HandleFunc("/api/drop/recent", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		if h.recentDrops != nil {
			writeJSON(w, http.StatusOK, h.recentDrops.GetAll())
		} else {
			writeJSON(w, http.StatusOK, []ReceivedDropItem{})
		}
	})

	// 10. GET /api/config & POST /api/config — Retrieve or update Hub config
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]string{
				"output_dir": h.OutputDir(),
				"transport":  h.TransportType(),
				"web_addr":   h.WebAddr(),
			})
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				OutputDir string `json:"output_dir"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json body"})
				return
			}
			req.OutputDir = strings.TrimSpace(req.OutputDir)
			if req.OutputDir == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "output_dir cannot be empty"})
				return
			}
			if err := os.MkdirAll(req.OutputDir, 0755); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": fmt.Sprintf("cannot create directory: %v", err)})
				return
			}
			h.SetOutputDir(req.OutputDir)
			writeJSON(w, http.StatusOK, map[string]string{
				"status":     "success",
				"message":    "configuration updated",
				"output_dir": h.OutputDir(),
			})
			return
		}
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
	})

	// 11. POST /api/open-folder — Launches OS file explorer for output_dir
	mux.HandleFunc("/api/open-folder", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		dir := h.OutputDir()
		if dir == "" {
			dir = "."
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": fmt.Sprintf("cannot create directory: %v", err)})
			return
		}
		if err := openDirectoryInOS(dir); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": fmt.Sprintf("failed to open folder: %v", err)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "folder opened in file explorer",
			"path":    dir,
		})
	})

	// 12. POST /api/pair/initiate — Initiates in-band pairing with a remote peer address
	mux.HandleFunc("/api/pair/initiate", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		var req struct {
			PeerAddr string `json:"peer_addr"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.PeerAddr) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid request: peer_addr is required"})
			return
		}
		peerAddr := strings.TrimSpace(req.PeerAddr)
		h.logger.Action(DomainPeer, fmt.Sprintf("Initiating in-band pairing with %s", peerAddr))
		res, err := pairing.DialInBandPairing(peerAddr, h.store, func(peerSAS, localSAS string) bool {
			h.logger.Action(DomainPeer, fmt.Sprintf("Pairing SAS match with %s: %s vs %s", peerAddr, peerSAS, localSAS))
			return true
		})
		if err != nil {
			h.logger.Error(DomainPeer, fmt.Sprintf("Pairing with %s failed: %v", peerAddr, err))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "error": err.Error()})
			return
		}
		if res.Accepted {
			h.logger.Action(DomainPeer, fmt.Sprintf("✅ Successfully paired with %s (SAS: %s)", peerAddr, res.PeerSAS))
		}
		peerName := ""
		if h.store != nil {
			if p, ok := h.store.GetPeer(res.PeerFingerprint); ok {
				peerName = p.DisplayName()
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":           "paired",
			"accepted":         res.Accepted,
			"peer_addr":        peerAddr,
			"peer_name":        peerName,
			"peer_sas":         res.PeerSAS,
			"peer_fingerprint": res.PeerFingerprint,
		})
	})

	// 12a. GET /api/pair/pending — Returns list of pending incoming pairing requests
	mux.HandleFunc("/api/pair/pending", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, h.ListPendingPairings())
	})

	// 12b. POST /api/pair/decision — Approves or rejects a pending pairing request
	mux.HandleFunc("/api/pair/decision", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		var req struct {
			ID     string `json:"id"`
			Accept bool   `json:"accept"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ID) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid request: id is required"})
			return
		}
		ok := h.ResolvePendingPairing(req.ID, req.Accept)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"status": "error", "message": "pending pairing not found or expired"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"id":       req.ID,
			"accepted": req.Accept,
		})
	})

	// 13. POST /api/peers/active — Set active peer
	mux.HandleFunc("/api/peers/active", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		var req struct {
			Peer string `json:"peer"`
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json body"})
				return
			}
		} else {
			req.Peer = r.FormValue("peer")
		}

		if err := h.SetActivePeer(req.Peer); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "success",
			"active_peer": h.GetActivePeer(),
		})
	})

	// 14. POST /api/peers/alias — Set peer alias
	mux.HandleFunc("/api/peers/alias", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		var req struct {
			Fingerprint string `json:"fingerprint"`
			Alias       string `json:"alias"`
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json body"})
				return
			}
		} else {
			req.Fingerprint = r.FormValue("fingerprint")
			req.Alias = r.FormValue("alias")
		}

		if req.Fingerprint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "fingerprint is required"})
			return
		}
		if h.store == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": "peer store not available"})
			return
		}
		if err := h.store.SetAlias(req.Fingerprint, req.Alias); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "alias updated",
		})
	})

	// 15. POST /api/peers/default — Set default peer
	mux.HandleFunc("/api/peers/default", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		var req struct {
			Fingerprint string `json:"fingerprint"`
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json body"})
				return
			}
		} else {
			req.Fingerprint = r.FormValue("fingerprint")
		}

		if req.Fingerprint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "fingerprint is required"})
			return
		}
		if h.store == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": "peer store not available"})
			return
		}
		if err := h.store.SetDefault(req.Fingerprint); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "default peer updated",
		})
	})

	// 16. POST /api/peers/remove — Remove / unpair peer
	mux.HandleFunc("/api/peers/remove", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		var req struct {
			Fingerprint string `json:"fingerprint"`
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json body"})
				return
			}
		} else {
			req.Fingerprint = r.FormValue("fingerprint")
		}

		if req.Fingerprint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "fingerprint is required"})
			return
		}
		if h.store == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": "peer store not available"})
			return
		}
		if err := h.store.RemovePeer(req.Fingerprint); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": err.Error()})
			return
		}
		if h.GetActivePeer() == req.Fingerprint {
			_ = h.SetActivePeer("")
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "peer removed",
		})
	})

	// 17. GET /api/discovery/peers — Discovered nearby Hubs on LAN
	mux.HandleFunc("/api/discovery/peers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}

		discovered := make([]discoveredPeerResponse, 0)
		if engine := h.DiscoveryEngine(); engine != nil {
			nodes := engine.ListNodes()
			for _, node := range nodes {
				isPaired := false
				if h.store != nil {
					if _, ok := h.store.GetPeer(node.Fingerprint); ok {
						isPaired = true
					}
				}
				discovered = append(discovered, discoveredPeerResponse{
					Name:        node.InstanceName,
					Address:     node.Address,
					Port:        node.Port,
					SAS:         node.SAS,
					Fingerprint: node.Fingerprint,
					IsPaired:    isPaired,
				})
			}
		}
		writeJSON(w, http.StatusOK, discovered)
	})
}

func openDirectoryInOS(dir string) error {
	cleanDir := filepath.Clean(dir)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", cleanDir)
	case "darwin":
		cmd = exec.Command("open", cleanDir)
	default:
		cmd = exec.Command("xdg-open", cleanDir)
	}
	return cmd.Start()
}
