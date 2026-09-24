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
			Name        string `json:"name"`
			Address     string `json:"address"`
			Port        int    `json:"port"`
			SAS         string `json:"sas"`
			Fingerprint string `json:"fingerprint"`
			IsPaired    bool   `json:"is_paired"`
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
				_ = eng.Start(scanCtx)
				<-scanCtx.Done()
				_ = eng.Close()
				scanCancel()
				for _, node := range eng.ListNodes() {
					if _, ok := store.GetPeer(node.Fingerprint); !ok {
						discovered = append(discovered, discPeer{
							Name:        node.InstanceName,
							Address:     node.Address,
							Port:        node.Port,
							SAS:         node.SAS,
							Fingerprint: node.Fingerprint,
							IsPaired:    false,
						})
					}
				}
			}
			fmt.Println("done.")
		}

		if len(discovered) > 0 {
			fmt.Println("\nDiscovered nearby hubs on LAN:")
			for idx, d := range discovered {
				fmt.Printf("  [%d] %s (%s, SAS: %s)\n", idx+1, d.Name, d.Address, d.SAS)
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
			if pairingPort == 9877 {
				pairingPort = 9878
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

		// Standalone initiator mode
		id, err := store.LoadIdentity()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading identity: %v\n", err)
			os.Exit(1)
		}
		if id == nil {
			id, err = pairing.GenerateIdentity()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating identity: %v\n", err)
				os.Exit(1)
			}
			if err := store.SaveIdentity(id); err != nil {
				fmt.Fprintf(os.Stderr, "Error saving identity: %v\n", err)
				os.Exit(1)
			}
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
