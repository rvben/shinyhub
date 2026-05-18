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
