package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/bhaskarjha-dev/tantu/internal/hub"
)

func TestRelayAPIError_PreservesFormat(t *testing.T) {
	body := []byte(`{"status":"error","message":"relay flow failed","operation_id":"rl-abc","destination":"Devbox","next_action":"Check the peer."}`)
	var err error = hub.NewRelayAPIError(502, body)
	re, ok := err.(*hub.RelayAPIError)
	if !ok {
		t.Fatalf("error type = %T", err)
	}
	if re.OperationID != "rl-abc" || re.Destination != "Devbox" || re.NextAction != "Check the peer." {
		t.Errorf("contract lost: %+v", re)
	}
	if !strings.Contains(re.Error(), "502") || !strings.Contains(re.Error(), "relay flow failed") {
		t.Errorf("Error() must preserve the historical human format, got %q", re.Error())
	}
}

func TestRelayAPIError_PlainBody(t *testing.T) {
	re := hub.NewRelayAPIError(500, []byte("bridge failure"))
	if re.Message != "bridge failure" {
		t.Errorf("message = %q", re.Message)
	}
	if re.OperationID != "" || re.Destination != "" || re.NextAction != "" {
		t.Errorf("plain body must not invent contract fields: %+v", re)
	}
}

func TestDecodeRelayOpenResult(t *testing.T) {
	res := hub.DecodeRelayOpenResult([]byte(`{"status":"success","operation_id":"rl-1","destination":"Devbox"}`))
	if res.OperationID != "rl-1" || res.Destination != "Devbox" {
		t.Errorf("decoded wrong: %+v", res)
	}
	empty := hub.DecodeRelayOpenResult(nil)
	if empty.OperationID != "" || empty.Destination != "" {
		t.Errorf("empty body must decode to zero result: %+v", empty)
	}
}

func TestOpenFailureJSON_PreservesContract(t *testing.T) {
	he := &hub.RelayAPIError{
		Message: "relay flow failed", OperationID: "rl-9",
		Destination: "Devbox", NextAction: "Check the peer.",
	}
	res := openFailureJSON(he)
	if res.Status != "error" || res.OperationID != "rl-9" || res.Destination != "Devbox" {
		t.Errorf("contract lost: %+v", res)
	}
	plain := openFailureJSON(errors.New("boom"))
	if plain.Message != "boom" || plain.Status != "error" {
		t.Errorf("generic error wrong: %+v", plain)
	}
}

func TestDisplayDestination(t *testing.T) {
	if got := displayDestination("loopback", nil, "", "127.0.0.1:9999"); got != "127.0.0.1:9999" {
		t.Errorf("raw address must echo, got %q", got)
	}
	if got := displayDestination("lan", nil, "", ""); got != "default peer" {
		t.Errorf("empty fallback = %q", got)
	}
}
