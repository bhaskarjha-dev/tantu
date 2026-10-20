package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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
	// The encoder must not clobber an explicitly set version. It must also
	// not accept it on the way back in: a version this release does not
	// implement is refused, because a peer ahead of us may mean things this
	// decoder does not know. The refusal is what
	// TestDecoderRefusesAnUnsupportedVersion asserts in detail; this test is
	// the encoder's half, and the point that the two halves are not the same
	// claim.
	//
	// It asserted the opposite before 2026-10-20: the version was emitted
	// and read by nothing, so v=7 came back happily. That is the defect row
	// 3.26 records.
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	explicit := ProtocolVersion + 5
	if err := enc.EncodeEnvelope(&Envelope{Type: TypeHeartbeat, Payload: json.RawMessage(`{}`), V: explicit}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(&Envelope{
		Type:    TypeHeartbeat,
		Payload: json.RawMessage(`{}`),
		V:       explicit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(fmt.Sprintf(`"v":%d`, explicit))) {
		t.Errorf("an explicitly set version was clobbered by the encoder: %s", raw)
	}
	_, err = NewDecoder(&buf).Decode()
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("decoding v=%d = %v, want ErrUnsupportedVersion", explicit, err)
	}
}
