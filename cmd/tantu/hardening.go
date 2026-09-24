package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
)

const (
	// Keep standalone text drops bounded even when the wire-level quota is
	// configured for files. This matches drop.DefaultMaxTextSize (the Hub's
	// text safety limit); it is duplicated here (not aliased) because the
	// main package also uses it for stdin/argv ingest caps.
	standaloneTextDropLimit int64 = 10 * 1024 * 1024

	maxStandaloneDropHistory      = 128
	maxStandaloneDropHistoryBytes = 64 * 1024 * 1024
	maxStandaloneDropSessions     = 32
	maxLocalRelayActive           = 64

	// One-time relay tickets bound the blast radius of a leaked capability
	// URL (browser history, synced bookmarks, server logs). A ticket is
	// single-use with a short TTL; the per-process token remains as a legacy
	// fallback for previously rendered pages and bookmarklets.
	maxLocalRelayTickets = 128
	localRelayTicketTTL  = 10 * time.Minute

	localHTTPMaxHeaderBytes    = 64 << 10
	localHTTPReadHeaderTimeout = 5 * time.Second
	localHTTPIdleTimeout       = 60 * time.Second
	localHTTPMinReadTimeout    = 15 * time.Minute
)

// unseekableWriter hides seeking (notably *os.File.Seek) from drop
// receivers. The receiver treats a seekable destination's current offset as
// already-received resume bytes; for stdout that offset is unrelated to the
// drop (e.g. a redirected log file's accumulated size) and would wrongly
// reject every text smaller than it.
type unseekableWriter struct{ io.Writer }

// stdoutTextWriter returns stdout as a text-drop destination that can never
// be mistaken for a resumable partial file.
func stdoutTextWriter() io.Writer {
	return unseekableWriter{os.Stdout}
}

// boundedTextBuffer caps retained standalone text drops for the local Web UI.
// File drops stream directly to their destination.
func standaloneDropItemBytes(item dropReceivedItem) int64 {
	return int64(len(item.ID) + len(item.Name) + len(item.Data) + len(item.URL) + len(item.LocalPath))
}

type boundedTextBuffer struct {
	buf   bytes.Buffer
	limit int64
}

func (b *boundedTextBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return 0, fmt.Errorf("text drop exceeds %d-byte safety limit", b.limit)
	}

	remaining := b.limit - int64(b.buf.Len())
	if remaining < 0 || int64(len(p)) > remaining {
		return 0, fmt.Errorf("text drop exceeds %d-byte safety limit", b.limit)
	}
	return b.buf.Write(p)
}

func (b *boundedTextBuffer) String() string {
	return b.buf.String()
}

// sanitizeIncomingFilename deliberately uses the Hub's cross-platform
// sanitizer so every CLI receive path gets the same traversal, ADS, control
// character, and Windows device-name protections.
func sanitizeIncomingFilename(name string) string {
	return hub.SanitizeDropFilename(name)
}

// createIncomingPart opens a private partial file and returns the desired
// final path. Existing final paths (including symlinks/reparse points) are
// treated as collisions, and partial symlinks are rejected rather than
// followed.
func createIncomingPart(outDir, rawName string) (*os.File, string, string, error) {
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return nil, "", "", fmt.Errorf("create output directory: %w", err)
	}
	name := sanitizeIncomingFilename(rawName)
	base := filepath.Join(outDir, name)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	finalPath := base
	for i := 0; i < 10000; i++ {
		if i > 0 {
			finalPath = filepath.Join(outDir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		}
		_, err := os.Lstat(finalPath)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return nil, "", "", fmt.Errorf("inspect destination: %w", err)
		}
		if i == 9999 {
			return nil, "", "", errors.New("too many existing destination candidates")
		}
	}

	stagingDir := filepath.Join(outDir, ".tantu-staging")
	if err := os.MkdirAll(stagingDir, 0700); err != nil {
		return nil, "", "", fmt.Errorf("create transfer staging directory: %w", err)
	}
	if info, err := os.Lstat(stagingDir); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("staging path is not a directory: %s", stagingDir)
		}
		return nil, "", "", fmt.Errorf("inspect staging directory: %w", err)
	}
	var f *os.File
	var partPath string
	for i := 0; i < 10000; i++ {
		candidate := finalPath + ".part"
		if i > 0 {
			candidate = fmt.Sprintf("%s.part-%d", finalPath, i)
		}
		if info, err := os.Lstat(candidate); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil, "", "", fmt.Errorf("partial path is not a regular file: %s", candidate)
			}
			continue
		} else if !os.IsNotExist(err) {
			return nil, "", "", fmt.Errorf("inspect partial file: %w", err)
		}
		var openErr error
		f, openErr = os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if openErr == nil {
			partPath = candidate
			break
		}
		if !os.IsExist(openErr) {
			return nil, "", "", fmt.Errorf("create partial file: %w", openErr)
		}
	}
	if f == nil || partPath == "" {
		return nil, "", "", errors.New("too many existing partial candidates")
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("partial path is not a regular file")
		}
		_ = f.Close()
		_ = os.Remove(partPath)
		return nil, "", "", fmt.Errorf("inspect partial file: %w", err)
	}
	return f, partPath, finalPath, nil
}

// publishIncomingPart publishes a completed partial file without overwriting
// an existing destination. Hard-link publication is preferred; an exclusive
// copy is used on filesystems that do not support links.
// finalizeIncomingPart flushes and closes a completed staging file before it
// is published. Keeping this operation in the receiver's completion hook
// ensures a sender is not told "success" while the final file is still absent
// or only exists under a private .part name.
func finalizeIncomingPart(f *os.File, partPath, desiredPath string) (string, error) {
	if strings.TrimSpace(partPath) == "" || strings.TrimSpace(desiredPath) == "" {
		return "", errors.New("missing staging or destination path")
	}
	if f != nil {
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("sync received file: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close received file: %w", err)
		}
	}
	return publishIncomingPart(partPath, desiredPath)
}

func publishIncomingPart(partPath, desiredPath string) (string, error) {
	ext := filepath.Ext(desiredPath)
	stem := strings.TrimSuffix(desiredPath, ext)
	for i := 0; ; i++ {
		candidate := desiredPath
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		if _, err := os.Lstat(candidate); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if err := os.Link(partPath, candidate); err == nil {
			_ = os.Remove(partPath)
			return candidate, nil
		} else if os.IsExist(err) {
			continue
		}
		out, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		in, err := os.Open(partPath)
		if err != nil {
			_ = out.Close()
			_ = os.Remove(candidate)
			return "", err
		}
		_, copyErr := io.Copy(out, in)
		closeInErr := in.Close()
		syncErr := out.Sync()
		closeOutErr := out.Close()
		if copyErr != nil || closeInErr != nil || syncErr != nil || closeOutErr != nil {
			_ = os.Remove(candidate)
			if copyErr != nil {
				return "", copyErr
			}
			if closeInErr != nil {
				return "", closeInErr
			}
			if syncErr != nil {
				return "", syncErr
			}
			return "", closeOutErr
		}
		_ = os.Remove(partPath)
		return candidate, nil
	}
}

func newLocalRequestToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate local request token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

func escapeHTML(value string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	).Replace(value)
}

func localRequestTokenMatches(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// localTicketPool mints single-use capability tickets for standalone servers,
// mirroring the Hub's relay-ticket model. Tickets are consumed on first use
// and expire quickly, so a ticket captured from browser history, a synced
// bookmark, or a log entry is worthless after use or after the TTL.
type localTicketPool struct {
	mu      sync.Mutex
	tickets map[string]time.Time
}

func newLocalTicketPool() *localTicketPool {
	return &localTicketPool{tickets: make(map[string]time.Time)}
}

// Issue mints a fresh ticket valid for localRelayTicketTTL. The pool is
// bounded; expired entries are reclaimed first, then arbitrary live entries
// when under sustained pressure.
func (p *localTicketPool) Issue() string {
	var ticket [32]byte
	if _, err := rand.Read(ticket[:]); err != nil {
		return ""
	}
	value := hex.EncodeToString(ticket[:])
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for issued, expires := range p.tickets {
		if now.After(expires) {
			delete(p.tickets, issued)
		}
	}
	for len(p.tickets) >= maxLocalRelayTickets {
		for issued := range p.tickets {
			delete(p.tickets, issued)
			break
		}
	}
	p.tickets[value] = now.Add(localRelayTicketTTL)
	return value
}

// Consume validates and burns a ticket. It returns false for empty, unknown,
// expired, or already-used tickets. Comparison is constant-time per candidate
// so no valid ticket is prefix-revealed by timing.
func (p *localTicketPool) Consume(presented string) bool {
	if presented == "" {
		return false
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for issued, expires := range p.tickets {
		if now.After(expires) {
			delete(p.tickets, issued)
			continue
		}
		if subtle.ConstantTimeCompare([]byte(presented), []byte(issued)) == 1 {
			delete(p.tickets, issued)
			return true
		}
	}
	return false
}

func isLoopbackHostname(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
}

func requestPort(hostport string) string {
	if hostport == "" {
		return ""
	}
	if _, port, err := net.SplitHostPort(hostport); err == nil {
		return port
	}
	// Host values without an explicit port are uncommon on a real HTTP/1.x
	// request, but parsing them here keeps the helper useful in unit tests.
	u, err := url.Parse("http://" + hostport)
	if err != nil {
		return ""
	}
	return u.Port()
}

func requestHostIsLoopback(hostport string) bool {
	if hostport == "" {
		return false
	}
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	return isLoopbackHostname(host)
}

func originPortMatches(originPort, requestHost string) bool {
	hostPort := requestPort(requestHost)
	// An empty request port is only expected in synthetic/unit requests. Do
	// not reject those while still enforcing an exact port for real servers.
	if hostPort == "" {
		return true
	}
	if originPort == "" {
		return hostPort == "80"
	}
	return originPort == hostPort
}

func localOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}

	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return isLoopbackHostname(u.Hostname()) && originPortMatches(u.Port(), r.Host)
}

func localRefererAllowed(r *http.Request) bool {
	referer := strings.TrimSpace(r.Header.Get("Referer"))
	if referer == "" {
		return true
	}

	u, err := url.Parse(referer)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return isLoopbackHostname(u.Hostname()) && originPortMatches(u.Port(), r.Host)
}

func requestRemoteIsLoopback(r *http.Request) bool {
	remoteAddr := r.RemoteAddr
	if remoteAddr == "" {
		// httptest and a few in-process callers do not populate RemoteAddr.
		// A real net/http server always does.
		return true
	}

	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// localRequestMiddleware keeps standalone servers usable by their loopback UI
// while rejecting browser-originated requests from arbitrary web pages.
// /relay is intentionally exempt from Referer checking because its bookmarklet
// is a top-level navigation from an OAuth page; that route has its own
// per-process token in runRelay.
func localRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Add("Vary", "Origin")

		origin := strings.TrimSpace(r.Header.Get("Origin"))
		// Opaque-origin top-level bookmarklet navigations are allowed through
		// the middleware only for /relay; that handler still requires its
		// per-process token before doing any work.
		allowOpaqueRelay := r.URL.Path == "/relay" && origin == "null"
		if !requestRemoteIsLoopback(r) || !requestHostIsLoopback(r.Host) || (!localOriginAllowed(r) && !allowOpaqueRelay) {
			http.Error(w, "local-only request rejected", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !localRefererAllowed(r) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}

		if origin != "" && origin != "null" {
			// Never use wildcard CORS. Echo only a validated local origin.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		}
		next.ServeHTTP(w, r)
	})
}

// localRequestTokenMiddleware protects every standalone API route, including
// GETs that can disclose received file content. The embedded UI supplies the
// per-process token; CORS preflights remain exempt so an allowed local origin
// can negotiate the custom header.
func localRequestTokenMiddleware(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodOptions {
			if token == "" || !localRequestTokenMatches(r.Header.Get("X-Tantu-IPC-Token"), token) {
				http.Error(w, "local request capability required", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func newLocalHTTPServer(addr string, handler http.Handler, operationTimeout time.Duration) *http.Server {
	return newLocalHTTPServerWithToken(addr, handler, "", operationTimeout)
}

func newLocalHTTPServerWithToken(addr string, handler http.Handler, token string, operationTimeout time.Duration) *http.Server {
	if operationTimeout <= 0 {
		operationTimeout = 5 * time.Minute
	}
	writeTimeout := operationTimeout + 30*time.Second
	readTimeout := localHTTPMinReadTimeout
	if writeTimeout > readTimeout {
		readTimeout = writeTimeout
	}

	if token != "" {
		handler = localRequestTokenMiddleware(handler, token)
	}
	return &http.Server{
		Addr:              addr,
		Handler:           localRequestMiddleware(handler),
		ReadHeaderTimeout: localHTTPReadHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       localHTTPIdleTimeout,
		MaxHeaderBytes:    localHTTPMaxHeaderBytes,
	}
}

// quoteBROWSERArg quotes one argument for the command-line convention used
// by BROWSER. POSIX consumers get shell-safe single quotes; Windows
// consumers get a quoted CommandLineToArgv-style value.
func quoteBROWSERArg(arg string) string {
	if runtime.GOOS == "windows" {
		if arg == "" {
			return `""`
		}
		return strconv.Quote(arg)
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

func joinBROWSERArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, quoteBROWSERArg(arg))
	}
	return strings.Join(quoted, " ")
}

type localSingleFlight struct {
	mu     sync.Mutex
	active map[string]*localSingleFlightCall
}

type localSingleFlightCall struct {
	done chan struct{}
	err  error
}

func newLocalSingleFlight() *localSingleFlight {
	return &localSingleFlight{active: make(map[string]*localSingleFlightCall)}
}

func (g *localSingleFlight) Do(ctx context.Context, key string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return err
	}
	if call, ok := g.active[key]; ok {
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-call.done:
			return call.err
		}
	}
	if len(g.active) >= maxLocalRelayActive {
		g.mu.Unlock()
		return errors.New("too many active local relay requests")
	}
	call := &localSingleFlightCall{done: make(chan struct{})}
	g.active[key] = call
	g.mu.Unlock()

	call.err = fn()
	g.mu.Lock()
	delete(g.active, key)
	close(call.done)
	g.mu.Unlock()
	return call.err
}
