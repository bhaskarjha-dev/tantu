package hub

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Outbound transfer operation states. These are user-visible lifecycle
// states, not wire protocol states. They answer "what happened?" and
// "what is safe to do next?" without overstating remote outcome.
const (
	OperationStateCompleted        = "completed"
	OperationStateRetryableFailure = "retryable_failure"
	OperationStateTerminalFailure  = "terminal_failure"
	OperationStateCancelled        = "cancelled"
	OperationStateDuplicateRisk    = "duplicate_risk"
	OperationStateSubmitted        = "submitted"
	OperationStateNegotiating      = "negotiating"
	OperationStateSending          = "sending"
	OperationStateVerifying        = "verifying"
)

// maxOutboundOperations bounds sender-side transfer truth (in memory and on
// disk). It is metadata-only: never payload bytes, text snippets, clipboard
// contents, tokens, or full sensitive URLs. Entries older than
// transferRetentionDays are pruned; the dashboard and CLI label saved history
// as of the last Hub generation so a restart is never mistaken for emptiness.
const maxOutboundOperations = 50

const (
	transferPersistVersion  = 1
	transferPersistFileName = "transfers.json"
	transferRetentionDays   = 30
	// maxTransferPersistBytes caps the persisted ledger read so a corrupt or
	// foreign file cannot become a memory sink. Fifty metadata records are
	// tens of kilobytes; 1 MiB is ample headroom.
	maxTransferPersistBytes = 1 << 20
)

// OperationRecord is the canonical sender-side truth for one Hub upload
// attempt. It contains only metadata: never payload bytes, clipboard
// contents, text snippets, tokens, or full sensitive URLs.
type OperationRecord struct {
	OperationID            string    `json:"operation_id"`
	DropID                 string    `json:"drop_id,omitempty"`
	Kind                   string    `json:"kind"`
	Name                   string    `json:"name,omitempty"`
	Size                   int64     `json:"size"`
	MIMEType               string    `json:"mime_type,omitempty"`
	Destination            string    `json:"destination,omitempty"`
	DestinationFingerprint string    `json:"destination_fingerprint,omitempty"`
	DestinationAddress     string    `json:"destination_address,omitempty"`
	State                  string    `json:"state"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
	BytesSent              int64     `json:"bytes_sent,omitempty"`
	Verified               bool      `json:"verified,omitempty"`
	RetrySafe              bool      `json:"retry_safe"`
	DuplicateRisk          bool      `json:"duplicate_risk"`
	DataSafe               bool      `json:"data_safe"`
	ErrorCode              string    `json:"error_code,omitempty"`
	ErrorMessage           string    `json:"error_message,omitempty"`
	NextAction             string    `json:"next_action,omitempty"`
	DiagnosticID           string    `json:"diagnostic_id,omitempty"`
}

// OperationLedger stores bounded sender-side operation metadata for the
// current Hub generation. Oldest entries are evicted first.
type OperationLedger struct {
	mu       sync.Mutex
	capacity int
	items    []OperationRecord
}

// NewOperationLedger creates a bounded ledger. Non-positive capacity uses
// the default bound.
func NewOperationLedger(capacity int) *OperationLedger {
	if capacity <= 0 {
		capacity = maxOutboundOperations
	}
	return &OperationLedger{
		capacity: capacity,
		items:    make([]OperationRecord, 0, capacity),
	}
}

// Add appends an operation, evicting the oldest when at capacity and
// pruning entries older than the retention window.
func (l *OperationLedger) Add(op OperationRecord) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.capacity <= 0 {
		l.capacity = maxOutboundOperations
	}
	for len(l.items) >= l.capacity && len(l.items) > 0 {
		l.items = l.items[1:]
	}
	l.items = append(l.items, op)
	l.items = pruneOperationsByAge(l.items)
}

// Clear empties the ledger, returning the number of removed records.
func (l *OperationLedger) Clear() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.items)
	l.items = l.items[:0]
	return n
}

// GetAll returns operations in chronological order (oldest first).
func (l *OperationLedger) GetAll() []OperationRecord {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	res := make([]OperationRecord, len(l.items))
	copy(res, l.items)
	return res
}

func newOperationID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	return "op-" + hex.EncodeToString(raw[:])
}

func newOutboundDropID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("drop-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

// ClassifyTransferError maps a send failure to honest user-facing safety
// flags. The guiding rule: bytes never sent means safe retry; bytes fully
// streamed without a confirmation means unknown outcome (possible
// duplicate); integrity rejections mean nothing was published. Exported so
// CLI send paths share the Hub's single taxonomy instead of drifting.
func ClassifyTransferError(err error, bytesSent, size int64, cancelled bool) (code, plain, nextAction string, retrySafe, duplicateRisk, dataSafe bool) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	lower := strings.ToLower(msg)
	dataSafe = true

	if cancelled || strings.Contains(lower, "context canceled") || strings.Contains(lower, "context cancelled") {
		if size > 0 && bytesSent >= size && bytesSent > 0 {
			return "cancelled_after_send",
				"Transfer cancelled after sending. The file may already be saved on the receiver.",
				"Check the receiver inbox before retrying to avoid a duplicate. Retrying creates a new transfer.",
				false, true, true
		}
		return "cancelled",
			"Transfer cancelled. No complete file was saved.",
			"Safe to retry. A retry creates a new transfer; any partial is retained briefly then cleaned.",
			true, false, true
	}
	if strings.Contains(lower, "deadline exceeded") || strings.Contains(lower, "timeout") {
		if size > 0 && bytesSent >= size && bytesSent > 0 {
			return "confirmation_timeout",
				"Transfer timed out waiting for confirmation. The file may already be saved.",
				"Check the receiver inbox before retrying. If missing, retry creates a new transfer.",
				false, true, true
		}
		return "transfer_timeout",
			"Transfer timed out before completing. No complete file was saved.",
			"Safe to retry when the peer is reachable. A retry creates a new transfer.",
			true, false, true
	}
	if strings.Contains(lower, "quota") || strings.Contains(lower, "too many") || strings.Contains(lower, "busy") || strings.Contains(lower, "503") {
		return "receiver_busy",
			"Receiver is busy. No data was sent.",
			"Wait a moment and retry. If it persists, check receiver load and try again later.",
			true, false, true
	}
	if strings.Contains(lower, "checksum") || strings.Contains(lower, "sha-256") || strings.Contains(lower, "sha256") || strings.Contains(lower, "final size mismatch") || strings.Contains(lower, "byte count mismatch") {
		return "integrity_rejected",
			"Transfer failed the integrity check. The file was not saved on the receiver.",
			"Safe to retry. If it repeats, check the source file and network stability.",
			true, false, true
	}
	if strings.Contains(lower, "drop rejected") {
		return "receiver_rejected",
			"Receiver rejected the transfer. No file was saved.",
			"Check the rejection reason in details. Fix the cause, then retry as a new transfer.",
			false, false, true
	}
	if strings.Contains(lower, "drop_ack") || strings.Contains(lower, "resume offset") || strings.Contains(lower, "invalid resume") || strings.Contains(lower, "resumption") {
		return "negotiation_failed",
			"Transfer could not start. No file was saved.",
			"Safe to retry. If it repeats, restart the Hub on both machines and try again.",
			true, false, true
	}
	// SendDrop only waits for drop_complete after streaming every byte. An
	// error mentioning drop_complete therefore means the payload left this
	// machine and the outcome is unknown — never an ordinary failure. These
	// stage-specific signals precede the generic connect/TLS match below so a
	// mid-stream "connection reset" is not mistaken for a pre-send dial error.
	if strings.Contains(lower, "drop_complete") || strings.Contains(lower, "receive drop_complete") {
		return "unknown_outcome",
			"Transfer may have completed but confirmation was lost. The file may already be saved.",
			"Check the receiver inbox before retrying to avoid a duplicate. Retrying creates a new transfer.",
			false, true, true
	}
	// Streaming errors (chunk send, payload read/write) happen mid-transfer:
	// no complete file was published, but a partial may be retained briefly.
	if strings.Contains(lower, "drop_data") || strings.Contains(lower, "read payload") || strings.Contains(lower, "write payload") || strings.Contains(lower, "chunk") {
		return "transfer_interrupted",
			"Transfer interrupted. No complete file was saved.",
			"Safe to retry. A retry creates a new transfer; any partial is retained briefly then cleaned.",
			true, false, true
	}
	if strings.Contains(lower, "dial") || strings.Contains(lower, "connection refused") || strings.Contains(lower, "no such host") || strings.Contains(lower, "i/o timeout") || strings.Contains(lower, "tls") || strings.Contains(lower, "certificate") || strings.Contains(lower, "not a trusted") || strings.Contains(lower, "not the configured") || strings.Contains(lower, "failed to connect") {
		return "destination_unreachable",
			"Could not reach the destination. No data was sent.",
			"Check the peer is online and paired, then retry. Use `tantu status` to verify trust.",
			true, false, true
	}
	// A bare "connect"/"connection" without stage context is treated as
	// unreachable only when nothing was negotiated; mid-stream resets are
	// covered by the drop_data branch above.
	if strings.Contains(lower, "connect") {
		return "destination_unreachable",
			"Could not reach the destination. No data was sent.",
			"Check the peer is online and paired, then retry. Use `tantu status` to verify trust.",
			true, false, true
	}
	// Generic send failure. If every byte was streamed but confirmation was
	// lost, the outcome is unknown — never report it as an ordinary failure.
	if size > 0 && bytesSent >= size && bytesSent > 0 {
		return "unknown_outcome",
			"Transfer may have completed but confirmation was lost. The file may already be saved.",
			"Check the receiver inbox before retrying to avoid a duplicate. Retrying creates a new transfer.",
			false, true, true
	}
	if bytesSent > 0 {
		return "transfer_interrupted",
			"Transfer interrupted. No complete file was saved.",
			"Safe to retry. A retry creates a new transfer; any partial is retained briefly then cleaned.",
			true, false, true
	}
	return "transfer_failed",
		"Transfer failed before sending. No data was sent.",
		"Check the error details, then retry. If the peer is offline, bring it online first.",
		true, false, true
}

// recordOutboundOperation appends sender-side truth to the ledger and
// persists it best-effort so a Hub restart or browser refresh keeps safe
// operation context. A persist failure never fails the upload itself.
func (h *Hub) recordOutboundOperation(op OperationRecord) {
	if h == nil {
		return
	}
	h.mu.RLock()
	ledger := h.outboundOps
	h.mu.RUnlock()
	if ledger == nil {
		ledger = NewOperationLedger(maxOutboundOperations)
		h.mu.Lock()
		if h.outboundOps == nil {
			h.outboundOps = ledger
		} else {
			ledger = h.outboundOps
		}
		h.mu.Unlock()
	}
	ledger.Add(op)
	if dir := h.hubStoreDir(); dir != "" {
		if err := persistOutboundOperations(dir, ledger.GetAll()); err != nil && h.logger != nil {
			h.logger.Warn(DomainDrop, fmt.Sprintf("Could not save transfer history: %v", err))
		}
	}
}

// listOutboundOperations returns a copy of the generation ledger.
func (h *Hub) listOutboundOperations() []OperationRecord {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	ledger := h.outboundOps
	h.mu.RUnlock()
	if ledger == nil {
		return nil
	}
	return ledger.GetAll()
}

// clearOutboundOperations empties the ledger and removes its persisted file
// (best-effort). Received files and staging state are unaffected: only
// sender-side metadata is deleted, and the deletion itself is logged.
func (h *Hub) clearOutboundOperations() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	ledger := h.outboundOps
	dir := h.storeDir
	h.mu.RUnlock()
	removed := 0
	if ledger != nil {
		removed = ledger.Clear()
	}
	if dir != "" {
		_ = os.Remove(filepath.Join(dir, transferPersistFileName))
	}
	if h.logger != nil {
		h.logger.Warn(DomainDrop, fmt.Sprintf("Transfer history cleared (%d record(s) removed)", removed))
	}
	return removed
}

// hubStoreDir returns the resolved store directory for persistence, or ""
// when the Hub has not started yet.
func (h *Hub) hubStoreDir() string {
	if h == nil {
		return ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.storeDir
}

// transferPersistEnvelope versions the on-disk ledger for forward
// compatibility: unknown fields are ignored on read.
type transferPersistEnvelope struct {
	Version    int               `json:"version"`
	Operations []OperationRecord `json:"operations"`
}

// pruneOperationsByAge drops entries older than the retention window,
// keeping the newest. It preserves input order (oldest first).
func pruneOperationsByAge(ops []OperationRecord) []OperationRecord {
	if len(ops) == 0 {
		return ops
	}
	cutoff := time.Now().AddDate(0, 0, -transferRetentionDays)
	kept := ops[:0]
	for _, op := range ops {
		if op.CreatedAt.IsZero() || !op.CreatedAt.Before(cutoff) {
			kept = append(kept, op)
		}
	}
	return kept
}

// validPersistedOperation reports whether a loaded record is safe to keep.
// Malformed or foreign entries are skipped rather than failing the load.
func validPersistedOperation(op OperationRecord) bool {
	if strings.TrimSpace(op.OperationID) == "" || op.CreatedAt.IsZero() {
		return false
	}
	switch op.Kind {
	case "text", "file":
	default:
		return false
	}
	switch op.State {
	case OperationStateCompleted, OperationStateRetryableFailure,
		OperationStateTerminalFailure, OperationStateCancelled,
		OperationStateDuplicateRisk, OperationStateSubmitted,
		OperationStateNegotiating, OperationStateSending,
		OperationStateVerifying:
	default:
		return false
	}
	return true
}

// writeAtomicTransferFile publishes data to dst with owner-only permissions
// via temp-file + fsync + rename, mirroring the peer store's crash safety
// (including the Windows replace-retry path). Single writer (the Hub);
// readers use atomic renames and never observe partial content.
func writeAtomicTransferFile(dst string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return fmt.Errorf("create transfer history directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary history file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0600); err != nil {
		cleanup()
		return fmt.Errorf("set history file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write history file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync history file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close history file: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		backupFile, backupErr := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".bak-*")
		if backupErr != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("create history replacement backup: %w", backupErr)
		}
		backupPath := backupFile.Name()
		_ = backupFile.Close()
		_ = os.Remove(backupPath)
		if backupErr = os.Rename(dst, backupPath); backupErr != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("preserve history before replacement: %w", backupErr)
		}
		if retryErr := os.Rename(tmpPath, dst); retryErr != nil {
			_ = os.Rename(backupPath, dst)
			_ = os.Remove(tmpPath)
			return fmt.Errorf("rename history file: %w", retryErr)
		}
		_ = os.Remove(backupPath)
	}
	return nil
}

// persistOutboundOperations writes the bounded, age-pruned ledger to the
// store directory. Records are metadata-only by construction.
func persistOutboundOperations(storeDir string, ops []OperationRecord) error {
	if strings.TrimSpace(storeDir) == "" {
		return fmt.Errorf("store directory is not configured")
	}
	ops = pruneOperationsByAge(ops)
	if len(ops) > maxOutboundOperations {
		ops = ops[len(ops)-maxOutboundOperations:]
	}
	data, err := json.MarshalIndent(transferPersistEnvelope{
		Version:    transferPersistVersion,
		Operations: ops,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal transfer history: %w", err)
	}
	return writeAtomicTransferFile(filepath.Join(storeDir, transferPersistFileName), data)
}

// loadOutboundOperations replaces the ledger with validated, pruned file
// contents. A missing file is a clean first run; a corrupt file warns and
// starts empty so the Hub never fails to start over history.
func (h *Hub) loadOutboundOperations(storeDir string) {
	if h == nil || strings.TrimSpace(storeDir) == "" {
		return
	}
	path := filepath.Join(storeDir, transferPersistFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) && h.logger != nil {
			h.logger.Warn(DomainDrop, fmt.Sprintf("Could not read transfer history: %v", err))
		}
		return
	}
	if len(raw) > maxTransferPersistBytes {
		if h.logger != nil {
			h.logger.Warn(DomainDrop, "Transfer history file exceeds size limit; starting fresh")
		}
		return
	}
	var env transferPersistEnvelope
	dec := json.NewDecoder(io.LimitReader(bytes.NewReader(raw), maxTransferPersistBytes+1))
	if err := dec.Decode(&env); err != nil {
		if h.logger != nil {
			h.logger.Warn(DomainDrop, fmt.Sprintf("Transfer history is corrupt; starting fresh: %v", err))
		}
		return
	}
	kept := make([]OperationRecord, 0, len(env.Operations))
	for _, op := range env.Operations {
		if validPersistedOperation(op) {
			kept = append(kept, op)
		}
	}
	kept = pruneOperationsByAge(kept)
	if len(kept) > maxOutboundOperations {
		kept = kept[len(kept)-maxOutboundOperations:]
	}
	h.mu.RLock()
	ledger := h.outboundOps
	h.mu.RUnlock()
	if ledger == nil {
		ledger = NewOperationLedger(maxOutboundOperations)
		h.mu.Lock()
		if h.outboundOps == nil {
			h.outboundOps = ledger
		} else {
			ledger = h.outboundOps
		}
		h.mu.Unlock()
	}
	ledger.mu.Lock()
	ledger.items = append(ledger.items[:0], kept...)
	ledger.mu.Unlock()
}

// resolveOperationDestination returns display metadata for the requested
// target without dialing. It never creates trust; it only describes what
// the Hub will attempt so the UI can show a destination before sending.
func (h *Hub) resolveOperationDestination(target string) (display, fingerprint, address string) {
	if h == nil {
		return strings.TrimSpace(target), "", strings.TrimSpace(target)
	}
	h.mu.RLock()
	store := h.store
	transportName := h.cfg.TransportType
	h.mu.RUnlock()
	if store == nil {
		trimmed := strings.TrimSpace(target)
		if trimmed == "" {
			trimmed = "default peer"
		}
		return trimmed, "", trimmed
	}
	query := strings.TrimSpace(target)
	if query == "" {
		if active := h.GetActivePeer(); active != "" {
			query = active
		}
	}
	if query == "" {
		peers := store.ListPeers()
		if len(peers) == 1 {
			return peers[0].DisplayName(), peers[0].Fingerprint, peers[0].Address
		}
		if len(peers) == 0 && transportName == "loopback" {
			// No peers to resolve, but loopback DialPeer("") dials the
			// local endpoint itself. Name that instead of implying a
			// peer that does not exist.
			return "local Hub", "", ""
		}
		return "default peer", "", ""
	}
	if p, err := store.ResolvePeer(query); err == nil && p != nil {
		return p.DisplayName(), p.Fingerprint, p.Address
	}
	// Unresolvable (offline address, loopback endpoint, or stale selection):
	// echo the request so the UI never substitutes a different peer silently.
	return query, "", query
}

// writeTransferError emits the canonical error contract. `message` stays
// compatible with older clients (short technical summary); `plain_message`
// is the user-facing sentence, plus explicit safety flags and one next
// action. No payload content is ever included.
func writeTransferError(w http.ResponseWriter, httpStatus int, message, code, plain, technical string, retrySafe, duplicateRisk, dataSafe bool, nextAction, opID, dest, destFP, destAddr string) {
	payload := map[string]any{
		"status":         "error",
		"message":        message,
		"code":           code,
		"plain_message":  plain,
		"retry_safe":     retrySafe,
		"duplicate_risk": duplicateRisk,
		"data_safe":      dataSafe,
		"next_action":    nextAction,
	}
	if technical != "" {
		payload["technical_details"] = technical
	}
	if opID != "" {
		payload["operation_id"] = opID
		payload["diagnostic_id"] = opID
	}
	if dest != "" {
		payload["destination"] = dest
	}
	if destFP != "" {
		payload["destination_fingerprint"] = destFP
	}
	if destAddr != "" {
		payload["destination_address"] = destAddr
	}
	writeJSON(w, httpStatus, payload)
}

// operationStateForFailure maps safety flags to a user-visible terminal state.
func operationStateForFailure(retrySafe, duplicateRisk, cancelled bool) string {
	if cancelled {
		return OperationStateCancelled
	}
	if duplicateRisk {
		return OperationStateDuplicateRisk
	}
	if retrySafe {
		return OperationStateRetryableFailure
	}
	return OperationStateTerminalFailure
}
