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

// watchDiscoveryHealth samples discovery and reports state transitions.
//
// Transition-driven rather than sample-driven: a permanently degraded engine
// would otherwise write the same warning every sample, and a log the user
// learns to scroll past is worth less than no log at all.
func (h *Hub) watchDiscoveryHealth(ctx context.Context, engine *discovery.Engine) {
	const sampleInterval = 10 * time.Second
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()

	state := discoveryHealthState{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		event, next := discoveryHealthEvent(state, engine.Health())
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
	case cur.Degraded && !prev.observed:
		return &discoveryLogEvent{Level: LevelWarn, Message: "Discovery degraded: " + cur.Detail +
			" Pair by address with `tantu pair --peer <ip>:9877` if a hub is known to be running."}, next
	case cur.Degraded && prev.degraded:
		// Already reported. A second line every ten seconds is how a log
		// becomes something users stop reading.
		return nil, next
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
