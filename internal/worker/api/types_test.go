// internal/worker/api/types_test.go
package api

import (
	"encoding/json"
	"testing"
)

func TestRegisterRoundTrip(t *testing.T) {
	req := RegisterRequest{
		Token:         "join-secret",
		Name:          "burst-a",
		AdvertiseAddr: "10.0.0.5:8443",
		Tier:          "burst",
		Version:       "v0.6.0",
		CSRPEM:        "-----BEGIN CERTIFICATE REQUEST-----\n...",
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got RegisterRequest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Tier != "burst" || got.CSRPEM == "" {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestLogFrameType(t *testing.T) {
	f := Frame{Kind: FrameLog, Data: []byte("hello")}
	b, _ := json.Marshal(f)
	var got Frame
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != FrameLog || string(got.Data) != "hello" {
		t.Fatalf("frame = %+v", got)
	}
}
