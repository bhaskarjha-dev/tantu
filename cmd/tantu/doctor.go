package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

// healthCheck is one plain-language diagnostic with a safe next action.
// It never carries payload bytes, tokens, URLs, or private keys.
type healthCheck struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	NextAction string `json:"next_action,omitempty"`
}

// healthPeer is redacted peer metadata for diagnostics (no certificates).
type healthPeer struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
	SAS         string `json:"sas"`
	IsDefault   bool   `json:"is_default,omitempty"`
}

// healthReport is the canonical `tantu doctor` / `tantu status --json`
// contract. Human output stays friendly; JSON is stable for automation.
type healthReport struct {
	Version     string        `json:"version"`
	StoreDir    string        `json:"store_dir"`
	HubRunning  bool          `json:"hub_running"`
	HubURL      string        `json:"hub_url,omitempty"`
	Transport   string        `json:"transport,omitempty"`
	OutputDir   string        `json:"output_dir,omitempty"`
	Identity    string        `json:"identity_fingerprint,omitempty"`
	IdentitySAS string        `json:"identity_sas,omitempty"`
	ActivePeer  string        `json:"active_peer,omitempty"`
	Peers       []healthPeer  `json:"peers"`
	Checks      []healthCheck `json:"checks"`
	NextAction  string        `json:"next_action"`
	Status      string        `json:"status"`
}

// fetchHubOutputDir asks a running Hub for its receiver directory via the
// capability-protected config endpoint. It returns "" when unavailable.
func fetchHubOutputDir(webAddr, storeDir string) string {
	token := hub.RuntimeIPCTokenForAddr(storeDir, webAddr)
	if token == "" {
		return ""
	}
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, "http://"+webAddr+"/api/config", nil)
	if err != nil {
		return ""
	}
	req.Header.Set(hub.IPCTokenHeader, token)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()
	var cfg struct {
		OutputDir string `json:"output_dir"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&cfg); err != nil {
		return ""
	}
	return cfg.OutputDir
}

// buildHealthReport gathers diagnostics without exiting. It never prints
// secrets and never invents a Hub that is not live-probed.
func buildHealthReport(storeDir, bridgeAddr string) healthReport {
	report := healthReport{
		Version: strings.TrimPrefix(version, "v"),
		Peers:   []healthPeer{},
		Checks:  []healthCheck{},
	}
	dir := storeDir
	if dir == "" {
		if def, err := pairing.DefaultStoreDir(); err == nil {
			dir = def
		}
	}
	report.StoreDir = dir

	store, storeErr := openPeerStore(storeDir)
	if storeErr != nil {
		report.Checks = append(report.Checks, healthCheck{
			Name:       "store",
			OK:         false,
			Message:    fmt.Sprintf("Cannot open config directory: %v", storeErr),
			NextAction: "Check permissions on the config directory and retry.",
		})
		report.Status = "degraded"
		report.NextAction = "Fix config directory access, then run `tantu doctor` again."
		return report
	}
	report.Checks = append(report.Checks, healthCheck{
		Name:    "store",
		OK:      true,
		Message: fmt.Sprintf("Config directory ready (%s).", dir),
	})

	id, idErr := store.LoadIdentity()
	if idErr != nil {
		report.Checks = append(report.Checks, healthCheck{
			Name:       "identity",
			OK:         false,
			Message:    fmt.Sprintf("Cannot load identity: %v", idErr),
			NextAction: "Run `tantu pair` to generate an identity.",
		})
	} else if id == nil {
		report.Checks = append(report.Checks, healthCheck{
			Name:       "identity",
			OK:         false,
			Message:    "No identity configured.",
			NextAction: "Run `tantu pair` to generate one.",
		})
	} else {
		report.Identity = id.Fingerprint
		report.IdentitySAS = pairing.SASCode(id.Fingerprint)
		report.Checks = append(report.Checks, healthCheck{
			Name:    "identity",
			OK:      true,
			Message: fmt.Sprintf("Identity present (SAS: %s).", report.IdentitySAS),
		})
	}

	peers := store.ListPeers()
	for _, p := range peers {
		report.Peers = append(report.Peers, healthPeer{
			Name:        p.DisplayName(),
			Address:     p.Address,
			Fingerprint: p.Fingerprint,
			SAS:         pairing.SASCode(p.Fingerprint),
			IsDefault:   p.IsDefault,
		})
	}
	if len(peers) == 0 {
		report.Checks = append(report.Checks, healthCheck{
			Name:       "peers",
			OK:         false,
			Message:    "No trusted peers.",
			NextAction: "Run `tantu pair` to connect another machine.",
		})
	} else {
		report.Checks = append(report.Checks, healthCheck{
			Name:    "peers",
			OK:      true,
			Message: fmt.Sprintf("%d trusted peer(s) configured.", len(peers)),
		})
	}

	if live, ok := hub.ProbeHubWithStoreDir(bridgeAddr, storeDir); ok && live != nil {
		report.HubRunning = true
		report.Transport = live.Transport
		dash := live.WebAddr
		if dash != "" && !strings.Contains(dash, "://") {
			dash = "http://" + dash
		}
		report.HubURL = dash
		if hub.CanonicalVersion(live.Version) != hub.CanonicalVersion(hub.HubVersion) {
			report.Checks = append(report.Checks, healthCheck{
				Name:       "version",
				OK:         false,
				Message:    fmt.Sprintf("Hub version %q differs from CLI %q.", live.Version, hub.HubVersion),
				NextAction: "Restart the Hub after upgrading so both run the same release.",
			})
		} else {
			report.Checks = append(report.Checks, healthCheck{
				Name:    "version",
				OK:      true,
				Message: "CLI and Hub versions match.",
			})
		}
		report.Checks = append(report.Checks, healthCheck{
			Name:    "hub",
			OK:      true,
			Message: fmt.Sprintf("Hub running (transport %s).", live.Transport),
		})
		if out := fetchHubOutputDir(live.WebAddr, storeDir); out != "" {
			report.OutputDir = out
			report.Checks = append(report.Checks, healthCheck{
				Name:    "output_dir",
				OK:      true,
				Message: fmt.Sprintf("Received files save to %s.", out),
			})
		}
	} else {
		report.Checks = append(report.Checks, healthCheck{
			Name:       "hub",
			OK:         false,
			Message:    "Hub is not running.",
			NextAction: "Start it with `tantu` or `tantu hub`. Then use `tantu dashboard` for a fresh link.",
		})
	}

	// Overall status and one next action: the first failing check wins so
	// every warning has a concrete recovery path and no dead end.
	report.Status = "healthy"
	for _, c := range report.Checks {
		if !c.OK {
			report.Status = "degraded"
			if c.NextAction != "" {
				report.NextAction = c.NextAction
				break
			}
		}
	}
	if report.NextAction == "" {
		if len(peers) > 0 && report.HubRunning {
			report.NextAction = "Send a test file or snippet from the dashboard or with `tantu send`."
		} else {
			report.NextAction = "Run `tantu status` for details."
		}
	}
	return report
}

func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	storeDir := fs.String("store-dir", "", "Override config directory (default: platform config directory)")
	bridgeAddr := fs.String("bridge", "", "Optional local Hub address override")
	jsonOut := fs.Bool("json", false, "Print machine-readable JSON and exit 0/1")
	bundlePath := fs.String("bundle-path", "", "Write a redacted support bundle (JSON, metadata only) to this path")
	_ = fs.Parse(NormalizeArgs(args))

	report := buildHealthReport(*storeDir, *bridgeAddr)
	if strings.TrimSpace(*bundlePath) != "" {
		if err := writeSupportBundle(strings.TrimSpace(*bundlePath), report, *storeDir, *bridgeAddr); err != nil {
			fmt.Fprintf(os.Stderr, "Error: could not write support bundle: %v\n", err)
			os.Exit(1)
		}
		// In JSON mode stdout stays pure machine-readable; the bundle path
		// goes to stderr so automation can parse stdout safely.
		if *jsonOut {
			fmt.Fprintf(os.Stderr, "Support bundle written to %s\n", strings.TrimSpace(*bundlePath))
		} else {
			fmt.Printf("Support bundle written to %s\n", strings.TrimSpace(*bundlePath))
		}
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		if report.Status != "healthy" {
			os.Exit(1)
		}
		return
	}

	fmt.Printf("tantu doctor (v%s)\n", report.Version)
	if report.StoreDir != "" {
		fmt.Printf("Store: %s\n", report.StoreDir)
	}
	if report.HubRunning {
		fmt.Printf("Hub: running (%s, transport %s)\n", report.HubURL, report.Transport)
	} else {
		fmt.Println("Hub: not running (start with `tantu` or `tantu hub`)")
	}
	if report.Identity != "" {
		fmt.Printf("Identity: SAS %s\n", report.IdentitySAS)
	} else {
		fmt.Println("Identity: missing")
	}
	if len(report.Peers) == 0 {
		fmt.Println("Peers: none trusted yet")
	} else {
		fmt.Printf("Peers: %d trusted\n", len(report.Peers))
		for _, p := range report.Peers {
			marker := ""
			if p.IsDefault {
				marker = " [default]"
			}
			fmt.Printf("  - %s (%s, SAS %s)%s\n", p.Name, p.Address, p.SAS, marker)
		}
	}
	if report.OutputDir != "" {
		fmt.Printf("Downloads: %s\n", report.OutputDir)
	}
	fmt.Println("\nChecks:")
	degraded := false
	for _, c := range report.Checks {
		mark := "ok"
		if !c.OK {
			mark = "attention"
			degraded = true
		}
		fmt.Printf("  [%s] %s: %s\n", mark, c.Name, c.Message)
		if c.NextAction != "" && !c.OK {
			fmt.Printf("        Next: %s\n", c.NextAction)
		}
	}
	fmt.Printf("\nNext action: %s\n", report.NextAction)
	if degraded {
		os.Exit(1)
	}
}

// fetchHubLogs returns the Hub's redacted ring-buffer events. The server
// redacts OAuth query values and shortens URLs before they enter the buffer,
// so this output is safe to share. Empty when the Hub is not running.
func fetchHubLogs(webAddr, storeDir string) ([]hub.LogEvent, error) {
	token := hub.RuntimeIPCTokenForAddr(storeDir, webAddr)
	if token == "" {
		return nil, fmt.Errorf("running Hub runtime metadata is unavailable")
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, "http://"+webAddr+"/api/logs", nil)
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
		return nil, fmt.Errorf("Hub returned logs status %d", resp.StatusCode)
	}
	var events []hub.LogEvent
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&events); err != nil {
		return nil, fmt.Errorf("decode Hub logs: %w", err)
	}
	return events, nil
}

// fetchRelayAttempts returns the Hub's authorization ledger. Only origin
// hosts are ever present; URLs, codes, and tokens never leave the Hub.
func fetchRelayAttempts(webAddr, storeDir string) ([]hub.RelayAttempt, error) {
	token := hub.RuntimeIPCTokenForAddr(storeDir, webAddr)
	if token == "" {
		return nil, fmt.Errorf("running Hub runtime metadata is unavailable")
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, "http://"+webAddr+"/api/relay/recent", nil)
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
		return nil, fmt.Errorf("Hub returned authorizations status %d", resp.StatusCode)
	}
	var attempts []hub.RelayAttempt
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&attempts); err != nil {
		return nil, fmt.Errorf("decode Hub authorizations: %w", err)
	}
	return attempts, nil
}

// writeSupportBundle exports a redacted diagnostic bundle: health report,
// transfer and authorization metadata (live or last-saved), and redacted Hub
// logs. It never contains payload bytes, text snippets, clipboard content,
// tokens, private keys, or full sensitive URLs. Written owner-only (0600).
func writeSupportBundle(path string, report healthReport, storeDir, bridgeAddr string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("bundle path is empty")
	}
	bundle := map[string]any{
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
		"tantu_version":  report.Version,
		"report":         report,
		"redaction_note": "Metadata only: no payload bytes, text snippets, clipboard content, tokens, private keys, or full sensitive URLs.",
	}
	if live, ok := hub.ProbeHubWithStoreDir(bridgeAddr, storeDir); ok && live != nil && strings.TrimSpace(live.WebAddr) != "" {
		if records, err := fetchTransferRecords(live.WebAddr, storeDir); err == nil {
			bundle["transfers"] = records
			bundle["transfers_source"] = "hub-live"
		} else {
			bundle["transfers"] = []transferRecord{}
			bundle["transfers_source"] = "hub-live-unavailable"
		}
		if events, err := fetchHubLogs(live.WebAddr, storeDir); err == nil {
			bundle["logs"] = events
			bundle["logs_source"] = "hub-live"
		} else {
			bundle["logs"] = []hub.LogEvent{}
			bundle["logs_source"] = "hub-live-unavailable"
		}
		if attempts, err := fetchRelayAttempts(live.WebAddr, storeDir); err == nil {
			bundle["authorizations"] = attempts
			bundle["authorizations_source"] = "hub-live"
		} else {
			bundle["authorizations"] = []hub.RelayAttempt{}
			bundle["authorizations_source"] = "hub-live-unavailable"
		}
	} else {
		if saved, err := readSavedTransferRecords(storeDir); err == nil {
			bundle["transfers"] = saved
			bundle["transfers_source"] = "saved-file-stale"
		} else {
			bundle["transfers"] = []transferRecord{}
			bundle["transfers_source"] = "unavailable"
		}
		bundle["logs"] = []hub.LogEvent{}
		bundle["logs_source"] = "hub-not-running"
		if saved, err := hub.ReadSavedRelayHistory(report.StoreDir); err == nil && saved != nil {
			bundle["authorizations"] = saved
			bundle["authorizations_source"] = "saved-file-stale"
		} else {
			bundle["authorizations"] = []hub.RelayAttempt{}
			bundle["authorizations_source"] = "unavailable"
		}
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal support bundle: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write support bundle: %w", err)
	}
	return nil
}
