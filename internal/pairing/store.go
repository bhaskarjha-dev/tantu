package pairing

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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
// The directory is created with 0700 permissions if it doesn't exist, and
// pre-existing lax permissions are tightened: identity material lives here.
func NewPeerStore(dir string) (*PeerStore, error) {
	if err := ensureStoreDir(dir); err != nil {
		return nil, err
	}
	return &PeerStore{dir: dir}, nil
}

// ensureStoreDir creates dir with 0700 when missing and repairs group/other
// access bits when a previous umask, restore, or manual chmod left them set.
// The repair is a single best-effort Chmod; a persistent failure is reported
// so a world-readable identity directory can never be silently accepted.
func ensureStoreDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create peer store directory: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat peer store directory: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		if err := os.Chmod(dir, 0700); err != nil {
			return fmt.Errorf("tighten peer store directory permissions: %w", err)
		}
	}
	return nil
}

// ensurePrivateFile tightens a store file to owner-only access when a
// previous umask, restore, or manual chmod left group/other bits set. It runs
// after a successful read so a Chmod failure can never turn valid data into a
// load error; files written by writeAtomic are already 0600.
func ensurePrivateFile(path string) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	if info.Mode().Perm()&0077 != 0 {
		_ = os.Chmod(path, 0600)
	}
}

const (
	peerMutationLockName = ".peers.lock"
	peerMutationWait     = 2 * time.Second
	peerMutationStale    = 30 * time.Second
)

// withPeerMutationLock serializes the complete load-modify-write transaction
// across Hub, CLI, and pairing processes sharing a store directory. The
// in-process mutex still protects local callers; this directory lock closes
// the lost-update window between independent Tantu processes.
func (s *PeerStore) withPeerMutationLock(fn func() error) error {
	return s.withStoreMutationLock(peerMutationLockName, "peer-store", fn)
}

const identityMutationLockName = ".identity.lock"

func (s *PeerStore) withIdentityMutationLock(fn func() error) error {
	return s.withStoreMutationLock(identityMutationLockName, "identity-store", fn)
}

func (s *PeerStore) withStoreMutationLock(name, label string, fn func() error) error {
	lockPath := filepath.Join(s.dir, name)
	deadline := time.Now().Add(peerMutationWait)
	for {
		err := os.Mkdir(lockPath, 0700)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return fmt.Errorf("acquire %s lock: %w", label, err)
		}
		if info, statErr := os.Lstat(lockPath); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("%s lock path is not a directory", label)
			}
			if time.Since(info.ModTime()) > peerMutationStale {
				_ = os.RemoveAll(lockPath)
				continue
			}
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect %s lock: %w", label, statErr)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s lock timeout", label)
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer func() { _ = os.RemoveAll(lockPath) }()
	return fn()
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

func (s *PeerStore) activePeerPath() string {
	return filepath.Join(s.dir, "active.json")
}

// writeAtomic writes data to a unique temporary file, flushes it, and
// atomically replaces the destination.  A fixed ".tmp" name allowed two
// processes sharing a store directory to corrupt each other's writes.
func (s *PeerStore) writeAtomic(dst string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		// Windows cannot replace an existing destination. Preserve a backup
		// before retrying so a crash or a failed retry never destroys the only
		// known-good copy.
		backupFile, backupErr := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".bak-*")
		if backupErr != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("create replacement backup: %w", backupErr)
		}
		backupPath := backupFile.Name()
		_ = backupFile.Close()
		_ = os.Remove(backupPath)
		if backupErr = os.Rename(dst, backupPath); backupErr != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("preserve destination before replacement: %w", backupErr)
		}
		if retryErr := os.Rename(tmpPath, dst); retryErr != nil {
			_ = os.Rename(backupPath, dst)
			_ = os.Remove(tmpPath)
			return fmt.Errorf("rename %s to %s: %w", tmpPath, dst, retryErr)
		}
		_ = os.Remove(backupPath)
	}
	return nil
}

// SaveIdentity persists the local node's own identity (cert + private key) to disk.
func (s *PeerStore) SaveIdentity(id *Identity) error {
	if err := ValidateIdentity(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withIdentityMutationLock(func() error {
		return s.saveIdentityUnlocked(id)
	})
}

func (s *PeerStore) saveIdentityUnlocked(id *Identity) error {
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
	return s.loadIdentityUnlocked()
}

func (s *PeerStore) loadIdentityUnlocked() (*Identity, error) {
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
	if err := ValidateIdentity(&id); err != nil {
		return nil, fmt.Errorf("validate identity: %w", err)
	}
	ensurePrivateFile(path)
	return &id, nil
}

// LoadOrCreateIdentity atomically returns the existing identity or creates one
// when multiple Tantu processes start against the same fresh store. Without
// this transaction two first-run processes could generate different keys and
// silently invalidate one another's pairings.
func (s *PeerStore) LoadOrCreateIdentity() (*Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result *Identity
	err := s.withIdentityMutationLock(func() error {
		id, err := s.loadIdentityUnlocked()
		if err != nil {
			return err
		}
		if id == nil {
			id, err = GenerateIdentity()
			if err != nil {
				return fmt.Errorf("generate identity: %w", err)
			}
			if err := s.saveIdentityUnlocked(id); err != nil {
				return fmt.Errorf("save identity: %w", err)
			}
		}
		result = id
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
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
	ensurePrivateFile(path)
	return peers, nil
}

func (s *PeerStore) savePeersMap(peers map[string]Peer) error {
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal peers: %w", err)
	}
	return s.writeAtomic(s.peersPath(), data, 0600)
}

func validatePeerLabel(label string) error {
	if len(label) > 256 {
		return errors.New("peer label is too long")
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return errors.New("peer label contains a control character")
		}
	}
	return nil
}

// validatePeerAddress rejects malformed host:port values before they persist.
// Bare hosts without a port are accepted: NormalizePeers assigns the default
// LAN port later. Explicit ports must be numeric (service names are rejected
// so a stored address can never resolve to a surprising port).
func validatePeerAddress(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" || len(addr) > 256 {
		return errors.New("peer address is invalid")
	}
	for _, r := range addr {
		if unicode.IsControl(r) {
			return errors.New("peer address contains a control character")
		}
	}
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if err := validatePeerHost(host); err != nil {
			return err
		}
		if err := validatePeerPort(port); err != nil {
			return err
		}
		return nil
	}
	if net.ParseIP(addr) != nil {
		return nil
	}
	if strings.Contains(addr, ":") {
		return errors.New("peer address must be host:port")
	}
	return validatePeerHost(addr)
}

// validatePeerHost rejects values that are not plain DNS names or IP
// literals: no URLs, paths, userinfo, or whitespace.
func validatePeerHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return errors.New("peer address must be host:port")
	}
	if strings.ContainsAny(host, " \t/@") || strings.Contains(host, "://") {
		return errors.New("peer address host is invalid")
	}
	return nil
}

// validatePeerPort requires an explicit numeric port in range.
func validatePeerPort(port string) error {
	n, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || n < 1 || n > 65535 {
		return errors.New("peer address has an invalid port")
	}
	return nil
}

// AddPeer persists a trusted peer. If the peer already exists, it is updated.
func (s *PeerStore) AddPeer(p Peer) error {
	p.Fingerprint = strings.ToLower(strings.TrimSpace(p.Fingerprint))
	if p.Fingerprint == "" {
		return errors.New("peer fingerprint cannot be empty")
	}
	if len(p.Fingerprint) > 256 || strings.ContainsAny(p.Fingerprint, "\r\n\x00") {
		return errors.New("peer fingerprint contains invalid characters")
	}
	if err := validatePeerLabel(p.Name); err != nil {
		return err
	}
	if err := validatePeerLabel(p.Alias); err != nil {
		return err
	}
	// Bind the stored certificate and address to the fingerprint claim. The
	// LAN verifier pins the fingerprint string, so a mismatched record would
	// otherwise persist unauthoritative material that display and audit paths
	// treat as the peer's identity.
	if len(p.CertPEM) > 0 {
		if len(p.CertPEM) > 64*1024 {
			return errors.New("peer certificate is too large")
		}
		certFP, err := Fingerprint(p.CertPEM)
		if err != nil {
			return fmt.Errorf("peer certificate is invalid: %w", err)
		}
		if !strings.EqualFold(certFP, p.Fingerprint) {
			return errors.New("peer certificate does not match fingerprint")
		}
	}
	if strings.TrimSpace(p.Address) != "" {
		if err := validatePeerAddress(p.Address); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.withPeerMutationLock(func() error {
		peers, err := s.loadPeersMap()
		if err != nil {
			return err
		}

		now := time.Now()
		if existing, ok := peers[p.Fingerprint]; ok {
			if p.FirstSeen.IsZero() {
				p.FirstSeen = existing.FirstSeen
			}
			// Re-pairing must not erase user-controlled routing metadata.
			if p.Alias == "" {
				p.Alias = existing.Alias
			}
			if !p.IsDefault {
				p.IsDefault = existing.IsDefault
			}
			if p.Name == "" {
				p.Name = existing.Name
			}
			if len(p.CertPEM) == 0 {
				p.CertPEM = existing.CertPEM
			}
			if p.Address == "" {
				p.Address = existing.Address
			}
		}
		if p.FirstSeen.IsZero() {
			p.FirstSeen = now
		}
		p.LastSeen = now

		peers[p.Fingerprint] = p
		return s.savePeersMap(peers)
	})
}

// GetPeer returns peer information by fingerprint.
func (s *PeerStore) GetPeer(fingerprint string) (Peer, bool) {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	s.mu.RLock()
	defer s.mu.RUnlock()

	peers, err := s.loadPeersMap()
	if err != nil {
		return Peer{}, false
	}
	p, ok := peers[fingerprint]
	return p, ok
}

// HasPeer reports whether a fingerprint is present in the peer store and
// distinguishes a readable store with no match from a transient read/parse
// failure. Callers that use presence for cleanup should not delete state when
// the store is temporarily unreadable.
func (s *PeerStore) HasPeer(fingerprint string) (bool, error) {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	s.mu.RLock()
	defer s.mu.RUnlock()
	peers, err := s.loadPeersMap()
	if err != nil {
		return false, err
	}
	_, ok := peers[fingerprint]
	return ok, nil
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
	sort.Slice(list, func(i, j int) bool {
		if list[i].Fingerprint == list[j].Fingerprint {
			return list[i].DisplayName() < list[j].DisplayName()
		}
		return list[i].Fingerprint < list[j].Fingerprint
	})
	return list
}

// LoadActivePeer returns the persisted active-peer selection, if it still
// names a trusted peer. A stale selection is treated as cleared rather than
// allowing a removed peer to remain a hidden routing preference. The read and
// stale-file cleanup share the peer mutation lock with writers in other
// processes, so a crash window cannot resurrect a removed selection on re-pair.
func (s *PeerStore) LoadActivePeer() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result string
	err := s.withPeerMutationLock(func() error {
		path := s.activePeerPath()
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("inspect active peer: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("active peer metadata is not a regular file")
		}
		if info.Size() > 4096 {
			return errors.New("active peer metadata is too large")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read active peer: %w", err)
		}
		if len(data) > 4096 {
			return errors.New("active peer metadata is too large")
		}
		var state struct {
			Fingerprint string `json:"fingerprint"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("decode active peer: %w", err)
		}
		fingerprint := strings.ToLower(strings.TrimSpace(state.Fingerprint))
		if fingerprint == "" {
			return nil
		}
		peers, err := s.loadPeersMap()
		if err != nil {
			return err
		}
		if _, ok := peers[fingerprint]; !ok {
			return s.removeActivePeerUnlocked()
		}
		ensurePrivateFile(path)
		result = fingerprint
		return nil
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

// SaveActivePeer persists a validated active-peer selection. It is separate
// from peers.json so peer metadata remains backward-compatible and unknown
// fields are harmless on upgrade.
func (s *PeerStore) SaveActivePeer(fingerprint string) error {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withPeerMutationLock(func() error {
		if fingerprint == "" {
			return s.removeActivePeerUnlocked()
		}
		peers, err := s.loadPeersMap()
		if err != nil {
			return err
		}
		if _, ok := peers[fingerprint]; !ok {
			return fmt.Errorf("peer %q not found", fingerprint)
		}
		data, err := json.MarshalIndent(struct {
			Fingerprint string `json:"fingerprint"`
		}{Fingerprint: fingerprint}, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal active peer: %w", err)
		}
		return s.writeAtomic(s.activePeerPath(), data, 0600)
	})
}

func (s *PeerStore) removeActivePeerUnlocked() error {
	if err := os.Remove(s.activePeerPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *PeerStore) clearActivePeerIfMatchesUnlocked(fingerprint string) error {
	data, err := os.ReadFile(s.activePeerPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) > 4096 {
		return nil
	}
	var state struct {
		Fingerprint string `json:"fingerprint"`
	}
	if json.Unmarshal(data, &state) != nil || !strings.EqualFold(strings.TrimSpace(state.Fingerprint), fingerprint) {
		// Do not make an unrelated peer removal fail because an old or
		// manually edited active file is malformed. It will be ignored by
		// LoadActivePeer and can be repaired on the next selection.
		return nil
	}
	return s.removeActivePeerUnlocked()
}

// ClearActivePeer removes the persisted active-peer selection.
func (s *PeerStore) ClearActivePeer() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withPeerMutationLock(func() error { return s.removeActivePeerUnlocked() })
}

// RemovePeer removes a trusted peer by fingerprint.
func (s *PeerStore) RemovePeer(fingerprint string) error {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.withPeerMutationLock(func() error {
		peers, err := s.loadPeersMap()
		if err != nil {
			return err
		}

		delete(peers, fingerprint)
		if err := s.savePeersMap(peers); err != nil {
			return err
		}
		// Do not let a removed identity become active again merely because a
		// later re-pair happens to reuse its fingerprint. The active state is
		// part of the same peer mutation transaction.
		return s.clearActivePeerIfMatchesUnlocked(fingerprint)
	})
}

// IsTrusted reports whether the fingerprint belongs to a trusted peer.
func (s *PeerStore) IsTrusted(fingerprint string) bool {
	_, ok := s.GetPeer(fingerprint)
	return ok
}

// SetAlias assigns a user-friendly alias/nickname to a trusted peer.
func (s *PeerStore) SetAlias(fingerprint, alias string) error {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	alias = strings.TrimSpace(alias)
	if err := validatePeerLabel(alias); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.withPeerMutationLock(func() error {
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
	})
}

// SetDefault designates a peer as the default target. Clears default flag on all other peers.
func (s *PeerStore) SetDefault(fingerprint string) error {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.withPeerMutationLock(func() error {
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
	})
}

// UpdatePeerAddress updates a peer's network host:port address and updates LastSeen.
// An empty address clears the stored value.
func (s *PeerStore) UpdatePeerAddress(fingerprint, newAddress string) error {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	if strings.TrimSpace(newAddress) != "" {
		if err := validatePeerAddress(newAddress); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.withPeerMutationLock(func() error {
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
	})
}

// ResolvePeer deterministically resolves a query to a Peer using a 5-tier fuzzy match.
// If query is empty: returns the designated default peer if one exists; a
// single peer if exactly one is paired; otherwise an error (silently picking
// the first peer in sort order could misroute a transfer to the wrong machine).
// Tier 1: Exact or prefix match against Fingerprint.
// Tier 2: Exact case-insensitive match against 6-char SAS code.
// Tier 3: Exact case-insensitive match against user-defined Alias.
// Tier 4: Exact case-insensitive match against machine Name.
// Tier 5: Match against Address host:port or IP.
//
// Tiers are evaluated in order, but when a query matches different peers in
// different tiers (e.g. peer A's SAS equals a fingerprint prefix of peer B)
// the result is ambiguous and an error is returned instead of shadowing.
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
		if len(peers) == 1 {
			res := peers[0]
			return &res, nil
		}
		return nil, errors.New("multiple paired peers and no default; specify a peer or set one as default")
	}

	// Tier 1: Fingerprint match (exact or prefix if prefix >= 6 chars).
	// An exact fingerprint match is authoritative and never ambiguous.
	for _, p := range peers {
		if strings.EqualFold(p.Fingerprint, trimmed) {
			res := p
			return &res, nil
		}
	}
	var prefixMatches []Peer
	if len(trimmed) >= 6 {
		for _, p := range peers {
			if strings.HasPrefix(strings.ToLower(p.Fingerprint), strings.ToLower(trimmed)) {
				prefixMatches = append(prefixMatches, p)
			}
		}
		if len(prefixMatches) > 1 {
			return nil, fmt.Errorf("fingerprint prefix %q is ambiguous", trimmed)
		}
	}

	// Tier 2: Exact SAS code match. The SAS is only 24 bits, so collisions are
	// possible; multiple matches are ambiguous and must not silently resolve
	// to the first peer in sort order.
	var sasMatches []Peer
	for _, p := range peers {
		if strings.EqualFold(p.SAS(), trimmed) {
			sasMatches = append(sasMatches, p)
		}
	}
	if len(sasMatches) > 1 {
		return nil, fmt.Errorf("SAS code %q is ambiguous; use a fingerprint prefix", trimmed)
	}

	// Cross-tier guard: a lone prefix match and a lone SAS match for different
	// peers means the query is genuinely ambiguous, not Tier-1-wins.
	if len(prefixMatches) == 1 && len(sasMatches) == 1 &&
		!strings.EqualFold(prefixMatches[0].Fingerprint, sasMatches[0].Fingerprint) {
		return nil, fmt.Errorf("query %q matches multiple peers; use a longer fingerprint prefix", trimmed)
	}
	if len(prefixMatches) == 1 {
		res := prefixMatches[0]
		return &res, nil
	}
	if len(sasMatches) == 1 {
		res := sasMatches[0]
		return &res, nil
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
