package drop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// DefaultMaxDropSize is the maximum default size (5GB) of a payload accepted by a receiver.
const DefaultMaxDropSize int64 = 5 * 1024 * 1024 * 1024

// DefaultMaxTextSize is the maximum default text payload size (10 MiB). It is
// the single source of truth shared by the Hub, the standalone receivers, the
// CLI stdin/argv guards, and the dashboard client-side check.
const DefaultMaxTextSize int64 = 10 * 1024 * 1024

// ReceiveDropConfig configures a drop receive operation.
type ReceiveDropConfig struct {
	Timeout     time.Duration // Max time for the entire operation (default: 5 min)
	MaxSize     int64         // Maximum total payload size to accept (default: 5GB). <0 = unlimited.
	MaxTextSize int64         // Maximum text payload size (default: 10 MiB). <0 = unlimited.
	// Quota optionally bounds concurrent transfer reservations across sessions.
	// Nil preserves the standalone library's historical unlimited behavior;
	// long-lived receivers should provide DefaultTransferQuota().
	Quota *TransferQuota
	// TouchActivity optionally refreshes a receiver's cross-process staging
	// activity marker while data is arriving. It is called with the validated
	// drop metadata before each non-empty data chunk is written.
	TouchActivity  func(DropSend) error
	ReceivedBytes  int64                                  // Initial existing bytes for resumption (default 0)
	OnMeta         func(meta DropSend) (io.Writer, error) // Optional: if set and w is nil, called to obtain writer
	BeforeComplete func(*ReceiveDropResult) error         // Optional finalization hook run before success is acknowledged
	// AttemptID is local correlation state for callbacks. It is never sent on
	// the wire; the dispatcher supplies one value per receive attempt.
	AttemptID string
}

type dropEnvelopeResult struct {
	env *protocol.Envelope
	err error
}

func closeDropConnOnContext(ctx context.Context, conn transport.Conn) func() {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	return func() { stop() }
}

func receiveDropEnvelope(ctx context.Context, conn transport.Conn) (*protocol.Envelope, error) {
	dl, hasDeadliner := conn.(transport.Deadliner)
	if supported, ok := conn.(interface{ DeadlineSupported() bool }); ok && !supported.DeadlineSupported() {
		hasDeadliner = false
	}
	stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopConn()
	if hasDeadliner {
		if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
			_ = dl.SetReadDeadline(deadline)
			defer func() { _ = dl.SetReadDeadline(time.Time{}) }()
		}
		return conn.Receive()
	}
	resultCh := make(chan dropEnvelopeResult, 1)
	go func() {
		env, err := conn.Receive()
		resultCh <- dropEnvelopeResult{env: env, err: err}
	}()
	select {
	case <-ctx.Done():
		_ = conn.Close()
		return nil, ctx.Err()
	case result := <-resultCh:
		return result.env, result.err
	}
}

// ReceiveDropResult contains the result of a successful drop receive.
type ReceiveDropResult struct {
	Meta            DropSend // The metadata from the sender
	BytesWritten    int64    // Total bytes written to the output
	SHA256          string   // Verified SHA-256 hex digest
	PeerFingerprint string   // Verified cryptographic fingerprint of peer (if mTLS)
	PeerAddress     string   // Remote network address of the sender
}

func validateDropMetadata(meta DropSend) error {
	if strings.TrimSpace(meta.DropID) == "" {
		return errors.New("drop_id is required")
	}
	if len(meta.DropID) > MaxDropIDLength {
		return fmt.Errorf("drop_id exceeds %d bytes", MaxDropIDLength)
	}
	for _, r := range meta.DropID {
		// Drop IDs are used as filenames for private staging files by the
		// built-in receivers. Keep them to an identifier alphabet rather than
		// merely printable text: a peer must not be able to smuggle path
		// separators, drive prefixes, or alternate-stream syntax into a
		// staging path.
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return errors.New("drop_id contains an invalid character")
	}
	if meta.DropID == "." || meta.DropID == ".." {
		return errors.New("drop_id cannot be a path segment")
	}
	baseID := meta.DropID
	if dot := strings.IndexByte(baseID, '.'); dot >= 0 {
		baseID = baseID[:dot]
	}
	switch strings.ToUpper(baseID) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return errors.New("drop_id cannot be a reserved device name")
	}
	// The idempotency key rides the same staging-path-adjacent trust as the
	// DropID (it will key future tombstones), so it earns the same alphabet
	// and length discipline. Receivers validate only; no duplicate
	// suppression happens yet.
	if len(meta.IdempotencyKey) > MaxDropIDLength {
		return fmt.Errorf("idempotency_key exceeds %d bytes", MaxDropIDLength)
	}
	for _, r := range meta.IdempotencyKey {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return errors.New("idempotency_key contains an invalid character")
	}
	if meta.Kind != DropKindText && meta.Kind != DropKindFile {
		return fmt.Errorf("unsupported drop kind %q", meta.Kind)
	}
	if meta.Size < 0 {
		return errors.New("drop size cannot be negative")
	}
	if meta.ChunkSize < 0 || meta.ChunkSize > MaxChunkSize {
		return fmt.Errorf("invalid chunk size %d (max %d)", meta.ChunkSize, MaxChunkSize)
	}
	if len(meta.Name) > MaxDropNameLength {
		return fmt.Errorf("drop name exceeds %d bytes", MaxDropNameLength)
	}
	for _, r := range meta.Name {
		if r < 0x20 || r == 0x7f {
			return errors.New("drop name contains a control character")
		}
	}
	if len(meta.MIMEType) > MaxMIMETypeLength {
		return fmt.Errorf("MIME type exceeds %d bytes", MaxMIMETypeLength)
	}
	for _, r := range meta.MIMEType {
		if r < 0x20 || r == 0x7f {
			return errors.New("MIME type contains a control character")
		}
	}
	if meta.HeadHash != "" {
		decoded, err := hex.DecodeString(meta.HeadHash)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("head_hash must be a SHA-256 hex digest")
		}
	}
	return nil
}

// ReceiveDrop waits for an incoming drop on the transport connection,
// writes the payload to w, and returns metadata about what was received.
// Returns error if the operation times out, is cancelled, or fails.
func ReceiveDrop(ctx context.Context, conn transport.Conn, w io.Writer, cfg ReceiveDropConfig) (*ReceiveDropResult, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	stopContextClose := closeDropConnOnContext(ctx, conn)
	defer stopContextClose()

	maxSize := cfg.MaxSize
	if maxSize == 0 {
		maxSize = DefaultMaxDropSize
	} else if maxSize < 0 {
		maxSize = 0 // no limit
	}
	maxTextSize := cfg.MaxTextSize
	if maxTextSize == 0 {
		maxTextSize = DefaultMaxTextSize
	} else if maxTextSize < 0 {
		maxTextSize = 0 // no limit
	}

	// 1. Wait for TypeDropSend message
	var meta DropSend
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		env, err := receiveDropEnvelope(ctx, conn)
		if err != nil {
			return nil, fmt.Errorf("receive drop_send: %w", err)
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			_ = env.DecodePayload(&hb)
			_ = conn.Send(protocol.TypeHeartbeat, hb)
			continue
		}
		if env.Type != TypeDropSend {
			return nil, fmt.Errorf("expected %q, got %q", TypeDropSend, env.Type)
		}
		if err := env.DecodePayload(&meta); err != nil {
			return nil, fmt.Errorf("decode drop_send: %w", err)
		}
		if err := validateDropMetadata(meta); err != nil {
			_ = conn.Send(TypeDropAck, DropAck{DropID: meta.DropID, Accepted: false, Error: err.Error()})
			return nil, fmt.Errorf("invalid drop metadata: %w", err)
		}
		break
	}
	// Normalize the negotiated chunk size before recording/reusing staging
	// metadata. A zero value is valid on the wire and means the protocol
	// default; persisting the literal zero would make a later retry look like a
	// different transfer.
	if meta.ChunkSize <= 0 {
		meta.ChunkSize = DefaultChunkSize
	}
	if cfg.AttemptID != "" {
		meta.AttemptID = cfg.AttemptID
	}

	// 2. Validate declared size against MaxSize
	if maxSize > 0 && meta.Size > 0 && meta.Size > maxSize {
		errMsg := fmt.Sprintf("drop size %d exceeds max size %d", meta.Size, maxSize)
		_ = conn.Send(TypeDropAck, DropAck{
			DropID:   meta.DropID,
			Accepted: false,
			Error:    errMsg,
		})
		return nil, errors.New(errMsg)
	}
	if meta.Kind == DropKindText && maxTextSize > 0 && meta.Size > maxTextSize {
		errMsg := fmt.Sprintf("text drop size %d exceeds max size %d", meta.Size, maxTextSize)
		_ = conn.Send(TypeDropAck, DropAck{
			DropID:   meta.DropID,
			Accepted: false,
			Error:    errMsg,
		})
		return nil, errors.New(errMsg)
	}
	if cfg.ReceivedBytes < 0 {
		errMsg := "received_bytes cannot be negative"
		_ = conn.Send(TypeDropAck, DropAck{DropID: meta.DropID, Error: errMsg})
		return nil, errors.New(errMsg)
	}
	if meta.Size > 0 && cfg.ReceivedBytes > meta.Size {
		errMsg := "received_bytes exceeds declared drop size"
		_ = conn.Send(TypeDropAck, DropAck{DropID: meta.DropID, Error: errMsg})
		return nil, errors.New(errMsg)
	}

	// Reserve receiver capacity only after the complete metadata has passed
	// validation. The reservation covers the whole receive attempt and is
	// released on every return path, including quota rejection and context
	// cancellation.
	var lease *TransferLease
	if cfg.Quota != nil {
		peerKey := transport.GetPeerFingerprint(conn)
		if peerKey == "" && conn.RemoteAddr() != nil {
			peerKey = conn.RemoteAddr().String()
		}
		var quotaErr error
		lease, quotaErr = cfg.Quota.Acquire(peerKey, meta.Size)
		if quotaErr != nil {
			_ = conn.Send(TypeDropAck, DropAck{
				DropID:   meta.DropID,
				Accepted: false,
				Error:    quotaErr.Error(),
			})
			return nil, quotaErr
		}
		defer lease.Release()
	}

	// 3. Resolve destination writer
	dest := w
	if dest == nil && cfg.OnMeta != nil {
		var err error
		dest, err = cfg.OnMeta(meta)
		if err != nil {
			_ = conn.Send(TypeDropAck, DropAck{
				DropID:   meta.DropID,
				Accepted: false,
				Error:    err.Error(),
			})
			return nil, err
		}
	}
	if dest == nil {
		_ = conn.Send(TypeDropAck, DropAck{
			DropID:   meta.DropID,
			Accepted: false,
			Error:    "no destination writer provided",
		})
		return nil, errors.New("no destination writer provided")
	}

	if cfg.ReceivedBytes == 0 {
		if rb, ok := dest.(interface{ ReceivedBytes() int64 }); ok {
			cfg.ReceivedBytes = rb.ReceivedBytes()
		} else if seeker, ok := dest.(io.Seeker); ok {
			if offset, err := seeker.Seek(0, io.SeekCurrent); err == nil && offset > 0 {
				cfg.ReceivedBytes = offset
			}
		}
	}
	if cfg.ReceivedBytes < 0 || (meta.Size > 0 && cfg.ReceivedBytes > meta.Size) {
		errMsg := "invalid existing byte count for resumption"
		_ = conn.Send(TypeDropAck, DropAck{DropID: meta.DropID, Error: errMsg})
		return nil, errors.New(errMsg)
	}

	// 4. Send TypeDropAck accepted
	if err := conn.Send(TypeDropAck, DropAck{
		DropID:        meta.DropID,
		Accepted:      true,
		ReceivedBytes: cfg.ReceivedBytes,
	}); err != nil {
		return nil, fmt.Errorf("send drop_ack: %w", err)
	}

	// 5. Receive TypeDropData chunks
	chunkSize := meta.ChunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if chunkSize > MaxChunkSize {
		errMsg := fmt.Sprintf("chunk size %d exceeds maximum %d", chunkSize, MaxChunkSize)
		_ = conn.Send(TypeDropComplete, DropComplete{DropID: meta.DropID, Error: errMsg})
		return nil, errors.New(errMsg)
	}
	if cfg.ReceivedBytes > 0 && cfg.ReceivedBytes%int64(chunkSize) != 0 {
		errMsg := "received_bytes is not chunk-aligned"
		_ = conn.Send(TypeDropComplete, DropComplete{DropID: meta.DropID, Error: errMsg})
		return nil, errors.New(errMsg)
	}
	expectedIndex := 0
	if cfg.ReceivedBytes > 0 {
		expectedIndex = int(cfg.ReceivedBytes / int64(chunkSize))
	}
	totalBytes := cfg.ReceivedBytes
	hasher := sha256.New()

	// If resuming from existing bytes, require a rewindable destination. A
	// writer that cannot be rewound would otherwise produce a transfer whose
	// advertised checksum covers only the newly received suffix.
	if cfg.ReceivedBytes > 0 {
		seeker, ok := dest.(io.ReadSeeker)
		if !ok {
			errMsg := "resume requires a rewindable destination"
			_ = conn.Send(TypeDropComplete, DropComplete{DropID: meta.DropID, Error: errMsg})
			return nil, errors.New(errMsg)
		}
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			errMsg := fmt.Sprintf("seek partial payload: %v", err)
			_ = conn.Send(TypeDropComplete, DropComplete{DropID: meta.DropID, Error: errMsg})
			return nil, errors.New(errMsg)
		}
		if _, err := io.CopyN(hasher, seeker, cfg.ReceivedBytes); err != nil {
			errMsg := fmt.Sprintf("read partial payload: %v", err)
			_ = conn.Send(TypeDropComplete, DropComplete{DropID: meta.DropID, Error: errMsg})
			return nil, errors.New(errMsg)
		}
		if _, err := seeker.Seek(cfg.ReceivedBytes, io.SeekStart); err != nil {
			errMsg := fmt.Sprintf("seek to resume point: %v", err)
			_ = conn.Send(TypeDropComplete, DropComplete{DropID: meta.DropID, Error: errMsg})
			return nil, errors.New(errMsg)
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			_ = conn.Send(TypeDropComplete, DropComplete{
				DropID:    meta.DropID,
				Success:   false,
				BytesRecv: totalBytes,
				Error:     err.Error(),
			})
			return nil, err
		}
		env, err := receiveDropEnvelope(ctx, conn)
		if err != nil {
			return nil, fmt.Errorf("receive drop_data: %w", err)
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			_ = env.DecodePayload(&hb)
			_ = conn.Send(protocol.TypeHeartbeat, hb)
			continue
		}
		if env.Type != TypeDropData {
			errMsg := fmt.Sprintf("expected %q, got %q", TypeDropData, env.Type)
			_ = conn.Send(TypeDropComplete, DropComplete{
				DropID:    meta.DropID,
				Success:   false,
				BytesRecv: totalBytes,
				Error:     errMsg,
			})
			return nil, errors.New(errMsg)
		}

		var chunk DropData
		if err := env.DecodePayload(&chunk); err != nil {
			errMsg := fmt.Sprintf("decode drop_data: %v", err)
			_ = conn.Send(TypeDropComplete, DropComplete{
				DropID:    meta.DropID,
				Success:   false,
				BytesRecv: totalBytes,
				Error:     errMsg,
			})
			return nil, errors.New(errMsg)
		}

		if chunk.DropID != meta.DropID {
			errMsg := fmt.Sprintf("chunk drop_id mismatch: expected %s, got %s", meta.DropID, chunk.DropID)
			_ = conn.Send(TypeDropComplete, DropComplete{
				DropID:    meta.DropID,
				Success:   false,
				BytesRecv: totalBytes,
				Error:     errMsg,
			})
			return nil, errors.New(errMsg)
		}

		if len(chunk.Data) > chunkSize {
			errMsg := fmt.Sprintf("chunk data exceeds negotiated chunk size %d", chunkSize)
			_ = conn.Send(TypeDropComplete, DropComplete{DropID: meta.DropID, Error: errMsg})
			return nil, errors.New(errMsg)
		}
		if !chunk.Final && len(chunk.Data) != chunkSize {
			errMsg := fmt.Sprintf("non-final chunk has %d bytes; expected %d", len(chunk.Data), chunkSize)
			_ = conn.Send(TypeDropComplete, DropComplete{DropID: meta.DropID, Error: errMsg})
			return nil, errors.New(errMsg)
		}
		if meta.Size > 0 && totalBytes+int64(len(chunk.Data)) > meta.Size {
			errMsg := "received data exceeds declared drop size"
			_ = conn.Send(TypeDropComplete, DropComplete{DropID: meta.DropID, Error: errMsg})
			return nil, errors.New(errMsg)
		}
		if chunk.ChunkIndex != expectedIndex {
			errMsg := fmt.Sprintf("out-of-order chunk: expected index %d, got %d", expectedIndex, chunk.ChunkIndex)
			_ = conn.Send(TypeDropComplete, DropComplete{
				DropID:    meta.DropID,
				Success:   false,
				BytesRecv: totalBytes,
				Error:     errMsg,
			})
			return nil, errors.New(errMsg)
		}

		if maxSize > 0 && (totalBytes+int64(len(chunk.Data))) > maxSize {
			errMsg := fmt.Sprintf("received bytes exceed max size %d", maxSize)
			_ = conn.Send(TypeDropComplete, DropComplete{
				DropID:    meta.DropID,
				Success:   false,
				BytesRecv: totalBytes,
				Error:     errMsg,
			})
			return nil, errors.New(errMsg)
		}
		if meta.Kind == DropKindText && maxTextSize > 0 && totalBytes+int64(len(chunk.Data)) > maxTextSize {
			errMsg := fmt.Sprintf("received text bytes exceed max size %d", maxTextSize)
			_ = conn.Send(TypeDropComplete, DropComplete{
				DropID:    meta.DropID,
				Success:   false,
				BytesRecv: totalBytes,
				Error:     errMsg,
			})
			return nil, errors.New(errMsg)
		}

		if len(chunk.Data) > 0 {
			if cfg.TouchActivity != nil {
				if err := cfg.TouchActivity(meta); err != nil {
					_ = conn.Send(TypeDropComplete, DropComplete{
						DropID:    meta.DropID,
						Success:   false,
						BytesRecv: totalBytes,
						Error:     err.Error(),
					})
					return nil, err
				}
			}
			if err := lease.AddBytes(int64(len(chunk.Data))); err != nil {
				_ = conn.Send(TypeDropComplete, DropComplete{
					DropID:    meta.DropID,
					Success:   false,
					BytesRecv: totalBytes,
					Error:     err.Error(),
				})
				return nil, err
			}
			n, err := dest.Write(chunk.Data)
			if err == nil && n != len(chunk.Data) {
				err = io.ErrShortWrite
			}
			if err != nil {
				errMsg := fmt.Sprintf("write payload: %v", err)
				_ = conn.Send(TypeDropComplete, DropComplete{
					DropID:    meta.DropID,
					Success:   false,
					BytesRecv: totalBytes,
					Error:     errMsg,
				})
				return nil, err
			}
			hasher.Write(chunk.Data)
			// Staged files are resumed from their on-disk length after a
			// restart. Sync each completed chunk before that length can be
			// observed by a later attempt; non-file writers (text buffers and
			// test sinks) simply do not expose Sync.
			if syncer, ok := dest.(interface{ Sync() error }); ok {
				if err := syncer.Sync(); err != nil {
					errMsg := fmt.Sprintf("sync payload chunk: %v", err)
					_ = conn.Send(TypeDropComplete, DropComplete{
						DropID:    meta.DropID,
						Success:   false,
						BytesRecv: totalBytes,
						Error:     errMsg,
					})
					return nil, errors.New(errMsg)
				}
			}
			totalBytes += int64(n)
		}

		expectedIndex++

		if chunk.Final {
			if meta.Size > 0 && totalBytes != meta.Size {
				errMsg := fmt.Sprintf("final size mismatch: got %d, want %d", totalBytes, meta.Size)
				_ = conn.Send(TypeDropComplete, DropComplete{DropID: meta.DropID, Error: errMsg})
				return nil, errors.New(errMsg)
			}
			decodedHash, hashErr := hex.DecodeString(chunk.SHA256)
			if hashErr != nil || len(decodedHash) != sha256.Size {
				errMsg := "final chunk must include a valid SHA-256 checksum"
				_ = conn.Send(TypeDropComplete, DropComplete{DropID: meta.DropID, Error: errMsg})
				return nil, errors.New(errMsg)
			}
			computedSHA := hex.EncodeToString(hasher.Sum(nil))
			if !strings.EqualFold(computedSHA, chunk.SHA256) {
				errMsg := fmt.Sprintf("SHA-256 checksum mismatch: expected %s, got %s", chunk.SHA256, computedSHA)
				_ = conn.Send(TypeDropComplete, DropComplete{
					DropID:    meta.DropID,
					Success:   false,
					BytesRecv: totalBytes,
					Error:     errMsg,
				})
				return nil, errors.New(errMsg)
			}
			break
		}
	}

	computedSHA := hex.EncodeToString(hasher.Sum(nil))
	peerFP := transport.GetPeerFingerprint(conn)
	peerAddr := ""
	if conn.RemoteAddr() != nil {
		peerAddr = conn.RemoteAddr().String()
	}
	result := &ReceiveDropResult{
		Meta:            meta,
		BytesWritten:    totalBytes,
		SHA256:          computedSHA,
		PeerFingerprint: peerFP,
		PeerAddress:     peerAddr,
	}
	if cfg.BeforeComplete != nil {
		if err := cfg.BeforeComplete(result); err != nil {
			_ = conn.Send(TypeDropComplete, DropComplete{
				DropID:    meta.DropID,
				Success:   false,
				BytesRecv: totalBytes,
				SHA256:    computedSHA,
				Error:     err.Error(),
			})
			return nil, err
		}
	}

	// 6. Send TypeDropComplete
	if err := conn.Send(TypeDropComplete, DropComplete{
		DropID:    meta.DropID,
		Success:   true,
		BytesRecv: totalBytes,
		SHA256:    computedSHA,
	}); err != nil {
		return nil, fmt.Errorf("send drop_complete: %w", err)
	}

	return result, nil
}
