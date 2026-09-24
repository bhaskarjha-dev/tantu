package main

import (
	"bufio"
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// ClearScreen emits ANSI escape sequences to clear the terminal screen and scrollback buffer.
func ClearScreen() {
	fmt.Print("\033[H\033[2J\033[3J")
}

// RunCockpit starts the interactive developer terminal cockpit.
// It renders a status banner, attaches to the Hub's EventLogger, and listens for non-blocking hotkeys.
func RunCockpit(ctx context.Context, h *hub.Hub, cancel context.CancelFunc, initialVerbose ...bool) {
	var verboseMode atomic.Bool
	if len(initialVerbose) > 0 && initialVerbose[0] {
		verboseMode.Store(true)
	}

	// Print Developer Cockpit Banner
	printCockpitBanner(h)

	// Stream colorized events directly to stdout
	if h.EventLogger() != nil {
		h.EventLogger().SetOnEvent(func(ev hub.LogEvent) {
			if !verboseMode.Load() && ev.Level == hub.LevelDebug {
				return
			}
			fmt.Println(hub.FormatTerminal(ev))
		})
	}

	// Attach snippet receiver hook to render incoming snippets as visual cards
	h.SetOnSnippetReceived(func(item hub.ReceivedDropItem) {
		RenderSnippetCard(item)
	})

	// Spawn hotkey listener in background goroutine
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if !scanner.Scan() {
				// EOF or stdin closed (e.g. non-interactive or piping)
				return
			}

			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			cmd := strings.ToLower(line)
			switch cmd {
			case "o", "open":
				webURL := h.DashboardURL()
				if webURL == "" {
					webURL = "http://" + h.WebAddr()
				}
				fmt.Printf("🌐 Opening browser dashboard: %s\n", h.WebAddr())
				_ = browser.OpenURL(webURL)

			case "s", "send", "file":
				var targetPeer string
				if h.Store() != nil {
					peers := h.Store().ListPeers()
					if len(peers) > 1 {
						activeFP := h.GetActivePeer()
						activePeer, _ := h.Store().ResolvePeer(activeFP)
						activeName := "Active"
						if activePeer != nil {
							activeName = activePeer.DisplayName()
						}
						var promptParts []string
						for idx, p := range peers {
							tag := ""
							if activePeer != nil && p.Fingerprint == activePeer.Fingerprint {
								tag = " (Active)"
							}
							promptParts = append(promptParts, fmt.Sprintf("%d: %s%s", idx+1, p.DisplayName(), tag))
						}
						fmt.Printf("🎯 Send to [%s, or Enter for %s]: ", strings.Join(promptParts, ", "), activeName)
						if scanner.Scan() {
							if sel := parsePeerSelection(peers, scanner.Text()); sel != "" {
								targetPeer = sel
							}
						}
					}
				}

				fmt.Print("📁 Enter file path to send: ")
				if scanner.Scan() {
					rawPath := scanner.Text()
					filePath := SanitizePath(rawPath)
					if filePath == "" {
						fmt.Println("❌ No path provided.")
						continue
					}
					info, err := os.Stat(filePath)
					if err != nil {
						fmt.Printf("❌ Cannot open file: %v\n", err)
						continue
					}
					if info.IsDir() {
						fmt.Println("❌ Directories cannot be sent directly; please archive into .zip first.")
						continue
					}

					go func(path string, size int64, peer string) {
						fmt.Printf("⏳ Streaming %s (%d bytes) to peer...\n", filepath.Base(path), size)
						f, err := os.Open(path)
						if err != nil {
							fmt.Printf("❌ Failed to open file: %v\n", err)
							return
						}
						defer f.Close()

						conn, err := h.DialPeer(peer)
						if err != nil {
							fmt.Printf("❌ Dial peer failed: %v\n", err)
							return
						}
						defer conn.Close()

						meta := drop.DropSend{
							Kind:     drop.DropKindFile,
							Name:     filepath.Base(path),
							Size:     size,
							MIMEType: mime.TypeByExtension(filepath.Ext(path)),
						}

						sendCtx, sendCancel := context.WithTimeout(ctx, h.Timeout())
						defer sendCancel()

						if err := drop.SendDrop(sendCtx, conn, meta, f, drop.SendDropConfig{Timeout: h.Timeout()}); err != nil {
							fmt.Printf("❌ Transfer failed: %v\n", err)
							return
						}
						fmt.Printf("✅ Transferred %s successfully!\n", filepath.Base(path))
					}(filePath, info.Size(), targetPeer)
				}

			case "t", "text", "snippet":
				var targetPeer string
				if h.Store() != nil {
					peers := h.Store().ListPeers()
					if len(peers) > 1 {
						activeFP := h.GetActivePeer()
						activePeer, _ := h.Store().ResolvePeer(activeFP)
						activeName := "Active"
						if activePeer != nil {
							activeName = activePeer.DisplayName()
						}
						var promptParts []string
						for idx, p := range peers {
							tag := ""
							if activePeer != nil && p.Fingerprint == activePeer.Fingerprint {
								tag = " (Active)"
							}
							promptParts = append(promptParts, fmt.Sprintf("%d: %s%s", idx+1, p.DisplayName(), tag))
						}
						fmt.Printf("🎯 Send to [%s, or Enter for %s]: ", strings.Join(promptParts, ", "), activeName)
						if scanner.Scan() {
							if sel := parsePeerSelection(peers, scanner.Text()); sel != "" {
								targetPeer = sel
							}
						}
					}
				}
				promptSendText(ctx, scanner, h, targetPeer)

			case "c", "clear", "cls":
				ClearScreen()
				printCockpitBanner(h)

			case "v", "verbose":
				cur := !verboseMode.Load()
				verboseMode.Store(cur)
				if cur {
					fmt.Println("Verbose mode: ON")
				} else {
					fmt.Println("Verbose mode: OFF")
				}

			case "p", "peer", "status":
				handlePeerCommand(scanner, h)

			case "q", "quit", "exit":
				fmt.Println("Stopping tantu hub...")
				cancel()
				return

			case "h", "help", "?":
				fmt.Println("Shortcuts: [o] open  [s] file  [t] text  [c] clear  [p] peer  [v] verbose  [q] quit")

			default:
				fmt.Printf("Unknown shortcut '%s'. Available: [o] open  [s] file  [t] text  [c] clear  [p] peer  [v] verbose  [q] quit\n", line)
			}
		}
	}()
}

// parsePeerSelection maps a cockpit peer-prompt answer to a peer fingerprint.
// A strict 1-based index selects from peers: leading/trailing junk (such as
// "1abc" or "+1") must not select, so only plain digits qualify. Anything
// else is returned verbatim as a raw query for the store resolver (name,
// alias, address) to interpret. Empty input yields "".
func parsePeerSelection(peers []pairing.Peer, choice string) string {
	choice = strings.TrimSpace(choice)
	if choice == "" || len(peers) == 0 {
		return ""
	}
	if isPlainIndex(choice) {
		if selIdx, err := strconv.Atoi(choice); err == nil && selIdx >= 1 && selIdx <= len(peers) {
			return peers[selIdx-1].Fingerprint
		}
	}
	return choice
}

// isPlainIndex reports whether s is ASCII digits only (no sign, no spaces,
// no trailing junk). strconv.Atoi alone would accept "+1" and silently steal
// that name from the resolver.
func isPlainIndex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func handlePeerCommand(scanner *bufio.Scanner, h *hub.Hub) {
	if h.Store() == nil || len(h.Store().ListPeers()) == 0 {
		fmt.Println("ℹ️ No paired LAN peers found. Run 'tantu pair' to connect a machine.")
		return
	}
	peers := h.Store().ListPeers()
	if len(peers) == 1 {
		printPeerStatus(h)
		return
	}

	activeFP := h.GetActivePeer()
	activePeer, _ := h.Store().ResolvePeer(activeFP)

	fmt.Println()
	fmt.Printf("--- Active Peer Selector (%d Paired Peers) ---\n", len(peers))
	for idx, p := range peers {
		tag := ""
		if activePeer != nil && p.Fingerprint == activePeer.Fingerprint {
			tag = " [ACTIVE 🟢]"
		} else if p.IsDefault {
			tag = " [DEFAULT]"
		}
		fmt.Printf("  [%d] %s (%s) (SAS: %s)%s\n", idx+1, p.DisplayName(), p.Address, p.SAS(), tag)
	}
	fmt.Println("--------------------------------------------")
	fmt.Printf("Select active peer [1-%d] or Enter to keep current: ", len(peers))
	if scanner.Scan() {
		choice := strings.TrimSpace(scanner.Text())
		if choice == "" {
			return
		}
		targetFP := parsePeerSelection(peers, choice)

		if err := h.SetActivePeer(targetFP); err != nil {
			fmt.Printf("❌ Failed to switch active peer: %v\n", err)
		} else {
			newActive, _ := h.Store().ResolvePeer(targetFP)
			name := targetFP
			addr := ""
			if newActive != nil {
				name = newActive.DisplayName()
				addr = newActive.Address
			}
			fmt.Printf("✅ Active peer set to: %s (%s)\n", name, addr)
		}
	}
}

func promptSendText(ctx context.Context, scanner *bufio.Scanner, h *hub.Hub, targetPeer string) {
	fmt.Print("📝 Enter text snippet (end with blank line): ")
	var lines []string
	var total int64
	for scanner.Scan() {
		text := scanner.Text()
		if text == "" {
			break
		}
		// Cap local ingest at the wire text limit: an unbounded paste would
		// otherwise balloon memory long before the receiver rejects it.
		total += int64(len(text) + 1)
		if total > standaloneTextDropLimit {
			fmt.Println("❌ Snippet exceeds the 10MB text limit; truncated input discarded.")
			return
		}
		lines = append(lines, text)
	}
	if len(lines) == 0 {
		fmt.Println("❌ No text provided.")
		return
	}
	content := strings.Join(lines, "\n")
	go func(snippet string, peer string) {
		preview := snippet
		if len(preview) > 40 {
			preview = preview[:37] + "..."
		}
		preview = strings.ReplaceAll(preview, "\n", " ")
		fmt.Printf("⏳ Streaming text snippet (%d bytes) to peer...\n", len(snippet))
		conn, err := h.DialPeer(peer)
		if err != nil {
			fmt.Printf("❌ Dial peer failed: %v\n", err)
			return
		}
		defer conn.Close()

		meta := drop.DropSend{
			Kind:     drop.DropKindText,
			Name:     "snippet.txt",
			Size:     int64(len(snippet)),
			MIMEType: "text/plain; charset=utf-8",
		}

		sendCtx, sendCancel := context.WithTimeout(ctx, h.Timeout())
		defer sendCancel()

		if err := drop.SendDrop(sendCtx, conn, meta, strings.NewReader(snippet), drop.SendDropConfig{Timeout: h.Timeout()}); err != nil {
			fmt.Printf("❌ Text transfer failed: %v\n", err)
			return
		}
		fmt.Printf("✅ Transferred text snippet successfully! (\"%s\")\n", preview)
	}(content, targetPeer)
}

func printCockpitBanner(h *hub.Hub) {
	peerLabel := "Waiting for peers (LAN Ready 🟢)"
	if h.TransportType() == "loopback" {
		peerLabel = "Loopback Mode (Local Only)"
	}
	if h.Store() != nil {
		peers := h.Store().ListPeers()
		if len(peers) == 1 {
			p := peers[0]
			name := p.DisplayName()
			peerLabel = fmt.Sprintf("%s (%s) [Online 🟢]", name, p.Address)
		} else if len(peers) > 1 {
			activeFP := h.GetActivePeer()
			activePeer, _ := h.Store().ResolvePeer(activeFP)
			var parts []string
			for _, p := range peers {
				dName := p.DisplayName()
				if activePeer != nil && activePeer.Fingerprint == p.Fingerprint {
					parts = append(parts, fmt.Sprintf("%s* [Online 🟢]", dName))
				} else {
					parts = append(parts, dName)
				}
			}
			peerLabel = fmt.Sprintf("Peers (%d): %s", len(peers), strings.Join(parts, " • "))
		}
	}

	p2pAddr := "127.0.0.1:9877"
	if h.P2PAddr() != nil {
		p2pAddr = h.P2PAddr().String()
	}

	var nearbyLabel string
	if engine := h.DiscoveryEngine(); engine != nil {
		for _, node := range engine.ListNodes() {
			isPaired := false
			if h.Store() != nil {
				if _, ok := h.Store().GetPeer(node.Fingerprint); ok {
					isPaired = true
				}
			}
			if !isPaired {
				nearbyLabel = fmt.Sprintf("⚡ Nearby: %s (%s)", node.InstanceName, node.Address)
				break
			}
		}
	}

	fmt.Println()
	fmt.Println("┌────────────────────────────────────────────────────────────────┐")
	fmt.Printf("│  🚀 tantu v%-45s │\n", hub.HubVersion+" (Unified Symmetric Hub)")
	fmt.Println("│                                                                │")
	fmt.Printf("│  ➜ Dashboard:  http://%-40s │\n", h.WebAddr()+"/")
	fmt.Printf("│  ➜ Network:    %-47s │\n", p2pAddr+" ("+h.TransportType()+" mTLS multiplexer)")
	fmt.Printf("│  ➜ Peer:       %-47s │\n", peerLabel)
	if nearbyLabel != "" {
		fmt.Printf("│  \033[32m%-59s\033[0m │\n", nearbyLabel)
	}
	fmt.Println("└────────────────────────────────────────────────────────────────┘")
	fmt.Println()
	fmt.Println("  Shortcuts: [o] open  [s] file  [t] text  [c] clear  [p] peer  [v] verbose  [q] quit")
	fmt.Println()
}

func printPeerStatus(h *hub.Hub) {
	fmt.Println("--- Hub & Peer Status ---")
	if id := h.Identity(); id != nil {
		fmt.Printf("Local Identity: SAS: %s • FP: %s\n", pairing.SASCode(id.Fingerprint), id.Fingerprint)
	}
	fmt.Printf("Transport:      %s\n", h.TransportType())
	if h.P2PAddr() != nil {
		fmt.Printf("P2P Socket:     %s\n", h.P2PAddr().String())
	}
	fmt.Printf("Web Socket:     http://%s\n", h.WebAddr())

	if engine := h.DiscoveryEngine(); engine != nil {
		for _, node := range engine.ListNodes() {
			isPaired := false
			if h.Store() != nil {
				if _, ok := h.Store().GetPeer(node.Fingerprint); ok {
					isPaired = true
				}
			}
			if !isPaired {
				fmt.Printf("Nearby Hub:     \033[32m⚡ %s (%s, SAS: %s)\033[0m\n", node.InstanceName, node.Address, node.SAS)
			}
		}
	}

	if h.Store() != nil {
		peers := h.Store().ListPeers()
		activeFP := h.GetActivePeer()
		activePeer, _ := h.Store().ResolvePeer(activeFP)
		fmt.Printf("Paired Peers (%d):\n", len(peers))
		for idx, p := range peers {
			tag := ""
			if activePeer != nil && p.Fingerprint == activePeer.Fingerprint {
				tag = " [ACTIVE 🟢]"
			} else if p.IsDefault {
				tag = " [DEFAULT]"
			}
			fmt.Printf("  [%d] %s — %s (SAS: %s, FP: %s)%s\n", idx+1, p.DisplayName(), p.Address, p.SAS(), p.Fingerprint, tag)
		}
	}
	fmt.Println("-------------------------")
}

// RenderSnippetCard prints a prominent boxed frame with 2-space indentation for incoming text drops.
func RenderSnippetCard(item hub.ReceivedDropItem) {
	timeStr := item.Timestamp.Format("15:04:05")
	from := item.FromPeer
	if from == "" {
		from = "Peer"
	}
	kindLabel := "Text Snippet"
	if item.IsURL {
		kindLabel = "URL Link"
	}

	fmt.Println()
	fmt.Printf("┌── 📝 QuickDrop %s (%s • %s) ──────────────────────────────\n", kindLabel, from, timeStr)
	// Never dump megabytes into the terminal: a malicious or careless peer
	// could otherwise scroll-bomb the cockpit with a 10MB text drop.
	content := item.Content
	const maxSnippetRender = 4096
	truncated := false
	if len(content) > maxSnippetRender {
		content = content[:maxSnippetRender]
		truncated = true
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		fmt.Printf("│  %s\n", line)
	}
	if truncated {
		fmt.Printf("│  … (%d total bytes, truncated)\n", len(item.Content))
	}
	fmt.Println("└─────────────────────────────────────────────────────────────────────────────")
	fmt.Println()
}
