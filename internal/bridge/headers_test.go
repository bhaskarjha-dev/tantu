package bridge

import (
	"net/http"
	"testing"
)

func TestAllowedCallbackRelayHeader(t *testing.T) {
	allowed := []string{"Accept", "Accept-Language", "Content-Type", "X-Requested-With"}
	for _, name := range allowed {
		if !allowedCallbackRelayHeader(name) {
			t.Errorf("allowedCallbackRelayHeader(%q) = false, want true", name)
		}
		if !allowedCallbackRelayHeader("  " + name + "  ") {
			t.Errorf("allowedCallbackRelayHeader with whitespace %q = false, want true", name)
		}
		lowered := ""
		for _, r := range name {
			if r >= 'A' && r <= 'Z' {
				lowered += string(r + ('a' - 'A'))
			} else {
				lowered += string(r)
			}
		}
		if !allowedCallbackRelayHeader(lowered) {
			t.Errorf("allowedCallbackRelayHeader(%q) = false, want true (case-insensitive)", lowered)
		}
	}
	denied := []string{
		"Cookie", "Authorization", "Host", "Content-Length", "Set-Cookie",
		"X-Forwarded-For", "X-Forwarded-Host", "Forwarded", "Proxy-Authorization",
		"Referer", "User-Agent", "Origin", "",
	}
	for _, name := range denied {
		if allowedCallbackRelayHeader(name) {
			t.Errorf("allowedCallbackRelayHeader(%q) = true, want false", name)
		}
	}
}

func TestFilterCallbackRelayHeaders(t *testing.T) {
	in := map[string]string{
		"Accept":           "text/html",
		"accept-language":  "en-US",
		"Cookie":           "session=stealme",
		"Authorization":    "Bearer stealme",
		"Host":             "evil.example",
		"X-Forwarded-For":  "1.2.3.4",
		"X-Requested-With": "XMLHttpRequest",
		"Content-Type":     "application/x-www-form-urlencoded",
		"Content-Length":   "9999",
		"Empty":            "",
	}
	got := filterCallbackRelayHeaders(in)
	want := map[string]string{
		"Accept":           "text/html",
		"Accept-Language":  "en-US",
		"X-Requested-With": "XMLHttpRequest",
		"Content-Type":     "application/x-www-form-urlencoded",
	}
	if len(got) != len(want) {
		t.Fatalf("filterCallbackRelayHeaders returned %d headers (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("header %q = %q, want %q", k, got[k], v)
		}
	}
	if filterCallbackRelayHeaders(nil) == nil {
		t.Error("filterCallbackRelayHeaders(nil) should return a non-nil empty map")
	}
}

func TestCallbackHeadersFromHTTP(t *testing.T) {
	h := http.Header{}
	h.Set("Accept", "text/html")
	h.Set("Cookie", "session=stealme")
	h.Set("Authorization", "Bearer stealme")
	h["x-custom-evil"] = []string{"1"}
	got := callbackHeaders(h)
	if got["Accept"] != "text/html" {
		t.Errorf("Accept = %q, want text/html", got["Accept"])
	}
	for _, k := range []string{"Cookie", "Authorization", "X-Custom-Evil"} {
		if _, ok := got[k]; ok {
			t.Errorf("callbackHeaders leaked %q", k)
		}
	}
}
