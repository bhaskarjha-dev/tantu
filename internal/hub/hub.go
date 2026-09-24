package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/discovery"
	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// Default addresses and ports for the Hub.
const (
	DefaultListenPort = "9877"
	DefaultWebPort    = "9876"
	DefaultWebAddr    = "127.0.0.1:9876"
	DefaultLANListen  = "0.0.0.0:9877"
	DefaultLoopListen = "127.0.0.1:9877"
)

// HubVersion is the version string reported in runtime metadata and dashboard.
var HubVersion = "1.0.0"

// SetHubVersion dynamically configures the Hub's reported version string.
func SetHubVersion(v string) {
	if v != "" {
		HubVersion = strings.TrimPrefix(v, "v")
	}
}

// HubConfig configures the Unified Symmetric Hub.
type HubConfig struct {
	Name                      string             // Node instance name for LAN discovery (default: hostname)
	TransportType             string             // "lan", "loopback", "ssh" (empty = smart auto-detect)
	ListenAddr                string             // P2P listener address (default: 0.0.0.0:9877 for LAN, 127.0.0.1:9877 for loopback)
	WebAddr                   string             // Web/IPC listener address (default: 127.0.0.1:9876)
	StoreDir                  string             // Directory for storing identity and peers
	OutputDir                 string             // Directory to save received QuickDrop files (default: ".")
	Headless                  bool               // Run in headless mode without opening browser
	Server                    bool               // Alias for Headless
	Timeout                   time.Duration      // Timeout for sessions and transfers (default: 5m)
	Verbose                   bool               // Enable detailed logging
	SSHHostKey                string             // Path to SSH host private key (when TransportType == "ssh")
	SSHAuthorizedKeys         [][]byte           // Public/client keys authorized to use the SSH Hub
	SSHAllowLegacyHostKeyAuth bool               // Explicit compatibility mode: authorize the host key for SSH clients
	AutoAcceptPairing         bool               // Automatically accept incoming pairings without interactive confirmation
	OpenBrowser               func(string) error // Optional hook for opening browser (defaults to browser.OpenURL)
	Logger                    *log.Logger        // Optional logger
}

// ReceivedDropItem represents a received text snippet or file transfer.
type ReceivedDropItem struct {
	ID                string    `json:"id"`
	Timestamp         time.Time `json:"timestamp"`
	Kind              string    `json:"kind"` // "text" or "file"
	Name              string    `json:"name"`
	Size              int64     `json:"size"`
	Content           string    `json:"content,omitempty"`
	SavedPath         string    `json:"saved_path,omitempty"`
	FromPeer          string    `json:"from_peer"`
	SenderFingerprint string    `json:"sender_fingerprint,omitempty"`
	IsURL             bool      `json:"is_url"`
}

// RecentDropsBuffer stores recent drop metadata and text in memory.  The
// byte budget prevents a sequence of large snippets from turning the history
// feature into an unbounded RAM sink.
type RecentDropsBuffer struct {
	mu       sync.RWMutex
	capacity int
	maxBytes int
	bytes    int
	items    []ReceivedDropItem
}

const defaultRecentDropsByteLimit = 50 * 1024 * 1024

func recentDropBytes(item ReceivedDropItem) int {
	return len(item.ID) + len(item.Name) + len(item.Content) + len(item.SavedPath) + len(item.FromPeer) + len(item.SenderFingerprint)
}

// NewRecentDropsBuffer creates a new buffer with the given capacity.
func NewRecentDropsBuffer(capacity int) *RecentDropsBuffer {
	if capacity <= 0 {
		capacity = 50
	}
	return &RecentDropsBuffer{
		capacity: capacity,
		maxBytes: defaultRecentDropsByteLimit,
		items:    make([]ReceivedDropItem, 0, capacity),
	}
}

// Add appends an item, retaining at most capacity items and a bounded total
// amount of text/metadata (oldest evicted first).
func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func (b *RecentDropsBuffer) Add(item ReceivedDropItem) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.maxBytes <= 0 {
		return
	}
	// A single pathological item must not evict the entire history or remain
	// larger than the byte budget. Truncate every metadata field, not just the
	// text body, because peer names and paths are attacker-controlled too.
	if recentDropBytes(item) > b.maxBytes {
		remaining := b.maxBytes
		item.Content = truncateUTF8(item.Content, remaining)
		remaining -= len(item.Content)
		item.Name = truncateUTF8(item.Name, remaining)
		remaining -= len(item.Name)
		item.ID = truncateUTF8(item.ID, remaining)
		remaining -= len(item.ID)
		item.SavedPath = truncateUTF8(item.SavedPath, remaining)
		remaining -= len(item.SavedPath)
		item.FromPeer = truncateUTF8(item.FromPeer, remaining)
		remaining -= len(item.FromPeer)
		item.SenderFingerprint = truncateUTF8(item.SenderFingerprint, remaining)
	}
	itemSize := recentDropBytes(item)
	for len(b.items) > 0 && (len(b.items) >= b.capacity || b.bytes+itemSize > b.maxBytes) {
		b.bytes -= recentDropBytes(b.items[0])
		b.items = b.items[1:]
	}
	b.items = append(b.items, item)
	b.bytes += itemSize
}

// GetAll returns all stored items in chronological order.
func (b *RecentDropsBuffer) GetAll() []ReceivedDropItem {
	b.mu.RLock()
	defer b.mu.RUnlock()
	res := make([]ReceivedDropItem, len(b.items))
	copy(res, b.items)
	return res
}

// Count returns the number of items currently stored.
func (b *RecentDropsBuffer) Count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.items)
}

// PendingPairing represents an inbound pairing request awaiting user confirmation.
type PendingPairing struct {
	ID              string    `json:"id"`
	RemoteAddr      string    `json:"remote_addr"`
	PeerSAS         string    `json:"peer_sas"`
	LocalSAS        string    `json:"local_sas"`
	PeerFingerprint string    `json:"peer_fingerprint"`
	PeerName        string    `json:"peer_name"`
	CreatedAt       time.Time `json:"created_at"`
	// decisionCh is closed exactly once when the request reaches a terminal
	// decision. decision is read under pendingPairingsMu after the channel is
	// closed, avoiding the send/timeout race of a buffered decision channel.
	decisionCh chan struct{}
	decision   bool
	resolved   bool
}

// Hub orchestrates P2P wire multiplexing and local Web/IPC services.
type Hub struct {
	cfg                     HubConfig
	store                   *pairing.PeerStore
	identity                *pairing.Identity
	tr                      transport.Transport
	p2pLn                   transport.Listener
	httpServer              *http.Server
	actualP2P               net.Addr
	actualWeb               string
	startTime               time.Time
	logger                  *EventLogger
	recentDrops             *RecentDropsBuffer
	relayCoordinator        *relayCoordinator
	relayToken              string
	ipcToken                string
	dashboardBootstrapToken string
	dashboardMu             sync.Mutex
	dashboardSessions       map[string]time.Time
	relayTickets            map[string]time.Time
	// uploadSlots bounds concurrent multipart file uploads (up to 5 GiB
	// each, 32 MiB parsed in RAM plus temp-disk spill). Without it any
	// capability holder could exhaust disk/RAM with parallel uploads.
	uploadSlots chan struct{}
	// textSlots bounds concurrent JSON text uploads; pairSlots bounds
	// concurrent outbound pairing attempts. Same rationale, smaller units.
	textSlots chan struct{}
	pairSlots chan struct{}
	onSnippetReceived       func(ReceivedDropItem)
	openBrowser             func(string) error
	readyCh                 chan struct{}
	stopCh                  chan struct{}
	runCancel               context.CancelFunc
	runContext              context.Context
	runDone                 chan struct{}
	mu                      sync.RWMutex
	running                 bool
	started                 bool
	// startErr records a startup failure. Ready is closed on failure too
	// (so waiters never hang), therefore callers must check Err after
	// Ready fires: a closed Ready channel alone does not mean success.
	startErr                error
	activePeer              string
	discoveryEngine         *discovery.Engine
	discoveryCancel         context.CancelFunc
	pendingPairingsMu       sync.Mutex
	pendingPairings         map[string]*PendingPairing
	roamingProbesMu         sync.Mutex
	roamingProbes           map[string]time.Time
}

// maxHubConcurrentUploads bounds simultaneous multipart file uploads through
// POST /api/drop/upload. Each upload may buffer up to 32 MiB in RAM and spill
// up to 5 GiB to temp disk, so unbounded parallelism lets any capability
// holder exhaust local resources. Excess uploads fail fast with 503 instead
// of queueing behind multi-gigabyte transfers.
const maxHubConcurrentUploads = 4

// maxHubConcurrentTextUploads bounds simultaneous JSON text uploads. Each may
// hold up to 11 MiB plus a wire connection; without a cap an authenticated
// caller could exhaust memory with parallel text drops.
const maxHubConcurrentTextUploads = 16

// maxHubConcurrentPairings bounds simultaneous outbound pairing attempts.
// Each may block for minutes on TCP+TLS dials and human SAS approval.
const maxHubConcurrentPairings = 4

// acquireUploadSlot takes one multipart-upload slot without blocking. A nil
// slot channel (zero-value Hub outside NewHub) imposes no limit.
func (h *Hub) acquireUploadSlot() bool {
	return acquireSlot(h.uploadSlots)
}

func (h *Hub) releaseUploadSlot() {
	releaseSlot(h.uploadSlots)
}

// acquireSlot takes one token from a bounded semaphore channel without
// blocking. A nil channel (zero-value Hub outside NewHub) imposes no limit.
func acquireSlot(slots chan struct{}) bool {
	if slots == nil {
		return true
	}
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseSlot(slots chan struct{}) {
	if slots == nil {
		return
	}
	select {
	case <-slots:
	default:
	}
}

// SanitizeDropFilename cleanses an untrusted incoming filename to prevent directory traversal,
// Windows DOS device conflicts, NTFS alternate data streams, and control character injection.
func SanitizeDropFilename(name string) string {
	// Normalize path separators to forward slash
	name = strings.ReplaceAll(name, "\\", "/")

	// Take the basename after the last slash
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}

	// Strip NTFS alternate data streams and drive letters (strip colon and anything after)
	if idx := strings.Index(name, ":"); idx >= 0 {
		name = name[:idx]
	}

	// Remove ASCII control characters (0-31 and 127)
	var sb strings.Builder
	for _, r := range name {
		if r < 32 || r == 127 {
			continue
		}
		sb.WriteRune(r)
	}
	name = sb.String()

	// Trim leading/trailing dots and spaces (Windows disallows trailing dots and spaces)
	name = strings.Trim(name, " .")

	if name == "" || name == "." || name == ".." {
		return "drop.bin"
	}

	// Protect against Windows reserved DOS device names (CON, PRN, AUX, NUL, COM1-9, LPT1-9)
	base := name
	if dotIdx := strings.Index(name, "."); dotIdx >= 0 {
		base = name[:dotIdx]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "drop_" + name
	}

	return name
}

// DefaultTransport determines the smart default transport.
// Always defaults to "lan" so the Hub listens on 0.0.0.0:9877 for LAN discovery and pairing.
func DefaultTransport(store *pairing.PeerStore) string {
	return "lan"
}

// EnsurePeerLANPort normalizes a peer address by ensuring a port is present.
// If addr already specifies a port, it is returned unchanged.
// Otherwise, DefaultLANPort (9877) is appended.
func EnsurePeerLANPort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(addr, transport.DefaultLANPort)
}

// ensureStagingDir verifies the staging directory is a real directory, not a
// symlink planted to redirect partial-file writes outside the output dir.
// MkdirAll follows symlinks, so the check must come after creation.
func ensureStagingDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect staging directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("staging path is not a directory: %s", dir)
	}
	return nil
}

// stagingHardlinked reports whether fi has more than one hard link. It is
// implemented per-OS: Unix exposes the link count, other platforms cannot,
// so the resume-path check degrades to a no-op there (documented at the call
// site).
func stagingHardlinked(fi os.FileInfo) bool {
	return stagingHardlinkedOS(fi)
}

// retryStagingOpen reports that a staging partial could not be opened for
// resume (usually raced away). The caller must fall back to exclusive
// creation, which surfaces persistent errors. It is distinct from a hard
// failure such as a symlink swap, which must abort the transfer.
type retryStagingOpen struct{ err error }

func (e *retryStagingOpen) Error() string {
	if e == nil || e.err == nil {
		return "staging partial unavailable for resume"
	}
	return "staging partial unavailable for resume: " + e.err.Error()
}

func (e *retryStagingOpen) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// openResumePart opens a previously Lstat-inspected staging partial for
// resume. The inspected descriptor must come from os.Lstat immediately
// beforehand; the opened file is verified to be the same regular file, so a
// symlink swap between inspection and open fails closed instead of truncating
// an unrelated victim file.
func openResumePart(partPath string, inspected os.FileInfo) (*os.File, error) {
	f, err := os.OpenFile(partPath, os.O_RDWR, 0600)
	if err != nil {
		return nil, &retryStagingOpen{err: err}
	}
	finfo, statErr := f.Stat()
	if statErr != nil || !finfo.Mode().IsRegular() || !os.SameFile(inspected, finfo) {
		_ = f.Close()
		if statErr != nil {
			return nil, fmt.Errorf("inspect partial file: %w", statErr)
		}
		return nil, fmt.Errorf("partial file changed during open: %s", partPath)
	}
	// A hardlink to a victim file passes the identity check above (same
	// inode); never resume into a multi-linked file. Fresh creation below
	// already unlinks planted entries instead of following them.
	if stagingHardlinked(finfo) {
		_ = f.Close()
		return nil, fmt.Errorf("partial file has multiple links: %s", partPath)
	}
	return f, nil
}

// createStagingPart exclusively creates a staging partial, never truncating
// through a possibly-swapped path. On EEXIST the conflicting entry is removed
// without following it (os.Remove never follows the final path element, so a
// planted symlink is unlinked rather than traversed) and the file is created
// exactly once more; any further race fails closed instead of truncating a
// victim file.
func createStagingPart(partPath string) (*os.File, error) {
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if os.IsExist(err) {
		if rmErr := os.Remove(partPath); rmErr == nil {
			f, err = os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		} else {
			err = fmt.Errorf("remove conflicting partial path: %w", rmErr)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("create part file: %w", err)
	}
	if info, statErr := f.Stat(); statErr != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if statErr == nil {
			statErr = errors.New("partial path is not a regular file")
		}
		return nil, fmt.Errorf("inspect created partial file: %w", statErr)
	}
	return f, nil
}

func sameConfiguredEndpoint(a, b string) bool {
	ah, ap, aerr := net.SplitHostPort(strings.TrimSpace(a))
	bh, bp, berr := net.SplitHostPort(strings.TrimSpace(b))
	if aerr != nil || berr != nil {
		return false
	}
	ah = strings.ToLower(strings.Trim(ah, "[]"))
	bh = strings.ToLower(strings.Trim(bh, "[]"))
	if ah == "localhost" {
		ah = "127.0.0.1"
	}
	if bh == "localhost" {
		bh = "127.0.0.1"
	}
	return ah == bh && ap == bp
}

// findAvailableWebPort attempts to bind a TCP listener on 127.0.0.1 starting at startPort.
// If occupied, it tries up to maxAttempts decrementing ports (e.g. 9876 -> 9875 -> 9874 -> 9873).
// If startPort is 0, it asks the OS for an ephemeral free port.
func findAvailableWebPort(startPort int, maxAttempts int) (net.Listener, int, error) {
	if startPort == 0 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, 0, err
		}
		tcpAddr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			return ln, 0, nil
		}
		return ln, tcpAddr.Port, nil
	}

	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		port := startPort - i
		if port <= 0 {
			break
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln, port, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("failed to bind web port after %d attempts (starting at %d): %w", maxAttempts, startPort, lastErr)
}

// NewHub initializes a Hub with the provided configuration.
func NewHub(cfg HubConfig) (*Hub, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.OutputDir == "" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			cfg.OutputDir = filepath.Join(home, "Downloads", "tantu")
		} else {
			cfg.OutputDir = "downloads"
		}
	}
	if cfg.WebAddr == "" {
		cfg.WebAddr = DefaultWebAddr
	} else {
		// Strict loopback check: ensure host binds to 127.0.0.1 for security
		host, port, err := net.SplitHostPort(cfg.WebAddr)
		if err != nil {
			port = strings.TrimPrefix(cfg.WebAddr, ":")
			cfg.WebAddr = net.JoinHostPort("127.0.0.1", port)
		} else if host != "127.0.0.1" && host != "localhost" {
			cfg.WebAddr = net.JoinHostPort("127.0.0.1", port)
		}
	}
	if cfg.Headless || cfg.Server {
		cfg.Headless = true
	}

	ob := cfg.OpenBrowser
	if ob == nil {
		ob = browser.OpenURL
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate relay token: %w", err)
	}
	ipcTokenBytes := make([]byte, 32)
	if _, err := rand.Read(ipcTokenBytes); err != nil {
		return nil, fmt.Errorf("generate IPC token: %w", err)
	}
	dashboardBootstrapBytes := make([]byte, 32)
	if _, err := rand.Read(dashboardBootstrapBytes); err != nil {
		return nil, fmt.Errorf("generate dashboard bootstrap token: %w", err)
	}

	return &Hub{
		cfg:                     cfg,
		logger:                  NewEventLogger(DefaultRingBufferSize),
		recentDrops:             NewRecentDropsBuffer(50),
		relayCoordinator:        newRelayCoordinator(),
		relayToken:              hex.EncodeToString(tokenBytes),
		ipcToken:                hex.EncodeToString(ipcTokenBytes),
		dashboardBootstrapToken: hex.EncodeToString(dashboardBootstrapBytes),
		dashboardSessions:       make(map[string]time.Time),
		relayTickets:            make(map[string]time.Time),
		uploadSlots:             make(chan struct{}, maxHubConcurrentUploads),
		textSlots:               make(chan struct{}, maxHubConcurrentTextUploads),
		pairSlots:               make(chan struct{}, maxHubConcurrentPairings),
		openBrowser:             ob,
		readyCh:                 make(chan struct{}),
		stopCh:                  make(chan struct{}),
		pendingPairings:         make(map[string]*PendingPairing),
		roamingProbes:           make(map[string]time.Time),
	}, nil
}

func newPendingPairingID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("pair-%d", time.Now().UnixNano())
	}
	return "pair-" + hex.EncodeToString(raw[:])
}

// ListPendingPairings returns all active inbound pairing requests awaiting confirmation.
func (h *Hub) ListPendingPairings() []*PendingPairing {
	h.pendingPairingsMu.Lock()
	defer h.pendingPairingsMu.Unlock()
	res := make([]*PendingPairing, 0, len(h.pendingPairings))
	for _, p := range h.pendingPairings {
		res = append(res, p)
	}
	return res
}

// ResolvePendingPairing records an approval or rejection for a pending pairing request.
func (h *Hub) ResolvePendingPairing(id string, accept bool) bool {
	h.pendingPairingsMu.Lock()
	defer h.pendingPairingsMu.Unlock()
	p, ok := h.pendingPairings[id]
	if !ok || p.resolved || p.decisionCh == nil {
		return false
	}
	p.decision = accept
	p.resolved = true
	close(p.decisionCh)
	return true
}

func (h *Hub) terminatePendingPairing(p *PendingPairing, decision bool) bool {
	if p == nil {
		return false
	}
	h.pendingPairingsMu.Lock()
	defer h.pendingPairingsMu.Unlock()
	if p.resolved {
		return p.decision
	}
	p.decision = decision
	p.resolved = true
	if p.decisionCh != nil {
		close(p.decisionCh)
	}
	return decision
}

// EnsureIdentity checks if identity.json exists; if absent, generates and persists a new one.
func (h *Hub) EnsureIdentity() (*pairing.Identity, error) {
	if h.store == nil {
		return nil, errors.New("peer store not initialized")
	}
	id, err := h.store.LoadIdentity()
	if err != nil {
		// Never replace an existing identity because its file is temporarily
		// unreadable or corrupt.  Rotating it silently invalidates every paired
		// peer and can turn a recoverable error into a permanent lockout.
		return nil, fmt.Errorf("load identity: %w", err)
	}
	if id == nil {
		id, err = pairing.GenerateIdentity()
		if err != nil {
			return nil, fmt.Errorf("generate identity: %w", err)
		}
		if err := h.store.SaveIdentity(id); err != nil {
			return nil, fmt.Errorf("save identity: %w", err)
		}
	}
	h.mu.Lock()
	h.identity = id
	h.mu.Unlock()
	return id, nil
}

// NormalizePeers rewrites any ephemeral ports in peers.json to DefaultLANPort.
func (h *Hub) NormalizePeers() {
	if h.store == nil {
		return
	}
	for _, p := range h.store.ListPeers() {
		normalized := EnsurePeerLANPort(p.Address)
		if normalized != p.Address {
			p.Address = normalized
			_ = h.store.AddPeer(p)
		}
	}
}

// Ready returns a channel that is closed when the Hub listeners are fully bound and ready.
func (h *Hub) Ready() <-chan struct{} {
	h.mu.Lock()
	// If a previous generation has completed, reserve the next generation's
	// channel before returning. This closes the race where a caller invokes
	// Ready immediately after Stop and before the next Start goroutine has
	// installed its state.
	if h.started && !h.running && h.runDone != nil {
		select {
		case <-h.runDone:
			h.readyCh = make(chan struct{})
		default:
		}
	}
	ch := h.readyCh
	h.mu.Unlock()
	return ch
}

// Err reports a startup failure for the current generation. Because Ready is
// closed on failure as well as success (so waiters never hang), callers must
// check Err after Ready fires before using the Hub.
func (h *Hub) Err() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.startErr
}

// P2PAddr returns the bound P2P listener address.
func (h *Hub) P2PAddr() net.Addr {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.actualP2P
}

// WebAddr returns the bound Web dashboard address.
func (h *Hub) WebAddr() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.actualWeb
}

// DashboardURL returns a browser URL carrying a one-time bootstrap value in
// the URL fragment. The fragment is consumed by the dashboard JavaScript and
// exchanged for an HttpOnly session cookie; the long-lived IPC capability is
// never rendered into the page or placed in a query string/referrer.
func (h *Hub) DashboardURL() string {
	webAddr := h.WebAddr()
	if webAddr == "" {
		return ""
	}

	h.dashboardMu.Lock()
	defer h.dashboardMu.Unlock()
	if h.dashboardBootstrapToken == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return ""
		}
		h.dashboardBootstrapToken = hex.EncodeToString(buf)
	}
	return (&url.URL{
		Scheme:   "http",
		Host:     webAddr,
		Path:     "/",
		Fragment: "tantu_bootstrap=" + h.dashboardBootstrapToken,
	}).String()
}

// resetDashboardSessions invalidates browser sessions and bootstrap values at
// the beginning of a new Hub run. A browser authorized for an old generation
// must not silently control a restarted listener.
func (h *Hub) resetDashboardSessions() {
	h.dashboardMu.Lock()
	h.dashboardSessions = make(map[string]time.Time)
	h.relayTickets = make(map[string]time.Time)
	h.dashboardBootstrapToken = ""
	h.dashboardMu.Unlock()
}

func (h *Hub) newRelayTicket() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	ticket := hex.EncodeToString(buf)
	h.dashboardMu.Lock()
	defer h.dashboardMu.Unlock()
	if h.relayTickets == nil {
		h.relayTickets = make(map[string]time.Time)
	}
	now := time.Now()
	for id, expires := range h.relayTickets {
		if !expires.After(now) {
			delete(h.relayTickets, id)
		}
	}
	const maxRelayTickets = 128
	if len(h.relayTickets) >= maxRelayTickets {
		return ""
	}
	h.relayTickets[ticket] = now.Add(10 * time.Minute)
	return ticket
}

func (h *Hub) consumeRelayTicket(ticket string) bool {
	if ticket == "" {
		return false
	}
	h.dashboardMu.Lock()
	defer h.dashboardMu.Unlock()
	expires, ok := h.relayTickets[ticket]
	if !ok || !expires.After(time.Now()) {
		delete(h.relayTickets, ticket)
		return false
	}
	delete(h.relayTickets, ticket)
	return true
}

func (h *Hub) newDashboardSession() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)
	h.dashboardMu.Lock()
	defer h.dashboardMu.Unlock()
	if h.dashboardSessions == nil {
		h.dashboardSessions = make(map[string]time.Time)
	}
	now := time.Now()
	for id, expires := range h.dashboardSessions {
		if !expires.After(now) {
			delete(h.dashboardSessions, id)
		}
	}
	const maxDashboardSessions = 32
	if len(h.dashboardSessions) >= maxDashboardSessions {
		return "", errors.New("too many dashboard sessions")
	}
	h.dashboardSessions[id] = now.Add(30 * time.Minute)
	return id, nil
}

func (h *Hub) dashboardSessionValid(id string) bool {
	if id == "" {
		return false
	}
	h.dashboardMu.Lock()
	defer h.dashboardMu.Unlock()
	expires, ok := h.dashboardSessions[id]
	if !ok || !expires.After(time.Now()) {
		if ok {
			delete(h.dashboardSessions, id)
		}
		return false
	}
	return true
}

func (h *Hub) withRunContext(parent context.Context) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	h.mu.RLock()
	runCtx := h.runContext
	h.mu.RUnlock()
	if runCtx == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(runCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// EventLogger returns the Hub's structured event logger and ring buffer.
func (h *Hub) EventLogger() *EventLogger {
	return h.logger
}

// Store returns the underlying PeerStore.
func (h *Hub) Store() *pairing.PeerStore {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.store
}

// Identity returns the active local node Identity.
func (h *Hub) Identity() *pairing.Identity {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.identity
}

// GetActivePeer returns the fingerprint of the currently selected active peer, or empty string.
func (h *Hub) GetActivePeer() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.activePeer
}

// SetActivePeer updates the currently active peer by query (name, alias, SAS, fingerprint, or empty to clear).
func (h *Hub) SetActivePeer(query string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if query == "" {
		h.activePeer = ""
		return nil
	}
	if h.store == nil {
		return errors.New("peer store not available")
	}
	p, err := h.store.ResolvePeer(query)
	if err != nil {
		return err
	}
	h.activePeer = p.Fingerprint
	return nil
}

// TransportType returns the resolved transport type ("lan", "loopback", "ssh").
func (h *Hub) TransportType() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg.TransportType
}

// OutputDir returns the configured directory for received files.
func (h *Hub) OutputDir() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg.OutputDir
}

// Timeout returns the configured operation timeout for wire flows.
func (h *Hub) Timeout() time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.cfg.Timeout <= 0 {
		return 5 * time.Minute
	}
	return h.cfg.Timeout
}

// SetOutputDir updates the destination directory for received files.
func (h *Hub) SetOutputDir(dir string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.OutputDir = dir
}

// RecentDrops returns the recent drops circular buffer.
func (h *Hub) RecentDrops() *RecentDropsBuffer {
	return h.recentDrops
}

// SetOnSnippetReceived sets a callback invoked whenever a text snippet is received.
func (h *Hub) SetOnSnippetReceived(fn func(ReceivedDropItem)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onSnippetReceived = fn
}

func shutdownHTTPServer(server *http.Server) {
	if server == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		// Shutdown deliberately leaves hijacked/long-lived handlers (notably
		// SSE) alone. Force-close them so Stop has a bounded, observable
		// lifecycle instead of returning while requests remain active.
		_ = server.Close()
	}
}

// Stop gracefully shuts down the Hub HTTP server, P2P listener, and discovery engine.
func (h *Hub) Stop() error {
	h.mu.Lock()
	if !h.running {
		h.mu.Unlock()
		return nil
	}
	cancel := h.runCancel
	runDone := h.runDone
	discoveryCancel := h.discoveryCancel
	discoveryEngine := h.discoveryEngine
	httpServer := h.httpServer
	p2pLn := h.p2pLn
	h.mu.Unlock()

	// Never hold h.mu while shutting down services. HTTP handlers and
	// discovery callbacks legitimately take that lock, so doing so here can
	// deadlock a running Hub. Keep running=true until the run defer completes;
	// this prevents a concurrent Start from replacing live resources.
	if cancel != nil {
		cancel()
	}
	if discoveryCancel != nil {
		discoveryCancel()
	}
	if discoveryEngine != nil {
		_ = discoveryEngine.Close()
	}
	if p2pLn != nil {
		_ = p2pLn.Close()
	}
	shutdownHTTPServer(httpServer)
	if runDone != nil {
		select {
		case <-runDone:
			return nil
		case <-time.After(5 * time.Second):
			// The run defer is the only path that clears running; returning
			// here without it would wedge the Hub ("already running"
			// forever). Force-close the listeners (idempotent) to unblock
			// the serve loops, then wait briefly once more before
			// reporting the timeout.
			if p2pLn != nil {
				_ = p2pLn.Close()
			}
			if httpServer != nil {
				_ = httpServer.Close()
			}
			select {
			case <-runDone:
				return errors.New("hub stop timed out waiting for run completion")
			case <-time.After(2 * time.Second):
				return errors.New("hub stop timed out waiting for run completion")
			}
		}
	}
	return nil
}

// DiscoveryEngine returns the active LAN discovery engine or nil.
func (h *Hub) DiscoveryEngine() *discovery.Engine {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.discoveryEngine
}

// SetDiscoveryEngine updates or injects a custom discovery engine.
func (h *Hub) SetDiscoveryEngine(e *discovery.Engine) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.discoveryEngine = e
}

// Start launches the Hub P2P listener and Web/IPC server.
func (h *Hub) Start(parent context.Context) (err error) {
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)

	// Do not let a new generation replace channels or listeners while the
	// previous generation is still unwinding. A closed old run is cleared and a
	// live one produces an explicit stopping error.
	var (
		readyCh     chan struct{}
		readyClosed bool
		runDone     chan struct{}
		markReady   func()
	)
	for {
		h.mu.Lock()
		if h.running {
			h.mu.Unlock()
			cancel()
			return errors.New("hub is already running")
		}
		if h.runDone != nil {
			select {
			case <-h.runDone:
				h.runDone = nil
				if h.started {
					select {
					case <-h.readyCh:
						h.readyCh = make(chan struct{})
					default:
					}
				}
				h.mu.Unlock()
				continue
			default:
				h.mu.Unlock()
				cancel()
				return errors.New("hub is stopping")
			}
		}

		if h.started {
			// Reuse the channel reserved by Ready, or create it if Start is
			// the first observer after the previous generation completed.
			select {
			case <-h.readyCh:
				h.readyCh = make(chan struct{})
			default:
			}
			readyCh = h.readyCh
		} else {
			// Preserve the channel returned by Ready before the first Start so
			// callers cannot race with startup and wait on an orphaned channel.
			readyCh = h.readyCh
		}
		readyClosed = false
		markReady = func() {
			if !readyClosed {
				readyClosed = true
				close(readyCh)
			}
		}
		h.running = true
		h.started = true
		h.startErr = nil
		h.readyCh = readyCh
		h.httpServer = nil
		h.p2pLn = nil
		h.runCancel = cancel
		h.runContext = ctx
		h.runDone = make(chan struct{})
		runDone = h.runDone
		h.relayCoordinator = newRelayCoordinator()
		h.mu.Unlock()
		h.resetDashboardSessions()
		break
	}

	defer func() {
		// Only the current generation may clear shared state. In particular,
		// an old deferred cleanup must never close a replacement readyCh or
		// clear a newer discovery engine.
		h.mu.Lock()
		current := h.runDone == runDone
		var discoveryCancel context.CancelFunc
		var discoveryEngine *discovery.Engine
		if current {
			h.running = false
			h.runCancel = nil
			h.runContext = nil
			discoveryCancel = h.discoveryCancel
			h.discoveryCancel = nil
			discoveryEngine = h.discoveryEngine
			h.discoveryEngine = nil
			h.httpServer = nil
			h.p2pLn = nil
			// A startup failure (return before markReady) is recorded so
			// waiters woken by the readyCh close below can distinguish it
			// from success via Err. A graceful shutdown returns nil and
			// leaves any prior value cleared at Start entry.
			if !readyClosed && err != nil {
				h.startErr = err
			}
		} else {
			// A defensive branch for future lifecycle changes: never cancel or
			// clear resources owned by a newer generation.
		}
		h.mu.Unlock()
		if !readyClosed {
			readyClosed = true
			close(readyCh)
		}
		if discoveryCancel != nil {
			discoveryCancel()
		}
		if discoveryEngine != nil {
			_ = discoveryEngine.Close()
		}
		cancel()
		close(runDone)
	}()

	// 1. PeerStore
	storeDir := h.cfg.StoreDir
	if storeDir == "" {
		var err error
		storeDir, err = pairing.DefaultStoreDir()
		if err != nil {
			return fmt.Errorf("default store dir: %w", err)
		}
	}
	store, err := pairing.NewPeerStore(storeDir)
	if err != nil {
		return fmt.Errorf("init peer store: %w", err)
	}
	h.mu.Lock()
	h.store = store
	h.mu.Unlock()

	// 2. Self-healing identity
	id, err := h.EnsureIdentity()
	if err != nil {
		return err
	}

	// 3. Normalize peers
	h.NormalizePeers()

	// 4. Smart Default Transport
	h.mu.RLock()
	transportType := h.cfg.TransportType
	h.mu.RUnlock()
	if transportType == "" {
		transportType = DefaultTransport(h.store)
		h.mu.Lock()
		h.cfg.TransportType = transportType
		h.mu.Unlock()
	}

	// 5. Determine listen address
	listenAddr := h.cfg.ListenAddr
	switch transportType {
	case "lan":
		if listenAddr == "" {
			listenAddr = DefaultLANListen
		}
	case "loopback":
		if listenAddr == "" {
			listenAddr = DefaultLoopListen
		}
	case "ssh":
		if listenAddr == "" {
			listenAddr = DefaultLoopListen
		}
		if h.cfg.SSHHostKey == "" {
			return errors.New("ssh-host-key required for ssh transport")
		}
	default:
		return fmt.Errorf("unknown transport type: %s", transportType)
	}

	// 6. Setup transport
	var tr transport.Transport
	switch transportType {
	case "lan":
		tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
		if err != nil {
			return fmt.Errorf("parse local TLS certificate: %w", err)
		}
		var trustedFPs []string
		// Trust is evaluated live against the peer store on every handshake.
		// A start-time snapshot must NOT be passed here: snapshot OR live
		// semantics would keep a peer trusted at the transport layer after
		// `unpair` removed it, until the next Hub restart. The dispatcher
		// applies the same live check at the application layer.
		tr, err = transport.NewLANTransport(transport.LANTransportConfig{
			Cert:                tlsCert,
			TrustedFingerprints: trustedFPs,
			IsTrusted: func(fp string) bool {
				if h.store == nil {
					return false
				}
				_, ok := h.store.GetPeer(fp)
				return ok
			},
			AllowPairing: true,
		})
		if err != nil {
			return fmt.Errorf("configure lan transport: %w", err)
		}
	case "loopback":
		tr = transport.NewLoopbackTransport()
	case "ssh":
		keyBytes, err := os.ReadFile(h.cfg.SSHHostKey)
		if err != nil {
			return fmt.Errorf("read ssh host key: %w", err)
		}
		tr, err = transport.NewSSHTransport(transport.SSHTransportConfig{
			HostKey:                keyBytes,
			AuthorizedKeys:         h.cfg.SSHAuthorizedKeys,
			AllowLegacyHostKeyAuth: h.cfg.SSHAllowLegacyHostKeyAuth,
		})
		if err != nil {
			return fmt.Errorf("configure ssh transport: %w", err)
		}
	}

	// 7. Start P2P Listener
	h.mu.Lock()
	h.tr = tr
	h.startTime = time.Now()
	h.mu.Unlock()

	p2pLn, err := tr.Listen(listenAddr)
	if err != nil {
		return fmt.Errorf("start p2p listener on %s: %w", listenAddr, err)
	}
	h.mu.Lock()
	h.p2pLn = p2pLn
	h.actualP2P = p2pLn.Addr()
	h.mu.Unlock()

	// 8. Start Web/IPC HTTP Listener with fallback loop
	_, portStr, err := net.SplitHostPort(h.cfg.WebAddr)
	startPort := 9876
	if err == nil {
		if p, convErr := strconv.Atoi(portStr); convErr == nil {
			startPort = p
		}
	}
	maxAttempts := 4
	if startPort == 0 {
		maxAttempts = 1
	}

	webLn, boundPort, err := findAvailableWebPort(startPort, maxAttempts)
	if err != nil {
		_ = p2pLn.Close()
		return fmt.Errorf("start web listener on %s: %w", h.cfg.WebAddr, err)
	}
	h.mu.Lock()
	h.actualWeb = webLn.Addr().String()
	h.mu.Unlock()

	if startPort != 0 && boundPort != startPort {
		h.logger.Action(DomainNet, fmt.Sprintf("Web dashboard bound to fallback port %d (preferred %d was occupied)", boundPort, startPort))
	}

	// 9. Dispatcher for P2P traffic
	var dropMu sync.Mutex
	openedFiles := make(map[string]*os.File)
	reservedDrops := make(map[string]struct{})
	savedPaths := make(map[string]string)
	partPaths := make(map[string]string)
	publishedPaths := make(map[string]string)
	textBuffers := make(map[string]*boundedBuffer)

	var asideFallback, dispatcherFallback io.Writer
	if h.cfg.Logger != nil {
		asideFallback = h.cfg.Logger.Writer()
		dispatcherFallback = h.cfg.Logger.Writer()
	}
	asideLogger := log.New(&eventLogWriter{logger: h.logger, domain: DomainOAuth, fallback: asideFallback}, "", 0)
	dispatcherLogger := log.New(&eventLogWriter{logger: h.logger, domain: DomainNet, fallback: dispatcherFallback}, "", 0)

	localPairingOptions := pairing.PairingOptions{}
	if p2pLn.Addr() != nil {
		_, p2pPortText, splitErr := net.SplitHostPort(p2pLn.Addr().String())
		if splitErr == nil {
			if port, convErr := strconv.Atoi(p2pPortText); convErr == nil {
				localPairingOptions.LocalListenPort = port
			}
		}
	}

	dispatcherCfg := bridge.DispatcherConfig{
		ASideConfig: bridge.ASideConfig{
			Timeout:     h.cfg.Timeout,
			OpenBrowser: h.openBrowser,
			Logger:      asideLogger,
		},
		DropConfig: drop.ReceiveDropConfig{
			Timeout:     h.cfg.Timeout,
			MaxSize:     drop.DefaultMaxDropSize,
			MaxTextSize: 10 * 1024 * 1024,
			BeforeComplete: func(res *drop.ReceiveDropResult) error {
				if res.Meta.Kind != drop.DropKindFile {
					return nil
				}
				dropMu.Lock()
				f := openedFiles[res.Meta.DropID]
				partPath := partPaths[res.Meta.DropID]
				desiredPath := savedPaths[res.Meta.DropID]
				dropMu.Unlock()
				if f != nil {
					if err := f.Sync(); err != nil {
						_ = f.Close()
						return fmt.Errorf("sync completed drop: %w", err)
					}
					if err := f.Close(); err != nil {
						return fmt.Errorf("close completed drop: %w", err)
					}
				}
				if partPath == "" || desiredPath == "" {
					return errors.New("completed file has no staging path")
				}
				published, err := finalizePartFile(partPath, desiredPath)
				if err != nil {
					return fmt.Errorf("publish completed drop: %w", err)
				}
				dropMu.Lock()
				publishedPaths[res.Meta.DropID] = published
				dropMu.Unlock()
				return nil
			},
			OnMeta: func(meta drop.DropSend) (writer io.Writer, err error) {
				dropMu.Lock()
				_, duplicateDrop := openedFiles[meta.DropID]
				_, duplicateText := textBuffers[meta.DropID]
				_, alreadyReserved := reservedDrops[meta.DropID]
				if !duplicateDrop && !duplicateText && !alreadyReserved {
					reservedDrops[meta.DropID] = struct{}{}
				}
				dropMu.Unlock()
				if duplicateDrop || duplicateText || alreadyReserved {
					return nil, fmt.Errorf("duplicate drop_id %q", meta.DropID)
				}
				defer func() {
					if err != nil {
						dropMu.Lock()
						delete(reservedDrops, meta.DropID)
						dropMu.Unlock()
					}
				}()
				if meta.Kind == drop.DropKindFile {
					fileName := SanitizeDropFilename(meta.Name)
					outDir := h.OutputDir()
					if outDir == "" {
						outDir = "."
					}
					if err := os.MkdirAll(outDir, 0700); err != nil {
						return nil, fmt.Errorf("create output directory: %w", err)
					}
					destPath := filepath.Join(outDir, fileName)
					if _, err := os.Lstat(destPath); err == nil {
						ext := filepath.Ext(fileName)
						base := strings.TrimSuffix(fileName, ext)
						idx := 1
						for {
							cand := filepath.Join(outDir, fmt.Sprintf("%s (%d)%s", base, idx, ext))
							_, statErr := os.Lstat(cand)
							if os.IsNotExist(statErr) {
								destPath = cand
								break
							}
							if statErr != nil {
								return nil, fmt.Errorf("inspect destination: %w", statErr)
							}
							idx++
						}
					} else if !os.IsNotExist(err) {
						return nil, fmt.Errorf("inspect destination: %w", err)
					}

					stagingDir := filepath.Join(outDir, ".tantu-staging")
					if err := os.MkdirAll(stagingDir, 0700); err != nil {
						return nil, fmt.Errorf("create transfer staging directory: %w", err)
					}
					if err := ensureStagingDir(stagingDir); err != nil {
						return nil, err
					}
					partPath := filepath.Join(stagingDir, meta.DropID+".part")
					var f *os.File
					var validBytes int64

					if info, err := os.Lstat(partPath); err == nil {
						if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
							return nil, fmt.Errorf("partial path is not a regular file: %s", partPath)
						}
						existingBytes := info.Size()
						chunkSize := int64(meta.ChunkSize)
						if chunkSize <= 0 {
							chunkSize = 1 << 20
						}
						validBytes = (existingBytes / chunkSize) * chunkSize
						if meta.Size > 0 && validBytes > 0 && validBytes < meta.Size && meta.HeadHash != "" {
							resumed, openErr := openResumePart(partPath, info)
							var retry *retryStagingOpen
							switch {
							case openErr == nil:
								f = resumed
								if meta.HeadHash != "" && validBytes >= 64*1024 {
									headBuf := make([]byte, 64*1024)
									if n, rErr := io.ReadFull(f, headBuf); rErr != nil {
										validBytes = 0
									} else {
										hsum := sha256.Sum256(headBuf[:n])
										if !strings.EqualFold(hex.EncodeToString(hsum[:]), meta.HeadHash) {
											validBytes = 0
										}
									}
								}
								if validBytes > 0 {
									if truncateErr := f.Truncate(validBytes); truncateErr != nil {
										_ = f.Close()
										return nil, fmt.Errorf("truncate partial file: %w", truncateErr)
									}
									if _, seekErr := f.Seek(validBytes, io.SeekStart); seekErr != nil {
										_ = f.Close()
										return nil, fmt.Errorf("seek partial file: %w", seekErr)
									}
									pct := int64(0)
									if meta.Size > 0 {
										pct = (validBytes * 100) / meta.Size
									}
									h.logger.Action(DomainDrop, fmt.Sprintf("Resuming %s from %s (%d%%)", fileName, formatBytes(validBytes), pct))
								} else {
									_ = f.Close()
									f = nil
								}
							case errors.As(openErr, &retry):
								validBytes = 0
							default:
								return nil, openErr
							}
						} else {
							validBytes = 0
						}
					} else if !os.IsNotExist(err) {
						return nil, fmt.Errorf("inspect partial file: %w", err)
					}

					if f == nil {
						created, err := createStagingPart(partPath)
						if err != nil {
							return nil, err
						}
						f = created
					}

					dropMu.Lock()
					openedFiles[meta.DropID] = f
					partPaths[meta.DropID] = partPath
					savedPaths[meta.DropID] = destPath
					dropMu.Unlock()
					return f, nil
				}

				// Text Drop: enforce 10MB limit
				const maxTextDropSize = 10 * 1024 * 1024 // 10MB
				if meta.Size > maxTextDropSize {
					return nil, fmt.Errorf("text drop exceeds 10MB limit (%d bytes)", meta.Size)
				}
				buf := &boundedBuffer{limit: maxTextDropSize}
				dropMu.Lock()
				textBuffers[meta.DropID] = buf
				dropMu.Unlock()
				return buf, nil
			},
		},
		OnDropReceived: func(res *drop.ReceiveDropResult) {
			dropMu.Lock()
			if f, ok := openedFiles[res.Meta.DropID]; ok && f != nil {
				_ = f.Close()
				delete(openedFiles, res.Meta.DropID)
			}
			finalSavedPath := publishedPaths[res.Meta.DropID]
			delete(partPaths, res.Meta.DropID)
			delete(savedPaths, res.Meta.DropID)
			delete(publishedPaths, res.Meta.DropID)
			var textContent string
			if buf, ok := textBuffers[res.Meta.DropID]; ok && buf != nil {
				textContent = buf.String()
			}
			dropMu.Unlock()

			if res.Meta.Kind == drop.DropKindFile && res.SHA256 != "" {
				shaShort := res.SHA256
				if len(shaShort) > 12 {
					shaShort = shaShort[:12] + "..."
				}
				h.logger.Action(DomainDrop, fmt.Sprintf("✓ SHA-256 verified (%s)", shaShort))
			}

			isURL := false
			trimmed := strings.TrimSpace(textContent)
			if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
				if _, err := url.ParseRequestURI(trimmed); err == nil {
					isURL = true
				}
			}

			peerName := "Unauthenticated Peer"
			if res.PeerFingerprint != "" {
				if h.store != nil {
					if p, ok := h.store.GetPeer(res.PeerFingerprint); ok {
						if p.Name != "" {
							peerName = p.Name
						} else {
							peerName = "peer-" + pairing.SASCode(p.Fingerprint)
						}
					} else {
						peerName = "peer-" + pairing.SASCode(res.PeerFingerprint)
					}
				} else {
					peerName = "peer-" + pairing.SASCode(res.PeerFingerprint)
				}
			}

			item := ReceivedDropItem{
				ID:                res.Meta.DropID,
				Timestamp:         time.Now(),
				Kind:              string(res.Meta.Kind),
				Name:              res.Meta.Name,
				Size:              res.BytesWritten,
				Content:           textContent,
				SavedPath:         finalSavedPath,
				FromPeer:          peerName,
				SenderFingerprint: res.PeerFingerprint,
				IsURL:             isURL,
			}
			if item.Kind == "" {
				item.Kind = "text"
			}
			h.recentDrops.Add(item)

			if res.Meta.Kind == drop.DropKindFile {
				h.logger.Action(DomainDrop, fmt.Sprintf("QuickDrop received file %q (%d bytes) from %s saved to %s", res.Meta.Name, res.BytesWritten, peerName, finalSavedPath))
			} else {
				h.logger.Action(DomainDrop, fmt.Sprintf("QuickDrop received text (%d bytes) from %s", res.BytesWritten, peerName))
				h.mu.RLock()
				snippetFn := h.onSnippetReceived
				h.mu.RUnlock()
				if snippetFn != nil {
					snippetFn(item)
				}
			}
			if h.cfg.Verbose {
				if res.Meta.Kind == drop.DropKindFile {
					log.Printf("📥 QuickDrop received file %q (%d bytes) from %q", res.Meta.Name, res.BytesWritten, peerName)
				} else {
					log.Printf("📥 QuickDrop received text (%d bytes) from %q", res.BytesWritten, peerName)
				}
			}

			// Dynamic IP self-healing: if incoming authenticated connection originates from a roaming IP
			if res.PeerFingerprint != "" && res.PeerAddress != "" && h.store != nil {
				if p, ok := h.store.GetPeer(res.PeerFingerprint); ok {
					remoteHost, _, err := net.SplitHostPort(res.PeerAddress)
					if err == nil && remoteHost != "" && remoteHost != "127.0.0.1" && remoteHost != "::1" && remoteHost != "localhost" {
						storedHost, storedPort, sErr := net.SplitHostPort(p.Address)
						if sErr != nil {
							storedPort = transport.DefaultLANPort
						}
						if remoteHost != storedHost {
							newAddr := net.JoinHostPort(remoteHost, storedPort)
							_ = h.store.UpdatePeerAddress(p.Fingerprint, newAddr)
							h.logger.Action(DomainPeer, fmt.Sprintf("Dynamic IP self-healed: peer %s address updated from %s to %s", peerName, p.Address, newAddr))
						}
					}
				}
			}
		},
		OnDropDone: func(dropID string, res *drop.ReceiveDropResult, err error) {
			dropMu.Lock()
			if f, ok := openedFiles[dropID]; ok && f != nil {
				_ = f.Close()
				delete(openedFiles, dropID)
			}
			partPath := partPaths[dropID]
			delete(partPaths, dropID)
			delete(savedPaths, dropID)
			delete(textBuffers, dropID)
			delete(publishedPaths, dropID)
			delete(reservedDrops, dropID)
			dropMu.Unlock()

			if err != nil && partPath != "" {
				if info, statErr := os.Lstat(partPath); statErr == nil && info.Mode().IsRegular() && info.Size() == 0 {
					_ = os.Remove(partPath)
				}
			}
		},
		OnASideDone: func(err error) {
			if err != nil && !errors.Is(err, context.Canceled) {
				h.logger.Error(DomainOAuth, fmt.Sprintf("OAuth session error: %v", err))
			} else if err == nil {
				h.logger.Action(DomainOAuth, "OAuth session completed successfully")
			}
			if h.cfg.Verbose && err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("OAuth session error: %v", err)
			}
		},
		IsPeerTrusted: func(fp string) bool {
			h.mu.RLock()
			transportName := h.cfg.TransportType
			store := h.store
			h.mu.RUnlock()
			if transportName == "loopback" {
				return true
			}
			if transportName == "ssh" {
				return fp != ""
			}
			if fp == "" || store == nil {
				return false
			}
			_, ok := store.GetPeer(fp)
			return ok
		},
		OnPairing: func(conn transport.Conn, firstEnv *protocol.Envelope) {
			h.logger.Action(DomainPeer, fmt.Sprintf("Incoming pairing handshake from %s", conn.RemoteAddr()))
			res, err := pairing.HandleInboundPairingWithOptions(ctx, conn, firstEnv, h.store, func(peerSAS, localSAS string) bool {
				if h.cfg.AutoAcceptPairing {
					h.logger.Action(DomainPeer, fmt.Sprintf("Auto-accepted pairing SAS match: %s vs %s", peerSAS, localSAS))
					return true
				}

				peerFP := transport.GetPeerFingerprint(conn)
				var hello protocol.PairHelloPayload
				_ = firstEnv.DecodePayload(&hello)
				peerName := hello.Name
				if peerName == "" {
					peerName = conn.RemoteAddr().String()
				}

				pairID := newPendingPairingID()
				decisionCh := make(chan struct{})

				pending := &PendingPairing{
					ID:              pairID,
					RemoteAddr:      conn.RemoteAddr().String(),
					PeerSAS:         peerSAS,
					LocalSAS:        localSAS,
					PeerFingerprint: peerFP,
					PeerName:        peerName,
					CreatedAt:       time.Now(),
					decisionCh:      decisionCh,
				}

				h.pendingPairingsMu.Lock()
				if len(h.pendingPairings) >= 10 {
					h.pendingPairingsMu.Unlock()
					h.logger.Action(DomainPeer, fmt.Sprintf("⚠️ Inbound pairing from %s rejected: too many pending pairing requests (max 10)", conn.RemoteAddr()))
					return false
				}
				h.pendingPairings[pairID] = pending
				h.pendingPairingsMu.Unlock()

				defer func() {
					h.pendingPairingsMu.Lock()
					delete(h.pendingPairings, pairID)
					h.pendingPairingsMu.Unlock()
				}()

				h.logger.Action(DomainPeer, fmt.Sprintf("🔐 Inbound pairing approval required: %s (SAS: %s, ID: %s)", peerName, peerSAS, pairID))

				select {
				case <-decisionCh:
					h.pendingPairingsMu.Lock()
					accepted := pending.decision
					h.pendingPairingsMu.Unlock()
					if accepted {
						h.logger.Action(DomainPeer, fmt.Sprintf("✅ Pairing approved for %s (SAS: %s)", peerName, peerSAS))
					} else {
						h.logger.Action(DomainPeer, fmt.Sprintf("❌ Pairing rejected for %s (SAS: %s)", peerName, peerSAS))
					}
					return accepted
				case <-time.After(60 * time.Second):
					h.logger.Action(DomainPeer, fmt.Sprintf("⏱️ Pairing request from %s timed out after 60s (rejected)", peerName))
					return h.terminatePendingPairing(pending, false)
				case <-ctx.Done():
					return h.terminatePendingPairing(pending, false)
				}
			}, localPairingOptions)
			if err != nil {
				h.logger.Error(DomainPeer, fmt.Sprintf("Pairing handshake failed: %v", err))
				return
			}
			if res.Accepted {
				peerDesc := res.PeerFingerprint
				if len(peerDesc) > 16 {
					peerDesc = peerDesc[:16] + "..."
				}
				h.logger.Action(DomainPeer, fmt.Sprintf("✅ Successfully paired with peer %s (SAS: %s)", peerDesc, res.PeerSAS))
			}
		},
		Logger: dispatcherLogger,
	}

	dispatcher := bridge.NewDispatcher(p2pLn, dispatcherCfg)

	// 10. HTTP Mux for Web/IPC
	mux := http.NewServeMux()
	h.registerDashboardRoutes(mux)

	httpServer := &http.Server{
		Handler:           h.securityMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 * 1024,
		// Transfers and SSE are intentionally long-lived.  Body size and
		// request contexts provide their bounds; a 30s WriteTimeout would kill
		// valid multi-GB uploads and OAuth sessions.
		WriteTimeout: 0,
		ReadTimeout:  0,
	}
	h.mu.Lock()
	h.httpServer = httpServer
	h.mu.Unlock()

	p2pAddrStr := h.P2PAddr().String()
	_, p2pPortStr, _ := net.SplitHostPort(p2pAddrStr)
	p2pPort, _ := strconv.Atoi(p2pPortStr)

	// 11. Start LAN Discovery Engine only when LAN transport is explicitly in
	// use. Loopback/SSH modes must not broadcast identity metadata on the LAN.
	if transportType == "lan" {
		nodeName := h.cfg.Name
		if nodeName == "" {
			if hn, err := os.Hostname(); err == nil && hn != "" {
				nodeName = hn
			} else {
				nodeName = "tantu-" + pairing.SASCode(id.Fingerprint)
			}
		}

		discCfg := discovery.DiscoveryConfig{
			NodeName:    nodeName,
			WirePort:    p2pPort,
			SAS:         pairing.SASCode(id.Fingerprint),
			Fingerprint: id.Fingerprint,
		}
		discEngine, dErr := discovery.NewEngine(discCfg)
		if dErr != nil {
			h.logger.Action(DomainNet, fmt.Sprintf("Discovery engine warning: %v", dErr))
		} else {
			var (
				discSeenMu sync.Mutex
				discSeen   = make(map[string]time.Time)
			)
			discEngine.OnNodeDiscovered(func(node discovery.DiscoveredNode) {
				h.mu.RLock()
				store := h.store
				h.mu.RUnlock()
				if store == nil {
					return
				}
				p, isPaired := store.GetPeer(node.Fingerprint)
				if !isPaired {
					now := time.Now()
					discSeenMu.Lock()
					lastSeen := discSeen[node.Fingerprint]
					seen := now.Sub(lastSeen) < 15*time.Second
					discSeen[node.Fingerprint] = now
					if len(discSeen) > 256 {
						for fp, seenAt := range discSeen {
							if now.Sub(seenAt) > time.Minute {
								delete(discSeen, fp)
							}
						}
					}
					discSeenMu.Unlock()
					if !seen {
						h.logger.Action(DomainNet, fmt.Sprintf("Nearby hub discovered on LAN: %s at %s (SAS: %s)", node.InstanceName, node.Address, node.SAS))
					}
				} else if p.Address != node.Address {
					go h.verifyAndApplyPeerRoaming(ctx, p.Fingerprint, node.Address, p.DisplayName())
				}
			})

			discCtx, discCancel := context.WithCancel(ctx)
			h.mu.Lock()
			h.discoveryEngine = discEngine
			h.discoveryCancel = discCancel
			h.mu.Unlock()

			go func() {
				if err := discEngine.Start(discCtx); err != nil && !errors.Is(err, context.Canceled) {
					h.logger.Action(DomainNet, fmt.Sprintf("Discovery engine network warning: %v", err))
				}
			}()
		}
	}

	errGroupCh := make(chan error, 2)
	go func() {
		if err := httpServer.Serve(webLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errGroupCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	go func() {
		if err := dispatcher.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errGroupCh <- fmt.Errorf("dispatcher serve: %w", err)
		}
	}()

	// Wait until the HTTP listener is actually accepting requests before
	// publishing readiness or runtime metadata. This closes the small window
	// in which a caller could observe a "ready" Hub that cannot serve a probe.
	healthClient := &http.Client{
		Timeout:   200 * time.Millisecond,
		Transport: &http.Transport{Proxy: nil},
	}
	defer healthClient.CloseIdleConnections()
	healthURL := "http://" + h.WebAddr() + "/healthz"
	ready := false
	for attempt := 0; attempt < 40; attempt++ {
		if ctx.Err() != nil {
			shutdownHTTPServer(httpServer)
			_ = p2pLn.Close()
			return nil
		}
		resp, probeErr := healthClient.Get(healthURL)
		if probeErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		shutdownHTTPServer(httpServer)
		_ = p2pLn.Close()
		return errors.New("web listener did not become ready")
	}

	runtimeInfo := RuntimeHubInfo{
		PID:       os.Getpid(),
		StartedAt: h.startTime,
		WebAddr:   h.WebAddr(),
		WebPort:   boundPort,
		P2PAddr:   p2pAddrStr,
		P2PPort:   p2pPort,
		Transport: h.cfg.TransportType,
		IPCToken:  h.ipcToken,
	}
	if err := WriteRuntimeInfo(storeDir, runtimeInfo); err != nil {
		shutdownHTTPServer(httpServer)
		_ = p2pLn.Close()
		return fmt.Errorf("publish runtime info: %w", err)
	}
	defer func() { _ = RemoveRuntimeInfoIfOwned(storeDir, runtimeInfo) }()

	// Reclaim staging orphans from aborted transfers. Sender DropIDs are
	// random per attempt, so most leftover .part files can never be resumed
	// and would otherwise accumulate without bound.
	if removed, freed := drop.SweepStalePartials(h.OutputDir(), drop.DefaultStagingMaxAge); removed > 0 {
		h.logger.Action(DomainDrop, fmt.Sprintf("Cleaned %d stale partial file(s) (%s reclaimed)", removed, formatBytes(freed)))
	}

	// Both serving loops are now accepting requests and runtime metadata is
	// published, so callers may safely use the Hub.
	markReady()

	if !h.cfg.Headless && !h.cfg.Server {
		dashboardURL := h.DashboardURL()
		go func(openCtx context.Context, target string) {
			timer := time.NewTimer(50 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-openCtx.Done():
				return
			case <-timer.C:
				_ = h.openBrowser(target)
			}
		}(ctx, dashboardURL)
	}

	select {
	case <-ctx.Done():
		shutdownHTTPServer(httpServer)
		_ = p2pLn.Close()
		return nil
	case err := <-errGroupCh:
		shutdownHTTPServer(httpServer)
		_ = p2pLn.Close()
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
}

// DialPeer dials the specified peer address, or the default/active peer if target is empty.
func (h *Hub) DialPeer(target string) (transport.Conn, error) {
	h.mu.RLock()
	tr := h.tr
	store := h.store
	active := h.activePeer
	transportName := h.cfg.TransportType
	configuredListen := h.cfg.ListenAddr
	h.mu.RUnlock()

	if tr == nil {
		return nil, errors.New("transport not initialized")
	}

	dialTarget := target
	if transportName == "lan" {
		if store == nil {
			return nil, errors.New("peer store not available")
		}
		query := target
		if query == "" && active != "" {
			query = active
		}
		resolved, err := store.ResolvePeer(query)
		if err != nil {
			return nil, fmt.Errorf("peer target is not a trusted paired peer: %w", err)
		}
		dialTarget = EnsurePeerLANPort(resolved.Address)
		return transport.DialPinned(tr, dialTarget, resolved.Fingerprint)
	} else {
		actual := h.P2PAddr()
		configured := configuredListen
		query := target
		if query == "" && active != "" {
			query = active
		}
		if query != "" && store != nil {
			resolved, err := store.ResolvePeer(query)
			if err != nil {
				return nil, fmt.Errorf("peer target is not a trusted paired peer: %w", err)
			}
			dialTarget = resolved.Address
			if transportName == "loopback" {
				dialTarget = EnsurePeerLANPort(dialTarget)
			}
			return tr.Dial(dialTarget)
		}
		if dialTarget == "" {
			if actual != nil {
				dialTarget = actual.String()
			} else if configured != "" {
				dialTarget = configured
			} else {
				dialTarget = "127.0.0.1:9877"
			}
		} else {
			// If no trusted store entry matched, never pass an arbitrary
			// HTTP/JSON target through as a local proxy; raw targets may select
			// only the configured listener endpoint.
			allowed := ""
			if actual != nil {
				allowed = actual.String()
			} else if configured != "" {
				allowed = configured
			}
			if allowed == "" || !sameConfiguredEndpoint(dialTarget, allowed) {
				return nil, errors.New("peer target is not the configured local endpoint")
			}
			dialTarget = allowed
		}
	}
	return tr.Dial(dialTarget)
}

type eventLogWriter struct {
	logger   *EventLogger
	domain   EventDomain
	fallback io.Writer
}

func (w *eventLogWriter) Write(p []byte) (n int, err error) {
	rawMsg := strings.TrimSpace(string(p))
	if w.fallback != nil {
		_, _ = w.fallback.Write([]byte(redactLogText(rawMsg) + "\n"))
	}
	if w.logger == nil {
		return len(p), nil
	}
	if rawMsg == "" {
		return len(p), nil
	}
	msg := redactLogText(rawMsg)
	level := LevelInfo
	domain := w.domain
	if domain == "" {
		domain = DomainOAuth
	}

	meta := make(map[string]string)
	for _, word := range strings.Fields(rawMsg) {
		clean := strings.TrimRight(word, ",.;)]}")
		if strings.HasPrefix(clean, "http://") || strings.HasPrefix(clean, "https://") {
			meta["url"] = ShortenURL(clean, 120)
			break
		}
	}

	switch {
	case strings.Contains(rawMsg, "[DEBUG]") || strings.Contains(rawMsg, "routing connection"):
		level = LevelDebug
		domain = DomainNet
	case strings.Contains(rawMsg, "❌") || strings.Contains(rawMsg, "Error") || strings.Contains(rawMsg, "error") || strings.Contains(rawMsg, "failed"):
		level = LevelError
	case strings.Contains(rawMsg, "⚠️"):
		level = LevelWarn
	case strings.Contains(rawMsg, "✅") || strings.Contains(rawMsg, "completed successfully") || strings.Contains(rawMsg, "Session started"):
		level = LevelAction
	}

	w.logger.Log(level, domain, msg, meta)
	return len(p), nil
}

type boundedBuffer struct {
	buf   bytes.Buffer
	limit int64
}

func (b *boundedBuffer) Write(p []byte) (n int, err error) {
	if int64(b.buf.Len()+len(p)) > b.limit {
		return 0, errors.New("text drop exceeds 10MB safety limit")
	}
	return b.buf.Write(p)
}

func (b *boundedBuffer) String() string {
	return b.buf.String()
}

// finalizePartFile publishes a completed .part file without ever deleting an
// existing destination.  Hard-linking gives us an atomic no-overwrite publish
// on the same filesystem; the rename fallback is retained for filesystems that
// do not support links.
func finalizePartFile(partPath, desiredPath string) (string, error) {
	ext := filepath.Ext(desiredPath)
	base := strings.TrimSuffix(desiredPath, ext)
	for i := 0; i < 10000; i++ {
		candidate := desiredPath
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		if _, err := os.Lstat(candidate); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if err := os.Link(partPath, candidate); err == nil {
			if removeErr := os.Remove(partPath); removeErr != nil {
				// The published candidate is still valid; report its path so
				// callers do not report a false transfer failure merely because
				// cleanup of the duplicate inode failed.
				return candidate, nil
			}
			return candidate, nil
		} else if !errors.Is(err, os.ErrExist) {
			// Hard links are unavailable on some filesystems. Fall back to an
			// exclusive copy rather than os.Rename, which can overwrite a
			// destination created by another process on Unix.
			out, openErr := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if os.IsExist(openErr) {
				continue
			}
			if openErr != nil {
				return "", openErr
			}
			in, openErr := os.Open(partPath)
			if openErr != nil {
				_ = out.Close()
				_ = os.Remove(candidate)
				return "", openErr
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
			if removeErr := os.Remove(partPath); removeErr != nil {
				return candidate, nil
			}
			return candidate, nil
		}
	}
	return "", errors.New("too many existing file candidates")
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func (h *Hub) verifyAndApplyPeerRoaming(ctx context.Context, expectedFP, candidateAddr, peerName string) {
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.RLock()
	tr := h.tr
	store := h.store
	h.mu.RUnlock()
	if tr == nil || store == nil {
		return
	}

	now := time.Now()
	h.roamingProbesMu.Lock()
	if last, ok := h.roamingProbes[expectedFP]; ok && now.Sub(last) < 30*time.Second {
		h.roamingProbesMu.Unlock()
		return
	}
	h.roamingProbes[expectedFP] = now
	if len(h.roamingProbes) > 256 {
		for fp, seen := range h.roamingProbes {
			if now.Sub(seen) > 10*time.Minute {
				delete(h.roamingProbes, fp)
			}
		}
	}
	h.roamingProbesMu.Unlock()

	conn, err := transport.DialPinnedContext(ctx, tr, candidateAddr, expectedFP)
	if err != nil {
		if ctx.Err() == nil {
			h.logger.Action(DomainNet, fmt.Sprintf("Roaming probe to %s for peer '%s' failed mTLS verification: %v", candidateAddr, peerName, err))
		}
		return
	}
	defer conn.Close()

	remoteFP := transport.GetPeerFingerprint(conn)
	if ctx.Err() != nil {
		return
	}
	if strings.EqualFold(remoteFP, expectedFP) {
		_ = store.UpdatePeerAddress(expectedFP, candidateAddr)
		h.logger.Action(DomainPeer, fmt.Sprintf("✅ Authenticated roaming update: peer '%s' verified at %s via mTLS", peerName, candidateAddr))
	} else {
		h.logger.Action(DomainNet, fmt.Sprintf("⚠️ Roaming probe to %s presented fingerprint %s (expected %s) — update rejected", candidateAddr, remoteFP, expectedFP))
	}
}
