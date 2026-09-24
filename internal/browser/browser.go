package browser

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

var (
	// ErrEmptyURL is returned when the provided URL string is empty or whitespace.
	ErrEmptyURL = errors.New("url cannot be empty")
	// ErrInvalidScheme is returned when the URL scheme is not http or https.
	ErrInvalidScheme = errors.New("url scheme must be http or https")
	// ErrMissingHost is returned when the URL has no host component.
	ErrMissingHost = errors.New("url must have a host")
)

// commandStarter abstracts command execution for testability.
var startCommand = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Start returns before the browser exits. Reap the child in the
	// background so repeated OAuth launches do not accumulate zombies or
	// process handles. The browser itself remains detached from the bridge
	// session and can outlive this process.
	go func() { _ = cmd.Wait() }()
	return nil
}

// ValidateURL checks that the URL is safe to open in a browser.
// It MUST reject any scheme other than "http" or "https".
// It MUST reject empty URLs and URLs without a host component.
func ValidateURL(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ErrEmptyURL
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		// url.Parse errors may echo the complete input, which can contain an
		// authorization code, state, or PKCE verifier. Keep validation errors
		// deliberately generic so callers cannot accidentally log credentials.
		return errors.New("invalid url")
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: got %q", ErrInvalidScheme, parsed.Scheme)
	}

	if strings.TrimSpace(parsed.Hostname()) == "" {
		return ErrMissingHost
	}

	return nil
}

// browserCommand returns the command name and arguments for opening a URL
// on the given operating system.
func browserCommand(goos, rawURL string) (string, []string) {
	switch goos {
	case "windows":
		// Use rundll32 to open URLs directly, avoiding cmd.exe shell
		// metacharacter interpretation that corrupts long OAuth URLs
		// containing %, &, ^, and other special characters.
		return "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	case "darwin":
		return "open", []string{rawURL}
	default:
		// linux, freebsd, openbsd, netbsd, etc.
		return "xdg-open", []string{rawURL}
	}
}

// SanitizeURL normalizes OAuth authorization URLs before handling them.
// When an OAuth flow begins in a browser on Machine B, Google redirects to intermediate
// session pages like accounts.google.com/v3/signin/accountchooser with device/session-specific
// parameters (dsh, continue, part, as). If forwarded to Machine A's browser,
// Google's anti-spoofing check fails with "400 malformed request" upon selecting an account.
// SanitizeURL detects Google OAuth sign-in URLs and converts them back to the canonical
// authorization endpoint (https://accounts.google.com/o/oauth2/v2/auth), preserving only
// standard OAuth 2.0 query parameters and discarding machine/browser-specific session tokens.
func SanitizeURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return rawURL
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return rawURL
	}

	if strings.EqualFold(parsed.Hostname(), "accounts.google.com") &&
		(strings.Contains(parsed.Path, "signin") || strings.Contains(parsed.Path, "accountchooser")) {

		q := parsed.Query()

		// If parameters are nested inside 'continue', pull them in
		if q.Get("client_id") == "" && q.Get("continue") != "" {
			if contURL, err := url.Parse(q.Get("continue")); err == nil {
				contQ := contURL.Query()
				for k, v := range contQ {
					if _, exists := q[k]; !exists {
						q[k] = v
					}
				}
			}
		}

		clientID := q.Get("client_id")
		redirectURI := q.Get("redirect_uri")
		if clientID != "" && redirectURI != "" {
			cleanQ := url.Values{}
			allowed := []string{
				"client_id", "redirect_uri", "response_type", "scope", "state",
				"access_type", "prompt", "code_challenge", "code_challenge_method",
				"nonce", "login_hint", "hd", "include_granted_scopes", "enable_granular_consent",
			}
			for _, k := range allowed {
				if vals, ok := q[k]; ok {
					cleanQ[k] = vals
				}
			}
			if cleanQ.Get("response_type") == "" {
				cleanQ.Set("response_type", "code")
			}

			parsed.Path = "/o/oauth2/v2/auth"
			parsed.RawQuery = cleanQ.Encode()
			return parsed.String()
		}
	}

	return rawURL
}

// RedactURL returns a log-safe representation of a URL.  OAuth query strings
// routinely contain authorization state, PKCE challenges, and callback codes;
// none of those values belong in application logs or the dashboard event ring.
func RedactURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "<empty-url>"
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<invalid-url>"
	}
	parsed.User = nil
	parsed.Fragment = ""
	parsed.RawFragment = ""
	if parsed.RawQuery != "" {
		parsed.RawQuery = "redacted"
		parsed.ForceQuery = true
	}
	return parsed.String()
}

// RedactRequestURI is the request-path equivalent of RedactURL.  It is used
// for callback logs, where the query string can contain a one-time code.
func RedactRequestURI(requestURI string) string {
	parsed, err := url.ParseRequestURI(requestURI)
	if err != nil {
		return "<invalid-request-uri>"
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = strings.Contains(requestURI, "?")
	parsed.Fragment = ""
	parsed.RawFragment = ""
	if parsed.ForceQuery {
		return parsed.String() + "?redacted"
	}
	return parsed.String()
}

// OpenURL validates and opens the given URL in the system's default browser.
// It returns an error if the URL is invalid or the browser command fails to start.
func OpenURL(rawURL string) error {
	rawURL = strings.TrimSpace(SanitizeURL(rawURL))
	if err := ValidateURL(rawURL); err != nil {
		return err
	}

	name, args := browserCommand(runtime.GOOS, rawURL)
	if err := startCommand(name, args...); err != nil {
		return fmt.Errorf("failed to launch browser (%s %s): %w", name, strings.Join([]string{RedactURL(rawURL)}, " "), err)
	}

	return nil
}
