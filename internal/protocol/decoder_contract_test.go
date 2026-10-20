package protocol

// The three wire-layer contracts the decoder did not keep, and the one it
// never had.
//
// Each of these was a claim in a comment or a document with nothing behind
// it:
//
//   - The decoder's own comment said a failed frame leaves the decoder
//     terminal "so a caller cannot accidentally parse the old body as the
//     next frame", and only the oversize-length branch did that.
//   - The envelope version was stamped at the encode choke point and read by
//     nothing, while the versioning spec's status line said "emit + validate".
//   - The control-frame cap was described as 64 KiB for everything that is
//     not QuickDrop data, in two documents and in the function's own comment,
//     and the code exempted two types.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// oneShotFailingReader delivers `limit` bytes, fails ONCE with a non-EOF
// error, and then serves the rest of its data.
//
// That models a socket whose read deadline expired once: the bytes after the
// failure are the ones the peer had already sent, or sent afterwards. A
// reader that failed permanently would not demonstrate the defect, because
// the resumed Decode would hit the same failure again rather than the
// attacker's frame.
//
// A reader that returned io.EOF would not exercise it either: io.ReadFull
// reports a short read as io.ErrUnexpectedEOF, which the decoder's old code
// latched differently.
type oneShotFailingReader struct {
	data   []byte
	limit  int
	pos    int
	err    error
	failed bool
}

func (r *oneShotFailingReader) Read(p []byte) (int, error) {
	if !r.failed {
		if r.pos >= r.limit {
			r.failed = true
			return 0, r.err
		}
		end := r.limit
		if end > len(r.data) {
			end = len(r.data)
		}
		n := copy(p, r.data[r.pos:end])
		r.pos += n
		return n, nil
	}
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// TestDecoderIsTerminalAfterAShortBodyRead is row 3.24.
//
// The stream is a length prefix promising a hundred bytes, followed by sixty
// of them, followed by a frame the peer never sent. The read fails part way
// through the body, and the decoder used to return the raw error and stay
// usable: the next Decode resumed mid-frame and read the leftover payload as
// the next frame's length prefix.
//
// The attacker's frame is well formed, so the old decoder returned it as a
// drop_send envelope. That is the whole claim: the peer chose those bytes,
// and the receiver believed them.
func TestDecoderIsTerminalAfterAShortBodyRead(t *testing.T) {
	// A real drop_send envelope, FRAMED, that the peer never sent as a frame
	// boundary: the bytes follow the failed frame, so a decoder that resumes
	// mid-frame reads them as the next frame.
	forged := &Envelope{Type: "drop_send", Payload: json.RawMessage(`{"drop_id":"forged"}`)}
	forgedBody, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	var forgedFrame bytes.Buffer
	var forgedLen [4]byte
	binary.BigEndian.PutUint32(forgedLen[:], uint32(len(forgedBody)))
	forgedFrame.Write(forgedLen[:])
	forgedFrame.Write(forgedBody)

	var stream bytes.Buffer
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], 100) // promise 100 bytes
	stream.Write(lenBuf[:])
	stream.Write([]byte(strings.Repeat("A", 60))) // deliver only 60
	stream.Write(forgedFrame.Bytes())             // the attacker's frame

	dec := NewDecoder(&oneShotFailingReader{
		data:  stream.Bytes(),
		limit: 4 + 60, // deliver the prefix and 60 body bytes, then fail once
		err:   errors.New("read timeout"),
	})

	if _, err := dec.Decode(); err == nil {
		t.Fatal("the first Decode succeeded, want the read error")
	}

	// The second Decode must NOT resume mid-frame.
	env, err := dec.Decode()
	if err == nil {
		t.Fatalf("the decoder resumed mid-frame and returned %q -- a frame the peer "+
			"never sent as a frame boundary", env.Type)
	}
	if env != nil {
		t.Errorf("the second Decode returned an envelope %+v alongside its error; the decoder "+
			"must return nothing once it is terminal", env)
	}
	if !strings.Contains(err.Error(), "read timeout") {
		t.Errorf("the second Decode error = %v, want the original read failure", err)
	}
}

// TestDecoderIsTerminalAfterAPartialLengthRead is the other half of the same
// defect: a length prefix that arrives incomplete desynchronises the stream
// just as surely as a short body.
func TestDecoderIsTerminalAfterAPartialLengthRead(t *testing.T) {
	dec := NewDecoder(&oneShotFailingReader{
		data:  []byte{0x00, 0x00},
		limit: 2,
		err:   errors.New("read timeout"),
	})
	if _, err := dec.Decode(); err == nil {
		t.Fatal("the first Decode succeeded, want the read error")
	}
	if _, err := dec.Decode(); err == nil {
		t.Fatal("the decoder resumed after a partial length prefix, so the remainder of the " +
			"prefix would be read as the next frame's length")
	}
}

// TestDecoderResumesAfterACleanEndOfStream is the property the fix had to
// keep. A reader that returns io.EOF having consumed nothing is at a frame
// boundary and has damaged nothing, and io.Reader explicitly allows a reader
// to produce data again afterwards -- bytes.Buffer does exactly that. Latching
// a clean EOF would turn "the peer closed" into "this decoder is broken", and
// the second Decode of a reader that has since been given more data would
// return the old error instead of the frame.
func TestDecoderResumesAfterACleanEndOfStream(t *testing.T) {
	reader := &bytes.Buffer{}
	dec := NewDecoder(reader)

	if _, err := dec.Decode(); !errors.Is(err, io.EOF) {
		t.Fatalf("the first Decode = %v, want io.EOF", err)
	}

	// The reader is given a frame after the EOF.
	var buf bytes.Buffer
	if err := NewEncoder(&buf).Encode(TypeHeartbeat, Heartbeat{RequestID: "after-eof"}); err != nil {
		t.Fatal(err)
	}
	reader.Write(buf.Bytes())

	env, err := dec.Decode()
	if err != nil {
		t.Fatalf("the decoder latched a clean EOF and could not read a frame that arrived "+
			"after it: %v", err)
	}
	if env.Type != TypeHeartbeat {
		t.Errorf("decoded type = %q, want %q", env.Type, TypeHeartbeat)
	}
}

// TestDecoderRefusesAnUnsupportedVersion is row 3.26.
//
// The version was emitted at the encode choke point and read by nothing: a
// peer announcing v: 9999 was decoded with no error and no rejection, while
// the versioning spec's status line said "emit + validate".
func TestDecoderRefusesAnUnsupportedVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		wantErr bool
	}{
		{name: "the current version", version: ProtocolVersion, wantErr: false},
		{name: "a pre-versioning peer", version: 0, wantErr: false},
		{name: "a peer ahead of us", version: ProtocolVersion + 1, wantErr: true},
		{name: "a peer far ahead of us", version: 9999, wantErr: true},
		{name: "a negative version", version: -1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := &Envelope{
				Type:    TypeHeartbeat,
				Payload: json.RawMessage(`{"request_id":"hb-1"}`),
				V:       tc.version,
			}
			raw, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			var framed bytes.Buffer
			var length [4]byte
			binary.BigEndian.PutUint32(length[:], uint32(len(raw)))
			framed.Write(length[:])
			framed.Write(raw)

			got, err := NewDecoder(&framed).Decode()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("a peer announcing v=%d was accepted and returned %+v; the version "+
						"is emitted but was never read", tc.version, got)
				}
				if !errors.Is(err, ErrUnsupportedVersion) {
					t.Errorf("error = %v, want ErrUnsupportedVersion", err)
				}
				if !strings.Contains(err.Error(), fmt.Sprintf("v=%d", tc.version)) {
					t.Errorf("error = %v, want it to name the version it refused", err)
				}
				// The message has to be actionable: "the other machine is
				// newer" is something a user can act on, and "unexpected
				// EOF" is not. A negative version is nonsense rather than
				// skew, so only the skew case is asked to say so.
				if tc.version > ProtocolVersion && !strings.Contains(err.Error(), "upgrade") {
					t.Errorf("error = %v, want it to say how to fix the skew", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("a peer announcing v=%d was refused: %v", tc.version, err)
			}
			if got.V != tc.version {
				t.Errorf("decoded V = %d, want %d", got.V, tc.version)
			}
		})
	}
}

// TestTheEncoderStampsTheVersion is the half of the versioning claim that
// was already true: the encoder stamps the version at the single choke
// point. The refusing half is TestDecoderRefusesAnUnsupportedVersion.
func TestTheEncoderStampsTheVersion(t *testing.T) {
	var buf bytes.Buffer
	if err := NewEncoder(&buf).Encode(TypeHeartbeat, Heartbeat{RequestID: "hb-1"}); err != nil {
		t.Fatal(err)
	}
	env, err := NewDecoder(&buf).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if env.V != ProtocolVersion {
		t.Errorf("decoded V = %d, want %d", env.V, ProtocolVersion)
	}
}

// TestTheControlFrameCapAndItsExemptions is row 3.25.
//
// The documents -- and the function's own comment -- said the control-frame
// cap was 64 KiB for everything that is not QuickDrop data. The code exempted
// two types, and the comment said one. The code is right: the capturing side
// reads a callback body up to 1 MiB (internal/bridge/aside.go) and the
// receiving side bounds the body and the header map explicitly
// (internal/bridge/bside.go), so a 64 KiB cap on a relay frame would
// contradict the product's own design.
//
// So the gate is on the BEHAVIOUR, and the documents are corrected to match
// it. This test is what keeps the two honest: if the exemption set changes,
// this fails and the documents have to be re-read.
func TestTheControlFrameCapAndItsExemptions(t *testing.T) {
	oversized := MaxControlMessageSize + 1024

	for _, tc := range []struct {
		name      string
		msgType   string
		wantAllow bool
	}{
		{name: "a control frame", msgType: TypeHeartbeat, wantAllow: false},
		{name: "a bridge request", msgType: TypeBridgeRequest, wantAllow: false},
		{name: "QuickDrop data", msgType: "drop_data", wantAllow: true},
		{name: "a callback relay", msgType: TypeCallbackRelay, wantAllow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]string{"pad": strings.Repeat("x", oversized)}

			// Encode.
			var buf bytes.Buffer
			err := NewEncoder(&buf).Encode(tc.msgType, payload)
			if tc.wantAllow {
				if err != nil {
					t.Fatalf("Encode of an oversized %s frame = %v, want nil: this type is "+
						"exempt from the control-frame cap", tc.msgType, err)
				}
			} else if !errors.Is(err, ErrMessageTooLarge) {
				t.Fatalf("Encode of an oversized %s frame = %v, want ErrMessageTooLarge",
					tc.msgType, err)
			}

			// Decode. The encoder's refusal is the local half; the
			// decoder's is the half a hostile peer exercises.
			env := &Envelope{Type: tc.msgType, Payload: json.RawMessage(`{"pad":"` +
				strings.Repeat("x", oversized) + `"}`)}
			raw, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			var framed bytes.Buffer
			var length [4]byte
			binary.BigEndian.PutUint32(length[:], uint32(len(raw)))
			framed.Write(length[:])
			framed.Write(raw)

			_, err = NewDecoder(&framed).Decode()
			if tc.wantAllow {
				if err != nil {
					t.Fatalf("Decode of an oversized %s frame = %v, want nil: this type is "+
						"exempt from the control-frame cap", tc.msgType, err)
				}
				return
			}
			if !errors.Is(err, ErrMessageTooLarge) {
				t.Fatalf("Decode of an oversized %s frame = %v, want ErrMessageTooLarge",
					tc.msgType, err)
			}
		})
	}
}
