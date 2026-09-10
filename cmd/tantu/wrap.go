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
	if (!transportExplicit || *transportType == "loopback") && *sshHost == "" {
		if _, ok := hub.ProbeHub(*bridgeAddr); ok {
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

	browserCmd := binPath
	if strings.Contains(binPath, " ") {
		browserCmd = `"` + binPath + `"`
	}

	if *transportType == "ssh" {
		browserCmd += fmt.Sprintf(" -transport=ssh -ssh-host=%s -ssh-port=%d -ssh-user=%s -ssh-key=%s",
			*sshHost, *sshPort, *sshUser, *sshKey)
	} else if *transportType == "lan" {
		browserCmd += fmt.Sprintf(" -transport=lan -peer=%s", *peerAddr)
		if *storeDir != "" {
			browserCmd += fmt.Sprintf(" -store-dir=%s", *storeDir)
		}
	} else if *bridgeAddr != "127.0.0.1:9876" {
		browserCmd += fmt.Sprintf(" -bridge=%s", *bridgeAddr)
	}

	if *timeout != 5*time.Minute {
		browserCmd += fmt.Sprintf(" -timeout=%v", *timeout)
	}
	if *verbose {
		browserCmd += " -v"
	}

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
