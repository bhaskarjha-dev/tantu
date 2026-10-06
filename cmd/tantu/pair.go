package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/discovery"
	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

func runPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	peerAddr := fs.String("peer", "", "Connect to an existing pairing listener at HOST:PORT (responder mode)")
	port := fs.Int("port", 9877, "Port to listen on for incoming pairing (default: 9877)")
	storeDir := fs.String("store-dir", "", "Override config directory (default: ~/.config/tantu)")
	_ = fs.Parse(args)

	sDir := *storeDir
	if sDir == "" {
		var err error
		sDir, err = pairing.DefaultStoreDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot determine config directory: %v\n", err)
			os.Exit(1)
		}
	}

	store, err := pairing.NewPeerStore(sDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open peer store: %v\n", err)
		os.Exit(1)
	}

	promptConfirm := func(peerSAS, localSAS string) bool {
		fmt.Println("\n=== Pairing Code Verification ===")
		fmt.Printf("My Code:   %s\n", localSAS)
		fmt.Printf("Peer Code: %s\n", peerSAS)
		fmt.Print("\nDo the codes match on both machines? [y/N]: ")
		var resp string
		_, _ = fmt.Scanln(&resp)
		resp = strings.ToLower(strings.TrimSpace(resp))
		return resp == "y" || resp == "yes"
	}

	if *peerAddr == "" {
		// Actively discover nearby hubs on LAN
		type discPeer struct {
			Name     string `json:"name"`
			Address  string `json:"address"`
			Port     int    `json:"port"`
			IsPaired bool   `json:"is_paired"`
		}
		var discovered []discPeer

		// 1. Probe local hub
		if status, ok := hub.ProbeHubWithStoreDir("", sDir); ok && status.WebAddr != "" {
			client := &http.Client{Timeout: 500 * time.Millisecond}
			req, reqErr := http.NewRequest(http.MethodGet, "http://"+status.WebAddr+"/api/discovery/peers", nil)
			if reqErr == nil {
				if token := hub.RuntimeIPCTokenForAddr(sDir, status.WebAddr); token != "" {
					req.Header.Set(hub.IPCTokenHeader, token)
				}
				resp, err := client.Do(req)
				if err == nil {
					defer resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						var all []discPeer
						if err := json.NewDecoder(resp.Body).Decode(&all); err == nil {
							for _, d := range all {
								if !d.IsPaired {
									discovered = append(discovered, d)
								}
							}
						}
					}
				}
			}
		} else {
			// 2. Local hub not running: ephemeral 2-second scan
			fmt.Print("🔍 Scanning LAN for nearby tantu hubs (2s)... ")
			discCfg := discovery.DiscoveryConfig{
				NodeName: "scan-initiator",
				Interval: 500 * time.Millisecond,
			}
			if eng, err := discovery.NewEngine(discCfg); err == nil {
				scanCtx, scanCancel := context.WithTimeout(context.Background(), 2*time.Second)
				if startErr := eng.Start(scanCtx); startErr != nil {
					// Silently scanning nothing for two seconds and reporting
					// "done." left a broken scan indistinguishable from an
					// empty LAN; say so and let the user enter an IP instead.
					fmt.Fprintf(os.Stderr, "\n(discovery unavailable: %v — enter a peer address manually)\n", startErr)
				}
				<-scanCtx.Done()
				_ = eng.Close()
				scanCancel()
				for _, node := range eng.ListNodes() {
					// Discovery carries no fingerprint to match on; paired
					// hubs are recognized by address host, which is a
					// filter hint only (see HasPeerAtAddress).
					if store.HasPeerAtAddress(node.Address) {
						continue
					}
					discovered = append(discovered, discPeer{
						Name:     node.InstanceName,
						Address:  node.Address,
						Port:     node.Port,
						IsPaired: false,
					})
				}
			}
			fmt.Println("done.")
		}

		if len(discovered) > 0 {
			fmt.Println("\nDiscovered nearby hubs on LAN:")
			for idx, d := range discovered {
				// No SAS is printed: discovery broadcasts no identity, so any
				// code shown here would be fabricated. The verification code
				// exists only inside an active pairing handshake.
				fmt.Printf("  [%d] %s (%s, not yet paired)\n", idx+1, d.Name, d.Address)
			}
			fmt.Printf("Select peer [1-%d] or enter custom IP (or press Enter to view my pairing code): ", len(discovered))
			var selection string
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				selection = strings.TrimSpace(scanner.Text())
			}
			if selection != "" {
				// Strict numeric parse: Sscanf would accept trailing junk
				// such as "1abc" as a valid selection (and Atoi a "+1").
				if isPlainIndex(selection) {
					if choice, err := strconv.Atoi(selection); err == nil && choice >= 1 && choice <= len(discovered) {
						*peerAddr = discovered[choice-1].Address
					} else {
						*peerAddr = selection
					}
				} else {
					*peerAddr = selection
				}
			}
		}
	}

	if *peerAddr != "" {
		fmt.Printf("Connecting to peer at %s for pairing...\n", *peerAddr)
		res, err := pairing.DialInBandPairing(*peerAddr, store, promptConfirm)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Pairing failed: %v\n", err)
			os.Exit(1)
		}
		if res.Accepted {
			fmt.Printf("\n✅ Successfully paired! Peer fingerprint: %s (SAS: %s)\n", res.PeerFingerprint, res.PeerSAS)
		} else {
			fmt.Println("\n❌ Pairing rejected. No trust established.")
			os.Exit(1)
		}
	} else {
		// Check if local Hub is already running to avoid port collision
		if status, ok := hub.ProbeHubWithStoreDir("", sDir); ok {
			sas := status.Identity.SAS
			if sas == "" {
				if id, _ := store.LoadIdentity(); id != nil {
					sas = pairing.SASCode(id.Fingerprint)
				}
			}
			pairingPort := *port
			// The guard used to be `if pairingPort == 9877 { pairingPort = 9878`,
			// comparing against the literal default rather than the port this Hub
			// is actually on. Any Hub not on 9877 -- a configured p2p_port, or a
			// second Hub, which is exactly the case the loopback transport exists
			// for -- got no bump, so the responder announced a port the Hub already
			// owned. Measured rather than theorised: with the Hub on 19702,
			// `tantu pair -port 19702` printed "Waiting for peer connection on
			// 0.0.0.0:19702..." and then hung silently, while the initiator's
			// connection landed on the Hub's p2p listener and returned
			// "wsarecv: An existing connection was forcibly closed by the remote
			// host". Comparing against the real port fixes exactly that.
			//
			// Staying off the Hub's port entirely is a deeper question and a
			// separate decision: it changes the command the user is told to run on
			// the other machine. This only stops the collision being invisible.
			if hubPort, ok := hubP2PPort(status.P2PAddr); ok {
				if chosen, moved := choosePairingPort(pairingPort, hubPort); moved {
					fmt.Fprintf(os.Stderr,
						"Port %d is already used by the local Tantu Hub; using %d for the pairing listener instead.\n",
						hubPort, chosen)
					pairingPort = chosen
				}
			}
			listenAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(pairingPort))
			fmt.Printf("🚀 Local tantu Hub is active. Starting dedicated pairing listener on port %d...\n", pairingPort)
			if sas != "" {
				fmt.Printf("Pairing Code (SAS): %s\n", sas)
			}
			fmt.Println("On your other machine, run:")
			fmt.Printf("  tantu pair --peer=<THIS_MACHINE_IP>:%d\n\n", pairingPort)
			fmt.Printf("Waiting for peer connection on %s...\n", listenAddr)

			res, err := pairing.PairInitiator(store, listenAddr, promptConfirm)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Pairing failed: %v\n", err)
				os.Exit(1)
			}
			if res.Accepted {
				fmt.Printf("\n✅ Successfully paired! Peer fingerprint: %s (SAS: %s)\n", res.PeerFingerprint, res.PeerSAS)
			} else {
				fmt.Println("\n❌ Pairing rejected. No trust established.")
				os.Exit(1)
			}
			return
		}

		// Standalone initiator mode. The store owns the cross-process
		// load/create transaction so two first-run pairers cannot generate
		// different identities for the same store.
		id, err := store.LoadOrCreateIdentity()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading or creating identity: %v\n", err)
			os.Exit(1)
		}

		localSAS := pairing.SASCode(id.Fingerprint)
		listenAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(*port))
		fmt.Printf("Pairing code: %s\n", localSAS)
		fmt.Printf("Waiting for peer connection on %s...\n", listenAddr)

		res, err := pairing.PairInitiator(store, listenAddr, promptConfirm)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Pairing failed: %v\n", err)
			os.Exit(1)
		}
		if res.Accepted {
			fmt.Printf("\n✅ Successfully paired! Peer fingerprint: %s (SAS: %s)\n", res.PeerFingerprint, res.PeerSAS)
		} else {
			fmt.Println("\n❌ Pairing rejected. No trust established.")
			os.Exit(1)
		}
	}
}

// hubP2PPort extracts the port number from a Hub's advertised p2p address.
// It reports false rather than guessing when the address is absent or
// malformed, because a wrong answer here would move the pairing listener off a
// port that was never in conflict.
func hubP2PPort(p2pAddr string) (int, bool) {
	if p2pAddr == "" {
		return 0, false
	}
	_, portStr, err := net.SplitHostPort(p2pAddr)
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// choosePairingPort keeps the dedicated pairing listener off the port the local
// Hub already owns.
//
// It was written as `if pairingPort == 9877 { pairingPort = 9878 }`, comparing
// against the literal default instead of the Hub's real port. Every Hub not on
// 9877 therefore got no adjustment: a configured p2p_port, or a second Hub,
// which is exactly what the loopback transport exists for. Measured, with a Hub
// on 19702: `tantu pair -port 19702` announced "Waiting for peer connection on
// 0.0.0.0:19702..." and then produced no further output, while the initiator's
// connection reached the Hub's p2p listener and came back as "wsarecv: An
// existing connection was forcibly closed by the remote host". The responder
// logged nothing at all.
//
// Extracted as a pure function so it can be gated directly. An earlier version of
// the gate defined this rule *inside the test* and asserted on it, which proved
// only that the test's own arithmetic worked -- the same mistake as asserting on
// a hand-built value, one level down.
//
// Staying off the Hub's port entirely is a separate, deeper decision: it changes
// the command the user is told to run on the other machine. This only stops the
// collision from being invisible.
func choosePairingPort(requested, hubPort int) (int, bool) {
	if hubPort <= 0 || requested != hubPort {
		return requested, false
	}
	return requested + 1, true
}
