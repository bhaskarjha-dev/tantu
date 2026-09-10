package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
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

	mu        sync.RWMutex
	nodes     map[string]*DiscoveredNode
	callbacks []func(DiscoveredNode)

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

	return &Engine{
		cfg:   cfg,
		nodes: make(map[string]*DiscoveredNode),
	}, nil
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
	return res
}

// Start binds network sockets and begins background advertising and listening.
func (e *Engine) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	e.cancel = cancel

	// 1. Bind UDP broadcast listener (port 9879 or configured)
	bcastAddr := &net.UDPAddr{IP: net.IPv4zero, Port: e.cfg.BroadcastPort}
	udpLn, err := net.ListenUDP("udp4", bcastAddr)
	if err != nil {
		cancel()
		return fmt.Errorf("listen udp broadcast on %d: %w", e.cfg.BroadcastPort, err)
	}
	e.udpLn = udpLn

	// 2. Best-effort mDNS multicast listener on 224.0.0.251:5353
	mcastAddr, mErr := net.ResolveUDPAddr("udp4", DefaultMulticastAddr)
	if mErr == nil {
		mcastLn, mListenErr := net.ListenMulticastUDP("udp4", nil, mcastAddr)
		if mListenErr == nil {
			e.mcastLn = mcastLn
			e.wg.Add(1)
			go e.listenLoop(mcastLn)
		}
	}

	// 3. Create outbound UDP socket for transmission
	outConn, outErr := net.ListenUDP("udp4", nil)
	if outErr != nil {
		_ = e.udpLn.Close()
		if e.mcastLn != nil {
			_ = e.mcastLn.Close()
		}
		cancel()
		return fmt.Errorf("listen outbound udp: %w", outErr)
	}
	e.outConn = outConn

	// 4. Start listener on primary broadcast socket
	e.wg.Add(1)
	go e.listenLoop(e.udpLn)

	// 5. Start advertising and pruning loop
	e.wg.Add(1)
	go e.advertiseLoop(ctx)

	return nil
}

func (e *Engine) listenLoop(conn *net.UDPConn) {
	defer e.wg.Done()
	buf := make([]byte, 2048)

	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "use of closed network connection") {
				return
			}
			continue
		}

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

		// Self-filtering: ignore beacons from our own node
		if (e.cfg.Fingerprint != "" && beacon.FP == e.cfg.Fingerprint) ||
			(e.cfg.SAS != "" && beacon.SAS == e.cfg.SAS) {
			continue
		}
		if beacon.Port <= 0 {
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

func (e *Engine) recordNode(node DiscoveredNode) {
	e.mu.Lock()
	key := node.Fingerprint
	if key == "" {
		key = node.Address
	}
	existing, isNew := e.nodes[key]
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

	for _, cb := range cbs {
		cb(node)
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

// Close gracefully stops the Engine and closes all network sockets.
func (e *Engine) Close() error {
	e.stopOnce.Do(func() {
		e.closeMu.Lock()
		e.closed = true
		if e.cancel != nil {
			e.cancel()
		}
		if e.udpLn != nil {
			_ = e.udpLn.Close()
		}
		if e.mcastLn != nil {
			_ = e.mcastLn.Close()
		}
		if e.outConn != nil {
			_ = e.outConn.Close()
		}
		e.closeMu.Unlock()
		e.wg.Wait()
	})
	return nil
}
