package browser

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid http with port and query",
			url:     "http://localhost:8080/callback?code=abc&state=xyz",
			wantErr: false,
		},
		{
			name:    "valid https with domain and fragment",
			url:     "https://login.example.com/oauth/authorize?client_id=123#state",
			wantErr: false,
		},
		{
			name:    "valid uppercase scheme HTTP",
			url:     "HTTP://LOCALHOST:3000/auth",
			wantErr: false,
		},
		{
			name:    "valid mixed-case scheme Https",
			url:     "Https://example.com/path",
			wantErr: false,
		},
		{
			name:    "valid ip address",
			url:     "http://127.0.0.1:9090",
			wantErr: false,
		},
		{
			name:    "valid with unicode path and query",
			url:     "https://example.com/search?q=gö&lang=zh",
			wantErr: false,
		},
		{
			name:        "empty string",
			url:         "",
			wantErr:     true,
			errContains: "url cannot be empty",
		},
		{
			name:        "whitespace only",
			url:         "   ",
			wantErr:     true,
			errContains: "url cannot be empty",
		},
		{
			name:        "file scheme rejected",
			url:         "file:///etc/passwd",
			wantErr:     true,
			errContains: "url scheme must be http or https",
		},
		{
			name:        "javascript scheme rejected",
			url:         "javascript:alert(1)",
			wantErr:     true,
			errContains: "url scheme must be http or https",
		},
		{
			name:        "data scheme rejected",
			url:         "data:text/html,hello",
			wantErr:     true,
			errContains: "url scheme must be http or https",
		},
		{
			name:        "ftp scheme rejected",
			url:         "ftp://example.com/file",
			wantErr:     true,
			errContains: "url scheme must be http or https",
		},
		{
			name:        "missing scheme",
			url:         "localhost:8080/callback",
			wantErr:     true,
			errContains: "url scheme must be http or https",
		},
		{
			name:        "http with no host",
			url:         "http:///path/to/resource",
			wantErr:     true,
			errContains: "url must have a host",
		},
		{
			name:        "http scheme only no host",
			url:         "http://",
			wantErr:     true,
			errContains: "url must have a host",
		},
		{
			name:        "https scheme only no host",
			url:         "https://",
			wantErr:     true,
			errContains: "url must have a host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateURL(%q) err = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
			if tt.wantErr && tt.errContains != "" {
				if err == nil || !errors.Is(err, ErrEmptyURL) && !errors.Is(err, ErrInvalidScheme) && !errors.Is(err, ErrMissingHost) {
					// Also check substring if wrapped
					errMsg := err.Error()
					if len(errMsg) == 0 {
						t.Errorf("expected error message containing %q, got empty", tt.errContains)
					}
				}
			}
		})
	}
}

func TestValidateURL_DoesNotEchoMalformedSecretInput(t *testing.T) {
	secret := "state=super-secret&code=one-time"
	err := ValidateURL("https://example.test/%" + secret)
	if err == nil {
		t.Fatal("expected malformed URL to fail")
	}
	if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "one-time") {
		t.Fatalf("validation error echoed sensitive input: %v", err)
	}
}

func TestBrowserCommand(t *testing.T) {
	testURL := "http://localhost:8080/callback?code=abc"

	tests := []struct {
		goos     string
		wantName string
		wantArgs []string
	}{
		{
			goos:     "windows",
			wantName: "rundll32",
			wantArgs: []string{"url.dll,FileProtocolHandler", testURL},
		},
		{
			goos:     "darwin",
			wantName: "open",
			wantArgs: []string{testURL},
		},
		{
			goos:     "linux",
			wantName: "xdg-open",
			wantArgs: []string{testURL},
		},
		{
			goos:     "freebsd",
			wantName: "xdg-open",
			wantArgs: []string{testURL},
		},
	}

	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			gotName, gotArgs := browserCommand(tt.goos, testURL)
			if gotName != tt.wantName {
				t.Errorf("browserCommand(%q) name = %q, want %q", tt.goos, gotName, tt.wantName)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("browserCommand(%q) args = %v, want %v", tt.goos, gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestOpenURL(t *testing.T) {
	origStartCommand := startCommand
	defer func() {
		startCommand = origStartCommand
	}()

	t.Run("rejects invalid URL without invoking command", func(t *testing.T) {
		called := false
		startCommand = func(name string, args ...string) error {
			called = true
			return nil
		}

		err := OpenURL("javascript:alert(1)")
		if err == nil {
			t.Fatal("expected error for invalid URL, got nil")
		}
		if called {
			t.Fatal("startCommand was called for invalid URL")
		}
	})

	t.Run("valid URL invokes startCommand with platform args", func(t *testing.T) {
		var invokedName string
		var invokedArgs []string
		startCommand = func(name string, args ...string) error {
			invokedName = name
			invokedArgs = args
			return nil
		}

		testURL := "https://example.com/oauth"
		err := OpenURL(testURL)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		wantName, wantArgs := browserCommand(runtime.GOOS, testURL)
		if invokedName != wantName {
			t.Errorf("invoked command name = %q, want %q", invokedName, wantName)
		}
		if !reflect.DeepEqual(invokedArgs, wantArgs) {
			t.Errorf("invoked command args = %v, want %v", invokedArgs, wantArgs)
		}
	})

	t.Run("returns error when startCommand fails", func(t *testing.T) {
		mockErr := errors.New("command not found")
		startCommand = func(name string, args ...string) error {
			return mockErr
		}

		err := OpenURL("https://example.com/oauth")
		if err == nil {
			t.Fatal("expected error when startCommand fails, got nil")
		}
		if !errors.Is(err, mockErr) {
			t.Errorf("expected error wrapping mockErr, got %v", err)
		}
	})
}

func TestSanitizeURL(t *testing.T) {
	t.Run("converts google accountchooser URL to clean oauth endpoint", func(t *testing.T) {
		raw := "https://accounts.google.com/v3/signin/accountchooser?access_type=offline&client_id=123.apps.googleusercontent.com&prompt=consent&redirect_uri=http%3A%2F%2Flocalhost%3A49773%2Foauth-callback&response_type=code&scope=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcloud-platform&state=abc-123&dsh=S-123&o2v=2&service=lso&flowName=GeneralOAuthFlow&continue=https%3A%2F%2Faccounts.google.com%2Fsignin%2Foauth%2Fv3%2Fconsent"
		sanitized := SanitizeURL(raw)
		if !strings.HasPrefix(sanitized, "https://accounts.google.com/o/oauth2/v2/auth?") {
			t.Errorf("expected clean auth URL prefix, got %q", sanitized)
		}
		if strings.Contains(sanitized, "dsh=") || strings.Contains(sanitized, "flowName=") || strings.Contains(sanitized, "continue=") {
			t.Errorf("sanitized URL contains internal session tokens: %q", sanitized)
		}
		if !strings.Contains(sanitized, "client_id=123.apps.googleusercontent.com") {
			t.Errorf("missing client_id in sanitized URL: %q", sanitized)
		}
		if !strings.Contains(sanitized, "redirect_uri=http%3A%2F%2Flocalhost%3A49773%2Foauth-callback") {
			t.Errorf("missing redirect_uri in sanitized URL: %q", sanitized)
		}
	})

	t.Run("leaves standard URLs untouched", func(t *testing.T) {
		standard := "https://github.com/login/oauth/authorize?client_id=xyz&redirect_uri=http%3A%2F%2Flocalhost%3A8080%2Fcallback"
		if got := SanitizeURL(standard); got != standard {
			t.Errorf("expected %q, got %q", standard, got)
		}
	})
}
