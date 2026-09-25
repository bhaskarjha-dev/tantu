package drop

// Completion tombstones give retried transfers at-most-once publication.
// When a sender retries the same logical operation with the same
// idempotency key, the receiver replays the recorded success acknowledgement
// instead of staging and publishing a second copy. The retry still streams
// its bytes (the protocol has no early abort), but those bytes go to a
// discard sink and their SHA-256 must match the recorded digest, otherwise
// the attempt fails closed.
//
// Tombstones are keyed by sender-supplied keys, so the safety argument is:
// same key + same metadata + same bytes = same file, acknowledged again.
// Anything else (unknown key, mismatched metadata, mismatched bytes) takes
// the legacy path or fails closed. Tombstones never authorize anything on
// their own: they only suppress duplicate publication for already-verified
// content from an already-authenticated peer.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// maxTombstoneEntries bounds the store; completion records are small
	// metadata, and 1,024 entries cover far more retries than any honest
	// operator performs within the retention window.
	maxTombstoneEntries = 1024
	// tombstoneRetention matches the staging-part sweep: an idempotency
	// window longer than a day invites surprising re-acks.
	tombstoneRetention = 24 * time.Hour
	tombstoneVersion   = 1
	// maxTombstoneBytes caps the persisted read so a corrupt or foreign file
	// cannot become a memory sink.
	maxTombstoneBytes = 1 << 20
)

// TombstoneFilename is the ledger name inside a Tantu-owned directory.
// Exported so Hub and CLI offline readers use the same file as the writer.
const TombstoneFilename = "tombstones.json"

// TombstoneEntry is one verified completion available for re-acknowledgement.
// Metadata only: digests and descriptors, never content.
type TombstoneEntry struct {
	Key         string    `json:"key"`
	DropID      string    `json:"drop_id,omitempty"`
	Kind        DropKind  `json:"kind"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	MIMEType    string    `json:"mime_type,omitempty"`
	ChunkSize   int       `json:"chunk_size"`
	HeadHash    string    `json:"head_hash,omitempty"`
	SHA256      string    `json:"sha256"`
	Bytes       int64     `json:"bytes"`
	CompletedAt time.Time `json:"completed_at"`
}

// Matches reports whether stored metadata describes the same transfer as
// the incoming metadata. Any difference fails closed: combining bytes from
// unrelated content under one key must be impossible.
func (e TombstoneEntry) Matches(meta DropSend) bool {
	meta = normalizedManifestMetadata(meta)
	return e.Kind == meta.Kind &&
		e.Name == meta.Name &&
		e.Size == meta.Size &&
		e.MIMEType == meta.MIMEType &&
		e.ChunkSize == meta.ChunkSize &&
		strings.EqualFold(e.HeadHash, meta.HeadHash)
}

// Tombstones is a bounded, optionally durable completion store. The zero
// value is a valid memory-only store; use NewTombstones for file backing.
// All methods are safe for concurrent receiver sessions.
type Tombstones struct {
	mu         sync.Mutex
	path       string
	maxEntries int
	maxAge     time.Duration
	entries    map[string]TombstoneEntry
}

// NewTombstones creates a store. An empty path means memory-only (useful for
// tests and short-lived receivers); otherwise records persist atomically
// with owner-only permissions and reload on creation.
func NewTombstones(path string) *Tombstones {
	s := &Tombstones{
		path:       path,
		maxEntries: maxTombstoneEntries,
		maxAge:     tombstoneRetention,
		entries:    make(map[string]TombstoneEntry),
	}
	s.load()
	return s
}

// Lookup returns the completion record for key, if any. Callers must still
// check Matches: a present key with different metadata is a hard error, not
// a duplicate.
func (s *Tombstones) Lookup(key string) (TombstoneEntry, bool) {
	if s == nil || strings.TrimSpace(key) == "" {
		return TombstoneEntry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	return e, ok
}

// Record publishes a completion for future duplicate suppression. It is
// best-effort durable: a persist failure never fails the transfer the
// record describes (memory stays authoritative for the process lifetime).
func (s *Tombstones) Record(e TombstoneEntry) {
	if s == nil || strings.TrimSpace(e.Key) == "" {
		return
	}
	s.mu.Lock()
	if s.entries == nil {
		s.entries = make(map[string]TombstoneEntry)
	}
	if e.CompletedAt.IsZero() {
		e.CompletedAt = time.Now()
	}
	s.entries[e.Key] = e
	s.pruneLocked()
	snapshot := make([]TombstoneEntry, 0, len(s.entries))
	for _, v := range s.entries {
		snapshot = append(snapshot, v)
	}
	path := s.path
	s.mu.Unlock()
	if path != "" {
		_ = writeTombstoneFile(path, snapshot)
	}
}

// Count returns the number of retained records (for diagnostics and tests).
func (s *Tombstones) Count() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *Tombstones) pruneLocked() {
	if s.maxAge > 0 {
		cutoff := time.Now().Add(-s.maxAge)
		for k, e := range s.entries {
			if !e.CompletedAt.IsZero() && e.CompletedAt.Before(cutoff) {
				delete(s.entries, k)
			}
		}
	}
	if s.maxEntries > 0 && len(s.entries) > s.maxEntries {
		ordered := make([]TombstoneEntry, 0, len(s.entries))
		for _, e := range s.entries {
			ordered = append(ordered, e)
		}
		sort.Slice(ordered, func(i, j int) bool {
			return ordered[i].CompletedAt.Before(ordered[j].CompletedAt)
		})
		for _, e := range ordered[:len(ordered)-s.maxEntries] {
			delete(s.entries, e.Key)
		}
	}
}

type tombstoneEnvelope struct {
	Version int              `json:"version"`
	Entries []TombstoneEntry `json:"entries"`
}

func (s *Tombstones) load() {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	if len(raw) > maxTombstoneBytes {
		return
	}
	var env tombstoneEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]TombstoneEntry)
	}
	for _, e := range env.Entries {
		if strings.TrimSpace(e.Key) == "" || e.SHA256 == "" {
			continue
		}
		s.entries[e.Key] = e
	}
	s.pruneLocked()
}

func writeTombstoneFile(dst string, entries []TombstoneEntry) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return fmt.Errorf("create tombstone directory: %w", err)
	}
	data, err := json.MarshalIndent(tombstoneEnvelope{Version: tombstoneVersion, Entries: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tombstones: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create tombstone temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0600); err != nil {
		cleanup()
		return fmt.Errorf("set tombstone permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write tombstones: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync tombstones: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close tombstones: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		backupFile, backupErr := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".bak-*")
		if backupErr != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("create tombstone replacement backup: %w", backupErr)
		}
		backupPath := backupFile.Name()
		_ = backupFile.Close()
		_ = os.Remove(backupPath)
		if backupErr = os.Rename(dst, backupPath); backupErr != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("preserve tombstones before replacement: %w", backupErr)
		}
		if retryErr := os.Rename(tmpPath, dst); retryErr != nil {
			_ = os.Rename(backupPath, dst)
			_ = os.Remove(tmpPath)
			return fmt.Errorf("rename tombstones: %w", retryErr)
		}
		_ = os.Remove(backupPath)
	}
	return nil
}

// validTombstoneKey reports whether s fits the staging-path-adjacent trust
// of transfer identifiers. Exported so CLI send paths and the Hub fail fast
// on caller-supplied keys instead of discovering the rejection mid-transfer.
func ValidTombstoneKey(s string) bool {
	if len(s) == 0 || len(s) > MaxDropIDLength {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
