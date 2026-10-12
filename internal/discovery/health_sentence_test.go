package discovery

// Gate for the health sentence's agreement with the dashboard's status chip.
//
// The Peers tab renders a chip and a sentence from the same Health value. The
// chip distinguishes "nothing has ever answered" from "a hub answered and has
// since aged out": nodes == 0 with beacons_received > 0 renders as
// "Discovery idle". All three transport branches of describeHealth claimed
// "No nearby hubs have answered yet", so on the same card the chip said a hub
// had been heard and quiet while the sentence beneath said nothing had ever
// answered. The word "yet" is what made it wrong.
//
// Found by reading the rendered Peers tab against /api/status -- running, usable,
// 334 beacons sent, 1 received, 0 nodes -- not by any gate, because the two
// halves are computed in two languages and nothing compared them.

import (
	"strings"
	"testing"
)

func TestHealthSentenceAgreesWithTheStatusChip(t *testing.T) {
	// The chip's rule, transcribed: nodes == 0 && beacons_received > 0 renders
	// "Discovery idle", meaning a hub was heard and has aged out.
	cases := []struct {
		name        string
		h           Health
		wantAgedOut bool
	}{
		{
			name:        "heard once, now aged out",
			h:           Health{Running: true, Broadcast: true, Multicast: true, Usable: true, Nodes: 0, BeaconsReceived: 1, BeaconsSent: 334},
			wantAgedOut: true,
		},
		{
			name:        "heard many, none in reach",
			h:           Health{Running: true, Broadcast: true, Multicast: true, Usable: true, Nodes: 0, BeaconsReceived: 7, BeaconsSent: 900},
			wantAgedOut: true,
		},
		{
			name:        "never heard anything",
			h:           Health{Running: true, Broadcast: true, Multicast: true, Usable: true, Nodes: 0, BeaconsReceived: 0, BeaconsSent: 120},
			wantAgedOut: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := describeHealth(tc.h, false, false)

			// The chip would render "Discovery idle" for this value.
			chipSaysAgedOut := tc.h.Nodes == 0 && tc.h.BeaconsReceived > 0
			if chipSaysAgedOut != tc.wantAgedOut {
				t.Fatalf("test premise is wrong: the chip renders idle=%v for this value", chipSaysAgedOut)
			}

			if tc.wantAgedOut {
				if strings.Contains(got, "No nearby hubs have answered yet") {
					t.Errorf("the chip says a hub was heard and has aged out, but the sentence denies any "+
						"hub has answered: %q", got)
				}
				if !strings.Contains(got, "beacon") {
					t.Errorf("the aged-out sentence should say what was actually observed: %q", got)
				}
				return
			}
			if !strings.Contains(got, "No nearby hubs have answered yet") {
				t.Errorf("with nothing ever heard, the sentence should say so plainly: %q", got)
			}
		})
	}
}

// TestAgedOutSentenceNamesTheCount keeps the new wording useful rather than
// merely non-contradictory: "something was heard" without saying how much is a
// worse sentence than the one it replaces for anyone debugging a network.
func TestAgedOutSentenceNamesTheCount(t *testing.T) {
	h := Health{Running: true, Broadcast: true, Multicast: true, Usable: true, Nodes: 0, BeaconsReceived: 3, BeaconsSent: 500}
	got := describeHealth(h, false, false)
	if !strings.Contains(got, "3") {
		t.Errorf("the aged-out sentence does not report how many beacons were heard: %q", got)
	}
}
