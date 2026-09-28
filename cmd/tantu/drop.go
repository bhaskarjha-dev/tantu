package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

const dropPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>ðŸ“¦ QuickDrop</title>
<style>
  :root {
    --bg: #0f1117;
    --card-bg: #1a1d27;
    --border: #2d3748;
    --text: #e2e8f0;
    --text-muted: #94a3b8;
    --accent: #3b82f6;
    --accent-hover: #2563eb;
    --success: #10b981;
    --error: #ef4444;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    background-color: var(--bg);
    color: var(--text);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 2rem 1rem;
  }
  .card {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 16px;
    box-shadow: 0 10px 30px rgba(0, 0, 0, 0.5);
    max-width: 640px;
    width: 100%;
    padding: 2.5rem;
  }
  header {
    margin-bottom: 2rem;
    text-align: center;
  }
  h1 {
    font-size: 1.75rem;
    font-weight: 700;
    margin-bottom: 0.5rem;
  }
  .peer-badge {
    display: inline-block;
    background: rgba(59, 130, 246, 0.15);
    border: 1px solid rgba(59, 130, 246, 0.3);
    color: #60a5fa;
    padding: 0.35rem 0.75rem;
    border-radius: 9999px;
    font-size: 0.85rem;
    margin-top: 0.5rem;
  }
  .section {
    margin-bottom: 2rem;
  }
  .section-title {
    font-size: 0.95rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    margin-bottom: 0.75rem;
  }
  .form-group {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    margin-bottom: 1rem;
  }
  textarea {
    width: 100%;
    min-height: 110px;
    background: #0f1117;
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    padding: 0.75rem 1rem;
    font-size: 0.95rem;
    font-family: inherit;
    outline: none;
    resize: vertical;
    transition: border-color 0.2s;
  }
  textarea:focus, input[type="text"]:focus {
    border-color: var(--accent);
  }
  input[type="text"] {
    background: #0f1117;
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    padding: 0.6rem 1rem;
    font-size: 0.9rem;
    outline: none;
    transition: border-color 0.2s;
  }
  button.action-btn {
    background: var(--accent);
    color: white;
    border: none;
    border-radius: 8px;
    padding: 0.75rem 1.5rem;
    font-weight: 600;
    font-size: 0.95rem;
    cursor: pointer;
    transition: background 0.2s, opacity 0.2s;
    align-self: flex-start;
  }
  button.action-btn:hover:not(:disabled) {
    background: var(--accent-hover);
  }
  button.action-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  .drop-zone {
    border: 2px dashed var(--border);
    border-radius: 12px;
    padding: 2rem 1rem;
    text-align: center;
    background: rgba(255, 255, 255, 0.02);
    cursor: pointer;
    transition: border-color 0.2s, background 0.2s;
  }
  .drop-zone.dragover {
    border-color: var(--accent);
    background: rgba(59, 130, 246, 0.08);
  }
  .drop-zone-icon {
    font-size: 2.2rem;
    margin-bottom: 0.5rem;
  }
  .drop-zone-text {
    font-size: 0.95rem;
    color: var(--text-muted);
  }
  .drop-zone-file {
    margin-top: 0.5rem;
    font-weight: 600;
    color: var(--accent);
  }
  input[type="file"] {
    display: none;
  }
  .status-msg {
    margin-top: 0.5rem;
    font-size: 0.875rem;
    min-height: 1.25rem;
  }
  .status-msg.success { color: var(--success); }
  .status-msg.error { color: var(--error); }
  .status-msg.sending { color: #60a5fa; }
  .received-list {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .received-item {
    background: #0f1117;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.85rem 1rem;
  }
  .received-item-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.4rem;
    font-size: 0.85rem;
    color: var(--text-muted);
  }
  .received-item-title {
    font-weight: 600;
    color: var(--text);
  }
  .received-item-content {
    font-family: monospace;
    font-size: 0.9rem;
    background: rgba(0, 0, 0, 0.3);
    padding: 0.5rem 0.75rem;
    border-radius: 6px;
    white-space: pre-wrap;
    word-break: break-all;
    max-height: 150px;
    overflow-y: auto;
  }
  .received-item-actions {
    margin-top: 0.5rem;
    display: flex;
    gap: 0.5rem;
  }
  .small-btn {
    background: rgba(255, 255, 255, 0.08);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 0.3rem 0.6rem;
    border-radius: 6px;
    font-size: 0.8rem;
    cursor: pointer;
    text-decoration: none;
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
  }
  .small-btn:hover {
    background: rgba(255, 255, 255, 0.15);
  }
  .empty-state {
    color: var(--text-muted);
    font-size: 0.875rem;
    font-style: italic;
  }
</style>
</head>
<body>
<div class="card">
  <header>
    <h1>ðŸ“¦ QuickDrop</h1>
    <div class="peer-badge">Connected to {{PEER}}</div>
  </header>

  <div class="status-msg" id="sessionBanner">Authorizing this page...</div>

  <!-- Section 1: Send Text -->
  <div class="section">
    <div class="section-title">Send Text</div>
    <div class="form-group">
      <input type="text" id="textLabel" placeholder="Optional label (e.g. API key, error trace)">
      <textarea id="textContent" placeholder="Paste or type text to send to paired machine..."></textarea>
      <button class="action-btn" id="btnSendText" onclick="sendText()">Send Text</button>
    </div>
    <div class="status-msg" id="textStatus"></div>
  </div>

  <!-- Section 2: Send File -->
  <div class="section">
    <div class="section-title">Send File</div>
    <div class="drop-zone" id="dropZone" onclick="document.getElementById('fileInput').click()">
      <div class="drop-zone-icon">ðŸ“</div>
      <div class="drop-zone-text">Click or drag-and-drop a file here</div>
      <div class="drop-zone-file" id="selectedFileName"></div>
      <input type="file" id="fileInput" onchange="onFileSelected(this.files)">
    </div>
    <div style="margin-top: 0.75rem;">
      <button class="action-btn" id="btnSendFile" onclick="sendFile()" disabled>Send File</button>
    </div>
    <div class="status-msg" id="fileStatus"></div>
  </div>

  <!-- Section 3: Received Drops -->
  <div class="section">
    <div class="section-title">Received Drops <span style="font-size: 0.8rem; text-transform: none; color: var(--success); margin-left: 0.5rem;" id="listenBadge">ðŸŸ¢ Listening</span></div>
    <div class="received-list" id="receivedList">
      <div class="empty-state" id="emptyState">No drops received yet. Waiting for incoming transfers...</div>
    </div>
  </div>
</div>

<script>
  // The long-lived API credential is deliberately NOT in this page. A token
  // served here on an unauthenticated listener is readable by any local
  // caller, which makes the API gate decorative. The page instead exchanges a
  // one-time bootstrap value — printed on the terminal that started tantu and
  // delivered out of band — for a short-lived HttpOnly session cookie.
  async function establishSession() {
    const supplied = new URLSearchParams(location.hash.slice(1)).get('bootstrap');
    if (!supplied) {
      document.getElementById('sessionBanner').textContent =
        'Open the one-time link printed by "tantu drop" in the terminal to authorize this page.';
      document.getElementById('sessionBanner').classList.add('error');
      return false;
    }
    const res = await fetch('/api/local/session', {
      method: 'POST',
      headers: { 'X-Tantu-Local-Bootstrap': supplied }
    });
    if (!res.ok) {
      document.getElementById('sessionBanner').textContent =
        'That bootstrap value is invalid or was already used. Run "tantu drop" again for a fresh link.';
      document.getElementById('sessionBanner').classList.add('error');
      return false;
    }
    // Drop the credential from the address bar and history.
    history.replaceState(null, '', location.pathname);
    document.getElementById('sessionBanner').remove();
    return true;
  }

  function apiFetch(path, options) {
    options = options || {};
    const headers = new Headers(options.headers || {});
    return fetch(path, Object.assign({}, options, { headers: headers, credentials: 'same-origin' }));
  }
  // All /api/* calls are session-gated; make the page inert until the
  // exchange succeeds rather than firing doomed requests.
  establishSession().then(function (ok) {
    if (!ok) return;
    pollIncomingDrops();
  });

  let selectedFile = null;

  function setStatus(id, text, type) {
    const el = document.getElementById(id);
    el.textContent = text;
    el.className = 'status-msg ' + (type || '');
  }

  function sendText() {
    const text = document.getElementById('textContent').value;
    const label = document.getElementById('textLabel').value.trim();
    if (!text) {
      setStatus('textStatus', 'âŒ Please enter text to send', 'error');
      return;
    }
    const btn = document.getElementById('btnSendText');
    btn.disabled = true;
    setStatus('textStatus', 'â³ Sending text...', 'sending');

    apiFetch('/api/send-text', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: text, name: label })
    })
    .then(r => r.json())
    .then(data => {
      if (data.status === 'success') {
        setStatus('textStatus', 'âœ… Sent successfully!', 'success');
        document.getElementById('textContent').value = '';
        document.getElementById('textLabel').value = '';
      } else {
        setStatus('textStatus', 'âŒ ' + (data.message || 'Send failed'), 'error');
      }
    })
    .catch(err => {
      setStatus('textStatus', 'âŒ ' + err.message, 'error');
    })
    .finally(() => {
      btn.disabled = false;
    });
  }

  function onFileSelected(files) {
    if (!files || files.length === 0) return;
    selectedFile = files[0];
    document.getElementById('selectedFileName').textContent = selectedFile.name + ' (' + formatBytes(selectedFile.size) + ')';
    document.getElementById('btnSendFile').disabled = false;
    setStatus('fileStatus', '', '');
  }

  function sendFile() {
    if (!selectedFile) return;
    const btn = document.getElementById('btnSendFile');
    btn.disabled = true;
    const isLarge = selectedFile.size > 50 * 1024 * 1024;
    const msg = isLarge 
      ? 'â³ Uploading large file (' + formatBytes(selectedFile.size) + ') â€” streaming to peer...' 
      : 'â³ Sending file (' + formatBytes(selectedFile.size) + ')...';
    setStatus('fileStatus', msg, 'sending');

    const formData = new FormData();
    formData.append('file', selectedFile);

    apiFetch('/api/send-file', {
      method: 'POST',
      body: formData
    })
    .then(r => r.json())
    .then(data => {
      if (data.status === 'success') {
        setStatus('fileStatus', 'âœ… File sent: ' + data.name + ' (' + formatBytes(data.size) + ')', 'success');
        selectedFile = null;
        document.getElementById('selectedFileName').textContent = '';
        document.getElementById('fileInput').value = '';
      } else {
        setStatus('fileStatus', 'âŒ ' + (data.message || 'Send failed'), 'error');
        btn.disabled = false;
      }
    })
    .catch(err => {
      setStatus('fileStatus', 'âŒ ' + err.message, 'error');
      btn.disabled = false;
    });
  }

  function formatBytes(bytes) {
    if (bytes === 0) return '0 Bytes';
    const k = 1024;
    const sizes = ['Bytes', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
  }

  // Drag and drop setup
  const dropZone = document.getElementById('dropZone');
  ['dragenter', 'dragover'].forEach(name => {
    dropZone.addEventListener(name, (e) => {
      e.preventDefault();
      dropZone.classList.add('dragover');
    }, false);
  });
  ['dragleave', 'drop'].forEach(name => {
    dropZone.addEventListener(name, (e) => {
      e.preventDefault();
      dropZone.classList.remove('dragover');
    }, false);
  });
  dropZone.addEventListener('drop', (e) => {
    const dt = e.dataTransfer;
    if (dt && dt.files && dt.files.length > 0) {
      onFileSelected(dt.files);
    }
  });

  // Long-poll incoming drops
  function pollIncomingDrops() {
    apiFetch('/api/receive')
      .then(r => r.json())
      .then(data => {
        if (data.status === 'success') {
          addReceivedDrop(data);
        }
        // Immediately poll again
        setTimeout(pollIncomingDrops, 500);
      })
      .catch(() => {
        // Retry after delay on error
        setTimeout(pollIncomingDrops, 3000);
      });
  }

  function addReceivedDrop(drop) {
    const emptyState = document.getElementById('emptyState');
    if (emptyState) emptyState.style.display = 'none';

    const list = document.getElementById('receivedList');
    const item = document.createElement('div');
    item.className = 'received-item';

    const header = document.createElement('div');
    header.className = 'received-item-header';
    const title = document.createElement('span');
    title.className = 'received-item-title';
    title.textContent = drop.kind === 'file'
      ? 'ðŸ“ File: ' + (drop.name || 'file')
      : 'ðŸ“ Text' + (drop.name ? ' (' + drop.name + ')' : '');
    const time = document.createElement('span');
    time.textContent = new Date().toLocaleTimeString();
    header.appendChild(title);
    header.appendChild(time);
    item.appendChild(header);

    const actions = document.createElement('div');
    actions.className = 'received-item-actions';
    if (drop.kind === 'file') {
      const content = document.createElement('div');
      content.textContent = (drop.name || 'file') + ' (' + formatBytes(drop.size) + ')';
      item.appendChild(content);
      if (typeof drop.url === 'string' && drop.url.startsWith('/api/download?')) {
        const link = document.createElement('a');
        link.href = drop.url;
        link.download = '';
        link.className = 'small-btn';
        link.textContent = 'â¬‡ï¸ Download';
        actions.appendChild(link);
      }
    } else {
      const content = document.createElement('div');
      content.className = 'received-item-content';
      content.textContent = drop.data || '';
      item.appendChild(content);
      const copy = document.createElement('button');
      copy.className = 'small-btn';
      copy.textContent = 'ðŸ“‹ Copy';
      copy.addEventListener('click', () => {
        const text = String(drop.data || '');
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).then(() => {
            const original = copy.textContent;
            copy.textContent = 'âœ… Copied!';
            setTimeout(() => { copy.textContent = original; }, 2000);
          }).catch(() => {});
        }
      });
      actions.appendChild(copy);
    }
    item.appendChild(actions);
    list.insertBefore(item, list.firstChild);
  }

  function escapeHtml(str) {
    if (!str) return '';
    return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  // Start polling
  pollIncomingDrops();
</script>
</body>
</html>
`

type dropReceivedItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
	LocalPath string `json:"-"`
}

func runDrop(args []string) {
	fs := flag.NewFlagSet("drop", flag.ExitOnError)
	port := fs.Int("port", 9875, "Port for QuickDrop Web UI (default: 9875)")
	transportType := fs.String("transport", "loopback", "Transport type: loopback, ssh, or lan (default: loopback)")
	bridgeAddr := fs.String("bridge", "127.0.0.1:9877", "Bridge server address for loopback (default: 127.0.0.1:9877)")
	peerAddr := fs.String("peer", "", "Address of paired peer HOST:PORT (optional when exactly 1 peer paired)")
	listenAddr := fs.String("listen", "", "Address to listen for incoming drops (optional background listener)")
	storeDir := fs.String("store-dir", "", "Override config directory (for --transport=lan)")
	sshHost := fs.String("ssh-host", "", "SSH server hostname (required when --transport=ssh)")
	sshPort := fs.Int("ssh-port", 22, "SSH server port (default: 22)")
	sshUser := fs.String("ssh-user", defaultUsername(), "SSH username (default: current OS user)")
	sshKey := fs.String("ssh-key", "", "Path to PEM private key for SSH auth (required when --transport=ssh)")
	sshKnownHosts := fs.String("ssh-known-hosts", "", "Path to OpenSSH known_hosts for server verification (default: ~/.ssh/known_hosts)")
	sshFingerprint := fs.String("ssh-fingerprint", "", "Pin the server host key (format: SHA256:...); overrides known_hosts when set")
	sshHostKey := fs.String("ssh-host-key", "", "Path to PEM-encoded host private key for SSH receive listener")
	timeout := fs.Duration("timeout", 5*time.Minute, "Timeout per drop transfer (default: 5 min)")
	verbose := fs.Bool("v", false, "Enable verbose output")
	outputDir := fs.String("output-dir", "", "Directory to save received files (default: system temp dir)")
	_ = fs.Parse(args)
	transportExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "transport" {
			transportExplicit = true
		}
	})
	if !transportExplicit && strings.TrimSpace(*sshHost) != "" {
		*transportType = "ssh"
	}

	outDir := *outputDir
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "tantu-drops")
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "Error: create output directory: %v\n", err)
		os.Exit(1)
	}
	if removed, _ := drop.SweepStalePartials(outDir, drop.DefaultStagingMaxAge); removed > 0 && *verbose {
		fmt.Printf("Cleaned %d stale partial file(s)\n", removed)
	}
	if removed, freed := drop.EnforcePartialBudget(outDir, drop.DefaultRetainedPartialBudget); removed > 0 && *verbose {
		fmt.Printf("Trimmed %d retained partial file(s) to the disk budget (%s reclaimed)\n", removed, formatBytes(freed))
	}
	dropTombstones := drop.NewTombstones(filepath.Join(outDir, drop.StagingDirName, drop.TombstoneFilename))

	var lanStore *pairing.PeerStore
	switch *transportType {
	case "loopback":
	case "ssh":
		if *sshHost == "" {
			fmt.Fprintln(os.Stderr, "Error: --ssh-host is required when --transport=ssh")
			os.Exit(1)
		}
		if *sshKey == "" {
			fmt.Fprintln(os.Stderr, "Error: --ssh-key is required when --transport=ssh")
			os.Exit(1)
		}
	case "lan":
		var err error
		lanStore, err = openPeerStore(*storeDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		resolved, err := resolvePeer(lanStore, *peerAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		*peerAddr = resolved
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown transport %q, expected loopback, ssh, or lan\n", *transportType)
		os.Exit(1)
	}

	var dialTarget string
	var expectedFingerprint string
	var tr transport.Transport
	switch *transportType {
	case "ssh":
		keyBytes, err := os.ReadFile(*sshKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read SSH key: %s: %v\n", *sshKey, err)
			os.Exit(1)
		}
		var hostKeyBytes []byte
		if *sshHostKey != "" {
			var readErr error
			hostKeyBytes, readErr = os.ReadFile(*sshHostKey)
			if readErr != nil {
				fmt.Fprintf(os.Stderr, "Error: cannot read SSH host key %q: %v\n", *sshHostKey, readErr)
				os.Exit(1)
			}
		}
		var sshErr error
		tr, sshErr = transport.NewSSHTransport(transport.SSHTransportConfig{
			User:               *sshUser,
			Host:               *sshHost,
			Port:               *sshPort,
			PrivateKey:         keyBytes,
			KnownHostsFile:     *sshKnownHosts,
			HostKeyFingerprint: *sshFingerprint,
			HostKey:            hostKeyBytes,
			AuthorizedKeys:     [][]byte{keyBytes},
		})
		if sshErr != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to configure SSH transport: %v\n", sshErr)
			os.Exit(1)
		}
		dialTarget = net.JoinHostPort(*sshHost, strconv.Itoa(*sshPort))
	case "lan":
		id, err := lanStore.LoadIdentity()
		if err != nil || id == nil {
			fmt.Fprintln(os.Stderr, "Error: No identity found. Run 'tantu pair' first.")
			os.Exit(1)
		}
		tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid local TLS identity: %v\n", err)
			os.Exit(1)
		}
		tr, err = transport.NewLANTransport(transport.LANTransportConfig{
			Cert:      tlsCert,
			IsTrusted: lanStore.IsTrusted,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to configure LAN transport: %v\n", err)
			os.Exit(1)
		}
		dialTarget = *peerAddr
		if resolvedPeer, resolveErr := resolvePeerForDial(lanStore, *peerAddr); resolveErr != nil {
			fmt.Fprintf(os.Stderr, "Error: LAN target is not a trusted paired peer: %v\n", resolveErr)
			os.Exit(1)
		} else {
			expectedFingerprint = resolvedPeer.Fingerprint
		}
	default:
		tr = transport.NewLoopbackTransport()
		dialTarget = *bridgeAddr
	}

	dialConfiguredPeer := func() (transport.Conn, error) {
		conn, dialErr := transport.DialPinned(tr, dialTarget, expectedFingerprint)
		if dialErr == nil {
			return conn, nil
		}
		// Preserve compatibility with the legacy `tantu serve` listener on
		// 9876 while preferring the unified Hub's wire listener on 9877.
		if *transportType == "loopback" && *bridgeAddr == "127.0.0.1:9877" {
			return tr.Dial("127.0.0.1:9876")
		}
		return nil, dialErr
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// In-memory drop queue for incoming receive drops
	var dropMu sync.Mutex
	dropItems := make(map[string]dropReceivedItem)
	reservedDrops := make(map[string]struct{})
	activeParts := make(map[string]string)
	dropOrder := make([]string, 0, maxStandaloneDropHistory)
	var dropHistoryBytes int64
	newDropChan := make(chan string, 32)
	sessionSlots := make(chan struct{}, maxStandaloneDropSessions)
	receiveQuota := drop.DefaultTransferQuota()
	// deliveredDrops tracks which queued items a poller has already served.
	// The wakeup channel above is a lossy hint (non-blocking sends drop
	// notifications under burst); the poll handler always scans the queue
	// for an unserved item instead of trusting a single channel delivery,
	// so a dropped hint can delay but never lose a notification.
	deliveredDrops := make(map[string]struct{})
	takeUndelivered := func() (dropReceivedItem, bool) {
		dropMu.Lock()
		defer dropMu.Unlock()
		for _, id := range dropOrder {
			item, ok := dropItems[id]
			if !ok {
				continue
			}
			if _, seen := deliveredDrops[id]; seen {
				continue
			}
			deliveredDrops[id] = struct{}{}
			return item, true
		}
		return dropReceivedItem{}, false
	}

	// Start background transport listener if --listen is provided
	if *listenAddr != "" {
		bgListener, err := tr.Listen(*listenAddr)
		if err != nil {
			if *verbose {
				fmt.Fprintf(os.Stderr, "âš ï¸ Background receive listener failed to bind %s: %v\n", *listenAddr, err)
			}
		} else {
			if *verbose {
				fmt.Printf("ðŸ“¥ QuickDrop background listener active on %s\n", bgListener.Addr().String())
			}
			go func() {
				<-ctx.Done()
				_ = bgListener.Close()
			}()

			go func() {
				for {
					conn, err := bgListener.Accept()
					if err != nil {
						if ctx.Err() != nil {
							break
						}
						continue
					}
					select {
					case sessionSlots <- struct{}{}:
					default:
						_ = conn.Close()
						if *verbose {
							fmt.Fprintf(os.Stderr, "âš ï¸ QuickDrop receive connection rejected: max active sessions (%d) reached\n", maxStandaloneDropSessions)
						}
						continue
					}
					go func(c transport.Conn) {
						defer func() { <-sessionSlots }()
						defer c.Close()
						var textBuf boundedTextBuffer
						textBuf.limit = standaloneTextDropLimit
						var fileObj *os.File
						var savedPath string
						var partPath string
						var releaseActivity func()
						var savedName string
						var reservedID string
						defer func() {
							if releaseActivity != nil {
								releaseActivity()
							} else if partPath != "" {
								_ = drop.ReleaseStagingActivity(partPath)
							}
						}()
						defer func() {
							if reservedID != "" {
								dropMu.Lock()
								delete(reservedDrops, reservedID)
								delete(activeParts, reservedID)
								dropMu.Unlock()
							}
						}()

						recvCfg := drop.ReceiveDropConfig{
							Timeout:     *timeout,
							MaxSize:     drop.DefaultMaxDropSize,
							MaxTextSize: standaloneTextDropLimit,
							Quota:       receiveQuota,
							Tombstones:  dropTombstones,
							TouchActivity: func(meta drop.DropSend) error {
								dropMu.Lock()
								partPath := activeParts[meta.DropID]
								dropMu.Unlock()
								if partPath == "" {
									return nil
								}
								return drop.TouchStagingActivity(partPath)
							},
							OnMeta: func(meta drop.DropSend) (io.Writer, error) {
								dropMu.Lock()
								if _, exists := reservedDrops[meta.DropID]; exists {
									dropMu.Unlock()
									return nil, fmt.Errorf("duplicate drop_id %q", meta.DropID)
								}
								reservedDrops[meta.DropID] = struct{}{}
								reservedID = meta.DropID
								dropMu.Unlock()
								if meta.Kind == drop.DropKindFile {
									safeName := sanitizeIncomingFilename(meta.Name)
									savedName = safeName
									unlockStaging := drop.LockStagingMaintenance()
									f, part, final, release, err := createIncomingPartWithActivity(outDir, safeName, meta)
									unlockStaging()
									if err != nil {
										return nil, err
									}
									fileObj = f
									partPath = part
									releaseActivity = release
									savedPath = final
									dropMu.Lock()
									activeParts[meta.DropID] = part
									dropMu.Unlock()
									return f, nil
								}
								return &textBuf, nil
							},
							BeforeComplete: func(res *drop.ReceiveDropResult) error {
								if res.Duplicate {
									// Re-acknowledged retry: staging never ran, so
									// there is nothing to publish.
									return nil
								}
								if res.Meta.Kind != drop.DropKindFile {
									return nil
								}
								if partPath == "" || savedPath == "" {
									return errors.New("completed file has no staging path")
								}
								published, err := finalizeIncomingPart(fileObj, partPath, savedPath, res.SHA256, res.Meta.Size, releaseActivity)
								if err != nil {
									return fmt.Errorf("publish received file: %w", err)
								}
								savedPath = published
								return nil
							},
						}

						res, err := drop.ReceiveDrop(ctx, c, nil, recvCfg)
						if fileObj != nil {
							_ = fileObj.Close()
						}
						if err != nil {
							dropMu.Lock()
							if reservedID != "" {
								delete(activeParts, reservedID)
							}
							protectedParts := make([]string, 0, len(activeParts)+1)
							for _, activePart := range activeParts {
								protectedParts = append(protectedParts, activePart)
							}
							if partPath != "" {
								protectedParts = append(protectedParts, partPath)
							}
							dropMu.Unlock()
							if partPath != "" {
								if info, statErr := drop.LstatStagingFile(partPath); statErr == nil && info.Mode().IsRegular() && info.Size() == 0 {
									_ = drop.RemoveStagingFile(partPath)
									_ = drop.RemoveStagingManifest(partPath)
								}
								if removed, freed := drop.EnforcePartialBudget(outDir, drop.DefaultRetainedPartialBudget, protectedParts...); removed > 0 && *verbose {
									fmt.Fprintf(os.Stderr, "Trimmed %d retained partial file(s) after failed transfer (%s reclaimed)\n", removed, formatBytes(freed))
								}
								if releaseActivity != nil {
									releaseActivity()
								} else {
									_ = drop.ReleaseStagingActivity(partPath)
								}
							}
							if *verbose {
								fmt.Fprintf(os.Stderr, "âŒ Error receiving drop: %v\n", err)
							}
							return
						}

						dropID := res.Meta.DropID
						if res.Duplicate {
							// Re-acknowledged retry: no new content arrived, so
							// no history item is created. The sender holds the
							// operation record.
							if *verbose {
								fmt.Fprintf(os.Stderr, "Duplicate delivery suppressed (idempotency key %q)\n", res.Meta.IdempotencyKey)
							}
							return
						}
						if dropID == "" {
							dropID = fmt.Sprintf("d-%d", time.Now().UnixNano())
						}

						item := dropReceivedItem{
							ID:   dropID,
							Kind: string(res.Meta.Kind),
							Name: res.Meta.Name,
							Size: res.BytesWritten,
						}

						if res.Meta.Kind == drop.DropKindFile {
							item.Name = savedName
							item.LocalPath = savedPath
							query := url.Values{}
							query.Set("id", dropID)
							item.URL = "/api/download?" + query.Encode()
						} else {
							item.Data = textBuf.String()
						}

						dropMu.Lock()
						if previous, exists := dropItems[dropID]; exists {
							dropHistoryBytes -= standaloneDropItemBytes(previous)
						} else {
							dropOrder = append(dropOrder, dropID)
						}
						dropItems[dropID] = item
						dropHistoryBytes += standaloneDropItemBytes(item)
						for len(dropOrder) > maxStandaloneDropHistory || dropHistoryBytes > maxStandaloneDropHistoryBytes {
							oldestID := dropOrder[0]
							if oldest, exists := dropItems[oldestID]; exists {
								dropHistoryBytes -= standaloneDropItemBytes(oldest)
								delete(dropItems, oldestID)
							}
							delete(deliveredDrops, oldestID)
							dropOrder = dropOrder[1:]
						}
						dropMu.Unlock()

						select {
						case newDropChan <- dropID:
						default:
						}
					}(conn)
				}
			}()
		}
	}

	mux := http.NewServeMux()

	peerInfo := fmt.Sprintf("%s (%s)", dialTarget, *transportType)
	// The API credential never enters the served page. The page must exchange
	// this one-time bootstrap value, which is printed here on the operator's
	// terminal, for a short-lived session cookie.
	localSession, tokenErr := NewLocalSession(0)
	if tokenErr != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to generate local session: %v\n", tokenErr)
		os.Exit(1)
	}
	// The one unauthenticated /api route: exchanges the one-time bootstrap
	// value for a session cookie.
	mux.HandleFunc("/api/local/session", handleLocalSessionExchange(localSession))
	pageContent := strings.ReplaceAll(dropPageHTML, "{{PORT}}", strconv.Itoa(*port))
	pageContent = strings.ReplaceAll(pageContent, "{{PEER}}", escapeHTML(peerInfo))
	if strings.Contains(pageContent, "IPC_TOKEN") {
		fmt.Fprintln(os.Stderr, "Error: refusing to serve a page that embeds an API credential")
		os.Exit(1)
	}

	writeJSON := func(w http.ResponseWriter, status int, data any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(data)
	}
	writeDropResponse := func(w http.ResponseWriter, item dropReceivedItem) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "success",
			"id":     item.ID,
			"kind":   item.Kind,
			"name":   item.Name,
			"size":   item.Size,
			"data":   item.Data,
			"url":    item.URL,
		})
	}

	// 1. GET / â€” serve HTML
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pageContent))
	})

	// 2. POST /api/send-text â€” send text drop
	mux.HandleFunc("/api/send-text", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "Method not allowed"})
			return
		}

		var req struct {
			Text string `json:"text"`
			Name string `json:"name"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, standaloneTextDropLimit+1)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "Invalid JSON body"})
			return
		}
		if req.Text == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "Text content is empty"})
			return
		}
		if int64(len(req.Text)) > standaloneTextDropLimit {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"status": "error", "message": "Text content exceeds 10MB limit"})
			return
		}

		conn, err := dialConfiguredPeer()
		if err != nil {
			if *verbose {
				fmt.Fprintf(os.Stderr, "âŒ Dial to %s failed: %v\n", dialTarget, err)
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"status": "error", "message": fmt.Sprintf("Failed to connect to peer at %s: %v", dialTarget, err)})
			return
		}
		defer conn.Close()

		meta := drop.DropSend{
			Kind: drop.DropKindText,
			Name: req.Name,
			Size: int64(len(req.Text)),
		}

		sendCtx, cancel := context.WithTimeout(r.Context(), *timeout)
		defer cancel()

		textSuppressed := false
		textCfg := drop.SendDropConfig{Timeout: *timeout}
		textCfg.OnComplete = func(c drop.DropComplete) { textSuppressed = c.Duplicate }
		if err := drop.SendDrop(sendCtx, conn, meta, strings.NewReader(req.Text), textCfg); err != nil {
			if *verbose {
				fmt.Fprintf(os.Stderr, "âŒ SendDrop error: %v\n", err)
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": fmt.Sprintf("Drop send failed: %v", err)})
			return
		}

		if textSuppressed {
			writeJSON(w, http.StatusOK, map[string]any{
				"status":    "success",
				"message":   "Already saved on this peer; duplicate delivery suppressed",
				"duplicate": true,
				"size":      len(req.Text),
			})
			return
		}

		if *verbose {
			fmt.Printf("âœ… Web UI sent text (%d bytes) to %s\n", len(req.Text), dialTarget)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "success",
			"message": "Text drop sent successfully",
			"size":    len(req.Text),
		})
	})

	// 3. POST /api/send-file â€” send file upload
	mux.HandleFunc("/api/send-file", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "Method not allowed"})
			return
		}

		// Cap file upload at the protocol default (buffer up to 32MB in RAM, spill remainder to disk)
		r.Body = http.MaxBytesReader(w, r.Body, drop.DefaultMaxDropSize)
		if err := r.ParseMultipartForm(32 * 1024 * 1024); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": fmt.Sprintf("Parse multipart error (max 5GB): %v", err)})
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()

		file, header, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "Missing file field in form"})
			return
		}
		defer file.Close()

		conn, err := dialConfiguredPeer()
		if err != nil {
			if *verbose {
				fmt.Fprintf(os.Stderr, "âŒ Dial to %s failed: %v\n", dialTarget, err)
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"status": "error", "message": fmt.Sprintf("Failed to connect to peer at %s: %v", dialTarget, err)})
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

		var filePayload io.Reader = file
		if seeker, ok := file.(io.ReadSeeker); ok {
			if header.Size >= 64*1024 {
				headBuf := make([]byte, 64*1024)
				n, rErr := io.ReadFull(seeker, headBuf)
				if n > 0 && (rErr == nil || rErr == io.EOF || rErr == io.ErrUnexpectedEOF) {
					h := sha256.Sum256(headBuf[:n])
					meta.HeadHash = hex.EncodeToString(h[:])
				}
				_, _ = seeker.Seek(0, io.SeekStart)
			}
			filePayload = &trackingFilePayload{
				seeker: seeker,
				onResume: func(offset int64) {
					fmt.Printf("âžœ Resuming transfer of %s from %s (offset %d)...\n", fileName, formatBytes(offset), offset)
				},
			}
		}

		sendCtx, cancel := context.WithTimeout(r.Context(), *timeout)
		defer cancel()

		fileSuppressed := false
		fileCfg := drop.SendDropConfig{Timeout: *timeout}
		fileCfg.OnComplete = func(c drop.DropComplete) { fileSuppressed = c.Duplicate }
		if err := drop.SendDrop(sendCtx, conn, meta, filePayload, fileCfg); err != nil {
			if *verbose {
				fmt.Fprintf(os.Stderr, "âŒ SendDrop file error: %v\n", err)
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": fmt.Sprintf("Drop file send failed: %v", err)})
			return
		}

		if fileSuppressed {
			writeJSON(w, http.StatusOK, map[string]any{
				"status":    "success",
				"message":   "Already saved on this peer; duplicate delivery suppressed",
				"duplicate": true,
				"name":      fileName,
				"size":      header.Size,
			})
			return
		}

		if *verbose {
			fmt.Printf("âœ… Web UI sent file %q (%d bytes) to %s\n", fileName, header.Size, dialTarget)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "success",
			"name":    fileName,
			"size":    header.Size,
			"message": "File drop sent successfully",
		})
	})

	// 4. GET /api/receive â€” long poll for incoming drops
	mux.HandleFunc("/api/receive", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "Method not allowed"})
			return
		}

		// Serve an already-queued but unserved item first: wakeup hints on
		// newDropChan are lossy under burst, so the queue itself is the
		// source of truth, never a single channel delivery.
		if item, ok := takeUndelivered(); ok {
			writeDropResponse(w, item)
			return
		}

		// Otherwise wait with a 25-second timeout
		pollTimer := time.NewTimer(25 * time.Second)
		defer pollTimer.Stop()

		select {
		case <-newDropChan:
			if item, ok := takeUndelivered(); ok {
				writeDropResponse(w, item)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{
				"status":  "timeout",
				"message": "No incoming drops",
			})
		case <-pollTimer.C:
			writeJSON(w, http.StatusOK, map[string]string{
				"status":  "timeout",
				"message": "No incoming drops",
			})
		case <-r.Context().Done():
			return
		}
	})

	// 5. GET /api/download â€” download a received file
	mux.HandleFunc("/api/download", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		dropMu.Lock()
		item, exists := dropItems[id]
		dropMu.Unlock()

		if !exists || item.LocalPath == "" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(item.Name)))
		http.ServeFile(w, r, item.LocalPath)
	})

	httpAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	server := newLocalHTTPServerWithSession(httpAddr, mux, localSession, *timeout)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	// The one-time bootstrap lives in the URL fragment so it never reaches the
	// server, a proxy log, or browser history.
	dashboardLink := fmt.Sprintf("http://%s/#bootstrap=%s", httpAddr, localSession.Bootstrap())
	fmt.Printf("QuickDrop Web UI listening on %s (forwarding to %s via %s)...\n", dashboardLink, dialTarget, *transportType)
	fmt.Println("Tip: 'tantu' (or 'tantu hub') now runs the unified Web Dashboard with Relay & QuickDrop.")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "QuickDrop server error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("tantu QuickDrop server stopped.")
}
