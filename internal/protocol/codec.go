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
	// Data frames (drop_data) retain the larger transfer allowance, and so do
	// callback relays, which carry a bounded form body.
	MaxControlMessageSize = 64 * 1024
)

// IsControlMessageType reports whether a frame should use the small control
// frame budget. Unknown types are treated as control frames; only the two
// message types that legitimately carry bulk are exempt.
func IsControlMessageType(msgType string) bool {
	// Two types are exempt, and both were added deliberately:
	//
	//   drop_data carries a QuickDrop chunk, and the 1 MiB chunk size is the
	//     product's own.
	//   callback_relay carries the captured HTTP callback request. The
	//     capturing side reads that body up to 1 MiB
	//     (internal/bridge/aside.go) and the receiving side bounds the body
	//     and the header map explicitly (internal/bridge/bside.go), because
	//     the frame budget does not bound them.
	//
	// The comment this replaced said only the QuickDrop data type was
	// exempt, which was false of the two lines beside it, and the documents
	// repeated the claim: the cap was described as 64 KiB for every frame
	// that was not QuickDrop data. A reader sizing a frame budget from that
	// would be wrong by a factor of sixty-four. See
	// docs/KNOWN-LIMITATIONS.md row 3.25.
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
	// ErrUnsupportedVersion is returned when a peer announces an envelope
	// version this release does not implement.
	ErrUnsupportedVersion = errors.New("unsupported envelope version")
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
	// Stamp the envelope version at the single encoding choke point so every
	// message type carries it without per-caller changes. An explicitly set
	// version is never clobbered.
	if env.V <= 0 {
		env.V = ProtocolVersion
	}
	envBytes, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if len(envBytes) > DefaultMaxMessageSize {
		return ErrMessageTooLarge
	}
	// Mirror the decoder's control-frame budget so a sender learns about an
	// oversized control frame locally instead of only after the receiver
	// closes the connection.
	if IsControlMessageType(env.Type) && len(envBytes) > MaxControlMessageSize {
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
	n, err := io.ReadFull(d.r, lenBuf[:])
	if err != nil {
		if n > 0 {
			// Part of the length prefix was consumed, so the stream is
			// desynchronised: the next Decode would read the remainder of
			// this prefix as a length. Mark the decoder terminal.
			return nil, d.fail(err)
		}
		// Nothing was consumed. The stream ended cleanly at a frame
		// boundary, and a reader is allowed to produce data again after an
		// EOF -- bytes.Buffer does exactly that -- so the decoder is not
		// latched here. There is no next frame in the stream, but there is
		// no damage to it either.
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
		// The length prefix was consumed, so whatever comes next in the
		// stream is BODY, not a frame header: resuming would read payload
		// bytes as the next frame's length. Mark the decoder terminal.
		//
		// This is the invariant the decoder's own comment claimed and only
		// the oversize-length branch kept: a short read is the case a socket
		// read-deadline expiry produces, and it left the decoder resuming
		// mid-frame, so leftover payload bytes were read as the next frame's
		// length prefix. See docs/KNOWN-LIMITATIONS.md row 3.24.
		return nil, d.fail(err)
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
	if err := validateEnvelopeVersion(env.V); err != nil {
		return nil, d.fail(err)
	}
	if IsControlMessageType(env.Type) && len(payloadBuf) > MaxControlMessageSize {
		return nil, d.fail(ErrMessageTooLarge)
	}
	return &env, nil
}

// validateEnvelopeVersion rejects a peer that speaks a version this release
// does not implement.
//
// Zero is a pre-versioning peer: the field was absent, and the rollout plan
// (docs/SPEC-WIRE-VERSIONING.md) requires those peers to keep working. A
// version BELOW the current one is an older peer, and is accepted for the same
// reason: the rollout is additive-only, so an older peer understands a
// superset of what this release sends.
//
// A version ABOVE the current one is a peer ahead of us. Its frames may mean
// things this release does not know, and the supported topology is
// same-release only, so the connection is refused rather than half-understood.
// The error names the cause, because "the other machine is newer" is an
// actionable thing and "unexpected EOF" is not.
func validateEnvelopeVersion(v int) error {
	switch {
	case v < 0:
		return fmt.Errorf("%w: the peer speaks v=%d, which is not a version", ErrUnsupportedVersion, v)
	case v > ProtocolVersion:
		return fmt.Errorf("%w: the peer speaks v=%d and this release speaks v=%d; upgrade both "+
			"machines to the same release", ErrUnsupportedVersion, v, ProtocolVersion)
	default:
		return nil
	}
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
