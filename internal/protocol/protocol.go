// Package protocol defines the core communication protocol data structures
// and serialization schemas for tantu requests and responses.
package protocol

import "encoding/json"

// MessageType constants identify the type of payload contained in an Envelope.
const (
	TypeBridgeRequest  = "bridge_request"
	TypeBridgeAck      = "bridge_ack"
	TypeCallbackRelay  = "callback_relay"
	TypeBridgeComplete = "bridge_complete"
	TypeHeartbeat      = "heartbeat"
)

// ProtocolVersion is the wire envelope version emitted by this release.
// Zero (absent on the wire via omitempty) means a pre-versioning peer.
// Decoders ignore unknown fields, so emitting it is backward compatible in
// both directions: old peers ignore it, new peers read it. See
// docs/SPEC-WIRE-VERSIONING.md for the rollout plan.
const ProtocolVersion = 1

// Envelope wraps any message for wire transmission.
type Envelope struct {
	Type    string          `json:"type"`        // "bridge_request", "bridge_ack", etc.
	Payload json.RawMessage `json:"payload"`     // JSON-encoded message
	V       int             `json:"v,omitempty"` // envelope version; 0 = pre-versioning peer
}

// NewEnvelope creates an Envelope wrapping the given payload under msgType.
func NewEnvelope(msgType string, payload any) (*Envelope, error) {
	if msgType == "" {
		return nil, ErrMissingMessageType
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || string(b) == "null" {
		return nil, ErrMissingPayload
	}
	return &Envelope{
		Type:    msgType,
		Payload: b,
	}, nil
}

// DecodePayload unmarshals the raw JSON payload into dest.
func (e *Envelope) DecodePayload(dest any) error {
	if e == nil || len(e.Payload) == 0 || string(e.Payload) == "null" {
		return ErrMissingPayload
	}
	return json.Unmarshal(e.Payload, dest)
}

// BridgeRequest: B→A. "Open this URL, forward callback on this port."
type BridgeRequest struct {
	URL          string `json:"url"`               // OAuth authorization URL to open
	CallbackPort int    `json:"callback_port"`     // Port where app's callback listener runs on B
	RequestID    string `json:"request_id"`        // Unique ID for this transport attempt
	FlowID       string `json:"flow_id,omitempty"` // Stable application flow/idempotency identity
}

// BridgeAck: A→B. "URL opened, I'm listening for the callback."
type BridgeAck struct {
	RequestID     string `json:"request_id"`
	ListeningPort int    `json:"listening_port"`  // Port A-side bound for callback capture
	Error         string `json:"error,omitempty"` // Non-empty if A-side can't fulfill
	// Replay indicates that this logical OAuth request already completed
	// successfully. The B-side can return success without opening a new
	// browser session or waiting for another callback.
	Replay bool `json:"replay,omitempty"`
}

// CallbackRelay: A→B. The captured HTTP callback request.
type CallbackRelay struct {
	RequestID  string            `json:"request_id"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`        // e.g. "/callback?code=xyz&state=abc"
	Headers    map[string]string `json:"headers"`     // Simplified: single value per header
	Body       []byte            `json:"body"`        // Raw body bytes (base64 in JSON)
	StatusCode int               `json:"status_code"` // Not needed for relay but useful for error callbacks
}

// BridgeComplete: B→A. "Auth complete, tear down."
type BridgeComplete struct {
	RequestID string `json:"request_id"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// Heartbeat: Both directions. Keep-alive.
type Heartbeat struct {
	RequestID string `json:"request_id,omitempty"` // Empty for general keep-alive
}
