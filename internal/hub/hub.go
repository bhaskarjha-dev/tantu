package hub

import (
	"bytes"
	"context"
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
	Name          string             // Node instance name for LAN discovery (default: hostname)
	TransportType string             // "lan", "loopback", "ssh" (empty = smart auto-detect)
	ListenAddr    string             // P2P listener address (default: 0.0.0.0:9877 for LAN, 127.0.0.1:9877 for loopback)
	WebAddr       string             // Web/IPC listener address (default: 127.0.0.1:9876)
	StoreDir      string             // Directory for storing identity and peers
	OutputDir     string             // Directory to save received QuickDrop files (default: ".")
	Headless      bool               // Run in headless mode without opening browser
	Server        bool               // Alias for Headless
	Timeout       time.Duration      // Timeout for sessions and transfers (default: 5m)
	Verbose       bool               // Enable detailed logging
	SSHHostKey        string             // Path to SSH host private key (when TransportType == "ssh")
	AutoAcceptPairing bool               // Automatically accept incoming pairings without interactive confirmation
	OpenBrowser       func(string) error // Optional hook for opening browser (defaults to browser.OpenURL)
	Logger            *log.Logger        // Optional logger
}

// ReceivedDropItem represents a received text snippet or file transfer.
type ReceivedDropItem struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Kind      string    `json:"kind"` // "text" or "file"
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Content   string    `json:"content,omitempty"`
	SavedPath         string    `json:"saved_path,omitempty"`
	FromPeer          string    `json:"from_peer"`
	SenderFingerprint string    `json:"sender_fingerprint,omitempty"`
	IsURL             bool      `json:"is_url"`
}

// RecentDropsBuffer stores up to 50 recent drop items in memory.
type RecentDropsBuffer struct {
	mu       sync.RWMutex
	capacity int
	items    []ReceivedDropItem
}

// NewRecentDropsBuffer creates a new buffer with the given capacity.
func NewRecentDropsBuffer(capacity int) *RecentDropsBuffer {
	if capacity <= 0 {
		capacity = 50
	}
	return &RecentDropsBuffer{
		capacity: capacity,
		items:    make([]ReceivedDropItem, 0, capacity),
	}
}

// Add appends an item, retaining at most capacity items (oldest evicted first).
func (b *RecentDropsBuffer) Add(item ReceivedDropItem) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) >= b.capacity {
		b.items = b.items[1:]
	}
	b.items = append(b.items, item)
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
	decisionCh      chan bool
}

// Hub orchestrates P2P wire multiplexing and local Web/IPC services.
type Hub struct {
	cfg               HubConfig
	store             *pairing.PeerStore
	identity          *pairing.Identity
	tr                transport.Transport
	p2pLn             transport.Listener
	httpServer        *http.Server
	actualP2P         net.Addr
	actualWeb         string
	startTime         time.Time
	logger            *EventLogger
	recentDrops       *RecentDropsBuffer
	onSnippetReceived func(ReceivedDropItem)
	openBrowser       func(string) error
	readyCh           chan struct{}
	stopCh            chan struct{}
	mu                sync.RWMutex
	running           bool
	activePeer        string
	discoveryEngine   *discovery.Engine
	discoveryCancel   context.CancelFunc
	pendingPairingsMu sync.Mutex
	pendingPairings   map[string]*PendingPairing
	roamingProbesMu   sync.Mutex
	roamingProbes     map[string]bool
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

	return &Hub{
		cfg:             cfg,
		logger:          NewEventLogger(DefaultRingBufferSize),
		recentDrops:     NewRecentDropsBuffer(50),
		openBrowser:     ob,
		readyCh:         make(chan struct{}),
		stopCh:          make(chan struct{}),
		pendingPairings: make(map[string]*PendingPairing),
		roamingProbes:   make(map[string]bool),
	}, nil
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
	p, ok := h.pendingPairings[id]
	h.pendingPairingsMu.Unlock()
	if !ok {
		return false
	}
	select {
	case p.decisionCh <- accept:
		return true
	default:
		return false
	}
}

// EnsureIdentity checks if identity.json exists; if absent, generates and persists a new one.
func (h *Hub) EnsureIdentity() (*pairing.Identity, error) {
	if h.store == nil {
		return nil, errors.New("peer store not initialized")
	}
	id, err := h.store.LoadIdentity()
	if err != nil || id == nil {
		id, err = pairing.GenerateIdentity()
		if err != nil {
			return nil, fmt.Errorf("generate identity: %w", err)
		}
		if err := h.store.SaveIdentity(id); err != nil {
			return nil, fmt.Errorf("save identity: %w", err)
		}
	}
	h.identity = id
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
	return h.readyCh
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

// Stop gracefully shuts down the Hub HTTP server, P2P listener, and discovery engine.
func (h *Hub) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.running {
		return nil
	}
	h.running = false
	if h.discoveryCancel != nil {
		h.discoveryCancel()
		h.discoveryCancel = nil
	}
	if h.discoveryEngine != nil {
		_ = h.discoveryEngine.Close()
		h.discoveryEngine = nil
	}
	if h.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.httpServer.Shutdown(ctx)
	}
	if h.p2pLn != nil {
		_ = h.p2pLn.Close()
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
func (h *Hub) Start(ctx context.Context) error {
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		return errors.New("hub is already running")
	}
	h.running = true
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		if h.discoveryCancel != nil {
			h.discoveryCancel()
			h.discoveryCancel = nil
		}
		if h.discoveryEngine != nil {
			_ = h.discoveryEngine.Close()
			h.discoveryEngine = nil
		}
		h.running = false
		h.mu.Unlock()
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
	h.store = store

	// 2. Self-healing identity
	id, err := h.EnsureIdentity()
	if err != nil {
		return err
	}

	// 3. Normalize peers
	h.NormalizePeers()

	// 4. Smart Default Transport
	transportType := h.cfg.TransportType
	if transportType == "" {
		transportType = DefaultTransport(h.store)
		h.cfg.TransportType = transportType
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
		for _, p := range h.store.ListPeers() {
			trustedFPs = append(trustedFPs, p.Fingerprint)
		}
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
			HostKey: keyBytes,
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
	savedPaths := make(map[string]string)
	partPaths := make(map[string]string)
	textBuffers := make(map[string]*boundedBuffer)

	var asideFallback, dispatcherFallback io.Writer
	if h.cfg.Logger != nil {
		asideFallback = h.cfg.Logger.Writer()
		dispatcherFallback = h.cfg.Logger.Writer()
	}
	asideLogger := log.New(&eventLogWriter{logger: h.logger, domain: DomainOAuth, fallback: asideFallback}, "", 0)
	dispatcherLogger := log.New(&eventLogWriter{logger: h.logger, domain: DomainNet, fallback: dispatcherFallback}, "", 0)

	dispatcherCfg := bridge.DispatcherConfig{
		ASideConfig: bridge.ASideConfig{
			Timeout:     h.cfg.Timeout,
			OpenBrowser: h.openBrowser,
			Logger:      asideLogger,
		},
		DropConfig: drop.ReceiveDropConfig{
			Timeout: h.cfg.Timeout,
			MaxSize: -1,
			OnMeta: func(meta drop.DropSend) (io.Writer, error) {
				if meta.Kind == drop.DropKindFile {
					fileName := SanitizeDropFilename(meta.Name)
					outDir := h.OutputDir()
					if outDir == "" {
						outDir = "."
					}
					if err := os.MkdirAll(outDir, 0755); err != nil {
						return nil, fmt.Errorf("create output directory: %w", err)
					}
					destPath := filepath.Join(outDir, fileName)
					if _, err := os.Stat(destPath); err == nil {
						ext := filepath.Ext(fileName)
						base := strings.TrimSuffix(fileName, ext)
						idx := 1
						for {
							cand := filepath.Join(outDir, fmt.Sprintf("%s (%d)%s", base, idx, ext))
							if _, err := os.Stat(cand); os.IsNotExist(err) {
								destPath = cand
								break
							}
							idx++
						}
					}

					partPath := destPath + ".part"
					var f *os.File
					var validBytes int64

					if info, err := os.Stat(partPath); err == nil {
						existingBytes := info.Size()
						chunkSize := int64(meta.ChunkSize)
						if chunkSize <= 0 {
							chunkSize = 1 << 20
						}
						validBytes = (existingBytes / chunkSize) * chunkSize
						if meta.Size > 0 && validBytes > 0 && validBytes < meta.Size {
							var openErr error
							f, openErr = os.OpenFile(partPath, os.O_RDWR, 0644)
							if openErr == nil {
								if meta.HeadHash != "" && validBytes >= 64*1024 {
									headBuf := make([]byte, 64*1024)
									if n, rErr := io.ReadFull(f, headBuf); rErr != nil {
										validBytes = 0
									} else {
										hsum := sha256.Sum256(headBuf[:n])
										if hex.EncodeToString(hsum[:]) != meta.HeadHash {
											validBytes = 0
										}
									}
								}
								if validBytes > 0 {
									_ = f.Truncate(validBytes)
									_, _ = f.Seek(validBytes, io.SeekStart)
									pct := int64(0)
									if meta.Size > 0 {
										pct = (validBytes * 100) / meta.Size
									}
									h.logger.Action(DomainDrop, fmt.Sprintf("Resuming %s from %s (%d%%)", fileName, formatBytes(validBytes), pct))
								} else {
									_ = f.Close()
									f = nil
								}
							} else {
								validBytes = 0
							}
						} else {
							validBytes = 0
						}
					}

					if f == nil {
						var err error
						f, err = os.OpenFile(partPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0644)
						if err != nil {
							return nil, fmt.Errorf("create part file: %w", err)
						}
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
			partPath := partPaths[res.Meta.DropID]
			savedPath := savedPaths[res.Meta.DropID]
			delete(partPaths, res.Meta.DropID)
			delete(savedPaths, res.Meta.DropID)
			var textContent string
			if buf, ok := textBuffers[res.Meta.DropID]; ok && buf != nil {
				textContent = buf.String()
			}
			dropMu.Unlock()

			if res.Meta.Kind == drop.DropKindFile && partPath != "" && savedPath != "" {
				if res.SHA256 != "" {
					shaShort := res.SHA256
					if len(shaShort) > 12 {
						shaShort = shaShort[:12] + "..."
					}
					h.logger.Action(DomainDrop, fmt.Sprintf("✓ SHA-256 verified (%s)", shaShort))
				}
				_ = os.Remove(savedPath)
				if err := os.Rename(partPath, savedPath); err != nil {
					h.logger.Action(DomainDrop, fmt.Sprintf("⚠️ Failed to rename .part to %s: %v", savedPath, err))
				}
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
				SavedPath:         savedPath,
				FromPeer:          peerName,
				SenderFingerprint: res.PeerFingerprint,
				IsURL:             isURL,
			}
			if item.Kind == "" {
				item.Kind = "text"
			}
			h.recentDrops.Add(item)

			if res.Meta.Kind == drop.DropKindFile {
				h.logger.Action(DomainDrop, fmt.Sprintf("QuickDrop received file %q (%d bytes) from %s saved to %s", res.Meta.Name, res.BytesWritten, peerName, savedPath))
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
					log.Printf("📥 QuickDrop received file %q (%d bytes) from %s", res.Meta.Name, res.BytesWritten, peerName)
				} else {
					log.Printf("📥 QuickDrop received text (%d bytes) from %s", res.BytesWritten, peerName)
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
			dropMu.Unlock()

			if err != nil && partPath != "" {
				if info, statErr := os.Stat(partPath); statErr == nil && info.Size() == 0 {
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
			if h.cfg.TransportType == "loopback" || h.cfg.TransportType == "ssh" {
				return true
			}
			if fp == "" || h.store == nil {
				return false
			}
			_, ok := h.store.GetPeer(fp)
			return ok
		},
		OnPairing: func(conn transport.Conn, firstEnv *protocol.Envelope) {
			h.logger.Action(DomainPeer, fmt.Sprintf("Incoming pairing handshake from %s", conn.RemoteAddr()))
			res, err := pairing.HandleInboundPairing(ctx, conn, firstEnv, h.store, func(peerSAS, localSAS string) bool {
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

				pairID := fmt.Sprintf("pair-%d", time.Now().UnixNano())
				decisionCh := make(chan bool, 1)

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
				case accepted := <-decisionCh:
					if accepted {
						h.logger.Action(DomainPeer, fmt.Sprintf("✅ Pairing approved for %s (SAS: %s)", peerName, peerSAS))
					} else {
						h.logger.Action(DomainPeer, fmt.Sprintf("❌ Pairing rejected for %s (SAS: %s)", peerName, peerSAS))
					}
					return accepted
				case <-time.After(60 * time.Second):
					h.logger.Action(DomainPeer, fmt.Sprintf("⏱️ Pairing request from %s timed out after 60s (rejected)", peerName))
					return false
				case <-ctx.Done():
					return false
				}
			})
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

	h.httpServer = &http.Server{
		Handler:      h.securityMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Write hub.json runtime info for local daemon discovery
	p2pAddrStr := h.actualP2P.String()
	_, p2pPortStr, _ := net.SplitHostPort(p2pAddrStr)
	p2pPort, _ := strconv.Atoi(p2pPortStr)
	runtimeInfo := RuntimeHubInfo{
		PID:       os.Getpid(),
		StartedAt: h.startTime,
		WebAddr:   h.actualWeb,
		WebPort:   boundPort,
		P2PAddr:   p2pAddrStr,
		P2PPort:   p2pPort,
		Transport: h.cfg.TransportType,
	}
	if err := WriteRuntimeInfo(storeDir, runtimeInfo); err != nil {
		h.logger.Action(DomainNet, fmt.Sprintf("Warning: failed to write hub.json runtime info: %v", err))
	}
	defer func() {
		_ = RemoveRuntimeInfo(storeDir)
	}()

	// 11. Start LAN Discovery Engine
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
			discSeen   = make(map[string]bool)
		)
		discEngine.OnNodeDiscovered(func(node discovery.DiscoveredNode) {
			if h.store == nil {
				return
			}
			p, isPaired := h.store.GetPeer(node.Fingerprint)
			if !isPaired {
				discSeenMu.Lock()
				seen := discSeen[node.Fingerprint]
				discSeen[node.Fingerprint] = true
				discSeenMu.Unlock()
				if !seen {
					h.logger.Action(DomainNet, fmt.Sprintf("Nearby hub discovered on LAN: %s at %s (SAS: %s)", node.InstanceName, node.Address, node.SAS))
				}
			} else {
				if p.Address != node.Address {
					go h.verifyAndApplyPeerRoaming(p.Fingerprint, node.Address, p.DisplayName())
				}
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

	// Close readyCh to notify waiters that listeners are bound and runtime info is ready
	close(h.readyCh)

	// Browser launch if not headless
	if !h.cfg.Headless && !h.cfg.Server {
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = h.openBrowser("http://" + h.actualWeb)
		}()
	}

	errGroupCh := make(chan error, 2)
	go func() {
		if err := h.httpServer.Serve(webLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errGroupCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	go func() {
		if err := dispatcher.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errGroupCh <- fmt.Errorf("dispatcher serve: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.httpServer.Shutdown(shutdownCtx)
		_ = p2pLn.Close()
		return nil
	case err := <-errGroupCh:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.httpServer.Shutdown(shutdownCtx)
		_ = p2pLn.Close()
		return err
	}
}

// DialPeer dials the specified peer address, or the default/active peer if target is empty.
func (h *Hub) DialPeer(target string) (transport.Conn, error) {
	h.mu.RLock()
	tr := h.tr
	store := h.store
	active := h.activePeer
	h.mu.RUnlock()

	if tr == nil {
		return nil, errors.New("transport not initialized")
	}

	dialTarget := target
	if h.cfg.TransportType == "lan" {
		if store == nil {
			return nil, errors.New("peer store not available")
		}
		query := target
		if query == "" && active != "" {
			query = active
		}
		resolved, err := store.ResolvePeer(query)
		if err != nil {
			// If target was an explicit host:port or IP, allow direct dial
			if target != "" && (strings.Contains(target, ":") || net.ParseIP(target) != nil) {
				dialTarget = EnsurePeerLANPort(target)
			} else {
				return nil, err
			}
		} else {
			dialTarget = EnsurePeerLANPort(resolved.Address)
		}
	} else {
		if dialTarget == "" {
			dialTarget = "127.0.0.1:9877"
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
	if w.fallback != nil {
		_, _ = w.fallback.Write(p)
	}
	if w.logger == nil {
		return len(p), nil
	}
	msg := strings.TrimSpace(string(p))
	if msg == "" {
		return len(p), nil
	}
	level := LevelInfo
	domain := w.domain
	if domain == "" {
		domain = DomainOAuth
	}

	meta := make(map[string]string)
	for _, word := range strings.Fields(msg) {
		clean := strings.TrimRight(word, ",.;)]}")
		if strings.HasPrefix(clean, "http://") || strings.HasPrefix(clean, "https://") {
			meta["url"] = clean
			break
		}
	}

	switch {
	case strings.Contains(msg, "[DEBUG]") || strings.Contains(msg, "routing connection"):
		level = LevelDebug
		domain = DomainNet
	case strings.Contains(msg, "❌") || strings.Contains(msg, "Error") || strings.Contains(msg, "error") || strings.Contains(msg, "failed"):
		level = LevelError
	case strings.Contains(msg, "⚠️"):
		level = LevelWarn
	case strings.Contains(msg, "✅") || strings.Contains(msg, "completed successfully") || strings.Contains(msg, "Session started"):
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

func (h *Hub) verifyAndApplyPeerRoaming(expectedFP, candidateAddr, peerName string) {
	if h.tr == nil || h.store == nil {
		return
	}

	h.roamingProbesMu.Lock()
	if h.roamingProbes[expectedFP] {
		h.roamingProbesMu.Unlock()
		return
	}
	h.roamingProbes[expectedFP] = true
	h.roamingProbesMu.Unlock()

	defer func() {
		h.roamingProbesMu.Lock()
		delete(h.roamingProbes, expectedFP)
		h.roamingProbesMu.Unlock()
	}()

	conn, err := h.tr.Dial(candidateAddr)
	if err != nil {
		h.logger.Action(DomainNet, fmt.Sprintf("Roaming probe to %s for peer '%s' failed mTLS verification: %v", candidateAddr, peerName, err))
		return
	}
	defer conn.Close()

	remoteFP := transport.GetPeerFingerprint(conn)
	if strings.EqualFold(remoteFP, expectedFP) {
		_ = h.store.UpdatePeerAddress(expectedFP, candidateAddr)
		h.logger.Action(DomainPeer, fmt.Sprintf("✅ Authenticated roaming update: peer '%s' verified at %s via mTLS", peerName, candidateAddr))
	} else {
		h.logger.Action(DomainNet, fmt.Sprintf("⚠️ Roaming probe to %s presented fingerprint %s (expected %s) — update rejected", candidateAddr, remoteFP, expectedFP))
	}
}



