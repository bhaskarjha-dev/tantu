package hub

// The proactive half of a heartbeat.
//
// The inbound half has always existed: a peer that receives a heartbeat answers
// one. Nothing ever sent one. That left a keepalive that could only be kept
// alive by the side that happened to dial, on a connection about to close
// anyway -- so it refreshed a NAT binding for the duration of a transfer and not
// for the hours between transfers, which is the only case the binding matters
// in.
//
// It also left the Hub unable to answer the one question a user has about a
// peer: can I reach it right now? The store could say when an address was last
// *proven*, but a peer that is up and idle would have looked identical to one
// that had been gone for a week, so that field was unusable as a status signal
// and nothing in the product read it.
//
// This closes both halves, and they are one change: a heartbeat that asks and
// waits for an answer keeps the binding alive *and* is what makes "last proven"
// mean something.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
)

// The schedule.
//
// peerHeartbeatFirstDelay is deliberately short and separate from the steady
// state: a Hub that just started should not sit blank for a minute before it can
// tell the user anything, and the first attempt is also what picks up a peer
// whose address moved while this Hub was down.
//
// The steady interval is a compromise. 30s is what most stateful firewalls want;
// five minutes is nearly free but too stale to be worth showing. Sixty seconds
// keeps a binding alive comfortably and is short enough that "answered 70
// seconds ago" is a useful thing to read.
//
// The backoff is what makes an absent peer cheap rather than a background process
// that dials a dead machine forever. Doubling from one minute reaches the
// fifteen-minute ceiling in four failures -- about six attempts in the first
// half hour, then four an hour. That is the right shape for a machine that is
// closed, renamed, or on a different network, which is most of the time a peer
// is unreachable.
//
// There is deliberately no jitter. This is one Hub talking to its own paired
// peers, not a fleet, and a deterministic schedule is one fewer thing to reason
// about in a bug that only shows on Tuesdays.
const (
	peerHeartbeatFirstDelay = 5 * time.Second
	peerHeartbeatInterval   = 60 * time.Second
	peerHeartbeatMaxBackoff = 15 * time.Minute

	// peerHeartbeatTick is how often the scheduler wakes to notice work that is
	// due. It is not the probe rate -- peers are probed when their own schedule
	// says so -- it only bounds how late a due probe can be, so that stopping the
	// Hub does not wait out a whole interval.
	peerHeartbeatTick = time.Second

	// peerHeartbeatMaxProbesPerInterval caps the *aggregate* probe rate.
	//
	// This is the bound the first version lacked. Probing every paired peer once
	// per interval is nothing for the two or three peers anyone actually pairs,
	// and a background dialer that scales without limit is the wrong shape for a
	// tool that runs on laptops and, per its own README, on metered and awkward
	// networks. Twenty peers at the default interval is twenty full mTLS
	// handshakes a minute, forever, and nothing in the design said no.
	//
	// The cap is expressed as a rate rather than a peer count because the rate is
	// the thing that costs something. Six probes per interval is comfortably
	// above any realistic pairing and below anything a user would notice, and
	// past it each peer is simply probed less often rather than some being
	// silently dropped -- a peer you have not paired with cannot be the peer you
	// were about to send to.
	peerHeartbeatMaxProbesPerInterval = 6

	// peerHeartbeatMaxConcurrent caps probes in flight at once.
	//
	// Without it the loop probed peers one at a time, so a single blackholed peer
	// -- a laptop that has gone to sleep behind a NAT that swallows SYNs rather
	// than refusing them -- held up every other peer for the full dial timeout.
	// One unreachable machine making six reachable ones look unreachable is a
	// worse failure than the traffic the concurrency costs.
	//
	// Eight rather than four, and the reason is worth keeping. A probe holds its
	// slot for the whole dial timeout, and a blackholed peer therefore occupies
	// one for five seconds. With four slots, four sleeping machines fill the pool
	// and a healthy peer competes for the leftovers on every tick -- at four
	// blackholes it took over twelve seconds to be probed, because the blackholes
	// re-acquire each slot as it frees and the healthy peer usually loses. Raising
	// the pool does not weaken the bound; it stops the bound from being the thing
	// that decides which peers get starved.
	peerHeartbeatMaxConcurrent = 8
)

// PeerHealth is what this Hub currently believes about a peer's reachability.
//
// Runtime state, deliberately not in the peer store. The store is trust: who this
// machine has decided to believe, and where. Reachability is neither, and writing
// it to peers.json would mean every Hub restart rewrote a file whose whole
// argument is that it changes only when trust does.
type PeerHealth struct {
	// Reachable is whether the most recent probe was answered.
	Reachable bool `json:"reachable"`
	// LastChecked is when the most recent probe completed, either way.
	LastChecked time.Time `json:"last_checked"`
	// LastOK is when the peer last answered. Zero if it never has.
	LastOK time.Time `json:"last_ok"`
	// ConsecutiveFailures drives the backoff and is what lets a UI tell
	// "asleep" from "gone". Reported rather than hidden.
	ConsecutiveFailures int `json:"consecutive_failures"`
}

// failed records an unanswered probe, preserving the last success.
//
// LastOK is not cleared: a peer that stopped answering is not a peer that was
// never there. That distinction is the difference between "this was working ten
// minutes ago" and "I have never managed to reach this machine", which are
// different bugs with different fixes.
func (h PeerHealth) failed(now time.Time) PeerHealth {
	h.Reachable = false
	h.LastChecked = now
	h.ConsecutiveFailures++
	return h
}

func (h PeerHealth) succeeded(now time.Time) PeerHealth {
	h.Reachable = true
	h.LastChecked = now
	h.LastOK = now
	h.ConsecutiveFailures = 0
	return h
}

// peerProbe is one peer's scheduling state.
type peerProbe struct {
	next     time.Time
	interval time.Duration
	// failures is the rung on the backoff ladder. Kept explicitly rather than
	// derived from the interval, because the first failure must retry at the
	// normal cadence and doubling from the first one would make a peer that
	// was answering a second ago wait two minutes for the next attempt -- which
	// is the peer most likely to come straight back.
	failures int
}

// scheduleProbeNext sets when this peer is next probed, backing off on failure.
//
// The first failure retries at the base interval, the second at twice it, the
// third at four times, and so on up to a ceiling.
//
// base is the steady-state interval, so a configured interval scales the whole
// ladder rather than only its first rung. The ceiling is the smaller of sixteen
// times the base and the absolute maximum: a ceiling that scaled freely with the
// base would mean a generous interval also buys a generous ceiling, and the
// ceiling is precisely the thing that keeps an absent peer from being dialled all
// afternoon.
func scheduleProbeNext(probe *peerProbe, ok bool, now time.Time, base time.Duration) {
	if ok {
		probe.failures = 0
		probe.interval = base
	} else {
		probe.failures++
		probe.interval = base << (probe.failures - 1)
		if max := heartbeatMaxBackoffFor(base); probe.interval > max {
			probe.interval = max
		}
	}
	probe.next = now.Add(probe.interval)
}

// heartbeatIntervalForPeers is the per-peer interval once the aggregate rate cap
// is applied.
//
// Below the cap every peer keeps the configured interval, so nothing changes for
// a realistic pairing. Past it, each peer is probed proportionally less often and
// the total rate stays flat. One interval is spent per peer beyond the cap, which
// keeps the arithmetic obvious: six peers at 60s, twelve at 120s, eighteen at
// 180s.
func heartbeatIntervalForPeers(base time.Duration, peers int) time.Duration {
	if peers <= peerHeartbeatMaxProbesPerInterval {
		return base
	}
	rounds := (peers + peerHeartbeatMaxProbesPerInterval - 1) / peerHeartbeatMaxProbesPerInterval
	return base * time.Duration(rounds)
}

// heartbeatMaxBackoffFor caps the backoff ladder.
func heartbeatMaxBackoffFor(base time.Duration) time.Duration {
	if scaled := base * 16; scaled < peerHeartbeatMaxBackoff {
		return scaled
	}
	return peerHeartbeatMaxBackoff
}

// heartbeatIntervalFor returns the configured steady-state probe interval.
func (h *Hub) heartbeatIntervalFor() time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.cfg.PeerHeartbeatInterval > 0 {
		return h.cfg.PeerHeartbeatInterval
	}
	return peerHeartbeatInterval
}

// runPeerHeartbeat probes every paired peer on a schedule until ctx ends.
//
// One goroutine for all peers rather than one per peer: a laptop with eight
// paired peers would otherwise hold eight timers, and every one of them would
// have to be reasoned about separately when stopping. The tick is coarse and the
// per-peer state decides who is actually due.
func (h *Hub) runPeerHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(peerHeartbeatTick)
	defer ticker.Stop()

	interval := h.heartbeatIntervalFor()
	probes := make(map[string]*peerProbe)
	// probesMu guards `probes`, which the tick loop and the probe goroutines
	// both touch. A probe updates its own entry when it finishes.
	var probesMu sync.Mutex

	// A cap on probes in flight, acquired before the goroutine is spawned rather
	// than inside it, so that a slow peer cannot cause unbounded goroutines
	// either. Acquired non-blockingly: a peer that cannot get a slot keeps its
	// existing `next` and is retried on the following tick, so it is delayed
	// rather than starved.
	sem := make(chan struct{}, peerHeartbeatMaxConcurrent)

	// syncProbes drops peers that are no longer trusted, so a machine that was
	// unpaired stops being probed and stops being reported. Without this the map
	// is harmless for traffic but keeps answering questions about a peer this
	// machine has decided to stop trusting.
	//
	// One read of the store per call, not one per peer. It used to call
	// store.GetPeer for each entry in `probes`, and every one of those is a full
	// read and parse of peers.json -- so a store with N peers cost N+1 reads a
	// second, forever, to answer a question the list already in hand answers.
	//
	// The membership set is built outside probesMu for a second reason: the probe
	// goroutines take that lock to schedule their next attempt, so holding it
	// across disk I/O stalls them too.
	syncProbes := func(now time.Time) {
		h.mu.RLock()
		store := h.store
		h.mu.RUnlock()
		if store == nil {
			return
		}
		list := store.ListPeers()

		trusted := make(map[string]struct{}, len(list))
		for _, p := range list {
			trusted[p.Fingerprint] = struct{}{}
		}

		probesMu.Lock()
		for _, p := range list {
			if _, ok := probes[p.Fingerprint]; ok {
				continue
			}
			// Spread the first round across one interval rather than firing
			// every peer on the same tick. Deterministic, so there is no jitter
			// to reason about, and it does not reshuffle when a peer is added.
			offset := time.Duration(0)
			if n := len(list); n > 1 {
				idx := 0
				for i, q := range list {
					if q.Fingerprint == p.Fingerprint {
						idx = i
						break
					}
				}
				offset = time.Duration(int64(idx) * int64(interval) / int64(n))
			}
			probes[p.Fingerprint] = &peerProbe{
				next:     now.Add(offset),
				interval: interval,
			}
		}
		var dropped []string
		for key := range probes {
			if _, ok := trusted[key]; !ok {
				delete(probes, key)
				dropped = append(dropped, key)
			}
		}
		probesMu.Unlock()

		// Health is cleared outside probesMu, which setPeerHealth does not need
		// and which the probe goroutines are using.
		for _, key := range dropped {
			h.setPeerHealth(key, PeerHealth{})
		}
	}

	// The aggregate rate, not the per-peer one, is what has to stay bounded. See
	// heartbeatIntervalForPeers.
	probeInterval := func() time.Duration {
		probesMu.Lock()
		n := len(probes)
		probesMu.Unlock()
		return heartbeatIntervalForPeers(interval, n)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			h.mu.RLock()
			tr := h.tr
			transportName := h.cfg.TransportType
			h.mu.RUnlock()

			switch {
			case tr == nil:
				// Not listening yet. Wait for the next tick.

			case transportName != "lan":
				// Only lan peers have addresses that decay and bindings that
				// expire. Loopback is the same machine by definition, and SSH is
				// an explicit single path a probe would add nothing to.
				//
				// Nothing here reads the store, and that is the point. The probe
				// map exists only to decide who to probe, so on a transport that
				// never probes, populating it is work whose result nothing reads.
				// It was costing a full read and parse of peers.json every second
				// for as long as the Hub ran -- on exactly the transports where
				// there was most obviously nothing to find.
				//
				// The map is therefore always empty on this arm: `probes` is
				// created per invocation of this function and only written by
				// syncProbes, which is reached only from the lan arm below. An
				// earlier version of this branch pushed every entry out to the
				// maximum backoff "so a transport that did change would start
				// probing on its own", and that was doing nothing: the branch is
				// decided from cfg.TransportType, which is fixed for the life of
				// a run, and a change would restart the Hub and rebuild this map
				// from empty.

			default:
				// Read the store once per tick, on the only transport that acts
				// on what it finds.
				syncProbes(now)

				base := probeInterval()
				probesMu.Lock()
				due := make([]string, 0, len(probes))
				for key, probe := range probes {
					if now.Before(probe.next) {
						continue
					}
					select {
					case sem <- struct{}{}:
						due = append(due, key)
					default:
						// At capacity. Leave `next` alone so this peer is
						// retried on the next tick instead of being skipped.
					}
				}
				probesMu.Unlock()

				for _, key := range due {
					k := key
					// The reservation is taken through beginBackgroundWork,
					// which refuses once the Hub is stopping.
					//
					// A plain Add here was a WaitGroup contract violation: the
					// counter can be zero when Stop calls Wait, and this line
					// could run after that -- the scheduler is inside a tick
					// when the cancellation arrives. A probe counted too late
					// is a probe Stop does not wait for, and a probe writes to
					// the peer store, which is the one thing Stop promises not
					// to leave running. See docs/KNOWN-LIMITATIONS.md row
					// 3.30.
					if !h.beginBackgroundWork() {
						<-sem
						continue
					}
					go func() {
						defer h.endBackgroundWork()
						defer func() { <-sem }()
						answered, health := h.probePeerOnce(ctx, k)
						finished := time.Now()
						probesMu.Lock()
						// Record only while the peer is still scheduled.
						//
						// syncProbes drops a peer the moment it stops being trusted,
						// and clears its health. A probe that was already in flight
						// finishes afterwards -- and used to write a verdict then,
						// resurrecting a record about a machine this Hub no longer
						// trusts. Checking *after* the dial is what closes that, and a
						// check before it cannot: the trust can be withdrawn during the
						// ten seconds a dial to an unroutable address hangs.
						if probe, still := probes[k]; still {
							h.setPeerHealth(k, health)
							scheduleProbeNext(probe, answered, finished, base)
						}
						probesMu.Unlock()
					}()
				}
			}
		}
	}
}

// probePeerOnce asks one peer whether it is there, and returns the verdict to
// record.
//
// It does not record it. The caller holds the probe schedule and already knows
// whether the peer is still tracked, and a verdict about a peer whose trust has
// been withdrawn should not be written at all -- see the call site.
//
// It also does not read the peer store to check the peer exists. It used to, and
// the read was dead: its result was used only for `p.Fingerprint`, which is the
// value already in hand, and DialPeer performs its own resolution and refuses an
// untrusted peer. So every probe paid a full read and parse of peers.json to
// learn something it already knew and to guard something checked again a moment
// later -- and guarding it *again* on this side is not what mattered. What
// mattered was guarding it *before* the dial, which cannot work, because trust can
// be withdrawn during the ten seconds a dial to an unroutable address hangs.
//
// It dials through DialPeer rather than the transport directly, and that is
// deliberate: DialPeer verifies the peer's certificate against its pinned
// fingerprint and, on success, already records where the peer was found. So the
// probe doubles as the periodic address observation -- a Hub left running for a
// week keeps `last_address_confirmed` honest without a single transfer, which is
// what turns that field from "when did we last happen to send something" into
// "when did we last prove this address works".
func (h *Hub) probePeerOnce(ctx context.Context, fingerprint string) (answered bool, health PeerHealth) {
	h.mu.RLock()
	store := h.store
	h.mu.RUnlock()

	now := time.Now()
	if store == nil {
		return false, PeerHealth{}
	}

	probeCtx, cancel := context.WithTimeout(ctx, bridge.PingRemoteTimeout)
	defer cancel()

	// Dial cancellably, so a Hub shutting down does not wait out the handshake.
	//
	// The cancellable path costs nothing when the peer answers and is the
	// difference between quitting promptly and quitting after
	// transport.defaultHandshakeTimeout when it does not.
	conn, err := h.DialPeerContext(ctx, fingerprint)
	if err != nil {
		return false, h.healthFor(fingerprint).failed(now)
	}
	defer conn.Close()

	if err := bridge.PingRemote(probeCtx, conn); err != nil {
		return false, h.healthFor(fingerprint).failed(now)
	}
	return true, h.healthFor(fingerprint).succeeded(now)
}

// PeerHealthOf returns what this Hub currently believes about a peer, by
// fingerprint.
func (h *Hub) PeerHealthOf(fingerprint string) PeerHealth {
	return h.healthFor(fingerprint)
}

func (h *Hub) healthFor(fingerprint string) PeerHealth {
	key := strings.ToLower(strings.TrimSpace(fingerprint))
	h.peerHealthMu.RLock()
	defer h.peerHealthMu.RUnlock()
	return h.peerHealth[key]
}

func (h *Hub) setPeerHealth(fingerprint string, health PeerHealth) {
	key := strings.ToLower(strings.TrimSpace(fingerprint))
	h.peerHealthMu.Lock()
	defer h.peerHealthMu.Unlock()
	if h.peerHealth == nil {
		h.peerHealth = make(map[string]PeerHealth)
	}
	if health.LastChecked.IsZero() {
		// A cleared record, which is how a removed peer stops being reported.
		delete(h.peerHealth, key)
		return
	}
	h.peerHealth[key] = health
}

// AllPeerHealth snapshots every peer's reachability, keyed by fingerprint. It is
// what the dashboard reads.
func (h *Hub) AllPeerHealth() map[string]PeerHealth {
	h.peerHealthMu.RLock()
	defer h.peerHealthMu.RUnlock()
	out := make(map[string]PeerHealth, len(h.peerHealth))
	for k, v := range h.peerHealth {
		out[k] = v
	}
	return out
}
