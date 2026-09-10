package oauthserver

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"
)

// DefaultCodeTTL is the duration an authorization code remains valid.
const DefaultCodeTTL = 60 * time.Second

// TokenResponse represents a successful OAuth 2.0 token response.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// ErrorResponse represents an OAuth 2.0 error response.
type ErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

type authCodeEntry struct {
	code                string
	clientID            string
	redirectURI         string
	codeChallenge       string
	codeChallengeMethod string
	createdAt           time.Time
	used                bool
}

// Server is a synthetic OAuth 2.0 authorization server with PKCE support.
type Server struct {
	URL     string
	server  *httptest.Server
	codeTTL time.Duration
	mu      sync.Mutex
	codes   map[string]*authCodeEntry
}

// ServerOption allows customizing the synthetic OAuth server.
type ServerOption func(*Server)

// WithCodeTTL sets a custom authorization code time-to-live.
func WithCodeTTL(ttl time.Duration) ServerOption {
	return func(s *Server) {
		s.codeTTL = ttl
	}
}

// New creates and starts a new synthetic OAuth server on a random available port.
// The server supports the authorization code grant with PKCE (S256).
// It binds to 127.0.0.1 only. Call Close() when done.
func New(opts ...ServerOption) *Server {
	s := &Server{
		codeTTL: DefaultCodeTTL,
		codes:   make(map[string]*authCodeEntry),
	}
	for _, opt := range opts {
		opt(s)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", s.handleAuthorize)
	mux.HandleFunc("/token", s.handleToken)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("oauthserver: failed to listen on 127.0.0.1:0: %v", err))
	}

	ts := httptest.NewUnstartedServer(mux)
	ts.Listener = listener
	ts.Start()

	s.server = ts
	s.URL = ts.URL
	return s
}

// Close shuts down the server and releases resources.
func (s *Server) Close() {
	if s.server != nil {
		s.server.Close()
	}
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request", "method must be GET")
		return
	}

	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	responseType := q.Get("response_type")
	state := q.Get("state")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")

	if clientID == "" || redirectURI == "" || codeChallenge == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "client_id, redirect_uri, and code_challenge are required")
		return
	}
	if responseType != "code" {
		writeError(w, http.StatusBadRequest, "unsupported_response_type", "response_type must be code")
		return
	}
	if codeChallengeMethod != "S256" {
		writeError(w, http.StatusBadRequest, "invalid_request", "code_challenge_method must be S256")
		return
	}

	targetURL, err := url.Parse(redirectURI)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid redirect_uri")
		return
	}

	code, err := randomString(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "failed to generate authorization code")
		return
	}

	s.mu.Lock()
	s.codes[code] = &authCodeEntry{
		code:                code,
		clientID:            clientID,
		redirectURI:         redirectURI,
		codeChallenge:       codeChallenge,
		codeChallengeMethod: codeChallengeMethod,
		createdAt:           time.Now(),
		used:                false,
	}
	s.mu.Unlock()

	targetQuery := targetURL.Query()
	targetQuery.Set("code", code)
	if state != "" {
		targetQuery.Set("state", state)
	}
	targetURL.RawQuery = targetQuery.Encode()

	http.Redirect(w, r, targetURL.String(), http.StatusFound)
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request", "method must be POST")
		return
	}

	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "failed to parse form body")
		return
	}

	grantType := r.Form.Get("grant_type")
	code := r.Form.Get("code")
	redirectURI := r.Form.Get("redirect_uri")
	clientID := r.Form.Get("client_id")
	codeVerifier := r.Form.Get("code_verifier")

	if grantType == "" || code == "" || redirectURI == "" || clientID == "" || codeVerifier == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "grant_type, code, redirect_uri, client_id, and code_verifier are required")
		return
	}
	if grantType != "authorization_code" {
		writeError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.codes[code]
	if !exists {
		writeError(w, http.StatusBadRequest, "invalid_grant", "authorization code not found")
		return
	}
	if entry.used {
		writeError(w, http.StatusBadRequest, "invalid_grant", "authorization code has already been used")
		return
	}
	if time.Since(entry.createdAt) > s.codeTTL {
		writeError(w, http.StatusBadRequest, "invalid_grant", "authorization code has expired")
		return
	}
	if entry.clientID != clientID {
		writeError(w, http.StatusBadRequest, "invalid_grant", "client_id mismatch")
		return
	}
	if entry.redirectURI != redirectURI {
		writeError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}

	// Validate PKCE: S256(code_verifier) == code_challenge
	verifierHash := sha256.Sum256([]byte(codeVerifier))
	computedChallenge := base64.RawURLEncoding.EncodeToString(verifierHash[:])

	if subtle.ConstantTimeCompare([]byte(computedChallenge), []byte(entry.codeChallenge)) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed: code_verifier does not match code_challenge")
		return
	}

	// Mark code as used (single-use)
	entry.used = true

	accessToken, err := randomString(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "failed to generate access token")
		return
	}

	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)

	_ = json.NewEncoder(w).Encode(TokenResponse{
		AccessToken: accessToken,
		TokenType:   "Bearer",
		ExpiresIn:   3600,
	})
}

func writeError(w http.ResponseWriter, status int, errCode, errDesc string) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error:            errCode,
		ErrorDescription: errDesc,
	})
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
