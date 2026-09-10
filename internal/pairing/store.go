package pairing

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Peer represents a trusted remote tantu instance.
type Peer struct {
	Fingerprint string    `json:"fingerprint"`
	Name        string    `json:"name"`                 // human-readable label
	Alias       string    `json:"alias,omitempty"`      // user-assigned friendly nickname
	IsDefault   bool      `json:"is_default,omitempty"` // true if designated default peer
	Address     string    `json:"address"`              // host:port
	CertPEM     []byte    `json:"cert_pem"`             // peer's certificate for TLS verification
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
}

// DisplayName returns the peer's preferred display name: Alias if set, else Name, else "peer-<SAS>".
func (p Peer) DisplayName() string {
	if p.Alias != "" {
		return p.Alias
	}
	if p.Name != "" {
		return p.Name
	}
	return "peer-" + p.SAS()
}

// SAS returns the 6-character Short Authentication String for the peer's certificate.
func (p Peer) SAS() string {
	return SASCode(p.Fingerprint)
}

// PeerStore manages trusted peer identities and the local node identity.
type PeerStore struct {
	dir string
	mu  sync.RWMutex
}

// NewPeerStore creates a store rooted at the given directory.
// The directory is created with 0700 permissions if it doesn't exist.
func NewPeerStore(dir string) (*PeerStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create peer store directory: %w", err)
	}
	return &PeerStore{dir: dir}, nil
}

// DefaultStoreDir returns the platform-appropriate config directory
// (~/.config/tantu or %APPDATA%\tantu).
func DefaultStoreDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config dir: %w", err)
	}
	return filepath.Join(base, "tantu"), nil
}

func (s *PeerStore) identityPath() string {
	return filepath.Join(s.dir, "identity.json")
}

func (s *PeerStore) peersPath() string {
	return filepath.Join(s.dir, "peers.json")
}

// writeAtomic writes data to a temp file and renames it to dst with target perm.
func (s *PeerStore) writeAtomic(dst string, data []byte, perm os.FileMode) error {
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("write tmp file: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		// Fallback for Windows file locking / replace issues
		_ = os.Remove(dst)
		if err2 := os.Rename(tmp, dst); err2 != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("rename %s to %s: %w", tmp, dst, err2)
		}
	}
	return nil
}

// SaveIdentity persists the local node's own identity (cert + private key) to disk.
func (s *PeerStore) SaveIdentity(id *Identity) error {
	if id == nil {
		return errors.New("cannot save nil identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal identity: %w", err)
	}

	return s.writeAtomic(s.identityPath(), data, 0600)
}

// LoadIdentity loads the local node's identity from disk.
// Returns nil, nil if the identity file does not exist.
func (s *PeerStore) LoadIdentity() (*Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.identityPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read identity file: %w", err)
	}

	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, fmt.Errorf("unmarshal identity: %w", err)
	}
	return &id, nil
}

// HasIdentity reports whether the local identity exists on disk.
func (s *PeerStore) HasIdentity() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	info, err := os.Stat(s.identityPath())
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Size() > 0
}

func (s *PeerStore) loadPeersMap() (map[string]Peer, error) {
	path := s.peersPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]Peer), nil
		}
		return nil, fmt.Errorf("read peers file: %w", err)
	}

	peers := make(map[string]Peer)
	if len(data) == 0 {
		return peers, nil
	}
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil, fmt.Errorf("unmarshal peers: %w", err)
	}
	return peers, nil
}

func (s *PeerStore) savePeersMap(peers map[string]Peer) error {
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal peers: %w", err)
	}
	return s.writeAtomic(s.peersPath(), data, 0644)
}

// AddPeer persists a trusted peer. If the peer already exists, it is updated.
func (s *PeerStore) AddPeer(p Peer) error {
	if p.Fingerprint == "" {
		return errors.New("peer fingerprint cannot be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	peers, err := s.loadPeersMap()
	if err != nil {
		return err
	}

	now := time.Now()
	if existing, ok := peers[p.Fingerprint]; ok {
		if p.FirstSeen.IsZero() {
			p.FirstSeen = existing.FirstSeen
		}
	}
	if p.FirstSeen.IsZero() {
		p.FirstSeen = now
	}
	p.LastSeen = now

	peers[p.Fingerprint] = p
	return s.savePeersMap(peers)
}

// GetPeer returns peer information by fingerprint.
func (s *PeerStore) GetPeer(fingerprint string) (Peer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peers, err := s.loadPeersMap()
	if err != nil {
		return Peer{}, false
	}
	p, ok := peers[fingerprint]
	return p, ok
}

// ListPeers returns all trusted peers.
func (s *PeerStore) ListPeers() []Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peers, err := s.loadPeersMap()
	if err != nil {
		return nil
	}

	list := make([]Peer, 0, len(peers))
	for _, p := range peers {
		list = append(list, p)
	}
	return list
}

// RemovePeer removes a trusted peer by fingerprint.
func (s *PeerStore) RemovePeer(fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	peers, err := s.loadPeersMap()
	if err != nil {
		return err
	}

	delete(peers, fingerprint)
	return s.savePeersMap(peers)
}

// IsTrusted reports whether the fingerprint belongs to a trusted peer.
func (s *PeerStore) IsTrusted(fingerprint string) bool {
	_, ok := s.GetPeer(fingerprint)
	return ok
}

// SetAlias assigns a user-friendly alias/nickname to a trusted peer.
func (s *PeerStore) SetAlias(fingerprint, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	peers, err := s.loadPeersMap()
	if err != nil {
		return err
	}
	p, ok := peers[fingerprint]
	if !ok {
		return fmt.Errorf("peer %q not found", fingerprint)
	}
	p.Alias = strings.TrimSpace(alias)
	peers[fingerprint] = p
	return s.savePeersMap(peers)
}

// SetDefault designates a peer as the default target. Clears default flag on all other peers.
func (s *PeerStore) SetDefault(fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	peers, err := s.loadPeersMap()
	if err != nil {
		return err
	}
	if _, ok := peers[fingerprint]; !ok {
		return fmt.Errorf("peer %q not found", fingerprint)
	}
	for fp, p := range peers {
		p.IsDefault = (fp == fingerprint)
		peers[fp] = p
	}
	return s.savePeersMap(peers)
}

// UpdatePeerAddress updates a peer's network host:port address and updates LastSeen.
func (s *PeerStore) UpdatePeerAddress(fingerprint, newAddress string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	peers, err := s.loadPeersMap()
	if err != nil {
		return err
	}
	p, ok := peers[fingerprint]
	if !ok {
		return fmt.Errorf("peer %q not found", fingerprint)
	}
	p.Address = newAddress
	p.LastSeen = time.Now()
	peers[fingerprint] = p
	return s.savePeersMap(peers)
}

// ResolvePeer deterministically resolves a query to a Peer using a 5-tier fuzzy match.
// If query is empty: returns the designated default peer if one exists; otherwise the first peer.
// Tier 1: Exact or prefix match against Fingerprint.
// Tier 2: Exact case-insensitive match against 6-char SAS code.
// Tier 3: Exact case-insensitive match against user-defined Alias.
// Tier 4: Exact case-insensitive match against machine Name.
// Tier 5: Match against Address host:port or IP.
func (s *PeerStore) ResolvePeer(query string) (*Peer, error) {
	peers := s.ListPeers()
	if len(peers) == 0 {
		return nil, errors.New("no paired peers found; pair with remote machine first")
	}

	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		for _, p := range peers {
			if p.IsDefault {
				res := p
				return &res, nil
			}
		}
		res := peers[0]
		return &res, nil
	}

	// Tier 1: Fingerprint match (exact or prefix if prefix >= 6 chars)
	for _, p := range peers {
		if strings.EqualFold(p.Fingerprint, trimmed) {
			res := p
			return &res, nil
		}
	}
	if len(trimmed) >= 6 {
		for _, p := range peers {
			if strings.HasPrefix(strings.ToLower(p.Fingerprint), strings.ToLower(trimmed)) {
				res := p
				return &res, nil
			}
		}
	}

	// Tier 2: Exact SAS code match
	for _, p := range peers {
		if strings.EqualFold(p.SAS(), trimmed) {
			res := p
			return &res, nil
		}
	}

	// Tier 3: Exact Alias match
	for _, p := range peers {
		if p.Alias != "" && strings.EqualFold(p.Alias, trimmed) {
			res := p
			return &res, nil
		}
	}

	// Tier 4: Exact Name match
	for _, p := range peers {
		if p.Name != "" && strings.EqualFold(p.Name, trimmed) {
			res := p
			return &res, nil
		}
	}

	// Tier 5: Address match
	for _, p := range peers {
		if strings.EqualFold(p.Address, trimmed) {
			res := p
			return &res, nil
		}
		if host, _, err := net.SplitHostPort(p.Address); err == nil && strings.EqualFold(host, trimmed) {
			res := p
			return &res, nil
		}
	}

	return nil, fmt.Errorf("peer %q not found in paired peers", query)
}
