package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// Policies can outlive a runtime configuration change. The detail envelope
// and both metrics endpoints must report the same actionable maximum.
func TestAutoscaleCapacityEndpointsIncludeRuntimeCeiling(t *testing.T) {
	srv, store := newAutoscaleTestServer(t, 4, 0.8)
	slug, token := seedAutoscaleApp(t, store)
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppAutoscale(db.SetAppAutoscaleParams{
		AppID: app.ID, Enabled: true, MinReplicas: 1, MaxReplicas: 8, Target: 0.8,
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/apps/" + slug,
		"/api/apps/" + slug + "/metrics",
		"/api/apps/metrics?slugs=" + slug + "&autoscale_load=1",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, authedRequest(t, "GET", path, nil, token))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Maximum int `json:"effective_autoscale_max_replicas"`
				Metrics map[string]struct {
					Maximum int `json:"effective_autoscale_max_replicas"`
				} `json:"metrics"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			got := body.Maximum
			if body.Metrics != nil {
				got = body.Metrics[slug].Maximum
			}
			if got != 4 {
				t.Fatalf("effective maximum = %d, want runtime ceiling 4", got)
			}
		})
	}
}

func TestElasticMetricsDoNotAdvertiseReplicaAutoscaleLoad(t *testing.T) {
	srv, store, mgr, prx := newMetricsTestServerWithProxy(t)
	slug, token := seedAutoscaleApp(t, store)
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppAutoscale(db.SetAppAutoscaleParams{
		AppID: app.ID, Enabled: true, MinReplicas: 1, MaxReplicas: 8, Target: 0.8,
	}); err != nil {
		t.Fatal(err)
	}
	seedElasticPool(t, prx, mgr, slug)
	for _, path := range []string{
		"/api/apps/" + slug + "/metrics",
		"/api/apps/metrics?slugs=" + slug + "&autoscale_load=1",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, authedRequest(t, "GET", path, nil, token))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if raw, ok := body["metrics"]; ok {
				var bySlug map[string]map[string]json.RawMessage
				if err := json.Unmarshal(raw, &bySlug); err != nil {
					t.Fatal(err)
				}
				body = bySlug[slug]
			}
			if _, ok := body["autoscale_active_sessions"]; ok {
				t.Fatal("elastic pools must not expose the multiplex controller's session signal")
			}
			if string(body["worker_isolation"]) != `"grouped"` || string(body["max_workers"]) != "5" {
				t.Fatal("elastic pool capacity must remain available")
			}
			if string(body["effective_autoscale_max_replicas"]) != "0" {
				t.Fatal("elastic pools must not advertise a replica autoscale maximum")
			}
		})
	}
}
