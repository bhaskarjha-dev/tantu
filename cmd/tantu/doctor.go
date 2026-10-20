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

	// Advisory marks a check that reports something true and actionable about
	// *something other than this installation*.
	//
	// A peer that is not answering is real, worth saying, and has a next action.
	// It is also the normal state of a laptop that has gone to sleep, and folding
	// it into the overall verdict would make `tantu doctor` and
	// `tantu status --json` report the machine degraded and exit 1 for a peer
	// being asleep -- which trains whatever consumes that exit code to ignore it.
	//
	// The distinction is *whose* fault: the store cannot be opened and the
	// identity is missing are faults of this machine and mean degraded. A remote
	// peer is offline is information about another machine.
	Advisory bool `json:"advisory,omitempty"`
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

// peerHealthView mirrors the heartbeat's verdict as /api/status reports it.
//
// A pointer, because the distinction is the whole point: a peer the Hub has not
// probed yet has no answer, and "no answer yet" is not "unreachable". Collapsing
// the two would make every peer read as down for the first seconds after a Hub
// starts and after every restart, which is the fastest way to teach someone not
// to believe a diagnostic.
type peerHealthView struct {
	Reachable           bool      `json:"reachable"`
	LastChecked         time.Time `json:"last_checked"`
	LastOK              time.Time `json:"last_ok"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

// peerReachability is what a running Hub believes about one peer.
type peerReachability struct {
	Name        string          `json:"name"`
	Fingerprint string          `json:"fingerprint"`
	Health      *peerHealthView `json:"health"`
}

// reachVerdict counts what a Hub believes about the peers in the store.
type reachVerdict struct {
	// down is how many peers the Hub has probed and found not answering.
	down int
	// unprobed is how many it has reached no verdict about.
	unprobed int
	// names lists the peers that are not answering, for the message.
	names string
}

// summarisePeerReachability counts a Hub's opinion, matching it against the store
// so a peer it has never heard of counts as unprobed rather than silently
// dropping out of the arithmetic.
//
// A nil opinion -- the Hub could not be asked -- yields every peer unprobed, which
// is what keeps a caller from mistaking "we do not know" for "they are fine".
func summarisePeerReachability(peers []pairing.Peer, opinion []peerReachability) reachVerdict {
	byFingerprint := make(map[string]*peerReachability, len(opinion))
	for i := range opinion {
		byFingerprint[strings.ToLower(opinion[i].Fingerprint)] = &opinion[i]
	}
	v := reachVerdict{}
	var down []string
	for _, p := range peers {
		seen, ok := byFingerprint[strings.ToLower(p.Fingerprint)]
		if !ok || seen.Health == nil || seen.Health.LastChecked.IsZero() {
			v.unprobed++
			continue
		}
		if !seen.Health.Reachable {
			v.down++
			down = append(down, p.DisplayName())
		}
	}
	v.names = strings.Join(down, ", ")
	return v
}

// fetchPeerReachability asks a running Hub what it believes about each peer.
//
// Returns nil when the Hub cannot be asked, which is not the same as "no peer
// answers" -- the caller must read an absent opinion as no opinion.
func fetchPeerReachability(webAddr, storeDir string) []peerReachability {
	token := hub.RuntimeIPCTokenForAddr(storeDir, webAddr)
	if token == "" {
		return nil
	}
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, "http://"+webAddr+"/api/status", nil)
	if err != nil {
		return nil
	}
	req.Header.Set(hub.IPCTokenHeader, token)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()
	var payload struct {
		Peers []peerReachability `json:"peers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil
	}
	return payload.Peers
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

	// The Hub is probed before the peer checks rather than after, because what
	// the Hub believes about its peers is the most useful thing doctor can say,
	// and a check cannot be built from an opinion nobody has asked for.
	live, hubReachable := hub.ProbeHubWithStoreDir(bridgeAddr, storeDir)
	if !hubReachable || live == nil {
		live = nil
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
	// A store that cannot be read is not a store with no peers, and doctor is
	// the command whose whole job is to tell the two apart. Reporting "No
	// trusted peers" for an unreadable store sends the user to pair again with
	// a machine they have already paired, and hides the reason they cannot.
	if fault := store.LastLoadError(); fault != nil {
		report.Checks = append(report.Checks, healthCheck{
			Name:       "peers",
			OK:         false,
			Message:    "The peer store could not be read: " + fault.Error(),
			NextAction: "The file is named in the message. Moving it aside lets Tantu pair again; the unreadable file is not deleted by Tantu.",
		})
	} else if len(peers) == 0 {
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

	// Reachability, as its own check rather than folded into "peers".
	//
	// It did not exist, and "peers: OK" was emitted purely on the presence of
	// peers in the store -- so doctor told a user whose peer had moved networks or
	// gone to sleep that their peers were fine. That is the same false claim the
	// cockpit's banner used to make, on the one surface where it costs most:
	// doctor is the command a person runs when something is already broken, on a
	// machine where something is already broken.
	//
	// Separate on purpose. "Peers are configured" stays true when the Hub is down
	// and should keep saying so -- the store is fine, the Hub is the problem, and
	// a user needs to be told which. Merging them would report one failure for two
	// different causes.
	if live != nil && len(peers) > 0 {
		switch verdict := summarisePeerReachability(peers, fetchPeerReachability(live.WebAddr, storeDir)); {
		case verdict.unprobed == len(peers):
			// No opinion about any of them yet. Saying nothing would lose the fact
			// that the Hub is up and has simply not asked yet; claiming all are
			// reachable would be inventing an answer.
			//
			// Guarded on there being peers at all. The first version ran
			// unconditionally and, with an empty store, emitted "Hub has not
			// probed any peer yet" beside a failing "No trusted peers" check --
			// answering a question nobody asked, marked ok beside a failure.
			// Rendering the output is what found that; no gate did.
			report.Checks = append(report.Checks, healthCheck{
				Name:    "peers_reachability",
				OK:      true,
				Message: "Hub has not probed any peer yet.",
			})
		case verdict.down == 0:
			report.Checks = append(report.Checks, healthCheck{
				Name:    "peers_reachability",
				OK:      true,
				Message: fmt.Sprintf("All %d answering.", len(peers)),
			})
		default:
			// "0 unprobed" is noise. It only carries information when the Hub has
			// a verdict about some peers and not others, which is a different
			// situation from all of them being down.
			msg := fmt.Sprintf("%d of %d not answering (%s).", verdict.down, len(peers), verdict.names)
			if verdict.unprobed > 0 {
				msg += fmt.Sprintf(" %d not yet probed.", verdict.unprobed)
			}
			report.Checks = append(report.Checks, healthCheck{
				Name: "peers_reachability",
				OK:   false,
				// Advisory, not a fault of this machine. A peer asleep is the
				// ordinary state of a laptop, and reporting this installation as
				// degraded for it -- and exiting 1 -- is how an exit code stops
				// being read.
				Advisory:   true,
				Message:    msg,
				NextAction: "A peer that is not answering is usually asleep, on another network, or at a new address. Wake it, or let it broadcast on this network and Tantu will find it.",
			})
		}
	}

	if live != nil {
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
	//
	// Advisory checks are skipped here. They are true and actionable, but they are
	// about another machine, and a sleeping peer must not make this installation
	// report degraded -- see healthCheck.Advisory for why that matters more than it
	// looks.
	finaliseHealthReport(&report)
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
		// Three states, not two. "warning" is a finding about something other
		// than this machine: true, actionable, and not a fault here. Rendering
		// it as "attention" would be wrong in the other direction, implying
		// something is broken that is not.
		mark := "ok"
		switch {
		case c.OK:
		case c.Advisory:
			mark = "warning"
		default:
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

// finaliseHealthReport sets the overall status and the single next action.
//
// The first failing check that is *not* advisory wins, so every problem reported
// has a concrete recovery path and no dead end.
//
// Advisory checks are excluded on purpose. They are true and actionable, but
// they are about another machine: a peer asleep is the ordinary state of a
// laptop, and reporting this installation degraded for it -- and exiting 1 -- is
// how a consumer of that exit code learns to ignore it. That was broken once,
// by adding the reachability check, and is gated by
// TestAnOfflinePeerDoesNotMakeTheInstallationDegraded.
func finaliseHealthReport(report *healthReport) {
	report.Status = "healthy"
	for _, c := range report.Checks {
		if c.OK || c.Advisory {
			continue
		}
		report.Status = "degraded"
		if c.NextAction != "" {
			report.NextAction = c.NextAction
			return
		}
	}
}

// buildHealthReportFromChecks applies the status contract to a set of checks.
//
// It exists so the contract can be gated without standing up a Hub and a store
// per case; buildHealthReport does the real work and ends at finaliseHealthReport.
func buildHealthReportFromChecks(checks []healthCheck) healthReport {
	r := healthReport{Checks: checks, Status: "healthy"}
	finaliseHealthReport(&r)
	return r
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
