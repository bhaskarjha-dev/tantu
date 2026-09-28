package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// Peer resolution decides which machine receives a file. A bug here sends data
// to the wrong host, so the rules are asserted explicitly rather than assumed.

// newStoreWithPeers builds a peer store seeded with n distinguishable peers.
func newStoreWithPeers(t *testing.T, n int) *pairing.PeerStore {
	t.Helper()
	store, err := pairing.NewPeerStore(t.TempDir())
	if err != nil {
		t.Fatalf("new peer store: %v", err)
	}
	for i := 0; i < n; i++ {
		fp := strings.Repeat(fmt.Sprintf("%02x", i+1), 32)[:64]
		if err := store.AddPeer(pairing.Peer{
			Fingerprint: fp,
			Name:        fmt.Sprintf("peer-%d", i),
			Address:     fmt.Sprintf("10.0.0.%d:9877", i+1),
		}); err != nil {
			t.Fatalf("seed peer %d: %v", i, err)
		}
	}
	return store
}

func TestEnsurePortAndEnsurePeerLANPortPreserveCustomPorts(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"192.168.1.50", "192.168.1.50:" + "9877"},
		{"192.168.1.50:9878", "192.168.1.50:9878"},
		{"devbox.local:2222", "devbox.local:2222"},
		{"[::1]", "[::1]:9877"},
		{"[::1]:9999", "[::1]:9999"},
	}
	for _, tc := range cases {
		if got := ensurePort(tc.in); got != tc.want {
			t.Errorf("ensurePort(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := ensurePeerLANPort(tc.in); got != tc.want {
			t.Errorf("ensurePeerLANPort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolvePeerWithNoPeersIsAnError(t *testing.T) {
	store, err := pairing.NewPeerStore(t.TempDir())
	if err != nil {
		t.Fatalf("new peer store: %v", err)
	}
	_, err = resolvePeer(store, "")
	if err == nil {
		t.Fatal("an empty peer store must not resolve to a destination")
	}
	if !strings.Contains(err.Error(), "No paired peers") {
		t.Errorf("error should name the missing prerequisite, got %q", err)
	}
}

func TestResolvePeerWithMultiplePeersNeverPicksSilently(t *testing.T) {
	store := newStoreWithPeers(t, 3)
	_, err := resolvePeer(store, "")
	if err == nil {
		t.Fatal("with multiple peers and no default, resolution must be an error, never a silent pick")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Multiple peers") {
		t.Errorf("error should explain the ambiguity, got %q", msg)
	}
	// Every candidate must be named so the user can choose.
	for i := 0; i < 3; i++ {
		if !strings.Contains(msg, "peer-") {
			t.Errorf("error should list candidate peers, got %q", msg)
			break
		}
	}
}

func TestResolvePeerWithSinglePeerResolvesToIt(t *testing.T) {
	store := newStoreWithPeers(t, 1)
	got, err := resolvePeer(store, "")
	if err != nil {
		t.Fatalf("a single peer must resolve unambiguously: %v", err)
	}
	if !strings.HasSuffix(got, ":9877") {
		t.Errorf("resolved address %q should carry the default wire port", got)
	}
}

func TestResolvePeerExplicitIPBypassesTheStore(t *testing.T) {
	store := newStoreWithPeers(t, 2)
	// An explicit IP is an operator decision and must be honoured verbatim,
	// even though it matches no stored peer.
	got, err := resolvePeer(store, "10.1.2.3")
	if err != nil {
		t.Fatalf("an explicit IP must resolve: %v", err)
	}
	if got != "10.1.2.3:9877" {
		t.Errorf("resolved %q, want 10.1.2.3:9877", got)
	}
	got, err = resolvePeer(store, "10.1.2.3:1234")
	if err != nil {
		t.Fatalf("an explicit host:port must resolve: %v", err)
	}
	if got != "10.1.2.3:1234" {
		t.Errorf("a custom port must be preserved, got %q", got)
	}
}

func TestResolvePeerUnknownNameIsAnError(t *testing.T) {
	store := newStoreWithPeers(t, 1)
	if _, err := resolvePeer(store, "does-not-exist"); err == nil {
		t.Fatal("an unresolvable peer name must be an error, never a silent fallback to the first peer")
	}
}

func TestDisplayDestinationNeverSubstitutesADifferentPeer(t *testing.T) {
	store := newStoreWithPeers(t, 2)

	// A resolvable name reports the peer's display name.
	if got := displayDestination("lan", store, "peer-0", "10.0.0.1:9877"); !strings.Contains(got, "peer-0") {
		t.Errorf("resolvable target should report the peer, got %q", got)
	}

	// An unresolvable target echoes the raw dial address rather than quietly
	// resolving to a stored peer the user did not ask for.
	got := displayDestination("lan", store, "typo", "10.0.0.9:9877")
	if got != "10.0.0.9:9877" {
		t.Errorf("unresolvable target must echo the dial address, got %q", got)
	}
	if strings.Contains(got, "peer-") {
		t.Errorf("must never substitute a different peer, got %q", got)
	}

	// A non-LAN transport has no peer store; report the dial target.
	if got := displayDestination("ssh", store, "peer-0", "box:22"); got != "box:22" {
		t.Errorf("non-lan destination should echo the dial target, got %q", got)
	}

	// An empty dial target has a named, non-deceptive placeholder.
	if got := displayDestination("lan", store, "typo", ""); got != "default peer" {
		t.Errorf("empty dial target should name the fallback, got %q", got)
	}
}

func TestDefaultTransportIsLAN(t *testing.T) {
	if got := defaultTransport(nil); got != "lan" {
		t.Errorf("defaultTransport = %q, want lan", got)
	}
}

func TestNoteHubVersionSkewHandlesMissingMatchingAndSkewedVersions(t *testing.T) {
	// None of these may panic, and each is a pure stderr warning.
	noteHubVersionSkew(nil)
	noteHubVersionSkew(&hub.HubStatus{})
	noteHubVersionSkew(&hub.HubStatus{Version: hub.HubVersion})
	noteHubVersionSkew(&hub.HubStatus{Version: "0.0.1-unrelated"})
}

func TestFormatRelativeTime(t *testing.T) {
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, "unknown"},
		{time.Now().Add(5 * time.Second), "just now"},
		{time.Now().Add(-90 * time.Second), "1m ago"},
		{time.Now().Add(-3 * time.Hour), "3h ago"},
		{time.Now().Add(-50 * time.Hour), "2d ago"},
		{time.Now().Add(-30 * 24 * time.Hour), "4w ago"},
	}
	for _, tc := range cases {
		if got := formatRelativeTime(tc.at); got != tc.want {
			t.Errorf("formatRelativeTime(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
}

func TestGenerateSessionIDIsUniqueAndHex(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := generateSessionID()
		if len(id) != 8 {
			t.Fatalf("session id %q should be 8 hex characters", id)
		}
		for _, r := range id {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				t.Fatalf("session id %q is not hex", id)
			}
		}
		if seen[id] {
			t.Fatalf("session id collision: %q", id)
		}
		seen[id] = true
	}
}
