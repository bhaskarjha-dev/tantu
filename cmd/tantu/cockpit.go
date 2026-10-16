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
	"unicode/utf8"

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

// cockpitRow is one line of the banner, before borders are drawn. The colour is
// kept separate so that truncating never has to reason about escape sequences.
type cockpitRow struct {
	text  string
	color string // "" plain, "33" warning, "32" positive
}

// cockpitRuneWidth reports how many terminal columns a rune occupies. The banner
// is full of emoji and arrows -- the rocket, the arrow, the status dot, the
// bullet, the warning sign -- and most of them are double width, so counting
// them as one column is exactly what made the old box look correct in a diff and
// wrong on screen.
//
// This is a compact table rather than a dependency. It covers the ranges this
// banner uses plus the East Asian blocks; it is not a general wcwidth and does
// not need to be, because the only strings it measures are ones this file
// builds.
func cockpitRuneWidth(r rune) int {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF, // pictographs, symbols and emoji
		r >= 0x1F900 && r <= 0x1F9FF, // supplemental symbols and pictographs
		r >= 0x2B00 && r <= 0x2BFF,   // miscellaneous arrows and shapes
		r >= 0xFE00 && r <= 0xFE0F,   // variation selectors
		r >= 0x1F1E6 && r <= 0x1F1FF: // regional indicators
		return 2
	case r == 0x26A0, // warning sign
		r == 0x2691, r == 0x2693, // the pin and the helmet below
		r >= 0x2600 && r <= 0x26FF: // miscellaneous symbols
		return 2
	case r >= 0x2700 && r <= 0x27BF: // dingbats: arrows and bullets
		return 2
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6:
		return 2
	case r >= 0x0300 && r <= 0x036F: // combining marks take no space
		return 0
	}
	return 1
}

// cockpitWidth is the rendered column count of s.
func cockpitWidth(s string) int {
	w := 0
	for _, r := range s {
		w += cockpitRuneWidth(r)
	}
	return w
}

// cockpitTruncate shortens s to at most max columns, ending in an ellipsis when
// it had to cut. It cuts on a rune boundary and never leaves a double-width rune
// straddling the edge.
func cockpitTruncate(s string, max int) string {
	if max <= 1 || cockpitWidth(s) <= max {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := cockpitRuneWidth(r)
		if w+rw > max-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteString("…")
	return b.String()
}

const (
	// cockpitPeerLineWidth is the column budget for one peer line.
	//
	// Not 80. The line carries an index, a name, an address, a pairing code, a
	// reachability sentence and a selection marker, and 80 does not hold them
	// once a peer has stopped answering. 100 is a width a terminal is very likely
	// to have, and a line that fits 100 still reads at 120.
	cockpitPeerLineWidth = 100

	// cockpitPeerNameFloor is how much of the name survives when the rest of the
	// line is long. A truncated name is still recognisable; an empty one leaves a
	// bare list of addresses.
	cockpitPeerNameFloor = 10

	// cockpitReachDetailShort and cockpitReachBareWidth are the budgets the
	// reachability sentence is shortened to, in that order, when the name has
	// already reached its floor.
	cockpitReachDetailShort = 16
	cockpitReachBareWidth   = 13
)

// cockpitBox draws the banner around rows, sizing the border to the widest row
// and capping it so one enormous value cannot produce a box nobody can read.
// The lines are returned as well as printed, because the only way to know a box
// is the right width is to measure what was drawn.
//
// The box used to be a fixed 66 columns containing six hardcoded minimum field
// widths. A minimum width pads short content and does nothing about long
// content, so both unbounded values broke it, in opposite directions. Measured
// against the same 66-column border on one build:
//
//	title   7 columns SHORT   (dev version string)
//	title   9 columns OVER    (release-shaped version string)
//	peer   22 columns OVER   (two peers)
//
// None of that was visible without running the binary and measuring its output.
func cockpitBox(rows []cockpitRow) []string {
	const (
		minContent = 60
		maxContent = 74
	)
	content := minContent
	for _, r := range rows {
		if w := cockpitWidth(r.text); w > content {
			content = w
		}
	}
	if content > maxContent {
		content = maxContent
	}

	// Every line must measure the same: one border column, two spaces, the
	// content padded to exactly content columns, two more spaces, one border
	// column. So the rule between the corners is content+4, not content+2.
	pad := strings.Repeat("─", content+4)
	lines := make([]string, 0, len(rows)+2)
	lines = append(lines, "┌"+pad+"┐")
	for _, r := range rows {
		text := cockpitTruncate(r.text, content)
		fill := content - cockpitWidth(text)
		if fill < 0 {
			fill = 0
		}
		line := "│  " + text + strings.Repeat(" ", fill) + "  │"
		if r.color != "" {
			line = "│  \033[" + r.color + "m" + text +
				strings.Repeat(" ", fill) + "\033[0m  │"
		}
		lines = append(lines, line)
	}
	lines = append(lines, "└"+pad+"┘")
	for _, l := range lines {
		fmt.Println(l)
	}
	return lines
}

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
						files, expErr := expandDirectory(filePath, true)
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

// cockpitReachMarker is the short form of what the Hub knows about a peer, for the
// banner.
//
// It used to be the literal string "[Online 🟢]", applied to every paired
// peer unconditionally. That is not a label, it is a claim: pairing records that
// you trust a machine, which says nothing about whether it is switched on. A peer
// whose address is unroutable was reported Online for as long as it stayed in
// the store -- including after the heartbeat had spent several minutes proving
// that it was not answering.
//
// The dashboard has carried a truthful reachability chip since the heartbeat
// landed, which made the cockpit's version worse than merely wrong: the same Hub,
// at the same moment, two interfaces, one of them honest.
//
// Silence is a legitimate answer. A peer the loopback transport never probes, or
// one added since the last tick, has no evidence either way, and "Online" would
// be inventing one.
func cockpitReachMarker(health hub.PeerHealth) string {
	switch {
	case health.LastChecked.IsZero():
		return ""
	case health.Reachable:
		return " [🟢]"
	default:
		return " [Not answering 🔴]"
	}
}

// cockpitReachDetail is the long form, for the listing where there is room to say
// when a peer was last heard from.
//
// "Not answering" alone tells a reader they have a problem without telling them
// which one, and the two are different problems: a peer that stopped answering
// four minutes ago is a laptop that went to sleep, and a peer that has never
// answered is a wrong address or a pairing that never actually worked.
func cockpitReachDetail(health hub.PeerHealth) string {
	switch {
	case health.LastChecked.IsZero():
		return ""
	case health.Reachable:
		return "Online"
	default:
		was := "has not answered since pairing"
		if !health.LastOK.IsZero() {
			was = "last answered " + formatRelativeTime(health.LastOK)
		}
		return "Not answering · " + was
	}
}

// cockpitReachDetailFitting shortens the sentence until it fits budget.
//
// The sentence is the one part of the line that grows without bound as a peer's
// problem gets more interesting, so it is what gives way -- but only after the
// name has, and never by being dropped. "Not answering" in any form is the fact
// a reader most needs; the age of the last success is a refinement.
func cockpitReachDetailFitting(health hub.PeerHealth, budget int) string {
	full := cockpitReachDetail(health)
	if full == "" || cockpitWidth(full) <= budget {
		return full
	}
	if health.Reachable {
		return "Online"
	}
	// A peer that has never answered has no age to report. "never answered" is the
	// whole truth and is shorter than the sentence it replaces, but it is still
	// 14 columns, so it has to be checked against the budget like every other
	// rung rather than assumed to fit.
	if health.LastOK.IsZero() {
		if cockpitWidth("never answered") <= budget {
			return "never answered"
		}
		for _, terse := range []string{"not answering", "down"} {
			if cockpitWidth(terse) <= budget {
				return terse
			}
		}
		return "down"
	}
	// "not answering (12m)" -- the age of the last success, which is what a
	// reader scanning a list is actually looking for.
	short := "not answering (" + formatRelativeTime(health.LastOK) + ")"
	if cockpitWidth(short) <= budget {
		return short
	}
	// The age itself is too long -- weeks, on a peer gone a long time. Drop the
	// age rather than the fact.
	// Every form must fit the budget it is given, so the ladder bottoms out in
	// something shorter rather than returning whatever was closest.
	for _, terse := range []string{"not answering", "down"} {
		if cockpitWidth(terse) <= budget {
			return terse
		}
	}
	// Four columns is the tightest case the line can reach, and "down" fits it.
	// If a smaller budget is ever asked for, the fact still has to be stated:
	// returning "" here would drop it silently, which is the one outcome this
	// whole function exists to avoid.
	return "down"
}

// reachPrefix renders the reachability sentence with its separator, or nothing
// when there is nothing to say.
func reachPrefix(reach string) string {
	if reach == "" {
		return ""
	}
	return " · " + reach + " "
}

// tagSuffix renders the selection marker with a leading space, so a peer line
// never runs two parts together. "[active]—" was printed once, and it is
// exactly the kind of thing a reader takes for "[active]-192.0.2.44:9877".
func tagSuffix(tag string) string {
	if tag == "" {
		return ""
	}
	return "  " + tag
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
			peerLabel = fmt.Sprintf("%s (%s)%s", name, p.Address, cockpitReachMarker(h.PeerHealthOf(p.Fingerprint)))
		} else if len(peers) > 1 {
			activeFP := h.GetActivePeer()
			activePeer, _ := h.Store().ResolvePeer(activeFP)
			var parts []string
			for _, p := range peers {
				dName := p.DisplayName()
				star := ""
				if activePeer != nil && activePeer.Fingerprint == p.Fingerprint {
					star = "*"
				}
				parts = append(parts, dName+star+cockpitReachMarker(h.PeerHealthOf(p.Fingerprint)))
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

	// The bare address is not a usable dashboard: every API behind it returns
	// 401 without a capability, so a user who copies this line gets a page whose
	// actions all fail and whose banner then tells them to run `tantu dashboard`
	// -- that is, to ignore the URL the Hub just printed. Verified by serving the
	// page with no token: the bookmarklet comes back as javascript:void(0) and
	// the box is marked unavailable.
	//
	// The headless branch in hub.go already prints the one-time link and labels
	// it. This one did not, which is the same defect 3.6 described for the
	// in-page recovery instructions: the advice given did not work from where
	// the reader was. Pressing `o` opens an authenticated link, so the fix is to
	// say that rather than to mint a token that may expire before it is used.
	//
	// Deliberately not printing the tokenised link: it is about 100 characters,
	// and the box caps content at 74 columns, so it would truncate into
	// uselessness. The instruction fits, says the same thing, and `o` is one
	// keypress away.
	rows := []cockpitRow{
		{text: "🚀 tantu v" + hub.HubVersion + " (Unified Symmetric Hub)"},
		{text: ""},
		{text: "➜ Dashboard:  http://" + h.WebAddr() + "/               (press o)"},
		{text: "➜ Network:    " + p2pAddr + " (" + h.TransportType() + " mTLS multiplexer)"},
		{text: "➜ Peer:       " + peerLabel},
	}
	// A sign-in waiting on a browser used to be invisible from the terminal
	// entirely: a CLI that never returns looked identical to a hung process.
	// One line in the banner turns "is it stuck?" into a visible answer, and
	// names the key that stops it.
	if active := h.ActiveRelays(); len(active) > 0 {
		rows = append(rows, cockpitRow{
			color: "33",
			text:  fmt.Sprintf("⚠️  %d sign-in(s) in progress — press [l] to list or release", len(active)),
		})
	}
	if nearbyLabel != "" {
		rows = append(rows, cockpitRow{color: "32", text: nearbyLabel})
	}

	fmt.Println()
	cockpitBox(rows)
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
				// No green dot. "Active" says which peer a send would go to --
				// it is a selection, not a status, and a green circle on the
				// same line as the reachability reads as a health signal. It was
				// "[ACTIVE 🟢]", which is the claim removed from the
				// banner above, restated.
				tag = " [active]"
			} else if p.IsDefault {
				tag = " [default]"
			}

			// The full 64-character fingerprint used to be printed inline here,
			// which made every peer line about 128 columns -- wrapping across two
			// lines on an 80-column terminal and pushing everything else off the
			// screen. The banner above spends seven tests and a good deal of code
			// respecting the terminal's width; the listing underneath it respected
			// nothing.
			//
			// The fingerprint is dropped rather than truncated, because it is not
			// what a reader of this line is for: the SAS is the code two people
			// compare out loud, the address is where it claims to be, and whether
			// it is answering is the thing a reader can act on. It remains in the
			// dashboard's peer listing and in the pair output, which is where
			// someone comparing two machines actually looks.
			index := fmt.Sprintf("  [%d] ", idx+1)
			address := fmt.Sprintf("— %s (SAS: %s)", p.Address, p.SAS())

			// Fit by measurement, not by a budget computed from the parts.
			//
			// An estimate that disagrees with the measurement produces a line
			// that is over budget while appearing to have been fitted, which is
			// the exact failure this code exists to prevent. The parts contain
			// runes whose width is not their byte length -- an em dash, a bullet,
			// an ellipsis -- and cockpitTruncate's own ellipsis adds a column the
			// budget never had. So: assemble, measure, cut by the excess, repeat.
			//
			// The name is what gives way first. A peer that is down is the fact
			// worth most on this line, and the name is still identifiable from
			// the address and pairing code beside it -- so the sentence is tried
			// at full length, then short, then as a bare fact, and only then is
			// the name allowed to reach its floor.
			assemble := func(name, reach string) string {
				// The trailing space belongs to the name, so that truncating the
				// name never leaves "laptop-3—" -- which happened, and which a
				// reader takes for part of the address.
				if name != "" {
					name += " "
				}
				return index + name + address + reachPrefix(reach) + tagSuffix(tag)
			}
			fitName := func(reach string) string {
				name := p.DisplayName()
				for pass := 0; pass < 8; pass++ {
					excess := cockpitWidth(assemble(name, reach)) - cockpitPeerLineWidth
					if excess <= 0 || cockpitWidth(name) <= cockpitPeerNameFloor {
						break
					}
					name = cockpitTruncate(p.DisplayName(), cockpitWidth(name)-excess)
				}
				return name
			}

			health := h.PeerHealthOf(p.Fingerprint)
			reach := cockpitReachDetail(health)
			name := fitName(reach)
			if cockpitWidth(assemble(name, reach)) > cockpitPeerLineWidth {
				for _, budget := range []int{cockpitReachDetailShort, cockpitReachBareWidth} {
					shorter := cockpitReachDetailFitting(health, budget)
					if shorter == "" || shorter == reach {
						continue
					}
					reach = shorter
					name = fitName(reach)
					if cockpitWidth(assemble(name, reach)) <= cockpitPeerLineWidth {
						break
					}
				}
			}

			// Order: index, name, address, reachability, selection. The two
			// growing parts come last so the line stays readable as it grows.
			fmt.Println(assemble(name, reach))
		}
	}
	fmt.Println("-------------------------")
}

// snippetRenderWidth is the column width a snippet line is wrapped to before the
// card is drawn.
//
// cockpitBox caps its content at 74 columns and pads to that, so anything wider
// is cut off at the edge -- which loses the tail of every long line rather than
// showing it on the next row. Wrapping first turns that truncation into a wrap.
const snippetRenderWidth = 74

// snippetMaxLines bounds how many rows a single drop may produce.
//
// The guard used to be a byte count, which bounded volume without bounding
// *rows*: 4096 bytes with no newlines is one 4096-column line, and on an
// 80-column terminal that is fifty-one visual rows carrying a single "│" prefix.
// A peer could still obliterate the cockpit without ever exceeding a byte cap.
const snippetMaxLines = 24

// snippetMaxBytes still bounds how much of a drop is considered at all, so a
// 10MB paste is not walked to find its line breaks.
const snippetMaxBytes = 64 << 10

// cockpitWrap breaks text into lines of at most width columns, on rune
// boundaries.
//
// Rune boundaries, not byte offsets: cutting a string by bytes can split a
// multi-byte character, and a terminal renders the halves as mojibake. The same
// reason cockpitTruncate exists.
func cockpitWrap(text string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if cockpitWidth(line) <= width {
			out = append(out, line)
			continue
		}
		var cur strings.Builder
		w := 0
		for _, r := range line {
			rw := cockpitRuneWidth(r)
			if w+rw > width {
				out = append(out, cur.String())
				cur.Reset()
				w = 0
			}
			cur.WriteRune(r)
			w += rw
		}
		out = append(out, cur.String())
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// snippetContentBounds returns the content to render, and whether anything was
// left out.
//
// The byte cut moves back to a rune boundary. The first version sliced by bytes
// (`content[:4096]`), so a drop whose 4096th byte landed inside a three-byte
// character emitted a replacement character into the user's terminal -- in the
// same file where cockpitTruncate exists precisely to prevent that.
func snippetContentBounds(content string) (string, bool) {
	if len(content) <= snippetMaxBytes {
		return content, false
	}
	cut := snippetMaxBytes
	// Step back to the start of the rune that straddles the cut.
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	return content[:cut], true
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

	// Never dump an arbitrary amount into the terminal: a careless or hostile
	// peer could scroll-bomb the cockpit. Bounded twice, because the two are not
	// the same bound -- see snippetMaxBytes and snippetMaxLines.
	content, cutBytes := snippetContentBounds(item.Content)
	lines := cockpitWrap(content, snippetRenderWidth)

	truncatedLines := false
	if len(lines) > snippetMaxLines {
		lines = lines[:snippetMaxLines]
		truncatedLines = true
	}

	rows := make([]cockpitRow, 0, len(lines)+2)
	rows = append(rows, cockpitRow{text: "📝 QuickDrop " + kindLabel + " (" + from + " • " + timeStr + ")"})
	rows = append(rows, cockpitRow{})
	for _, line := range lines {
		rows = append(rows, cockpitRow{text: line})
	}
	if cutBytes || truncatedLines {
		what := ""
		switch {
		case truncatedLines:
			what = fmt.Sprintf("%d lines shown", len(lines))
		default:
			what = fmt.Sprintf("%d bytes", len(item.Content))
		}
		rows = append(rows, cockpitRow{text: "… (" + what + ", truncated)", color: "33"})
	}

	// cockpitBox, not hand-drawn borders.
	//
	// This drew its own top and bottom rules, and they disagreed: the top was
	// sized by the label, the peer and the time, while the bottom was a fixed
	// run of dashes. A URL card rendered 74 columns against a 78-column base and
	// a card from a long-named peer rendered 90 against 78. Every QuickDrop a
	// user received came out ragged, and only agreed by accident when the
	// content happened to fit.
	//
	// cockpitBox already sizes both borders from the widest row, truncates with
	// cockpitTruncate (which cuts on rune boundaries), and prints what it builds.
	// Rendering the card found that last part: the first version of this fix
	// looped over the returned lines as well, and drew every QuickDrop twice.
	fmt.Println()
	cockpitBox(rows)
	fmt.Println()
}
