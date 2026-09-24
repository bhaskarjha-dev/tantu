package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
)

func runHub(args []string) {
	fs := flag.NewFlagSet("hub", flag.ExitOnError)
	server := fs.Bool("server", false, "Run in headless server mode (do not open browser)")
	headless := fs.Bool("headless", false, "Run in headless mode (alias for --server)")
	transportType := fs.String("transport", "", "Transport type: lan, loopback, or ssh (default: auto-detected from peers)")
	listenAddr := fs.String("listen", "", "P2P listener address (default: 0.0.0.0:9877 for lan, 127.0.0.1:9877 for loopback)")
	webAddr := fs.String("web-addr", "127.0.0.1:9876", "Web dashboard & IPC address")
	webPort := fs.Int("web-port", 0, "Web dashboard port (overrides port in --web-addr if non-zero)")
	storeDir := fs.String("store-dir", "", "Configuration directory")
	outputDir := fs.String("output-dir", "", "Directory to save received QuickDrop files (default: private Downloads/tantu directory)")
	timeout := fs.Duration("timeout", 5*time.Minute, "Timeout for sessions and transfers")
	verbose := fs.Bool("v", false, "Enable verbose output")
	sshHostKey := fs.String("ssh-host-key", "", "Path to PEM-encoded host private key (when --transport=ssh)")
	sshAuthorizedKey := fs.String("ssh-authorized-key", "", "Path to an authorized SSH client public/private key (repeat by using a comma-separated list is not supported)")
	sshAllowLegacy := fs.Bool("ssh-allow-legacy-host-key", false, "Explicitly authorize the SSH host key for clients (legacy compatibility)")
	autoPair := fs.Bool("auto-pair", false, "Automatically accept incoming pairing requests (default: false, requires user confirmation)")
	normalizedArgs := NormalizeArgs(args)
	_ = fs.Parse(normalizedArgs)

	targetWebAddr := *webAddr
	if *webPort > 0 {
		targetWebAddr = fmt.Sprintf("127.0.0.1:%d", *webPort)
	}

	var sshAuthorizedKeys [][]byte
	if strings.TrimSpace(*sshAuthorizedKey) != "" {
		keyBytes, readErr := os.ReadFile(*sshAuthorizedKey)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read SSH authorized key: %v\n", readErr)
			os.Exit(1)
		}
		sshAuthorizedKeys = append(sshAuthorizedKeys, keyBytes)
	}

	cfg := hub.HubConfig{
		TransportType:             *transportType,
		ListenAddr:                *listenAddr,
		WebAddr:                   targetWebAddr,
		StoreDir:                  *storeDir,
		OutputDir:                 *outputDir,
		Headless:                  *headless || *server,
		Server:                    *server || *headless,
		Timeout:                   *timeout,
		Verbose:                   *verbose,
		SSHHostKey:                *sshHostKey,
		SSHAuthorizedKeys:         sshAuthorizedKeys,
		SSHAllowLegacyHostKeyAuth: *sshAllowLegacy,
		AutoAcceptPairing:         *autoPair,
	}

	h, err := hub.NewHub(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to initialize Hub: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		select {
		case <-h.Ready():
			if !cfg.Headless && !cfg.Server {
				RunCockpit(ctx, h, stop, *verbose)
			} else {
				// Headless mode has no cockpit from which to request a fresh
				// authenticated link. Print the one-time bootstrap URL itself;
				// a bare /api-protected dashboard address is not usable without
				// it and was a particularly confusing first-run experience.
				dashboardURL := h.DashboardURL()
				if dashboardURL == "" {
					dashboardURL = "http://" + h.WebAddr()
				}
				fmt.Printf("🚀 tantu hub active (headless server mode)\n")
				fmt.Printf("   ├─ Web Dashboard: %s\n", dashboardURL)
				fmt.Printf("   │  (one-time link; use `tantu dashboard` to mint another)\n")
				if h.P2PAddr() != nil {
					fmt.Printf("   └─ P2P Listener:  %s (transport: %s)\n", h.P2PAddr().String(), h.TransportType())
				}
			}
		case <-ctx.Done():
		}
	}()

	if err := h.Start(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "Hub error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\ntantu hub stopped.")
}
