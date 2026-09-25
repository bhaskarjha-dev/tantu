package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDelegateSendWithNamePreservesTextName(t *testing.T) {
	var gotName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotName = body.Name
		if body.Text != "hello" {
			t.Errorf("unexpected delegated text %q", body.Text)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	addr := strings.TrimPrefix(server.URL, "http://")
	if _, err := delegateSendWithName(addr, "", "hello", "custom label", "", time.Second, ""); err != nil {
		t.Fatalf("text delegation failed: %v", err)
	}
	if gotName != "custom label" {
		t.Fatalf("delegated text name = %q, want %q", gotName, "custom label")
	}
}

func TestDelegateSendWithNamePreservesMultipartFilename(t *testing.T) {
	var gotName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = file.Close()
		gotName = header.Filename
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "source.bin")
	if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimPrefix(server.URL, "http://")
	if _, err := delegateSendWithName(addr, path, "", "renamed.txt", "", time.Second, ""); err != nil {
		t.Fatalf("file delegation failed: %v", err)
	}
	if gotName != "renamed.txt" {
		t.Fatalf("delegated filename = %q, want %q", gotName, "renamed.txt")
	}
}

func TestDelegateSendWithNameForwardsIdempotencyKey(t *testing.T) {
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text           string `json:"text"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotKey = body.IdempotencyKey
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	addr := strings.TrimPrefix(server.URL, "http://")
	if _, err := delegateSendWithName(addr, "", "hello", "", "", time.Second, "op-retry-1"); err != nil {
		t.Fatalf("text delegation failed: %v", err)
	}
	if gotKey != "op-retry-1" {
		t.Fatalf("delegated idempotency key = %q, want %q", gotKey, "op-retry-1")
	}
}
