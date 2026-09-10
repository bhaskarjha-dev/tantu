package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestRoundTripMessageTypes(t *testing.T) {
	tests := []struct {
		name    string
		msgType string
		payload any
		verify  func(t *testing.T, env *Envelope)
	}{
		{
			name:    "BridgeRequest",
			msgType: TypeBridgeRequest,
			payload: BridgeRequest{
				URL:          "http://127.0.0.1:8080/authorize?client_id=123",
				CallbackPort: 8080,
				RequestID:    "req-1",
			},
			verify: func(t *testing.T, env *Envelope) {
				if env.Type != TypeBridgeRequest {
					t.Fatalf("expected type %q, got %q", TypeBridgeRequest, env.Type)
				}
				var req BridgeRequest
				if err := env.DecodePayload(&req); err != nil {
					t.Fatalf("decode payload: %v", err)
				}
				expected := BridgeRequest{
					URL:          "http://127.0.0.1:8080/authorize?client_id=123",
					CallbackPort: 8080,
					RequestID:    "req-1",
				}
				if req != expected {
					t.Errorf("expected %+v, got %+v", expected, req)
				}
			},
		},
		{
			name:    "BridgeAck",
			msgType: TypeBridgeAck,
			payload: BridgeAck{
				RequestID:     "req-1",
				ListeningPort: 9090,
				Error:         "",
			},
			verify: func(t *testing.T, env *Envelope) {
				if env.Type != TypeBridgeAck {
					t.Fatalf("expected type %q, got %q", TypeBridgeAck, env.Type)
				}
				var ack BridgeAck
				if err := env.DecodePayload(&ack); err != nil {
					t.Fatalf("decode payload: %v", err)
				}
				expected := BridgeAck{
					RequestID:     "req-1",
					ListeningPort: 9090,
					Error:         "",
				}
				if ack != expected {
					t.Errorf("expected %+v, got %+v", expected, ack)
				}
			},
		},
		{
			name:    "CallbackRelay",
			msgType: TypeCallbackRelay,
			payload: CallbackRelay{
				RequestID: "req-1",
				Method:    "GET",
				Path:      "/callback?code=auth123&state=xyz\nwith-newline",
				Headers: map[string]string{
					"Host":       "localhost:8080",
					"User-Agent": "Mozilla/5.0",
				},
				Body:       []byte("sample body with\nnewlines and binary \x00\x01\x02"),
				StatusCode: 200,
			},
			verify: func(t *testing.T, env *Envelope) {
				if env.Type != TypeCallbackRelay {
					t.Fatalf("expected type %q, got %q", TypeCallbackRelay, env.Type)
				}
				var relay CallbackRelay
				if err := env.DecodePayload(&relay); err != nil {
					t.Fatalf("decode payload: %v", err)
				}
				if relay.RequestID != "req-1" || relay.Method != "GET" || relay.StatusCode != 200 {
					t.Errorf("mismatched basic fields in CallbackRelay: %+v", relay)
				}
				if relay.Path != "/callback?code=auth123&state=xyz\nwith-newline" {
					t.Errorf("mismatched path: %q", relay.Path)
				}
				if relay.Headers["Host"] != "localhost:8080" {
					t.Errorf("mismatched headers: %+v", relay.Headers)
				}
				if !bytes.Equal(relay.Body, []byte("sample body with\nnewlines and binary \x00\x01\x02")) {
					t.Errorf("mismatched body: %v", relay.Body)
				}
			},
		},
		{
			name:    "BridgeComplete",
			msgType: TypeBridgeComplete,
			payload: BridgeComplete{
				RequestID: "req-1",
				Success:   true,
			},
			verify: func(t *testing.T, env *Envelope) {
				if env.Type != TypeBridgeComplete {
					t.Fatalf("expected type %q, got %q", TypeBridgeComplete, env.Type)
				}
				var comp BridgeComplete
				if err := env.DecodePayload(&comp); err != nil {
					t.Fatalf("decode payload: %v", err)
				}
				if comp.RequestID != "req-1" || !comp.Success {
					t.Errorf("unexpected BridgeComplete: %+v", comp)
				}
			},
		},
		{
			name:    "Heartbeat",
			msgType: TypeHeartbeat,
			payload: Heartbeat{
				RequestID: "",
			},
			verify: func(t *testing.T, env *Envelope) {
				if env.Type != TypeHeartbeat {
					t.Fatalf("expected type %q, got %q", TypeHeartbeat, env.Type)
				}
				var hb Heartbeat
				if err := env.DecodePayload(&hb); err != nil {
					t.Fatalf("decode payload: %v", err)
				}
				if hb.RequestID != "" {
					t.Errorf("unexpected heartbeat request ID: %q", hb.RequestID)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			enc := NewEncoder(&buf)
			dec := NewDecoder(&buf)

			if err := enc.Encode(tt.msgType, tt.payload); err != nil {
				t.Fatalf("encode: %v", err)
			}

			// Verify framing: first 4 bytes is length prefix
			raw := buf.Bytes()
			if len(raw) < 4 {
				t.Fatalf("encoded buffer too short: %d", len(raw))
			}
			msgLen := binary.BigEndian.Uint32(raw[:4])
			if int(msgLen) != len(raw)-4 {
				t.Fatalf("framing length %d does not match remaining bytes %d", msgLen, len(raw)-4)
			}

			env, err := dec.Decode()
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			tt.verify(t, env)
		})
	}
}

func TestLargePayload(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	dec := NewDecoder(&buf)

	largeBody := make([]byte, 1024*1024) // 1MB body
	for i := range largeBody {
		largeBody[i] = byte(i % 256)
	}

	relay := CallbackRelay{
		RequestID: "large-1",
		Method:    "POST",
		Path:      "/callback",
		Body:      largeBody,
	}

	if err := enc.Encode(TypeCallbackRelay, relay); err != nil {
		t.Fatalf("encode large payload: %v", err)
	}

	env, err := dec.Decode()
	if err != nil {
		t.Fatalf("decode large payload: %v", err)
	}

	var decodedRelay CallbackRelay
	if err := env.DecodePayload(&decodedRelay); err != nil {
		t.Fatalf("decode large payload struct: %v", err)
	}

	if !bytes.Equal(decodedRelay.Body, largeBody) {
		t.Fatalf("decoded body does not match original large body")
	}
}

func TestMaxMessageSizeExceeded(t *testing.T) {
	var buf bytes.Buffer
	dec := NewDecoder(&buf)
	dec.SetMaxMessageSize(100)

	// Write frame with length 150
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], 150)
	buf.Write(lenBuf[:])
	buf.Write(make([]byte, 150))

	_, err := dec.Decode()
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge, got %v", err)
	}
}

func TestEOFHandling(t *testing.T) {
	var buf bytes.Buffer
	dec := NewDecoder(&buf)

	_, err := dec.Decode()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF on empty reader, got %v", err)
	}

	// Partial length read
	buf.Write([]byte{0x00, 0x00})
	_, err = dec.Decode()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF on partial length, got %v", err)
	}

	// Partial payload read
	buf.Reset()
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], 20)
	buf.Write(lenBuf[:])
	buf.Write([]byte("short"))
	_, err = dec.Decode()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF on partial payload, got %v", err)
	}
}

func TestNewCodec(t *testing.T) {
	var buf bytes.Buffer
	codec := NewCodec(&buf)

	req := BridgeRequest{
		URL:          "http://localhost:3000/auth",
		CallbackPort: 3000,
		RequestID:    "codec-req",
	}

	if err := codec.Encode(TypeBridgeRequest, req); err != nil {
		t.Fatalf("codec encode: %v", err)
	}

	env, err := codec.Decode()
	if err != nil {
		t.Fatalf("codec decode: %v", err)
	}

	var decoded BridgeRequest
	if err := env.DecodePayload(&decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if !reflect.DeepEqual(req, decoded) {
		t.Fatalf("expected %+v, got %+v", req, decoded)
	}
}
