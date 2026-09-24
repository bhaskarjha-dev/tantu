package bridge

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/protocol"
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

func TestValidateCallbackRelay_HeaderLimits(t *testing.T) {
	base := protocol.CallbackRelay{
		Method: "GET",
		Path:   "/callback?code=x",
		Body:   []byte{},
	}
	many := make(map[string]string)
	for i := 0; i < 65; i++ {
		many["X-Pad-"+string(rune('a'+i%26))+string(rune('0'+i%10))] = "v"
	}
	if err := validateCallbackRelay(callbackExpectation(""), protocol.CallbackRelay{
		Method: "GET", Path: "/callback", Headers: many,
	}); err == nil {
		t.Error("65 headers accepted, want error")
	}
	bigBytes := make([]byte, 17*1024)
	for i := range bigBytes {
		bigBytes[i] = 'a'
	}
	big := map[string]string{"X-Big": string(bigBytes)}
	if err := validateCallbackRelay(callbackExpectation(""), protocol.CallbackRelay{
		Method: "GET", Path: "/callback", Headers: big,
	}); err == nil {
		t.Error("17KiB headers accepted, want error")
	}
	if err := validateCallbackRelay(callbackExpectation(""), base); err != nil {
		t.Errorf("minimal relay rejected: %v", err)
	}
}

func TestCanonicalRelayHeader_Deterministic(t *testing.T) {
	in := map[string]string{
		"Content-Type": "text/plain",
		"content-type": "application/x-www-form-urlencoded",
		"HOST":         "127.0.0.1:8080",
	}
	// Sorted keys: "Content-Type" < "content-type", so the canonical form wins.
	if got := canonicalRelayHeader(in, "Content-Type"); got != "text/plain" {
		t.Errorf("canonical Content-Type = %q, want text/plain", got)
	}
	if got := canonicalRelayHeader(in, "host"); got != "127.0.0.1:8080" {
		t.Errorf("canonical Host = %q, want 127.0.0.1:8080", got)
	}
	filtered := filterCallbackRelayHeaders(in)
	if filtered["Content-Type"] != "text/plain" {
		t.Errorf("filtered Content-Type = %q, want text/plain", filtered["Content-Type"])
	}
	if _, ok := filtered["Host"]; ok {
		t.Errorf("Host must never be forwarded: %v", filtered)
	}
}

func FuzzValidateCallbackRelay(f *testing.F) {
	seeds := []string{
		`{"RequestID":"r","Method":"GET","Path":"/cb?code=x","Headers":{"Accept":"text/html"},"Body":""}`,
		`{"RequestID":"r","Method":"POST","Path":"/cb","Headers":{"Content-Type":"text/plain"},"Body":"aGVsbG8="}`,
		`not json`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var relay protocol.CallbackRelay
		if err := json.Unmarshal(data, &relay); err != nil {
			return
		}
		_ = validateCallbackRelay(callbackExpectation(""), relay)
	})
}
