package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/bridge"
	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func runNode(args []string) {
	fs := flag.NewFlagSet("node", flag.ExitOnError)
	transportType := fs.String("transport", "lan", "Transport type: lan, loopback, or ssh (default: lan)")
	listenAddr := fs.String("listen", "", "Address to listen for incoming connections (default: 0.0.0.0:9877 for lan, 127.0.0.1:9876 for loopback)")
	sshHostKey := fs.String("ssh-host-key", "", "Path to PEM-encoded host private key (required when --transport=ssh)")
	storeDir := fs.String("store-dir", "", "Override config directory (for --transport=lan)")
	timeout := fs.Duration("timeout", 5*time.Minute, "Timeout for each session or transfer")
	outputDir := fs.String("output-dir", ".", "Directory to save received files (default: current working directory)")
	verbose := fs.Bool("v", false, "Enable verbose output")
	_ = fs.Parse(args)

	switch *transportType {
	case "lan":
		if *listenAddr == "" {
			*listenAddr = "0.0.0.0:9877"
		}
	case "loopback":
		if *listenAddr == "" {
			*listenAddr = "127.0.0.1:9876"
		}
	case "ssh":
		if *listenAddr == "" {
			*listenAddr = "127.0.0.1:9876"
		}
		if *sshHostKey == "" {
			fmt.Fprintln(os.Stderr, "Error: --ssh-host-key is required when --transport=ssh")
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown transport %q, expected lan, loopback, or ssh\n", *transportType)
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
		fmt.Fprintf(os.Stderr, "Failed to start node listener on %s: %v\n", *listenAddr, err)
		os.Exit(1)
	}
	defer listener.Close()

	fmt.Printf("🚀 tantu node active on %s (transport: %s)\n", listener.Addr().String(), *transportType)
	fmt.Printf("   ├─ OAuth: ready to handle authorization callbacks\n")
	fmt.Printf("   └─ QuickDrop: ready to receive files and text (saving to %s)\n", *outputDir)
	if *verbose {
		fmt.Printf("Verbose output enabled. Multiplexing OAuth and QuickDrop traffic on port %s\n", listener.Addr().String())
	}

	openBrowser := func(targetURL string) error {
		if *verbose {
			fmt.Printf("🌐 Opening browser for URL: %s\n", targetURL)
		}
		if err := browser.OpenURL(targetURL); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Warning: failed to open browser: %v\n", err)
			fmt.Fprintf(os.Stderr, "Please open the URL manually in your browser:\n  %s\n", targetURL)
			return err
		}
		return nil
	}

	var fileMu sync.Mutex
	openedFiles := make(map[string]*os.File)

	var nodeLogger *log.Logger
	if *verbose {
		nodeLogger = log.New(os.Stderr, "[node] ", log.LstdFlags)
	}

	cfg := bridge.DispatcherConfig{
		ASideConfig: bridge.ASideConfig{
			Timeout:     *timeout,
			OpenBrowser: openBrowser,
			Logger:      nodeLogger,
		},
		DropConfig: drop.ReceiveDropConfig{
			Timeout: *timeout,
			MaxSize: -1, // Unlimited for node transfers
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
					fileMu.Lock()
					openedFiles[meta.DropID] = f
					fileMu.Unlock()
					return f, nil
				}
				return os.Stdout, nil
			},
		},
		OnDropReceived: func(res *drop.ReceiveDropResult) {
			if res.Meta.Kind == drop.DropKindFile {
				fmt.Fprintf(os.Stderr, "📥 QuickDrop received file %q (%s)\n", res.Meta.Name, formatSize(res.BytesWritten))
			} else {
				fmt.Fprintf(os.Stderr, "\n📥 QuickDrop received text (%d bytes)\n", res.BytesWritten)
			}
		},
		OnDropDone: func(dropID string, res *drop.ReceiveDropResult, err error) {
			fileMu.Lock()
			f, ok := openedFiles[dropID]
			if ok && f != nil {
				_ = f.Close()
				delete(openedFiles, dropID)
			}
			fileMu.Unlock()

			if err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "❌ QuickDrop transfer error (%s): %v\n", dropID, err)
			}
		},
		OnASideDone: func(err error) {
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					fmt.Fprintf(os.Stderr, "❌ OAuth session error: %v\n", err)
				}
			} else {
				fmt.Fprintf(os.Stderr, "✅ OAuth session completed successfully\n")
			}
		},
		Logger: nodeLogger,
	}

	dispatcher := bridge.NewDispatcher(listener, cfg)
	if err := dispatcher.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "Node server error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\ntantu node stopped.")
}
