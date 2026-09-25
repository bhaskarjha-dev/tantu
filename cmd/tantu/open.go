package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
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
	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// open exit codes distinguish outcomes for automation: 0 success,
// 1 relay failure, 2 invalid input or usage. Relay performs no publication,
// so there is no duplicate-risk code.
const (
	openExitOK     = 0
	openExitFailed = 1
	openExitUsage  = 2
)

// openJSONResult is the machine-readable `tantu open --json` contract. The
// authorization URL itself is never included.
type openJSONResult struct {
	Status             string `json:"status"`
	OperationID        string `json:"operation_id,omitempty"`
	Destination        string `json:"destination,omitempty"`
	DestinationAddress string `json:"destination_address,omitempty"`
	Code               string `json:"code,omitempty"`
	Message            string `json:"message,omitempty"`
	NextAction         string `json:"next_action,omitempty"`
}

func emitOpenJSON(res openJSONResult, exitCode int) {
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(res)
	os.Exit(exitCode)
}

// openFailureJSON converts a delegation failure to the JSON contract,
// preserving the Hub's recorded fields when the server supplied them.
func openFailureJSON(err error) openJSONResult {
	res := openJSONResult{Status: "error", Message: err.Error()}
	var re *hub.RelayAPIError
	if errors.As(err, &re) {
		if re.Message != "" {
			res.Message = re.Message
		}
		res.OperationID = re.OperationID
		res.Destination = re.Destination
		res.NextAction = re.NextAction
	}
	return res
}

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
	sshKnownHosts := fs.String("ssh-known-hosts", "", "Path to OpenSSH known_hosts for server verification (default: ~/.ssh/known_hosts)")
	sshFingerprint := fs.String("ssh-fingerprint", "", "Pin the server host key (format: SHA256:...); overrides known_hosts when set")
	timeout := fs.Duration("timeout", 5*time.Minute, "Timeout waiting for authentication flow to complete")
	verbose := fs.Bool("v", false, "Enable verbose output")
	jsonOut := fs.Bool("json", false, "Print machine-readable JSON result (never includes the URL)")
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
			emitOpenJSON(openJSONResult{Status: "error", Code: "invalid_input", Message: "missing OAuth authorization URL"}, openExitUsage)
		}
		fmt.Fprintln(os.Stderr, "Error: missing OAuth authorization URL")
		fmt.Fprintln(os.Stderr, "Usage: tantu open [flags] <url>")
		os.Exit(openExitUsage)
	}
	if len(rest) > 1 {
		if *jsonOut {
			emitOpenJSON(openJSONResult{Status: "error", Code: "invalid_input", Message: "open accepts exactly one URL"}, openExitUsage)
		}
		fmt.Fprintln(os.Stderr, "Error: open accepts exactly one URL (quote the URL if it contains shell spaces)")
		os.Exit(openExitUsage)
	}
	oauthURL := rest[0]
	if oauthURL == "-" {
		// OAuth URLs may approach the 64 KiB bridge limit; the default
		// 64 KiB scanner token cap would reject near-limit URLs that succeed
		// via argv, so size the buffer for the longest acceptable input.
		oauthURL = ""
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 64*1024), 128*1024)
		if scanner.Scan() {
			oauthURL = strings.TrimSpace(scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			if *jsonOut {
				emitOpenJSON(openJSONResult{Status: "error", Code: "input_error", Message: fmt.Sprintf("reading URL from stdin: %v", err)}, openExitFailed)
			}
			fmt.Fprintf(os.Stderr, "Error: reading URL from stdin: %v\n", err)
			os.Exit(openExitFailed)
		}
		if oauthURL == "" {
			if *jsonOut {
				emitOpenJSON(openJSONResult{Status: "error", Code: "invalid_input", Message: "no URL received on stdin"}, openExitUsage)
			}
			fmt.Fprintln(os.Stderr, "Error: no URL received on stdin")
			os.Exit(openExitUsage)
		}
	}

	// 1. If transport is not explicitly overridden to SSH, probe local Hub with strict 200ms timeout
	if (!transportExplicit || *transportType == "loopback") && *sshHost == "" {
		if status, ok := hub.ProbeHubWithStoreDir(*bridgeAddr, *storeDir); ok {
			noteHubVersionSkew(status)
			if *verbose {
				fmt.Printf("Connected to local Hub (v%s, %s transport)\n", status.Version, status.Transport)
			}
			if *verbose {
				fmt.Println("Delegating OAuth authorization URL to local Hub...")
			}
			delegateAddr := *bridgeAddr
			if status.WebAddr != "" {
				delegateAddr = status.WebAddr
			}
			relayRes, err := hub.DelegateOpenFromStore(*storeDir, delegateAddr, oauthURL, *peerAddr)
			if err != nil {
				if *jsonOut {
					emitOpenJSON(openFailureJSON(err), openExitFailed)
				}
				fmt.Fprintf(os.Stderr, "❌ Hub delegation failed: %v\n", err)
				os.Exit(openExitFailed)
			}
			if *jsonOut {
				dest := ""
				opID := ""
				if relayRes != nil {
					dest = relayRes.Destination
					opID = relayRes.OperationID
				}
				if dest == "" {
					dest = strings.TrimSpace(*peerAddr)
					if dest == "" {
						dest = "default/active peer"
					}
				}
				emitOpenJSON(openJSONResult{Status: "success", OperationID: opID, Destination: dest}, openExitOK)
			}
			fmt.Println("✅ Authentication completed successfully.")
			if relayRes != nil && relayRes.Destination != "" {
				fmt.Printf("Destination: %s\n", relayRes.Destination)
			}
			if relayRes != nil && relayRes.OperationID != "" {
				fmt.Println("Operation ID: " + relayRes.OperationID + " (see dashboard Recent Authorizations)")
			}
			return
		}
	}

	// Supplying SSH connection options selects SSH when --transport was not
	// explicitly supplied.
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
				emitOpenJSON(openJSONResult{Status: "error", Code: "invalid_input", Message: "--ssh-host is required when --transport=ssh"}, openExitUsage)
			}
			fmt.Fprintln(os.Stderr, "Error: --ssh-host is required when --transport=ssh")
			os.Exit(openExitUsage)
		}
		if *sshKey == "" {
			if *jsonOut {
				emitOpenJSON(openJSONResult{Status: "error", Code: "invalid_input", Message: "--ssh-key is required when --transport=ssh"}, openExitUsage)
			}
			fmt.Fprintln(os.Stderr, "Error: --ssh-key is required when --transport=ssh")
			os.Exit(openExitUsage)
		}
	case "lan":
		if lanStore == nil {
			var err error
			lanStore, err = openPeerStore(*storeDir)
			if err != nil {
				if *jsonOut {
					emitOpenJSON(openJSONResult{Status: "error", Code: "config_error", Message: fmt.Sprintf("%v", err)}, openExitFailed)
				}
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(openExitFailed)
			}
		}
		resolved, err := resolvePeer(lanStore, *peerAddr)
		if err != nil {
			if *jsonOut {
				emitOpenJSON(openJSONResult{Status: "error", Code: "invalid_input", Message: fmt.Sprintf("%v", err), NextAction: "Specify --peer=NAME or set a default peer."}, openExitUsage)
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(openExitUsage)
		}
		*peerAddr = resolved
	default:
		if *jsonOut {
			emitOpenJSON(openJSONResult{Status: "error", Code: "invalid_input", Message: fmt.Sprintf("unknown transport %q, expected loopback, ssh, or lan", *transportType)}, openExitUsage)
		}
		fmt.Fprintf(os.Stderr, "Error: unknown transport %q, expected loopback, ssh, or lan\n", *transportType)
		os.Exit(openExitUsage)
	}

	var dialTarget string
	var expectedFingerprint string
	var tr transport.Transport
	switch *transportType {
	case "ssh":
		keyBytes, err := os.ReadFile(*sshKey)
		if err != nil {
			if *jsonOut {
				emitOpenJSON(openJSONResult{Status: "error", Code: "input_error", Message: fmt.Sprintf("cannot read SSH key: %s", *sshKey)}, openExitFailed)
			}
			fmt.Fprintf(os.Stderr, "Error: cannot read SSH key: %s: %v\n", *sshKey, err)
			os.Exit(openExitFailed)
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
				emitOpenJSON(openJSONResult{Status: "error", Code: "config_error", Message: fmt.Sprintf("failed to configure SSH transport: %v", sshErr)}, openExitFailed)
			}
			fmt.Fprintf(os.Stderr, "Error: failed to configure SSH transport: %v\n", sshErr)
			os.Exit(openExitFailed)
		}
		dialTarget = net.JoinHostPort(*sshHost, strconv.Itoa(*sshPort))
	case "lan":
		id, err := lanStore.LoadIdentity()
		if err != nil || id == nil {
			if *jsonOut {
				emitOpenJSON(openJSONResult{Status: "error", Code: "not_paired", Message: "No identity found.", NextAction: "Run `tantu pair` first."}, openExitUsage)
			}
			fmt.Fprintln(os.Stderr, "Error: No identity found. Run 'tantu pair' first.")
			os.Exit(openExitUsage)
		}
		tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
		if err != nil {
			if *jsonOut {
				emitOpenJSON(openJSONResult{Status: "error", Code: "config_error", Message: fmt.Sprintf("invalid local TLS identity: %v", err)}, openExitFailed)
			}
			fmt.Fprintf(os.Stderr, "Error: invalid local TLS identity: %v\n", err)
			os.Exit(openExitFailed)
		}
		tr, err = transport.NewLANTransport(transport.LANTransportConfig{
			Cert:      tlsCert,
			IsTrusted: lanStore.IsTrusted,
		})
		if err != nil {
			if *jsonOut {
				emitOpenJSON(openJSONResult{Status: "error", Code: "config_error", Message: fmt.Sprintf("failed to configure LAN transport: %v", err)}, openExitFailed)
			}
			fmt.Fprintf(os.Stderr, "Error: failed to configure LAN transport: %v\n", err)
			os.Exit(openExitFailed)
		}
		dialTarget = *peerAddr
		if resolvedPeer, resolveErr := resolvePeerForDial(lanStore, *peerAddr); resolveErr != nil {
			if *jsonOut {
				emitOpenJSON(openJSONResult{Status: "error", Code: "not_paired", Message: fmt.Sprintf("LAN target is not a trusted paired peer: %v", resolveErr), NextAction: "Run `tantu pair` first."}, openExitUsage)
			}
			fmt.Fprintf(os.Stderr, "Error: LAN target is not a trusted paired peer: %v\n", resolveErr)
			os.Exit(openExitUsage)
		} else {
			expectedFingerprint = resolvedPeer.Fingerprint
		}
	default:
		tr = transport.NewLoopbackTransport()
		dialTarget = *bridgeAddr
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("🔗 Connecting to bridge at %s (transport: %s)...\n", dialTarget, *transportType)
	if *verbose {
		fmt.Printf("Target OAuth URL: %s\n", browser.RedactURL(oauthURL))
		fmt.Printf("Session timeout: %v\n", *timeout)
	}

	conn, err := transport.DialPinned(tr, dialTarget, expectedFingerprint)
	if err != nil {
		code, plain, nextAction, _, _, _ := hub.ClassifyTransferError(err, 0, 0, false)
		if *jsonOut {
			emitOpenJSON(openJSONResult{Status: "error", Destination: displayDestination(*transportType, lanStore, *peerAddr, dialTarget), DestinationAddress: dialTarget, Code: code, Message: plain, NextAction: nextAction}, openExitFailed)
		}
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to bridge server at %s: %v\n", dialTarget, err)
		os.Exit(openExitFailed)
	}
	defer conn.Close()

	fmt.Println("Connected. Sending URL to bridge...")
	if *verbose {
		fmt.Println("Handshake established with bridge server.")
	}

	fmt.Println("📥 Waiting for callback...")
	cfg := bridge.BSideConfig{Timeout: *timeout}
	if err := bridge.HandleBSide(ctx, conn, oauthURL, cfg); err != nil {
		if *jsonOut {
			emitOpenJSON(openJSONResult{Status: "error", Destination: displayDestination(*transportType, lanStore, *peerAddr, dialTarget), DestinationAddress: dialTarget, Code: "relay_failed", Message: bridge.RedactError(err), NextAction: "Check the peer is online, then retry the login."}, openExitFailed)
		}
		fmt.Fprintf(os.Stderr, "❌ Authentication bridge failed: %s\n", bridge.RedactError(err))
		os.Exit(openExitFailed)
	}

	if *jsonOut {
		emitOpenJSON(openJSONResult{Status: "success", Destination: displayDestination(*transportType, lanStore, *peerAddr, dialTarget), DestinationAddress: dialTarget}, openExitOK)
	}
	fmt.Printf("Destination: %s\n", displayDestination(*transportType, lanStore, *peerAddr, dialTarget))
	fmt.Println("✅ Authentication completed successfully.")
}
