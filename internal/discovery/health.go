package discovery

// Health reporting: what the discovery engine is actually doing, in words a
// user can act on.
//
// This exists because the previous design discarded every signal. Broadcast
// write errors were dropped on the floor, the mDNS listener was a best-effort
// fallback whose bind failure was never recorded, and a receive loop that gave
// up after 50 consecutive errors simply returned. The result was a Hub that
// showed discovery as running, showed an empty "Nearby" list, and offered no
// explanation — which is indistinguishable from "there is no second machine on
// this network". A user who cannot tell those apart has exactly two options:
// reinstall everything, or buy a second hub.
//
// So the engine counts what it does and reports it. Nothing here is a claim
// about the network: BeaconsSent counts writes this process completed,
// BeaconsReceived counts beacons this process accepted, and neither says
// anything about whether a peer exists.

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Health is a point-in-time report of one discovery engine.
//
// Every field is a fact about this process. Health is safe to serialise to the
// dashboard: it contains no host identity, no peer address, and nothing a peer
// sent beyond the counter of beacons that passed validation.
type Health struct {
	// Running is true between a successful Start and the next Close.
	Running bool `json:"running"`
	// Broadcast is true when the IPv4 subnet-broadcast listener is bound.
	Broadcast bool `json:"broadcast"`
	// Multicast is true when the IPv4 mDNS listener is bound.
	Multicast bool `json:"multicast"`
	// Usable is true when at least one usable discovery socket is bound.
	Usable bool `json:"usable"`
	// Degraded means the engine is running but cannot see peers. It is
	// deliberately narrower than "something is off": a blocked mDNS socket on
	// a network where subnet broadcast works is normal, not a fault, and
	// reporting it as one would train users to ignore the warning.
	Degraded bool `json:"degraded"`
	// Detail is one plain sentence describing the current state. It is never
	// empty while Running, because an unlabelled status is the defect this
	// type was written to remove.
	Detail string `json:"detail"`
	// Family is the address family discovery can see peers on.
	Family string `json:"family"`
	// BeaconsSent and BeaconsReceived are this process's own counters.
	BeaconsSent     int64 `json:"beacons_sent"`
	BeaconsReceived int64 `json:"beacons_received"`
	// Nodes is how many peers are currently in the table.
	Nodes int `json:"nodes"`
	// SendErrors and ListenErrors are cumulative for the engine's lifetime.
	SendErrors   int64 `json:"send_errors"`
	ListenErrors int64 `json:"listen_errors"`
	// LastError is the most recent failure, redacted of anything a peer
	// supplied (only this process's own socket errors are recorded).
	LastError string `json:"last_error,omitempty"`
	// Since is when the engine started, so a UI can say "for 4 minutes".
	Since time.Time `json:"since,omitempty"`
}

// countSend, countReceive, countSendError, countListenError and noteError are
// the engine's only mutation points for Health. They are deliberately
// lock-free: the advertise and listen loops are hot paths and taking a mutex to
// bump a counter would make health reporting a contention source in exactly the
// code that must not be slowed down.
func (e *Engine) countSend()          { atomic.AddInt64(&e.stats.beaconsSent, 1) }
func (e *Engine) countReceive()       { atomic.AddInt64(&e.stats.beaconsReceived, 1) }
func (e *Engine) countSendError()     { atomic.AddInt64(&e.stats.sendErrors, 1) }
func (e *Engine) countListenError()   { atomic.AddInt64(&e.stats.listenErrors, 1) }
func (e *Engine) listenLoopGaveUp()   { atomic.StoreInt32(&e.stats.listenStopped, 1) }
func (e *Engine) mcastListenStopped() { atomic.StoreInt32(&e.stats.mcastStopped, 1) }

func (e *Engine) noteError(err error) {
	if err == nil {
		return
	}
	e.statsMu.Lock()
	e.lastError = truncateError(err.Error())
	e.lastErrorAt = time.Now()
	e.statsMu.Unlock()
}

func truncateError(msg string) string {
	const max = 200
	if len(msg) <= max {
		return msg
	}
	// Rune-safe, and byte-bounded: the ellipsis is three bytes in UTF-8, so
	// slicing to max-1 and appending it would overshoot the bound.
	cut := max - len("\u2026")
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut] + "\u2026"
}

// engineStats is the unexported counter block. It is a plain struct of atomics
// so it can be copied into a Health value without holding a lock on the hot
// path; the one field that needs a lock (LastError) is guarded separately.
type engineStats struct {
	beaconsSent     int64
	beaconsReceived int64
	sendErrors      int64
	listenErrors    int64
	listenStopped   int32
	mcastStopped    int32
	broadcastBound  int32
	multicastBound  int32
	bcastBindError  atomic.Value // string
	mcastBindError  atomic.Value // string
}

// Health returns the engine's current state. It is safe to call before Start
// and after Close, and reports the corresponding state rather than zero values
// that could be mistaken for "running but idle".
func (e *Engine) Health() Health {
	if e == nil {
		return Health{Running: false, Family: "none", Detail: "Discovery is not running on this Hub."}
	}
	// `started` and the two bound flags are read under one acquisition of
	// startMu, and shutdown updates all three while holding it. That is what
	// makes a snapshot coherent.
	//
	// Reading `started` and then releasing the lock before loading the bound
	// flags allowed a torn observation: shutdown clears `started` and then
	// closes the sockets, so a caller landing in that window saw Running=false
	// with Broadcast and Multicast still true. That is a state the engine
	// cannot actually be in -- it has stopped, yet appears to hold transports.
	// It surfaced as an intermittent failure of a gate that polls for
	// `!Running` and then asserts nothing is bound, and only under
	// full-suite load, because the window is short.
	//
	// Note the inverse tear is equally prevented rather than merely traded
	// for it: because the flags are read under the same lock that shutdown
	// holds, a caller that reports Running=true also observes the flags as they
	// were before shutdown began, rather than catching sockets already closed.
	e.startMu.Lock()
	running := e.started
	e.startMu.Unlock()

	h := Health{
		Running:         running,
		Broadcast:       atomic.LoadInt32(&e.stats.broadcastBound) == 1,
		Multicast:       atomic.LoadInt32(&e.stats.multicastBound) == 1,
		Family:          "IPv4 LAN (multicast + subnet broadcast)",
		BeaconsSent:     atomic.LoadInt64(&e.stats.beaconsSent),
		BeaconsReceived: atomic.LoadInt64(&e.stats.beaconsReceived),
		SendErrors:      atomic.LoadInt64(&e.stats.sendErrors),
		ListenErrors:    atomic.LoadInt64(&e.stats.listenErrors),
	}
	h.Nodes = len(e.ListNodes())
	// Bind failures are preferred over the last transient socket error: they
	// explain why a transport is *absent*, which is the question a user
	// answering "why is discovery not working" actually has. Both are joined
	// when both failed, because in the degraded case neither is redundant.
	var reasons []string
	if v, ok := e.stats.bcastBindError.Load().(string); ok && v != "" {
		reasons = append(reasons, v)
	}
	if v, ok := e.stats.mcastBindError.Load().(string); ok && v != "" {
		reasons = append(reasons, v)
	}
	if len(reasons) > 0 {
		h.LastError = strings.Join(reasons, "; ")
	}
	e.statsMu.Lock()
	if e.lastError != "" && h.LastError == "" {
		h.LastError = e.lastError
	}
	e.statsMu.Unlock()

	h.Usable = h.Running && (h.Broadcast || h.Multicast)
	h.Degraded = h.Running && !h.Usable
	if running {
		e.statsMu.Lock()
		h.Since = e.startedAt
		e.statsMu.Unlock()
	}
	h.Detail = describeHealth(h, atomic.LoadInt32(&e.stats.listenStopped) == 1, atomic.LoadInt32(&e.stats.mcastStopped) == 1)
	return h
}

// describeHealth renders the one sentence the dashboard shows. It is a
// function rather than an inline template so the wording is testable on its own
// and cannot drift between the Go server and the page.
//
// Usability is derived here rather than read from Health.Usable so the wording
// depends only on the socket state a caller can actually observe; a caller that
// forgot to populate a derived field would otherwise get a sentence describing
// a state it is not in.
func describeHealth(h Health, broadcastStopped, multicastStopped bool) string {
	usable := h.Broadcast || h.Multicast
	switch {
	case !h.Running:
		return "Discovery is not running on this Hub."
	case !usable:
		return "Discovery cannot reach this network: neither multicast nor subnet broadcast could be opened. Pair by entering the other machine's address instead."
	case h.BeaconsSent == 0 && h.SendErrors > 0:
		// Sockets are bound, so every "discovery is running on X" sentence below
		// would be true and useless at once: this hub has announced itself
		// nothing and had every announcement refused, so no peer can see it. That
		// is the state a host with no multicast-capable interface produces, and
		// it was reported as healthy -- the precise "looks fine, is invisible"
		// failure this surface exists to remove. Found by a macOS CI run where
		// the gate for the best-effort binds failed and the health block read
		// "running on multicast only" while 100 of 100 sends had errored.
		return fmt.Sprintf("Discovery is bound but cannot announce this hub: every beacon send is failing (%d attempt(s)), so no peer can see it. Pair by entering the other machine's address instead.", h.SendErrors)
	case broadcastStopped && multicastStopped:
		return "Discovery sockets stopped responding after repeated errors, so nearby hubs may be missing. Restart the Hub to retry."
	case broadcastStopped:
		return "Subnet broadcast stopped responding after repeated errors; only multicast discovery is still working."
	case multicastStopped:
		return "Multicast discovery stopped responding after repeated errors; only subnet broadcast is still working."
	case h.Broadcast && h.Multicast:
		if h.Nodes == 0 {
			return "Discovery is running on multicast and subnet broadcast. No nearby hubs have answered yet \u2014 check the other machine is on this network and its Hub is running."
		}
		return fmt.Sprintf("Discovery is running on multicast and subnet broadcast; %d nearby hub(s) in reach.", h.Nodes)
	case h.Broadcast:
		if h.Nodes == 0 {
			return "Discovery is running on subnet broadcast only (multicast is unavailable on this network). No nearby hubs have answered yet."
		}
		return fmt.Sprintf("Discovery is running on subnet broadcast only (multicast is unavailable); %d nearby hub(s) in reach.", h.Nodes)
	default:
		if h.Nodes == 0 {
			return "Discovery is running on multicast only (subnet broadcast is unavailable on this network). No nearby hubs have answered yet."
		}
		return fmt.Sprintf("Discovery is running on multicast only; %d nearby hub(s) in reach.", h.Nodes)
	}
}
