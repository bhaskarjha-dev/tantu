package drop_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func TestReceiveDropReportsFinalizationFailureToSender(t *testing.T) {
	payload := []byte("finalization-sensitive")
	meta := drop.DropSend{
		DropID: "finalize-failure",
		Kind:   drop.DropKindFile,
		Name:   "payload.bin",
		Size:   int64(len(payload)),
	}
	_, sendErr, recvErr := runDropPair(t, meta, bytes.NewReader(payload), drop.SendDropConfig{}, &bytes.Buffer{}, drop.ReceiveDropConfig{
		BeforeComplete: func(*drop.ReceiveDropResult) error {
			return context.DeadlineExceeded
		},
	})
	if sendErr == nil || recvErr == nil {
		t.Fatalf("expected both sides to observe finalization failure, send=%v recv=%v", sendErr, recvErr)
	}
}

func TestReceiveDropRejectsUnsafeDropIDs(t *testing.T) {
	unsafeIDs := []string{
		"../../outside",
		`..\\outside`,
		"C:stream",
		".",
		"..",
		"NUL.part",
		"CON",
	}
	for _, dropID := range unsafeIDs {
		t.Run(dropID, func(t *testing.T) {
			tr := transport.NewLoopbackTransport()
			listener, err := tr.Listen("127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer listener.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			recvErrCh := make(chan error, 1)
			go func() {
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					recvErrCh <- acceptErr
					return
				}
				defer conn.Close()
				_, recvErr := drop.ReceiveDrop(ctx, conn, &bytes.Buffer{}, drop.ReceiveDropConfig{})
				recvErrCh <- recvErr
			}()

			client, err := tr.Dial(listener.Addr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer client.Close()
			if err := client.Send(drop.TypeDropSend, drop.DropSend{
				DropID: dropID,
				Kind:   drop.DropKindText,
				Name:   "safe.txt",
				Size:   1,
			}); err != nil {
				t.Fatalf("send metadata: %v", err)
			}
			env, err := client.Receive()
			if err != nil {
				t.Fatalf("receive rejection: %v", err)
			}
			if env.Type != drop.TypeDropAck {
				t.Fatalf("expected drop ack, got %q", env.Type)
			}
			var ack drop.DropAck
			if err := env.DecodePayload(&ack); err != nil {
				t.Fatalf("decode ack: %v", err)
			}
			if ack.Accepted {
				t.Fatal("unsafe drop ID was accepted")
			}
			if ack.Error == "" || !strings.Contains(strings.ToLower(ack.Error), "drop_id") {
				t.Fatalf("expected a drop_id validation error, got %q", ack.Error)
			}
			select {
			case recvErr := <-recvErrCh:
				if recvErr == nil {
					t.Fatal("receiver unexpectedly accepted unsafe drop ID")
				}
			case <-time.After(time.Second):
				t.Fatal("receiver did not finish after rejecting metadata")
			}
		})
	}
}
