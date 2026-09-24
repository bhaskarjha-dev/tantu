package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEventLogger_ConcurrentIDsRemainReplayOrdered(t *testing.T) {
	logger := NewEventLogger(128)
	const count = 100
	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func() {
			defer wg.Done()
			logger.Info(DomainNet, "concurrent")
		}()
	}
	wg.Wait()
	events := logger.GetRecent()
	if len(events) != count {
		t.Fatalf("event count = %d, want %d", len(events), count)
	}
	for i := 1; i < len(events); i++ {
		if events[i].ID != events[i-1].ID+1 {
			t.Fatalf("event IDs out of order at %d: %d then %d", i, events[i-1].ID, events[i].ID)
		}
	}
}

func TestRingBuffer_CircularBounds(t *testing.T) {
	capacity := 200
	rb := NewRingBuffer(capacity)

	// Fill with 250 events
	for i := 1; i <= 250; i++ {
		rb.Append(LogEvent{
			ID:      uint64(i),
			Domain:  DomainDrop,
			Level:   LevelInfo,
			Message: "event test",
		})
	}

	if count := rb.Count(); count != capacity {
		t.Errorf("expected count %d, got %d", capacity, count)
	}

	events := rb.GetRecent()
	if len(events) != capacity {
		t.Fatalf("expected %d events, got %d", capacity, len(events))
	}

	// First event should be 51
	if events[0].ID != 51 {
		t.Errorf("expected oldest event ID 51, got %d", events[0].ID)
	}

	// Last event should be 250
	if events[capacity-1].ID != 250 {
		t.Errorf("expected newest event ID 250, got %d", events[capacity-1].ID)
	}

	// Verify strict monotonicity
	for j := 1; j < len(events); j++ {
		if events[j].ID != events[j-1].ID+1 {
			t.Errorf("expected consecutive IDs, got %d then %d", events[j-1].ID, events[j].ID)
		}
	}
}

func TestRingBuffer_ConcurrentAccess(t *testing.T) {
	rb := NewRingBuffer(200)
	var wg sync.WaitGroup

	// 10 concurrent writers
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				rb.Append(LogEvent{
					ID:      uint64(gid*1000 + i),
					Domain:  DomainNet,
					Level:   LevelInfo,
					Message: "concurrent event",
				})
			}
		}(g)
	}

	// 5 concurrent readers
	for r := 0; r < 5; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_ = rb.GetRecent()
				time.Sleep(1 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
	if count := rb.Count(); count != 200 {
		t.Errorf("expected buffer count to be 200, got %d", count)
	}
}

func TestSSEBroadcaster(t *testing.T) {
	b := NewEventBroadcaster()

	ch1 := b.Subscribe()
	ch2 := b.Subscribe()

	if subs := b.SubscriberCount(); subs != 2 {
		t.Errorf("expected 2 subscribers, got %d", subs)
	}

	ev := LogEvent{
		ID:      42,
		Domain:  DomainOAuth,
		Level:   LevelAction,
		Message: "test broadcast",
	}

	b.Broadcast(ev)

	select {
	case received := <-ch1:
		if received.ID != 42 || received.Domain != DomainOAuth {
			t.Errorf("ch1 received unexpected event: %+v", received)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for ch1")
	}

	select {
	case received := <-ch2:
		if received.ID != 42 {
			t.Errorf("ch2 received unexpected event: %+v", received)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for ch2")
	}

	b.Unsubscribe(ch1)
	if subs := b.SubscriberCount(); subs != 1 {
		t.Errorf("expected 1 subscriber after unsubscribe, got %d", subs)
	}
	b.Unsubscribe(ch2)
}

func TestEventLogger_HTTPHandlers(t *testing.T) {
	logger := NewEventLogger(50)
	logger.Info(DomainSys, "system boot")
	logger.Action(DomainOAuth, "auth complete")

	// 1. Test HandleLogs
	reqLogs := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	wLogs := httptest.NewRecorder()
	logger.HandleLogs(wLogs, reqLogs)

	if wLogs.Code != http.StatusOK {
		t.Errorf("HandleLogs expected status 200, got %d", wLogs.Code)
	}

	var events []LogEvent
	if err := json.Unmarshal(wLogs.Body.Bytes(), &events); err != nil {
		t.Fatalf("failed to decode JSON logs: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Message != "system boot" || events[1].Message != "auth complete" {
		t.Errorf("unexpected events returned: %+v", events)
	}

	// 2. Test HandleEvents SSE
	server := httptest.NewServer(http.HandlerFunc(logger.HandleEvents))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sseReq, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create SSE req failed: %v", err)
	}

	resp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	// Initial comment
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read initial line: %v", err)
	}
	if !strings.HasPrefix(line, ":") {
		t.Errorf("expected initial comment line, got %q", line)
	}

	// Broadcast an event while reading
	go func() {
		time.Sleep(50 * time.Millisecond)
		logger.Error(DomainDrop, "transfer checksum failure")
	}()

	// Find data line
	for {
		l, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("failed reading SSE stream: %v", err)
		}
		if strings.HasPrefix(l, "data:") {
			var ev LogEvent
			jsonStr := strings.TrimPrefix(l, "data:")
			jsonStr = strings.TrimSpace(jsonStr)
			if err := json.Unmarshal([]byte(jsonStr), &ev); err != nil {
				t.Fatalf("failed to unmarshal SSE event json: %v", err)
			}
			if ev.Domain != DomainDrop || ev.Level != LevelError {
				t.Errorf("unexpected SSE event: %+v", ev)
			}
			break
		}
	}
}

func TestEventLogger_SSEReplaysLastEventID(t *testing.T) {
	logger := NewEventLogger(10)
	logger.Info(DomainSys, "before reconnect")
	logger.Action(DomainOAuth, "after reconnect")

	server := httptest.NewServer(http.HandlerFunc(logger.HandleEvents))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("reading replay: %v", readErr)
		}
		if strings.HasPrefix(line, "data:") {
			var ev LogEvent
			if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev); err != nil {
				t.Fatal(err)
			}
			if ev.ID != 2 || ev.Message != "after reconnect" {
				t.Fatalf("replayed event = %+v, want ID 2 after reconnect", ev)
			}
			return
		}
	}
}

func TestFormatTerminal(t *testing.T) {
	ev := LogEvent{
		ID:        1,
		Timestamp: time.Date(2026, 9, 10, 15, 30, 45, 123000000, time.UTC),
		Level:     LevelAction,
		Domain:    DomainDrop,
		Message:   "Received file test.bin",
	}
	formatted := FormatTerminal(ev)
	if !strings.Contains(formatted, "15:30:45.123") {
		t.Errorf("formatted string missing timestamp: %s", formatted)
	}
	if !strings.Contains(formatted, "DROP") {
		t.Errorf("formatted string missing domain: %s", formatted)
	}
	if !strings.Contains(formatted, "Received file test.bin") {
		t.Errorf("formatted string missing message: %s", formatted)
	}
}

func TestShortenURL(t *testing.T) {
	ghURL := "https://github.com/login/oauth/authorize?client_id=Iv1.xyz123456789&redirect_uri=http%3A%2F%2F127.0.0.1%3A54321%2Fcallback&scope=read%3Auser&state=secret12345678901234567890"
	shortGH := ShortenURL(ghURL, 60)
	if shortGH != "github.com/login/oauth/authorize?..." {
		t.Errorf("expected 'github.com/login/oauth/authorize?...', got %q", shortGH)
	}

	googleURL := "https://accounts.google.com/o/oauth2/v2/auth?client_id=123.apps.googleusercontent.com&redirect_uri=http://127.0.0.1:8080&response_type=code&scope=openid%20profile%20email"
	shortGoogle := ShortenURL(googleURL, 60)
	if shortGoogle != "accounts.google.com/o/oauth2/v2/auth?..." {
		t.Errorf("expected 'accounts.google.com/o/oauth2/v2/auth?...', got %q", shortGoogle)
	}

	plainURL := "http://127.0.0.1:9876/"
	shortPlain := ShortenURL(plainURL, 60)
	if shortPlain != "127.0.0.1:9876/" {
		t.Errorf("expected '127.0.0.1:9876/', got %q", shortPlain)
	}
}

func TestFormatTerminalSignal_URLTruncation(t *testing.T) {
	longURL := "https://github.com/login/oauth/authorize?client_id=Iv1.xyz123456789&redirect_uri=http%3A%2F%2F127.0.0.1%3A54321%2Fcallback&scope=read%3Auser&state=secret12345678901234567890"
	ev := LogEvent{
		ID:        2,
		Timestamp: time.Now(),
		Level:     LevelInfo,
		Domain:    DomainOAuth,
		Message:   "Opening browser for URL: " + longURL,
	}

	res := FormatTerminalSignal(ev)
	if strings.Contains(res, "redirect_uri") {
		t.Errorf("expected long query params to be truncated, got: %s", res)
	}
	if !strings.Contains(res, "github.com/login/oauth/authorize?...") {
		t.Errorf("expected shortened URL in output, got: %s", res)
	}

	// Test debug level prefix
	debugEv := LogEvent{
		ID:        3,
		Timestamp: time.Now(),
		Level:     LevelDebug,
		Domain:    DomainNet,
		Message:   "Multiplexer: routing connection",
	}
	debugRes := FormatTerminalSignal(debugEv)
	if !strings.Contains(debugRes, "[DBG]") {
		t.Errorf("expected [DBG] prefix for LevelDebug, got: %s", debugRes)
	}
}
