package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/fleet"
)

// captureFleetPatch runs fn against a server that records every request body it
// receives, so a test can assert both what was sent and that nothing was sent.
func captureFleetPatch(t *testing.T, fn func(cfg *cliConfig)) []map[string]any {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		bodies = append(bodies, body)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	fn(&cliConfig{Host: srv.URL, Token: "shk_test"})
	return bodies
}

// applyConfigDrift must send the DECLARED value, not the drift item's Desired:
// the string keys render their desired value %q-quoted for the plan table, so
// forwarding it would write a name with literal quotation marks around it.
func TestApplyConfigDrift_NameDescription(t *testing.T) {
	name := "Quarterly Revenue"
	desc := ""
	declared := fleet.Config{Name: &name, Description: &desc}
	drift := []fleet.ConfigDriftItem{
		{Key: "name", Server: `"Renamed In UI"`, Desired: `"Quarterly Revenue"`},
		{Key: "description", Server: `"Stale copy"`, Desired: `""`},
	}

	bodies := captureFleetPatch(t, func(cfg *cliConfig) {
		if err := applyConfigDrift(cfg, "demo", drift, declared, nil, nil, "r"); err != nil {
			t.Fatalf("applyConfigDrift: %v", err)
		}
	})
	if len(bodies) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(bodies))
	}
	if bodies[0]["name"] != name {
		t.Errorf("PATCH name = %#v, want the unquoted declared value %q", bodies[0]["name"], name)
	}
	v, present := bodies[0]["description"]
	if !present || v != "" {
		t.Errorf(`PATCH description = %#v (present=%v), want ""`, v, present)
	}
}
