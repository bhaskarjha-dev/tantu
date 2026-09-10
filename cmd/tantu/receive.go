package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func runReceive(args []string) {
	fs := flag.NewFlagSet("receive", flag.ExitOnError)
	transportType := fs.String("transport", "loopback", "Transport type: loopback, ssh, or lan (default: loopback)")
	listenAddr := fs.String("listen", "127.0.0.1:9876", "Address to listen for bridge connections")
	sshHostKey := fs.String("ssh-host-key", "", "Path to PEM-encoded host private key (required when --transport=ssh)")
	storeDir := fs.String("store-dir", "", "Override config directory (for --transport=lan)")
	timeout := fs.Duration("timeout", 5*time.Minute, "Timeout for each drop session")
	verbose := fs.Bool("v", false, "Enable verbose output")
	outputDir := fs.String("output-dir", ".", "Directory to save received files (default: current working directory)")
	loopFlag := fs.Bool("loop", false, "Keep listening for more drops after each one")
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

	fmt.Fprintf(os.Stderr, "Listening for drops on %s (transport: %s)...\n", listener.Addr().String(), *transportType)
	if *verbose {
		fmt.Fprintf(os.Stderr, "Verbose output enabled. Ready to receive drops.\n")
	}

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			fmt.Fprintf(os.Stderr, "Accept error: %v\n", err)
			continue
		}

		handleConnection := func(c transport.Conn) error {
			defer c.Close()

			var openedFile *os.File
			var targetPath string

			recvCfg := drop.ReceiveDropConfig{
				Timeout: *timeout,
				OnMeta: func(meta drop.DropSend) (io.Writer, error) {
					if meta.Kind == drop.DropKindFile {
						fileName := filepath.Base(meta.Name)
						if fileName == "" || fileName == "." {
							fileName = "drop.bin"
						}
						outDir := *outputDir
						if outDir == "" {
							outDir = "."
						}
						if err := os.MkdirAll(outDir, 0755); err != nil {
							return nil, fmt.Errorf("create output directory: %w", err)
						}

						destPath := filepath.Join(outDir, fileName)
						if _, err := os.Stat(destPath); err == nil {
							ext := filepath.Ext(fileName)
							base := strings.TrimSuffix(fileName, ext)
							idx := 1
							for {
								cand := filepath.Join(outDir, fmt.Sprintf("%s (%d)%s", base, idx, ext))
								if _, err := os.Stat(cand); os.IsNotExist(err) {
									destPath = cand
									break
								}
								idx++
							}
						}

						f, err := os.Create(destPath)
						if err != nil {
							return nil, fmt.Errorf("create destination file: %w", err)
						}
						openedFile = f
						targetPath = destPath
						return f, nil
					}
					return os.Stdout, nil
				},
			}

			result, err := drop.ReceiveDrop(ctx, c, nil, recvCfg)
			if openedFile != nil {
				_ = openedFile.Close()
			}
			if err != nil {
				return err
			}

			if result.Meta.Kind == drop.DropKindFile {
				fmt.Fprintf(os.Stderr, "📥 Received file %q (%s) → %s\n", result.Meta.Name, formatSize(result.BytesWritten), targetPath)
			} else {
				fmt.Fprintf(os.Stderr, "📥 Received text (%d bytes)\n", result.BytesWritten)
			}
			return nil
		}

		if err := handleConnection(conn); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Receive error: %v\n", err)
		}

		if !*loopFlag {
			break
		}
	}
}
