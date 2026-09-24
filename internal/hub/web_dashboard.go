package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'%3E%3Ctext y='.9em' font-size='90'%3E%F0%9F%94%90%3C/text%3E%3C/svg%3E">
<title>🔐 tantu hub</title>
<style>
  :root {
    --bg: #090b10;
    --card-bg: #131722;
    --card-hover: #181d2b;
    --surface: #0f1420;
    --surface-hover: #1a2233;
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
  .tab-btn:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
    border-radius: 6px;
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }
  .drop-zone:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 3px;
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
  .received-item-preview {
    max-width: 100%;
    max-height: 320px;
    border-radius: 8px;
    border: 1px solid rgba(255,255,255,0.08);
    object-fit: contain;
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

  <nav class="tabs-nav" role="tablist" aria-label="Dashboard sections">
    <button id="tab-btn-drop" class="tab-btn active" role="tab" aria-selected="true" aria-controls="tab-drop" data-tab="tab-drop">📦 QuickDrop</button>
    <button id="tab-btn-relay" class="tab-btn" role="tab" aria-selected="false" aria-controls="tab-relay" data-tab="tab-relay">⚡ OAuth Relay</button>
    <button id="tab-btn-peers" class="tab-btn" role="tab" aria-selected="false" aria-controls="tab-peers" data-tab="tab-peers">🔗 Peers & Network</button>
    <button id="tab-btn-logs" class="tab-btn" role="tab" aria-selected="false" aria-controls="tab-logs" data-tab="tab-logs">📋 Live Logs</button>
  </nav>

  <main class="tab-content">
    <div id="sessionBanner" role="alert" style="display: none; background: rgba(245,158,11,0.12); border: 1px solid rgba(245,158,11,0.4); color: #fbbf24; border-radius: 12px; padding: 0.85rem 1.25rem; margin-bottom: 1.25rem; font-size: 0.9rem;">
      ⚠️ <strong>Dashboard session not established.</strong>
      <span>Actions on this page will fail until you reconnect. Reopen the dashboard from the Hub terminal (press <kbd>o</kbd>) or reload the page the Hub opened for you.</span>
    </div>
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
    <div id="tab-drop" class="tab-pane active" role="tabpanel" aria-labelledby="tab-btn-drop">
      <!-- Download Location Bar -->
      <div class="card" style="margin-bottom: 1.25rem; padding: 0.75rem 1.25rem;">
        <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 0.75rem;">
          <div style="display: flex; align-items: center; gap: 0.5rem; font-size: 0.9rem;">
            <span style="color: var(--text-muted);">📁 Downloads location:</span>
            <code id="downloadDirPath" style="color: #60a5fa; background: rgba(59,130,246,0.1); padding: 0.2rem 0.5rem; border-radius: 4px; font-size: 0.85rem;">...</code>
          </div>
          <div style="display: flex; gap: 0.5rem;">
            <button class="btn-sm" data-action="change-download-dir">✏️ Change</button>
            <button class="btn-sm" data-action="open-folder">📂 Open Folder</button>
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
            <div class="drop-zone" id="dropZone" data-action="choose-file" tabindex="0" role="button" aria-label="Choose a file to send to your peer">
              <div class="drop-zone-icon">📁</div>
              <div class="drop-zone-text">Drag & drop files here, or click to browse</div>
              <div class="drop-zone-subtext">Direct peer-to-peer streaming via mTLS • zero intermediate servers</div>
            </div>
            <input type="file" id="fileInput" aria-label="File to send to your peer" style="display: none;">
            
            <div class="progress-container" id="uploadProgress">
              <div class="progress-bar" role="progressbar" aria-label="File upload progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow="0" id="progressBar">
                <div class="progress-fill" id="progressFill"></div>
              </div>
              <div class="progress-meta">
                <span id="progressFile">Uploading...</span>
                <span id="progressPercent" aria-live="polite">0%</span>
                <button type="button" class="btn-sm" id="btnCancelUpload" data-action="cancel-upload" style="display: none;">✕ Cancel</button>
              </div>
            </div>
            <div class="status-banner" id="dropStatus" role="status" aria-live="polite"></div>
          </div>

          <div class="card">
            <div class="card-title">📝 Quick Text & Snippet Sharing</div>
            <label class="sr-only" for="textPayload">Text snippet to send</label>
            <textarea id="textPayload" placeholder="Paste code snippet, auth tokens, commands, or notes to send immediately to the peer..."></textarea>
            <div style="display: flex; justify-content: flex-end; margin-top: 0.75rem;">
              <button class="btn-primary" id="btnSendText" data-action="send-text">Send to Peer</button>
            </div>
          </div>
        </div>

        <!-- Right Pane: Received Items & History -->
        <div class="card" style="display: flex; flex-direction: column;">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem;">
            <div class="card-title" style="margin-bottom: 0;">📥 Received Items & History</div>
            <button class="btn-sm" data-action="load-recent">🔄 Refresh</button>
          </div>
          <div id="receivedDropsList" style="flex: 1; overflow-y: auto; max-height: 580px; display: flex; flex-direction: column; gap: 0.75rem;">
            <p style="color: var(--text-muted); font-size: 0.85rem; text-align: center; padding: 2rem 0;">No received items yet.<br>Snippets and files sent by your peer appear here in real-time.</p>
          </div>
        </div>
      </div>
    </div>

    <!-- TAB 2: OAUTH RELAY -->
    <div id="tab-relay" class="tab-pane" role="tabpanel" aria-labelledby="tab-btn-relay">
      <div class="bookmarklet-box">
        <a class="bookmarklet-btn" href="{{BOOKMARKLET_HREF}}">⚡ Tantu</a>
        <div class="hint-text">Drag this button to your browser Bookmarks Bar. When logging in on any OAuth tab, click it to relay authorization directly back to your dev terminal!</div>
      </div>

      <div class="card">
        <div class="card-title">⚡ Manual OAuth URL Forwarder</div>
        <form data-action="relay-oauth" class="form-group">
          <input type="text" id="oauthUrlInput" aria-label="OAuth authorization URL" placeholder="Paste OAuth URL (https://accounts.google.com/o/oauth2/...)" required>
          <button type="submit" class="btn-primary" id="btnRelay">Relay to Peer</button>
        </form>
        <div class="status-banner" id="relayStatus" role="status" aria-live="polite"></div>
      </div>
    </div>

    <!-- TAB 3: PEERS & NETWORK -->
    <div id="tab-peers" class="tab-pane" role="tabpanel" aria-labelledby="tab-btn-peers">
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
            <div class="card-title" style="margin-bottom: 0;">👥 Trusted Peers</div>
            <button class="btn-sm" data-action="open-pair-modal">+ Pair New Device</button>
          </div>
          <div id="peersList">
            <p style="color: var(--text-muted); font-size: 0.9rem;">Loading peer status...</p>
          </div>
        </div>
      </div>

      <!-- PAIRING MODAL -->
      <div id="pairModal" class="modal-overlay" role="dialog" aria-modal="true" aria-labelledby="pairModalTitle" style="display:none; position:fixed; inset:0; background:rgba(0,0,0,0.75); backdrop-filter:blur(4px); z-index:9999; align-items:center; justify-content:center;">
        <div class="modal-box" style="background:#0f172a; border:1px solid #334155; border-radius:12px; width:90%; max-width:500px; padding:1.5rem; box-shadow:0 25px 50px -12px rgba(0,0,0,0.5);">
          <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:1rem;">
            <h3 id="pairModalTitle" style="margin:0; font-size:1.1rem; color:#f8fafc;">🔗 Pair New Device</h3>
            <button data-action="close-pair-modal" aria-label="Close pairing dialog" style="background:none; border:none; color:#94a3b8; font-size:1.4rem; cursor:pointer; line-height:1;">&times;</button>
          </div>
          
          <p style="font-size:0.85rem; color:#94a3b8; margin-bottom:1.25rem;">
            Pairing establishes mutual zero-trust encryption (TLS + SAS authentication) between this machine and another tantu instance over in-band port 9877.
          </p>

          <div style="background:rgba(30,41,59,0.7); border:1px solid #334155; border-radius:8px; padding:1rem; margin-bottom:1.25rem; text-align:center;">
            <div style="font-size:0.75rem; text-transform:uppercase; letter-spacing:0.05em; color:#94a3b8; margin-bottom:0.25rem;">Local SAS Verification Code</div>
            <div id="pairModalSAS" style="font-family:monospace; font-size:1.5rem; font-weight:700; color:#60a5fa; letter-spacing:0.1em;">---</div>
          </div>

          <div style="margin-bottom:1.25rem;">
            <label style="display:block; font-size:0.8rem; font-weight:600; color:#cbd5e1; margin-bottom:0.5rem;">Option 1: Discover and pair from the remote device</label>
            <div style="display:flex; gap:0.5rem; align-items:center;">
              <code id="pairCmdText" style="flex:1; background:rgba(0,0,0,0.4); border:1px solid #334155; border-radius:6px; padding:0.5rem 0.75rem; font-size:0.8rem; color:#38bdf8; overflow-x:auto; white-space:nowrap;">tantu pair --peer=&lt;THIS_IP&gt;:9877</code>
              <button class="btn-sm" id="btnCopyPairCmd" data-action="copy-pair-command">📋 Copy</button>
            </div>
          </div>

          <div style="margin-bottom:1.25rem;">
            <label for="pairRemoteAddrInput" style="display:block; font-size:0.8rem; font-weight:600; color:#cbd5e1; margin-bottom:0.5rem;">Option 2: Connect to Remote Peer Address</label>
            <div style="display:flex; gap:0.5rem; align-items:center;">
              <input type="text" id="pairRemoteAddrInput" placeholder="192.168.1.50:9877" style="flex:1; font-size:0.85rem; padding:0.5rem 0.75rem;">
              <button class="btn" id="btnPairConnect" data-action="pair-connect" style="white-space:nowrap; padding:0.5rem 1rem;">Pair Device</button>
            </div>
            <div id="pairStatusMsg" role="status" aria-live="polite" style="font-size:0.8rem; margin-top:0.5rem; min-height:1.2rem;"></div>
          </div>

          <div style="display:flex; justify-content:flex-end;">
            <button class="btn-sm" data-action="close-pair-modal">Close</button>
          </div>
        </div>
      </div>
    </div>

    <!-- TAB 4: LIVE LOGS -->
    <div id="tab-logs" class="tab-pane" role="tabpanel" aria-labelledby="tab-btn-logs">
      <div class="card">
        <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 0.75rem; margin-bottom: 1rem;">
          <div class="card-title" style="margin-bottom: 0;">📋 Live Diagnostics & Activity Feed</div>
          <div style="display: flex; gap: 0.5rem;">
            <button class="btn-sm" data-action="export-logs">📥 Export Logs</button>
            <button class="btn-sm" data-action="clear-logs">Clear</button>
          </div>
        </div>

        <!-- Filter Pills & Search Bar -->
        <div style="display: flex; flex-direction: column; gap: 0.75rem; margin-bottom: 1rem;">
          <div style="display: flex; gap: 0.5rem; flex-wrap: wrap; align-items: center;">
            <span style="font-size: 0.8rem; color: var(--text-muted); margin-right: 0.25rem;">Filter:</span>
            <button class="pill active" data-filter="ALL">All</button>
            <button class="pill" data-filter="OAUTH">🔵 OAuth</button>
            <button class="pill" data-filter="DROP">🟢 QuickDrop</button>
            <button class="pill" data-filter="PEER">🟣 Peer</button>
            <button class="pill" data-filter="NET">🟡 Network</button>
            <button class="pill" data-filter="DEBUG">🐛 Debug / Verbose</button>
            <button class="pill" data-filter="ERROR">🔴 Errors</button>
          </div>

          <div style="position: relative;">
            <input type="text" id="logSearchInput" data-action="filter-logs" aria-label="Search live logs" placeholder="🔍 Search live logs by text, URL, host, or request ID..." style="padding-left: 1rem; font-size: 0.85rem;">
          </div>
        </div>

        <div class="log-console" id="logConsole">
          <div class="log-entry" data-domain="NET" data-level="INFO"><span style="color:#64748b; margin-right:0.5rem;">--:--:--</span><span class="tag tag-net">NET</span> Hub initialized and listening.</div>
        </div>
      </div>
    </div>
  </main>

  <script nonce="{{NONCE}}">
    // The server tells the page whether the HttpOnly session cookie is already
    // present. This lets an unauthenticated/stale bookmark render a clear
    // recovery state without firing a burst of doomed 401 API requests.
    const pageSessionReady = {{SESSION_READY}};
    async function bootstrapDashboard() {
      const params = new URLSearchParams(location.hash.slice(1));
      const token = params.get('tantu_bootstrap');
      if (!token) return pageSessionReady;
      try {
        const response = await fetch('/api/session', {
          method: 'POST',
          headers: { 'X-Tantu-Dashboard-Bootstrap': token },
          credentials: 'same-origin'
        });
        if (!response.ok) return false;
        history.replaceState(null, '', location.pathname + location.search);
        // Reload once so the server can render a relay bookmarklet only after
        // the HttpOnly session cookie has been established.
        window.location.reload();
        return true;
      } catch (_) {
        return false;
      }
    }
    let dashboardReady = bootstrapDashboard();
    function apiFetch(path, options) {
      options = options || {};
      return dashboardReady.then((ready) => {
        if (!ready) {
          throw new Error('Dashboard session not established. Reopen it from the Hub terminal.');
        }
        const headers = new Headers(options.headers || {});
        const requestOptions = Object.assign({}, options, {
          headers: headers,
          credentials: options.credentials || 'same-origin'
        });
        return fetch(path, requestOptions);
      });
    }

    // CSP-safe event delegation: all user actions are bound from JavaScript
    // rather than inline HTML attributes. Values supplied by peers are read
    // from data attributes or the short-lived receivedActionValues map.
    document.addEventListener('click', function(event) {
      const target = event.target && event.target.closest ? event.target.closest('[data-action], [data-filter], [data-tab]') : null;
      if (!target) return;
      if (target.hasAttribute('data-tab')) {
        switchTab(target.getAttribute('data-tab'), target);
        return;
      }
      if (target.hasAttribute('data-filter')) {
        setLogFilter(target.getAttribute('data-filter'), target);
        return;
      }
      const action = target.getAttribute('data-action');
      switch (action) {
        case 'choose-file':
          document.getElementById('fileInput').click();
          break;
        case 'open-folder':
          openDownloadFolder();
          break;
        case 'change-download-dir':
          promptChangeDownloadDir();
          break;
        case 'cancel-upload':
          cancelUpload();
          break;
        case 'send-text':
          sendTextDrop();
          break;
        case 'load-recent':
          loadRecentDrops();
          break;
        case 'open-pair-modal':
          openPairModal();
          break;
        case 'close-pair-modal':
          closePairModal();
          break;
        case 'copy-pair-command':
          copyPairCmd();
          break;
        case 'pair-connect':
          initiateWebPairing();
          break;
        case 'export-logs':
          exportLogs();
          break;
        case 'clear-logs':
          clearLogs();
          break;
        case 'select-peer':
          selectDropPeer(target.getAttribute('data-value') || '');
          break;
        case 'decide-pairing':
          decidePairing(target.getAttribute('data-value') || '', target.getAttribute('data-accept') === 'true');
          break;
        case 'prefill-pair':
          prefillPairModal(target.getAttribute('data-address') || '', target.getAttribute('data-sas') || '');
          break;
        case 'set-default-peer':
          setDefaultPeer(target.getAttribute('data-value') || '');
          break;
        case 'edit-alias':
          promptEditAlias(target.getAttribute('data-value') || '', target.getAttribute('data-alias') || '');
          break;
        case 'unpair-peer':
          confirmUnpair(target.getAttribute('data-value') || '', target.getAttribute('data-name') || '');
          break;
        case 'copy-snippet':
          copySnippet(receivedActionValues[target.getAttribute('data-value')] || '', target);
          break;
        case 'open-url':
          openURL(receivedActionValues[target.getAttribute('data-value')] || '');
          break;
      }
    });
    document.addEventListener('submit', function(event) {
      const form = event.target;
      if (form && form.getAttribute && form.getAttribute('data-action') === 'relay-oauth') {
        relayOAuth(event);
      }
    });
    document.addEventListener('input', function(event) {
      const input = event.target;
      if (input && input.getAttribute && input.getAttribute('data-action') === 'filter-logs') {
        applyLogFilters();
      }
    });
    document.addEventListener('change', function(event) {
      const select = event.target;
      if (select && select.getAttribute && select.getAttribute('data-action') === 'active-peer') {
        switchActivePeer(select.value);
      }
    });
    document.addEventListener('keydown', function(event) {
      const modal = document.getElementById('pairModal');
      if (event.key === 'Escape' && modal && modal.style.display !== 'none') {
        event.preventDefault();
        closePairModal();
        return;
      }
      const zone = event.target && event.target.closest ? event.target.closest('#dropZone') : null;
      if (zone && (event.key === 'Enter' || event.key === ' ')) {
        event.preventDefault();
        document.getElementById('fileInput').click();
      }
    });
    document.addEventListener('error', function(event) {
      const image = event.target;
      if (image && image.matches && image.matches('img.received-item-preview')) {
        image.remove();
      }
    }, true);

    function switchTab(tabId, sourceButton) {
      document.querySelectorAll('.tab-btn').forEach(b => {
        b.classList.remove('active');
        b.setAttribute('aria-selected', 'false');
      });
      document.querySelectorAll('.tab-pane').forEach(p => p.classList.remove('active'));
      // Click path passes the source button explicitly; programmatic callers
      // (such as paste-to-upload) may omit it and are matched by data-tab.
      const btn = sourceButton || Array.prototype.find.call(document.querySelectorAll('.tab-btn'), b => b.getAttribute('data-tab') === tabId);
      if (btn) {
        btn.classList.add('active');
        btn.setAttribute('aria-selected', 'true');
      }
      const pane = document.getElementById(tabId);
      if (pane) pane.classList.add('active');
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

      row.innerHTML = '<span style="color:#64748b; margin-right:0.5rem;">' + timeStr + '</span><span class="tag ' + tagClass + '">' + escapeHTML(domain) + '</span> ' + escapeHTML(msg) + metaHTML;
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
        const res = await apiFetch('/api/logs');
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
        const res = await apiFetch('/api/logs');
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
    // Short-lived indirection for user-controlled text/URLs keeps large or
    // quote-heavy values out of HTML attributes and inline event handlers.
    let receivedActionValues = Object.create(null);
    let receivedActionSequence = 0;
    function rememberReceivedValue(value) {
      const id = 'received-' + (++receivedActionSequence);
      receivedActionValues[id] = String(value == null ? '' : value);
      return id;
    }
    // Signature of the last rendered peer DOM. updateStatus polls every 3s;
    // rewriting the header <select>, pills, and peer list on every poll
    // steals focus/selection and breaks keyboard interaction, so the peer
    // DOM is only re-rendered when this signature changes (and never while
    // the header select holds focus — the render is deferred to a later poll).
    let lastPeerSignature = '';

    async function updateStatus() {
      try {
        const res = await apiFetch('/api/status');
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

        // Normalize selection first so the render signature below is stable.
        if (peers.length === 0) {
          selectedPeerTarget = '';
        } else if (peers.length === 1) {
          selectedPeerTarget = peers[0].fingerprint;
        } else {
          const activePeer = peers.find(p => p.active) || peers[0];
          if (!selectedPeerTarget || !peers.some(p => p.fingerprint === selectedPeerTarget)) {
            selectedPeerTarget = activePeer.fingerprint;
          }
        }

        const peerSig = JSON.stringify(peers.map(p => [p.fingerprint, p.name, p.alias, p.active, p.is_default, p.address])) + '|' + (selectedPeerTarget || '');
        const headerFocused = document.activeElement && document.activeElement.id === 'headerPeerSelect';
        // Skip re-render while the user interacts with the header select;
        // lastPeerSignature stays stale so the deferred update lands on a
        // later poll after blur.
        if (peerSig !== lastPeerSignature && !headerFocused) {
          lastPeerSignature = peerSig;
        if (peers.length === 0) {
          peerLabel.innerText = 'Loopback Mode (No LAN Peers)';
          peerDot.className = 'status-dot offline';
          peersList.innerHTML = '<p style="color: var(--text-muted); font-size: 0.9rem;">No paired LAN peers yet. Run <code>tantu pair</code> to connect another machine.</p>';
          if (dropSelector) dropSelector.style.display = 'none';
        } else if (peers.length === 1) {
          const p = peers[0];
          peerLabel.innerText = (p.name || 'Peer') + ' [Paired · ready]';
          peerDot.className = 'status-dot';
          if (dropSelector) dropSelector.style.display = 'none';
          renderPeersList(peers);
        } else {
          // Multiple peers
          peerDot.className = 'status-dot';
          const activePeer = peers.find(p => p.active) || peers[0];

          // Header dropdown
          const optionsHTML = peers.map(p => {
            const sel = (p.fingerprint === activePeer.fingerprint) ? 'selected' : '';
            const defBadge = p.is_default ? ' (Default)' : '';
            return '<option value="' + escapeHTML(p.fingerprint) + '" ' + sel + ' style="background:var(--card-bg); color:var(--text);">' + escapeHTML(p.name) + defBadge + '</option>';
          }).join('');
          peerLabel.innerHTML = '<label class="sr-only" for="headerPeerSelect">Active peer</label><select id="headerPeerSelect" data-action="active-peer" aria-label="Active peer" style="background:transparent; color:var(--text); border:none; outline:none; font-size:0.85rem; font-weight:600; cursor:pointer;">' + optionsHTML + '</select>';

          // Tab 1 destination pills
          if (dropSelector && dropPills) {
            dropSelector.style.display = 'flex';
            dropPills.innerHTML = peers.map(p => {
              const isSelected = (p.fingerprint === selectedPeerTarget);
              const pillClass = isSelected ? 'pill active' : 'pill';
              const defTag = p.is_default ? ' ⭐️' : '';
              return '<button type="button" class="' + pillClass + '" data-action="select-peer" data-value="' + escapeHTML(p.fingerprint) + '">' + escapeHTML(p.name) + defTag + '</button>';
            }).join('');
          }

          renderPeersList(peers);
        }
        }
      } catch (err) {
        document.getElementById('peerLabel').innerText = 'Disconnected';
        document.getElementById('peerDot').className = 'status-dot offline';
      }

      // Query discovered LAN peers
      try {
        const discRes = await apiFetch('/api/discovery/peers');
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
        const res = await apiFetch('/api/pair/pending');
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
              '<button class="btn-sm" style="background: #10b981; color: white; border: none; font-weight: 600; padding: 0.4rem 1rem; cursor: pointer; border-radius: 6px;" data-action="decide-pairing" data-value="' + escapeHTML(id) + '" data-accept="true">✓ Approve</button>' +
              '<button class="btn-sm" style="background: #ef4444; color: white; border: none; font-weight: 600; padding: 0.4rem 1rem; cursor: pointer; border-radius: 6px;" data-action="decide-pairing" data-value="' + escapeHTML(id) + '" data-accept="false">✕ Reject</button>' +
            '</div>' +
          '</div>';
        }).join('');
      } catch (err) {
        // Pending check non-critical
      }
    }

    async function decidePairing(id, accept) {
      try {
        const res = await apiFetch('/api/pair/decision', {
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
            '<div style="font-size: 0.75rem; color: var(--text-muted); font-family: monospace;">' + escapeHTML(d.address) + ' · unverified SAS: <span style="color: var(--accent); font-weight: 600;">' + escapeHTML(d.sas) + '</span></div>' +
          '</div>' +
          '<button class="btn btn-primary" style="padding: 0.3rem 0.75rem; font-size: 0.8rem;" data-action="prefill-pair" data-address="' + escapeHTML(d.address) + '" data-sas="' + escapeHTML(d.sas) + '">⚡ Pair</button>' +
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
        const res = await apiFetch('/api/peers/active', {
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
        const defaultBtn = !pr.is_default ? '<button class="btn-sm" data-action="set-default-peer" data-value="' + escapeHTML(pr.fingerprint) + '">⭐️ Set Default</button>' : '';
        const fpShort = pr.fingerprint ? (pr.fingerprint.slice(0, 16) + '...') : '-';

        return '<div style="background: rgba(255,255,255,0.02); border:1px solid var(--border); border-radius:8px; padding:0.85rem 1rem; margin-bottom:0.75rem;">' +
          '<div style="display:flex; justify-content:space-between; align-items:center; flex-wrap:wrap; gap:0.5rem;">' +
            '<div style="display:flex; align-items:center; gap:0.5rem;">' +
              '<span style="font-weight:600; color:var(--text); font-size:1rem;">' + escapeHTML(pr.name || 'Unnamed Peer') + '</span>' +
              defaultBadge + activeBadge +
            '</div>' +
            '<div style="display:flex; gap:0.35rem;">' +
              '<button class="btn-sm" data-action="edit-alias" data-value="' + escapeHTML(pr.fingerprint) + '" data-alias="' + escapeHTML(pr.alias || pr.name || '') + '">✏️ Alias</button>' +
              defaultBtn +
              '<button class="btn-sm" style="color:#f87171; border-color:rgba(239,68,68,0.3);" data-action="unpair-peer" data-value="' + escapeHTML(pr.fingerprint) + '" data-name="' + escapeHTML(pr.name || '') + '">🗑️ Unpair</button>' +
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
        const res = await apiFetch('/api/peers/alias', {
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
        const res = await apiFetch('/api/peers/default', {
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
        const res = await apiFetch('/api/peers/remove', {
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

    let pairModalReturnFocus = null;
    function openPairModal() {
      const modal = document.getElementById('pairModal');
      pairModalReturnFocus = document.activeElement;
      const sas = document.getElementById('idSAS').innerText || '---';
      document.getElementById('pairModalSAS').innerText = sas;
      document.getElementById('pairCmdText').innerText = 'tantu pair';
      document.getElementById('pairStatusMsg').innerText = '';
      modal.style.display = 'flex';
      const input = document.getElementById('pairRemoteAddrInput');
      if (input) input.focus();
    }

    function closePairModal() {
      const modal = document.getElementById('pairModal');
      if (!modal || modal.style.display === 'none') return;
      modal.style.display = 'none';
      if (pairModalReturnFocus && typeof pairModalReturnFocus.focus === 'function') {
        pairModalReturnFocus.focus();
      }
      pairModalReturnFocus = null;
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
        const res = await apiFetch('/api/pair/initiate', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ peer_addr: addr })
        });
        const data = await res.json();
        if (res.ok && data.accepted === true) {
          statusMsg.style.color = '#4ade80';
          statusMsg.innerText = '✅ Successfully paired with ' + (data.peer_name || addr) + ' (SAS: ' + data.peer_sas + ')';
          input.value = '';
          updateStatus();
        } else if (data.status === 'rejected' || data.accepted === false) {
          statusMsg.style.color = '#f87171';
          statusMsg.innerText = '❌ Pairing was rejected by the remote device.';
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

    // Clipboard-first uploads: pasting an image (screenshot, copied file)
    // anywhere on the dashboard sends it directly through the authenticated
    // upload path. Text pastes fall through to the focused field (e.g. the
    // snippet textarea). Only file payloads are logged (name + size); no
    // clipboard content ever enters logs or URLs.
    document.addEventListener('paste', (e) => {
      const files = (e.clipboardData && e.clipboardData.files) || [];
      if (files.length === 0) return;
      const image = Array.prototype.find.call(files, f => f.type && f.type.startsWith('image/')) || files[0];
      if (!image) return;
      e.preventDefault();
      const tab = document.getElementById('tab-drop');
      if (tab && !tab.classList.contains('active')) {
        switchTab('tab-drop');
      }
      // Move keyboard/screen-reader context to the upload target after the
      // automatic tab switch. Focusing never opens the file dialog (click
      // only); the outcome is also announced via the aria-live status region.
      const zone = document.getElementById('dropZone');
      if (zone && typeof zone.focus === 'function') {
        zone.focus({ preventScroll: true });
      }
      uploadFile(image);
    });

    // At most one dashboard file upload at a time. A second upload while one
    // is in flight is rejected with a message (rather than queued or
    // silently orphaned) so progress, completion, and cancellation always
    // refer to exactly one transfer.
    let uploadXhr = null;

    function cancelUpload() {
      if (uploadXhr) {
        uploadXhr.abort();
      }
    }

    async function uploadFile(file) {
      const progress = document.getElementById('uploadProgress');
      const bar = document.getElementById('progressBar');
      const fill = document.getElementById('progressFill');
      const pFile = document.getElementById('progressFile');
      const pPercent = document.getElementById('progressPercent');
      const status = document.getElementById('dropStatus');
      const cancelBtn = document.getElementById('btnCancelUpload');
      const fileInput = document.getElementById('fileInput');

      if (uploadXhr) {
        status.style.display = 'block';
        status.className = 'status-banner error';
        status.textContent = '⚠️ An upload is already in progress — cancel it before starting another.';
        return;
      }
      // apiFetch waits for the bootstrap exchange; XHR must do the same so
      // the session cookie exists before the upload is sent. Avoid opening a
      // doomed request when this page was opened without a Hub session.
      const ready = await dashboardReady;
      if (!ready) {
        status.style.display = 'block';
        status.className = 'status-banner error';
        status.textContent = '⚠️ Dashboard session not established. Reopen it from the Hub terminal.';
        return;
      }

      progress.style.display = 'block';
      cancelBtn.style.display = 'inline-block';
      fill.style.width = '0%';
      bar.setAttribute('aria-valuenow', '0');
      pFile.innerText = file.name + ' (' + formatBytes(file.size) + ')';
      pPercent.innerText = 'Preparing...';
      status.style.display = 'none';

      addLog('DROP', 'Initiating file upload: ' + file.name + ' (' + file.size + ' bytes)');

      const fd = new FormData();
      fd.append('file', file);
      if (selectedPeerTarget) {
        fd.append('peer', selectedPeerTarget);
      }

      const xhr = new XMLHttpRequest();
      uploadXhr = xhr;
      xhr.open('POST', '/api/drop/upload');
      xhr.withCredentials = true;
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) {
          const pct = Math.floor((e.loaded / e.total) * 100);
          fill.style.width = pct + '%';
          bar.setAttribute('aria-valuenow', String(pct));
          pPercent.innerText = pct + '% · ' + formatBytes(e.loaded) + ' / ' + formatBytes(e.total);
        } else {
          pPercent.innerText = formatBytes(e.loaded) + ' sent...';
        }
      };
      const finishUpload = (ok, message) => {
        uploadXhr = null;
        cancelBtn.style.display = 'none';
        if (fileInput) fileInput.value = '';
        status.style.display = 'block';
        if (ok) {
          fill.style.width = '100%';
          bar.setAttribute('aria-valuenow', '100');
          pPercent.innerText = '100% · ' + formatBytes(file.size) + ' / ' + formatBytes(file.size);
          status.className = 'status-banner success';
          status.textContent = '✅ File transferred successfully to peer!';
          addLog('DROP', 'Completed transfer of ' + file.name);
          setTimeout(() => { progress.style.display = 'none'; fill.style.width = '0%'; }, 2500);
        } else {
          status.className = 'status-banner error';
          status.textContent = message;
          // Keep the progress bar visible on failure so the final state is
          // inspectable; the next upload resets it.
        }
      };
      xhr.onload = () => {
        let message = 'Unknown error';
        try {
          const data = JSON.parse(xhr.responseText);
          if (xhr.status >= 200 && xhr.status < 300 && data.status === 'success') {
            finishUpload(true);
            return;
          }
          message = data.message || ('HTTP ' + xhr.status);
        } catch (_) {
          message = 'HTTP ' + xhr.status;
        }
        if (xhr.status === 503) {
          message += ' (server busy — wait a moment and retry)';
        }
        addLog('ERROR', 'Drop failed: ' + message);
        finishUpload(false, '❌ Transfer failed: ' + message);
      };
      xhr.onerror = () => {
        addLog('ERROR', 'Upload error: connection failed');
        finishUpload(false, '❌ Connection error during upload.');
      };
      xhr.onabort = () => {
        addLog('DROP', 'Upload cancelled: ' + file.name);
        finishUpload(false, '⚠️ Upload cancelled.');
      };
      xhr.send(fd);
    }

    async function sendTextDrop() {
      const textarea = document.getElementById('textPayload');
      const text = textarea.value.trim();
      if (!text) return;

      // Fail fast client-side with the same 10 MiB byte limit the server
      // enforces, instead of uploading megabytes just to be rejected.
      // TextEncoder measures UTF-8 bytes (what the server counts), not
      // UTF-16 code units. Only lengths are logged, never content.
      const textBytes = new TextEncoder().encode(text).length;
      // Client-side mirror of the server text limit (drop.DefaultMaxTextSize =
      // 10 MiB, enforced again server-side): fail fast instead of uploading
      // megabytes the server will reject.
      const maxTextBytes = 10 * 1024 * 1024;
      if (textBytes > maxTextBytes) {
        addLog('ERROR', 'Text snippet too large (' + formatBytes(textBytes) + ' exceeds the 10.0 MB limit). Send it as a file instead.');
        return;
      }

      const btn = document.getElementById('btnSendText');
      btn.disabled = true;
      addLog('DROP', 'Sending text snippet (' + text.length + ' chars, ' + formatBytes(textBytes) + ')...');

      const payload = { text: text };
      if (selectedPeerTarget) {
        payload.peer = selectedPeerTarget;
      }

      try {
        const res = await apiFetch('/api/drop/upload', {
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
      status.textContent = '⏳ Relaying OAuth callback through bridge...';
      addLog('OAUTH', 'Forwarding URL: ' + redactForLog(url));

      const payload = { url: url };
      if (selectedPeerTarget) {
        payload.peer = selectedPeerTarget;
      }

      try {
        const res = await apiFetch('/api/relay/open', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });
        const data = await res.json();
        if (res.ok && data.status === 'success') {
          status.className = 'status-banner success';
          status.textContent = '✅ Authentication completed successfully!';
          input.value = '';
          addLog('OAUTH', 'OAuth flow completed successfully.');
        } else {
          status.className = 'status-banner error';
          status.textContent = '❌ Relay failed: ' + (data.message || 'Unknown error');
          addLog('ERROR', 'Relay failed: ' + (data.message || 'Unknown error'));
        }
      } catch (err) {
        status.className = 'status-banner error';
        status.textContent = '❌ Connection error: ' + err.message;
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
      try {
        const parsed = new URL(url);
        if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return;
        window.open(parsed.href, '_blank', 'noopener,noreferrer');
      } catch (_) {}
    }

    async function openDownloadFolder() {
      try {
        const res = await apiFetch('/api/open-folder', { method: 'POST' });
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
        const res = await apiFetch('/api/config', {
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
        const res = await apiFetch('/api/config');
        if (res.ok) {
          const cfg = await res.json();
          if (cfg.output_dir) {
            document.getElementById('downloadDirPath').innerText = cfg.output_dir;
          }
        }
      } catch (_) {}
    }

    let recentDropsReloadInFlight = false;
    let recentDropsReloadQueued = false;
    async function scheduleRecentDropsReload() {
      if (recentDropsReloadInFlight) {
        recentDropsReloadQueued = true;
        return;
      }
      recentDropsReloadInFlight = true;
      try {
        await loadRecentDrops();
      } finally {
        recentDropsReloadInFlight = false;
        if (recentDropsReloadQueued) {
          recentDropsReloadQueued = false;
          void scheduleRecentDropsReload();
        }
      }
    }

    async function loadRecentDrops() {
      try {
        const res = await apiFetch('/api/drop/recent');
        if (!res.ok) return;
        const items = await res.json();
        renderRecentDrops(items);
      } catch (_) {}
    }

    function renderRecentDrops(items) {
      receivedActionValues = Object.create(null);
      const list = document.getElementById('receivedDropsList');
      if (!items || items.length === 0) {
        list.innerHTML = '<p style="color: var(--text-muted); font-size: 0.85rem; text-align: center; padding: 2rem 0;">No received items yet.<br>Snippets and files sent by your peer appear here in real-time.</p>';
        return;
      }
      const reversed = items.slice().reverse();
      list.innerHTML = reversed.map(function(item) {
        const copyID = rememberReceivedValue(item.content || '');
        const urlID = rememberReceivedValue(item.content || '');
        const timeStr = item.timestamp ? new Date(item.timestamp).toLocaleTimeString() : '';
        const isFile = item.kind === 'file';
        const isURL = item.is_url;
        const icon = isFile ? '📁' : (isURL ? '🌐' : '📝');
        const title = isFile ? (item.name || 'Received File') : (isURL ? 'Web URL' : 'Text Snippet');
        const safeTitle = escapeHTML(title);
        const sizeStr = formatBytes(item.size);

        let contentHTML = '';
        if (isFile) {
          const fileURL = item.id ? '/api/drop/file?id=' + encodeURIComponent(item.id) : '';
          const extMatch = (item.name || '').toLowerCase().match(/\.[a-z0-9]+$/);
          const imgExts = ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.bmp', '.avif', '.ico'];
          const previewable = fileURL && extMatch && imgExts.indexOf(extMatch[0]) !== -1 && item.size > 0 && item.size <= 8 * 1024 * 1024;
          if (previewable) {
            contentHTML = '<img class="received-item-preview" src="' + fileURL + '&mode=inline" alt="' + escapeHTML('Preview of ' + (item.name || 'received image')) + '" loading="lazy">' +
              '<div class="received-item-content">Path: ' + escapeHTML(item.saved_path || item.name) + ' (' + sizeStr + ')</div>';
          } else {
            contentHTML = '<div class="received-item-content">Path: ' + escapeHTML(item.saved_path || item.name) + ' (' + sizeStr + ')</div>';
          }
        } else {
          contentHTML = '<div class="received-item-content">' + escapeHTML(item.content || '') + '</div>';
        }

        let actionsHTML = '<div class="received-item-actions">';
        if (!isFile && item.content) {
          actionsHTML += '<button class="btn-sm" data-action="copy-snippet" data-value="' + escapeHTML(copyID) + '">📋 Copy</button>';
          if (isURL || item.content.startsWith('http://') || item.content.startsWith('https://')) {
            const cleanURL = item.content.trim();
            actionsHTML += '<button class="btn-sm" data-action="open-url" data-value="' + escapeHTML(urlID) + '">🌐 Open in Browser</button>';
          }
        } else if (isFile && item.saved_path) {
          if (item.id) {
            actionsHTML += '<a class="btn-sm" href="/api/drop/file?id=' + encodeURIComponent(item.id) + '&mode=download">⬇️ Download</a>';
          }
          actionsHTML += '<button class="btn-sm" data-action="open-folder">📂 Open in Folder</button>';
        }
        actionsHTML += '</div>';

        return '<div class="received-item">' +
          '<div class="received-item-header">' +
            '<span><strong>' + icon + ' ' + safeTitle + '</strong> from ' + escapeHTML(item.from_peer || 'Peer') + '</span>' +
            '<span>' + timeStr + ' &bull; ' + sizeStr + '</span>' +
          '</div>' +
          contentHTML +
          actionsHTML +
        '</div>';
      }).join('');
    }

    function formatBytes(bytes) {
      if (!bytes || bytes <= 0) return '0 B';
      const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
      let value = bytes;
      let unit = 0;
      while (value >= 1024 && unit < units.length - 1) {
        value /= 1024;
        unit++;
      }
      return (unit === 0 ? String(Math.floor(value)) : value.toFixed(1)) + ' ' + units[unit];
    }

    function redactForLog(rawURL) {
      try {
        const parsed = new URL(rawURL);
        parsed.username = '';
        parsed.password = '';
        parsed.hash = '';
        if (parsed.search) parsed.search = '?redacted';
        return parsed.toString();
      } catch (_) {
        return '<invalid-url>';
      }
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

    dashboardReady.then((ready) => {
      if (!ready) {
        document.getElementById('sessionBanner').style.display = 'block';
        return;
      }
      // SSE + polling start here; HttpOnly session cookies ride automatically.
      if (window.EventSource) {
        const sse = new EventSource('/api/events', { withCredentials: true });
        sse.onmessage = (e) => {
          try {
            const item = JSON.parse(e.data);
            const domain = item.domain || item.tag || 'SYS';
            addLog(domain, item.message || JSON.stringify(item), item.level, item.metadata);
            if (domain === 'DROP') {
              scheduleRecentDropsReload();
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
    });
  </script>
</body>
</html>`

type relayResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func newDashboardNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// javascriptCSPHash authorizes the dashboard's user-activated bookmarklet
// without reopening script-src to unsafe-inline. CSP hashes the javascript:
// navigation's script body; the URL itself is generated per response because
// it carries a one-use relay ticket.
func javascriptCSPHashes(raw string) string {
	source := strings.TrimPrefix(raw, "javascript:")
	sumSource := sha256.Sum256([]byte(source))
	sumFull := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("'sha256-%s' 'sha256-%s'", base64.StdEncoding.EncodeToString(sumSource[:]), base64.StdEncoding.EncodeToString(sumFull[:]))
}

func escapeHTMLText(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	).Replace(s)
}

func isAllowedOrigin(originHeader string) bool {
	return isAllowedOriginForHost(originHeader, "")
}

func isAllowedOriginForHost(originHeader, requestHost string) bool {
	if originHeader == "" || strings.EqualFold(originHeader, "null") {
		return originHeader == ""
	}
	u, err := url.Parse(originHeader)
	if err != nil || u.User != nil {
		return false
	}
	if u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return false
	}
	if requestHost == "" {
		return true
	}
	requestName, requestPort, splitErr := net.SplitHostPort(requestHost)
	if splitErr != nil {
		requestName = strings.Trim(requestHost, "[]")
		requestPort = ""
	}
	if requestName != "" {
		requestName = strings.ToLower(requestName)
		if requestName != "127.0.0.1" && requestName != "localhost" && requestName != "::1" {
			return false
		}
	}
	originPort := u.Port()
	if originPort == "" {
		if u.Scheme == "https" {
			originPort = "443"
		} else {
			originPort = "80"
		}
	}
	if requestPort == "" {
		return originPort == "80" || originPort == "443"
	}
	return originPort == requestPort
}

func requestHostIsLoopbackHeader(hostHeader string) bool {
	host := strings.TrimSpace(hostHeader)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else {
		host = strings.Trim(host, "[]")
	}
	host = strings.ToLower(host)
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func canonicalLoopbackHost(host string) string {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if host == "localhost" {
		return "127.0.0.1"
	}
	return host
}

func splitAuthority(authority string) (host, port string, ok bool) {
	authority = strings.TrimSpace(authority)
	if authority == "" {
		return "", "", false
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return "", "", false
	}
	host = canonicalLoopbackHost(host)
	if host != "127.0.0.1" && host != "::1" {
		return "", "", false
	}
	if port == "" {
		return "", "", false
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", "", false
	}
	return host, port, true
}

func sameLoopbackAuthority(actual, candidate string) bool {
	actualHost, actualPort, actualOK := splitAuthority(actual)
	candidateHost, candidatePort, candidateOK := splitAuthority(candidate)
	return actualOK && candidateOK && actualHost == candidateHost && actualPort == candidatePort
}

func isAllowedOriginForActual(originHeader, actualAuthority string) bool {
	if originHeader == "" || strings.EqualFold(originHeader, "null") {
		return false
	}
	u, err := url.Parse(originHeader)
	if err != nil || u.User != nil || u.Scheme != "http" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return sameLoopbackAuthority(actualAuthority, u.Host)
}

func (h *Hub) requestHasDashboardCapability(r *http.Request) bool {
	if r == nil {
		return false
	}
	ipcToken := h.currentIPCToken()
	if ipcToken != "" {
		provided := r.Header.Get(IPCTokenHeader)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(ipcToken)) == 1 {
			return true
		}
	}
	if cookie, err := r.Cookie("tantu_dashboard_session"); err == nil && h.dashboardSessionValid(cookie.Value) {
		return true
	}
	return false
}

func (h *Hub) consumeDashboardBootstrap(r *http.Request) bool {
	if r == nil {
		return false
	}
	provided := strings.TrimSpace(r.Header.Get("X-Tantu-Dashboard-Bootstrap"))
	if provided == "" {
		return false
	}
	h.dashboardMu.Lock()
	expected := h.dashboardBootstrapToken
	if expected == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		h.dashboardMu.Unlock()
		return false
	}
	// A bootstrap value is one-use. It is removed before allocating a session,
	// so concurrent requests cannot exchange it twice.
	h.dashboardBootstrapToken = ""
	h.dashboardMu.Unlock()
	return true
}

func (h *Hub) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")

		// Validate the complete authority, including its port. Checking only
		// the hostname lets a raw client forge a different local authority and
		// makes Origin validation depend on an attacker-controlled Host value.
		actualWeb := h.WebAddr()
		if actualWeb == "" || !sameLoopbackAuthority(actualWeb, r.Host) {
			http.Error(w, "local-only request rejected", http.StatusForbidden)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/") {
			origin := r.Header.Get("Origin")
			if origin != "" && !isAllowedOriginForActual(origin, actualWeb) {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"status":  "error",
					"message": "forbidden cross-origin request",
				})
				return
			}
			referer := r.Header.Get("Referer")
			if referer != "" {
				refererURL, err := url.Parse(referer)
				if err != nil || !isAllowedOriginForActual(refererURL.Scheme+"://"+refererURL.Host, actualWeb) {
					writeJSON(w, http.StatusForbidden, map[string]string{
						"status":  "error",
						"message": "forbidden cross-origin request",
					})
					return
				}
			}

			// The bootstrap exchange is the only unauthenticated API operation.
			// It consumes a one-time value delivered in a browser URL fragment
			// and returns an HttpOnly, same-site session cookie.
			if r.URL.Path == "/api/session" {
				if r.Method != http.MethodPost {
					writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
					return
				}
				if !h.consumeDashboardBootstrap(r) {
					writeJSON(w, http.StatusForbidden, map[string]string{"status": "error", "message": "dashboard bootstrap capability required"})
					return
				}
				sessionID, err := h.newDashboardSession()
				if err != nil {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "message": "dashboard session unavailable"})
					return
				}
				http.SetCookie(w, &http.Cookie{
					Name:     "tantu_dashboard_session",
					Value:    sessionID,
					Path:     "/",
					MaxAge:   1800,
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
				writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
				return
			}

			if r.Method != http.MethodOptions && !h.requestHasDashboardCapability(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"status":  "error",
					"message": "dashboard session or local IPC capability required",
				})
				return
			}
			// CLI/Hub version skew is otherwise invisible: delegation sends
			// X-CLI-Version on every IPC call but nothing reads it. Log the
			// mismatch (deduped per version per Hub run) so mixed-version
			// API drift shows up in diagnostics instead of surfacing as
			// confusing downstream errors. Mixed versions remain best-effort.
			if cliVer := strings.TrimSpace(r.Header.Get("X-CLI-Version")); cliVer != "" && CanonicalVersion(cliVer) != CanonicalVersion(HubVersion) && h.logger != nil {
				h.dashboardMu.Lock()
				alreadyWarned := h.skewWarnedFor == cliVer
				if !alreadyWarned {
					h.skewWarnedFor = cliVer
				}
				h.dashboardMu.Unlock()
				if !alreadyWarned {
					h.logger.Warn(DomainSys, fmt.Sprintf("CLI/Hub version skew: caller %q vs hub %q on %s %s", cliVer, HubVersion, r.Method, r.URL.Path))
				}
			}
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token, X-Tantu-Dashboard-Bootstrap")
				w.Header().Add("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func publicRelayError(err error) string {
	if err == nil {
		return "relay flow failed"
	}
	return "relay flow failed"
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

const maxControlBodySize = 64 * 1024

func boundControlBody(w http.ResponseWriter, r *http.Request, maxBytes int64) func() {
	if r == nil || r.Body == nil {
		return func() {}
	}
	if maxBytes <= 0 {
		maxBytes = maxControlBodySize
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	// ReadHeaderTimeout does not cover a slow body. Control requests are
	// small, so give them a short absolute read deadline while leaving the
	// long-lived upload endpoint's transfer budget intact. Clear it before
	// returning so a keep-alive connection is not poisoned for its next
	// request.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(15 * time.Second))
	return func() { _ = controller.SetReadDeadline(time.Time{}) }
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
	PID             int            `json:"pid,omitempty"`
	StartedAt       time.Time      `json:"started_at,omitempty"`
}

type probeStatusResponse struct {
	Status    string    `json:"status"`
	Version   string    `json:"version"`
	Transport string    `json:"transport"`
	WebAddr   string    `json:"web_addr"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
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
		nonce, nonceErr := newDashboardNonce()
		if nonceErr != nil {
			http.Error(w, "dashboard security nonce unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		webAddr := h.WebAddr()
		if webAddr == "" {
			h.mu.RLock()
			webAddr = h.cfg.WebAddr
			h.mu.RUnlock()
		}
		bookmarkletJS := "javascript:void(0)"
		if h.requestHasDashboardCapability(r) {
			if relayTicket := h.newRelayTicket(); relayTicket != "" {
				bookmarkletJS = fmt.Sprintf("javascript:void(window.open('http://%s/relay?url='+encodeURIComponent(location.href)+'&ticket=%s','_blank','width=550,height=380'))", webAddr, url.QueryEscape(relayTicket))
			}
		}
		// `unsafe-hashes` is limited to the exact generated bookmarklet
		// navigation; ordinary inline scripts still require the per-response
		// nonce, and all DOM actions are delegated from that script.
		w.Header().Set("Content-Security-Policy", fmt.Sprintf("default-src 'self'; script-src 'self' 'nonce-%s' 'unsafe-hashes' %s; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'", nonce, javascriptCSPHashes(bookmarkletJS)))

		page := strings.ReplaceAll(dashboardHTML, "{{VERSION}}", escapeHTMLText(HubVersion))
		page = strings.ReplaceAll(page, "{{BOOKMARKLET_HREF}}", escapeHTMLText(bookmarkletJS))
		// Do not make an unauthenticated page probe every sensitive endpoint
		// just to discover that it needs the one-time bootstrap link. The
		// HttpOnly cookie is intentionally invisible to JavaScript, so the
		// server-side capability check is the authoritative session hint.
		page = strings.ReplaceAll(page, "{{SESSION_READY}}", strconv.FormatBool(h.requestHasDashboardCapability(r)))
		page = strings.ReplaceAll(page, "{{NONCE}}", nonce)
		_, _ = w.Write([]byte(page))
	})

	// 2. GET /healthz
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// 2a. POST /api/session — exchange a one-time browser bootstrap value for
	// an HttpOnly dashboard session. The handler is completed by the security
	// middleware above so the bootstrap value is never accepted by another API.
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		// Kept as an explicit route for documentation and for direct handler
		// tests; the middleware performs the one-time validation and response.
		http.NotFound(w, r)
	})

	// 2b. GET /api/probe — minimal authenticated runtime identity response.
	// Unlike /api/status it contains no identity SAS, peer list, logs, or
	// received content.
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		h.mu.RLock()
		webAddr := h.actualWeb
		transportName := h.cfg.TransportType
		pid := os.Getpid()
		startedAt := h.startTime
		h.mu.RUnlock()
		writeJSON(w, http.StatusOK, probeStatusResponse{
			Status:    "online",
			Version:   HubVersion,
			Transport: transportName,
			WebAddr:   webAddr,
			PID:       pid,
			StartedAt: startedAt,
		})
	})

	// 3. GET /api/status
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		h.mu.RLock()
		store := h.store
		transportName := h.cfg.TransportType
		webAddrSnapshot := h.actualWeb
		p2pSnapshot := h.actualP2P
		identitySnapshot := h.identity
		startedAtSnapshot := h.startTime
		engine := h.discoveryEngine
		h.mu.RUnlock()
		activeFP := h.GetActivePeer()

		var peers []peerStatus
		if store != nil {
			allPeers := store.ListPeers()
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
		if engine != nil {
			discCount = len(engine.ListNodes())
		}

		resp := statusResponse{
			Status:          "online",
			Version:         HubVersion,
			Transport:       transportName,
			WebAddr:         webAddrSnapshot,
			ActivePeer:      activeFP,
			DiscoveredCount: discCount,
			DiscoveredPeers: discCount,
			Peers:           peers,
			PID:             os.Getpid(),
			StartedAt:       startedAtSnapshot,
		}
		if p2pSnapshot != nil {
			resp.P2PAddr = p2pSnapshot.String()
		}
		if identitySnapshot != nil {
			resp.Identity = identityStatus{
				Fingerprint: identitySnapshot.Fingerprint,
				SAS:         pairing.SASCode(identitySnapshot.Fingerprint),
			}
		}

		writeJSON(w, http.StatusOK, resp)
	})

	// 4. GET /relay — Legacy bookmarklet handler
	mux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
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
					fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>✅ Auth Relayed</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;}.box{background:#131722;border:1px solid #232a3b;border-radius:12px;padding:2rem;max-width:450px;text-align:center;box-shadow:0 4px 20px rgba(0,0,0,0.5);}.icon{font-size:2.5rem;margin-bottom:0.75rem;}h2{color:#34d399;margin:0 0 0.5rem 0;}p{color:#8b949e;font-size:0.9rem;margin:0 0 1rem 0;}.hint{color:#64748b;font-size:0.8rem;}</style></head><body><div class="box"><div class="icon">✅</div><h2>Authentication Relayed!</h2><p>%s</p><div class="hint">This window will close automatically in 3 seconds...</div></div><script>setTimeout(function(){window.close();},3000);</script></body></html>`, escapeHTMLText(resp.Message))
				} else {
					fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>❌ Relay Error</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;}.box{background:#131722;border:1px solid #232a3b;border-radius:12px;padding:2rem;max-width:450px;text-align:center;box-shadow:0 4px 20px rgba(0,0,0,0.5);}.icon{font-size:2.5rem;margin-bottom:0.75rem;}h2{color:#f87171;margin:0 0 0.5rem 0;}p{color:#8b949e;font-size:0.9rem;margin:0 0 1rem 0;}.hint{color:#64748b;font-size:0.8rem;}</style></head><body><div class="box"><div class="icon">❌</div><h2>Relay Error</h2><p>%s</p><div class="hint">Check terminal logs or verify your peer is connected.</div></div></body></html>`, escapeHTMLText(resp.Message))
				}
				return
			}
			writeJSON(w, status, resp)
		}

		rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
		if rawURL == "" {
			// Keep the legacy endpoint's harmless validation response readable
			// for old callers; no relay work is performed without a URL.
			respond(http.StatusBadRequest, relayResponse{
				Status:  "error",
				Message: "missing or empty url parameter",
			})
			return
		}
		// The legacy GET endpoint deliberately does not accept the browser
		// session cookie: a cross-site top-level navigation must not be able to
		// turn that cookie into a relay capability. Authenticated dashboard
		// mutations use POST /api/relay/open; the bookmarklet uses a one-use
		// ticket.
		ipcToken := h.currentIPCToken()
		relayAuthorized := ipcToken != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get(IPCTokenHeader)), []byte(ipcToken)) == 1
		if !relayAuthorized {
			relayAuthorized = h.consumeRelayTicket(r.URL.Query().Get("ticket"))
		}
		if !relayAuthorized {
			providedRelayToken := r.URL.Query().Get("token")
			relayToken := h.currentRelayToken()
			relayAuthorized = relayToken != "" && subtle.ConstantTimeCompare([]byte(providedRelayToken), []byte(relayToken)) == 1
		}
		if !relayAuthorized {
			http.Error(w, "relay token or dashboard session required", http.StatusForbidden)
			return
		}

		if err := h.relayOAuth(r.Context(), "", rawURL); err != nil {
			respond(http.StatusBadGateway, relayResponse{
				Status:  "error",
				Message: publicRelayError(err),
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
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
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
		releaseBody := boundControlBody(w, r, 128*1024)
		defer releaseBody()
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

		if err := h.relayOAuth(r.Context(), req.Peer, rawURL); err != nil {
			writeJSON(w, http.StatusBadGateway, relayResponse{
				Status:  "error",
				Message: publicRelayError(err),
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
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
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
			if !acquireSlot(h.textSlots) {
				w.Header().Set("Retry-After", "10")
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "message": "too many concurrent uploads, retry later"})
				return
			}
			defer releaseSlot(h.textSlots)
			var textReq struct {
				Text string `json:"text"`
				Name string `json:"name"`
				Peer string `json:"peer"`
			}
			r.Body = http.MaxBytesReader(w, r.Body, 11*1024*1024)
			if err := json.NewDecoder(r.Body).Decode(&textReq); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json payload"})
				return
			}
			if textReq.Text == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "empty text payload"})
				return
			}
			if int64(len(textReq.Text)) > drop.DefaultMaxTextSize {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"status": "error", "message": "text payload exceeds 10 MiB limit"})
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

		// Multipart file drop (up to 5GB). Slot acquisition comes before any
		// body buffering so rejected uploads cost nothing.
		if !h.acquireUploadSlot() {
			w.Header().Set("Retry-After", "30")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "message": "too many concurrent uploads, retry later"})
			return
		}
		defer h.releaseUploadSlot()
		r.Body = http.MaxBytesReader(w, r.Body, drop.DefaultMaxDropSize)
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
		if seeker, ok := file.(io.ReadSeeker); ok && header.Size >= 64*1024 {
			_, _ = seeker.Seek(0, io.SeekStart)
			head := make([]byte, 64*1024)
			if n, readErr := io.ReadFull(seeker, head); n > 0 && (readErr == nil || readErr == io.EOF || readErr == io.ErrUnexpectedEOF) {
				sum := sha256.Sum256(head[:n])
				meta.HeadHash = hex.EncodeToString(sum[:])
			}
			_, _ = seeker.Seek(0, io.SeekStart)
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

	// 9b. GET /api/drop/file — Serve one received file for inline preview
	// or download. The file is addressed by drop ID (never by path): the
	// recorded SavedPath must resolve inside the current output directory,
	// be a regular file (never a symlink), and inline rendering is limited
	// to small raster images with a fixed content-type map. SVG, HTML, and
	// everything else are forced to attachment so a malicious peer can never
	// plant active content in the dashboard origin. Capability/session auth
	// is enforced by securityMiddleware like every other /api/* route.
	mux.HandleFunc("/api/drop/file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" || len(id) > drop.MaxDropIDLength {
			http.NotFound(w, r)
			return
		}
		if h.recentDrops == nil {
			http.NotFound(w, r)
			return
		}
		item, ok := h.recentDrops.Find(id)
		if !ok || item.Kind != string(drop.DropKindFile) || item.SavedPath == "" {
			http.NotFound(w, r)
			return
		}
		serveReceivedFile(w, r, h.OutputDir(), item)
	})

	// 9c. POST /api/dashboard-url — mint a fresh authenticated dashboard link
	// for local CLI/headless workflows. The response is capability-protected by
	// securityMiddleware and is never cached or logged; the fragment token is
	// one-use and therefore cannot be replayed from shell history.
	mux.HandleFunc("/api/dashboard-url", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed, use POST"})
			return
		}
		dashboardURL := h.NewDashboardURL()
		if dashboardURL == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "message": "dashboard link unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ok",
			"url":    dashboardURL,
		})
	})

	// 10. GET /api/config & POST /api/config — Retrieve or update Hub config
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
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
			releaseBody := boundControlBody(w, r, maxControlBodySize)
			defer releaseBody()
			var req struct {
				OutputDir string `json:"output_dir"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json body"})
				return
			}
			req.OutputDir = strings.TrimSpace(req.OutputDir)
			if err := validateOutputDir(req.OutputDir); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": fmt.Sprintf("invalid output directory: %v", err)})
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
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
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
		if err := os.MkdirAll(dir, 0700); err != nil {
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
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
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
		releaseBody := boundControlBody(w, r, 8*1024)
		defer releaseBody()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.PeerAddr) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid request: peer_addr is required"})
			return
		}
		peerAddr := strings.TrimSpace(req.PeerAddr)
		h.logger.Action(DomainPeer, fmt.Sprintf("Initiating in-band pairing with %s", peerAddr))
		if !acquireSlot(h.pairSlots) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "message": "too many concurrent pairing attempts, retry later"})
			return
		}
		defer releaseSlot(h.pairSlots)
		pairCtx, pairCancel := h.withRunContext(r.Context())
		defer pairCancel()
		pairingOptions := pairing.PairingOptions{}
		if actual := h.P2PAddr(); actual != nil {
			_, portText, splitErr := net.SplitHostPort(actual.String())
			if splitErr == nil {
				if port, convErr := strconv.Atoi(portText); convErr == nil {
					pairingOptions.LocalListenPort = port
				}
			}
		}
		res, err := pairing.DialInBandPairingWithOptions(pairCtx, peerAddr, h.store, func(peerSAS, localSAS string) bool {
			if h.cfg.AutoAcceptPairing {
				h.logger.Action(DomainPeer, fmt.Sprintf("Auto-accepted pairing SAS match with %s: %s vs %s", peerAddr, peerSAS, localSAS))
				return true
			}

			pairID := newPendingPairingID()
			decisionCh := make(chan struct{})
			pending := &PendingPairing{
				ID:         pairID,
				RemoteAddr: peerAddr,
				PeerSAS:    peerSAS,
				LocalSAS:   localSAS,
				PeerName:   "Remote Device",
				CreatedAt:  time.Now(),
				decisionCh: decisionCh,
			}
			h.pendingPairingsMu.Lock()
			if len(h.pendingPairings) >= 10 {
				h.pendingPairingsMu.Unlock()
				h.logger.Warn(DomainPeer, "Pairing request rejected: too many pending requests")
				return false
			}
			h.pendingPairings[pairID] = pending
			h.pendingPairingsMu.Unlock()
			defer func() {
				h.pendingPairingsMu.Lock()
				delete(h.pendingPairings, pairID)
				h.pendingPairingsMu.Unlock()
			}()

			h.logger.Action(DomainPeer, fmt.Sprintf("🔐 Web pairing approval required: %s (SAS: %s, ID: %s)", peerAddr, peerSAS, pairID))
			select {
			case <-decisionCh:
				h.pendingPairingsMu.Lock()
				accepted := pending.decision
				h.pendingPairingsMu.Unlock()
				return accepted
			case <-pairCtx.Done():
				return h.terminatePendingPairing(pending, false)
			case <-time.After(60 * time.Second):
				h.logger.Warn(DomainPeer, fmt.Sprintf("Pairing request from %s timed out", peerAddr))
				return h.terminatePendingPairing(pending, false)
			}
		}, pairingOptions)
		if err != nil {
			h.logger.Error(DomainPeer, fmt.Sprintf("Pairing with %s failed: %v", peerAddr, err))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "error": err.Error()})
			return
		}
		if res == nil || !res.Accepted {
			peerName := ""
			peerSAS := ""
			peerFP := ""
			if res != nil {
				peerSAS = res.PeerSAS
				peerFP = res.PeerFingerprint
				if h.store != nil {
					if p, ok := h.store.GetPeer(peerFP); ok {
						peerName = p.DisplayName()
					}
				}
			}
			writeJSON(w, http.StatusConflict, map[string]any{
				"status":           "rejected",
				"accepted":         false,
				"peer_addr":        peerAddr,
				"peer_name":        peerName,
				"peer_sas":         peerSAS,
				"peer_fingerprint": peerFP,
			})
			return
		}
		h.logger.Action(DomainPeer, fmt.Sprintf("✅ Successfully paired with %s (SAS: %s)", peerAddr, res.PeerSAS))
		peerName := ""
		if h.store != nil {
			if p, ok := h.store.GetPeer(res.PeerFingerprint); ok {
				peerName = p.DisplayName()
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":           "paired",
			"accepted":         true,
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
		releaseBody := boundControlBody(w, r, 8*1024)
		defer releaseBody()
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
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		releaseBody := boundControlBody(w, r, maxControlBodySize)
		defer releaseBody()
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
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		releaseBody := boundControlBody(w, r, maxControlBodySize)
		defer releaseBody()
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
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		releaseBody := boundControlBody(w, r, maxControlBodySize)
		defer releaseBody()
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
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		releaseBody := boundControlBody(w, r, maxControlBodySize)
		defer releaseBody()
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
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

// maxInlinePreviewBytes caps inline dashboard previews. Larger images are
// served as downloads only, so opening the inbox can never pull gigabytes
// into the browser process.
const maxInlinePreviewBytes = 8 * 1024 * 1024

// inlinePreviewTypes maps file extensions eligible for inline preview to
// their fixed content types. SVG/HTML and everything unlisted are deliberately
// absent: only inert raster formats may render in the dashboard origin.
var inlinePreviewTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".avif": "image/avif",
	".ico":  "image/x-icon",
}

// serveReceivedFile serves one received file addressed by drop ID. The
// recorded SavedPath must resolve inside outputDir and be a regular file;
// symlinks, directories, and escapes fail closed with 404 (no existence
// oracle beyond what the recent-items feed already discloses).
func serveReceivedFile(w http.ResponseWriter, r *http.Request, outputDir string, item ReceivedDropItem) {
	abs, err := filepath.Abs(item.SavedPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	outAbs, err := filepath.Abs(outputDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Resolve symlinks on both sides before the containment check: a
	// purely lexical Rel check would pass a symlinked directory component
	// (or a symlinked OutputDir itself) that escapes at open time.
	absEval, err := filepath.EvalSymlinks(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	outEval, err := filepath.EvalSymlinks(outAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rel, err := filepath.Rel(outEval, absEval)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		http.NotFound(w, r)
		return
	}
	inspected, err := os.Lstat(absEval)
	if err != nil || !inspected.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(absEval)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	// The descriptor is bound at open; verify it is still the inspected
	// regular file so a leaf swap between Lstat and Open fails closed
	// instead of serving a victim file. (Directory-component swaps inside
	// the residual Lstat→Open window remain a same-user local race.)
	finfo, err := f.Stat()
	if err != nil || !finfo.Mode().IsRegular() || !os.SameFile(inspected, finfo) {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("mode") == "inline" {
		if contentType, ok := inlinePreviewTypes[strings.ToLower(filepath.Ext(item.Name))]; ok && finfo.Size() > 0 && finfo.Size() <= maxInlinePreviewBytes {
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Content-Disposition", "inline")
			// Defense in depth for direct navigation: a mislabeled file
			// opened as a top-level document gets no script execution.
			// (For <img> embeds the protection comes from the image
			// decoder plus nosniff; sandbox does not constrain
			// subresources.)
			w.Header().Set("Content-Security-Policy", "sandbox")
			http.ServeContent(w, r, item.Name, finfo.ModTime(), f)
			return
		}
	}
	safeName := SanitizeDropFilename(item.Name)
	if safeName == "" {
		safeName = "download"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(safeName, `"`, "_")+`"`)
	http.ServeContent(w, r, item.Name, finfo.ModTime(), f)
}

func openDirectoryInOS(dir string) error {
	cleanDir := filepath.Clean(dir)
	// Resolve to an absolute path: a relative OutputDir such as "-n" would
	// otherwise be parsed as a helper flag by open/xdg-open. Absolute paths
	// can never begin with '-'. A resolution failure fails closed rather
	// than executing the helper with the raw relative value.
	absDir, err := filepath.Abs(cleanDir)
	if err != nil {
		return fmt.Errorf("resolve directory path: %w", err)
	}
	cleanDir = absDir
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", cleanDir)
	case "darwin":
		cmd = exec.Command("open", cleanDir)
	default:
		cmd = exec.Command("xdg-open", cleanDir)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
