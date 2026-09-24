package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/hub"
)

// runDashboard opens an authenticated local dashboard without requiring an
// interactive cockpit. This is the recovery path for headless/systemd/Docker
// deployments and for a second terminal on a machine whose dashboard tab was
// closed.
func runDashboard(args []string) {
	fs := flag.NewFlagSet("dashboard", flag.ExitOnError)
	storeDir := fs.String("store-dir", "", "Override config directory (default: platform config directory)")
	bridgeAddr := fs.String("bridge", "", "Optional local dashboard address override")
	printOnly := fs.Bool("print", false, "Print the one-time dashboard URL without opening a browser")
	_ = fs.Parse(NormalizeArgs(args))

	status, ok := hub.ProbeHubWithStoreDir(*bridgeAddr, *storeDir)
	if !ok || status == nil || strings.TrimSpace(status.WebAddr) == "" {
		fmt.Fprintln(os.Stderr, "Error: no running Tantu Hub found. Start one with `tantu` or `tantu hub`.")
		os.Exit(1)
	}

	noteHubVersionSkew(status)
	dashboardURL, err := hub.DashboardURLFromRuntime(status.WebAddr, *storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not obtain an authenticated dashboard URL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Tantu dashboard: %s\n", dashboardURL)
	if *printOnly {
		return
	}
	if err := browser.OpenURL(dashboardURL); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open browser: %v\n", err)
		fmt.Fprintln(os.Stderr, "The authenticated URL is printed above; open it manually.")
		os.Exit(1)
	}
}
