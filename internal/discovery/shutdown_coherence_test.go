package discovery

// Gate for the coherence of a Health snapshot taken while the engine shuts down.
//
// The bug: the cancellation goroutine cleared `started` and *then* released
// startMu before closing the sockets, while Health() read `started` under that
// lock and loaded the bound flags after releasing it. A caller landing between
// the two saw:
//
//	Running=false, Broadcast=true, Multicast=true
//
// That is a state the engine is never actually in. It has stopped, yet it
// appears to still hold transports. Nothing in the product reacted to it --
// describeHealth checks Running first -- so it was invisible, which is exactly
// why it survived. It surfaced only as an intermittent failure of
// TestEngine_HealthAfterContextCancelIsNotDegraded under full-suite -race load.
//
// Why this gate exists rather than simply re-running that test until it fails:
// that test polls for `!Running` and then asserts nothing is bound. The window
// is short, so the poll usually lands after it closes and the test passes
// whether or not the ordering is correct. It reported 12 clean runs in
// isolation, then failed once in a full run. A gate whose failure rate depends
// on machine load is not a gate.
//
// This one produces the condition -- a real context cancellation, real socket
// closes -- and asserts the invariant on every sample taken across the
// transition, over many start/cancel cycles, so the window is covered by
// sampling rather than by luck.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// cycles is the number of independent transitions sampled per shutdown path.
// The tear window is a few microseconds wide, so one cycle proves little; each
// cycle adds another run of the sampler through the shutdown.
const shutdownCoherenceCycles = 25

// TestHealthSnapshotIsCoherentAcrossShutdown asserts that no Health() call ever
// reports a stopped engine as still holding a transport, on either path an
// engine can stop by.
//
// Both paths matter and both were wrong in the same way. A running Hub stops by
// cancelling the context, which the engine's own goroutine handles. A caller
// with an explicit handle stops it with Close(). Red proof initially reverted
// only the Close() ordering, and this gate stayed green -- the opposite of what
// red proof is for. It was asserting on the path the gate happened to drive,
// and nothing else. So the gate drives both.
func TestHealthSnapshotIsCoherentAcrossShutdown(t *testing.T) {
	t.Run("context cancellation", func(t *testing.T) {
		assertCoherentAcross(t, shutdownByContextCancel)
	})
	t.Run("Close", func(t *testing.T) {
		assertCoherentAcross(t, shutdownByClose)
	})
}

// shutdownByCancel and shutdownByClose are the two ways an engine can stop.
// Each returns the call that performs the shutdown, given the engine and the
// cancel func for the context it was started with.
func shutdownByContextCancel(engine *Engine, cancel context.CancelFunc) func() { return cancel }
func shutdownByClose(engine *Engine, _ context.CancelFunc) func() {
	return func() { _ = engine.Close() }
}

// assertCoherentAcross runs `cycles` start/shutdown transitions, sampling
// Health() throughout each, and fails on any torn snapshot.
//
// Ports increment per cycle so a previous cycle's sockets cannot make the next
// bind fail. A bind failure here would silently weaken the gate: with nothing
// bound there is no torn state to catch, and the run would pass having proved
// nothing.
func assertCoherentAcross(t *testing.T, shutdownFn func(*Engine, context.CancelFunc) func()) {
	t.Helper()
	for cycle := 0; cycle < shutdownCoherenceCycles; cycle++ {
		port := 21000 + cycle*2
		engine, err := NewEngine(DiscoveryConfig{
			NodeName:      "coherence",
			WirePort:      port,
			BroadcastPort: port,
		})
		if err != nil {
			t.Fatalf("cycle %d: NewEngine: %v", cycle, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		if err := engine.Start(ctx); err != nil {
			cancel()
			_ = engine.Close()
			t.Fatalf("cycle %d: Start: %v", cycle, err)
		}
		shutdown := shutdownFn(engine, cancel)

		// Sample across the shutdown, stopping at the first stopped engine --
		// that is the only window in which the tear is expressible.
		var (
			wg      sync.WaitGroup
			samples int64
			torn    int64
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				h := engine.Health()
				atomic.AddInt64(&samples, 1)
				if !h.Running && (h.Broadcast || h.Multicast) {
					atomic.AddInt64(&torn, 1)
				}
				if !h.Running {
					return
				}
			}
		}()

		shutdown()
		wg.Wait()
		cancel()
		_ = engine.Close()

		if n := atomic.LoadInt64(&samples); n == 0 {
			t.Fatalf("cycle %d: the sampler observed no Health() snapshot, so the gate proved nothing", cycle)
		}
		if atomic.LoadInt64(&torn) > 0 {
			t.Fatalf("cycle %d: Health() reported a stopped engine with bound sockets in %d of %d snapshots; "+
				"`started` and the bound flags must move under one hold of startMu, or a snapshot can catch them apart",
				cycle, atomic.LoadInt64(&torn), atomic.LoadInt64(&samples))
		}
	}
}
