package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// transferRecord mirrors the Hub's sender-side operation contract for CLI
// display. Payload content is never present.
type transferRecord struct {
	OperationID   string    `json:"operation_id"`
	Kind          string    `json:"kind"`
	Name          string    `json:"name"`
	Size          int64     `json:"size"`
	Destination   string    `json:"destination"`
	State         string    `json:"state"`
	CreatedAt     time.Time `json:"created_at"`
	Verified      bool      `json:"verified,omitempty"`
	RetrySafe     bool      `json:"retry_safe"`
	DuplicateRisk bool      `json:"duplicate_risk"`
	ErrorCode     string    `json:"error_code,omitempty"`
	NextAction    string    `json:"next_action,omitempty"`
}

func fetchTransferRecords(webAddr, storeDir string) ([]transferRecord, error) {
	token := hub.RuntimeIPCTokenForAddr(storeDir, webAddr)
	if token == "" {
		return nil, fmt.Errorf("running Hub runtime metadata is unavailable")
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, "http://"+webAddr+"/api/transfers/recent", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(hub.IPCTokenHeader, token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("Hub returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var records []transferRecord
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&records); err != nil {
		return nil, fmt.Errorf("decode transfers: %w", err)
	}
	return records, nil
}

func printTransferRecords(records []transferRecord, jsonOut bool) {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(records)
		return
	}
	if len(records) == 0 {
		fmt.Println("No transfers recorded yet.")
		fmt.Println("Completed sends appear here and survive Hub restart (kept 30 days, last 50).")
		return
	}
	fmt.Printf("Transfers (last %d, oldest first, kept 30 days):\n", len(records))
	for _, r := range records {
		label := r.Name
		if label == "" {
			label = r.Kind
		}
		flags := ""
		if r.DuplicateRisk {
			flags += " [check inbox before retry]"
		} else if r.State == "retryable_failure" {
			flags += " [safe to retry]"
		}
		verified := ""
		if r.Verified {
			verified = " verified"
		}
		dest := r.Destination
		if dest == "" {
			dest = "unknown destination"
		}
		fmt.Printf("  %s  %s  %s (%s) -> %s  %s%s%s\n",
			r.OperationID, r.CreatedAt.Format("15:04:05"), r.Kind, formatSize(r.Size), dest, r.State, verified, flags)
		if r.NextAction != "" && r.State != "completed" {
			fmt.Printf("      Next: %s\n", r.NextAction)
		}
		_ = label
	}
	fmt.Println("Note: bounded durable history (last 50, 30 days); metadata only.")
}

func runTransfers(args []string) {
	fs := flag.NewFlagSet("transfers", flag.ExitOnError)
	storeDir := fs.String("store-dir", "", "Override config directory (default: platform config directory)")
	bridgeAddr := fs.String("bridge", "", "Optional local Hub address override")
	jsonOut := fs.Bool("json", false, "Print machine-readable JSON")
	_ = fs.Parse(NormalizeArgs(args))

	if status, ok := hub.ProbeHubWithStoreDir(*bridgeAddr, *storeDir); ok && status != nil && strings.TrimSpace(status.WebAddr) != "" {
		noteHubVersionSkew(status)
		records, err := fetchTransferRecords(status.WebAddr, *storeDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		printTransferRecords(records, *jsonOut)
		return
	}
	// Hub stopped: fall back to last-saved durable history so a restart does
	// not erase safe operation context. Explicitly labeled stale.
	saved, err := readSavedTransferRecords(*storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: no running Tantu Hub found. Start one with `tantu` or `tantu hub`.\n")
		fmt.Fprintf(os.Stderr, "Saved history is also unavailable: %v\n", err)
		os.Exit(1)
	}
	if len(saved) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no running Tantu Hub found. Start one with `tantu` or `tantu hub`.")
		fmt.Fprintln(os.Stderr, "No saved transfers either.")
		os.Exit(1)
	}
	fmt.Println("Hub: not running (showing last saved transfers; may be stale).")
	printTransferRecords(saved, *jsonOut)
}

// readSavedTransferRecords loads the durable ledger file for offline display.
// It returns metadata only and never touches payload content.
func readSavedTransferRecords(storeDir string) ([]transferRecord, error) {
	dir := storeDir
	if dir == "" {
		def, err := pairing.DefaultStoreDir()
		if err != nil {
			return nil, err
		}
		dir = def
	}
	raw, err := os.ReadFile(filepath.Join(dir, "transfers.json"))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("saved transfer history exceeds size limit")
	}
	var env struct {
		Operations []transferRecord `json:"operations"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode saved transfers: %w", err)
	}
	return env.Operations, nil
}

// runTransfer implements `tantu transfer <subcommand>` (`list`, `clear`).
// A `retry` subcommand is deliberately absent: retrying after an ambiguous
// acknowledgement is at-least-once and may duplicate receiver output, so the
// CLI refuses blind retry and points at the safe check.
func runTransfer(args []string) {
	rest := NormalizeArgs(args)
	if len(rest) == 0 || rest[0] == "list" {
		listArgs := []string{}
		if len(rest) > 1 {
			listArgs = rest[1:]
		}
		runTransfers(listArgs)
		return
	}
	if rest[0] == "clear" {
		clearArgs := []string{}
		if len(rest) > 1 {
			clearArgs = rest[1:]
		}
		runTransferClear(clearArgs)
		return
	}
	if rest[0] == "retry" {
		fmt.Fprintln(os.Stderr, "Error: `tantu transfer retry` is not offered because the Hub keeps no payload to resend, and retry after an unknown outcome may create a duplicate.")
		fmt.Fprintln(os.Stderr, "For a duplicate-safe retry, re-run the original send with the same --idempotency-key: the receiver re-acknowledges instead of publishing twice.")
		fmt.Fprintln(os.Stderr, "Otherwise check `tantu transfers` for duplicate-risk flags and the receiver inbox first; then send again as a new transfer.")
		os.Exit(1)
		return
	}
	fmt.Fprintf(os.Stderr, "Unknown transfer subcommand: %s\n\nUsage:\n  tantu transfer list [--json]\n  tantu transfer clear --yes [--store-dir DIR]\n", rest[0])
	os.Exit(1)
}

// runTransferClear deletes sender-side transfer metadata after explicit
// confirmation. Received files are never touched. When the Hub is running
// the request goes through the authenticated API (clearing memory and disk
// together); when stopped with --yes, the saved file is removed directly.
func runTransferClear(args []string) {
	fs := flag.NewFlagSet("transfer clear", flag.ExitOnError)
	storeDir := fs.String("store-dir", "", "Override config directory (default: platform config directory)")
	bridgeAddr := fs.String("bridge", "", "Optional local Hub address override")
	confirm := fs.Bool("yes", false, "Confirm deletion (required: history removal cannot be undone)")
	_ = fs.Parse(NormalizeArgs(args))

	if !*confirm {
		fmt.Fprintln(os.Stderr, "This removes all sender-side transfer metadata (destinations, states, times) for this store.")
		fmt.Fprintln(os.Stderr, "Received files are kept. Re-run with --yes to confirm.")
		os.Exit(2)
	}
	if status, ok := hub.ProbeHubWithStoreDir(*bridgeAddr, *storeDir); ok && status != nil && strings.TrimSpace(status.WebAddr) != "" {
		removed, err := deleteLiveTransfers(status.WebAddr, *storeDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Transfer history cleared (%d record(s) removed).\n", removed)
		return
	}
	removed, err := clearSavedTransferFile(*storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if removed {
		fmt.Println("Saved transfer history cleared (Hub was not running).")
	} else {
		fmt.Println("No saved transfer history to clear (Hub was not running).")
	}
}

// deleteLiveTransfers clears the Hub ledger via the authenticated API.
func deleteLiveTransfers(webAddr, storeDir string) (int, error) {
	token := hub.RuntimeIPCTokenForAddr(storeDir, webAddr)
	if token == "" {
		return 0, fmt.Errorf("running Hub runtime metadata is unavailable")
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodDelete, "http://"+webAddr+"/api/transfers/recent", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set(hub.IPCTokenHeader, token)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Hub returned clear status %d", resp.StatusCode)
	}
	var payload struct {
		Status  string `json:"status"`
		Removed int    `json:"removed"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&payload); err != nil {
		return 0, fmt.Errorf("decode clear response: %w", err)
	}
	return payload.Removed, nil
}

// clearSavedTransferFile removes the durable ledger file. It reports whether
// a file existed; a missing file is not an error.
func clearSavedTransferFile(storeDir string) (bool, error) {
	dir := storeDir
	if dir == "" {
		def, err := pairing.DefaultStoreDir()
		if err != nil {
			return false, err
		}
		dir = def
	}
	err := os.Remove(filepath.Join(dir, "transfers.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("remove saved transfer history: %w", err)
	}
	return true, nil
}
