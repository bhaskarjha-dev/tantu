package hub

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func chromeBinary() string {
	if runtime.GOOS != "windows" {
		if p, err := exec.LookPath("google-chrome"); err == nil {
			return p
		}
		if p, err := exec.LookPath("chromium"); err == nil {
			return p
		}
		return ""
	}
	candidates := []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// TestWebDashboard_RelayInterstitialBrowser loads the confirmation shell in
// real headless Chrome with an isolated profile: no session cookie exists,
// so the page must settle into its recovery state with zero script errors.
// This exercises the actual parser, fetch, and DOM code paths unit tests
// cannot reach.
func TestWebDashboard_RelayInterstitialBrowser(t *testing.T) {
	chrome := chromeBinary()
	if chrome == "" {
		t.Skip("no Chrome/Chromium binary found")
	}
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	target := "https://accounts.google.com/o/oauth2/v2/auth?client_id=x&code=BROWSERSECRET"
	pageURL := "http://" + h.WebAddr() + "/relay?url=" + url.QueryEscape(target)
	profile := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir="+filepath.Join(profile, "chrome-profile"),
		"--virtual-time-budget=5000",
		"--dump-dom",
		pageURL,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	dom, err := cmd.Output()
	if err != nil {
		t.Fatalf("headless chrome failed: %v\nstderr: %s", err, stderr.String())
	}
	page := string(dom)
	for _, want := range []string{"Relay this authorization?", "accounts.google.com", "Dashboard session not established"} {
		if !strings.Contains(page, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
	for _, leak := range []string{"BROWSERSECRET", "code="} {
		if strings.Contains(page, leak) {
			t.Errorf("rendered page leaked %q", leak)
		}
	}
	for _, bad := range []string{"Uncaught", "ERROR:CONSOLE"} {
		if strings.Contains(stderr.String(), bad) {
			t.Errorf("chrome console error %q in: %s", bad, stderr.String())
		}
	}
}
