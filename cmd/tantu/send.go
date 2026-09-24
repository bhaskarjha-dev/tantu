package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"flag"
	"fmt"
	"hash"
	"io"
	"mime"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

func runSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	transportType := fs.String("transport", "", "Transport type: loopback, ssh, or lan (default: auto)")
	bridgeAddr := fs.String("bridge", "127.0.0.1:9876", "Bridge server address for loopback (default: 127.0.0.1:9876)")
	peerAddr := fs.String("peer", "", "Address of paired peer HOST:PORT (optional when exactly 1 peer paired)")
	storeDir := fs.String("store-dir", "", "Override config directory (for --transport=lan)")
	sshHost := fs.String("ssh-host", "", "SSH server hostname (required when --transport=ssh)")
	sshPort := fs.Int("ssh-port", 22, "SSH server port (default: 22)")
	sshUser := fs.String("ssh-user", defaultUsername(), "SSH username (default: current OS user)")
	sshKey := fs.String("ssh-key", "", "Path to PEM private key for SSH auth (required when --transport=ssh)")
	sshKnownHosts := fs.String("ssh-known-hosts", "", "Path to OpenSSH known_hosts for server verification (default: ~/.ssh/known_hosts)")
	sshFingerprint := fs.String("ssh-fingerprint", "", "Pin the server host key (format: SHA256:...); overrides known_hosts when set")
	timeout := fs.Duration("timeout", 5*time.Minute, "Timeout waiting for drop transfer to complete")
	verbose := fs.Bool("v", false, "Enable verbose output")
	nameFlag := fs.String("name", "", "Override filename or text label")
	_ = fs.Parse(args)

	transportExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "transport" {
			transportExplicit = true
		}
	})

	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintln(os.Stderr, "Error: missing content or file to send")
		fmt.Fprintln(os.Stderr, "Usage: tantu send [flags] <text|file|->")
		os.Exit(1)
	}

	firstArg := rest[0]
	var payload io.Reader
	var meta drop.DropSend
	var closeAfter func()
	var cleanPath string
	var isFile bool
	var textContent string

	if firstArg == "-" {
		// Read at most the standalone text-drop limit plus one byte so an
		// unbounded stdin producer cannot force an unbounded allocation.
		data, err := io.ReadAll(io.LimitReader(os.Stdin, standaloneTextDropLimit+1))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: reading from stdin: %v\n", err)
			os.Exit(1)
		}
		if int64(len(data)) > standaloneTextDropLimit {
			fmt.Fprintln(os.Stderr, "Error: stdin text exceeds 10MB limit")
			os.Exit(1)
		}
		label := *nameFlag
		if label == "" {
			label = "stdin"
		}
		meta = drop.DropSend{
			Kind: drop.DropKindText,
			Name: label,
			Size: int64(len(data)),
		}
		textContent = string(data)
		payload = bytes.NewReader(data)
	} else {
		// Detect file vs text
		cleanPath = firstArg
		if strings.HasPrefix(cleanPath, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				cleanPath = filepath.Join(home, cleanPath[1:])
			}
		}

		hasPathHint := strings.HasPrefix(firstArg, "./") || strings.HasPrefix(firstArg, ".\\") ||
			strings.HasPrefix(firstArg, "/") || strings.HasPrefix(firstArg, "\\") ||
			strings.HasPrefix(firstArg, "~") || strings.Contains(firstArg, "/") ||
			strings.Contains(firstArg, "\\")

		stat, err := os.Stat(cleanPath)
		if err == nil && !stat.IsDir() {
			if hasPathHint || len(rest) == 1 {
				isFile = true
			}
		}

		if isFile {
			f, err := os.Open(cleanPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: cannot open file %s: %v\n", cleanPath, err)
				os.Exit(1)
			}
			closeAfter = func() { _ = f.Close() }

			size := stat.Size()
			if size > 50*1024*1024 && *verbose {
				fmt.Printf("📦 Large file (%s): streaming transfer in progress...\n", formatSize(size))
			}

			fileName := *nameFlag
			if fileName == "" {
				fileName = filepath.Base(cleanPath)
			}
			mimeType := mime.TypeByExtension(filepath.Ext(fileName))

			meta = drop.DropSend{
				Kind:     drop.DropKindFile,
				Name:     fileName,
				Size:     size,
				MIMEType: mimeType,
			}
			payload = f
		} else {
			// Text mode. Command-line text shares the 10MB text-drop limit
			// enforced on stdin and HTTP ingress; without it an unbounded
			// argv allocation reaches the wire only to be rejected there.
			text := strings.Join(rest, "")
			if len(rest) > 1 {
				text = strings.Join(rest, " ")
			}
			if int64(len(text)) > standaloneTextDropLimit {
				fmt.Fprintln(os.Stderr, "Error: text exceeds 10MB limit")
				os.Exit(1)
			}
			meta = drop.DropSend{
				Kind: drop.DropKindText,
				Name: *nameFlag,
				Size: int64(len(text)),
			}
			textContent = text
			payload = strings.NewReader(text)
		}
	}
	if closeAfter != nil {
		defer closeAfter()
	}

	// 1. If transport is not explicitly overridden to SSH, probe local Hub with strict 200ms timeout
	if (!transportExplicit || *transportType == "loopback") && *sshHost == "" {
		if status, ok := hub.ProbeHubWithStoreDir(*bridgeAddr, *storeDir); ok {
			if *verbose {
				fmt.Printf("Connected to local Hub (v%s, %s transport)\n", status.Version, status.Transport)
			}
			delegateAddr := *bridgeAddr
			if status.WebAddr != "" {
				delegateAddr = status.WebAddr
			}
			var err error
			delegatedFilePath := ""
			if isFile {
				delegatedFilePath = cleanPath
				if *verbose {
					fmt.Printf("Delegating file transfer of %q to local Hub...\n", meta.Name)
				}
			} else if *verbose {
				fmt.Printf("Delegating text transfer to local Hub...\n")
			}
			err = delegateSendWithNameFromStore(*storeDir, delegateAddr, delegatedFilePath, textContent, meta.Name, *peerAddr, *timeout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "❌ Hub delegation failed: %v\n", err)
				os.Exit(1)
			}
			if isFile {
				fmt.Printf("✅ Sent file %q (%s) via local Hub\n", meta.Name, formatSize(meta.Size))
			} else {
				fmt.Printf("✅ Sent text (%d bytes) via local Hub\n", meta.Size)
			}
			return
		}
	}

	// Supplying SSH connection options is an explicit request for SSH when the
	// caller did not spell out --transport. Do this after the Hub probe so a
	// local Hub is never silently preferred for an SSH invocation.
	if !transportExplicit && strings.TrimSpace(*sshHost) != "" {
		*transportType = "ssh"
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
	var expectedFingerprint string
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
			User:               *sshUser,
			Host:               *sshHost,
			Port:               *sshPort,
			PrivateKey:         keyBytes,
			KnownHostsFile:     *sshKnownHosts,
			HostKeyFingerprint: *sshFingerprint,
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
			// Live store lookup in addition to the snapshot: a peer removed
			// between ListPeers and Dial must not remain dialable.
			IsTrusted: lanStore.IsTrusted,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to configure LAN transport: %v\n", err)
			os.Exit(1)
		}
		dialTarget = *peerAddr
		if resolvedPeer, resolveErr := resolvePeerForDial(lanStore, *peerAddr); resolveErr != nil {
			fmt.Fprintf(os.Stderr, "Error: LAN target is not a trusted paired peer: %v\n", resolveErr)
			os.Exit(1)
		} else {
			expectedFingerprint = resolvedPeer.Fingerprint
		}
	default:
		tr = transport.NewLoopbackTransport()
		dialTarget = *bridgeAddr
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *verbose {
		fmt.Printf("Connecting to %s (transport: %s)...\n", dialTarget, *transportType)
	}

	conn, err := transport.DialPinned(tr, dialTarget, expectedFingerprint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to peer at %s: %v\n", dialTarget, err)
		os.Exit(1)
	}
	defer conn.Close()

	if *verbose {
		fmt.Println("Connection established. Sending drop...")
	}

	var hasher hash.Hash
	if meta.Kind == drop.DropKindFile {
		if seeker, ok := payload.(io.ReadSeeker); ok && meta.Size >= 64*1024 {
			headBuf := make([]byte, 64*1024)
			n, rErr := io.ReadFull(seeker, headBuf)
			if n > 0 && (rErr == nil || rErr == io.EOF || rErr == io.ErrUnexpectedEOF) {
				h := sha256.Sum256(headBuf[:n])
				meta.HeadHash = hex.EncodeToString(h[:])
			}
			_, _ = seeker.Seek(0, io.SeekStart)
		}
		if seeker, ok := payload.(io.ReadSeeker); ok {
			hasher = sha256.New()
			payload = &trackingFilePayload{
				seeker: seeker,
				hasher: hasher,
				onResume: func(offset int64) {
					fmt.Printf("➜ Resuming transfer from %s (offset %d)...\n", formatBytes(offset), offset)
				},
			}
		}
	}

	sendCfg := drop.SendDropConfig{Timeout: *timeout}
	if err := drop.SendDrop(ctx, conn, meta, payload, sendCfg); err != nil {
		fmt.Fprintf(os.Stderr, "❌ QuickDrop failed: %v\n", err)
		os.Exit(1)
	}

	if meta.Kind == drop.DropKindFile {
		if hasher != nil {
			computedSHA := hex.EncodeToString(hasher.Sum(nil))
			fmt.Printf("✅ Drop sent & verified (SHA-256: %s)\n", computedSHA)
		} else {
			fmt.Printf("✅ Sent file %q (%s)\n", meta.Name, formatSize(meta.Size))
		}
	} else {
		fmt.Printf("✅ Sent text (%d bytes)\n", meta.Size)
	}
}

type trackingFilePayload struct {
	seeker        io.ReadSeeker
	hasher        hash.Hash
	onResume      func(offset int64)
	resumedOffset int64
}

func (t *trackingFilePayload) Read(p []byte) (int, error) {
	n, err := t.seeker.Read(p)
	if n > 0 && t.hasher != nil {
		t.hasher.Write(p[:n])
	}
	return n, err
}

func (t *trackingFilePayload) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekStart && offset > 0 && t.resumedOffset == 0 {
		t.resumedOffset = offset
		if t.onResume != nil {
			t.onResume(offset)
		}
	}
	return t.seeker.Seek(offset, whence)
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d bytes", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
