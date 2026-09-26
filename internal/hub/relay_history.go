package hub

// Relay authorization attempt history. OAuth relay is synchronous from the
// Hub's perspective (submit URL, wait for the callback, report), so the
// honest state machine here is a faithful subset of the product model:
// submitted → waiting_callback → complete | failed | cancelled. The
// browser-opened instant is visible only in the A-side (browser machine)
// logs, never here — this ledger does not invent it.
//
// Privacy is structural: only the sanitized origin (host plus a redacted
// query marker) is stored, never the raw URL, codes, or tokens. Failures
// keep a category code, never the raw error string, which may embed
// sensitive URL material from the bridge layer.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Relay authorization attempt states (user-visible, sender side).
const (
	RelayStateSubmitted       = "submitted"
	RelayStateWaiting         = "waiting_callback"
	RelayStateComplete        = "complete"
	RelayStateFailed          = "failed"
	RelayStateCancelled       = "cancelled"
	maxRelayHistory           = 50
	relayHistoryVersion       = 1
	relayHistoryRetentionDays = 30
	maxRelayHistoryBytes      = 1 << 20
)

// RelayHistoryFilename is exported so CLI offline readers use the same file
// as the Hub writer instead of duplicating the name.
const RelayHistoryFilename = "authorizations.json"

// RelayAttempt is one authorization handoff attempt (metadata only).
type RelayAttempt struct {
	ID            string    `json:"id"`
	TargetPeer    string    `json:"target_peer,omitempty"`
	TargetFP      string    `json:"target_fingerprint,omitempty"`
	TargetAddress string    `json:"target_address,omitempty"`
	SafeOrigin    string    `json:"safe_origin,omitempty"`
	State         string    `json:"state"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	ErrorCode     string    `json:"error_code,omitempty"`
	NextAction    string    `json:"next_action,omitempty"`
	DiagnosticID  string    `json:"diagnostic_id,omitempty"`
}

// RelayLedger stores bounded relay attempt metadata, oldest evicted first.
type RelayLedger struct {
	mu       sync.Mutex
	capacity int
	items    []RelayAttempt
}

// NewRelayLedger creates a bounded ledger. Non-positive capacity uses the
// default bound.
func NewRelayLedger(capacity int) *RelayLedger {
	if capacity <= 0 {
		capacity = maxRelayHistory
	}
	return &RelayLedger{
		capacity: capacity,
		items:    make([]RelayAttempt, 0, capacity),
	}
}

func newRelayID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("rl-%d", time.Now().UnixNano())
	}
	return "rl-" + hex.EncodeToString(raw[:])
}

// Add appends an attempt, evicting the oldest at capacity and pruning entries
// older than the retention window.
func (l *RelayLedger) Add(op RelayAttempt) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.capacity <= 0 {
		l.capacity = maxRelayHistory
	}
	for len(l.items) >= l.capacity && len(l.items) > 0 {
		l.items = l.items[1:]
	}
	l.items = append(l.items, op)
	l.items = pruneRelayByAge(l.items)
}

// Update applies fn to the attempt with the given ID, refreshing UpdatedAt.
func (l *RelayLedger) Update(id string, fn func(*RelayAttempt)) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.items {
		if l.items[i].ID == id {
			fn(&l.items[i])
			l.items[i].UpdatedAt = time.Now()
			return
		}
	}
}

// GetAll returns attempts in chronological order (oldest first).
func (l *RelayLedger) GetAll() []RelayAttempt {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	res := make([]RelayAttempt, len(l.items))
	copy(res, l.items)
	return res
}

// Clear empties the ledger, returning the number of removed records.
func (l *RelayLedger) Clear() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.items)
	l.items = l.items[:0]
	return n
}

// pruneRelayByAge drops entries older than the retention window, keeping the
// newest. Input order (oldest first) is preserved.
func pruneRelayByAge(ops []RelayAttempt) []RelayAttempt {
	if len(ops) == 0 {
		return ops
	}
	cutoff := time.Now().AddDate(0, 0, -relayHistoryRetentionDays)
	kept := ops[:0]
	for _, op := range ops {
		if op.CreatedAt.IsZero() || !op.CreatedAt.Before(cutoff) {
			kept = append(kept, op)
		}
	}
	return kept
}

// validRelayAttempt reports whether a loaded record is safe to keep.
func validRelayAttempt(op RelayAttempt) bool {
	if strings.TrimSpace(op.ID) == "" || op.CreatedAt.IsZero() {
		return false
	}
	switch op.State {
	case RelayStateSubmitted, RelayStateWaiting, RelayStateComplete,
		RelayStateFailed, RelayStateCancelled:
	default:
		return false
	}
	return true
}

// safeRelayOrigin reduces a sanitized authorization URL to its bare host.
// The sanitizer preserves single-use query values (state, nonce), so the
// full sanitized URL must never be persisted: the host answers "where was I
// about to authenticate" with nothing sensitive attached.
func safeRelayOrigin(sanitized string) string {
	u, err := url.Parse(strings.TrimSpace(sanitized))
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return ""
	}
	if len(host) > 253 {
		host = host[:253]
	}
	return host
}

// relayHistoryEnvelope versions the on-disk ledger for forward compatibility.
type relayHistoryEnvelope struct {
	Version        int            `json:"version"`
	Authorizations []RelayAttempt `json:"authorizations"`
}

// persistRelayHistory writes the bounded, age-pruned ledger atomically with
// owner-only permissions. Records are metadata-only by construction.
func persistRelayHistory(storeDir string, ops []RelayAttempt) error {
	if strings.TrimSpace(storeDir) == "" {
		return fmt.Errorf("store directory is not configured")
	}
	ops = pruneRelayByAge(ops)
	if len(ops) > maxRelayHistory {
		ops = ops[len(ops)-maxRelayHistory:]
	}
	data, err := json.MarshalIndent(relayHistoryEnvelope{
		Version:        relayHistoryVersion,
		Authorizations: ops,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal authorization history: %w", err)
	}
	return writeAtomicTransferFile(filepath.Join(storeDir, RelayHistoryFilename), data)
}

// loadRelayHistory reads validated, pruned file contents. A missing file is
// a clean first run; a corrupt or oversized file yields an empty result
// rather than failing the caller.
func loadRelayHistory(storeDir string) []RelayAttempt {
	if strings.TrimSpace(storeDir) == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(storeDir, RelayHistoryFilename))
	if err != nil {
		return nil
	}
	if len(raw) > maxRelayHistoryBytes {
		return nil
	}
	var env relayHistoryEnvelope
	dec := json.NewDecoder(io.LimitReader(bytes.NewReader(raw), maxRelayHistoryBytes+1))
	if err := dec.Decode(&env); err != nil {
		return nil
	}
	kept := make([]RelayAttempt, 0, len(env.Authorizations))
	for _, op := range env.Authorizations {
		if validRelayAttempt(op) {
			kept = append(kept, op)
		}
	}
	kept = pruneRelayByAge(kept)
	if len(kept) > maxRelayHistory {
		kept = kept[len(kept)-maxRelayHistory:]
	}
	return kept
}

// ReadSavedRelayHistory loads the durable authorization ledger for offline
// display (CLI fallback). It returns metadata only.
func ReadSavedRelayHistory(storeDir string) ([]RelayAttempt, error) {
	if strings.TrimSpace(storeDir) == "" {
		return nil, fmt.Errorf("store directory is not configured")
	}
	raw, err := os.ReadFile(filepath.Join(storeDir, RelayHistoryFilename))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxRelayHistoryBytes {
		return nil, fmt.Errorf("saved authorization history exceeds size limit")
	}
	var env relayHistoryEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode saved authorization history: %w", err)
	}
	return env.Authorizations, nil
}

// recordRelayAttempt appends sender-side truth to the ledger and persists it
// best-effort. A persist failure never fails the relay itself.
func (h *Hub) recordRelayAttempt(op RelayAttempt) {
	if h == nil {
		return
	}
	h.mu.RLock()
	ledger := h.relayHistory
	h.mu.RUnlock()
	if ledger == nil {
		ledger = NewRelayLedger(maxRelayHistory)
		h.mu.Lock()
		if h.relayHistory == nil {
			h.relayHistory = ledger
		} else {
			ledger = h.relayHistory
		}
		h.mu.Unlock()
	}
	ledger.Add(op)
	if dir := h.hubStoreDir(); dir != "" {
		if err := persistRelayHistory(dir, ledger.GetAll()); err != nil && h.logger != nil {
			h.logger.Warn(DomainOAuth, fmt.Sprintf("Could not save authorization history: %v", err))
		}
	}
}

// clearRelayHistory empties the authorization ledger and removes its
// persisted file (best-effort). Transfer history and received files are
// unaffected: only sender-side authorization metadata is deleted, and the
// deletion itself is logged.
func (h *Hub) clearRelayHistory() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	ledger := h.relayHistory
	dir := h.storeDir
	h.mu.RUnlock()
	removed := 0
	if ledger != nil {
		removed = ledger.Clear()
	}
	if dir != "" {
		_ = os.Remove(filepath.Join(dir, RelayHistoryFilename))
	}
	if h.logger != nil {
		h.logger.Warn(DomainOAuth, fmt.Sprintf("Authorization history cleared (%d record(s) removed)", removed))
	}
	return removed
}

// listRelayAttempts returns a copy of the ledger.
func (h *Hub) listRelayAttempts() []RelayAttempt {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	ledger := h.relayHistory
	h.mu.RUnlock()
	if ledger == nil {
		return nil
	}
	return ledger.GetAll()
}

// loadRelayHistoryInto replaces the ledger with validated file contents.
func (h *Hub) loadRelayHistoryInto(storeDir string) {
	if h == nil || strings.TrimSpace(storeDir) == "" {
		return
	}
	kept := loadRelayHistory(storeDir)
	h.mu.RLock()
	ledger := h.relayHistory
	h.mu.RUnlock()
	if ledger == nil {
		ledger = NewRelayLedger(maxRelayHistory)
		h.mu.Lock()
		if h.relayHistory == nil {
			h.relayHistory = ledger
		} else {
			ledger = h.relayHistory
		}
		h.mu.Unlock()
	}
	ledger.mu.Lock()
	ledger.items = append(ledger.items[:0], kept...)
	ledger.mu.Unlock()
}
