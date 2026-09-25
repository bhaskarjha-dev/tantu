package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// send exit codes distinguish outcomes for automation: 0 success,
// 1 transfer failure, 2 invalid input or usage, 3 unknown outcome with
// duplicate risk (check the receiver inbox before resending).
const (
	sendExitOK            = 0
	sendExitFailed        = 1
	sendExitUsage         = 2
	sendExitDuplicateRisk = 3
)

// sendJSONResult is the machine-readable `tantu send --json` contract.
// Payload content is never included.
type sendJSONResult struct {
	Status             string `json:"status"`
	OperationID        string `json:"operation_id,omitempty"`
	Kind               string `json:"kind,omitempty"`
	Name               string `json:"name,omitempty"`
	Size               int64  `json:"size,omitempty"`
	Destination        string `json:"destination,omitempty"`
	DestinationAddress string `json:"destination_address,omitempty"`
	Verified           bool   `json:"verified,omitempty"`
	Code               string `json:"code,omitempty"`
	Message            string `json:"message,omitempty"`
	RetrySafe          bool   `json:"retry_safe,omitempty"`
	DuplicateRisk      bool   `json:"duplicate_risk,omitempty"`
	DataSafe           bool   `json:"data_safe,omitempty"`
	NextAction         string `json:"next_action,omitempty"`
}

func emitSendJSON(res sendJSONResult, exitCode int) {
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(res)
	os.Exit(exitCode)
}

// sendExitForHubError maps a delegation failure to an exit code using the
// Hub's structured contract when available.
func sendExitForHubError(err error) int {
	var he *hubError
	if errors.As(err, &he) {
		if he.DuplicateRisk {
			return sendExitDuplicateRisk
		}
		switch he.Code {
		case "invalid_input", "limit_exceeded":
			return sendExitUsage
		}
	}
	return sendExitFailed
}

// sendFailureJSON converts a delegation failure to the JSON contract,
// preserving the Hub's safety flags when the server supplied them.
func sendFailureJSON(err error) sendJSONResult {
	res := sendJSONResult{Status: "error", Message: err.Error()}
	var he *hubError
	if errors.As(err, &he) {
		if he.PlainMessage != "" {
			res.Message = he.PlainMessage
		}
		res.Code = he.Code
		res.RetrySafe = he.RetrySafe
		res.DuplicateRisk = he.DuplicateRisk
		res.DataSafe = he.DataSafe
		res.NextAction = he.NextAction
		res.OperationID = he.OperationID
		res.Destination = he.Destination
	}
	return res
}

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
	jsonOut := fs.Bool("json", false, "Print machine-readable JSON result (never includes payload content)")
	_ = fs.Parse(args)

	transportExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "transport" {
			transportExplicit = true
		}
	})

	rest := fs.Args()
	if len(rest) < 1 {
		if *jsonOut {
			emitSendJSON(sendJSONResult{Status: "error", Code: "invalid_input", Message: "missing content or file to send"}, sendExitUsage)
		}
		fmt.Fprintln(os.Stderr, "Error: missing content or file to send")
		fmt.Fprintln(os.Stderr, "Usage: tantu send [flags] <text|file|->")
		os.Exit(sendExitUsage)
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
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "input_error", Message: fmt.Sprintf("reading from stdin: %v", err)}, sendExitFailed)
			}
			fmt.Fprintf(os.Stderr, "Error: reading from stdin: %v\n", err)
			os.Exit(sendExitFailed)
		}
		if int64(len(data)) > standaloneTextDropLimit {
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "limit_exceeded", Message: "stdin text exceeds 10MB limit", NextAction: "Send it as a file instead."}, sendExitUsage)
			}
			fmt.Fprintln(os.Stderr, "Error: stdin text exceeds 10MB limit")
			os.Exit(sendExitUsage)
		}
		label := *nameFlag
		if label == "" {
			label = "stdin"
		}
		meta = drop.DropSend{
			DropID: drop.NewDropID(),
			Kind:   drop.DropKindText,
			Name:   label,
			Size:   int64(len(data)),
		}
		// One-shot CLI sends are their own logical operation: the wire
		// attempt ID doubles as the idempotency key.
		meta.IdempotencyKey = meta.DropID
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
		if err == nil && stat.IsDir() && (hasPathHint || len(rest) == 1) {
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "invalid_input", Message: fmt.Sprintf("%s is a directory", cleanPath), NextAction: "Archive it before sending."}, sendExitUsage)
			}
			fmt.Fprintf(os.Stderr, "Error: %s is a directory; archive it before sending\n", cleanPath)
			os.Exit(sendExitUsage)
		}
		if err == nil && !stat.IsDir() {
			if hasPathHint || len(rest) == 1 {
				isFile = true
			}
		}

		if isFile {
			f, err := os.Open(cleanPath)
			if err != nil {
				if *jsonOut {
					emitSendJSON(sendJSONResult{Status: "error", Code: "input_error", Message: fmt.Sprintf("cannot open file %s", cleanPath)}, sendExitFailed)
				}
				fmt.Fprintf(os.Stderr, "Error: cannot open file %s: %v\n", cleanPath, err)
				os.Exit(sendExitFailed)
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
				DropID:   drop.NewDropID(),
				Kind:     drop.DropKindFile,
				Name:     fileName,
				Size:     size,
				MIMEType: mimeType,
			}
			// One-shot CLI sends are their own logical operation: the wire
			// attempt ID doubles as the idempotency key.
			meta.IdempotencyKey = meta.DropID
			payload = f
		} else {
			// Text mode. Command-line text shares the 10MB text-drop limit
			// enforced on stdin and HTTP ingress; without it an unbounded
			// argv allocation reaches the wire only to be rejected there.
			text := strings.Join(rest, "")
			if len(rest) > 1 {
				text = strings.Join(rest, " ")
			}
			if strings.TrimSpace(text) == "" {
				if *jsonOut {
					emitSendJSON(sendJSONResult{Status: "error", Code: "invalid_input", Message: "text content is empty"}, sendExitUsage)
				}
				fmt.Fprintln(os.Stderr, "Error: text content is empty")
				os.Exit(sendExitUsage)
			}
			if int64(len(text)) > standaloneTextDropLimit {
				if *jsonOut {
					emitSendJSON(sendJSONResult{Status: "error", Code: "limit_exceeded", Message: "text exceeds 10MB limit", NextAction: "Send it as a file instead."}, sendExitUsage)
				}
				fmt.Fprintln(os.Stderr, "Error: text exceeds 10MB limit")
				os.Exit(sendExitUsage)
			}
			meta = drop.DropSend{
				DropID: drop.NewDropID(),
				Kind:   drop.DropKindText,
				Name:   *nameFlag,
				Size:   int64(len(text)),
			}
			// One-shot CLI sends are their own logical operation: the wire
			// attempt ID doubles as the idempotency key.
			meta.IdempotencyKey = meta.DropID
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
			noteHubVersionSkew(status)
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
			delegRes, err := delegateSendWithNameFromStore(*storeDir, delegateAddr, delegatedFilePath, textContent, meta.Name, *peerAddr, *timeout)
			if err != nil {
				if *jsonOut {
					res := sendFailureJSON(err)
					res.Kind = string(meta.Kind)
					res.Name = meta.Name
					res.Size = meta.Size
					emitSendJSON(res, sendExitForHubError(err))
				}
				fmt.Fprintf(os.Stderr, "❌ Hub delegation failed: %v\n", err)
				os.Exit(sendExitForHubError(err))
			}
			if *jsonOut {
				dest := ""
				opID := ""
				verified := false
				if delegRes != nil {
					dest = delegRes.Destination
					opID = delegRes.OperationID
					verified = delegRes.Verified
				}
				if dest == "" {
					dest = strings.TrimSpace(*peerAddr)
					if dest == "" {
						dest = "default/active peer"
					}
				}
				emitSendJSON(sendJSONResult{Status: "success", OperationID: opID, Kind: string(meta.Kind), Name: meta.Name, Size: meta.Size, Destination: dest, Verified: verified}, sendExitOK)
			}
			if isFile {
				fmt.Printf("✅ Sent file %q (%s) via local Hub\n", meta.Name, formatSize(meta.Size))
			} else {
				fmt.Printf("✅ Sent text (%d bytes) via local Hub\n", meta.Size)
			}
			if delegRes != nil && delegRes.Destination != "" {
				fmt.Printf("Destination: %s\n", delegRes.Destination)
			}
			if delegRes != nil && delegRes.OperationID != "" {
				fmt.Println("Operation ID: " + delegRes.OperationID + " (see `tantu transfers`)")
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
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "invalid_input", Message: "--ssh-host is required when --transport=ssh"}, sendExitUsage)
			}
			fmt.Fprintln(os.Stderr, "Error: --ssh-host is required when --transport=ssh")
			os.Exit(sendExitUsage)
		}
		if *sshKey == "" {
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "invalid_input", Message: "--ssh-key is required when --transport=ssh"}, sendExitUsage)
			}
			fmt.Fprintln(os.Stderr, "Error: --ssh-key is required when --transport=ssh")
			os.Exit(sendExitUsage)
		}
	case "lan":
		if lanStore == nil {
			var err error
			lanStore, err = openPeerStore(*storeDir)
			if err != nil {
				if *jsonOut {
					emitSendJSON(sendJSONResult{Status: "error", Code: "config_error", Message: fmt.Sprintf("%v", err)}, sendExitFailed)
				}
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(sendExitFailed)
			}
		}
		resolved, err := resolvePeer(lanStore, *peerAddr)
		if err != nil {
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "invalid_input", Message: fmt.Sprintf("%v", err), NextAction: "Specify --peer=NAME or set a default peer."}, sendExitUsage)
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(sendExitUsage)
		}
		*peerAddr = resolved
	default:
		if *jsonOut {
			emitSendJSON(sendJSONResult{Status: "error", Code: "invalid_input", Message: fmt.Sprintf("unknown transport %q, expected loopback, ssh, or lan", *transportType)}, sendExitUsage)
		}
		fmt.Fprintf(os.Stderr, "Error: unknown transport %q, expected loopback, ssh, or lan\n", *transportType)
		os.Exit(sendExitUsage)
	}

	var dialTarget string
	var expectedFingerprint string
	var destDisplay string
	var tr transport.Transport
	switch *transportType {
	case "ssh":
		keyBytes, err := os.ReadFile(*sshKey)
		if err != nil {
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "input_error", Message: fmt.Sprintf("cannot read SSH key: %s", *sshKey)}, sendExitFailed)
			}
			fmt.Fprintf(os.Stderr, "Error: cannot read SSH key: %s: %v\n", *sshKey, err)
			os.Exit(sendExitFailed)
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
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "config_error", Message: fmt.Sprintf("failed to configure SSH transport: %v", sshErr)}, sendExitFailed)
			}
			fmt.Fprintf(os.Stderr, "Error: failed to configure SSH transport: %v\n", sshErr)
			os.Exit(sendExitFailed)
		}
		dialTarget = net.JoinHostPort(*sshHost, strconv.Itoa(*sshPort))
	case "lan":
		id, err := lanStore.LoadIdentity()
		if err != nil || id == nil {
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "not_paired", Message: "No identity found.", NextAction: "Run `tantu pair` first."}, sendExitUsage)
			}
			fmt.Fprintln(os.Stderr, "Error: No identity found. Run 'tantu pair' first.")
			os.Exit(sendExitUsage)
		}
		tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
		if err != nil {
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "config_error", Message: fmt.Sprintf("invalid local TLS identity: %v", err)}, sendExitFailed)
			}
			fmt.Fprintf(os.Stderr, "Error: invalid local TLS identity: %v\n", err)
			os.Exit(sendExitFailed)
		}
		tr, err = transport.NewLANTransport(transport.LANTransportConfig{
			Cert:      tlsCert,
			IsTrusted: lanStore.IsTrusted,
		})
		if err != nil {
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "config_error", Message: fmt.Sprintf("failed to configure LAN transport: %v", err)}, sendExitFailed)
			}
			fmt.Fprintf(os.Stderr, "Error: failed to configure LAN transport: %v\n", err)
			os.Exit(sendExitFailed)
		}
		dialTarget = *peerAddr
		if resolvedPeer, resolveErr := resolvePeerForDial(lanStore, *peerAddr); resolveErr != nil {
			if *jsonOut {
				emitSendJSON(sendJSONResult{Status: "error", Code: "not_paired", Message: fmt.Sprintf("LAN target is not a trusted paired peer: %v", resolveErr), NextAction: "Run `tantu pair` first."}, sendExitUsage)
			}
			fmt.Fprintf(os.Stderr, "Error: LAN target is not a trusted paired peer: %v\n", resolveErr)
			os.Exit(sendExitUsage)
		} else {
			expectedFingerprint = resolvedPeer.Fingerprint
		}
	default:
		tr = transport.NewLoopbackTransport()
		dialTarget = *bridgeAddr
	}

	// Resolve a display destination without ever substituting a different
	// peer silently: explicit target wins, otherwise the single or active
	// peer, otherwise the raw dial address.
	destDisplay = displayDestination(*transportType, lanStore, *peerAddr, dialTarget)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *verbose {
		fmt.Printf("Connecting to %s (transport: %s)...\n", dialTarget, *transportType)
	}

	conn, err := transport.DialPinned(tr, dialTarget, expectedFingerprint)
	if err != nil {
		code, plain, nextAction, retrySafe, duplicateRisk, dataSafe := hub.ClassifyTransferError(err, 0, meta.Size, false)
		if *jsonOut {
			emitSendJSON(sendJSONResult{Status: "error", OperationID: meta.DropID, Kind: string(meta.Kind), Name: meta.Name, Size: meta.Size, Destination: destDisplay, DestinationAddress: dialTarget, Code: code, Message: plain, RetrySafe: retrySafe, DuplicateRisk: duplicateRisk, DataSafe: dataSafe, NextAction: nextAction}, sendExitFailed)
		}
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to peer at %s: %v\n", dialTarget, err)
		os.Exit(sendExitFailed)
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
	negotiated := false
	sendCfg.OnAck = func(drop.DropAck) { negotiated = true }
	if sendErr := drop.SendDrop(ctx, conn, meta, payload, sendCfg); sendErr != nil {
		bytesForClassify := int64(0)
		if negotiated {
			bytesForClassify = meta.Size
		}
		preCancel := !negotiated && ctx.Err() != nil
		code, plain, nextAction, retrySafe, duplicateRisk, dataSafe := hub.ClassifyTransferError(sendErr, bytesForClassify, meta.Size, preCancel)
		exitCode := sendExitFailed
		if duplicateRisk {
			exitCode = sendExitDuplicateRisk
		}
		if *jsonOut {
			emitSendJSON(sendJSONResult{Status: "error", OperationID: meta.DropID, Kind: string(meta.Kind), Name: meta.Name, Size: meta.Size, Destination: destDisplay, DestinationAddress: dialTarget, Code: code, Message: plain, RetrySafe: retrySafe, DuplicateRisk: duplicateRisk, DataSafe: dataSafe, NextAction: nextAction}, exitCode)
		}
		if duplicateRisk {
			fmt.Fprintf(os.Stderr, "Error: transfer may have completed (confirmation lost). Check the receiver inbox before retrying to avoid a duplicate: %v\n", sendErr)
		} else {
			fmt.Fprintf(os.Stderr, "❌ QuickDrop failed: %v\n", sendErr)
		}
		if nextAction != "" {
			fmt.Fprintf(os.Stderr, "Next: %s\n", nextAction)
		}
		os.Exit(exitCode)
	}

	if *jsonOut {
		emitSendJSON(sendJSONResult{Status: "success", OperationID: meta.DropID, Kind: string(meta.Kind), Name: meta.Name, Size: meta.Size, Destination: destDisplay, DestinationAddress: dialTarget, Verified: true}, sendExitOK)
	}
	fmt.Printf("Destination: %s\n", destDisplay)

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
