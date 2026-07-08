package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAppsTransfer_PostsToOwnerEndpoint(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"slug":"demo","owner_id":7}`))
	})
	cmd := newAppsCmd()
	var o, e bytes.Buffer
	cmd.SetOut(&o)
	cmd.SetErr(&e)
	cmd.SetArgs([]string{"transfer", "demo", "alice"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	got := (*reqs)[0]
	if got.Method != "POST" || got.Path != "/api/apps/demo/owner" {
		t.Errorf("request = %s %s, want POST /api/apps/demo/owner", got.Method, got.Path)
	}
	var body map[string]string
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["username"] != "alice" {
		t.Errorf("body username = %q, want alice", body["username"])
	}
	if !strings.Contains(o.String(), "transferred") {
		t.Errorf("output should confirm the transfer, got %q", o.String())
	}
}
