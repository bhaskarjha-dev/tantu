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
	// HeadHash is the SHA-256 of the first 64KB. Resume is impossible
	// without it (the Hub restarts the transfer instead) and it is verified
	// when the partial is at least 64KB; omitempty reflects the wire shape,
	// not optionality.
	HeadHash string `json:"head_hash,omitempty"`
	// IdempotencyKey is the sender's logical-operation key (see
	// docs/SPEC-WIRE-VERSIONING.md). Receivers validate it: a completed
	// transfer records a completion tombstone under the key, a retry with
	// the same metadata re-verifies the payload digest against that record
	// instead of republishing, and reuse with different metadata or a
	// differing digest is refused outright.
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
	// Duplicate is set when the payload matched a completion tombstone and
	// nothing was staged or published. The wire reply is still a success —
	// the transfer is not a failure — but a sender that cannot see this bit
	// would tell the user a second copy exists when it does not, so it is
	// additive: peers that predate it ignore the field.
	Duplicate bool `json:"duplicate,omitempty"`
}

// RejectionError reports that a receiver answered a transfer with an explicit
// failure, so the outcome is known rather than merely unknown.
//
// This distinction is the difference between an honest and a dishonest error
// message. When every byte has been streamed and the connection then dies, the
// sender genuinely does not know whether the file landed, and "it may already
// be saved" is the only truthful thing it can say. But when the receiver sends
// a drop_complete carrying a specific rejection reason, the receiver has told
// the sender exactly what happened. Reporting that as an unknown outcome is
// not caution, it is a false warning: it tells the user to go and check a
// receiver inbox for a file that was definitively not saved, and it marks a
// clean, retryable failure as unsafe to retry.
//
// The reason travels verbatim from the receiver, so callers must still treat it
// as untrusted input for display purposes.
type RejectionError struct {
	// Reason is the receiver's own explanation, carried through unchanged.
	Reason string
	// BytesReceived is what the receiver had taken when it rejected. It is
	// diagnostic only: a rejection means nothing was published regardless.
	BytesReceived int64
}

func (e *RejectionError) Error() string {
	if e == nil {
		return "receiver rejected the transfer"
	}
	return "drop failed: " + e.Reason
}

// Is lets errors.Is match any rejection, so a caller can ask "did the receiver
// answer?" without depending on the reason's wording.
func (e *RejectionError) Is(target error) bool {
	_, ok := target.(*RejectionError)
	return ok
}
