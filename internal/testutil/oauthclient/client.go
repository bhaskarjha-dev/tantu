package oauthclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout is the default duration to wait for an OAuth callback.
const DefaultTimeout = 5 * time.Minute

// Config holds the configuration for an OAuth client flow.
type Config struct {
	AuthServerURL string        // Base URL of the OAuth authorization server
	ClientID      string        // OAuth client ID
	RedirectPath  string        // Callback path (e.g., "/callback" or "/auth/callback")
	Timeout       time.Duration // How long to wait for the callback (default: 5 minutes)
}

// Result contains the outcome of a successful OAuth flow.
type Result struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

type callbackData struct {
	code string
	err  error
}

// Client performs OAuth authorization code flows with PKCE.
type Client struct {
	cfg          Config
	CallbackURL  string
	AuthURL      string
	codeVerifier string
	state        string

	server       *http.Server
	listener     net.Listener
	callbackChan chan callbackData
	closeOnce    sync.Once
	mu           sync.Mutex
}

// New creates a new OAuth client with the given configuration.
func New(cfg Config) *Client {
	if cfg.RedirectPath == "" {
		cfg.RedirectPath = "/callback"
	}
	if !strings.HasPrefix(cfg.RedirectPath, "/") {
		cfg.RedirectPath = "/" + cfg.RedirectPath
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}

	return &Client{
		cfg:          cfg,
		callbackChan: make(chan callbackData, 1),
	}
}

// StartCallbackServer starts the localhost HTTP listener on a dynamic port (127.0.0.1:0).
// Sets c.CallbackURL. Must be called before BuildAuthURL.
func (c *Client) StartCallbackServer() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.listener != nil {
		return errors.New("callback server already started")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to listen on 127.0.0.1:0: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	c.listener = listener
	c.CallbackURL = fmt.Sprintf("http://127.0.0.1:%d%s", port, c.cfg.RedirectPath)

	mux := http.NewServeMux()
	mux.HandleFunc(c.cfg.RedirectPath, c.handleCallback)

	c.server = &http.Server{
		Handler: mux,
	}

	go func() {
		if err := c.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case c.callbackChan <- callbackData{err: fmt.Errorf("callback server error: %w", err)}:
			default:
			}
		}
	}()

	return nil
}

// BuildAuthURL constructs the OAuth authorization URL with PKCE parameters.
// Sets c.AuthURL. Requires StartCallbackServer to be called first.
func (c *Client) BuildAuthURL() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.CallbackURL == "" {
		return "", errors.New("callback server must be started before building auth URL")
	}

	// Generate PKCE code_verifier (43 unreserved chars from 32 cryptographically random bytes)
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", fmt.Errorf("failed to generate code_verifier: %w", err)
	}
	c.codeVerifier = base64.RawURLEncoding.EncodeToString(verifierBytes)

	// Compute code_challenge = base64url(sha256(code_verifier))
	h := sha256.Sum256([]byte(c.codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

	// Generate random state (32 bytes hex)
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}
	c.state = hex.EncodeToString(stateBytes)

	base := strings.TrimRight(c.cfg.AuthServerURL, "/") + "/authorize"
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid auth server URL: %w", err)
	}

	q := u.Query()
	q.Set("client_id", c.cfg.ClientID)
	q.Set("redirect_uri", c.CallbackURL)
	q.Set("response_type", "code")
	q.Set("state", c.state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()

	c.AuthURL = u.String()
	return c.AuthURL, nil
}

func (c *Client) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	errParam := q.Get("error")
	if errParam != "" {
		errDesc := q.Get("error_description")
		if errDesc == "" {
			errDesc = "no description provided"
		}
		http.Error(w, fmt.Sprintf("OAuth error: %s (%s)", errParam, errDesc), http.StatusBadRequest)
		select {
		case c.callbackChan <- callbackData{err: fmt.Errorf("oauth callback error: %s (%s)", errParam, errDesc)}:
		default:
		}
		go c.Close()
		return
	}

	state := q.Get("state")
	c.mu.Lock()
	expectedState := c.state
	c.mu.Unlock()

	if state != expectedState {
		http.Error(w, "State mismatch", http.StatusBadRequest)
		select {
		case c.callbackChan <- callbackData{err: fmt.Errorf("state mismatch: expected %q, got %q", expectedState, state)}:
		default:
		}
		go c.Close()
		return
	}

	code := q.Get("code")
	if code == "" {
		http.Error(w, "Missing code parameter", http.StatusBadRequest)
		select {
		case c.callbackChan <- callbackData{err: errors.New("missing code parameter in callback")}:
		default:
		}
		go c.Close()
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<html><body><h1>Authentication successful</h1><p>You may close this tab.</p></body></html>"))

	select {
	case c.callbackChan <- callbackData{code: code}:
	default:
	}

	go c.Close()
}

// WaitForCallback blocks until the OAuth callback is received or timeout.
// Returns the authorization code extracted from the callback.
func (c *Client) WaitForCallback() (string, error) {
	select {
	case res := <-c.callbackChan:
		if res.err != nil {
			return "", res.err
		}
		return res.code, nil
	case <-time.After(c.cfg.Timeout):
		c.Close()
		return "", fmt.Errorf("timeout waiting for OAuth callback after %v", c.cfg.Timeout)
	}
}

// ExchangeCode exchanges the authorization code for an access token via POST /token.
// Sends the code_verifier for PKCE validation.
func (c *Client) ExchangeCode(code string) (*Result, error) {
	c.mu.Lock()
	verifier := c.codeVerifier
	callbackURL := c.CallbackURL
	c.mu.Unlock()

	if verifier == "" {
		return nil, errors.New("code_verifier is missing; BuildAuthURL must be called before ExchangeCode")
	}

	tokenEndpoint := strings.TrimRight(c.cfg.AuthServerURL, "/") + "/token"
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {callbackURL},
		"client_id":     {c.cfg.ClientID},
		"code_verifier": {verifier},
	}

	req, err := http.NewRequest(http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if decodeErr := json.NewDecoder(resp.Body).Decode(&errResp); decodeErr == nil && errResp.Error != "" {
			return nil, fmt.Errorf("token exchange failed (%s): %s", errResp.Error, errResp.ErrorDescription)
		}
		return nil, fmt.Errorf("token exchange failed with status HTTP %d", resp.StatusCode)
	}

	var res Result
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed to decode token response: %w", err)
	}

	return &res, nil
}

// Close shuts down the callback server and releases resources.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		if c.server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = c.server.Shutdown(ctx)
		}
		if c.listener != nil {
			_ = c.listener.Close()
		}
	})
}

// DoFlow is a convenience method that runs the complete flow:
// StartCallbackServer → BuildAuthURL → (caller opens URL) → WaitForCallback → ExchangeCode
// Returns the result and the authorization URL that needs to be opened.
func (c *Client) DoFlow() (string, func() (*Result, error), error) {
	if err := c.StartCallbackServer(); err != nil {
		return "", nil, fmt.Errorf("failed to start callback server: %w", err)
	}

	authURL, err := c.BuildAuthURL()
	if err != nil {
		c.Close()
		return "", nil, fmt.Errorf("failed to build auth URL: %w", err)
	}

	waitAndExchange := func() (*Result, error) {
		code, err := c.WaitForCallback()
		if err != nil {
			return nil, err
		}
		return c.ExchangeCode(code)
	}

	return authURL, waitAndExchange, nil
}
