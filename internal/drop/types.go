// Package drop implements the QuickDrop cross-machine content sharing protocol,
// allowing paired machines to exchange text snippets and files over encrypted transports.
package drop

// Message type constants for QuickDrop protocol
const (
	TypeDropSend     = "drop_send"     // Sender → Receiver: metadata about the drop
	TypeDropAck      = "drop_ack"      // Receiver → Sender: acceptance/rejection
	TypeDropData     = "drop_data"     // Sender → Receiver: payload chunk
	TypeDropComplete = "drop_complete" // Receiver → Sender: receipt confirmation

	// MaxChunkSize bounds memory use for a single decoded JSON payload.  The
	// wire envelope must also fit the transport's frame limit after base64
	// expansion; 2 MiB leaves ample room for that expansion.
	MaxChunkSize = 2 * 1024 * 1024

	// MaxDropIDLength and MaxDropNameLength keep metadata from becoming an
	// accidental memory/indexing sink on otherwise small protocol frames.
	MaxDropIDLength   = 128
	MaxDropNameLength = 4096
	MaxMIMETypeLength = 255
)

// DropKind identifies what type of content is being dropped.
type DropKind string

const (
	DropKindText DropKind = "text" // Plain text content (inline in DropData.Data)
	DropKindFile DropKind = "file" // File content (may be chunked across multiple DropData messages)
)

// DropSend: Sender → Receiver. "I want to send you this."
type DropSend struct {
	DropID    string   `json:"drop_id"`             // Unique ID for this drop session
	Kind      DropKind `json:"kind"`                // "text" or "file"
	Name      string   `json:"name,omitempty"`      // Filename (for files) or label (for text)
	Size      int64    `json:"size"`                // Total size in bytes (0 if unknown for stdin pipe)
	MIMEType  string   `json:"mime_type,omitempty"` // MIME type hint (e.g. "text/plain", "image/png")
	ChunkSize int      `json:"chunk_size"`          // Max bytes per DropData chunk (default 1MB)
	HeadHash  string   `json:"head_hash,omitempty"` // SHA-256 of first 64KB for resumption file identity check
	// IdempotencyKey is the sender's logical-operation key, reserved for
	// idempotent retry (see docs/SPEC-WIRE-VERSIONING.md). Receivers validate
	// it but take no duplicate-suppression action yet; retries remain
	// at-least-once with explicit duplicate-risk UX.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// AttemptID is local dispatcher state and is never serialized on the wire.
	// It lets receiver callbacks distinguish concurrent attempts that reuse a
	// DropID, so cleanup for one attempt cannot tear down another.
	AttemptID string `json:"-"`
}

// DropAck: Receiver → Sender. "Go ahead" or "Rejected."
type DropAck struct {
	DropID        string `json:"drop_id"`
	Accepted      bool   `json:"accepted"`
	ReceivedBytes int64  `json:"received_bytes,omitempty"` // Chunk-aligned existing bytes on receiver for resumption
	Error         string `json:"error,omitempty"`          // Non-empty if rejected
}

// DropData: Sender → Receiver. One chunk of payload.
type DropData struct {
	DropID     string `json:"drop_id"`
	ChunkIndex int    `json:"chunk_index"`      // 0-based chunk number
	Data       []byte `json:"data"`             // Raw bytes (base64 in JSON wire format)
	Final      bool   `json:"final"`            // True if this is the last chunk
	SHA256     string `json:"sha256,omitempty"` // Hex-encoded SHA-256 digest of entire payload (present on Final=true)
}

// DropComplete: Receiver → Sender. "Got it."
type DropComplete struct {
	DropID    string `json:"drop_id"`
	Success   bool   `json:"success"`
	BytesRecv int64  `json:"bytes_received"`   // Total bytes received (for verification)
	SHA256    string `json:"sha256,omitempty"` // Receiver's verified SHA-256 hex digest
	Error     string `json:"error,omitempty"`
}
