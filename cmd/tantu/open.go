package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func runOpen(args []string) {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	transportType := fs.String("transport", "", "Transport type: loopback, ssh, or lan (default: auto)")
	bridgeAddr := fs.String("bridge", "127.0.0.1:9876", "Bridge server address for loopback (default: 127.0.0.1:9876)")
	peerAddr := fs.String("peer", "", "Address of paired peer HOST:PORT (optional when exactly 1 peer paired)")
	storeDir := fs.String("store-dir", "", "Override config directory (for --transport=lan)")
	sshHost := fs.String("ssh-host", "", "SSH server hostname (required when --transport=ssh)")
	sshPort := fs.Int("ssh-port", 22, "SSH server port (default: 22)")
	sshUser := fs.String("ssh-user", defaultUsername(), "SSH username (default: current OS user)")
	sshKey := fs.String("ssh-key", "", "Path to PEM private key for SSH auth (required when --transport=ssh)")
	timeout := fs.Duration("timeout", 5*time.Minute, "Timeout waiting for authentication flow to complete")
	verbose := fs.Bool("v", false, "Enable verbose output")
	_ = fs.Parse(args)

	transportExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "transport" {
			transportExplicit = true
		}
	})

	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintln(os.Stderr, "Error: missing OAuth authorization URL")
		fmt.Fprintln(os.Stderr, "Usage: tantu open [flags] <url>")
		os.Exit(1)
	}
	oauthURL := rest[0]
	if oauthURL == "-" {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			oauthURL = strings.TrimSpace(scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: reading URL from stdin: %v\n", err)
			os.Exit(1)
		}
		if oauthURL == "" {
			fmt.Fprintln(os.Stderr, "Error: no URL received on stdin")
			os.Exit(1)
		}
	}

	// 1. If transport is not explicitly overridden to SSH, probe local Hub with strict 200ms timeout
	if (!transportExplicit || *transportType == "loopback") && *sshHost == "" {
		if status, ok := hub.ProbeHub(*bridgeAddr); ok {
			if *verbose {
				fmt.Printf("Connected to local Hub (v%s, %s transport)\n", status.Version, status.Transport)
			}
			if *verbose {
				fmt.Println("Delegating OAuth authorization URL to local Hub...")
			}
			if err := hub.DelegateOpen(*bridgeAddr, oauthURL, *peerAddr); err != nil {
				fmt.Fprintf(os.Stderr, "❌ Hub delegation failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✅ Authentication completed successfully.")
			return
		}
	}

	// 2. Fallback to direct standalone transport execution using Smart Default Transport
	var lanStore *pairing.PeerStore
	if !transportExplicit || *transportType == "" {
		var err error
		lanStore, err = openPeerStore(*storeDir)
		if err == nil {
			*transportType = defaultTransport(lanStore)
		} else {
			*transportType = "loopback"
		}
	}

	switch *transportType {
	case "loopback":
	case "ssh":
		if *sshHost == "" {
			fmt.Fprintln(os.Stderr, "Error: --ssh-host is required when --transport=ssh")
			os.Exit(1)
		}
		if *sshKey == "" {
			fmt.Fprintln(os.Stderr, "Error: --ssh-key is required when --transport=ssh")
			os.Exit(1)
		}
	case "lan":
		if lanStore == nil {
			var err error
			lanStore, err = openPeerStore(*storeDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
		resolved, err := resolvePeer(lanStore, *peerAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		*peerAddr = resolved
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown transport %q, expected loopback, ssh, or lan\n", *transportType)
		os.Exit(1)
	}

	var dialTarget string
	var tr transport.Transport
	switch *transportType {
	case "ssh":
		keyBytes, err := os.ReadFile(*sshKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read SSH key: %s: %v\n", *sshKey, err)
			os.Exit(1)
		}
		var sshErr error
		tr, sshErr = transport.NewSSHTransport(transport.SSHTransportConfig{
			User:       *sshUser,
			Host:       *sshHost,
			Port:       *sshPort,
			PrivateKey: keyBytes,
		})
		if sshErr != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to configure SSH transport: %v\n", sshErr)
			os.Exit(1)
		}
		dialTarget = net.JoinHostPort(*sshHost, strconv.Itoa(*sshPort))
	case "lan":
		id, err := lanStore.LoadIdentity()
		if err != nil || id == nil {
			fmt.Fprintln(os.Stderr, "Error: No identity found. Run 'tantu pair' first.")
			os.Exit(1)
		}
		peers := lanStore.ListPeers()
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
		dialTarget = *peerAddr
	default:
		tr = transport.NewLoopbackTransport()
		dialTarget = *bridgeAddr
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("🔗 Connecting to bridge at %s (transport: %s)...\n", dialTarget, *transportType)
	if *verbose {
		fmt.Printf("Target OAuth URL: %s\n", oauthURL)
		fmt.Printf("Session timeout: %v\n", *timeout)
	}

	conn, err := tr.Dial(dialTarget)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to bridge server at %s: %v\n", dialTarget, err)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Println("Connected. Sending URL to bridge...")
	if *verbose {
		fmt.Println("Handshake established with bridge server.")
	}

	fmt.Println("📥 Waiting for callback...")
	cfg := bridge.BSideConfig{Timeout: *timeout}
	if err := bridge.HandleBSide(ctx, conn, oauthURL, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Authentication bridge failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Authentication completed successfully.")
}
