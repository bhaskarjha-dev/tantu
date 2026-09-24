package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// DefaultBroadcastPort is the fallback UDP broadcast port for LAN peer discovery.
	DefaultBroadcastPort = 9879

	// DefaultMulticastAddr is the RFC 6762 mDNS multicast group.
	DefaultMulticastAddr = "224.0.0.251:5353"

	// DefaultInterval is the periodic beacon transmission interval.
	DefaultInterval = 3 * time.Second

	// DefaultTTL is the inactivity duration after which a discovered peer is expired.
	DefaultTTL = 15 * time.Second

	// BeaconPrefix identifies tantu discovery frames.
	BeaconPrefix = "AUTHDISC:"

	maxDiscoveredNodes = 256
)

// DiscoveredNode represents an active tantu peer found on the local network.
type DiscoveredNode struct {
	InstanceName string    `json:"name"`
	Address      string    `json:"addr"` // IP address or host:port
	Port         int       `json:"port"` // Wire port (e.g. 9877)
	SAS          string    `json:"sas"`  // 6-character SAS code
	Fingerprint  string    `json:"fp"`   // SHA-256 certificate fingerprint
	LastSeen     time.Time `json:"last_seen"`
}

// DiscoveryConfig holds parameters for the discovery Engine.
type DiscoveryConfig struct {
	NodeName      string        `json:"name"`
	WirePort      int           `json:"port"`
	SAS           string        `json:"sas"`
	Fingerprint   string        `json:"fp"`
	Interval      time.Duration // Default: 3s
	TTL           time.Duration // Default: 15s
	BroadcastPort int           // Fallback UDP broadcast port (default: 9879)
}

type beaconPayload struct {
	Version int    `json:"v"`
	Name    string `json:"name"`
	Port    int    `json:"port"`
	SAS     string `json:"sas"`
	FP      string `json:"fp"`
}

// Engine advertises node coordinates and maintains an active subnet registry.
type Engine struct {
	cfg DiscoveryConfig

	startMu     sync.Mutex
	started     bool
	lifecycleMu sync.Mutex
	mu          sync.RWMutex
	nodes       map[string]*DiscoveredNode
	callbacks   []func(DiscoveredNode)
	lastBeacons map[string]time.Time

	udpLn    *net.UDPConn
	mcastLn  *net.UDPConn
	outConn  *net.UDPConn
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	closed   bool
	closeMu  sync.Mutex
	stopOnce sync.Once
}

// NewEngine creates and initializes a LAN discovery Engine with defaults.
func NewEngine(cfg DiscoveryConfig) (*Engine, error) {
	if cfg.BroadcastPort <= 0 {
		cfg.BroadcastPort = DefaultBroadcastPort
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.NodeName == "" {
		cfg.NodeName = "tantu-node"
	}
	if cfg.WirePort == 0 {
		cfg.WirePort = 9877
	}
	if cfg.WirePort < 0 || cfg.WirePort > 65535 {
		return nil, fmt.Errorf("wire port %d is out of range", cfg.WirePort)
	}
	if len(cfg.NodeName) > 128 || containsControl(cfg.NodeName) {
		return nil, errors.New("node name is too long or contains control characters")
	}
	if len(cfg.SAS) > 32 || len(cfg.Fingerprint) > 128 || containsControl(cfg.SAS) || containsControl(cfg.Fingerprint) {
		return nil, errors.New("discovery identity metadata is invalid")
	}

	return &Engine{
		cfg:         cfg,
		nodes:       make(map[string]*DiscoveredNode),
		lastBeacons: make(map[string]time.Time),
	}, nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// OnNodeDiscovered registers a callback triggered whenever a new node is discovered or updated.
func (e *Engine) OnNodeDiscovered(cb func(DiscoveredNode)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.callbacks = append(e.callbacks, cb)
}

// ListNodes returns a slice of currently active discovered nodes.
func (e *Engine) ListNodes() []DiscoveredNode {
	e.mu.RLock()
	defer e.mu.RUnlock()

	now := time.Now()
	res := make([]DiscoveredNode, 0, len(e.nodes))
	for _, n := range e.nodes {
		if now.Sub(n.LastSeen) <= e.cfg.TTL {
			res = append(res, *n)
		}
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Fingerprint == res[j].Fingerprint {
			return res[i].Address < res[j].Address
		}
		return res[i].Fingerprint < res[j].Fingerprint
	})
	return res
}

// Start binds network sockets and begins background advertising and listening.
func (e *Engine) Start(parent context.Context) error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	e.startMu.Lock()
	if e.started {
		e.startMu.Unlock()
		return errors.New("discovery engine is already started")
	}
	e.closeMu.Lock()
	closed := e.closed
	e.closeMu.Unlock()
	if closed {
		e.startMu.Unlock()
		return errors.New("discovery engine is closed")
	}
	e.started = true
	e.startMu.Unlock()

	ctx, cancel := context.WithCancel(parent)
	e.closeMu.Lock()
	e.cancel = cancel
	e.closeMu.Unlock()

	// 1. Bind UDP broadcast listener (port 9879 or configured)
	bcastAddr := &net.UDPAddr{IP: net.IPv4zero, Port: e.cfg.BroadcastPort}
	udpLn, err := net.ListenUDP("udp4", bcastAddr)
	if err != nil {
		cancel()
		e.markStartFailed()
		return fmt.Errorf("listen udp broadcast on %d: %w", e.cfg.BroadcastPort, err)
	}
	e.closeMu.Lock()
	e.udpLn = udpLn
	e.closeMu.Unlock()

	// 2. Best-effort mDNS multicast listener on 224.0.0.251:5353
	mcastAddr, mErr := net.ResolveUDPAddr("udp4", DefaultMulticastAddr)
	if mErr == nil {
		mcastLn, mListenErr := net.ListenMulticastUDP("udp4", nil, mcastAddr)
		if mListenErr == nil {
			e.closeMu.Lock()
			e.mcastLn = mcastLn
			e.closeMu.Unlock()
			e.wg.Add(1)
			go e.listenLoop(mcastLn)
		}
	}

	// 3. Create outbound UDP socket for transmission
	outConn, outErr := net.ListenUDP("udp4", nil)
	if outErr != nil {
		e.closeSockets()
		cancel()
		e.markStartFailed()
		return fmt.Errorf("listen outbound udp: %w", outErr)
	}
	e.closeMu.Lock()
	e.outConn = outConn
	e.closeMu.Unlock()

	// 4. Start listener on primary broadcast socket
	e.wg.Add(1)
	go e.listenLoop(e.udpLn)

	// 5. Start advertising and pruning loop
	e.wg.Add(1)
	go e.advertiseLoop(ctx)

	// Context cancellation must close sockets as well as stop advertising.
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		<-ctx.Done()
		e.closeSockets()
	}()

	return nil
}

func (e *Engine) listenLoop(conn *net.UDPConn) {
	defer e.wg.Done()
	buf := make([]byte, 2048)
	var consecutiveErrs int

	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "use of closed network connection") {
				return
			}
			// Transient UDP errors (notably ICMP ECONNRESET on some
			// platforms, which any LAN host can trigger) must not kill the
			// loop permanently with no signal. Back off briefly and keep
			// listening; only a sustained failure (50 consecutive errors)
			// gives up so a genuinely broken socket cannot hot-spin.
			// Stale nodes age out via TTL in the meantime.
			consecutiveErrs++
			if consecutiveErrs > 50 {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		consecutiveErrs = 0

		if n <= len(BeaconPrefix) {
			continue
		}
		payload := buf[:n]
		if !strings.HasPrefix(string(payload), BeaconPrefix) {
			continue
		}

		jsonBytes := payload[len(BeaconPrefix):]
		var beacon beaconPayload
		if err := json.Unmarshal(jsonBytes, &beacon); err != nil {
			continue
		}

		if !validBeacon(beacon) {
			continue
		}
		// Self-filtering: ignore beacons from our own node (see
		// isSelfBeacon for the exact rules).
		if isSelfBeacon(e.cfg.Fingerprint, e.cfg.SAS, beacon) {
			continue
		}
		if !e.allowBeaconSource(remoteAddr.IP.String()) {
			continue
		}

		nodeAddr := remoteAddr.IP.String()
		node := DiscoveredNode{
			InstanceName: beacon.Name,
			Address:      net.JoinHostPort(nodeAddr, fmt.Sprintf("%d", beacon.Port)),
			Port:         beacon.Port,
			SAS:          beacon.SAS,
			Fingerprint:  beacon.FP,
			LastSeen:     time.Now(),
		}

		e.recordNode(node)
	}
}

// isSelfBeacon reports whether a beacon originates from our own node.
// Fingerprint comparison is case-insensitive (hex) and therefore can never
// hide a legitimate peer. The 24-bit SAS clause applies only to ephemeral
// engines with no fingerprint of their own (e.g. pair-scan initiators);
// otherwise a SAS collision would hide a real peer's beacons.
func isSelfBeacon(ownFP, ownSAS string, beacon beaconPayload) bool {
	if ownFP != "" {
		return strings.EqualFold(beacon.FP, ownFP)
	}
	return ownSAS != "" && strings.EqualFold(beacon.SAS, ownSAS)
}

func validBeacon(beacon beaconPayload) bool {
	if beacon.Version != 1 || beacon.Port <= 0 || beacon.Port > 65535 {
		return false
	}
	if len(beacon.Name) > 128 || containsControl(beacon.Name) {
		return false
	}
	// Beacons are plaintext and trivially spoofable, so the identity fields
	// must at least be well-formed: the fingerprint is hex SHA-256 and the
	// SAS is the 6-hex short form. Malformed beacons are dropped before they
	// can pollute the pairing-selection list.
	if !isHexFingerprint(beacon.FP) || !isSASCode(beacon.SAS) {
		return false
	}
	return true
}

// isHexFingerprint reports whether s is a 64-character hex SHA-256 fingerprint.
func isHexFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// isSASCode reports whether s is a 6-character hex Short Authentication String.
func isSASCode(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func (e *Engine) allowBeaconSource(source string) bool {
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastBeacons == nil {
		e.lastBeacons = make(map[string]time.Time)
	}
	if last, ok := e.lastBeacons[source]; ok && now.Sub(last) < 50*time.Millisecond {
		return false
	}
	e.lastBeacons[source] = now
	// Keep the rate limiter bounded even when an attacker rotates source
	// addresses across a long-lived engine. Time-based GC alone is not
	// enough: a flood of distinct spoofed sources within the 10s window
	// would grow the map without bound, so evict the oldest entry past the
	// cap unconditionally, mirroring recordNode.
	if len(e.lastBeacons) > maxDiscoveredNodes*2 {
		for key, seen := range e.lastBeacons {
			if now.Sub(seen) > 10*time.Second {
				delete(e.lastBeacons, key)
			}
		}
		for len(e.lastBeacons) > maxDiscoveredNodes*2 {
			var oldestKey string
			var oldest time.Time
			for key, seen := range e.lastBeacons {
				if oldestKey == "" || seen.Before(oldest) {
					oldestKey, oldest = key, seen
				}
			}
			if oldestKey == "" {
				break
			}
			delete(e.lastBeacons, oldestKey)
		}
	}
	return true
}

func (e *Engine) recordNode(node DiscoveredNode) {
	e.mu.Lock()
	key := node.Fingerprint
	if key == "" {
		key = node.Address
	}
	existing, isNew := e.nodes[key]
	if !isNew && len(e.nodes) >= maxDiscoveredNodes {
		// Evict the least recently seen hint before accepting a new identity.
		// Discovery is unauthenticated, so this bound is a security boundary.
		var oldestKey string
		var oldest time.Time
		for candidateKey, candidate := range e.nodes {
			if oldestKey == "" || candidate.LastSeen.Before(oldest) {
				oldestKey, oldest = candidateKey, candidate.LastSeen
			}
		}
		if oldestKey != "" {
			delete(e.nodes, oldestKey)
		}
	}
	if !isNew {
		e.nodes[key] = &node
	} else {
		existing.LastSeen = node.LastSeen
		existing.Address = node.Address
		existing.Port = node.Port
		existing.InstanceName = node.InstanceName
		existing.SAS = node.SAS
	}
	cbs := make([]func(DiscoveredNode), len(e.callbacks))
	copy(cbs, e.callbacks)
	e.mu.Unlock()

	e.startMu.Lock()
	started := e.started
	e.startMu.Unlock()
	for _, cb := range cbs {
		if started {
			go func(callback func(DiscoveredNode), value DiscoveredNode) {
				defer func() { _ = recover() }()
				callback(value)
			}(cb, node)
		} else {
			cb(node)
		}
	}
}

func (e *Engine) advertiseLoop(ctx context.Context) {
	defer e.wg.Done()

	// Initial broadcast on start
	e.broadcastBeacon()

	ticker := time.NewTicker(e.cfg.Interval)
	defer ticker.Stop()

	pruneTicker := time.NewTicker(e.cfg.Interval)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.broadcastBeacon()
		case <-pruneTicker.C:
			e.pruneStaleNodes()
		}
	}
}

func (e *Engine) broadcastBeacon() {
	beacon := beaconPayload{
		Version: 1,
		Name:    e.cfg.NodeName,
		Port:    e.cfg.WirePort,
		SAS:     e.cfg.SAS,
		FP:      e.cfg.Fingerprint,
	}
	data, err := json.Marshal(beacon)
	if err != nil {
		return
	}
	packet := append([]byte(BeaconPrefix), data...)

	e.closeMu.Lock()
	conn := e.outConn
	e.closeMu.Unlock()
	if conn == nil {
		return
	}

	// 1. Broadcast to 255.255.255.255:port
	globalBcast := &net.UDPAddr{IP: net.IPv4bcast, Port: e.cfg.BroadcastPort}
	_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	_, _ = conn.WriteToUDP(packet, globalBcast)

	// 2. Broadcast to each interface's directed subnet broadcast address
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, aErr := iface.Addrs()
			if aErr != nil {
				continue
			}
			for _, addr := range addrs {
				ipnet, ok := addr.(*net.IPNet)
				if !ok || ipnet.IP.To4() == nil {
					continue
				}
				ip4 := ipnet.IP.To4()
				mask := ipnet.Mask
				if len(mask) == 4 {
					bcastIP := net.IPv4(
						ip4[0]|^mask[0],
						ip4[1]|^mask[1],
						ip4[2]|^mask[2],
						ip4[3]|^mask[3],
					)
					subnetBcast := &net.UDPAddr{IP: bcastIP, Port: e.cfg.BroadcastPort}
					_, _ = conn.WriteToUDP(packet, subnetBcast)
				}
			}
		}
	}

	// 3. Also multicast to 224.0.0.251:5353 if mDNS is open
	mcastAddr, mErr := net.ResolveUDPAddr("udp4", DefaultMulticastAddr)
	if mErr == nil {
		_, _ = conn.WriteToUDP(packet, mcastAddr)
	}
}

func (e *Engine) pruneStaleNodes() {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	for k, n := range e.nodes {
		if now.Sub(n.LastSeen) > e.cfg.TTL {
			delete(e.nodes, k)
		}
	}
}

func (e *Engine) markStartFailed() {
	e.startMu.Lock()
	e.started = false
	e.startMu.Unlock()
}

func (e *Engine) closeSockets() {
	e.closeMu.Lock()
	if e.udpLn != nil {
		_ = e.udpLn.Close()
		e.udpLn = nil
	}
	if e.mcastLn != nil {
		_ = e.mcastLn.Close()
		e.mcastLn = nil
	}
	if e.outConn != nil {
		_ = e.outConn.Close()
		e.outConn = nil
	}
	e.closeMu.Unlock()
}

// Close gracefully stops the Engine and closes all network sockets.
func (e *Engine) Close() error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	e.stopOnce.Do(func() {
		e.closeMu.Lock()
		e.closed = true
		cancel := e.cancel
		e.closeMu.Unlock()
		if cancel != nil {
			cancel()
		}
		e.closeSockets()
		e.wg.Wait()
	})
	return nil
}
