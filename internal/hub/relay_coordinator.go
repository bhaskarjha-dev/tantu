package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/browser"
)

const (
	maxRelayActiveEntries = 128
)

// relayCoordinator coalesces duplicate OAuth requests before they reach the
// wire.  Browser launchers and CLI clients often retry the same authorization
// URL; without single-flight behavior every retry opens another browser
// session and competes for the same localhost callback port.
type relayCoordinator struct {
	mu     sync.Mutex
	active map[string]*relayCall
}

type relayCall struct {
	done chan struct{}
	err  error
}

func newRelayCoordinator() *relayCoordinator {
	return &relayCoordinator{
		active: make(map[string]*relayCall),
	}
}

// Do runs fn once for key. Concurrent callers wait for the leader's result.
// Deliberately there is no post-success replay: the request key normalizes
// away per-attempt OAuth values (state, PKCE, loopback port), so replaying a
// past success would report "completed" for a login whose own callback was
// never delivered. A duplicate after completion runs the flow again, which is
// slower but correct. Failures are not cached either, so a transient
// dial/browser error is immediately retryable.
func (c *relayCoordinator) Do(ctx context.Context, key string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil {
		return fn()
	}
	c.mu.Lock()
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
	close(call.done)
	c.mu.Unlock()
	return err
}

// relayRequestKey builds a bounded, non-secret key.  It intentionally hashes
// the canonical URL rather than retaining OAuth query values in memory. The
// normalized flow identity is paired with the exact-request identity, so
// identical retries coalesce while a new login (fresh per-attempt values)
// always runs its own flow.
func relayRequestKey(peer, rawURL string) string {
	canonical := browser.SanitizeURL(strings.TrimSpace(rawURL))
	flowID := bridge.DeriveOAuthFlowID(canonical)
	if flowID != "" {
		raw := strings.TrimSpace(peer) + "\x00" + flowID + "\x00" + bridge.RequestURLIdentity(canonical)
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
// listener behind. It returns the recorded attempt ID ("" when the request
// was coalesced onto another caller's in-flight flow) so API responses can
// point at the durable authorization record.
func (h *Hub) relayOAuth(ctx context.Context, peer, rawURL string) (string, error) {
	oauthURL := browser.SanitizeURL(strings.TrimSpace(rawURL))
	if len(oauthURL) > 64*1024 {
		return h.recordRelayFailure(peer, "", "invalid_input", "Fix the URL and retry."), errors.New("OAuth authorization URL exceeds the maximum supported length")
	}
	if err := browser.ValidateURL(oauthURL); err != nil {
		return h.recordRelayFailure(peer, safeRelayOrigin(oauthURL), "invalid_input", "Fix the URL and retry."), err
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
	h.mu.RUnlock()
	activePeer := h.GetActivePeer()
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
			return h.recordRelayFailure(peer, safeRelayOrigin(oauthURL), "destination_unreachable", "Check the peer is online and paired, then retry."), resolveErr
		}
	}
	key := relayRequestKey(keyPeer, oauthURL)
	var attemptID string
	err := coordinator.Do(ctx, key, func() error {
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

		attemptID = newRelayID()
		destName, destFP, destAddr := h.resolveOperationDestination(dialPeer)
		origin := safeRelayOrigin(oauthURL)
		now := time.Now()
		h.recordRelayAttempt(RelayAttempt{
			ID: attemptID, TargetPeer: destName, TargetFP: destFP,
			TargetAddress: destAddr, SafeOrigin: origin,
			State: RelayStateSubmitted, CreatedAt: now, UpdatedAt: now,
			DiagnosticID: attemptID,
		})
		h.updateRelayAttempt(attemptID, RelayStateWaiting, "", "")

		conn, err := h.DialPeer(dialPeer)
		if err != nil {
			h.updateRelayAttempt(attemptID, RelayStateFailed, "destination_unreachable", "Check the peer is online and paired, then retry.")
			return err
		}
		defer conn.Close()
		if err := bridge.HandleBSide(relayCtx, conn, oauthURL, bridge.BSideConfig{Timeout: timeout}); err != nil {
			state, code, next := RelayStateFailed, "relay_failed", "Check the peer is online, then retry the login."
			if ctx.Err() != nil || relayCtx.Err() != nil {
				state, code, next = RelayStateCancelled, "cancelled", "Retry the login flow."
			} else if msg := strings.ToLower(err.Error()); strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
				code, next = "relay_timeout", "Retry; the authorization callback may have expired."
			}
			h.updateRelayAttempt(attemptID, state, code, next)
			return err
		}
		h.updateRelayAttempt(attemptID, RelayStateComplete, "", "")
		return nil
	})
	return attemptID, err
}

// recordRelayFailure records a terminal failed attempt that never reached
// the wire (validation or resolution) and returns its ID. Only the safe
// origin host is kept, never the raw URL, codes, or tokens.
func (h *Hub) recordRelayFailure(peer, safeOrigin, code, next string) string {
	destName, destFP, destAddr := h.resolveOperationDestination(peer)
	now := time.Now()
	id := newRelayID()
	h.recordRelayAttempt(RelayAttempt{
		ID: id, TargetPeer: destName, TargetFP: destFP, TargetAddress: destAddr,
		SafeOrigin: safeOrigin, State: RelayStateFailed,
		CreatedAt: now, UpdatedAt: now,
		ErrorCode: code, NextAction: next, DiagnosticID: id,
	})
	return id
}

// updateRelayAttempt sets a follow-up state on a recorded attempt and
// persists the ledger best-effort.
func (h *Hub) updateRelayAttempt(id, state, code, next string) {
	if h == nil {
		return
	}
	h.mu.RLock()
	ledger := h.relayHistory
	h.mu.RUnlock()
	if ledger == nil {
		return
	}
	ledger.Update(id, func(op *RelayAttempt) {
		op.State = state
		op.ErrorCode = code
		op.NextAction = next
	})
	if dir := h.hubStoreDir(); dir != "" {
		if err := persistRelayHistory(dir, ledger.GetAll()); err != nil && h.logger != nil {
			h.logger.Warn(DomainOAuth, fmt.Sprintf("Could not save authorization history: %v", err))
		}
	}
}

// relayAttemptNextAction returns the recorded recovery guidance for an
// attempt, letting API responses echo exactly what the ledger holds.
func (h *Hub) relayAttemptNextAction(id string) string {
	if h == nil || id == "" {
		return ""
	}
	h.mu.RLock()
	ledger := h.relayHistory
	h.mu.RUnlock()
	if ledger == nil {
		return ""
	}
	for _, op := range ledger.GetAll() {
		if op.ID == id {
			return op.NextAction
		}
	}
	return ""
}
