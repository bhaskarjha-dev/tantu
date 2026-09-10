package oauthclient

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClient_DynamicPortAndPKCE(t *testing.T) {
	client := New(Config{
		AuthServerURL: "http://example.com/oauth",
		ClientID:      "test-client-123",
		RedirectPath:  "/callback",
	})
	defer client.Close()

	if err := client.StartCallbackServer(); err != nil {
		t.Fatalf("StartCallbackServer failed: %v", err)
	}

	if !strings.HasPrefix(client.CallbackURL, "http://127.0.0.1:") {
		t.Fatalf("CallbackURL should start with http://127.0.0.1:, got %q", client.CallbackURL)
	}
	if !strings.HasSuffix(client.CallbackURL, "/callback") {
		t.Fatalf("CallbackURL should end with /callback, got %q", client.CallbackURL)
	}

	authURLStr, err := client.BuildAuthURL()
	if err != nil {
		t.Fatalf("BuildAuthURL failed: %v", err)
	}

	authURL, err := url.Parse(authURLStr)
	if err != nil {
		t.Fatalf("failed to parse AuthURL: %v", err)
	}

	q := authURL.Query()
	if q.Get("client_id") != "test-client-123" {
		t.Errorf("expected client_id test-client-123, got %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != client.CallbackURL {
		t.Errorf("expected redirect_uri %q, got %q", client.CallbackURL, q.Get("redirect_uri"))
	}
	if q.Get("response_type") != "code" {
		t.Errorf("expected response_type code, got %q", q.Get("response_type"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("expected code_challenge_method S256, got %q", q.Get("code_challenge_method"))
	}

	challenge := q.Get("code_challenge")
	if challenge == "" {
		t.Errorf("expected non-empty code_challenge")
	}

	// Verify PKCE match
	client.mu.Lock()
	verifier := client.codeVerifier
	client.mu.Unlock()

	if len(verifier) < 43 || len(verifier) > 128 {
		t.Errorf("code_verifier length %d out of bounds [43, 128]", len(verifier))
	}
	h := sha256.Sum256([]byte(verifier))
	expectedChallenge := base64.RawURLEncoding.EncodeToString(h[:])
	if challenge != expectedChallenge {
		t.Errorf("challenge mismatch: expected %q, got %q", expectedChallenge, challenge)
	}
}

func TestClient_HappyPathFlow(t *testing.T) {
	var tokenReqMutex sync.Mutex
	var receivedForm url.Values

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			tokenReqMutex.Lock()
			receivedForm = r.Form
			tokenReqMutex.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "mock-access-token-999",
				"token_type":   "Bearer",
				"expires_in":   3600,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	client := New(Config{
		AuthServerURL: mockServer.URL,
		ClientID:      "my-client-app",
		RedirectPath:  "/auth/callback",
		Timeout:       2 * time.Second,
	})
	defer client.Close()

	authURL, waitAndExchange, err := client.DoFlow()
	if err != nil {
		t.Fatalf("DoFlow failed: %v", err)
	}

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("failed to parse authURL: %v", err)
	}
	state := u.Query().Get("state")

	// Simulate callback to client's listener
	callbackResp, err := http.Get(fmt.Sprintf("%s?code=test-auth-code&state=%s", client.CallbackURL, state))
	if err != nil {
		t.Fatalf("GET callback failed: %v", err)
	}
	callbackResp.Body.Close()
	if callbackResp.StatusCode != http.StatusOK {
		t.Fatalf("callback returned status %d", callbackResp.StatusCode)
	}

	result, err := waitAndExchange()
	if err != nil {
		t.Fatalf("waitAndExchange failed: %v", err)
	}

	if result.AccessToken != "mock-access-token-999" {
		t.Errorf("expected access token mock-access-token-999, got %q", result.AccessToken)
	}
	if result.TokenType != "Bearer" {
		t.Errorf("expected token type Bearer, got %q", result.TokenType)
	}
	if result.ExpiresIn != 3600 {
		t.Errorf("expected expires_in 3600, got %d", result.ExpiresIn)
	}

	tokenReqMutex.Lock()
	defer tokenReqMutex.Unlock()
	if receivedForm.Get("grant_type") != "authorization_code" {
		t.Errorf("expected grant_type authorization_code, got %q", receivedForm.Get("grant_type"))
	}
	if receivedForm.Get("code") != "test-auth-code" {
		t.Errorf("expected code test-auth-code, got %q", receivedForm.Get("code"))
	}
	if receivedForm.Get("redirect_uri") != client.CallbackURL {
		t.Errorf("expected redirect_uri %q, got %q", client.CallbackURL, receivedForm.Get("redirect_uri"))
	}
	if receivedForm.Get("client_id") != "my-client-app" {
		t.Errorf("expected client_id my-client-app, got %q", receivedForm.Get("client_id"))
	}
	if receivedForm.Get("code_verifier") == "" {
		t.Errorf("expected non-empty code_verifier in token exchange")
	}
}

func TestClient_StateMismatch(t *testing.T) {
	client := New(Config{
		AuthServerURL: "http://127.0.0.1:5000",
		ClientID:      "test-client",
		Timeout:       1 * time.Second,
	})
	defer client.Close()

	if err := client.StartCallbackServer(); err != nil {
		t.Fatalf("StartCallbackServer failed: %v", err)
	}
	if _, err := client.BuildAuthURL(); err != nil {
		t.Fatalf("BuildAuthURL failed: %v", err)
	}

	// Send callback with wrong state
	resp, err := http.Get(fmt.Sprintf("%s?code=test-code&state=wrong-state", client.CallbackURL))
	if err != nil {
		t.Fatalf("callback GET failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 on state mismatch, got %d", resp.StatusCode)
	}

	_, err = client.WaitForCallback()
	if err == nil {
		t.Fatalf("expected error from WaitForCallback on state mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("expected error message to contain 'state mismatch', got %q", err.Error())
	}
}

func TestClient_CallbackErrorResponse(t *testing.T) {
	client := New(Config{
		AuthServerURL: "http://127.0.0.1:5000",
		ClientID:      "test-client",
		Timeout:       1 * time.Second,
	})
	defer client.Close()

	if err := client.StartCallbackServer(); err != nil {
		t.Fatalf("StartCallbackServer failed: %v", err)
	}
	if _, err := client.BuildAuthURL(); err != nil {
		t.Fatalf("BuildAuthURL failed: %v", err)
	}

	resp, err := http.Get(fmt.Sprintf("%s?error=access_denied&error_description=user_cancelled", client.CallbackURL))
	if err != nil {
		t.Fatalf("callback GET failed: %v", err)
	}
	resp.Body.Close()

	_, err = client.WaitForCallback()
	if err == nil {
		t.Fatalf("expected error from WaitForCallback on OAuth error, got nil")
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("expected error message to contain 'access_denied', got %q", err.Error())
	}
}

func TestClient_Timeout(t *testing.T) {
	client := New(Config{
		AuthServerURL: "http://127.0.0.1:5000",
		ClientID:      "test-client",
		Timeout:       50 * time.Millisecond,
	})
	defer client.Close()

	if err := client.StartCallbackServer(); err != nil {
		t.Fatalf("StartCallbackServer failed: %v", err)
	}

	_, err := client.WaitForCallback()
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout waiting for OAuth callback") {
		t.Errorf("expected timeout message, got %q", err.Error())
	}
}

func TestClient_TokenExchangeError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "code expired",
		})
	}))
	defer mockServer.Close()

	client := New(Config{
		AuthServerURL: mockServer.URL,
		ClientID:      "test-client",
	})
	defer client.Close()

	_ = client.StartCallbackServer()
	_, _ = client.BuildAuthURL()

	_, err := client.ExchangeCode("bad-code")
	if err == nil {
		t.Fatalf("expected error on token exchange failure, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_grant") || !strings.Contains(err.Error(), "code expired") {
		t.Errorf("expected error to contain 'invalid_grant' and 'code expired', got %q", err.Error())
	}
}
