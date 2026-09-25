package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
)

func TestEnvelopeVersionEmitted(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.Encode(TypeHeartbeat, Heartbeat{RequestID: "hb-1"}); err != nil {
		t.Fatal(err)
	}
	dec := NewDecoder(&buf)
	env, err := dec.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if env.V != ProtocolVersion || ProtocolVersion <= 0 {
		t.Errorf("decoded V = %d, want %d", env.V, ProtocolVersion)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"v":`)) {
		t.Errorf("encoded envelope lacks a version field: %s", raw)
	}
}

func TestEnvelopeLegacyDecode(t *testing.T) {
	// A pre-versioning peer sends no "v" member. Decoding must succeed with
	// V == 0 rather than failing the connection.
	legacy, err := json.Marshal(map[string]any{
		"type":    TypeHeartbeat,
		"payload": map[string]string{"request_id": "hb-old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacy, []byte(`"v"`)) {
		t.Fatal("legacy fixture must not contain a version field")
	}
	frame := make([]byte, 4+len(legacy))
	binary.BigEndian.PutUint32(frame[0:4], uint32(len(legacy)))
	copy(frame[4:], legacy)
	dec := NewDecoder(bytes.NewReader(frame))
	env, err := dec.Decode()
	if err != nil {
		t.Fatalf("legacy decode failed: %v", err)
	}
	if env.V != 0 {
		t.Errorf("legacy V = %d, want 0", env.V)
	}
}

func TestEnvelopeExplicitVersionPreserved(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.EncodeEnvelope(&Envelope{Type: TypeHeartbeat, Payload: json.RawMessage(`{}`), V: 7}); err != nil {
		t.Fatal(err)
	}
	env, err := NewDecoder(&buf).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if env.V != 7 {
		t.Errorf("explicit V = %d, want 7", env.V)
	}
}
