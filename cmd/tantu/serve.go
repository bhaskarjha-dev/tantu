package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	transportType := fs.String("transport", "loopback", "Transport type: loopback, ssh, or lan (default: loopback)")
	listenAddr := fs.String("listen", "127.0.0.1:9876", "Address to listen for bridge connections")
	sshHostKey := fs.String("ssh-host-key", "", "Path to PEM-encoded host private key (required when --transport=ssh)")
	storeDir := fs.String("store-dir", "", "Override config directory (for --transport=lan)")
	timeout := fs.Duration("timeout", 5*time.Minute, "Timeout for each authentication session")
	verbose := fs.Bool("v", false, "Enable verbose output")
	_ = fs.Parse(args)

	switch *transportType {
	case "loopback":
	case "ssh":
		if *sshHostKey == "" {
			fmt.Fprintln(os.Stderr, "Error: --ssh-host-key is required when --transport=ssh")
			os.Exit(1)
		}
	case "lan":
		if *listenAddr == "127.0.0.1:9876" {
			*listenAddr = "0.0.0.0:9877"
		}
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown transport %q, expected loopback, ssh, or lan\n", *transportType)
		os.Exit(1)
	}

	var tr transport.Transport
	switch *transportType {
	case "ssh":
		keyBytes, err := os.ReadFile(*sshHostKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read SSH key: %s: %v\n", *sshHostKey, err)
			os.Exit(1)
		}
		var sshErr error
		tr, sshErr = transport.NewSSHTransport(transport.SSHTransportConfig{
			HostKey: keyBytes,
		})
		if sshErr != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to configure SSH transport: %v\n", sshErr)
			os.Exit(1)
		}
	case "lan":
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
			fmt.Fprintf(os.Stderr, "Error: cannot open peer store: %v\n", err)
			os.Exit(1)
		}
		id, err := store.LoadIdentity()
		if err != nil || id == nil {
			fmt.Fprintln(os.Stderr, "Error: No identity found. Run 'tantu pair' first.")
			os.Exit(1)
		}
		peers := store.ListPeers()
		if len(peers) == 0 {
			fmt.Fprintln(os.Stderr, "Error: No trusted peers. Run 'tantu pair' to pair with a remote machine.")
			os.Exit(1)
		}
		var trustedFPs []string
		for _, p := range peers {
			trustedFPs = append(trustedFPs, p.Fingerprint)
		}
		tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid local TLS identity: %v\n", err)
			os.Exit(1)
		}
		tr, err = transport.NewLANTransport(transport.LANTransportConfig{
			Cert:                tlsCert,
			TrustedFingerprints: trustedFPs,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to configure LAN transport: %v\n", err)
			os.Exit(1)
		}
	default:
		tr = transport.NewLoopbackTransport()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := tr.Listen(*listenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start listener on %s: %v\n", *listenAddr, err)
		os.Exit(1)
	}
	defer listener.Close()

	fmt.Printf("Listening on %s (transport: %s, timeout: %v)...\n", listener.Addr().String(), *transportType, *timeout)
	if *verbose {
		fmt.Printf("Verbose output enabled. A-side bridge listener active on %s\n", listener.Addr().String())
	}

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	var activeSessions atomic.Int64

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				// Clean shutdown
				break
			}
			fmt.Fprintf(os.Stderr, "Accept error: %v\n", err)
			continue
		}

		go func() {
			defer conn.Close()
			active := activeSessions.Add(1)
			defer activeSessions.Add(-1)

			sessionID := generateSessionID()
			sessionPrefix := fmt.Sprintf("[session-%s] ", sessionID)
			sessionLogger := log.New(os.Stdout, sessionPrefix, 0)

			remoteAddr := conn.RemoteAddr().String()
			sessionLogger.Printf("🔗 Session %s started (%d active) from %s", sessionID, active, remoteAddr)
			if *verbose {
				sessionLogger.Printf("Starting A-side session for %s", remoteAddr)
			}

			openBrowser := func(targetURL string) error {
				if *verbose {
					sessionLogger.Printf("🌐 Opening browser for URL: %s", targetURL)
				}
				if err := browser.OpenURL(targetURL); err != nil {
					sessionLogger.Printf("❌ Warning: failed to open browser: %v", err)
					sessionLogger.Printf("Please open the URL manually in your browser:\n  %s", targetURL)
					return err
				}
				return nil
			}

			cfg := bridge.ASideConfig{
				Timeout:     *timeout,
				OpenBrowser: openBrowser,
				Logger:      sessionLogger,
			}
			if err := bridge.HandleASide(ctx, conn, cfg); err != nil && !errors.Is(err, context.Canceled) {
				sessionLogger.Printf("❌ Session error: %v", err)
				return
			}
			sessionLogger.Printf("✅ Session complete")
		}()
	}

	fmt.Println("tantu server stopped.")
}
