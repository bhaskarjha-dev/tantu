package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalRequestTokenMiddlewareProtectsMutations(t *testing.T) {
	const token = "local-token"
	called := false
	handler := localRequestTokenMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}), token)

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9875/api/send-text", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden || called {
		t.Fatalf("token-less mutation was not rejected: status=%d called=%t", recorder.Code, called)
	}

	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9875/api/send-text", nil)
	req.Header.Set("X-Tantu-IPC-Token", token)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent || !called {
		t.Fatalf("authorized mutation was rejected: status=%d called=%t", recorder.Code, called)
	}
}

func TestLocalRequestMiddlewareOriginAndLoopbackChecks(t *testing.T) {
	called := false
	handler := localRequestMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	allowed := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9875/api/status", nil)
	allowed.Host = "127.0.0.1:9875"
	allowed.RemoteAddr = "127.0.0.1:43210"
	allowed.Header.Set("Origin", "http://127.0.0.1:9875")
	allowed.Header.Set("Referer", "http://127.0.0.1:9875/")
	allowedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(allowedRecorder, allowed)
	if allowedRecorder.Code != http.StatusNoContent || !called {
		t.Fatalf("local request was not allowed: status=%d called=%t", allowedRecorder.Code, called)
	}
	if got := allowedRecorder.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:9875" {
		t.Fatalf("expected exact local CORS origin, got %q", got)
	}
	if got := allowedRecorder.Header().Get("Access-Control-Allow-Origin"); got == "*" {
		t.Fatal("wildcard CORS must never be emitted")
	}

	foreign := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9875/api/status", nil)
	foreign.Host = "127.0.0.1:9875"
	foreign.RemoteAddr = "127.0.0.1:43210"
	foreign.Header.Set("Origin", "https://evil.example")
	foreignRecorder := httptest.NewRecorder()
	handler.ServeHTTP(foreignRecorder, foreign)
	if foreignRecorder.Code != http.StatusForbidden {
		t.Fatalf("foreign origin should be rejected, got %d", foreignRecorder.Code)
	}

	foreignReferer := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9875/api/send-text", nil)
	foreignReferer.Host = "127.0.0.1:9875"
	foreignReferer.RemoteAddr = "127.0.0.1:43210"
	foreignReferer.Header.Set("Referer", "https://evil.example/")
	foreignRecorder = httptest.NewRecorder()
	handler.ServeHTTP(foreignRecorder, foreignReferer)
	if foreignRecorder.Code != http.StatusForbidden {
		t.Fatalf("foreign referer should be rejected, got %d", foreignRecorder.Code)
	}

	nonLoopback := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9875/api/status", nil)
	nonLoopback.Host = "127.0.0.1:9875"
	nonLoopback.RemoteAddr = "203.0.113.10:43210"
	nonLoopbackRecorder := httptest.NewRecorder()
	handler.ServeHTTP(nonLoopbackRecorder, nonLoopback)
	if nonLoopbackRecorder.Code != http.StatusForbidden {
		t.Fatalf("non-loopback request should be rejected, got %d", nonLoopbackRecorder.Code)
	}

	opaqueRelay := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9875/relay", nil)
	opaqueRelay.Host = "127.0.0.1:9875"
	opaqueRelay.RemoteAddr = "127.0.0.1:43210"
	opaqueRelay.Header.Set("Origin", "null")
	opaqueRecorder := httptest.NewRecorder()
	handler.ServeHTTP(opaqueRecorder, opaqueRelay)
	if opaqueRecorder.Code != http.StatusNoContent {
		t.Fatalf("opaque-origin relay navigation should reach its token check, got %d", opaqueRecorder.Code)
	}
}

func TestLocalSingleFlight_FollowerSurvivesLeaderCancellation(t *testing.T) {
	group := newLocalSingleFlight()
	leaderStarted := make(chan struct{})
	releaseLeader := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- group.Do(context.Background(), "same", func() error {
			close(leaderStarted)
			<-releaseLeader
			return nil
		})
	}()
	select {
	case <-leaderStarted:
	case <-time.After(time.Second):
		t.Fatal("leader did not start")
	}

	followerDone := make(chan error, 1)
	go func() {
		followerDone <- group.Do(context.Background(), "same", func() error {
			return errors.New("follower unexpectedly became leader")
		})
	}()
	// Give the follower time to observe the active call before releasing the
	// leader; otherwise it can legitimately win after the leader completes.
	time.Sleep(25 * time.Millisecond)
	close(releaseLeader)
	select {
	case err := <-leaderDone:
		if err != nil {
			t.Fatalf("leader failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("leader did not finish")
	}
	select {
	case err := <-followerDone:
		if err != nil {
			t.Fatalf("follower failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("follower remained blocked after leader completion")
	}
}

func TestLocalSingleFlight_CanceledBeforeLeadershipDoesNotStart(t *testing.T) {
	group := newLocalSingleFlight()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := group.Do(ctx, "cancelled", func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if called {
		t.Fatal("cancelled request started the operation")
	}
}

func TestFinalizeIncomingPartDoesNotOverwriteExistingFile(t *testing.T) {
	dir := t.TempDir()
	desired := filepath.Join(dir, "payload.txt")
	part := filepath.Join(dir, ".tantu-staging", "payload.txt.part")
	if err := os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desired, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("received")); err != nil {
		t.Fatal(err)
	}
	published, err := finalizeIncomingPart(f, part, desired)
	if err != nil {
		t.Fatalf("finalizeIncomingPart failed: %v", err)
	}
	if published == desired {
		t.Fatal("publication overwrote the existing destination")
	}
	existing, err := os.ReadFile(desired)
	if err != nil || string(existing) != "existing" {
		t.Fatalf("existing destination changed: %q, err=%v", existing, err)
	}
	received, err := os.ReadFile(published)
	if err != nil || string(received) != "received" {
		t.Fatalf("published payload mismatch: %q, err=%v", received, err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("staging file still exists after publication: %v", err)
	}
}

func TestEscapeHTML(t *testing.T) {
	got := escapeHTML(`<tag a="b">&'`)
	want := `&lt;tag a=&quot;b&quot;&gt;&amp;&#39;`
	if got != want {
		t.Fatalf("escapeHTML() = %q, want %q", got, want)
	}
}

func TestBoundedTextBuffer(t *testing.T) {
	buf := boundedTextBuffer{limit: 4}
	if n, err := buf.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("unexpected initial write: n=%d err=%v", n, err)
	}
	if _, err := buf.Write([]byte("de")); err == nil {
		t.Fatal("expected bounded text write to fail")
	}
	if got := buf.String(); got != "abc" {
		t.Fatalf("unexpected buffer contents %q", got)
	}
}

func TestNewLocalHTTPServerHasBoundedSettings(t *testing.T) {
	server := newLocalHTTPServer("127.0.0.1:0", http.NotFoundHandler(), 5*time.Minute)
	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("expected positive HTTP timeouts: %+v", server)
	}
	if server.MaxHeaderBytes <= 0 || server.MaxHeaderBytes > localHTTPMaxHeaderBytes {
		t.Fatalf("unexpected header limit: %d", server.MaxHeaderBytes)
	}
}

func TestBROWSERArgsQuoteSpacesAndMetacharacters(t *testing.T) {
	quoted := quoteBROWSERArg("host;echo bad")
	if quoted == "host;echo bad" {
		t.Fatalf("BROWSER argument was not quoted: %q", quoted)
	}
	if !strings.ContainsAny(quoted, "'\\\"") {
		t.Fatalf("BROWSER argument is missing a quote: %q", quoted)
	}
}
