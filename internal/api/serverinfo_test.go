package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerInfoAdvertisesFleetCapabilities(t *testing.T) {
	srv, _ := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/server-info", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var got struct {
		Capabilities struct {
			FleetPreconditions bool `json:"fleet_preconditions"`
			ContentDigest      bool `json:"content_digest"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Capabilities.FleetPreconditions {
		t.Errorf("fleet_preconditions not advertised")
	}
	if !got.Capabilities.ContentDigest {
		t.Errorf("content_digest not advertised")
	}
}

// GET /api/server-info reports which app runtimes are available on the host so
// a developer (or the CLI) can tell that, e.g., an R deploy will fail because R
// is not installed - rather than seeing an opaque "deploy failed".
func TestServerInfoReportsRuntimes(t *testing.T) {
	srv, _ := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/server-info", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var got struct {
		Runtimes map[string]bool `json:"runtimes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Runtimes == nil {
		t.Fatal("runtimes not reported")
	}
	for _, key := range []string{"python", "r"} {
		if _, ok := got.Runtimes[key]; !ok {
			t.Errorf("runtimes missing %q key", key)
		}
	}
}

// GET /api/server-info reports the binary version so a CLI can detect a
// half-provisioned host (front proxy up, shinyhub not) and check version
// requirements before issuing any mutating call.
func TestServerInfoReportsVersion(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.SetVersion("9.9.9")
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/server-info", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var got struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != "9.9.9" {
		t.Errorf("version = %q, want 9.9.9", got.Version)
	}
}
