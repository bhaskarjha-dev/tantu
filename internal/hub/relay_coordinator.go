package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/browser"
)

const (
	defaultRelayReplayWindow = 45 * time.Second
	maxRelayReplayEntries    = 256
	maxRelayActiveEntries    = 128
)

// relayCoordinator coalesces duplicate OAuth requests before they reach the
// wire.  Browser launchers and CLI clients often retry the same authorization
// URL; without single-flight behavior every retry opens another browser
// session and competes for the same localhost callback port.
type relayCoordinator struct {
	mu     sync.Mutex
	active map[string]*relayCall
	recent map[string]time.Time
	window time.Duration
}

type relayCall struct {
	done chan struct{}
	err  error
}

func newRelayCoordinator() *relayCoordinator {
	return &relayCoordinator{
		active: make(map[string]*relayCall),
		recent: make(map[string]time.Time),
		window: defaultRelayReplayWindow,
	}
}

func (c *relayCoordinator) pruneLocked(now time.Time) {
	for key, expires := range c.recent {
		if now.After(expires) {
			delete(c.recent, key)
		}
	}
	for len(c.recent) > maxRelayReplayEntries {
		var oldestKey string
		var oldest time.Time
		for key, expires := range c.recent {
			if oldestKey == "" || expires.Before(oldest) {
				oldestKey, oldest = key, expires
			}
		}
		if oldestKey == "" {
			break
		}
		delete(c.recent, oldestKey)
	}
}

// Do runs fn once for key. Concurrent callers wait for the same result, and a
// successful result is replayable for a short window. Failures are not cached
// so a user can retry a transient dial/browser error immediately.
func (c *relayCoordinator) Do(ctx context.Context, key string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil {
		return fn()
	}
	c.mu.Lock()
	now := time.Now()
	c.pruneLocked(now)
	if expires, ok := c.recent[key]; ok && now.Before(expires) {
		c.mu.Unlock()
		return nil
	}
	if call, ok := c.active[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-call.done:
			return call.err
		}
	}
	if len(c.active) >= maxRelayActiveEntries {
		c.mu.Unlock()
		return errors.New("too many active OAuth relay requests")
	}

	call := &relayCall{done: make(chan struct{})}
	c.active[key] = call
	c.mu.Unlock()

	err := fn()

	c.mu.Lock()
	call.err = err
	delete(c.active, key)
	if err == nil {
		c.recent[key] = time.Now().Add(c.window)
		c.pruneLocked(time.Now())
	}
	close(call.done)
	c.mu.Unlock()
	return err
}

// relayRequestKey builds a bounded, non-secret key.  It intentionally hashes
// the canonical URL rather than retaining OAuth query values in memory.
func relayRequestKey(peer, rawURL string) string {
	canonical := browser.SanitizeURL(strings.TrimSpace(rawURL))
	flowID := bridge.DeriveOAuthFlowID(canonical)
	if flowID != "" {
		raw := strings.TrimSpace(peer) + "\x00" + flowID
		digest := sha256.Sum256([]byte(raw))
		return hex.EncodeToString(digest[:])
	}
	if parsed, err := url.Parse(canonical); err == nil {
		parsed.RawQuery = parsed.Query().Encode()
		canonical = parsed.String()
	}
	raw := peer + "\x00" + canonical
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

// relayOAuth performs one wire flow, with duplicate requests coalesced at the
// local Hub boundary. The leader uses a detached timeout context so a browser
// client disconnecting does not strand the peer connection or leave a callback
// listener behind.
func (h *Hub) relayOAuth(ctx context.Context, peer, rawURL string) error {
	oauthURL := browser.SanitizeURL(strings.TrimSpace(rawURL))
	if len(oauthURL) > 64*1024 {
		return errors.New("OAuth authorization URL exceeds the maximum supported length")
	}
	if err := browser.ValidateURL(oauthURL); err != nil {
		return err
	}
	h.mu.Lock()
	coordinator := h.relayCoordinator
	if coordinator == nil {
		coordinator = newRelayCoordinator()
		h.relayCoordinator = coordinator
	}
	h.mu.Unlock()
	keyPeer := strings.TrimSpace(peer)
	dialPeer := keyPeer
	h.mu.RLock()
	transportName := h.cfg.TransportType
	store := h.store
	activePeer := h.activePeer
	h.mu.RUnlock()
	peerKey := strings.TrimSpace(peer)
	if peerKey == "" {
		peerKey = strings.TrimSpace(activePeer)
		dialPeer = peerKey
	}
	if store != nil {
		resolved, resolveErr := store.ResolvePeer(peerKey)
		if resolveErr == nil {
			// Resolve aliases/defaults before keying replay state. Otherwise a
			// completed flow for peer A could be replayed to peer B after the
			// active/default selection changes.
			keyPeer = resolved.Fingerprint
			dialPeer = resolved.Fingerprint
		} else if transportName == "lan" {
			return resolveErr
		}
	}
	key := relayRequestKey(keyPeer, oauthURL)
	return coordinator.Do(ctx, key, func() error {
		timeout := h.cfg.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		baseCtx := ctx
		h.mu.RLock()
		if h.runContext != nil {
			baseCtx = h.runContext
		}
		h.mu.RUnlock()
		relayCtx, cancel := context.WithTimeout(baseCtx, timeout)
		defer cancel()

		conn, err := h.DialPeer(dialPeer)
		if err != nil {
			return err
		}
		defer conn.Close()
		return bridge.HandleBSide(relayCtx, conn, oauthURL, bridge.BSideConfig{Timeout: timeout})
	})
}
