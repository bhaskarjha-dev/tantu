package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	// DefaultMaxMessageSize is the maximum allowed frame size (4 MiB).  A
	// 1 MiB QuickDrop chunk is base64 encoded inside JSON and therefore still
	// fits comfortably within this limit.
	DefaultMaxMessageSize = 4 * 1024 * 1024
	// MaxControlMessageSize is a tighter post-header limit for control frames.
	// Data frames (drop_data) retain the larger transfer allowance.
	MaxControlMessageSize = 64 * 1024
)

// IsControlMessageType reports whether a frame should use the small control
// frame budget. Unknown types are treated as control frames; only the explicit
// QuickDrop data type is allowed to use the larger transfer budget.
func IsControlMessageType(msgType string) bool {
	// Callback relays may contain a bounded form body and retain the legacy
	// data allowance; QuickDrop data is the only deliberately large frame.
	return msgType != "drop_data" && msgType != TypeCallbackRelay
}

var (
	// ErrMessageTooLarge is returned when a message exceeds the maximum allowed size.
	ErrMessageTooLarge = errors.New("message exceeds maximum allowed size")
	// ErrMissingMessageType is returned for an envelope without an explicit
	// discriminator.
	ErrMissingMessageType = errors.New("envelope type is required")
	// ErrMissingPayload is returned for an envelope without a JSON payload.
	ErrMissingPayload = errors.New("envelope payload is required")
)

// Encoder encodes protocol messages into length-prefixed JSON frames.
type Encoder struct {
	w  io.Writer
	mu sync.Mutex
}

// NewEncoder creates a new Encoder writing to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

// Encode wraps payload in an Envelope with msgType and writes it as a framed message.
func (e *Encoder) Encode(msgType string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	env := &Envelope{Type: msgType, Payload: raw}
	return e.EncodeEnvelope(env)
}

// EncodeEnvelope writes an Envelope as a framed message.
func (e *Encoder) EncodeEnvelope(env *Envelope) error {
	if e == nil || e.w == nil {
		return errors.New("encoder writer is nil")
	}
	if env == nil {
		return errors.New("cannot encode a nil envelope")
	}
	if env.Type == "" {
		return ErrMissingMessageType
	}
	if len(env.Payload) == 0 || string(env.Payload) == "null" {
		return ErrMissingPayload
	}
	envBytes, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if len(envBytes) > DefaultMaxMessageSize {
		return ErrMessageTooLarge
	}

	buf := make([]byte, 4+len(envBytes))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(envBytes)))
	copy(buf[4:], envBytes)

	e.mu.Lock()
	defer e.mu.Unlock()

	// net.Conn normally writes the complete buffer, but io.Writer is allowed
	// to perform a short write.  Treat that as a framing error instead of
	// silently leaving a partial message on the wire.
	for len(buf) > 0 {
		n, writeErr := e.w.Write(buf)
		if n < 0 || n > len(buf) {
			return io.ErrShortWrite
		}
		if n > 0 {
			buf = buf[n:]
		}
		if writeErr != nil {
			return fmt.Errorf("write frame: %w", writeErr)
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// Decoder decodes length-prefixed JSON frames into Envelopes.
type Decoder struct {
	r              io.Reader
	maxMessageSize uint32
	mu             sync.RWMutex
	readMu         sync.Mutex
	terminalErr    error
}

// NewDecoder creates a new Decoder reading from r with DefaultMaxMessageSize limit.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{
		r:              r,
		maxMessageSize: DefaultMaxMessageSize,
	}
}

// SetMaxMessageSize configures the maximum allowed frame size in bytes.
// A non-positive value restores the safe default.
func (d *Decoder) SetMaxMessageSize(size uint32) {
	if size == 0 {
		size = DefaultMaxMessageSize
	}
	d.mu.Lock()
	d.maxMessageSize = size
	d.mu.Unlock()
}

func (d *Decoder) fail(err error) error {
	d.mu.Lock()
	if d.terminalErr == nil {
		d.terminalErr = err
	}
	terminal := d.terminalErr
	d.mu.Unlock()
	return terminal
}

// Decode reads one length-prefixed JSON frame and returns the Envelope.
func (d *Decoder) Decode() (*Envelope, error) {
	if d == nil || d.r == nil {
		return nil, errors.New("decoder reader is nil")
	}
	d.readMu.Lock()
	defer d.readMu.Unlock()

	d.mu.RLock()
	terminalErr := d.terminalErr
	maxMessageSize := d.maxMessageSize
	d.mu.RUnlock()
	if terminalErr != nil {
		return nil, terminalErr
	}

	var lenBuf [4]byte
	if _, err := io.ReadFull(d.r, lenBuf[:]); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(lenBuf[:])
	if length > maxMessageSize {
		// The body was not consumed.  Mark the decoder terminal so a caller
		// cannot accidentally parse the old body as the next frame.
		return nil, d.fail(ErrMessageTooLarge)
	}
	if length == 0 {
		return nil, d.fail(ErrMissingPayload)
	}

	payloadBuf := make([]byte, length)
	if _, err := io.ReadFull(d.r, payloadBuf); err != nil {
		return nil, err
	}

	var env Envelope
	if err := json.Unmarshal(payloadBuf, &env); err != nil {
		return nil, d.fail(fmt.Errorf("unmarshal envelope: %w", err))
	}
	if env.Type == "" {
		return nil, d.fail(ErrMissingMessageType)
	}
	if len(env.Payload) == 0 || string(env.Payload) == "null" {
		return nil, d.fail(ErrMissingPayload)
	}
	if IsControlMessageType(env.Type) && len(payloadBuf) > MaxControlMessageSize {
		return nil, d.fail(ErrMessageTooLarge)
	}
	return &env, nil
}

// Codec combines an Encoder and Decoder for bidirectional communication over an io.ReadWriter.
type Codec struct {
	*Encoder
	*Decoder
}

// NewCodec creates a Codec from an io.ReadWriter.
func NewCodec(rw io.ReadWriter) *Codec {
	return &Codec{
		Encoder: NewEncoder(rw),
		Decoder: NewDecoder(rw),
	}
}
