package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// EventLevel represents the severity of a log event.
type EventLevel string

const (
	LevelDebug  EventLevel = "DEBUG"
	LevelInfo   EventLevel = "INFO"
	LevelAction EventLevel = "ACTION"
	LevelWarn   EventLevel = "WARN"
	LevelError  EventLevel = "ERROR"
)

// EventDomain categorizes the subsystem emitting the event.
type EventDomain string

const (
	DomainOAuth EventDomain = "OAUTH"
	DomainDrop  EventDomain = "DROP"
	DomainPeer  EventDomain = "PEER"
	DomainNet   EventDomain = "NET"
	DomainSys   EventDomain = "SYS"
)

// LogEvent represents a structured, immutable event in the Hub.
type LogEvent struct {
	ID        uint64            `json:"id"`
	Timestamp time.Time         `json:"timestamp"`
	Level     EventLevel        `json:"level"`
	Domain    EventDomain       `json:"domain"`
	Message   string            `json:"message"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// DefaultRingBufferSize is the fixed capacity for the Hub event log buffer.
const DefaultRingBufferSize = 200

// RingBuffer stores the last N LogEvents with zero dynamic memory allocation.
type RingBuffer struct {
	mu       sync.RWMutex
	capacity int
	events   []LogEvent
	start    int
	count    int
}

// NewRingBuffer allocates a new ring buffer with the specified capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = DefaultRingBufferSize
	}
	return &RingBuffer{
		capacity: capacity,
		events:   make([]LogEvent, capacity),
	}
}

// Append adds a new event to the circular buffer, overwriting the oldest event if full.
func (rb *RingBuffer) Append(event LogEvent) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.count < rb.capacity {
		rb.events[rb.count] = event
		rb.count++
	} else {
		rb.events[rb.start] = event
		rb.start = (rb.start + 1) % rb.capacity
	}
}

// GetRecent returns all stored events in chronological order.
func (rb *RingBuffer) GetRecent() []LogEvent {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	res := make([]LogEvent, rb.count)
	if rb.count < rb.capacity {
		copy(res, rb.events[:rb.count])
	} else {
		n := rb.capacity - rb.start
		copy(res, rb.events[rb.start:])
		copy(res[n:], rb.events[:rb.start])
	}
	return res
}

// Count returns the number of events currently stored in the buffer.
func (rb *RingBuffer) Count() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.count
}

// Clear resets the buffer.
func (rb *RingBuffer) Clear() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.start = 0
	rb.count = 0
}

// EventBroadcaster manages real-time event distribution to connected SSE clients.
type EventBroadcaster struct {
	mu      sync.RWMutex
	clients map[chan LogEvent]struct{}
}

// NewEventBroadcaster creates an SSE broadcaster.
func NewEventBroadcaster() *EventBroadcaster {
	return &EventBroadcaster{
		clients: make(map[chan LogEvent]struct{}),
	}
}

const maxSSEClients = 32

// TrySubscribe registers a subscriber if the bounded client budget allows it.
func (b *EventBroadcaster) TrySubscribe() (chan LogEvent, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.clients) >= maxSSEClients {
		return nil, false
	}
	ch := make(chan LogEvent, 64)
	b.clients[ch] = struct{}{}
	return ch, true
}

// Subscribe registers a new subscriber channel. It is retained for callers
// that do not need admission control; HTTP handlers should use TrySubscribe.
func (b *EventBroadcaster) Subscribe() chan LogEvent {
	ch, ok := b.TrySubscribe()
	if !ok {
		return nil
	}
	return ch
}

// Unsubscribe removes and closes a subscriber channel.
func (b *EventBroadcaster) Unsubscribe(ch chan LogEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.clients[ch]; ok {
		delete(b.clients, ch)
		close(ch)
	}
}

// Broadcast sends the event to all subscribers without blocking.
func (b *EventBroadcaster) Broadcast(event LogEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.clients {
		select {
		case ch <- event:
		default:
			// Non-blocking drop for slow clients to avoid stalling the pipeline
		}
	}
}

// SubscriberCount returns the number of active SSE subscribers.
func (b *EventBroadcaster) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

// EventLogger coordinates the RingBuffer, EventBroadcaster, and ID generation.
type EventLogger struct {
	ring        *RingBuffer
	broadcaster *EventBroadcaster
	nextID      uint64
	// sequenceMu makes ID assignment, ring insertion, and subscriber fan-out
	// one ordered transaction. Without it, concurrent Log calls can append a
	// higher ID before a lower one, making Last-Event-ID replay skip events.
	sequenceMu sync.Mutex
	onEventMu  sync.RWMutex
	onEvent    func(LogEvent)
}

// NewEventLogger creates an EventLogger with a circular buffer.
func NewEventLogger(capacity int) *EventLogger {
	return &EventLogger{
		ring:        NewRingBuffer(capacity),
		broadcaster: NewEventBroadcaster(),
	}
}

// SetOnEvent sets an optional callback invoked on every logged event (e.g. for terminal output).
func (l *EventLogger) SetOnEvent(fn func(LogEvent)) {
	l.onEventMu.Lock()
	l.onEvent = fn
	l.onEventMu.Unlock()
}

func sanitizeLogText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	if len(value) > 4096 {
		value = value[:4096] + "…"
	}
	return value
}

// Log records and broadcasts a new structured LogEvent.
func (l *EventLogger) Log(level EventLevel, domain EventDomain, msg string, meta map[string]string) LogEvent {
	msg = redactLogText(sanitizeLogText(msg))
	if meta != nil {
		copyMeta := make(map[string]string, len(meta))
		for key, value := range meta {
			copyMeta[sanitizeLogText(key)] = redactLogText(sanitizeLogText(value))
		}
		meta = copyMeta
	}
	l.sequenceMu.Lock()
	id := atomic.AddUint64(&l.nextID, 1)
	ev := LogEvent{
		ID:        id,
		Timestamp: time.Now(),
		Level:     level,
		Domain:    domain,
		Message:   msg,
		Metadata:  meta,
	}
	l.ring.Append(ev)
	l.broadcaster.Broadcast(ev)
	l.sequenceMu.Unlock()
	l.onEventMu.RLock()
	onEvent := l.onEvent
	l.onEventMu.RUnlock()
	if onEvent != nil {
		onEvent(ev)
	}
	return ev
}

// Info logs an informational event.
func (l *EventLogger) Info(domain EventDomain, msg string) LogEvent {
	return l.Log(LevelInfo, domain, msg, nil)
}

// Action logs an important user or system action (e.g., file transferred, auth complete).
func (l *EventLogger) Action(domain EventDomain, msg string) LogEvent {
	return l.Log(LevelAction, domain, msg, nil)
}

// Warn logs a warning event.
func (l *EventLogger) Warn(domain EventDomain, msg string) LogEvent {
	return l.Log(LevelWarn, domain, msg, nil)
}

// Error logs an error event.
func (l *EventLogger) Error(domain EventDomain, msg string) LogEvent {
	return l.Log(LevelError, domain, msg, nil)
}

// Debug logs a debug-level event.
func (l *EventLogger) Debug(domain EventDomain, msg string) LogEvent {
	return l.Log(LevelDebug, domain, msg, nil)
}

// GetRecent returns the recent historical events from the ring buffer.
func (l *EventLogger) GetRecent() []LogEvent {
	return l.ring.GetRecent()
}

// HandleLogs serves historical logs as JSON for dashboard hydration (GET /api/logs).
func (l *EventLogger) HandleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(l.GetRecent())
}

// HandleEvents streams events over Server-Sent Events (GET /api/events).
// When a browser reconnects with Last-Event-ID, events still in the bounded
// ring are replayed before live delivery. Slow clients can still miss events
// while connected (the broadcaster is intentionally non-blocking), but a
// reconnect no longer starts blindly at "now".
func (l *EventLogger) HandleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch, admitted := l.broadcaster.TrySubscribe()
	if !admitted {
		http.Error(w, "too many event subscribers", http.StatusServiceUnavailable)
		return
	}
	defer l.broadcaster.Unsubscribe(ch)

	// Subscribe before taking the ring snapshot. Events arriving during the
	// replay remain queued and are de-duplicated by lastSentID below.
	lastSentID := uint64(0)
	if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		if parsed, parseErr := strconv.ParseUint(raw, 10, 64); parseErr == nil {
			lastSentID = parsed
		}
	}

	// Send initial connection comment.
	_, _ = fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	writeEvent := func(ev LogEvent) bool {
		data, marshalErr := json.Marshal(ev)
		if marshalErr != nil {
			return true
		}
		if _, writeErr := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.ID, data); writeErr != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if lastSentID > 0 {
		for _, ev := range l.GetRecent() {
			if ev.ID <= lastSentID {
				continue
			}
			if !writeEvent(ev) {
				return
			}
			lastSentID = ev.ID
		}
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.ID <= lastSentID {
				continue
			}
			if !writeEvent(ev) {
				return
			}
			lastSentID = ev.ID
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// ShortenURL extracts host + path from a URL and truncates with ?... if query exists or length exceeds maxLen.
func ShortenURL(rawURL string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 60
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "<invalid-url>"
	}

	summary := u.Host + u.Path
	if u.RawQuery != "" {
		summary += "?..."
	}
	if len(summary) > maxLen && maxLen > 3 {
		return summary[:maxLen-3] + "..."
	}
	return summary
}

func replaceURLs(msg string, maxLen int) string {
	words := strings.Fields(msg)
	for i, w := range words {
		clean := strings.TrimRight(w, ",.;)]}")
		trailing := w[len(clean):]
		if strings.HasPrefix(clean, "http://") || strings.HasPrefix(clean, "https://") {
			words[i] = ShortenURL(clean, maxLen) + trailing
		}
	}
	return strings.Join(words, " ")
}

var sensitiveQueryValue = regexp.MustCompile(`(?i)([?&](?:code|state|access_token|refresh_token|id_token|token|assertion|code_verifier|client_secret|error_description)=)[^&\s]+`)

// redactLogText removes OAuth query values even when a caller bypasses the
// normal URL formatting helpers.  The key names remain visible for debugging,
// but authorization codes, state values, and token material never enter the
// ring buffer or terminal fallback.
func redactLogText(msg string) string {
	msg = replaceURLs(msg, 120)
	return sensitiveQueryValue.ReplaceAllString(msg, `${1}<redacted>`)
}

// FormatTerminalSignal renders a clean, single-line colorized badge:
// 15:04:05 [DOMAIN] [LEVEL] message, truncating any embedded URLs to ensure zero line-wrapping.
func FormatTerminalSignal(event LogEvent) string {
	timeStr := event.Timestamp.Format("15:04:05.000")
	var domainColor string
	switch event.Domain {
	case DomainOAuth:
		domainColor = "\033[34m" // Blue
	case DomainDrop:
		domainColor = "\033[32m" // Green
	case DomainPeer:
		domainColor = "\033[35m" // Magenta
	case DomainNet:
		domainColor = "\033[33m" // Yellow
	default:
		domainColor = "\033[36m" // Cyan
	}

	var levelPrefix string
	switch event.Level {
	case LevelError:
		levelPrefix = "\033[31m[ERR]\033[0m "
	case LevelWarn:
		levelPrefix = "\033[33m[WRN]\033[0m "
	case LevelAction:
		levelPrefix = "\033[1;32m[ACT]\033[0m "
	case LevelDebug:
		levelPrefix = "\033[90m[DBG]\033[0m "
	}

	displayMsg := replaceURLs(event.Message, 60)
	return fmt.Sprintf("%s %s[%-5s]\033[0m %s%s", timeStr, domainColor, event.Domain, levelPrefix, displayMsg)
}

// FormatTerminal renders a LogEvent with clean ANSI colors and aligned domain tags.
func FormatTerminal(event LogEvent) string {
	return FormatTerminalSignal(event)
}
