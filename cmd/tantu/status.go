package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
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

	fmt.Printf("tantu v%s\n", strings.TrimPrefix(version, "v"))

	dir := *storeDir
	if dir == "" {
		if def, derr := pairing.DefaultStoreDir(); derr == nil {
			dir = def
		}
	}
	if dir != "" {
		fmt.Printf("Store: %s\n", dir)
	}

	// Hub liveness first: most user-facing state (dashboard URL, transport)
	// lives with the running Hub, and a stale hub.json must read as
	// "not running" rather than surfacing a dead endpoint.
	if live, ok := hub.ProbeHubWithStoreDir("", *storeDir); ok {
		noteHubVersionSkew(live)
		dash := live.WebAddr
		if dash != "" && !strings.Contains(dash, "://") {
			dash = "http://" + dash
		}
		fmt.Printf("Hub: running (dashboard %s, transport %s)\n", dash, live.Transport)
		fmt.Println("Dashboard access: tantu dashboard (mints a fresh authenticated link)")
	} else {
		fmt.Println("Hub: not running (start with `tantu` or `tantu hub`)")
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
		pName := p.DisplayName()
		marker := ""
		if p.IsDefault {
			marker += " [default]"
		}
		sas := pairing.SASCode(p.Fingerprint)
		timeStr := formatRelativeTime(p.FirstSeen)
		fmt.Printf("  %-18s  %-21s  (SAS: %s, paired %s)%s\n", pName, p.Address, sas, timeStr, marker)
	}
}
