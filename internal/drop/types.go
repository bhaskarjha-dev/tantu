// Package drop implements the QuickDrop cross-machine content sharing protocol,
// allowing paired machines to exchange text snippets and files over encrypted transports.
package drop

// Message type constants for QuickDrop protocol
const (
	TypeDropSend     = "drop_send"     // Sender → Receiver: metadata about the drop
	TypeDropAck      = "drop_ack"      // Receiver → Sender: acceptance/rejection
	TypeDropData     = "drop_data"     // Sender → Receiver: payload chunk
	TypeDropComplete = "drop_complete" // Receiver → Sender: receipt confirmation
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
	ChunkIndex int    `json:"chunk_index"` // 0-based chunk number
	Data       []byte `json:"data"`        // Raw bytes (base64 in JSON wire format)
	Final      bool   `json:"final"`       // True if this is the last chunk
	SHA256     string `json:"sha256,omitempty"` // Hex-encoded SHA-256 digest of entire payload (present on Final=true)
}

// DropComplete: Receiver → Sender. "Got it."
type DropComplete struct {
	DropID    string `json:"drop_id"`
	Success   bool   `json:"success"`
	BytesRecv int64  `json:"bytes_received"` // Total bytes received (for verification)
	SHA256    string `json:"sha256,omitempty"` // Receiver's verified SHA-256 hex digest
	Error     string `json:"error,omitempty"`
}
