package drop

import (
	"context"
	"crypto/rand"
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

// DefaultChunkSize is the maximum size of a single DropData chunk (1MB).
const DefaultChunkSize = 1 << 20

// SendDropConfig configures a drop send operation.
type SendDropConfig struct {
	// OnAck optionally observes the receiver's accepted resume offset. It is
	// local test/diagnostic instrumentation and is not sent on the wire.
	OnAck   func(DropAck)
	Timeout time.Duration // Max time for the entire operation (default: 5 min)
}

func generateDropID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("drop-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// SendDrop sends content over a transport connection to a receiver.
// meta contains the drop metadata (kind, name, size, etc.).
// payload is an io.Reader providing the content bytes.
// Returns nil on success, error on failure or rejection.
func SendDrop(ctx context.Context, conn transport.Conn, meta DropSend, payload io.Reader, cfg SendDropConfig) error {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	stopContextClose := closeDropConnOnContext(ctx, conn)
	defer stopContextClose()

	if meta.DropID == "" {
		meta.DropID = generateDropID()
	}
	if meta.ChunkSize <= 0 {
		meta.ChunkSize = DefaultChunkSize
	}
	if err := validateDropMetadata(meta); err != nil {
		return err
	}
	if payload == nil {
		return errors.New("drop payload is nil")
	}

	// 1. Send DropSend metadata
	if err := conn.Send(TypeDropSend, meta); err != nil {
		return fmt.Errorf("send drop_send: %w", err)
	}

	// 2. Wait for DropAck
	var ack DropAck
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		env, err := receiveDropEnvelope(ctx, conn)
		if err != nil {
			return fmt.Errorf("receive drop_ack: %w", err)
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			_ = env.DecodePayload(&hb)
			_ = conn.Send(protocol.TypeHeartbeat, hb)
			continue
		}
		if env.Type != TypeDropAck {
			return fmt.Errorf("expected %q, got %q", TypeDropAck, env.Type)
		}
		if err := env.DecodePayload(&ack); err != nil {
			return fmt.Errorf("decode drop_ack: %w", err)
		}
		if ack.DropID != meta.DropID {
			return fmt.Errorf("drop_ack id mismatch: got %q, want %q", ack.DropID, meta.DropID)
		}
		if !ack.Accepted {
			if ack.Error != "" {
				return fmt.Errorf("drop rejected: %s", ack.Error)
			}
			return errors.New("drop rejected by receiver")
		}
		if ack.ReceivedBytes < 0 || (meta.Size > 0 && ack.ReceivedBytes > meta.Size) {
			return fmt.Errorf("drop_ack returned invalid resume offset %d", ack.ReceivedBytes)
		}
		if cfg.OnAck != nil {
			cfg.OnAck(ack)
		}
		break
	}

	// 3. Stream DropData chunks with running SHA-256 and offset resumption support
	hasher := sha256.New()
	startChunkIndex := 0

	// Handle offset resumption if receiver already has partial bytes.
	if ack.ReceivedBytes > 0 {
		if ack.ReceivedBytes < 0 || ack.ReceivedBytes%int64(meta.ChunkSize) != 0 {
			return fmt.Errorf("receiver returned invalid resume offset %d", ack.ReceivedBytes)
		}
		seeker, ok := payload.(io.ReadSeeker)
		if !ok {
			return errors.New("receiver requested resume but payload is not seekable")
		}
		// Hash the existing bytes from 0..ack.ReceivedBytes so running SHA-256 covers the full file.
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek to start for hash: %w", err)
		}
		if _, err := io.CopyN(hasher, seeker, ack.ReceivedBytes); err != nil {
			return fmt.Errorf("hash existing bytes: %w", err)
		}
		if _, err := seeker.Seek(ack.ReceivedBytes, io.SeekStart); err != nil {
			return fmt.Errorf("seek to resume offset %d: %w", ack.ReceivedBytes, err)
		}
		startChunkIndex = int(ack.ReceivedBytes / int64(meta.ChunkSize))
	}

	buf := make([]byte, meta.ChunkSize)
	n, err := io.ReadFull(payload, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return fmt.Errorf("read payload: %w", err)
	}

	if err == io.EOF {
		// Empty payload or already at end
		if err := ctx.Err(); err != nil {
			return err
		}
		finalSHA := hex.EncodeToString(hasher.Sum(nil))
		chunk := DropData{
			DropID:     meta.DropID,
			ChunkIndex: startChunkIndex,
			Data:       []byte{},
			Final:      true,
			SHA256:     finalSHA,
		}
		if err := conn.Send(TypeDropData, chunk); err != nil {
			return fmt.Errorf("send drop_data chunk %d: %w", startChunkIndex, err)
		}
	} else if err == io.ErrUnexpectedEOF {
		// Exactly one partial chunk
		if err := ctx.Err(); err != nil {
			return err
		}
		chunkData := make([]byte, n)
		copy(chunkData, buf[:n])
		hasher.Write(chunkData)
		finalSHA := hex.EncodeToString(hasher.Sum(nil))
		chunk := DropData{
			DropID:     meta.DropID,
			ChunkIndex: startChunkIndex,
			Data:       chunkData,
			Final:      true,
			SHA256:     finalSHA,
		}
		if err := conn.Send(TypeDropData, chunk); err != nil {
			return fmt.Errorf("send drop_data chunk %d: %w", startChunkIndex, err)
		}
	} else {
		// Full chunk read, read-ahead loop
		currentData := make([]byte, n)
		copy(currentData, buf[:n])
		hasher.Write(currentData)
		chunkIndex := startChunkIndex

		for {
			if err := ctx.Err(); err != nil {
				return err
			}

			nextN, nextErr := io.ReadFull(payload, buf)
			if nextErr != nil && nextErr != io.EOF && nextErr != io.ErrUnexpectedEOF {
				return fmt.Errorf("read payload: %w", nextErr)
			}

			if nextErr == io.EOF {
				// currentData is final
				finalSHA := hex.EncodeToString(hasher.Sum(nil))
				chunk := DropData{
					DropID:     meta.DropID,
					ChunkIndex: chunkIndex,
					Data:       currentData,
					Final:      true,
					SHA256:     finalSHA,
				}
				if err := conn.Send(TypeDropData, chunk); err != nil {
					return fmt.Errorf("send drop_data chunk %d: %w", chunkIndex, err)
				}
				break
			} else if nextErr == io.ErrUnexpectedEOF {
				// currentData is not final, but next is final
				chunk := DropData{
					DropID:     meta.DropID,
					ChunkIndex: chunkIndex,
					Data:       currentData,
					Final:      false,
				}
				if err := conn.Send(TypeDropData, chunk); err != nil {
					return fmt.Errorf("send drop_data chunk %d: %w", chunkIndex, err)
				}

				chunkIndex++
				lastData := make([]byte, nextN)
				copy(lastData, buf[:nextN])
				hasher.Write(lastData)
				finalSHA := hex.EncodeToString(hasher.Sum(nil))
				lastChunk := DropData{
					DropID:     meta.DropID,
					ChunkIndex: chunkIndex,
					Data:       lastData,
					Final:      true,
					SHA256:     finalSHA,
				}
				if err := conn.Send(TypeDropData, lastChunk); err != nil {
					return fmt.Errorf("send drop_data chunk %d: %w", chunkIndex, err)
				}
				break
			} else {
				// nextN == meta.ChunkSize, currentData is not final
				chunk := DropData{
					DropID:     meta.DropID,
					ChunkIndex: chunkIndex,
					Data:       currentData,
					Final:      false,
				}
				if err := conn.Send(TypeDropData, chunk); err != nil {
					return fmt.Errorf("send drop_data chunk %d: %w", chunkIndex, err)
				}

				chunkIndex++
				currentData = make([]byte, nextN)
				copy(currentData, buf[:nextN])
				hasher.Write(currentData)
			}
		}
	}

	// 4. Wait for DropComplete
	sentSHA := hex.EncodeToString(hasher.Sum(nil))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		env, err := receiveDropEnvelope(ctx, conn)
		if err != nil {
			return fmt.Errorf("receive drop_complete: %w", err)
		}
		if env.Type == protocol.TypeHeartbeat {
			var hb protocol.Heartbeat
			_ = env.DecodePayload(&hb)
			_ = conn.Send(protocol.TypeHeartbeat, hb)
			continue
		}
		if env.Type != TypeDropComplete {
			return fmt.Errorf("expected %q, got %q", TypeDropComplete, env.Type)
		}
		var comp DropComplete
		if err := env.DecodePayload(&comp); err != nil {
			return fmt.Errorf("decode drop_complete: %w", err)
		}
		if comp.DropID != meta.DropID {
			return fmt.Errorf("drop_complete id mismatch: got %q, want %q", comp.DropID, meta.DropID)
		}
		if !comp.Success {
			if comp.Error != "" {
				return fmt.Errorf("drop failed: %s", comp.Error)
			}
			return errors.New("drop failed on receiver")
		}
		if comp.BytesRecv < 0 || (meta.Size > 0 && comp.BytesRecv != meta.Size) {
			return fmt.Errorf("drop_complete byte count mismatch: got %d, want %d", comp.BytesRecv, meta.Size)
		}
		if comp.SHA256 == "" || !strings.EqualFold(comp.SHA256, sentSHA) {
			return fmt.Errorf("drop_complete checksum mismatch: got %q, want %s", comp.SHA256, sentSHA)
		}
		break
	}

	return nil
}
