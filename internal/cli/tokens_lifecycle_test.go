package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestTokensCreate_SendsExpiry(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1,"name":"ci","token":"shk_x","created_at":"2026-01-01T00:00:00Z","expires_at":"2026-03-01T00:00:00Z"}`))
	})
	cmd := newTokensCmd()
	var o, e bytes.Buffer
	cmd.SetOut(&o)
	cmd.SetErr(&e)
	cmd.SetArgs([]string{"create", "--name", "ci", "--expires-in-days", "60"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if days, ok := body["expires_in_days"].(float64); !ok || days != 60 {
		t.Errorf("expires_in_days = %v, want 60", body["expires_in_days"])
	}
}

func TestTokensCreate_NoExpiryOmitsField(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1,"name":"ci","token":"shk_x","created_at":"2026-01-01T00:00:00Z","expires_at":null}`))
	})
	cmd := newTokensCmd()
	var o, e bytes.Buffer
	cmd.SetOut(&o)
	cmd.SetErr(&e)
	cmd.SetArgs([]string{"create", "--name", "ci"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["expires_in_days"]; present {
		t.Errorf("expires_in_days should be omitted when not set, body=%v", body)
	}
}

func TestTokensList_AllFlag(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[{"id":1,"user_id":2,"username":"dev","name":"ci","created_at":"2026-01-01T00:00:00Z","expires_at":null,"last_used_at":null}],"total":1,"limit":50,"offset":0}`))
	})
	cmd := newTokensCmd()
	var o, e bytes.Buffer
	cmd.SetOut(&o)
	cmd.SetErr(&e)
	cmd.SetArgs([]string{"list", "--all"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	got := (*reqs)[0]
	if got.Path != "/api/tokens" || !bytes.Contains([]byte(got.Query), []byte("all=1")) {
		t.Errorf("request = %s?%s, want /api/tokens with all=1", got.Path, got.Query)
	}
}
