package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// notifyRemovedPeers tells each just-removed peer that the pair is gone, the
// same best-effort notice the dashboard's Unpair sends. Without it a CLI
// unpair would converge only one store: the other machine would keep listing
// this one as paired until its next send was refused. Failure to notify is
// reported but never fatal - the removal already happened, and the refused
// send carries an honest reason as the fallback.
func notifyRemovedPeers(store *pairing.PeerStore, removed []pairing.Peer) {
	if len(removed) == 0 {
		return
	}
	id, err := store.LoadOrCreateIdentity()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Note: could not load identity to notify removed peers: %v\n", err)
		return
	}
	for _, p := range removed {
		name := p.DisplayName()
		if err := hub.NotifyUnpaired(context.Background(), id, p); err != nil {
			fmt.Printf("Note: could not notify %s it was unpaired (%v). Its next send will get an honest \"no longer trusted\" reason instead of a generic refusal.\n", name, err)
			continue
		}
		fmt.Printf("📣 Told %s it was unpaired.\n", name)
	}
}

func runUnpair(args []string) {
	fs := flag.NewFlagSet("unpair", flag.ExitOnError)
	name := fs.String("name", "", "Remove peer by name")
	fingerprint := fs.String("fingerprint", "", "Remove peer by fingerprint prefix")
	all := fs.Bool("all", false, "Remove all trusted peers")
	storeDir := fs.String("store-dir", "", "Override config directory (default: ~/.config/tantu)")
	_ = fs.Parse(args)

	store, err := openPeerStore(*storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	peers := store.ListPeers()

	if !*all && *name == "" && *fingerprint == "" {
		fmt.Println("Usage: tantu unpair [--name=<name> | --fingerprint=<fp> | --all]")
		if len(peers) == 0 {
			fmt.Println("\nNo paired peers found.")
		} else {
			fmt.Printf("\nCurrent paired peers (%d):\n", len(peers))
			for _, p := range peers {
				pName := p.Name
				if pName == "" {
					pName = "(unnamed)"
				}
				fpShort := p.Fingerprint
				if len(fpShort) > 12 {
					fpShort = fpShort[:12]
				}
				fmt.Printf("  - %s (%s, fp: %s)\n", pName, p.Address, fpShort)
			}
		}
		os.Exit(0)
	}

	if *all {
		if len(peers) == 0 {
			fmt.Println("No peers to remove.")
			return
		}
		fmt.Printf("Remove all %d peers? [y/N]: ", len(peers))
		var resp string
		_, _ = fmt.Scanln(&resp)
		resp = strings.ToLower(strings.TrimSpace(resp))
		if resp != "y" && resp != "yes" {
			fmt.Println("Aborted.")
			return
		}
		var removed []pairing.Peer
		for _, p := range peers {
			if err := store.RemovePeer(p.Fingerprint); err != nil {
				fmt.Fprintf(os.Stderr, "Error removing peer %s: %v\n", p.Fingerprint, err)
				continue
			}
			removed = append(removed, p)
		}
		fmt.Printf("✅ Successfully removed all %d peers.\n", len(peers))
		// Only peers whose removal actually persisted are announced; telling a
		// still-listed peer it was unpaired would split the stores the other
		// way.
		notifyRemovedPeers(store, removed)
		return
	}

	printAvailablePeers := func() {
		if len(peers) == 0 {
			fmt.Fprintln(os.Stderr, "No paired peers found.")
		} else {
			fmt.Fprintln(os.Stderr, "Available peers:")
			for _, p := range peers {
				pName := p.Name
				if pName == "" {
					pName = "(unnamed)"
				}
				fpShort := p.Fingerprint
				if len(fpShort) > 12 {
					fpShort = fpShort[:12]
				}
				fmt.Fprintf(os.Stderr, "  - %s (%s, fp: %s)\n", pName, p.Address, fpShort)
			}
		}
	}

	if *name != "" {
		var matches []pairing.Peer
		for _, p := range peers {
			// Match display name or alias, case-insensitively, consistent
			// with the peer store's own resolution tiers.
			if strings.EqualFold(p.Name, *name) || (p.Alias != "" && strings.EqualFold(p.Alias, *name)) {
				matches = append(matches, p)
			}
		}
		if len(matches) == 0 {
			fmt.Fprintf(os.Stderr, "Error: no peer found with name %q\n\n", *name)
			printAvailablePeers()
			os.Exit(1)
		}
		if len(matches) > 1 {
			fmt.Fprintf(os.Stderr, "Error: multiple peers found with name %q. Use --fingerprint to specify:\n", *name)
			for _, m := range matches {
				fmt.Fprintf(os.Stderr, "  - %s (%s, fp: %s)\n", m.Name, m.Address, m.Fingerprint)
			}
			os.Exit(1)
		}
		target := matches[0]
		if err := store.RemovePeer(target.Fingerprint); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to remove peer: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Successfully removed peer %q (%s)\n", target.Name, target.Fingerprint)
		notifyRemovedPeers(store, []pairing.Peer{target})
		return
	}

	if *fingerprint != "" {
		fpTarget := strings.ToLower(strings.TrimSpace(*fingerprint))
		// A fingerprint prefix shorter than 6 hex characters is not
		// distinctive: with a single paired peer even one character would
		// match, turning a typo into silent trust removal. The 6-character
		// minimum mirrors the peer store's own prefix-resolution rule.
		if len(fpTarget) < 6 {
			fmt.Fprintf(os.Stderr, "Error: fingerprint prefix must be at least 6 characters (got %d)\n", len(fpTarget))
			os.Exit(1)
		}
		var matches []pairing.Peer
		for _, p := range peers {
			if strings.HasPrefix(strings.ToLower(p.Fingerprint), fpTarget) {
				matches = append(matches, p)
			}
		}
		if len(matches) == 0 {
			fmt.Fprintf(os.Stderr, "Error: no peer found matching fingerprint prefix %q\n\n", *fingerprint)
			printAvailablePeers()
			os.Exit(1)
		}
		if len(matches) > 1 {
			fmt.Fprintf(os.Stderr, "Error: multiple peers match fingerprint prefix %q. Please provide more characters:\n", *fingerprint)
			for _, m := range matches {
				fmt.Fprintf(os.Stderr, "  - %s (%s, fp: %s)\n", m.Name, m.Address, m.Fingerprint)
			}
			os.Exit(1)
		}
		target := matches[0]
		if err := store.RemovePeer(target.Fingerprint); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to remove peer: %v\n", err)
			os.Exit(1)
		}
		pName := target.Name
		if pName == "" {
			pName = "(unnamed)"
		}
		fmt.Printf("✅ Successfully removed peer %s (fp: %s)\n", pName, target.Fingerprint)
		notifyRemovedPeers(store, []pairing.Peer{target})
		return
	}
}
