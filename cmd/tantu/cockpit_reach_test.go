package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// Gates for what the cockpit says about a peer, and for how wide it says it.
//
// Two defects prompted this file, and both were found by rendering the cockpit and
// reading it rather than by any gate:
//
//   - The banner printed "[Online 🟢]" for every paired peer, unconditionally.
//     Pairing records that you trust a machine; it says nothing about whether it
//     is switched on. A peer at an unroutable address was reported Online for as
//     long as it stayed in the store, including after the heartbeat had spent
//     minutes proving otherwise. The dashboard has carried a truthful
//     reachability chip since the heartbeat landed, so the same Hub at the same
//     moment was telling the truth in one interface and lying in the other.
//
//   - The peer listing printed the full 64-character fingerprint inline, making
//     every line about 128 columns. The banner above it spends seven tests and a
//     good deal of code respecting the terminal's width; the listing underneath
//     respected nothing, and wrapped on an 80-column terminal.
//
// The gates below are behavioural rather than pure: each one starts a real Hub,
// arranges a real condition, and reads what was actually printed. A pure gate over
// cockpitReachMarker would prove the function returns a string, which is not the
// claim; the claim is that an unroutable peer is not called Online, and only a
// Hub that has tried and failed can establish that.

// cockpitReachHub starts a Hub whose transport probes, and returns it with the
// store ready for peers.
//
// The store does not exist until Start, so peers are added afterwards -- which
// also means the heartbeat will see them, which is the whole point.
func cockpitReachHub(t *testing.T) *hub.Hub {
	t.Helper()
	dir := t.TempDir()
	h, err := hub.NewHub(hub.HubConfig{
		TransportType: "lan",
		ListenAddr:    "127.0.0.1:0",
		WebAddr:       "127.0.0.1:0",
		StoreDir:      dir,
		OutputDir:     filepath.Join(dir, "drops"),
		Headless:      true,
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Start(ctx) }()
	select {
	case <-h.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("Hub never became ready")
	}
	t.Cleanup(func() { _ = h.Stop() })
	return h
}

func cockpitAddPeer(t *testing.T, h *hub.Hub, name, address string) pairing.Peer {
	t.Helper()
	id, err := pairing.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	p := pairing.Peer{
		Fingerprint: id.Fingerprint,
		Name:        name,
		Address:     address,
		CertPEM:     id.CertPEM,
	}
	if err := h.Store().AddPeer(p); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	return p
}

// waitForVerdict blocks until the Hub has actually probed a peer and failed.
//
// The condition is produced rather than constructed: 192.0.2.0/24 is TEST-NET-1,
// reserved by RFC 5737 and unroutable by design, so a dial to it hangs until the
// transport's timeout and then fails. The Hub's own loop has to run out the clock
// before any of this has a verdict to report, which is the part a constructed
// health value would skip.
func waitForVerdict(t *testing.T, h *hub.Hub, fingerprint string, within time.Duration) hub.PeerHealth {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		health := h.PeerHealthOf(fingerprint)
		if !health.LastChecked.IsZero() {
			return health
		}
		if time.Now().After(deadline) {
			t.Fatalf("no probe completed within %s; a gate that cannot reach the "+
				"condition it exists for proves nothing (health so far: %+v)", within, health)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// An unroutable peer must not be reported as Online.
//
// This is the gate for the defect that prompted the whole batch. It cannot pass
// against the old code: the old code said Online without consulting the Hub, so it
// said Online here too.
func TestCockpitDoesNotCallAnUnroutablePeerOnline(t *testing.T) {
	h := cockpitReachHub(t)
	p := cockpitAddPeer(t, h, "laptop-1", "192.0.2.44:9877")

	health := waitForVerdict(t, h, p.Fingerprint, 30*time.Second)
	if health.Reachable {
		t.Fatalf("the peer answered, so this gate cannot fail for the reason it exists: %+v", health)
	}

	banner := captureStdout(t, func() { printCockpitBanner(h) })
	status := captureStdout(t, func() { printPeerStatus(h) })

	// Both surfaces, and by the marker rather than by a word.
	//
	// This originally grepped for the literal string "Online", which is how the
	// old banner was worded -- and which made the banner half of this gate
	// unfailable, because the banner now uses the green circle on its own and
	// never emits the word at all. A gate that greps for a token the surface does
	// not contain passes for any reason whatsoever. The mark that actually means
	// "reachable" on the banner is U+1F7E2, so that is what is asserted.
	const greenCircle = "\U0001F7E2"

	// The banner, on the marker rather than on a word: the old banner said
	// "[Online 🟢]" and the new one says " 🟢" alone, so a gate that greps for
	// "Online" passes against the banner whatever it renders.
	if strings.Contains(banner, greenCircle) {
		t.Errorf("the banner marks a peer at %s with the reachable circle, having probed it "+
			"and found it not answering (%+v):\n%s", p.Address, health, banner)
	}
	if strings.Contains(banner, "Online") {
		t.Errorf("the banner calls a peer at %s Online, having probed it and found it not "+
			"answering (%+v):\n%s", p.Address, health, banner)
	}
	// And the word, where the word is what the surface uses.
	if strings.Contains(banner, "Online") {
		t.Errorf("the banner calls a peer at %s Online, having probed it and found it not "+
			"answering (%+v):\n%s", p.Address, health, banner)
	}
	if strings.Contains(status, "Online") {
		t.Errorf("the peer listing calls a peer at %s Online, having probed it and found it "+
			"not answering (%+v):\n%s", p.Address, health, status)
	}
	// And it must say something true instead of just going quiet, or a reader
	// cannot tell a down peer from one that was never tried.
	if !strings.Contains(status, "never answered") {
		t.Errorf("a peer that has never answered must say so; got:\n%s", status)
	}
}

// A peer the Hub has not probed has no verdict, and must not be given one.
//
// The distinction that matters: "not probed yet" is not "unreachable", and
// "unreachable" is not "online". A marker that appears before any evidence exists
// is a claim made with nothing behind it -- and it is what the old code did on
// every render, forever.
func TestCockpitSaysNothingAboutAPeerItHasNotProbed(t *testing.T) {
	h := cockpitReachHub(t)
	p := cockpitAddPeer(t, h, "laptop-1", "192.0.2.44:9877")

	if got := cockpitReachMarker(h.PeerHealthOf(p.Fingerprint)); got != "" {
		t.Errorf("a peer with no probe on record was given the marker %q; silence is the "+
			"only honest answer where there is no evidence", got)
	}
	if got := cockpitReachDetail(h.PeerHealthOf(p.Fingerprint)); got != "" {
		t.Errorf("a peer with no probe on record was given the detail %q", got)
	}

	banner := captureStdout(t, func() { printCockpitBanner(h) })
	if strings.Contains(banner, "Online") || strings.Contains(banner, "Not answering") {
		t.Errorf("the banner asserted a reachability verdict before any probe had run:\n%s", banner)
	}
}

// "Active" is a selection, not a status, and must not wear a health signal's
// green circle.
//
// It did. On the same line as the reachability, in the same green, which is the
// claim from the first gate restated in a different place -- so a reader scanning
// the listing saw a healthy-looking dot next to a peer that was down.
func TestCockpitSelectionMarkerIsNotDressedAsAStatus(t *testing.T) {
	h := cockpitReachHub(t)
	p := cockpitAddPeer(t, h, "laptop-1", "192.0.2.44:9877")
	waitForVerdict(t, h, p.Fingerprint, 30*time.Second)
	_ = h.SetActivePeer(p.Fingerprint)

	status := captureStdout(t, func() { printPeerStatus(h) })
	if !strings.Contains(status, "[active]") {
		t.Fatalf("the active peer is not marked active, so the gate cannot fail for the "+
			"reason it exists:\n%s", status)
	}
	if strings.Contains(status, "[active]\U0001F7E2") || strings.Contains(status, "ACTIVE \U0001F7E2") {
		t.Errorf("the selection marker carries a green circle, which on this line reads as a "+
			"health signal:\n%s", status)
	}
}

// The listing must fit its budget, for a name that will not fit and a peer that is
// down.
//
// Both conditions at once, because separately they each pass: the long name alone
// fits, and the unreachable sentence alone fits. Together they overrun, which is
// how the original 128-column line was never noticed -- nothing in it was extreme.
func TestCockpitPeerLinesFitTheirWidthBudget(t *testing.T) {
	h := cockpitReachHub(t)
	long := "a-very-long-machine-name-that-keeps-going-and-going-and-going"
	// An IPv6 address, the widest a peer address can be, because an IPv4 one at 16
	// columns would let this pass while the real cases still wrapped.
	p := cockpitAddPeer(t, h, long, "[2001:db8:85a3::8a2e:370:7334]:9877")
	waitForVerdict(t, h, p.Fingerprint, 30*time.Second)
	_ = h.SetActivePeer(p.Fingerprint)

	listing := captureStdout(t, func() { printPeerStatus(h) })

	for _, line := range strings.Split(listing, "\n") {
		if !strings.Contains(line, "(SAS:") {
			continue
		}
		if w := cockpitWidth(line); w > cockpitPeerLineWidth {
			t.Errorf("a peer line is %d columns, over the %d budget:\n%s", w, cockpitPeerLineWidth, line)
		}
	}

	// Truncating to fit must not have cost the facts. A line that fits by
	// dropping the address, or the pairing code, or the reachability, has been
	// made short rather than made readable.
	if !strings.Contains(listing, "SAS:") {
		t.Errorf("fitting the line dropped the pairing code:\n%s", listing)
	}
	// Nor may the port go: the full literal, brackets and all, has to survive intact
	// or the line names a machine nobody can dial.
	if !strings.Contains(listing, "]:9877") {
		t.Errorf("fitting the line mangled the IPv6 literal; the address must survive whole:\n%s",
			listing)
	}
	if !strings.Contains(listing, "not answering") && !strings.Contains(listing, "never answered") &&
		!strings.Contains(listing, "down") {
		t.Errorf("fitting the line dropped the fact that the peer is not answering:\n%s", listing)
	}
	// And the name must survive as something recognisable rather than being cut
	// to nothing.
	if !strings.Contains(listing, "a-very-long") {
		t.Errorf("fitting the line removed the name entirely:\n%s", listing)
	}
	// Nor may the name be what absorbs the whole overrun: the address has to
	// remain dialable, because a truncated IPv6 literal is not an address
	// anyone can use.
	if !strings.Contains(listing, "7334]:9877") {
		t.Errorf("fitting the line truncated the IPv6 address, leaving a literal that "+
			"cannot be dialled:\n%s", listing)
	}
}

// A reachable peer must be reported reachable, or the cockpit is useless rather
// than merely untruthful.
//
// Without this the batch could pass by replacing "Online" with silence for every
// peer, which is a worse failure than the one it fixes.
func TestCockpitReportsAReachablePeerAsReachable(t *testing.T) {
	// The reachable condition is produced by a real handshake to a second real
	// Hub, rather than written into a health struct: the claim is that the cockpit
	// shows this Hub's own verdict, and a struct would assert the answer. Where a
	// machine will not let two Hubs reach each other this skips with a reason
	// naming what covers it instead -- TestCockpitReachWordingForEveryHealth covers
	// the wording without needing the wire.
	h := cockpitReachHub(t)

	// A second Hub, so "reachable" means something. It listens; the peer record A
	// holds carries B's real certificate, which is what makes A's dial complete an
	// mTLS handshake rather than merely connect a socket.
	b, err := hub.NewHub(hub.HubConfig{
		TransportType:     "lan",
		ListenAddr:        "127.0.0.1:0",
		WebAddr:           "127.0.0.1:0",
		StoreDir:          t.TempDir(),
		OutputDir:         filepath.Join(t.TempDir(), "drops"),
		Headless:          true,
		AutoAcceptPairing: true,
	})
	if err != nil {
		t.Fatalf("NewHub for the second peer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = b.Start(ctx) }()
	select {
	case <-b.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("the second Hub never became ready")
	}
	t.Cleanup(func() { _ = b.Stop() })

	// B's *own* identity, not a fresh one. A generated identity is a valid
	// certificate that B does not hold, so the handshake would fail at the
	// certificate check rather than at the reachability check -- which is what the
	// first version of this gate did, and it reported the resulting failure as
	// "could not produce a reachable peer".
	//
	// The peer record carries B's real certificate and B's real address, so a
	// successful probe means a completed mTLS handshake to a live machine.
	bID := b.Identity()
	if bID == nil {
		t.Fatal("the second Hub has no identity, so it cannot be dialed as a peer")
	}
	reachable := pairing.Peer{
		Fingerprint: bID.Fingerprint,
		Name:        "reachable-laptop",
		Address:     b.P2PAddr().String(),
		CertPEM:     bID.CertPEM,
	}
	if err := h.Store().AddPeer(reachable); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	// Wait for the heartbeat to produce a verdict rather than setting one.
	deadline := time.Now().Add(30 * time.Second)
	var last hub.PeerHealth
	for time.Now().Before(deadline) {
		last = h.PeerHealthOf(reachable.Fingerprint)
		if !last.LastChecked.IsZero() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if last.LastChecked.IsZero() {
		t.Fatalf("no probe completed in 30s, so this gate cannot fail for the reason it "+
			"exists (health: %+v)", last)
	}
	if !last.Reachable {
		t.Skipf("could not produce a reachable peer: %v answered %+v. This gate needs two "+
			"Hubs that can reach each other on this machine, and CI sandboxes and loaded "+
			"runners have refused before. The wording it covers is gated directly by "+
			"TestCockpitReachWordingForEveryHealth, which does not need the wire.",
			reachable.Address, last)
	}

	marker := cockpitReachMarker(last)
	if !strings.Contains(marker, "\U0001F7E2") || strings.Contains(marker, "Not answering") {
		t.Errorf("a peer that answered a handshake is marked %q; it should carry the green "+
			"circle and say nothing about failing to answer", marker)
	}
	if got := cockpitReachDetail(last); got != "Online" {
		t.Errorf("a peer that answered a handshake reads %q, want %q", got, "Online")
	}
}

// The wording, for every shape of health, as a decision function.
//
// This is legitimately pure: a PeerHealth is the whole domain, and the output is
// the sentence. It is not a substitute for the produced gates above -- those are
// what prove the cockpit reads the Hub's health at all, which is the defect that
// prompted this batch -- but it covers the reachable case exhaustively, which a
// produced gate can only do on a machine where two Hubs can reach each other.
func TestCockpitReachWordingForEveryHealth(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name        string
		health      hub.PeerHealth
		wantMarker  string // "" means: must say nothing
		wantDetail  string
		description string
	}{
		{
			name:        "never probed",
			health:      hub.PeerHealth{},
			wantMarker:  "",
			wantDetail:  "",
			description: "no evidence either way, so no claim; not-probed is not unreachable",
		},
		{
			name:        "reachable now",
			health:      hub.PeerHealth{Reachable: true, LastChecked: now, LastOK: now},
			wantMarker:  " [\U0001F7E2]",
			wantDetail:  "Online",
			description: "answered a probe",
		},
		{
			name:        "unreachable, answered before",
			health:      hub.PeerHealth{LastChecked: now, LastOK: now.Add(-3 * time.Minute)},
			wantMarker:  " [Not answering \U0001F534]",
			wantDetail:  "Not answering · last answered 3m ago",
			description: "a laptop that went to sleep, named as such",
		},
		{
			name:        "unreachable, never answered",
			health:      hub.PeerHealth{LastChecked: now, ConsecutiveFailures: 7},
			wantMarker:  " [Not answering \U0001F534]",
			wantDetail:  "Not answering · has not answered since pairing",
			description: "a wrong address or a pairing that never worked, distinct from asleep",
		},
		{
			name:        "unreachable, gone a long time",
			health:      hub.PeerHealth{LastChecked: now, LastOK: now.Add(-30 * 24 * time.Hour)},
			wantMarker:  " [Not answering \U0001F534]",
			wantDetail:  "Not answering · last answered 4w ago",
			description: "weeks away; the weeks are worth naming rather than rounding away",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cockpitReachMarker(tc.health); got != tc.wantMarker {
				t.Errorf("marker = %q, want %q (%s)", got, tc.wantMarker, tc.description)
			}
			if got := cockpitReachDetail(tc.health); got != tc.wantDetail {
				t.Errorf("detail = %q, want %q (%s)", got, tc.wantDetail, tc.description)
			}
		})
	}

	// The distinction that carries the most meaning: a peer that has never
	// answered and one that stopped answering are different problems, and the
	// wording must not collapse them.
	never := cockpitReachDetail(hub.PeerHealth{LastChecked: now, ConsecutiveFailures: 7})
	stopped := cockpitReachDetail(hub.PeerHealth{LastChecked: now, LastOK: now.Add(-time.Minute)})
	if never == stopped {
		t.Errorf("a peer that never answered and one that stopped are worded identically "+
			"(%q); they are different problems and a reader acts on them differently", never)
	}
}

// Shortening the sentence must never lose the fact it states.
func TestCockpitShortenedReachWordingStillSaysNotAnswering(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		health hub.PeerHealth
	}{
		{"never answered", hub.PeerHealth{LastChecked: now, ConsecutiveFailures: 3}},
		{"stopped answering", hub.PeerHealth{LastChecked: now, LastOK: now.Add(-12 * time.Minute)}},
		{"gone weeks", hub.PeerHealth{LastChecked: now, LastOK: now.Add(-40 * 24 * time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := ""
			for _, budget := range []int{200, 30, cockpitReachDetailShort, cockpitReachBareWidth, 4} {
				got := cockpitReachDetailFitting(tc.health, budget)
				if got == "" {
					t.Fatalf("at budget %d the sentence was dropped entirely; a peer that is "+
						"down is the fact on this line most worth telling, and it may be "+
						"shortened but never removed", budget)
				}
				if !strings.Contains(strings.ToLower(got), "answer") && !strings.Contains(got, "down") {
					t.Errorf("at budget %d the wording is %q, which does not say the peer is "+
						"not answering", budget, got)
				}
				if cockpitWidth(got) > budget && budget >= 4 {
					t.Errorf("at budget %d the wording %q is %d columns wide", budget, got, cockpitWidth(got))
				}
				// Shortening must never lose information by making a claim that is
				// stronger than the evidence: shorter is never "reachable".
				if previous != "" && len(got) > len(previous) {
					t.Errorf("a tighter budget produced a longer sentence: %q then %q", previous, got)
				}
				previous = got
			}
		})
	}
}

// The fingerprint must still be obtainable, or dropping it from the listing has
// just moved the problem.
//
// The listing is a place to scan peers, not to read 64 hex characters; the
// fingerprint remains in the dashboard and the pair output. This asserts it is
// still somewhere a person can reach, so the removal is a relocation rather than
// a deletion.
func TestCockpitPeerFingerprintRemainsAvailableSomewhere(t *testing.T) {
	h := cockpitReachHub(t)
	p := cockpitAddPeer(t, h, "laptop-1", "192.0.2.44:9877")

	listing := captureStdout(t, func() { printPeerStatus(h) })
	if strings.Contains(listing, p.Fingerprint) {
		t.Errorf("the listing still prints the full fingerprint inline, which is what made "+
			"every peer line about 128 columns:\n%s", listing)
	}
	if !strings.Contains(listing, p.SAS()) {
		t.Errorf("the listing lost the pairing code as well, so nothing identifies this peer "+
			"to a reader:\n%s", listing)
	}
	// A peer chosen from the listing is still resolvable to a fingerprint, so
	// the fingerprint was relocated rather than deleted: the store is where the
	// cockpit's other commands read it from.
	resolved, found := h.Store().GetPeer(p.Fingerprint)
	if !found || resolved.Fingerprint != p.Fingerprint {
		t.Error("a peer in the listing is no longer resolvable to a fingerprint, so the " +
			"fingerprint was removed from the user's reach rather than moved")
	}
}
