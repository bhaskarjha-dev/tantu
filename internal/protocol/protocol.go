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

// Envelope wraps any message for wire transmission.
type Envelope struct {
	Type    string          `json:"type"`    // "bridge_request", "bridge_ack", etc.
	Payload json.RawMessage `json:"payload"` // JSON-encoded message
}

// NewEnvelope creates an Envelope wrapping the given payload under msgType.
func NewEnvelope(msgType string, payload any) (*Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Envelope{
		Type:    msgType,
		Payload: b,
	}, nil
}

// DecodePayload unmarshals the raw JSON payload into dest.
func (e *Envelope) DecodePayload(dest any) error {
	return json.Unmarshal(e.Payload, dest)
}

// BridgeRequest: B→A. "Open this URL, forward callback on this port."
type BridgeRequest struct {
	URL          string `json:"url"`           // OAuth authorization URL to open
	CallbackPort int    `json:"callback_port"` // Port where app's callback listener runs on B
	RequestID    string `json:"request_id"`    // Unique ID for this bridge session
}

// BridgeAck: A→B. "URL opened, I'm listening for the callback."
type BridgeAck struct {
	RequestID     string `json:"request_id"`
	ListeningPort int    `json:"listening_port"` // Port A-side bound for callback capture
	Error         string `json:"error,omitempty"` // Non-empty if A-side can't fulfill
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
}

// Heartbeat: Both directions. Keep-alive.
type Heartbeat struct {
	RequestID string `json:"request_id,omitempty"` // Empty for general keep-alive
}
