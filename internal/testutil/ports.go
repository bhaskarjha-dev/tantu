package testutil

// Helpers for tests that need a port, and need the ports *around* it to be
// free.
//
// Why not the operating system's ephemeral ports:
//
// The port the OS hands out for net.Listen(":0") comes from the ephemeral
// range -- the range the OS allocates outbound connections from. On macOS it
// allocates them sequentially, so a burst of connections occupies consecutive
// ports. A product that falls back to a LOWER port when the configured one is
// taken then walks straight into ports another process is holding.
//
// That is not hypothetical. On 2026-10-20 the macOS runner failed
// TestPresentStartedHubAnnouncesThePortActuallyInUse with
//
//     start p2p listener on 127.0.0.1:49936: bind: address already in use;
//     3 lower port(s) were also unavailable
//
// The test had taken 49936 from the ephemeral range; 49935, 49934 and 49933
// were outbound connections belonging to other tests, running in other
// packages, on the same host at the same time. The Hub tried them, found them
// taken, and refused to start -- correctly, and loudly, because a Hub
// listening somewhere nobody can reach is worse than a Hub that says so.
//
// So the run is found in a low range that no platform hands out ephemeral
// ports from, and every port in the run is verified by binding it rather than
// assumed.

import (
	"fmt"
	"net"
)

const (
	// portRunBase is the bottom of the range PortRun searches.
	//
	// Below the default ephemeral range on every platform this project runs
	// on: Linux 32768-60999, macOS 49152-65535, Windows 49152-65535.
	portRunBase = 20000
	// portRunSpan is how far up the range PortRun looks.
	portRunSpan = 2000
)

// PortRun finds n consecutive free loopback ports and returns a listener on
// each of them, highest first.
//
// Every port in the run is verified by binding it. Assuming a port is free is
// how a test passes locally and fails in CI.
//
// The whole run is held while it is chosen, so two packages running at the
// same time choose different runs: the second one finds the first one's ports
// taken and moves on. The caller releases the ports it does not want to hold.
func PortRun(n int) ([]net.Listener, error) {
	if n < 1 {
		return nil, fmt.Errorf("a port run needs at least one port, got %d", n)
	}
	for base := portRunBase; base < portRunBase+portRunSpan; base++ {
		run := make([]net.Listener, 0, n)
		ok := true
		for i := 0; i < n; i++ {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", base-i))
			if err != nil {
				ok = false
				break
			}
			run = append(run, ln)
		}
		if !ok {
			CloseListeners(run...)
			continue
		}
		return run, nil
	}
	return nil, fmt.Errorf("no run of %d consecutive free loopback ports in [%d,%d)",
		n, portRunBase, portRunBase+portRunSpan)
}

// PortOf returns the port a listener is bound to, or 0 if it cannot be read.
func PortOf(ln net.Listener) int {
	if ln == nil {
		return 0
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}
	return addr.Port
}

// CloseListeners closes every listener given, ignoring the error from closing
// one that is already closed.
func CloseListeners(lns ...net.Listener) {
	for _, ln := range lns {
		if ln != nil {
			_ = ln.Close()
		}
	}
}
