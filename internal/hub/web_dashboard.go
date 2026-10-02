package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
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

	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/discovery"
	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// dashboardHTML is the Hub dashboard: one HTML document embedded from
// dashboard.html instead of held in a Go raw string. The source gets real
// tooling (highlighting, linting, per-hunk diffs), the JavaScript may use
// template literals - a backtick is the one character a Go raw string can
// never contain - and the served bytes are identical to the previous
// in-source literal. {{VERSION}} and {{NONCE}} are substituted per response.
//
//go:embed dashboard.html
var dashboardHTML string

type relayResponse struct {
	Status      string `json:"status"`
	Message     string `json:"message"`
	OperationID string `json:"operation_id,omitempty"`
	Destination string `json:"destination,omitempty"`
	NextAction  string `json:"next_action,omitempty"`
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
					MaxAge:   int(dashboardSessionTTL.Seconds()),
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
	Name     string `json:"name"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	IsPaired bool   `json:"is_paired"`
}

// buildDiscoveredPeers converts discovery's unauthenticated hints into the
// dashboard response. Discovery carries no identity, so there is no
// fingerprint or SAS to report — nothing here has been verified. IsPaired is
// a display/filter hint derived from the address host (see
// pairing.PeerStore.HasPeerAtAddress) and must never be used for
// authorization; trust is decided by fingerprint checks on authenticated
// connections.
func buildDiscoveredPeers(nodes []discovery.DiscoveredNode, store *pairing.PeerStore) []discoveredPeerResponse {
	out := make([]discoveredPeerResponse, 0, len(nodes))
	for _, node := range nodes {
		isPaired := false
		if store != nil {
			isPaired = store.HasPeerAtAddress(node.Address)
		}
		out = append(out, discoveredPeerResponse{
			Name:     node.InstanceName,
			Address:  node.Address,
			Port:     node.Port,
			IsPaired: isPaired,
		})
	}
	return out
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

// relayInterstitialHTML is the bookmarklet confirmation shell. It carries
// no credential and authorizes nothing: the full authorization URL reaches
// the relay endpoint only through an explicit same-origin POST after a click.
// Dynamic values enter via textContent or a JSON-encoded literal, never via
// HTML interpolation of untrusted content.
//
//go:embed relay_interstitial.html
var relayInterstitialHTML string

// renderRelayInterstitial serves the bookmarklet confirmation shell for a
// relay request that carries no credential. It authorizes nothing: the page
// shows only the safe origin host and offers an explicit Relay button, which
// POSTs same-origin so the dashboard session flows. Without a click and a
// valid session, no relay happens. expiredLink notes a previously-ticketed
// bookmark and explains the extra step instead of dead-ending.
func (h *Hub) renderRelayInterstitial(w http.ResponseWriter, rawURL string, expiredLink bool) {
	sanitized := browser.SanitizeURL(strings.TrimSpace(rawURL))
	renderErr := func() {
		// A dead end is not an error message. This page is reached by a bad or
		// non-relayable link, so it names the reason, the one safe next step,
		// and a keyboard-reachable way back to the dashboard.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Relay Error</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;padding:1rem;box-sizing:border-box;}.box{background:#131722;border:1px solid #232a3b;border-radius:16px;padding:2rem;max-width:460px;width:92%;text-align:center;box-shadow:0 25px 50px -12px rgba(0,0,0,0.5);}h1{margin:0 0 0.5rem 0;font-size:1.1rem;}p{color:#94a3b8;font-size:0.9rem;margin:0 0 1rem 0;line-height:1.5;}.btn{display:inline-block;background:#2563eb;color:#fff;min-height:44px;padding:0.6rem 1.25rem;border-radius:8px;font-weight:600;font-size:0.9rem;text-decoration:none;}.btn:focus{outline:2px solid #60a5fa;outline-offset:2px;}@media (prefers-color-scheme: light){body{background:#f4f6fb;color:#16213a;}.box{background:#fff;border-color:#d9e0ec;box-shadow:0 25px 50px -12px rgba(22,33,58,0.25);}p{color:#5b6478;}}@media (prefers-reduced-motion: reduce){*{transition:none!important;}}</style></head><body><div class="box"><h1>This link cannot be relayed</h1><p>Only an http or https authorization URL can be relayed, and this link did not contain one. Nothing was sent to any machine.</p><p>Go back to the OAuth login page and click the Tantu bookmark again, or open the dashboard and use the manual forwarder there.</p><a class="btn" href="/">Open the dashboard</a></div></body></html>`))
	}
	if err := browser.ValidateURL(sanitized); err != nil {
		renderErr()
		return
	}
	origin := ""
	if u, err := url.Parse(sanitized); err == nil {
		origin = u.Hostname()
	}
	if origin == "" {
		renderErr()
		return
	}
	notice := ""
	if expiredLink {
		notice = `<p class="note">This saved link already expired — confirm below to continue. Your bookmark still works; nothing needs re-saving.</p>`
	}
	page := relayInterstitialHTML
	page = strings.ReplaceAll(page, "{{ORIGIN}}", escapeHTMLText(origin))
	page = strings.ReplaceAll(page, "{{NOTICE}}", notice)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(page))
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
			// The bookmark carries no credential: opening it renders a
			// confirmation interstitial, and the relay itself still needs an
			// explicit click plus a valid dashboard session at POST time.
			// Nothing here expires, so a saved bookmark keeps working.
			bookmarkletJS = fmt.Sprintf("javascript:void(window.open('http://%s/relay?url='+encodeURIComponent(location.href),'_blank','width=550,height=380'))", webAddr)
		}
		// `unsafe-hashes` is limited to the exact generated bookmarklet
		// navigation; ordinary inline scripts still require the per-response
		// nonce, and all DOM actions are delegated from that script.
		// img-src allows blob: because the send preview stages the chosen local
		// File as a page-created object URL. A blob: URL is same-origin and
		// scoped to this document's lifetime, so it adds no remote origin and
		// no cross-document read; without it the clipboard-image confirmation
		// card renders an invisible, permanently broken thumbnail and logs a
		// CSP violation on every paste, drop, and file-picker selection.
		w.Header().Set("Content-Security-Policy", fmt.Sprintf("default-src 'self'; script-src 'self' 'nonce-%s' 'unsafe-hashes' %s; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data: blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'", nonce, javascriptCSPHashes(bookmarkletJS)))

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
					fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>✅ Auth Relayed</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;-webkit-font-smoothing:antialiased;}.box{background:#131722;border:1px solid #232a3b;border-radius:16px;padding:2rem;max-width:460px;width:92%%;text-align:center;box-shadow:0 25px 50px -12px rgba(0,0,0,0.5);}.icon{font-size:2.5rem;margin-bottom:0.75rem;}h2{color:#34d399;margin:0 0 0.5rem 0;letter-spacing:-0.01em;}p{color:#8b949e;font-size:0.9rem;margin:0 0 1rem 0;}.hint{color:#64748b;font-size:0.8rem;}@media (prefers-color-scheme: light){body{background:#f4f6fb;color:#16213a;}.box{background:#ffffff;border-color:#d9e0ec;box-shadow:0 25px 50px -12px rgba(22,33,58,0.25);}p{color:#5b6478;}.hint{color:#7b8499;}}</style></head><body><div class="box"><div class="icon">✅</div><h2>Authentication Relayed!</h2><p>%s</p><div class="hint">This window will close automatically in 3 seconds...</div></div><script>setTimeout(function(){window.close();},3000);</script></body></html>`, escapeHTMLText(resp.Message))
				} else {
					fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>❌ Relay Error</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;-webkit-font-smoothing:antialiased;}.box{background:#131722;border:1px solid #232a3b;border-radius:16px;padding:2rem;max-width:460px;width:92%%;text-align:center;box-shadow:0 25px 50px -12px rgba(0,0,0,0.5);}.icon{font-size:2.5rem;margin-bottom:0.75rem;}h2{color:#f87171;margin:0 0 0.5rem 0;letter-spacing:-0.01em;}p{color:#8b949e;font-size:0.9rem;margin:0 0 1rem 0;}.hint{color:#64748b;font-size:0.8rem;}@media (prefers-color-scheme: light){body{background:#f4f6fb;color:#16213a;}.box{background:#ffffff;border-color:#d9e0ec;box-shadow:0 25px 50px -12px rgba(22,33,58,0.25);}p{color:#5b6478;}.hint{color:#7b8499;}}</style></head><body><div class="box"><div class="icon">❌</div><h2>Relay Error</h2><p>%s</p><div class="hint">Check terminal logs or verify your peer is connected.</div></div></body></html>`, escapeHTMLText(resp.Message))
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
		// session cookie as relay authorization: a cross-site top-level
		// navigation must not be able to turn that cookie into a relay
		// capability. Authenticated dashboard mutations use POST
		// /api/relay/open. Requests without an explicit credential render a
		// confirmation interstitial instead of relaying: the shell authorizes
		// nothing, and the relay itself still requires an explicit click plus
		// a valid session at POST time. A previously-ticketed bookmark whose
		// ticket is gone degrades to the same page instead of dead-ending.
		ipcToken := h.currentIPCToken()
		relayAuthorized := ipcToken != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get(IPCTokenHeader)), []byte(ipcToken)) == 1
		if !relayAuthorized {
			providedRelayToken := r.URL.Query().Get("token")
			relayToken := h.currentRelayToken()
			relayAuthorized = relayToken != "" && subtle.ConstantTimeCompare([]byte(providedRelayToken), []byte(relayToken)) == 1
		}
		if !relayAuthorized {
			h.renderRelayInterstitial(w, rawURL, strings.TrimSpace(r.URL.Query().Get("ticket")) != "")
			return
		}

		attemptID, err := h.relayOAuth(r.Context(), "", rawURL)
		if err != nil {
			dest, _, _ := h.resolveOperationDestination("")
			respond(http.StatusBadGateway, relayResponse{
				Status:      "error",
				Message:     publicRelayError(err),
				OperationID: attemptID,
				Destination: dest,
				NextAction:  h.relayAttemptNextAction(attemptID),
			})
			return
		}

		dest, _, _ := h.resolveOperationDestination("")
		respond(http.StatusOK, relayResponse{
			Status:      "success",
			Message:     "Authentication completed successfully",
			OperationID: attemptID,
			Destination: dest,
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

		attemptID, err := h.relayOAuth(r.Context(), req.Peer, rawURL)
		if err != nil {
			dest, _, _ := h.resolveOperationDestination(req.Peer)
			writeJSON(w, http.StatusBadGateway, relayResponse{
				Status:      "error",
				Message:     publicRelayError(err),
				OperationID: attemptID,
				Destination: dest,
				NextAction:  h.relayAttemptNextAction(attemptID),
			})
			return
		}

		dest, _, _ := h.resolveOperationDestination(req.Peer)
		writeJSON(w, http.StatusOK, relayResponse{
			Status:      "success",
			Message:     "Authentication completed successfully",
			OperationID: attemptID,
			Destination: dest,
		})
	})

	// 5a. GET/POST /api/relay/active — list in-flight sign-ins, and cancel one.
	//
	// This is the endpoint that makes an abandoned sign-in recoverable. Without
	// it, walking away from a relay left the peer holding a bound loopback
	// callback port (normally the application's fixed redirect port) for the
	// full timeout, which blocked the retry - so restarting the Hub was the only
	// recovery. GET exists so the dashboard can show what is open rather than
	// leaving the user to guess.
	mux.HandleFunc("/api/relay/active", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.Method {
		case http.MethodGet:
			active := h.ActiveRelays()
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "success",
				"active": active,
				"count":  len(active),
			})
		case http.MethodPost:
			var req struct {
				OperationID string `json:"operation_id"`
			}
			releaseBody := boundControlBody(w, r, 8*1024)
			defer releaseBody()
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, relayResponse{Status: "error", Message: "invalid json body"})
				return
			}
			result, err := h.CancelRelay(r.Context(), req.OperationID)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, relayResponse{
					Status:      "error",
					Message:     publicRelayError(err),
					OperationID: req.OperationID,
				})
				return
			}
			result.OperationID = req.OperationID
			writeJSON(w, http.StatusOK, map[string]any{
				"status":  "success",
				"message": result.Message,
				"result":  result,
			})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, relayResponse{
				Status: "error", Message: "method not allowed, use GET or POST",
			})
		}
	})

	// 5b. GET/DELETE /api/relay/recent — Sender-side authorization truth
	// (durable: last 50, 30 days; origin hosts only, never URLs, codes, or
	// tokens). DELETE clears the ledger and its file; transfer history and
	// received files are unaffected.
	mux.HandleFunc("/api/relay/recent", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			removed := h.clearRelayHistory()
			writeJSON(w, http.StatusOK, map[string]any{
				"status":  "success",
				"message": "Authorization history cleared",
				"removed": removed,
			})
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		ops := h.listRelayAttempts()
		if ops == nil {
			ops = []RelayAttempt{}
		}
		writeJSON(w, http.StatusOK, ops)
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
				writeTransferError(w, http.StatusServiceUnavailable, "too many concurrent uploads, retry later", "hub_busy", "Hub is busy. No data was sent.", "too many concurrent uploads", true, false, true, "Wait a moment and retry. If it persists, try again later.", "", "", "", "")
				return
			}
			defer releaseSlot(h.textSlots)
			var textReq struct {
				Text           string `json:"text"`
				Name           string `json:"name"`
				Peer           string `json:"peer"`
				IdempotencyKey string `json:"idempotency_key"`
			}
			r.Body = http.MaxBytesReader(w, r.Body, 11*1024*1024)
			if err := json.NewDecoder(r.Body).Decode(&textReq); err != nil {
				writeTransferError(w, http.StatusBadRequest, "invalid json payload", "invalid_input", "Request was not valid JSON. Nothing was sent.", "invalid json payload", false, false, true, "Fix the request and retry.", "", "", "", "")
				return
			}
			if textReq.Text == "" {
				writeTransferError(w, http.StatusBadRequest, "empty text payload", "invalid_input", "Text was empty. Nothing was sent.", "empty text payload", false, false, true, "Type or paste text, then send again.", "", "", "", "")
				return
			}
			if int64(len(textReq.Text)) > drop.DefaultMaxTextSize {
				writeTransferError(w, http.StatusRequestEntityTooLarge, "text payload exceeds 10 MiB limit", "limit_exceeded", "Text exceeds the 10 MiB limit. Nothing was sent.", "text payload exceeds 10 MiB limit", false, false, true, "Send it as a file instead.", "", "", "", "")
				return
			}
			if textReq.IdempotencyKey != "" && !drop.ValidTombstoneKey(textReq.IdempotencyKey) {
				writeTransferError(w, http.StatusBadRequest, "invalid idempotency key", "invalid_input", "The idempotency key was not a valid identifier. Nothing was sent.", "invalid idempotency key", false, false, true, "Use 1-128 identifier characters (letters, digits, -, _, .) or omit it.", "", "", "", "")
				return
			}

			opID := newOperationID()
			dropID := newOutboundDropID()
			opCreated := time.Now()
			destName, destFP, destAddr := h.resolveOperationDestination(textReq.Peer)
			opKind := "text"
			opName := textReq.Name
			opSize := int64(len(textReq.Text))
			conn, err := h.DialPeer(textReq.Peer)
			if err != nil {
				code, plain, nextAction, retrySafe, duplicateRisk, dataSafe := ClassifyTransferError(err, 0, opSize, false)
				opState := operationStateForFailure(retrySafe, duplicateRisk, false)
				h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: opState, CreatedAt: opCreated, UpdatedAt: time.Now(), RetrySafe: retrySafe, DuplicateRisk: duplicateRisk, DataSafe: dataSafe, ErrorCode: code, ErrorMessage: plain, NextAction: nextAction, DiagnosticID: opID})
				writeTransferError(w, http.StatusBadGateway, fmt.Sprintf("dial peer failed: %v", err), code, plain, err.Error(), retrySafe, duplicateRisk, dataSafe, nextAction, opID, destName, destFP, destAddr)
				return
			}
			defer conn.Close()

			// A caller-supplied key makes retried sends duplicate-safe; it was
			// validated above. Otherwise the operation ID is the key.
			wireKey := opID
			if textReq.IdempotencyKey != "" {
				wireKey = textReq.IdempotencyKey
			}
			meta := drop.DropSend{
				DropID:         dropID,
				IdempotencyKey: wireKey,
				Kind:           drop.DropKindText,
				Name:           textReq.Name,
				Size:           int64(len(textReq.Text)),
			}
			sendCtx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
			defer cancel()

			negotiated := false
			suppressedDuplicate := false
			sendCfg := drop.SendDropConfig{Timeout: h.cfg.Timeout}
			sendCfg.OnAck = func(drop.DropAck) { negotiated = true }
			sendCfg.OnComplete = func(c drop.DropComplete) { suppressedDuplicate = c.Duplicate }
			if err = drop.SendDrop(sendCtx, conn, meta, strings.NewReader(textReq.Text), sendCfg); err != nil {
				bytesForClassify := int64(0)
				if negotiated {
					bytesForClassify = opSize
				}
				preNegotiationCancel := !negotiated && (r.Context().Err() != nil || sendCtx.Err() != nil)
				code, plain, nextAction, retrySafe, duplicateRisk, dataSafe := ClassifyTransferError(err, bytesForClassify, opSize, preNegotiationCancel)
				cancelled := preNegotiationCancel
				opState := operationStateForFailure(retrySafe, duplicateRisk, cancelled)
				// A failed send is exactly what a user opens Live Logs to
				// diagnose. The classification is logged rather than the raw
				// error, so the console shows the same plain language the
				// dashboard and CLI show, and never a raw transport string.
				level := LevelWarn
				if !retrySafe || duplicateRisk {
					level = LevelError
				}
				h.logger.Log(level, DomainDrop, fmt.Sprintf("Send to %s failed (%s): %s", destName, code, plain), nil)
				h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: opState, CreatedAt: opCreated, UpdatedAt: time.Now(), RetrySafe: retrySafe, DuplicateRisk: duplicateRisk, DataSafe: dataSafe, ErrorCode: code, ErrorMessage: plain, NextAction: nextAction, DiagnosticID: opID})
				writeTransferError(w, http.StatusInternalServerError, fmt.Sprintf("send drop failed: %v", err), code, plain, err.Error(), retrySafe, duplicateRisk, dataSafe, nextAction, opID, destName, destFP, destAddr)
				return
			}

			if suppressedDuplicate {
				h.logger.Warn(DomainDrop, fmt.Sprintf("Duplicate text drop suppressed on peer for %s: already delivered by this idempotency key", opName))
				h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: OperationStateDuplicateSuppressed, CreatedAt: opCreated, UpdatedAt: time.Now(), BytesSent: opSize, Verified: true, RetrySafe: false, DuplicateRisk: true, DataSafe: true, NextAction: "Already saved on this peer from an earlier send. Nothing new was written.", DiagnosticID: opID})
				writeJSON(w, http.StatusOK, map[string]any{
					"status":       "success",
					"message":      "Already saved on this peer; duplicate delivery suppressed",
					"duplicate":    true,
					"size":         len(textReq.Text),
					"operation_id": opID,
					"destination":  destName,
					"verified":     true,
				})
				return
			}

			// Logged for the same reason as the file path below: the Live Logs tab
			// is a user-facing surface, and a working send that leaves no trace
			// makes that surface look broken.
			h.logger.Action(DomainDrop, fmt.Sprintf("Sent text (%s) to %s · verified", formatBytes(opSize), destName))
			h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: OperationStateCompleted, CreatedAt: opCreated, UpdatedAt: time.Now(), BytesSent: opSize, Verified: true, RetrySafe: false, DuplicateRisk: false, DataSafe: true, NextAction: "", DiagnosticID: opID})
			writeJSON(w, http.StatusOK, map[string]any{
				"status":       "success",
				"message":      "Text drop sent successfully",
				"size":         len(textReq.Text),
				"operation_id": opID,
				"destination":  destName,
				"verified":     true,
			})
			return
		}

		// Multipart file drop (up to 5GB). Slot acquisition comes before any
		// body buffering so rejected uploads cost nothing.
		if !h.acquireUploadSlot() {
			w.Header().Set("Retry-After", "30")
			writeTransferError(w, http.StatusServiceUnavailable, "too many concurrent uploads, retry later", "hub_busy", "Hub is busy. No data was sent.", "too many concurrent uploads", true, false, true, "Wait a moment and retry. If it persists, try again later.", "", "", "", "")
			return
		}
		defer h.releaseUploadSlot()
		r.Body = http.MaxBytesReader(w, r.Body, drop.DefaultMaxDropSize)
		if err := r.ParseMultipartForm(32 * 1024 * 1024); err != nil {
			writeTransferError(w, http.StatusBadRequest, fmt.Sprintf("multipart parse error (max 5GB): %v", err), "invalid_input", "Upload could not be read. Nothing was sent.", fmt.Sprintf("multipart parse error: %v", err), false, false, true, "Check the file and retry.", "", "", "", "")
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()

		file, header, err := r.FormFile("file")
		if err != nil {
			writeTransferError(w, http.StatusBadRequest, "missing file field in form", "invalid_input", "No file was attached. Nothing was sent.", "missing file field in form", false, false, true, "Choose a file, then send again.", "", "", "", "")
			return
		}
		defer file.Close()

		targetPeer := r.FormValue("peer")
		formKey := strings.TrimSpace(r.FormValue("idempotency_key"))
		if formKey != "" && !drop.ValidTombstoneKey(formKey) {
			writeTransferError(w, http.StatusBadRequest, "invalid idempotency key", "invalid_input", "The idempotency key was not a valid identifier. Nothing was sent.", "invalid idempotency key", false, false, true, "Use 1-128 identifier characters (letters, digits, -, _, .) or omit it.", "", "", "", "")
			return
		}
		opID := newOperationID()
		dropID := newOutboundDropID()
		opCreated := time.Now()
		destName, destFP, destAddr := h.resolveOperationDestination(targetPeer)
		opKind := "file"
		opName := filepath.Base(header.Filename)
		opSize := header.Size
		conn, err := h.DialPeer(targetPeer)
		if err != nil {
			code, plain, nextAction, retrySafe, duplicateRisk, dataSafe := ClassifyTransferError(err, 0, opSize, false)
			opState := operationStateForFailure(retrySafe, duplicateRisk, false)
			h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: opState, CreatedAt: opCreated, UpdatedAt: time.Now(), RetrySafe: retrySafe, DuplicateRisk: duplicateRisk, DataSafe: dataSafe, ErrorCode: code, ErrorMessage: plain, NextAction: nextAction, DiagnosticID: opID})
			writeTransferError(w, http.StatusBadGateway, fmt.Sprintf("dial peer failed: %v", err), code, plain, err.Error(), retrySafe, duplicateRisk, dataSafe, nextAction, opID, destName, destFP, destAddr)
			return
		}
		defer conn.Close()

		fileName := filepath.Base(header.Filename)
		mimeType := mime.TypeByExtension(filepath.Ext(fileName))
		wireKey := opID
		if formKey != "" {
			wireKey = formKey
		}
		meta := drop.DropSend{
			DropID:         dropID,
			IdempotencyKey: wireKey,
			Kind:           drop.DropKindFile,
			Name:           fileName,
			Size:           header.Size,
			MIMEType:       mimeType,
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

		negotiated := false
		suppressedDuplicate := false
		sendCfg := drop.SendDropConfig{Timeout: h.cfg.Timeout}
		sendCfg.OnAck = func(drop.DropAck) { negotiated = true }
		sendCfg.OnComplete = func(c drop.DropComplete) { suppressedDuplicate = c.Duplicate }
		if err = drop.SendDrop(sendCtx, conn, meta, file, sendCfg); err != nil {
			bytesForClassify := int64(0)
			if negotiated {
				bytesForClassify = opSize
			}
			preNegotiationCancel := !negotiated && (r.Context().Err() != nil || sendCtx.Err() != nil)
			code, plain, nextAction, retrySafe, duplicateRisk, dataSafe := ClassifyTransferError(err, bytesForClassify, opSize, preNegotiationCancel)
			cancelled := preNegotiationCancel
			opState := operationStateForFailure(retrySafe, duplicateRisk, cancelled)
			// See the text path above: the classification is logged rather
			// than the raw error, so Live Logs speaks the same plain language
			// as the dashboard and the CLI.
			level := LevelWarn
			if !retrySafe || duplicateRisk {
				level = LevelError
			}
			h.logger.Log(level, DomainDrop, fmt.Sprintf("Send to %s failed (%s): %s", destName, code, plain), nil)
			h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, MIMEType: mimeType, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: opState, CreatedAt: opCreated, UpdatedAt: time.Now(), RetrySafe: retrySafe, DuplicateRisk: duplicateRisk, DataSafe: dataSafe, ErrorCode: code, ErrorMessage: plain, NextAction: nextAction, DiagnosticID: opID})
			writeTransferError(w, http.StatusInternalServerError, fmt.Sprintf("drop file transfer failed: %v", err), code, plain, err.Error(), retrySafe, duplicateRisk, dataSafe, nextAction, opID, destName, destFP, destAddr)
			return
		}

		if suppressedDuplicate {
			// The receiver recognised this idempotency key from a previous
			// completion and wrote nothing. Saying "completed" would tell the
			// user a second copy exists on the peer.
			h.logger.Warn(DomainDrop, fmt.Sprintf("Duplicate suppressed on peer for %s: already delivered by this idempotency key", opName))
			h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, MIMEType: mimeType, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: OperationStateDuplicateSuppressed, CreatedAt: opCreated, UpdatedAt: time.Now(), BytesSent: opSize, Verified: true, RetrySafe: false, DuplicateRisk: true, DataSafe: true, NextAction: "Already saved on this peer from an earlier send of the same file. Nothing new was written.", DiagnosticID: opID})
			writeJSON(w, http.StatusOK, map[string]any{
				"status":       "success",
				"message":      "Already saved on this peer; duplicate delivery suppressed",
				"duplicate":    true,
				"name":         fileName,
				"size":         header.Size,
				"operation_id": opID,
				"destination":  destName,
				"verified":     true,
			})
			return
		}

		// The Live Logs tab is a user-facing surface, so the ordinary success
		// has to appear in it. Without this the console stayed empty through a
		// working send and the feature looked broken. Failures are logged for
		// the same reason: the ring buffer is the only place a user can see
		// what the Hub actually did after the fact.
		h.logger.Action(DomainDrop, fmt.Sprintf("Sent file %q (%s) to %s · verified", fileName, formatBytes(opSize), destName))
		h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, MIMEType: mimeType, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: OperationStateCompleted, CreatedAt: opCreated, UpdatedAt: time.Now(), BytesSent: opSize, Verified: true, RetrySafe: false, DuplicateRisk: false, DataSafe: true, DiagnosticID: opID})
		writeJSON(w, http.StatusOK, map[string]any{
			"status":       "success",
			"message":      "File drop sent successfully",
			"name":         fileName,
			"size":         header.Size,
			"operation_id": opID,
			"destination":  destName,
			"verified":     true,
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

	// 9a. GET/DELETE /api/transfers/recent — Sender-side operation truth
	// (durable: last 50, 30 days; metadata only, never payload). DELETE
	// clears the ledger and its file; received files are unaffected.
	mux.HandleFunc("/api/transfers/recent", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			removed := h.clearOutboundOperations()
			writeJSON(w, http.StatusOK, map[string]any{
				"status":  "success",
				"message": "Transfer history cleared",
				"removed": removed,
			})
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		ops := h.listOutboundOperations()
		if ops == nil {
			ops = []OperationRecord{}
		}
		writeJSON(w, http.StatusOK, ops)
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
			discovered = buildDiscoveredPeers(engine.ListNodes(), h.store)
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
