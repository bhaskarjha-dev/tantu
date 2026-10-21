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
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
	"github.com/bhaskarjha-dev/tantu/internal/webui"
)

const relayPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>tantu relay</title>
<style>{{THEME}}</style>
<style>{{NODEPAGE}}</style>
</head>
<body>
  <div class="card">
    <header>
      <h1><span class="ico ico--lock" aria-hidden="true"></span>tantu relay</h1>
      <div class="peer-badge">Connected to {{PEER}}</div>
    </header>

    <div class="section">
      <div class="section-title">One-Click Bookmarklet</div>
      <div class="bookmarklet-box">
        <a class="bookmarklet-btn" href="{{BOOKMARKLET_HREF}}"><span class="ico ico--bolt" aria-hidden="true"></span>Tantu</a>
        <div class="hint">Drag this button to your browser bookmarks bar. Click it on any OAuth login tab.</div>
      </div>
    </div>

    <div class="section">
      <div class="section-title">Manual Relay</div>
      <form id="relayForm" class="form-group form-group--inline">
        <input type="text" id="urlInput" placeholder="Paste OAuth URL (https://accounts.google.com/...)" required>
        <button type="submit" id="submitBtn" class="submit-btn">Relay</button>
        <button type="button" id="cancelBtn" class="submit-btn" style="display:none;">Cancel</button>
      </form>
      <div id="status"></div>
    </div>
  </div>

  <script>
    const form = document.getElementById('relayForm');
    const input = document.getElementById('urlInput');
    const btn = document.getElementById('submitBtn');
    const cancelBtn = document.getElementById('cancelBtn');
    const status = document.getElementById('status');
    // The mark is the product's own drawn glyph -- an element with a class,
    // not a character -- and every word that came from the server is set as
    // TEXT. The status used to be built with innerHTML and a message
    // interpolated into it, so a relayed URL could have become markup.
    // The label and the message are separate nodes, so the emphasis is
    // kept and nothing untrusted is ever parsed as markup.
    function showStatus(kind, label, message) {
      status.className = kind;
      status.textContent = '';
      const icon = document.createElement('span');
      icon.className = 'ico ' + (kind === 'success' ? 'ico--check' : kind === 'relaying' ? 'ico--clock' : 'ico--alert');
      icon.setAttribute('aria-hidden', 'true');
      status.appendChild(icon);
      if (label) {
        const strong = document.createElement('strong');
        strong.textContent = label;
        status.appendChild(strong);
      }
      if (message) status.appendChild(document.createTextNode(' ' + message));
    }
    // The relay POST blocks for the whole sign-in. Aborting it stops this page
    // waiting; the server-side cancel is what actually releases the peer.
    let relayAbort = null;
    let relayInFlight = false;

    cancelBtn.addEventListener('click', async () => {
      cancelBtn.disabled = true;
      if (relayAbort) relayAbort.abort();
      try {
        const resp = await fetch('/relay/cancel', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-Tantu-Relay-Ticket': '{{RELAY_TOKEN}}' },
          body: JSON.stringify({})
        });
        const data = await resp.json();
        showStatus('success', '', data.message || 'Sign-in cancelled. You can start again.');
      } catch (err) {
        showStatus('error', 'Could not cancel:', err.message +
          ' The sign-in will end on its own timeout.');
      } finally {
        cancelBtn.disabled = false;
        cancelBtn.style.display = 'none';
        btn.disabled = false;
        relayInFlight = false;
      }
    });

    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const url = input.value.trim();
      if (!url) return;

      btn.disabled = true;
      cancelBtn.style.display = 'inline-block';
      cancelBtn.disabled = false;
      relayInFlight = true;
      relayAbort = new AbortController();
      showStatus('relaying', '', 'Relaying OAuth URL through bridge...');

      try {
        const resp = await fetch('/relay', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-Tantu-Relay-Ticket': '{{RELAY_TOKEN}}' },
          body: JSON.stringify({ url: url }),
          signal: relayAbort.signal
        });
        relayInFlight = false;
        const data = await resp.json();
        if (resp.ok && data.status === 'success') {
          showStatus('success', 'Authentication completed successfully!', '');
          input.value = '';
        } else {
          showStatus('error', 'Relay failed:', data.message || 'Unknown error');
        }
      } catch (err) {
        relayInFlight = false;
        if (err.name === 'AbortError') return;
        showStatus('error', 'Connection error:', err.message);
      } finally {
        btn.disabled = false;
        cancelBtn.style.display = 'none';
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
	sshKnownHosts := fs.String("ssh-known-hosts", "", "Path to OpenSSH known_hosts for server verification (default: ~/.ssh/known_hosts)")
	sshFingerprint := fs.String("ssh-fingerprint", "", "Pin the server host key (format: SHA256:...); overrides known_hosts when set")
	timeout := fs.Duration("timeout", 5*time.Minute, "Timeout per relay session")
	verbose := fs.Bool("v", false, "Enable verbose output")
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
		var sshErr error
		tr, sshErr = transport.NewSSHTransport(transport.SSHTransportConfig{
			User:               *sshUser,
			Host:               *sshHost,
			Port:               *sshPort,
			PrivateKey:         keyBytes,
			KnownHostsFile:     *sshKnownHosts,
			HostKeyFingerprint: *sshFingerprint,
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	relayGroup := newLocalSingleFlight()

	// The page is gated behind a one-time bootstrap session. Previously GET /
	// was an unauthenticated ticket dispenser, so any local caller could obtain
	// a usable relay capability and drive the peer's browser to a hostile page.
	pageSession, err := NewLocalSession(0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// One-time tickets are the capability the /relay endpoint checks. A
	// bookmarklet is a cross-site top-level navigation that cannot set headers
	// and cannot rely on a SameSite=Strict cookie, so it must carry its ticket
	// in the query string — but the ticket is only ever minted for an operator
	// who has already authenticated to this page.
	//
	// The long-lived per-process token fallback is deliberately removed: it
	// makes a single captured value replayable forever.
	relayTickets := newLocalTicketPool()
	peerInfo := fmt.Sprintf("%s (%s)", dialTarget, *transportType)
	renderRelayPage := func(bookmarkletTicket, formTicket string) string {
		// The bookmarklet and the manual form each get their own ticket:
		// using one capability for both would burn the saved bookmarklet
		// the first time the form is submitted (and vice versa).
		if bookmarkletTicket == "" {
			bookmarkletTicket = relayTickets.Issue()
		}
		if formTicket == "" {
			formTicket = relayTickets.Issue()
		}
		bookmarkletJS := fmt.Sprintf("javascript:void(window.open('http://127.0.0.1:%d/relay?ticket=%s&url='+encodeURIComponent(location.href),'_blank','width=550,height=380'))", *port, bookmarkletTicket)
		pageContent := strings.ReplaceAll(relayPageHTML, "{{PORT}}", strconv.Itoa(*port))
		pageContent = strings.ReplaceAll(pageContent, "{{PEER}}", escapeHTML(peerInfo))
		pageContent = strings.ReplaceAll(pageContent, "{{RELAY_TOKEN}}", formTicket)
		// The page draws from the product's design system and the shared
		// node shell, so a relay page and a Hub page are the same product
		// rather than two that happen to share a name. The page used to
		// carry its own palette -- a third set of values, hand-copied --
		// and its own copy of the layout, which the QuickDrop page carried
		// too.
		pageContent = strings.ReplaceAll(pageContent, "{{THEME}}", webui.ThemeCSS)
		pageContent = strings.ReplaceAll(pageContent, "{{NODEPAGE}}", nodePageCSS)
		return strings.ReplaceAll(pageContent, "{{BOOKMARKLET_HREF}}", bookmarkletJS)
	}

	type relayResponse struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}

	writeJSON := func(w http.ResponseWriter, status int, resp relayResponse) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}

	// The URL of the relay currently in flight, so /relay/cancel can name the
	// flow. Guarded because the cancel endpoint is a separate request on its own
	// goroutine. Holding the raw URL here is no worse than the request that is
	// already using it; the flow identity derived from it is what goes on the
	// wire, and the value is cleared when the flow ends.
	var lastRelayURLMu sync.Mutex
	var lastRelayURL string
	setLastRelayURL := func(u string) {
		lastRelayURLMu.Lock()
		lastRelayURL = u
		lastRelayURLMu.Unlock()
	}
	clearLastRelayURL := func() {
		lastRelayURLMu.Lock()
		lastRelayURL = ""
		lastRelayURLMu.Unlock()
	}
	readLastRelayURL := func() string {
		lastRelayURLMu.Lock()
		defer lastRelayURLMu.Unlock()
		return lastRelayURL
	}

	mux.HandleFunc("/api/local/session", handleLocalSessionExchange(pageSession))
	mux.HandleFunc("/api/local/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		if !pageSession.Authorize(r) {
			http.Error(w, "local session required", http.StatusForbidden)
			return
		}
		// Issue a fresh single-use ticket pair for the authenticated operator.
		// A caller that must present a session cannot drive this by accident.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"form":        relayTickets.Issue(),
			"bookmarklet": relayTickets.Issue(),
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		// The page requires a session; without one it explains how to get one
		// and mints no capability at all.
		if !pageSession.Authorize(r) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			// The dead end, on the shared design system: the colours it used
			// to carry were hand-copied from the Hub's, and a page nobody looks
			// at is a page whose colours nobody checks.
			// The landing page for the printed link, and the place the
			// one-time bootstrap value is exchanged for a session.
			//
			// It has to be HERE and not on the page behind the gate: the
			// server hands out a session cookie and nothing else, and the
			// value in the address bar's fragment is never sent to a server.
			// So the page behind the gate could never be reached, and a
			// person who opened the printed link was told to open the
			// printed link. The exchange route has been registered since the
			// page was put behind a session; nothing ever called it.
			deadEnd := "<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\">" +
				"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">" +
				"<title>tantu relay — authorization required</title>" +
				"<style>{{THEME}}</style><style>{{NODEPAGE}}</style></head>" +
				"<body><div class=\"card\"><header><h1>Authorization required</h1></header>" +
				"<div class=\"status-msg\" id=\"sessionBanner\">Exchanging the one-time link for a session...</div>" +
				"<p class=\"hint\">No relay capability is issued to an unauthenticated caller.</p>" +
				"</div>" +
				"<script>(function(){" +
				"var banner=document.getElementById('sessionBanner');" +
				"var supplied=new URLSearchParams(location.hash.slice(1)).get('bootstrap');" +
				"if(!supplied){banner.textContent='Open the one-time link printed by \\\"tantu relay\\\" in the terminal to authorize this page.';banner.classList.add('error');return;}" +
				"fetch('/api/local/session',{method:'POST',headers:{'X-Tantu-Local-Bootstrap':supplied}})" +
				".then(function(res){" +
				"if(!res.ok){banner.textContent='That bootstrap value is invalid or was already used. Run \\\"tantu relay\\\" again for a fresh link.';banner.classList.add('error');return;}" +
				"history.replaceState(null,'',location.pathname);" +
				"location.reload();" +
				"}).catch(function(){banner.textContent='The Hub could not be reached. Try again.';banner.classList.add('error');});" +
				"})();</script>" +
				"</body></html>"
			deadEnd = strings.ReplaceAll(deadEnd, "{{THEME}}", webui.ThemeCSS)
			deadEnd = strings.ReplaceAll(deadEnd, "{{NODEPAGE}}", nodePageCSS)
			_, _ = w.Write([]byte(deadEnd))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(renderRelayPage(relayTickets.Issue(), relayTickets.Issue())))
	})

	// /relay/cancel releases the sign-in this relay opened.
	//
	// The Cancel button in the served page used to be a window close, which
	// cancelled nothing: the peer kept its sign-in and its bound loopback
	// callback port until the timeout, and because that port is normally the
	// application's fixed redirect port, the next attempt was refused. This
	// endpoint is what makes the button mean what it says.
	mux.HandleFunc("/relay/cancel", func(w http.ResponseWriter, r *http.Request) {
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
		// Same single-use capability as /relay. A cancel is a privileged
		// operation, so it must not be reachable by a bare POST from any local
		// process.
		if !relayTickets.Consume(r.Header.Get("X-Tantu-Relay-Ticket")) {
			writeJSON(w, http.StatusForbidden, relayResponse{
				Status:  "error",
				Message: "invalid or missing local relay token",
			})
			return
		}

		flowID := bridge.DeriveOAuthFlowID(readLastRelayURL())
		if flowID == "" {
			// No relay in progress, or the URL is gone. Not a failure: the flow
			// may have already finished.
			writeJSON(w, http.StatusOK, relayResponse{
				Status:  "success",
				Message: "No sign-in is in progress. It may have already finished or timed out.",
			})
			return
		}
		conn, dialErr := transport.DialPinned(tr, dialTarget, expectedFingerprint)
		if dialErr != nil {
			writeJSON(w, http.StatusBadGateway, relayResponse{
				Status:  "error",
				Message: "Could not reach the other machine to release its sign-in. It will end on its own timeout.",
			})
			return
		}
		defer conn.Close()
		released, cancelErr := bridge.CancelRemoteSession(r.Context(), conn, "", flowID, "cancelled from the relay page")
		if cancelErr != nil {
			writeJSON(w, http.StatusBadGateway, relayResponse{
				Status:  "error",
				Message: "The cancel could not be delivered. The sign-in will end on its own timeout.",
			})
			return
		}
		if !released {
			writeJSON(w, http.StatusOK, relayResponse{
				Status:  "success",
				Message: "No matching sign-in was open on the other machine. Nothing is being held.",
			})
			return
		}
		if *verbose {
			fmt.Println("🚪 [relay] Released the sign-in on the peer at the user's request")
		}
		writeJSON(w, http.StatusOK, relayResponse{
			Status:  "success",
			Message: "Sign-in cancelled. The other machine released it; you can start again.",
		})
	})

	mux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, relayResponse{
				Status:  "error",
				Message: "method not allowed, use GET or POST",
			})
			return
		}

		respond := func(status int, resp relayResponse) {
			if strings.Contains(r.Header.Get("Accept"), "text/html") {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(status)
				if resp.Status == "success" {
					fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>✅ Auth Relayed</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;}.box{background:#131722;border:1px solid #232a3b;border-radius:12px;padding:2rem;max-width:450px;text-align:center;box-shadow:0 4px 20px rgba(0,0,0,0.5);}.icon{font-size:2.5rem;margin-bottom:0.75rem;}h2{color:#34d399;margin:0 0 0.5rem 0;}p{color:#8b949e;font-size:0.9rem;margin:0 0 1rem 0;}.hint{color:#64748b;font-size:0.8rem;}</style></head><body><div class="box"><div class="icon">✅</div><h2>Authentication Relayed!</h2><p>%s</p><div class="hint">This window will close automatically in 3 seconds...</div></div><script>setTimeout(function(){window.close();},3000);</script></body></html>`, escapeHTML(resp.Message))
				} else {
					fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>❌ Relay Error</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;}.box{background:#131722;border:1px solid #232a3b;border-radius:12px;padding:2rem;max-width:450px;text-align:center;box-shadow:0 4px 20px rgba(0,0,0,0.5);}.icon{font-size:2.5rem;margin-bottom:0.75rem;}h2{color:#f87171;margin:0 0 0.5rem 0;}p{color:#8b949e;font-size:0.9rem;margin:0 0 1rem 0;}.hint{color:#64748b;font-size:0.8rem;}</style></head><body><div class="box"><div class="icon">❌</div><h2>Relay Error</h2><p>%s</p><div class="hint">Check terminal logs or verify your peer is connected.</div></div></body></html>`, escapeHTML(resp.Message))
				}
				return
			}
			writeJSON(w, status, resp)
		}

		// URL presence is validated before the capability check so malformed
		// or empty requests never burn a single-use ticket. This reveals
		// nothing sensitive: it distinguishes "no URL" from "bad token"
		// only, mirroring the Hub's legacy endpoint behavior.
		rawURL := ""
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
			var postReq struct {
				URL string `json:"url"`
			}
			if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				if err := json.NewDecoder(r.Body).Decode(&postReq); err == nil {
					rawURL = postReq.URL
				}
			} else {
				rawURL = r.FormValue("url")
			}
		}
		if rawURL == "" {
			rawURL = strings.TrimSpace(r.URL.Query().Get("url"))
		} else {
			rawURL = strings.TrimSpace(rawURL)
		}
		if rawURL == "" {
			respond(http.StatusBadRequest, relayResponse{
				Status:  "error",
				Message: "missing or empty url parameter",
			})
			return
		}

		// Capability check: single-use tickets only — in a header for the
		// manual form, in the query for bookmarklet navigations which cannot
		// set headers. No long-lived token is accepted, so one captured value
		// cannot be replayed indefinitely.
		headerCap := r.Header.Get("X-Tantu-Relay-Ticket")
		queryTicket := r.URL.Query().Get("ticket")
		authorized := relayTickets.Consume(headerCap) || relayTickets.Consume(queryTicket)
		if !authorized {
			respond(http.StatusForbidden, relayResponse{
				Status:  "error",
				Message: "invalid or missing local relay token",
			})
			return
		}

		oauthURL := browser.SanitizeURL(rawURL)
		if *verbose {
			fmt.Printf("🔗 [relay] Received request for URL: %s\n", browser.RedactURL(oauthURL))
		}

		flowKey := bridge.DeriveOAuthFlowID(oauthURL)
		if flowKey == "" {
			// Never retain a raw authorization URL (which may contain state,
			// PKCE, or code material) as a coordination key.
			digest := sha256.Sum256([]byte(oauthURL))
			flowKey = hex.EncodeToString(digest[:])
		}
		relayKey := dialTarget + "\x00" + flowKey
		// Published so /relay/cancel can identify the flow. The URL is already
		// in memory for the duration of the request; this keeps a pointer, not
		// a copy, and it is cleared when the flow ends.
		setLastRelayURL(oauthURL)
		defer clearLastRelayURL()
		relayErr := relayGroup.Do(r.Context(), relayKey, func() error {
			conn, err := transport.DialPinned(tr, dialTarget, expectedFingerprint)
			if err != nil {
				if *verbose {
					fmt.Printf("❌ [relay] Dial failed to %s: %v\n", dialTarget, err)
				}
				return fmt.Errorf("failed to connect to bridge at %s: %w", dialTarget, err)
			}
			defer conn.Close()

			// The leader owns the wire/browser flow, not the first HTTP
			// request. A user closing or refreshing that tab must not cancel a
			// flow that another duplicate request is still waiting on.
			relayCtx, cancel := context.WithTimeout(ctx, *timeout)
			defer cancel()
			if err := bridge.HandleBSide(relayCtx, conn, oauthURL, bridge.BSideConfig{Timeout: *timeout}); err != nil {
				if *verbose {
					fmt.Printf("❌ [relay] Bridge flow failed: %s\n", bridge.RedactError(err))
				}
				return fmt.Errorf("bridge flow failed: %w", err)
			}
			return nil
		})
		if relayErr != nil {
			status := http.StatusBadGateway
			if errors.Is(relayErr, context.Canceled) || errors.Is(relayErr, context.DeadlineExceeded) {
				status = http.StatusGatewayTimeout
			}
			respond(status, relayResponse{
				Status:  "error",
				Message: "relay flow failed",
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
	server := newLocalHTTPServerWithSession(httpAddr, mux, pageSession, *timeout)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	// The one-time bootstrap lives in the URL fragment so it never reaches the
	// server, a proxy log, or browser history.
	fmt.Printf("Relay server ready at http://%s/#bootstrap=%s (forwarding to %s via %s)...\n", httpAddr, pageSession.Bootstrap(), dialTarget, *transportType)
	fmt.Println("Tip: 'tantu' (or 'tantu hub') now runs the unified Web Dashboard with Relay & QuickDrop.")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "Relay server error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("tantu relay server stopped.")
}
