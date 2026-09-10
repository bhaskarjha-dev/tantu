package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	if d < 0 {
		return "just now"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%dh ago", h)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	default:
		weeks := int(d.Hours() / (24 * 7))
		return fmt.Sprintf("%dw ago", weeks)
	}
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	storeDir := fs.String("store-dir", "", "Override config directory (default: ~/.config/tantu)")
	_ = fs.Parse(args)

	store, err := openPeerStore(*storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	id, err := store.LoadIdentity()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading identity: %v\n", err)
		os.Exit(1)
	}

	if id == nil {
		fmt.Println("No identity configured. Run 'tantu pair' to generate one.")
	} else {
		sas := pairing.SASCode(id.Fingerprint)
		fmt.Printf("Identity: %s (SAS: %s)\n", id.Fingerprint, sas)
	}

	peers := store.ListPeers()
	if len(peers) == 0 {
		fmt.Println("\nNo paired peers.")
		return
	}

	fmt.Printf("\nTrusted Peers (%d):\n", len(peers))
	for _, p := range peers {
		pName := p.Name
		if pName == "" {
			pName = "(unnamed)"
		}
		sas := pairing.SASCode(p.Fingerprint)
		timeStr := formatRelativeTime(p.FirstSeen)
		fmt.Printf("  %-12s  %-21s  (SAS: %s, paired %s)\n", pName, p.Address, sas, timeStr)
	}
}
