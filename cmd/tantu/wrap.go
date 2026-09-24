package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

func runWrap(args []string) {
	sepIdx := -1
	for i, a := range args {
		if a == "--" {
			sepIdx = i
			break
		}
	}

	if sepIdx == -1 || sepIdx == len(args)-1 {
		fmt.Fprintln(os.Stderr, "Error: missing command to wrap")
		fmt.Fprintln(os.Stderr, "Usage: tantu wrap [flags] -- <command> [args...]")
		os.Exit(1)
	}

	wrapFlags := args[:sepIdx]
	cmdArgs := args[sepIdx+1:]

	fs := flag.NewFlagSet("wrap", flag.ExitOnError)
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
	_ = fs.Parse(wrapFlags)

	transportExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "transport" {
			transportExplicit = true
		}
	})

	var store *pairing.PeerStore
	usingLocalHub := false
	localHubAddr := ""
	if !transportExplicit && strings.TrimSpace(*sshHost) != "" {
		// Supplying SSH connection options selects SSH when --transport was
		// not explicitly supplied.
		*transportType = "ssh"
	} else if (!transportExplicit || *transportType == "loopback") && *sshHost == "" {
		if status, ok := hub.ProbeHubWithStoreDir(*bridgeAddr, *storeDir); ok {
			usingLocalHub = true
			if status.WebAddr != "" {
				localHubAddr = status.WebAddr
			}
			*transportType = "loopback"
		} else {
			var err error
			store, err = openPeerStore(*storeDir)
			if err == nil {
				*transportType = defaultTransport(store)
			} else {
				*transportType = "loopback"
			}
		}
	} else if *transportType == "" {
		*transportType = "loopback"
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
		if store == nil {
			var err error
			store, err = openPeerStore(*storeDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
		resolved, err := resolvePeer(store, *peerAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		*peerAddr = resolved
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown transport %q, expected loopback, ssh, or lan\n", *transportType)
		os.Exit(1)
	}

	binPath, err := os.Executable()
	if err != nil || binPath == "" {
		binPath = os.Args[0]
	}

	browserArgs := []string{binPath}
	if *transportType == "ssh" {
		browserArgs = append(browserArgs,
			"-transport=ssh",
			fmt.Sprintf("-ssh-host=%s", *sshHost),
			fmt.Sprintf("-ssh-port=%d", *sshPort),
			fmt.Sprintf("-ssh-user=%s", *sshUser),
			fmt.Sprintf("-ssh-key=%s", *sshKey),
		)
	} else if *transportType == "lan" {
		browserArgs = append(browserArgs,
			"-transport=lan",
			fmt.Sprintf("-peer=%s", *peerAddr),
		)
		if *storeDir != "" {
			browserArgs = append(browserArgs, fmt.Sprintf("-store-dir=%s", *storeDir))
		}
	} else {
		// A local Hub still needs an explicit peer argument when the caller
		// supplied one; otherwise the child CLI can resolve the Hub's active
		// peer just as it did before.
		if usingLocalHub && *storeDir != "" {
			browserArgs = append(browserArgs, fmt.Sprintf("-store-dir=%s", *storeDir))
		}
		if usingLocalHub && *peerAddr != "" {
			browserArgs = append(browserArgs, fmt.Sprintf("-peer=%s", *peerAddr))
		}
		if localHubAddr != "" && localHubAddr != "127.0.0.1:9876" {
			browserArgs = append(browserArgs, fmt.Sprintf("-bridge=%s", localHubAddr))
		} else if *bridgeAddr != "127.0.0.1:9876" {
			browserArgs = append(browserArgs, fmt.Sprintf("-bridge=%s", *bridgeAddr))
		}
	}

	if *timeout != 5*time.Minute {
		browserArgs = append(browserArgs, fmt.Sprintf("-timeout=%s", timeout.String()))
	}
	if *verbose {
		browserArgs = append(browserArgs, "-v")
	}
	browserCmd := joinBROWSERArgs(browserArgs)

	env := os.Environ()
	browserFound := false
	browserEnv := "BROWSER=" + browserCmd
	for i, e := range env {
		if strings.HasPrefix(strings.ToUpper(e), "BROWSER=") {
			env[i] = browserEnv
			browserFound = true
			break
		}
	}
	if !browserFound {
		env = append(env, browserEnv)
	}

	childCmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	childCmd.Env = env
	childCmd.Stdin = os.Stdin
	childCmd.Stdout = os.Stdout
	childCmd.Stderr = os.Stderr

	if err := childCmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "Error: failed to execute %s: %v\n", cmdArgs[0], err)
		os.Exit(1)
	}
}
