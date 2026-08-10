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

// The display-metadata keys follow the same declared-only rule as the numeric
// ones, with the one wrinkle that a declared empty description is a real value
// (clear it) rather than an absent key.
func TestFleetConfigBody_NameAndDescription(t *testing.T) {
	name := "Quarterly Revenue"
	desc := "Regional roll-up"
	body := fleetConfigBody(fleet.Config{Name: &name, Description: &desc})
	if body["name"] != name {
		t.Errorf("body[name] = %#v, want %q", body["name"], name)
	}
	if body["description"] != desc {
		t.Errorf("body[description] = %#v, want %q", body["description"], desc)
	}

	empty := ""
	body = fleetConfigBody(fleet.Config{Description: &empty})
	v, present := body["description"]
	if !present {
		t.Fatal(`a declared empty description must be sent (it clears the field), got absent`)
	}
	if v != "" {
		t.Errorf(`body[description] = %#v, want ""`, v)
	}
	if _, present := body["name"]; present {
		t.Errorf("undeclared name must be absent, got %#v", body["name"])
	}
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

// A bundle deploy rewrites the columns its own shinyhub.toml [app] block
// declares, so the fleet manifest (the outer authority) re-asserts its declared
// metadata afterwards. Without this, a fleet-declared name silently loses to the
// bundle on every deploy and the next plan reports the same drift again.
func TestReassertFleetConfig_ReassertsDeclaredMetadata(t *testing.T) {
	name := "Quarterly Revenue"
	desc := ""
	enabled := true
	declared := fleet.Config{
		Name:        &name,
		Description: &desc,
		Autoscale:   &fleet.AutoscaleConfig{Enabled: &enabled, MinReplicas: 1, MaxReplicas: 8, Target: 0.8},
	}

	bodies := captureFleetPatch(t, func(cfg *cliConfig) {
		if err := reassertFleetConfig(cfg, "demo", declared, nil, nil, "r"); err != nil {
			t.Fatalf("reassertFleetConfig: %v", err)
		}
	})
	if len(bodies) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(bodies))
	}
	if bodies[0]["name"] != name {
		t.Errorf("reasserted name = %#v, want %q", bodies[0]["name"], name)
	}
	if v, present := bodies[0]["description"]; !present || v != "" {
		t.Errorf(`reasserted description = %#v (present=%v), want ""`, v, present)
	}
	if _, ok := bodies[0]["autoscale"].(map[string]any); !ok {
		t.Errorf("reasserted autoscale = %#v, want the policy object", bodies[0]["autoscale"])
	}
	// replicas is deliberately excluded: PATCHing it triggers a redeploy, which
	// would drop the sessions the deploy just started.
	if _, present := bodies[0]["replicas"]; present {
		t.Errorf("reassert must not send replicas, got %#v", bodies[0]["replicas"])
	}
}

// Declaring none of the reasserted keys must cost no round trip, so a plain
// source-only fleet deploy is one request rather than two.
func TestReassertFleetConfig_NoDeclaredKeysIsNoRequest(t *testing.T) {
	replicas := 3
	bodies := captureFleetPatch(t, func(cfg *cliConfig) {
		if err := reassertFleetConfig(cfg, "demo", fleet.Config{Replicas: &replicas}, nil, nil, "r"); err != nil {
			t.Fatalf("reassertFleetConfig: %v", err)
		}
	})
	if len(bodies) != 0 {
		t.Fatalf("expected no request, got %d: %#v", len(bodies), bodies)
	}
}
