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
	sshAuthorizedKey := fs.String("ssh-authorized-key", "", "Path to an authorized SSH client public/private key")
	sshAllowLegacy := fs.Bool("ssh-allow-legacy-host-key", false, "Explicitly authorize the SSH host key for clients (legacy compatibility)")
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
		var authorizedKeys [][]byte
		if strings.TrimSpace(*sshAuthorizedKey) != "" {
			authorized, readErr := os.ReadFile(*sshAuthorizedKey)
			if readErr != nil {
				fmt.Fprintf(os.Stderr, "Error: cannot read SSH authorized key: %v\n", readErr)
				os.Exit(1)
			}
			authorizedKeys = append(authorizedKeys, authorized)
		}
		var sshErr error
		tr, sshErr = transport.NewSSHTransport(transport.SSHTransportConfig{
			HostKey:                keyBytes,
			AuthorizedKeys:         authorizedKeys,
			AllowLegacyHostKeyAuth: *sshAllowLegacy,
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
			IsTrusted:           store.IsTrusted,
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
	if removed, _ := drop.SweepStalePartials(*outputDir, drop.DefaultStagingMaxAge); removed > 0 && *verbose {
		fmt.Printf("Cleaned %d stale partial file(s)\n", removed)
	}
	if *verbose {
		fmt.Printf("Verbose output enabled. Multiplexing OAuth and QuickDrop traffic on port %s\n", listener.Addr().String())
	}

	openBrowser := func(targetURL string) error {
		if *verbose {
			fmt.Printf("🌐 Opening browser for URL: %s\n", browser.RedactURL(targetURL))
		}
		if err := browser.OpenURL(targetURL); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Warning: failed to open browser: %v\n", err)
			fmt.Fprintln(os.Stderr, "Please reopen the original authorization URL manually; Tantu will not print its sensitive query values.")
			return err
		}
		return nil
	}

	var fileMu sync.Mutex
	openedFiles := make(map[string]*os.File)
	partPaths := make(map[string]string)
	finalPaths := make(map[string]string)
	reservedDrops := make(map[string]struct{})

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
			Timeout:     *timeout,
			MaxSize:     drop.DefaultMaxDropSize,
			MaxTextSize: standaloneTextDropLimit,
			OnMeta: func(meta drop.DropSend) (io.Writer, error) {
				fileMu.Lock()
				if _, exists := reservedDrops[meta.DropID]; exists {
					fileMu.Unlock()
					return nil, fmt.Errorf("duplicate drop_id %q", meta.DropID)
				}
				reservedDrops[meta.DropID] = struct{}{}
				fileMu.Unlock()
				reservationHeld := true
				defer func() {
					if reservationHeld {
						fileMu.Lock()
						delete(reservedDrops, meta.DropID)
						fileMu.Unlock()
					}
				}()

				if meta.Kind == drop.DropKindFile {
					fileName := sanitizeIncomingFilename(meta.Name)
					outDir := *outputDir
					if outDir == "" {
						outDir = "."
					}
					f, partPath, finalPath, err := createIncomingPart(outDir, fileName)
					if err != nil {
						return nil, err
					}
					fileMu.Lock()
					openedFiles[meta.DropID] = f
					partPaths[meta.DropID] = partPath
					finalPaths[meta.DropID] = finalPath
					fileMu.Unlock()
					reservationHeld = false
					return f, nil
				}
				reservationHeld = false
				return os.Stdout, nil
			},
			BeforeComplete: func(res *drop.ReceiveDropResult) error {
				if res.Meta.Kind != drop.DropKindFile {
					return nil
				}
				fileMu.Lock()
				f := openedFiles[res.Meta.DropID]
				partPath := partPaths[res.Meta.DropID]
				desiredPath := finalPaths[res.Meta.DropID]
				fileMu.Unlock()
				if partPath == "" || desiredPath == "" {
					return errors.New("completed file has no staging path")
				}
				published, err := finalizeIncomingPart(f, partPath, desiredPath)
				if err != nil {
					return fmt.Errorf("publish received file: %w", err)
				}
				fileMu.Lock()
				finalPaths[res.Meta.DropID] = published
				fileMu.Unlock()
				return nil
			},
		},
		OnDropReceived: func(res *drop.ReceiveDropResult) {
			if res.Meta.Kind == drop.DropKindFile {
				fileMu.Lock()
				file := openedFiles[res.Meta.DropID]
				finalPath := finalPaths[res.Meta.DropID]
				if file != nil {
					_ = file.Close()
					delete(openedFiles, res.Meta.DropID)
				}
				fileMu.Unlock()
				if finalPath != "" {
					fmt.Fprintf(os.Stderr, "📥 QuickDrop received file %q (%s) → %s\n", res.Meta.Name, formatSize(res.BytesWritten), finalPath)
				} else {
					fmt.Fprintf(os.Stderr, "📥 QuickDrop received file %q (%s)\n", res.Meta.Name, formatSize(res.BytesWritten))
				}
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
			delete(partPaths, dropID)
			delete(finalPaths, dropID)
			delete(reservedDrops, dropID)
			fileMu.Unlock()

			if err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "❌ QuickDrop transfer error (%s): %v\n", dropID, err)
			}
		},
		OnASideDone: func(err error) {
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					fmt.Fprintf(os.Stderr, "❌ OAuth session error: %s\n", bridge.RedactError(err))
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
