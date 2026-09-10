package oauthserver

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func generatePKCEPair(verifier string) (string, string) {
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge
}

func TestOAuthServer_HappyPath(t *testing.T) {
	srv := New()
	defer srv.Close()

	verifier, challenge := generatePKCEPair("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	clientID := "test-client-id"
	redirectURI := "http://127.0.0.1:8080/callback"
	state := "xyz123"

	// 1. GET /authorize
	authURL, err := url.Parse(srv.URL + "/authorize")
	if err != nil {
		t.Fatalf("url.Parse failed: %v", err)
	}
	q := authURL.Query()
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authURL.RawQuery = q.Encode()

	// Client that does not follow redirects automatically
	noRedirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := noRedirectClient.Get(authURL.String())
	if err != nil {
		t.Fatalf("GET /authorize failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected status 302 Found, got %d", resp.StatusCode)
	}

	locHeader := resp.Header.Get("Location")
	locURL, err := url.Parse(locHeader)
	if err != nil {
		t.Fatalf("failed to parse Location URL %q: %v", locHeader, err)
	}

	if locURL.Query().Get("state") != state {
		t.Fatalf("expected state %q, got %q", state, locURL.Query().Get("state"))
	}
	code := locURL.Query().Get("code")
	if code == "" {
		t.Fatalf("expected code parameter in redirect URL")
	}

	// 2. POST /token
	tokenValues := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}

	tokenResp, err := http.PostForm(srv.URL+"/token", tokenValues)
	if err != nil {
		t.Fatalf("POST /token failed: %v", err)
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tokenResp.Body)
		t.Fatalf("expected status 200 OK, got %d. Body: %s", tokenResp.StatusCode, string(body))
	}

	var tok TokenResponse
	if err := json.NewDecoder(tokenResp.Body).Decode(&tok); err != nil {
		t.Fatalf("failed to decode token response: %v", err)
	}

	if tok.AccessToken == "" {
		t.Errorf("expected non-empty access_token")
	}
	if tok.TokenType != "Bearer" {
		t.Errorf("expected token_type 'Bearer', got %q", tok.TokenType)
	}
	if tok.ExpiresIn != 3600 {
		t.Errorf("expected expires_in 3600, got %d", tok.ExpiresIn)
	}
}

func TestOAuthServer_PKCEMismatch(t *testing.T) {
	srv := New()
	defer srv.Close()

	_, challenge := generatePKCEPair("correct-verifier-with-sufficient-entropy-12345")
	clientID := "test-client"
	redirectURI := "http://127.0.0.1:9090/callback"

	// GET /authorize
	authReqURL := fmt.Sprintf("%s/authorize?client_id=%s&redirect_uri=%s&response_type=code&code_challenge=%s&code_challenge_method=S256",
		srv.URL, clientID, url.QueryEscape(redirectURI), challenge)

	noRedirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := noRedirectClient.Get(authReqURL)
	if err != nil {
		t.Fatalf("GET /authorize failed: %v", err)
	}
	defer resp.Body.Close()

	locURL, _ := url.Parse(resp.Header.Get("Location"))
	code := locURL.Query().Get("code")

	// POST /token with wrong verifier
	tokenValues := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {"wrong-verifier-1234567890123456789012345"},
	}

	tokenResp, err := http.PostForm(srv.URL+"/token", tokenValues)
	if err != nil {
		t.Fatalf("POST /token failed: %v", err)
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400 Bad Request, got %d", tokenResp.StatusCode)
	}

	var errResp ErrorResponse
	_ = json.NewDecoder(tokenResp.Body).Decode(&errResp)
	if errResp.Error != "invalid_grant" {
		t.Fatalf("expected error 'invalid_grant', got %q", errResp.Error)
	}
}

func TestOAuthServer_CodeReuse(t *testing.T) {
	srv := New()
	defer srv.Close()

	verifier, challenge := generatePKCEPair("my-secure-pkce-code-verifier-0123456789")
	clientID := "client-test"
	redirectURI := "http://127.0.0.1:9999/cb"

	noRedirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	authReqURL := fmt.Sprintf("%s/authorize?client_id=%s&redirect_uri=%s&response_type=code&code_challenge=%s&code_challenge_method=S256",
		srv.URL, clientID, url.QueryEscape(redirectURI), challenge)
	resp, _ := noRedirectClient.Get(authReqURL)
	resp.Body.Close()

	locURL, _ := url.Parse(resp.Header.Get("Location"))
	code := locURL.Query().Get("code")

	tokenValues := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}

	// 1st exchange: success
	resp1, err := http.PostForm(srv.URL+"/token", tokenValues)
	if err != nil {
		t.Fatalf("POST 1 failed: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on first exchange, got %d", resp1.StatusCode)
	}

	// 2nd exchange with same code: must fail
	resp2, err := http.PostForm(srv.URL+"/token", tokenValues)
	if err != nil {
		t.Fatalf("POST 2 failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on code reuse, got %d", resp2.StatusCode)
	}
	var errResp ErrorResponse
	_ = json.NewDecoder(resp2.Body).Decode(&errResp)
	if errResp.Error != "invalid_grant" {
		t.Fatalf("expected error 'invalid_grant', got %q", errResp.Error)
	}
}

func TestOAuthServer_CodeExpiration(t *testing.T) {
	// Set TTL very short: 50ms
	srv := New(WithCodeTTL(50 * time.Millisecond))
	defer srv.Close()

	verifier, challenge := generatePKCEPair("my-verifier-string-for-expiration-test")
	clientID := "client-test"
	redirectURI := "http://127.0.0.1:9999/cb"

	noRedirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	authReqURL := fmt.Sprintf("%s/authorize?client_id=%s&redirect_uri=%s&response_type=code&code_challenge=%s&code_challenge_method=S256",
		srv.URL, clientID, url.QueryEscape(redirectURI), challenge)
	resp, _ := noRedirectClient.Get(authReqURL)
	resp.Body.Close()

	locURL, _ := url.Parse(resp.Header.Get("Location"))
	code := locURL.Query().Get("code")

	// Wait for code to expire
	time.Sleep(100 * time.Millisecond)

	tokenValues := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}

	respToken, err := http.PostForm(srv.URL+"/token", tokenValues)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer respToken.Body.Close()

	if respToken.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on expired code, got %d", respToken.StatusCode)
	}
	var errResp ErrorResponse
	_ = json.NewDecoder(respToken.Body).Decode(&errResp)
	if errResp.Error != "invalid_grant" {
		t.Fatalf("expected error 'invalid_grant', got %q", errResp.Error)
	}
}

func TestOAuthServer_RedirectAndClientValidation(t *testing.T) {
	srv := New()
	defer srv.Close()

	verifier, challenge := generatePKCEPair("my-verifier-redirect-client-check-12345")
	clientID := "correct-client"
	redirectURI := "http://127.0.0.1:8888/cb"

	noRedirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	authReqURL := fmt.Sprintf("%s/authorize?client_id=%s&redirect_uri=%s&response_type=code&code_challenge=%s&code_challenge_method=S256",
		srv.URL, clientID, url.QueryEscape(redirectURI), challenge)
	resp, _ := noRedirectClient.Get(authReqURL)
	resp.Body.Close()

	locURL, _ := url.Parse(resp.Header.Get("Location"))
	code := locURL.Query().Get("code")

	// 1. Wrong redirect_uri
	valsWrongURI := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {"http://127.0.0.1:8888/other-cb"},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	r1, _ := http.PostForm(srv.URL+"/token", valsWrongURI)
	r1.Body.Close()
	if r1.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for wrong redirect_uri, got %d", r1.StatusCode)
	}

	// 2. Wrong client_id
	valsWrongClient := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {"wrong-client"},
		"code_verifier": {verifier},
	}
	r2, _ := http.PostForm(srv.URL+"/token", valsWrongClient)
	r2.Body.Close()
	if r2.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for wrong client_id, got %d", r2.StatusCode)
	}

	// 3. Unknown code
	valsUnknownCode := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"nonexistent-code-123"},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	r3, _ := http.PostForm(srv.URL+"/token", valsUnknownCode)
	r3.Body.Close()
	if r3.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown code, got %d", r3.StatusCode)
	}
}

func TestOAuthServer_ValidationErrors(t *testing.T) {
	srv := New()
	defer srv.Close()

	// Missing params on /authorize
	resp, err := http.Get(srv.URL + "/authorize?client_id=1")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing authorize params, got %d", resp.StatusCode)
	}

	// Unsupported response_type
	resp2, err := http.Get(srv.URL + "/authorize?client_id=1&redirect_uri=http://localhost&response_type=token&code_challenge=xyz&code_challenge_method=S256")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for response_type=token, got %d", resp2.StatusCode)
	}

	// Unsupported code_challenge_method (plain)
	resp3, err := http.Get(srv.URL + "/authorize?client_id=1&redirect_uri=http://localhost&response_type=code&code_challenge=xyz&code_challenge_method=plain")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for code_challenge_method=plain, got %d", resp3.StatusCode)
	}

	// Missing params on /token
	respToken, err := http.PostForm(srv.URL+"/token", url.Values{"grant_type": {"authorization_code"}})
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	respToken.Body.Close()
	if respToken.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing token params, got %d", respToken.StatusCode)
	}

	// Unsupported grant_type on /token
	respGrant, err := http.PostForm(srv.URL+"/token", url.Values{
		"grant_type":    {"password"},
		"code":          {"abc"},
		"redirect_uri":  {"http://localhost"},
		"client_id":     {"cid"},
		"code_verifier": {"cv"},
	})
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	respGrant.Body.Close()
	if respGrant.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for unsupported grant_type, got %d", respGrant.StatusCode)
	}
}

func TestOAuthServer_ConcurrentAccess(t *testing.T) {
	srv := New()
	defer srv.Close()

	const concurrency = 10
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()

			verifier, challenge := generatePKCEPair(fmt.Sprintf("verifier-concurrency-%03d-123456789012345678", idx))
			clientID := fmt.Sprintf("client-%d", idx)
			redirectURI := fmt.Sprintf("http://127.0.0.1:%d/cb", 10000+idx)

			authURL := fmt.Sprintf("%s/authorize?client_id=%s&redirect_uri=%s&response_type=code&state=st-%d&code_challenge=%s&code_challenge_method=S256",
				srv.URL, clientID, url.QueryEscape(redirectURI), idx, challenge)

			client := &http.Client{
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
			resp, err := client.Get(authURL)
			if err != nil {
				t.Errorf("[%d] authorize failed: %v", idx, err)
				return
			}
			resp.Body.Close()

			locURL, err := url.Parse(resp.Header.Get("Location"))
			if err != nil {
				t.Errorf("[%d] parse location failed: %v", idx, err)
				return
			}
			code := locURL.Query().Get("code")

			tokenVals := url.Values{
				"grant_type":    {"authorization_code"},
				"code":          {code},
				"redirect_uri":  {redirectURI},
				"client_id":     {clientID},
				"code_verifier": {verifier},
			}
			tokResp, err := http.PostForm(srv.URL+"/token", tokenVals)
			if err != nil {
				t.Errorf("[%d] token failed: %v", idx, err)
				return
			}
			defer tokResp.Body.Close()

			if tokResp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(tokResp.Body)
				t.Errorf("[%d] expected 200, got %d: %s", idx, tokResp.StatusCode, string(body))
				return
			}

			var tok TokenResponse
			if err := json.NewDecoder(tokResp.Body).Decode(&tok); err != nil {
				t.Errorf("[%d] decode failed: %v", idx, err)
				return
			}
			if !strings.HasPrefix(tok.TokenType, "Bearer") || tok.AccessToken == "" {
				t.Errorf("[%d] invalid token response: %+v", idx, tok)
			}
		}(i)
	}

	wg.Wait()
}
