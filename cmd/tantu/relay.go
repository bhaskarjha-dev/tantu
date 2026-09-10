package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

const relayPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>🔐 tantu relay</title>
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
    max-width: 600px;
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
  .bookmarklet-box {
    background: rgba(255, 255, 255, 0.03);
    border: 1px dashed var(--border);
    border-radius: 12px;
    padding: 1.25rem;
    text-align: center;
  }
  .bookmarklet-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    background: var(--accent);
    color: #fff;
    padding: 0.65rem 1.25rem;
    border-radius: 8px;
    font-weight: 600;
    font-size: 1rem;
    text-decoration: none;
    cursor: grab;
    transition: background 0.2s, transform 0.1s;
    user-select: none;
  }
  .bookmarklet-btn:hover {
    background: var(--accent-hover);
    transform: translateY(-1px);
  }
  .hint {
    font-size: 0.8rem;
    color: var(--text-muted);
    margin-top: 0.6rem;
  }
  .form-group {
    display: flex;
    gap: 0.5rem;
  }
  input[type="text"] {
    flex: 1;
    background: #0f1117;
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    padding: 0.75rem 1rem;
    font-size: 0.95rem;
    outline: none;
    transition: border-color 0.2s;
  }
  input[type="text"]:focus {
    border-color: var(--accent);
  }
  button.submit-btn {
    background: var(--accent);
    color: white;
    border: none;
    border-radius: 8px;
    padding: 0 1.25rem;
    font-weight: 600;
    font-size: 0.95rem;
    cursor: pointer;
    transition: background 0.2s;
  }
  button.submit-btn:hover {
    background: var(--accent-hover);
  }
  button.submit-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  #status {
    display: none;
    border-radius: 8px;
    padding: 1rem;
    margin-top: 1.5rem;
    font-size: 0.9rem;
    line-height: 1.4;
  }
  #status.relaying {
    display: block;
    background: rgba(234, 179, 8, 0.1);
    border: 1px solid rgba(234, 179, 8, 0.3);
    color: #facc15;
  }
  #status.success {
    display: block;
    background: rgba(16, 185, 129, 0.1);
    border: 1px solid rgba(16, 185, 129, 0.3);
    color: #34d399;
  }
  #status.error {
    display: block;
    background: rgba(239, 68, 68, 0.1);
    border: 1px solid rgba(239, 68, 68, 0.3);
    color: #f87171;
  }
</style>
</head>
<body>
  <div class="card">
    <header>
      <h1>🔐 tantu relay</h1>
      <div class="peer-badge">Connected to {{PEER}}</div>
    </header>

    <div class="section">
      <div class="section-title">One-Click Bookmarklet</div>
      <div class="bookmarklet-box">
        <a class="bookmarklet-btn" href="{{BOOKMARKLET_HREF}}">⚡ Tantu</a>
        <div class="hint">Drag this button to your browser bookmarks bar. Click it on any OAuth login tab.</div>
      </div>
    </div>

    <div class="section">
      <div class="section-title">Manual Relay</div>
      <form id="relayForm" class="form-group">
        <input type="text" id="urlInput" placeholder="Paste OAuth URL (https://accounts.google.com/...)" required>
        <button type="submit" id="submitBtn" class="submit-btn">Relay</button>
      </form>
      <div id="status"></div>
    </div>
  </div>

  <script>
    const form = document.getElementById('relayForm');
    const input = document.getElementById('urlInput');
    const btn = document.getElementById('submitBtn');
    const status = document.getElementById('status');

    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const url = input.value.trim();
      if (!url) return;

      btn.disabled = true;
      status.className = 'relaying';
      status.innerHTML = '⏳ Relaying OAuth URL through bridge...';

      try {
        const resp = await fetch('/relay?url=' + encodeURIComponent(url));
        const data = await resp.json();
        if (resp.ok && data.status === 'success') {
          status.className = 'success';
          status.innerHTML = '✅ <strong>Authentication completed successfully!</strong>';
          input.value = '';
        } else {
          status.className = 'error';
          status.innerHTML = '❌ <strong>Relay failed:</strong> ' + (data.message || 'Unknown error');
        }
      } catch (err) {
        status.className = 'error';
        status.innerHTML = '❌ <strong>Connection error:</strong> ' + err.message;
      } finally {
        btn.disabled = false;
      }
    });
  </script>
</body>
</html>`

func runRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	transportType := fs.String("transport", "loopback", "Transport type: loopback, ssh, or lan (default: loopback)")
	bridgeAddr := fs.String("bridge", "127.0.0.1:9876", "Bridge server address for loopback (default: 127.0.0.1:9876)")
	peerAddr := fs.String("peer", "", "Address of paired peer HOST:PORT (optional when exactly 1 peer paired)")
	port := fs.Int("port", 9876, "Local HTTP server port (default: 9876)")
	storeDir := fs.String("store-dir", "", "Override config directory (for --transport=lan)")
	sshHost := fs.String("ssh-host", "", "SSH server hostname (required when --transport=ssh)")
	sshPort := fs.Int("ssh-port", 22, "SSH server port (default: 22)")
	sshUser := fs.String("ssh-user", defaultUsername(), "SSH username (default: current OS user)")
	sshKey := fs.String("ssh-key", "", "Path to PEM private key for SSH auth (required when --transport=ssh)")
	timeout := fs.Duration("timeout", 5*time.Minute, "Timeout per relay session")
	verbose := fs.Bool("v", false, "Enable verbose output")
	_ = fs.Parse(args)

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
	var tr transport.Transport
	switch *transportType {
	case "ssh":
		keyBytes, err := os.ReadFile(*sshKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read SSH key: %s: %v\n", *sshKey, err)
			os.Exit(1)
		}
		var sshErr error
		tr, sshErr = transport.NewSSHTransport(transport.SSHTransportConfig{
			User:       *sshUser,
			Host:       *sshHost,
			Port:       *sshPort,
			PrivateKey: keyBytes,
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
		peers := lanStore.ListPeers()
		var trustedFPs []string
		for _, p := range peers {
			trustedFPs = append(trustedFPs, p.Fingerprint)
		}
		tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid local TLS identity: %v\n", err)
			os.Exit(1)
		}
		tr, err = transport.NewLANTransport(transport.LANTransportConfig{
			Cert:                tlsCert,
			TrustedFingerprints: trustedFPs,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to configure LAN transport: %v\n", err)
			os.Exit(1)
		}
		dialTarget = *peerAddr
	default:
		tr = transport.NewLoopbackTransport()
		dialTarget = *bridgeAddr
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()

	bookmarkletJS := fmt.Sprintf("javascript:void(window.open('http://localhost:%d/relay?url='+encodeURIComponent(location.href),'_blank','width=550,height=380'))", *port)
	peerInfo := fmt.Sprintf("%s (%s)", dialTarget, *transportType)
	pageContent := strings.ReplaceAll(relayPageHTML, "{{PORT}}", strconv.Itoa(*port))
	pageContent = strings.ReplaceAll(pageContent, "{{PEER}}", peerInfo)
	pageContent = strings.ReplaceAll(pageContent, "{{BOOKMARKLET_HREF}}", bookmarkletJS)

	type relayResponse struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}

	writeJSON := func(w http.ResponseWriter, status int, resp relayResponse) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pageContent))
	})

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
		if *verbose {
			fmt.Printf("🔗 [relay] Received request for URL: %s\n", oauthURL)
		}

		conn, err := tr.Dial(dialTarget)
		if err != nil {
			if *verbose {
				fmt.Printf("❌ [relay] Dial failed to %s: %v\n", dialTarget, err)
			}
			respond(http.StatusBadGateway, relayResponse{
				Status:  "error",
				Message: fmt.Sprintf("failed to connect to bridge at %s: %v", dialTarget, err),
			})
			return
		}
		defer conn.Close()

		relayCtx, cancel := context.WithTimeout(r.Context(), *timeout)
		defer cancel()

		cfg := bridge.BSideConfig{Timeout: *timeout}
		if err := bridge.HandleBSide(relayCtx, conn, oauthURL, cfg); err != nil {
			if *verbose {
				fmt.Printf("❌ [relay] Bridge flow failed: %v\n", err)
			}
			respond(http.StatusInternalServerError, relayResponse{
				Status:  "error",
				Message: fmt.Sprintf("bridge flow failed: %v", err),
			})
			return
		}

		if *verbose {
			fmt.Println("✅ [relay] Authentication completed successfully.")
		}
		respond(http.StatusOK, relayResponse{
			Status:  "success",
			Message: "Authentication completed successfully",
		})
	})

	httpAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	server := &http.Server{
		Addr:    httpAddr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Printf("Relay server listening on http://%s (forwarding to %s via %s)...\n", httpAddr, dialTarget, *transportType)
	fmt.Println("Tip: 'tantu' (or 'tantu hub') now runs the unified Web Dashboard with Relay & QuickDrop.")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "Relay server error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("tantu relay server stopped.")
}
