package bridge

// Gate for limitation 3.9: an OAuth flow with no redirect binding must be
// reported to the user as weaker, because it is weaker.
//
// The defect this closes is not a wording problem. `callbackExpectationSpec`
// carried an `unbound` field whose own comment said it "must be reported as
// weaker to the user", and callbackExpectation set it correctly on every call --
// and no production code read it. Only tests did. So the weaker flow was
// detected, documented, and then discarded, which is precisely the failure mode
// this repository's own rules describe: a function that exists, a comment that
// explains why it matters, and no evidence that either is connected.
//
// Why this is a behaviour gate and not a pure-function one. A pure-function
// version would call callbackExpectation("...?client_id=x") and assert
// `unbound == true`, which the existing tests already do and which would pass
// against a product that warns nobody. The claim under test is "the user is
// told", so what runs during the test must be the session that opens the
// browser and logs the warning: a real loopback connection, a real
// HandleASide, a real listener, and the real logger a user would see.
//
// Both directions are asserted. Warning on every flow would train users to
// ignore it and is its own defect, so a flow carrying a loopback redirect_uri
// must stay silent.

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// syncBuffer collects log output from the A-side goroutine while the test reads
// it. A plain bytes.Buffer would be a data race under -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// runASideSession drives one real A-side OAuth session over a loopback
// connection and returns everything the session logged.
//
// It stops at BridgeAck, which the A-side sends immediately after binding its
// callback listener and immediately before it warns and opens the browser. That
// ordering is the whole point: waiting for ack and then asserting on the log
// would race the warning, and a gate that flakes is a gate nobody trusts.
func runASideSession(t *testing.T, oauthURL string) string {
	t.Helper()

	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out := &syncBuffer{}
	opened := make(chan string, 1)

	// OpenBrowser signals that the session reached the point where it opens the
	// browser, which it does after the warning. Waiting for it rather than
	// sleeping makes the assertion deterministic.
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = HandleASide(ctx, conn, ASideConfig{
			Timeout: 2 * time.Second,
			OpenBrowser: func(targetURL string) error {
				select {
				case opened <- targetURL:
				default:
				}
				return nil
			},
			Logger: log.New(out, "", 0),
		})
	}()

	bConn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer bConn.Close()

	req := protocol.BridgeRequest{URL: oauthURL, CallbackPort: 0, RequestID: "req-unbound-gate"}
	if err := bConn.Send(protocol.TypeBridgeRequest, req); err != nil {
		t.Fatalf("send bridge_request failed: %v", err)
	}

	env, err := bConn.Receive()
	if err != nil {
		t.Fatalf("receive ack failed: %v", err)
	}
	var ack protocol.BridgeAck
	if err := env.DecodePayload(&ack); err != nil {
		t.Fatalf("decode ack failed: %v", err)
	}

	// The browser is opened after the warning is logged, so this is the
	// synchronisation point that makes the following read complete.
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the session never opened the browser, so there is nothing to assert on")
	}
	return out.String()
}

// TestUnboundOAuthFlowIsReportedToTheUser is the gate.
func TestUnboundOAuthFlowIsReportedToTheUser(t *testing.T) {
	t.Run("a flow with no redirect binding warns", func(t *testing.T) {
		// No redirect_uri and no state: exactly what the product sees when a user
		// pastes an authorization URL from an app that binds its callback some
		// other way.
		got := runASideSession(t, "https://provider.example/authorize?client_id=app&response_type=code")

		// The warning has to name what is missing, say what follows from it, and
		// say what the user can do. A bare "this flow is weaker" teaches nothing.
		for _, want := range []string{
			"no redirect binding", // what is missing
			"redirect_uri",        // and the parameter that would fix it
			"state",               // the other one
			"another process",     // what it costs, in terms the user can act on
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the warning does not mention %q, so it cannot tell the user what to do about it.\nFull log:\n%s", want, got)
			}
		}
		// It must appear before the browser opens, or the user has already
		// navigated.
		warnAt := strings.Index(got, "no redirect binding")
		openAt := strings.Index(got, "Opening browser")
		if warnAt < 0 || openAt < 0 {
			t.Fatalf("expected both the warning and the browser-open line in the log:\n%s", got)
		}
		if warnAt > openAt {
			t.Errorf("the warning is logged after the browser is opened, so it cannot inform the decision it exists to inform:\n%s", got)
		}
	})

	t.Run("a flow with a loopback redirect_uri stays quiet", func(t *testing.T) {
		got := runASideSession(t, "https://provider.example/authorize?client_id=app&redirect_uri="+
			"http%3A%2F%2F127.0.0.1%3A9999%2Fcallback&state=abc123")

		if strings.Contains(got, "no redirect binding") {
			t.Errorf("a correctly bound flow was reported as weaker. Warning on every sign-in is how a warning stops being read:\n%s", got)
		}
		if !strings.Contains(got, "Opening browser") {
			t.Fatalf("the session did not reach the browser-open step, so this case proved nothing:\n%s", got)
		}
	})
}
