package discovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
)

const (
	// maxDiscoveredNodes bounds the unauthenticated discovery table.
	maxDiscoveredNodes = 256
	// maxNodesPerSourceHost bounds how many table slots a single source host
	// may occupy. Without it one host can manufacture distinct Address keys by
	// claiming rotating ports until the oldest-first eviction has removed
	// every legitimate peer. Eight comfortably covers several hubs on one
	// machine while keeping a flood from dominating a 256-slot table.
	maxNodesPerSourceHost = 8
)

// DiscoveredNode describes a hub seen on the LAN.
//
// It carries no identity of any kind, and the type makes that impossible to
// violate: there are no SAS or fingerprint fields. They used to be broadcast
// in cleartext every few seconds, which handed any host on the network the
// exact value a user is asked to verify by eye during pairing — an attacker
// who learns it can grind certificates sharing that prefix and then defeat
// the human check entirely. The handshake SAS is derived from the pairing
// transcript and only ever travels inside an authenticated, mTLS-protected
// flow, so a discovered node has no verifiable identity at all until the user
// pairs. Consumers that need identity must take it from the authenticated
// session, never from here.
type DiscoveredNode struct {
	InstanceName string    `json:"name"`
	Address      string    `json:"addr"` // IP address or host:port
	Port         int       `json:"port"` // Wire port (e.g. 9877)
	LastSeen     time.Time `json:"last_seen"`
}

// DiscoveryConfig holds parameters for the discovery Engine.
type DiscoveryConfig struct {
	NodeName      string        `json:"name"`
	WirePort      int           `json:"port"`
	Interval      time.Duration // Default: 3s
	TTL           time.Duration // Default: 15s
	BroadcastPort int           // Fallback UDP broadcast port (default: 9879)
}

// beaconPayload is the unauthenticated advertisement. It deliberately carries
// no SAS and no certificate fingerprint: both are pre-computation material for
// an active pairing man-in-the-middle, and neither is needed to render "a hub
// is nearby". BeaconNonce is random per engine start and identifies only our
// own broadcast echo; it is never identity.
type beaconPayload struct {
	Version     int    `json:"v"`
	Name        string `json:"name"`
	Port        int    `json:"port"`
	BeaconNonce string `json:"nonce,omitempty"`
	// SAS and FP exist only so a beacon from an older peer can be recognised
	// and rejected. Pre-d7de281 declared them without omitempty, so an older
	// peer emits the keys even when it has nothing to advertise — presence of
	// the key, not its value, marks the sender, hence pointers. Nothing is
	// ever read from them.
	SAS *string `json:"sas,omitempty"`
	FP  *string `json:"fp,omitempty"`
}

// Engine advertises node coordinates and maintains an active subnet registry.
type Engine struct {
	cfg         DiscoveryConfig
	beaconNonce string

	startMu     sync.Mutex
	started     bool
	startedAt   time.Time
	lifecycleMu sync.Mutex
	mu          sync.RWMutex
	nodes       map[string]*DiscoveredNode
	callbacks   []func(DiscoveredNode)
	lastBeacons map[string]time.Time

	// stats is the Health counter block; lastError/lastErrorAt are guarded by
	// statsMu. See health.go.
	stats     engineStats
	statsMu   sync.Mutex
	lastError string
	// lastErrorAt is diagnostic only: it is not rendered, because a timestamp
	// for "the last socket error" reads as a cause the user can act on when
	// what they need is the explanation, not the clock.
	lastErrorAt time.Time

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

	// A per-start nonce identifies our own broadcast echo without carrying any
	// identity. It is not a secret and is not used for authentication.
	nonceBytes := make([]byte, 12)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("generate beacon nonce: %w", err)
	}

	return &Engine{
		cfg:         cfg,
		beaconNonce: hex.EncodeToString(nonceBytes),
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
	// Address is the only key that identifies a node: discovery carries no
	// identity fields, so this both orders the list and keeps it stable
	// between calls.
	sort.Slice(res, func(i, j int) bool {
		if res[i].Address == res[j].Address {
			return res[i].InstanceName < res[j].InstanceName
		}
		return res[i].Address < res[j].Address
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
	e.startedAt = time.Now()
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
	atomic.StoreInt32(&e.stats.broadcastBound, 1)

	// 2. Multicast listener on 224.0.0.251:5353.
	//
	// This was a silent best-effort fallback: a bind failure was discarded, so
	// a network or firewall that blocks mDNS left the Hub reporting working
	// discovery with no explanation of why nothing was being found. The error
	// is now recorded and reported through Health, and the bind state is
	// tracked so Health can name the one engine that is actually working.
	mcastAddr, mErr := net.ResolveUDPAddr("udp4", DefaultMulticastAddr)
	if mErr != nil {
		e.stats.mcastBindError.Store(truncateError("resolve mDNS group: " + mErr.Error()))
		e.noteError(fmt.Errorf("resolve mDNS group %s: %w", DefaultMulticastAddr, mErr))
	} else if mcastLn, mListenErr := net.ListenMulticastUDP("udp4", nil, mcastAddr); mListenErr != nil {
		e.stats.mcastBindError.Store(truncateError("mDNS unavailable: " + mListenErr.Error()))
		e.noteError(fmt.Errorf("listen mDNS on %s: %w", DefaultMulticastAddr, mListenErr))
	} else {
		e.closeMu.Lock()
		e.mcastLn = mcastLn
		e.closeMu.Unlock()
		atomic.StoreInt32(&e.stats.multicastBound, 1)
		e.wg.Add(1)
		go e.listenLoop(mcastLn, listenMulticast)
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
	go e.listenLoop(e.udpLn, listenBroadcast)

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

// listenLoop names the socket it is reading so a give-up can be attributed.
// Two loops share this code, and "discovery stopped working" without saying
// which socket is the exact ambiguity Health exists to remove.
const (
	listenBroadcast = "broadcast"
	listenMulticast = "multicast"
)

func (e *Engine) listenLoop(conn *net.UDPConn, which string) {
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
			// listening; only a sustained failure gives up so a genuinely
			// broken socket cannot hot-spin. Stale nodes age out via TTL in the
			// meantime.
			//
			// Giving up used to be silent, so a dead socket looked exactly like
			// a quiet network. Both are now counted, and the give-up is recorded
			// so Health can name the engine that stopped.
			e.countListenError()
			if consecutiveErrs == 1 || consecutiveErrs%25 == 0 {
				e.noteError(fmt.Errorf("%s discovery read failed %d times: %w", which, consecutiveErrs, err))
			}
			consecutiveErrs++
			if consecutiveErrs > 50 {
				if which == listenBroadcast {
					e.listenLoopGaveUp()
				} else {
					e.mcastListenStopped()
				}
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
		if isSelfBeacon(e.beaconNonce, beacon) {
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
			LastSeen:     time.Now(),
		}

		e.countReceive()
		e.recordNode(node)
	}
}

// isSelfBeacon reports whether a beacon is our own broadcast coming back to us.
// Beacons no longer carry a fingerprint or SAS, so recognition is by the random
// per-start nonce. A node that does not advertise identity is therefore still
// recognized, and no identity comparison can hide a legitimate peer.
func isSelfBeacon(ownNonce string, beacon beaconPayload) bool {
	return ownNonce != "" && beacon.BeaconNonce != "" && beacon.BeaconNonce == ownNonce
}

func validBeacon(beacon beaconPayload) bool {
	if beacon.Version != 1 || beacon.Port <= 0 || beacon.Port > 65535 {
		return false
	}
	if len(beacon.Name) > 128 || containsControl(beacon.Name) {
		return false
	}
	// Reject any beacon that still carries identity fields. A peer running an
	// older release sends them unconditionally (pre-d7de281 declared them
	// without omitempty), so the keys are checked for presence rather than
	// content: an empty value is still the cleartext-identity wire format this
	// change exists to close, and such a beacon is dropped — the peer simply
	// does not appear in discovery.
	if beacon.SAS != nil || beacon.FP != nil {
		return false
	}
	if beacon.BeaconNonce != "" && len(beacon.BeaconNonce) > 64 {
		return false
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

// sourceHostCountLocked counts table entries sharing addr's host. The caller
// must hold e.mu.
func (e *Engine) sourceHostCountLocked(addr string) int {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	count := 0
	for key := range e.nodes {
		entryHost, _, entryErr := net.SplitHostPort(key)
		if entryErr != nil {
			entryHost = key
		}
		if strings.EqualFold(entryHost, host) {
			count++
		}
	}
	return count
}

func (e *Engine) recordNode(node DiscoveredNode) {
	e.mu.Lock()
	key := node.Address
	// found is the map lookup's ok value: true when the key is already present.
	existing, found := e.nodes[key]
	if !found {
		// New keys from a host already at its slot cap are refused before the
		// global eviction can remove a legitimate peer on this host's behalf.
		// Existing entries keep refreshing normally at the cap.
		if e.sourceHostCountLocked(key) >= maxNodesPerSourceHost {
			e.mu.Unlock()
			return
		}
		if len(e.nodes) >= maxDiscoveredNodes {
			// Evict the least recently seen hint before accepting a new one.
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
	}
	if !found {
		e.nodes[key] = &node
	} else {
		existing.LastSeen = node.LastSeen
		existing.Address = node.Address
		existing.Port = node.Port
		existing.InstanceName = node.InstanceName
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
		Version:     1,
		Name:        e.cfg.NodeName,
		Port:        e.cfg.WirePort,
		BeaconNonce: e.beaconNonce,
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

	// A beacon that is written nowhere is why a hub is invisible, and this
	// function used to discard every error it produced. Each destination is
	// now attempted and the outcome recorded, so a network that silently drops
	// broadcast is reported as such instead of looking like a quiet LAN.
	//
	// "Sent" means this write to this address returned without error. It does
	// not mean a peer received it - UDP gives no such guarantee - which is why
	// Health.Detail speaks of hubs "in reach" rather than "connected".
	sent, failures := 0, 0
	send := func(target *net.UDPAddr) {
		_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		if _, writeErr := conn.WriteToUDP(packet, target); writeErr != nil {
			failures++
			e.noteError(fmt.Errorf("beacon to %s: %w", target, writeErr))
			return
		}
		sent++
	}

	// 1. Broadcast to 255.255.255.255:port
	send(&net.UDPAddr{IP: net.IPv4bcast, Port: e.cfg.BroadcastPort})

	// 2. Broadcast to each interface's directed subnet broadcast address
	ifaces, err := net.Interfaces()
	if err != nil {
		// Interface enumeration failing means the directed broadcasts are
		// skipped entirely. Previously silent, and the reason a hub behind a
		// /16 or an unusual netmask was invisible to itself.
		e.noteError(fmt.Errorf("enumerate interfaces for directed broadcast: %w", err))
		e.countSendError()
	} else {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, aErr := iface.Addrs()
			if aErr != nil {
				e.countSendError()
				continue
			}
			for _, addr := range addrs {
				ipnet, ok := addr.(*net.IPNet)
				if !ok || ipnet.IP.To4() == nil {
					continue
				}
				ip4 := ipnet.IP.To4()
				mask := ipnet.Mask
				if len(mask) != 4 {
					continue
				}
				bcastIP := net.IPv4(
					ip4[0]|^mask[0],
					ip4[1]|^mask[1],
					ip4[2]|^mask[2],
					ip4[3]|^mask[3],
				)
				send(&net.UDPAddr{IP: bcastIP, Port: e.cfg.BroadcastPort})
			}
		}
	}

	// 3. Also multicast to 224.0.0.251:5353 if mDNS is open
	if mcastAddr, mErr := net.ResolveUDPAddr("udp4", DefaultMulticastAddr); mErr == nil {
		send(mcastAddr)
	} else {
		e.noteError(fmt.Errorf("resolve mDNS group for beacon: %w", mErr))
	}

	atomic.AddInt64(&e.stats.sendErrors, int64(failures))
	if sent > 0 {
		e.countSend()
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
	// Clear the bound flags with the sockets. A later Health() call must not
	// report sockets that have been closed, or a stopped engine would look
	// usable.
	atomic.StoreInt32(&e.stats.broadcastBound, 0)
	atomic.StoreInt32(&e.stats.multicastBound, 0)
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
		// Clear the started flag with the sockets. A closed engine that still
		// answers Health() with Running=true is a Hub that claims to be
		// discovering peers while holding nothing to discover them with, which
		// is the exact class of silent lie this surface removes.
		e.startMu.Lock()
		e.started = false
		e.startMu.Unlock()
		if cancel != nil {
			cancel()
		}
		e.closeSockets()
		e.wg.Wait()
	})
	return nil
}
