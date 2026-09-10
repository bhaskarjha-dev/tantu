package testutil_test

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/testutil/oauthclient"
	"github.com/bhaskarjha-dev/tantu/internal/testutil/oauthserver"
)

func TestE2E_HappyPath(t *testing.T) {
	start := time.Now()

	srv := oauthserver.New()
	defer srv.Close()

	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-app",
		RedirectPath:  "/callback",
		Timeout:       5 * time.Second,
	})
	defer client.Close()

	authURL, waitAndExchange, err := client.DoFlow()
	if err != nil {
		t.Fatalf("DoFlow failed: %v", err)
	}

	// Browser simulation: HTTP GET to authURL following all redirects to the callback listener
	errChan := make(chan error, 1)
	go func() {
		browserClient := &http.Client{
			Timeout: 4 * time.Second,
		}
		resp, err := browserClient.Get(authURL)
		if err != nil {
			errChan <- fmt.Errorf("browser visit failed: %w", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			errChan <- fmt.Errorf("browser expected final status 200 OK from callback, got %d: %s", resp.StatusCode, string(body))
			return
		}
		errChan <- nil
	}()

	result, err := waitAndExchange()
	if err != nil {
		t.Fatalf("waitAndExchange failed: %v", err)
	}

	if browserErr := <-errChan; browserErr != nil {
		t.Fatalf("browser error: %v", browserErr)
	}

	if result.AccessToken == "" {
		t.Errorf("expected non-empty AccessToken")
	}
	if result.TokenType != "Bearer" {
		t.Errorf("expected TokenType 'Bearer', got %q", result.TokenType)
	}
	if result.ExpiresIn != 3600 {
		t.Errorf("expected ExpiresIn 3600, got %d", result.ExpiresIn)
	}

	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Errorf("expected flow to complete in under 5 seconds, took %v", elapsed)
	}
}

func TestE2E_PKCEMismatch(t *testing.T) {
	srv := oauthserver.New()
	defer srv.Close()

	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-app",
		RedirectPath:  "/callback",
		Timeout:       5 * time.Second,
	})
	defer client.Close()

	if err := client.StartCallbackServer(); err != nil {
		t.Fatalf("StartCallbackServer failed: %v", err)
	}

	authURL, err := client.BuildAuthURL()
	if err != nil {
		t.Fatalf("BuildAuthURL failed: %v", err)
	}

	go func() {
		browserClient := &http.Client{Timeout: 3 * time.Second}
		resp, err := browserClient.Get(authURL)
		if err == nil {
			resp.Body.Close()
		}
	}()

	code, err := client.WaitForCallback()
	if err != nil {
		t.Fatalf("WaitForCallback failed: %v", err)
	}

	// Tamper with the code_verifier by performing direct exchange with invalid verifier
	tokenURL := srv.URL + "/token"
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {client.CallbackURL},
		"client_id":     {"synthetic-app"},
		"code_verifier": {"tampered-verifier-wrong-content-123456789012345"},
	}

	resp, err := http.PostForm(tokenURL, form)
	if err != nil {
		t.Fatalf("POST to /token failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400 Bad Request on PKCE mismatch, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "invalid_grant") {
		t.Errorf("expected error response to contain 'invalid_grant', got: %s", string(body))
	}
}

func TestE2E_ExpiredCode(t *testing.T) {
	// Set very short TTL (50ms) to test expiration quickly
	srv := oauthserver.New(oauthserver.WithCodeTTL(50 * time.Millisecond))
	defer srv.Close()

	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-app",
		RedirectPath:  "/callback",
		Timeout:       5 * time.Second,
	})
	defer client.Close()

	if err := client.StartCallbackServer(); err != nil {
		t.Fatalf("StartCallbackServer failed: %v", err)
	}

	authURL, err := client.BuildAuthURL()
	if err != nil {
		t.Fatalf("BuildAuthURL failed: %v", err)
	}

	go func() {
		browserClient := &http.Client{Timeout: 3 * time.Second}
		resp, err := browserClient.Get(authURL)
		if err == nil {
			resp.Body.Close()
		}
	}()

	code, err := client.WaitForCallback()
	if err != nil {
		t.Fatalf("WaitForCallback failed: %v", err)
	}

	// Wait for code to expire
	time.Sleep(100 * time.Millisecond)

	_, err = client.ExchangeCode(code)
	if err == nil {
		t.Fatalf("expected ExchangeCode to fail on expired code, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("expected error to contain 'invalid_grant', got: %v", err)
	}
}

func TestE2E_StateMismatch(t *testing.T) {
	srv := oauthserver.New()
	defer srv.Close()

	client := oauthclient.New(oauthclient.Config{
		AuthServerURL: srv.URL,
		ClientID:      "synthetic-app",
		RedirectPath:  "/callback",
		Timeout:       1 * time.Second,
	})
	defer client.Close()

	if err := client.StartCallbackServer(); err != nil {
		t.Fatalf("StartCallbackServer failed: %v", err)
	}

	if _, err := client.BuildAuthURL(); err != nil {
		t.Fatalf("BuildAuthURL failed: %v", err)
	}

	// Simulate forged callback with invalid state
	resp, err := http.Get(fmt.Sprintf("%s?code=fake-code&state=forged-state-12345", client.CallbackURL))
	if err != nil {
		t.Fatalf("GET callback failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 on forged state, got %d", resp.StatusCode)
	}

	_, err = client.WaitForCallback()
	if err == nil {
		t.Fatalf("expected WaitForCallback to return error on state mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("expected error to contain 'state mismatch', got: %v", err)
	}
}
