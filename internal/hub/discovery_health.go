package hub

// Discovery health: turning "discovery is broken" from an invisible condition
// into something the product says out loud.
//
// The gap this closes was recorded, not invented. Every failure signal in the
// discovery engine used to be discarded:
//
//   - broadcastBeacon ignored the result of every WriteToUDP, so a network that
//     silently dropped beacons looked identical to a quiet LAN;
//   - the mDNS listener was a best-effort fallback whose bind error was never
//     stored, so a blocked multicast socket cost half the discovery engines
//     with no trace;
//   - listenLoop gave up after 50 consecutive read errors and simply returned,
//     leaving a dead socket indistinguishable from a live one.
//
// The product's response to all three was the same: an empty "Nearby" list.
// That is exactly what a correct installation with no second machine also
// shows, so a user whose firewall ate discovery had no way to tell the two
// apart and no next action to take.

import (
	"context"
	"fmt"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/discovery"
)

// discoveryHealthState is the previous sample, kept so the watcher reports
// transitions rather than repeating itself.
type discoveryHealthState struct {
	degraded bool
	observed bool
}

// discoveryHealthSampleInterval is how often the watcher samples. Ten seconds
// is frequent enough that a user who breaks their network sees the consequence
// while they are still looking at the screen, and rare enough that a healthy
// Hub does no measurable work.
const discoveryHealthSampleInterval = 10 * time.Second

// discoveryHealthSource is the single method the watcher needs from the
// discovery engine.
//
// Narrowing the dependency to one method is what makes the loop testable, and
// that is the whole point: with a concrete *discovery.Engine the only states a
// test can reach through a real socket are the healthy ones, so every gate on
// this file passed while the watcher was never started by anything. That is the
// defect staticcheck found, and it was invisible to the whole test suite.
type discoveryHealthSource interface {
	Health() discovery.Health
}

// watchDiscoveryHealth samples discovery and reports state transitions for the
// life of the Hub.
//
// Transition-driven rather than sample-driven: a permanently degraded engine
// would otherwise write the same warning every sample, and a log the user
// learns to scroll past is worth less than no log at all.
//
// This is started from the one place the Hub creates a discovery engine, and
// shares that engine's context. Deleting the call without deleting this
// function is a compile-time no-op but a staticcheck failure, which is
// deliberate: this function existing is not evidence that anything runs it.
func (h *Hub) watchDiscoveryHealth(ctx context.Context, src discoveryHealthSource) {
	h.watchDiscoveryHealthEvery(ctx, src, discoveryHealthSampleInterval)
}

// watchDiscoveryHealthEvery is the loop, with the sample interval as a
// parameter so a test can drive transitions in milliseconds instead of waiting
// out the production clock. A non-positive interval falls back to the
// production value rather than becoming a busy loop.
func (h *Hub) watchDiscoveryHealthEvery(ctx context.Context, src discoveryHealthSource, interval time.Duration) {
	if interval <= 0 {
		interval = discoveryHealthSampleInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	state := discoveryHealthState{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		event, next := discoveryHealthEvent(state, src.Health())
		state = next
		if event != nil {
			h.logger.Log(event.Level, DomainNet, event.Message, nil)
		}
	}
}

// discoveryLogEvent is one decision about what to log. It is a value rather
// than a direct log call so the rules are testable without a running Hub and a
// ten-second clock.
type discoveryLogEvent struct {
	Level   EventLevel
	Message string
}

// discoveryHealthEvent decides what a transition from prev to cur should say.
//
// Three rules, each answering a question a user actually has:
//
//   - discovery became unusable: say so and name the fallback that still works;
//   - discovery was unusable and now is not: say so, because a user who gave
//     up on pairing by address should know to try discovery again;
//   - discovery works but its own beacons are failing: this is the case that
//     was previously invisible and most confusing, because the engine looks
//     healthy while no peer can see this hub at all.
func discoveryHealthEvent(prev discoveryHealthState, cur discovery.Health) (*discoveryLogEvent, discoveryHealthState) {
	next := discoveryHealthState{degraded: cur.Degraded, observed: true}
	if !cur.Running {
		// A stopped engine is not a fault. Several documented configurations
		// (SSH-only transport, a loopback test rig) run the Hub with no
		// discovery at all, and warning about that would be noise.
		return nil, next
	}
	switch {
	case cur.Degraded && !prev.degraded:
		// The transition into the fault, whether or not a sample has been seen
		// yet. The previous guard here was !prev.observed, which meant a Hub
		// only ever reported discovery that was already broken before its first
		// sample: discovery failing *while the Hub runs* - a firewall change, a
		// laptop changing network, a VPN eating broadcast - produced no line at
		// all, which is the condition this whole file exists to make visible.
		// prev.degraded is false on the first sample, so initial breakage is
		// still reported, and a fault that persists stays silent.
		return &discoveryLogEvent{Level: LevelWarn, Message: "Discovery degraded: " + cur.Detail +
			" Pair by address with `tantu pair --peer <ip>:9877` if a hub is known to be running."}, next
	case !cur.Degraded && prev.degraded:
		return &discoveryLogEvent{Level: LevelInfo, Message: "Discovery recovered: " + cur.Detail}, next
	case !prev.observed && cur.SendErrors > 0:
		return &discoveryLogEvent{Level: LevelWarn, Message: "Discovery beacons are not reaching this network: " +
			describeDiscoverySendFailure(cur) + " Pair by address instead."}, next
	default:
		return nil, next
	}
}

// describeDiscoverySendFailure explains a beacon send that keeps failing
// without inventing a cause. UDP gives no delivery guarantee, so the only
// honest statement available is that writes from this machine are erroring.
func describeDiscoverySendFailure(health discovery.Health) string {
	msg := fmt.Sprintf("%d beacon send(s) failed on this machine", health.SendErrors)
	if health.LastError != "" {
		msg += " (" + health.LastError + ")"
	}
	return msg + "."
}
