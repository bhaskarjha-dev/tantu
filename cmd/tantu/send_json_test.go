package main

import (
	"errors"
	"strings"
	"testing"
)

func TestNewHubError_ParsesContract(t *testing.T) {
	body := []byte(`{"status":"error","message":"dial peer failed","code":"destination_unreachable","plain_message":"Could not reach the destination. No data was sent.","retry_safe":true,"duplicate_risk":false,"data_safe":true,"next_action":"Check the peer is online.","operation_id":"op-abc","destination":"Devbox"}`)
	he := newHubError(502, body)
	if he.StatusCode != 502 {
		t.Errorf("status = %d", he.StatusCode)
	}
	if he.Code != "destination_unreachable" || !he.RetrySafe || he.DuplicateRisk || !he.DataSafe {
		t.Errorf("contract flags wrong: %+v", he)
	}
	if he.OperationID != "op-abc" || he.Destination != "Devbox" {
		t.Errorf("identity fields wrong: %+v", he)
	}
	if !strings.Contains(he.Error(), "502") || !strings.Contains(he.Error(), "dial peer failed") {
		t.Errorf("Error() must preserve the historical human format, got %q", he.Error())
	}
}

func TestNewHubError_PlainBody(t *testing.T) {
	he := newHubError(500, []byte("bridge failure"))
	if he.Message != "bridge failure" {
		t.Errorf("message = %q", he.Message)
	}
	if he.Code != "" || he.DuplicateRisk {
		t.Errorf("plain body must not invent contract fields: %+v", he)
	}
}

func TestDecodeDelegatedResult(t *testing.T) {
	res := decodeDelegatedResult([]byte(`{"status":"success","operation_id":"op-1","destination":"Devbox","verified":true}`))
	if res.OperationID != "op-1" || res.Destination != "Devbox" || !res.Verified {
		t.Errorf("decoded wrong: %+v", res)
	}
	empty := decodeDelegatedResult(nil)
	if empty.OperationID != "" || empty.Destination != "" || empty.Verified {
		t.Errorf("empty body must decode to zero result: %+v", empty)
	}
}

func TestSendExitForHubError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"duplicate", &hubError{DuplicateRisk: true}, sendExitDuplicateRisk},
		{"invalid input", &hubError{Code: "invalid_input"}, sendExitUsage},
		{"limit", &hubError{Code: "limit_exceeded"}, sendExitUsage},
		{"generic hub", &hubError{Code: "destination_unreachable"}, sendExitFailed},
		{"non-hub", errors.New("dial tcp: connect: connection refused"), sendExitFailed},
	}
	for _, tc := range cases {
		if got := sendExitForHubError(tc.err); got != tc.want {
			t.Errorf("%s: exit = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestSendFailureJSON_PreservesContract(t *testing.T) {
	he := &hubError{
		Message: "dial peer failed", Code: "destination_unreachable",
		PlainMessage: "Could not reach the destination.", RetrySafe: true,
		DataSafe: true, NextAction: "Check the peer.", OperationID: "op-9",
		Destination: "Devbox",
	}
	res := sendFailureJSON(he)
	if res.Status != "error" || res.Code != "destination_unreachable" || !res.RetrySafe {
		t.Errorf("contract lost: %+v", res)
	}
	if res.OperationID != "op-9" || res.Destination != "Devbox" || res.NextAction != "Check the peer." {
		t.Errorf("identity/action lost: %+v", res)
	}
	plain := sendFailureJSON(errors.New("boom"))
	if plain.Message != "boom" || plain.Status != "error" {
		t.Errorf("generic error wrong: %+v", plain)
	}
}
