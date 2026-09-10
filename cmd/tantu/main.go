package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

var version = "1.0.0"

func generateSessionID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xFFFFFFFF)
	}
	return hex.EncodeToString(b)
}

func defaultUsername() string {
	if u, err := user.Current(); err == nil && u != nil && u.Username != "" {
		username := u.Username
		if idx := strings.LastIndex(username, "\\"); idx != -1 {
			username = username[idx+1:]
		}
		return username
	}
	return "tantu"
}

func openPeerStore(storeDir string) (*pairing.PeerStore, error) {
	sDir := storeDir
	if sDir == "" {
		var err error
		sDir, err = pairing.DefaultStoreDir()
		if err != nil {
			return nil, fmt.Errorf("cannot determine config directory: %w", err)
		}
	}
	return pairing.NewPeerStore(sDir)
}

func ensurePort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return net.JoinHostPort(addr, transport.DefaultLANPort)
	}
	return addr
}

func ensurePeerLANPort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(addr, transport.DefaultLANPort)
}

// resolvePeer resolves the peer address for LAN transport.
// If peerFlag is non-empty, returns it as-is (with default port appended if needed).
// If peerFlag is empty, auto-selects from the peer store.
func resolvePeer(store *pairing.PeerStore, peerFlag string) (string, error) {
	peers := store.ListPeers()
	if peerFlag != "" {
		for _, p := range peers {
			if p.Name == peerFlag || p.Fingerprint == peerFlag || strings.HasPrefix(p.Fingerprint, peerFlag) {
				return ensurePeerLANPort(p.Address), nil
			}
		}
		return ensurePort(peerFlag), nil
	}

	if len(peers) == 0 {
		return "", errors.New("No paired peers. Run tantu pair first.")
	}
	if len(peers) == 1 {
		return ensurePeerLANPort(peers[0].Address), nil
	}

	var b strings.Builder
	b.WriteString("Multiple peers found. Specify --peer=NAME or --peer=HOST:PORT:\n")
	for _, p := range peers {
		name := p.Name
		if name == "" {
			name = "(unnamed)"
		}
		fp := p.Fingerprint
		if len(fp) > 12 {
			fp = fp[:12]
		}
		fmt.Fprintf(&b, "  - %s (%s, %s)\n", name, ensurePeerLANPort(p.Address), fp)
	}
	return "", errors.New(strings.TrimRight(b.String(), "\n"))
}

// defaultTransport returns "lan" by default to enable LAN discovery and pairing.
func defaultTransport(store *pairing.PeerStore) string {
	return "lan"
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `tantu - Zero-configuration encrypted peer-to-peer thread for developers

Usage:
  tantu                         (starts the Unified Symmetric Hub)
  tantu <command> [arguments]
  tantu [flags] <url>           (implicit open for $BROWSER compatibility)

Commands:
  hub           Start the Unified Symmetric Hub (default when run with no arguments)
  node          Start unified peer node (OAuth + QuickDrop listener simultaneously)
  serve         Start the A-side bridge listener (on the machine with the browser)
  open <url>    Send an OAuth authorization URL through the bridge (on the remote/app machine)
  relay         Start a local HTTP server for one-click bookmarklet and Web UI relay
  wrap          Execute a command with BROWSER set to tantu
  pair          Pair with a remote machine for direct LAN transport (mTLS)
  unpair        Remove a paired peer from the trusted peer store
  status        Show local identity and paired peers
  send          Send text, images, or files to a paired machine (QuickDrop)
  receive       Receive text, images, or files from a paired machine (QuickDrop)
  drop          Start a local Web UI for drag-and-drop file sharing (QuickDrop)
  version       Print tantu version

Flags:
  -v, --version Print version and exit
  -h, --help    Print this help message and exit

Run 'tantu <command> -help' for more information about a command.
`)
}

func main() {
	hub.SetHubVersion(version)
	if len(os.Args) < 2 {
		runHub(nil)
		return
	}

	arg := os.Args[1]
	subArgs := NormalizeArgs(os.Args[2:])
	switch arg {
	case "-v", "--version", "version":
		fmt.Printf("tantu v%s\n", strings.TrimPrefix(version, "v"))
		os.Exit(0)
	case "-h", "--help", "-help", "help":
		printUsage()
		os.Exit(0)
	case "hub":
		runHub(subArgs)
	case "node":
		runNode(subArgs)
	case "serve":
		runServe(subArgs)
	case "open":
		runOpen(subArgs)
	case "relay":
		runRelay(subArgs)
	case "wrap":
		runWrap(subArgs)
	case "pair":
		runPair(subArgs)
	case "unpair":
		runUnpair(subArgs)
	case "status":
		runStatus(subArgs)
	case "send":
		runSend(subArgs)
	case "receive":
		runReceive(subArgs)
	case "drop":
		runDrop(subArgs)
	default:
		if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
			runOpen(os.Args[1:])
			return
		}
		if strings.HasPrefix(arg, "-") {
			for _, a := range os.Args[1:] {
				if strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") {
					runOpen(os.Args[1:])
					return
				}
			}
			runHub(os.Args[1:])
			return
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", arg)
		printUsage()
		os.Exit(1)
	}
}
