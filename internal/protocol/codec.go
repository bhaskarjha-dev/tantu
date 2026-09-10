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
	// DefaultMaxMessageSize is the maximum allowed frame size (4MB) to prevent OOM.
	DefaultMaxMessageSize = 4 * 1024 * 1024
)

var (
	// ErrMessageTooLarge is returned when a message exceeds the maximum allowed size.
	ErrMessageTooLarge = errors.New("message exceeds maximum allowed size")
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
	env := &Envelope{
		Type:    msgType,
		Payload: raw,
	}
	return e.EncodeEnvelope(env)
}

// EncodeEnvelope writes an Envelope as a framed message.
func (e *Encoder) EncodeEnvelope(env *Envelope) error {
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

	if _, err := e.w.Write(buf); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// Decoder decodes length-prefixed JSON frames into Envelopes.
type Decoder struct {
	r              io.Reader
	maxMessageSize uint32
}

// NewDecoder creates a new Decoder reading from r with DefaultMaxMessageSize limit.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{
		r:              r,
		maxMessageSize: DefaultMaxMessageSize,
	}
}

// SetMaxMessageSize configures the maximum allowed frame size in bytes.
func (d *Decoder) SetMaxMessageSize(size uint32) {
	d.maxMessageSize = size
}

// Decode reads one length-prefixed JSON frame and returns the Envelope.
func (d *Decoder) Decode() (*Envelope, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(d.r, lenBuf[:]); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(lenBuf[:])
	if length > d.maxMessageSize {
		return nil, ErrMessageTooLarge
	}

	payloadBuf := make([]byte, length)
	if _, err := io.ReadFull(d.r, payloadBuf); err != nil {
		return nil, err
	}

	var env Envelope
	if err := json.Unmarshal(payloadBuf, &env); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
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
