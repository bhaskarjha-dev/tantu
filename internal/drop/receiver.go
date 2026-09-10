package drop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// DefaultMaxDropSize is the maximum default size (5GB) of a payload accepted by a receiver.
const DefaultMaxDropSize int64 = 5 * 1024 * 1024 * 1024

// ReceiveDropConfig configures a drop receive operation.
type ReceiveDropConfig struct {
	Timeout       time.Duration                          // Max time for the entire operation (default: 5 min)
	MaxSize       int64                                  // Maximum total payload size to accept (default: 5GB). <0 = unlimited.
	ReceivedBytes int64                                  // Initial existing bytes for resumption (default 0)
	OnMeta        func(meta DropSend) (io.Writer, error) // Optional: if set and w is nil, called to obtain writer
}

// ReceiveDropResult contains the result of a successful drop receive.
type ReceiveDropResult struct {
	Meta            DropSend // The metadata from the sender
	BytesWritten    int64    // Total bytes written to the output
	SHA256          string   // Verified SHA-256 hex digest
	PeerFingerprint string   // Verified cryptographic fingerprint of peer (if mTLS)
	PeerAddress     string   // Remote network address of the sender
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

	maxSize := cfg.MaxSize
	if maxSize == 0 {
		maxSize = DefaultMaxDropSize
	} else if maxSize < 0 {
		maxSize = 0 // no limit
	}

	// 1. Wait for TypeDropSend message
	var meta DropSend
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		env, err := conn.Receive()
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
		break
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
	expectedIndex := 0
	if cfg.ReceivedBytes > 0 {
		expectedIndex = int(cfg.ReceivedBytes / int64(chunkSize))
	}
	totalBytes := cfg.ReceivedBytes
	hasher := sha256.New()

	// If resuming from existing bytes, pre-hash existing bytes if destination is readable
	if cfg.ReceivedBytes > 0 {
		if seeker, ok := dest.(io.ReadSeeker); ok {
			if _, err := seeker.Seek(0, io.SeekStart); err == nil {
				_, _ = io.CopyN(hasher, seeker, cfg.ReceivedBytes)
				_, _ = seeker.Seek(cfg.ReceivedBytes, io.SeekStart)
			}
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
		env, err := conn.Receive()
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

		if len(chunk.Data) > 0 {
			n, err := dest.Write(chunk.Data)
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
			totalBytes += int64(n)
		}

		expectedIndex++

		if chunk.Final {
			if chunk.SHA256 != "" {
				computedSHA := hex.EncodeToString(hasher.Sum(nil))
				if computedSHA != chunk.SHA256 {
					errMsg := fmt.Sprintf("SHA-256 checksum mismatch: expected %s, got %s", chunk.SHA256, computedSHA)
					_ = conn.Send(TypeDropComplete, DropComplete{
						DropID:    meta.DropID,
						Success:   false,
						BytesRecv: totalBytes,
						Error:     errMsg,
					})
					return nil, errors.New(errMsg)
				}
			}
			break
		}
	}

	computedSHA := hex.EncodeToString(hasher.Sum(nil))

	// 6. Send TypeDropComplete
	if err := conn.Send(TypeDropComplete, DropComplete{
		DropID:    meta.DropID,
		Success:   true,
		BytesRecv: totalBytes,
		SHA256:    computedSHA,
	}); err != nil {
		return nil, fmt.Errorf("send drop_complete: %w", err)
	}

	peerFP := transport.GetPeerFingerprint(conn)
	peerAddr := ""
	if conn.RemoteAddr() != nil {
		peerAddr = conn.RemoteAddr().String()
	}

	return &ReceiveDropResult{
		Meta:            meta,
		BytesWritten:    totalBytes,
		SHA256:          computedSHA,
		PeerFingerprint: peerFP,
		PeerAddress:     peerAddr,
	}, nil
}
