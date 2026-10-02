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
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// ClearScreen emits ANSI escape sequences to clear the terminal screen and scrollback buffer.
func ClearScreen() {
	fmt.Print("\033[H\033[2J\033[3J")
}

// cockpitShortcutHelp is the single source for the shortcut line, so the
// banner, [h], and the unknown-key message can never drift apart.
func cockpitShortcutHelp() string {
	return "[o] open  [s] file  [t] text  [l] sign-ins  [c] clear  [p] peer  [v] verbose  [q] quit"
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
				// Always mint a fresh link: the printed URL is one-use, so
				// re-printing a spent link would send the user to a page
				// that can only fail. The terminal shows the current valid
				// link every time.
				webURL := h.NewDashboardURL()
				if webURL == "" {
					webURL = "http://" + h.WebAddr()
				}
				fmt.Printf("🌐 Opening browser dashboard: %s\n", webURL)
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
						// Same policy as `tantu send <dir>`: expand into
						// independent per-file transfers rather than refusing.
						// A directory is the most natural thing to point a
						// file-send prompt at, and refusing it there while
						// accepting it in the CLI would be an inconsistency
						// the user has to learn rather than remember.
						fmt.Printf("📁 Directory: sending each file separately to %s...\n", cockpitDestinationName(h, targetPeer))
						files, expErr := expandDirectory(filePath)
						if expErr != nil {
							fmt.Printf("❌ %s\n", expErr.Message)
							if expErr.NextAction != "" {
								fmt.Printf("   Next: %s\n", expErr.NextAction)
							}
							continue
						}
						dest := targetPeer
						go func(batch []expandedFile, peer string) {
							sendCockpitBatch(ctx, h, batch, peer)
						}(files, dest)
						continue
					}

					fmt.Printf("Destination: %s\n", cockpitDestinationName(h, targetPeer))
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

						fileSuppressed := false
						fileCfg := drop.SendDropConfig{Timeout: h.Timeout()}
						fileCfg.OnComplete = func(c drop.DropComplete) { fileSuppressed = c.Duplicate }
						if err := drop.SendDrop(sendCtx, conn, meta, f, fileCfg); err != nil {
							fmt.Printf("❌ Transfer failed: %v\n", err)
							return
						}
						if fileSuppressed {
							fmt.Printf("ℹ️  Already saved on %s; duplicate delivery suppressed\n", filepath.Base(path))
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

			case "l", "logins", "signin", "sign-ins":
				// In-flight sign-ins were invisible from the terminal, so
				// "why is my login stuck" had no answer here even though the
				// dashboard could answer it. Cancellation matters more than the
				// listing: an abandoned sign-in holds the peer's loopback
				// callback port, which is normally the application's fixed
				// redirect port, so it blocks the retry.
				printActiveSignIns(ctx, scanner, h)

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
				fmt.Println("Shortcuts: " + cockpitShortcutHelp())

			default:
				fmt.Printf("Unknown shortcut '%s'. Available: %s\n", line, cockpitShortcutHelp())
			}
		}
	}()
}

// selectActiveSignIn maps a cockpit answer to one of the listed sign-ins.
//
// It is deliberately strict. Only a plain in-range 1-based index selects:
// empty input means "keep waiting", and anything ambiguous is refused rather
// than guessed, because releasing the wrong sign-in would tear down a login
// the user still wanted.
func selectActiveSignIn(active []hub.ActiveRelay, choice string) (hub.ActiveRelay, bool) {
	choice = strings.TrimSpace(choice)
	if choice == "" || len(active) == 0 {
		return hub.ActiveRelay{}, false
	}
	if !isPlainIndex(choice) {
		return hub.ActiveRelay{}, false
	}
	idx, err := strconv.Atoi(choice)
	if err != nil || idx < 1 || idx > len(active) {
		return hub.ActiveRelay{}, false
	}
	return active[idx-1], true
}

// describeActiveSignIn renders one listed sign-in. A relay that has not yet
// reached the wire is labelled distinctly, so the user is not told it is
// "in flight" on the peer when it is not.
func describeActiveSignIn(relay hub.ActiveRelay) string {
	peer := strings.TrimSpace(relay.Peer)
	if peer == "" {
		peer = "resolving"
	}
	state := "waiting for browser"
	if relay.State == "starting" {
		state = "starting (not yet on the wire)"
	}
	age := relay.Age
	if relay.AgeSeconds > 0 {
		age = time.Duration(relay.AgeSeconds) * time.Second
	}
	return fmt.Sprintf("%s · %s · %s", peer, state, age.Round(time.Second))
}

// printActiveSignIns lists in-flight OAuth sign-ins and offers to release one.
//
// A sign-in that is waiting on a browser is invisible everywhere in the
// terminal by default, and the symptom - a CLI that never returns - has no
// explanation attached to it. Worse, the peer is holding a loopback callback
// port for the duration, and because an application's redirect port is
// normally fixed, an abandoned sign-in blocks the retry that would follow. So
// this lists what is open, how long it has been open, and offers to stop it.
func printActiveSignIns(ctx context.Context, scanner *bufio.Scanner, h *hub.Hub) {
	active := h.ActiveRelays()
	if len(active) == 0 {
		fmt.Println("ℹ️  No sign-in is in progress.")
		return
	}

	fmt.Println()
	fmt.Printf("--- In-Flight Sign-Ins (%d) ---\n", len(active))
	for idx, relay := range active {
		fmt.Printf("  [%d] %s\n", idx+1, describeActiveSignIn(relay))
	}
	fmt.Println("--------------------------------")
	fmt.Printf("Release one [1-%d], or press Enter to keep waiting: ", len(active))

	if !scanner.Scan() {
		return
	}
	target, ok := selectActiveSignIn(active, scanner.Text())
	if !ok {
		// Enter means "keep waiting", which is the common case and needs no
		// explanation. Anything else is a mis-typed selection, and releasing
		// the wrong sign-in is worse than releasing none, so it is refused.
		if strings.TrimSpace(scanner.Text()) != "" {
			fmt.Println("ℹ️  Enter a number from the list, or press Enter to keep waiting.")
		}
		return
	}
	// A cancel can take a round trip to the peer, so it is bounded: a slow or
	// unreachable peer must not hang the cockpit, which is also how the user
	// would release it if it did.
	cancelCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := h.CancelRelay(cancelCtx, target.OperationID)
	if err != nil {
		fmt.Printf("❌ Could not release that sign-in: %v\n", err)
		return
	}
	fmt.Printf("🚪 %s\n", result.Message)
}

// sendCockpitBatch streams a directory from the cockpit, reusing the same
// per-file transfer path as a single file send so both surfaces report
// identically. It runs in its own goroutine because the cockpit's hotkey
// listener must stay responsive while files are moving.
func sendCockpitBatch(ctx context.Context, h *hub.Hub, files []expandedFile, peer string) {
	sent, failed := 0, 0
	for _, f := range files {
		select {
		case <-ctx.Done():
			fmt.Printf("\n⏹️  Stopped after %d of %d file(s).\n", sent, len(files))
			return
		default:
		}

		file, err := os.Open(f.Path)
		if err != nil {
			failed++
			fmt.Printf("❌ %s: %v\n", f.Name, err)
			continue
		}
		conn, dialErr := h.DialPeer(peer)
		if dialErr != nil {
			_ = file.Close()
			failed++
			fmt.Printf("❌ %s: %v\n", f.Name, dialErr)
			continue
		}

		dropID := drop.NewDropID()
		meta := drop.DropSend{
			DropID:   dropID,
			Kind:     drop.DropKindFile,
			Name:     f.Name,
			Size:     f.Size,
			MIMEType: mime.TypeByExtension(filepath.Ext(f.Name)),
		}
		sendCtx, cancel := context.WithTimeout(ctx, h.Timeout())
		suppressed := false
		cfg := drop.SendDropConfig{Timeout: h.Timeout()}
		cfg.OnComplete = func(c drop.DropComplete) { suppressed = c.Duplicate }
		sendErr := drop.SendDrop(sendCtx, conn, meta, file, cfg)
		cancel()
		_ = file.Close()
		_ = conn.Close()

		switch {
		case sendErr != nil:
			failed++
			fmt.Printf("❌ %s: %v\n", f.Name, sendErr)
		case suppressed:
			// A success, but nothing new was written, so it must not be
			// reported as a fresh copy.
			sent++
			fmt.Printf("ℹ️  %s was already saved on the peer\n", f.Name)
		default:
			sent++
			fmt.Printf("✅ %s (%s)\n", f.Name, formatBytes(f.Size))
		}
	}
	if failed == 0 {
		fmt.Printf("✅ Sent %d file(s).\n", sent)
	} else {
		fmt.Printf("⚠️  Sent %d of %d file(s); %d failed.\n", sent, len(files), failed)
	}
}

// cockpitDestinationName resolves a display destination for sends without
// ever substituting a different peer silently. Empty with a single peer
// means that peer; empty with an active peer means the active peer.
func cockpitDestinationName(h *hub.Hub, targetPeer string) string {
	if h == nil || h.Store() == nil {
		if strings.TrimSpace(targetPeer) == "" {
			return "default peer"
		}
		return targetPeer
	}
	peers := h.Store().ListPeers()
	if strings.TrimSpace(targetPeer) != "" {
		if rp, err := h.Store().ResolvePeer(targetPeer); err == nil && rp != nil {
			return rp.DisplayName()
		}
		return targetPeer
	}
	if len(peers) == 1 {
		return peers[0].DisplayName()
	}
	if activeFP := h.GetActivePeer(); activeFP != "" {
		if ap, err := h.Store().ResolvePeer(activeFP); err == nil && ap != nil {
			return ap.DisplayName()
		}
	}
	return "default peer"
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
	fmt.Printf("Destination: %s\n", cockpitDestinationName(h, targetPeer))
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

		snippetSuppressed := false
		snippetCfg := drop.SendDropConfig{Timeout: h.Timeout()}
		snippetCfg.OnComplete = func(c drop.DropComplete) { snippetSuppressed = c.Duplicate }
		if err := drop.SendDrop(sendCtx, conn, meta, strings.NewReader(snippet), snippetCfg); err != nil {
			fmt.Printf("❌ Text transfer failed: %v\n", err)
			return
		}
		if snippetSuppressed {
			fmt.Printf("ℹ️  Already saved on the peer; duplicate delivery suppressed\n")
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
			isPaired := h.Store() != nil && h.Store().HasPeerAtAddress(node.Address)
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
	// A sign-in waiting on a browser used to be invisible from the terminal
	// entirely: a CLI that never returns looked identical to a hung process.
	// One line in the banner turns "is it stuck?" into a visible answer, and
	// names the key that stops it.
	if active := h.ActiveRelays(); len(active) > 0 {
		label := fmt.Sprintf("  ⚠️  %d sign-in(s) in progress — press [l] to list or release", len(active))
		fmt.Printf("│  \033[33m%-59s\033[0m │\n", label)
	}
	if nearbyLabel != "" {
		fmt.Printf("│  \033[32m%-59s\033[0m │\n", nearbyLabel)
	}
	fmt.Println("└────────────────────────────────────────────────────────────────┘")
	fmt.Println()
	fmt.Println("  Shortcuts: " + cockpitShortcutHelp())
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
			isPaired := h.Store() != nil && h.Store().HasPeerAtAddress(node.Address)
			if !isPaired {
				// No SAS is shown: discovery broadcasts no identity, so any
				// code printed here would be fabricated. The real verification
				// code exists only inside an active pairing handshake.
				fmt.Printf("Nearby Hub:     \033[32m⚡ %s (%s, not yet paired)\033[0m\n", node.InstanceName, node.Address)
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
