package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestGetAppConfigurationDefaultsRemainSeparateFromOverrides(t *testing.T) {
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:      config.AuthConfig{Secret: "test-secret"},
		Storage:   config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
		Lifecycle: config.LifecycleConfig{HibernateTimeout: 45 * time.Minute},
		Scheduler: config.SchedulerConfig{Location: time.FixedZone("Review/Timezone", 3600)},
		Runtime: config.RuntimeConfig{MaxReplicas: 32, DefaultMaxSessionsPerReplica: 12,
			Autoscale: config.AutoscaleConfig{DefaultTarget: .65},
			Docker:    config.DockerRuntimeConfig{DefaultMemoryMB: 512, DefaultCPUPercent: 150}},
	}
	srv := api.New(cfg, store, nil, nil)
	srv.SetHostCapacity(8, "affinity", 0, "")
	_, token := seedAutoscaleApp(t, store)
	patch := patchAutoscale(t, srv, token, []byte(`{"max_sessions_per_replica":99,"hibernate_timeout_minutes":5,"autoscale":{"enabled":true,"min_replicas":1,"max_replicas":2,"target":0.9}}`))
	if patch.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", patch.Code, patch.Body.String())
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "GET", "/api/apps/myapp", nil, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		HostCores     float64        `json:"configuration_host_cpu_cores"`
		CapacityScope string         `json:"configuration_capacity_scope"`
		Defaults      map[string]any `json:"configuration_defaults"`
		Sources       struct {
			Fields map[string]struct {
				Source    string `json:"source"`
				Effective string `json:"effective"`
			} `json:"fields"`
		} `json:"configuration_sources"`
		EffectiveCap    int     `json:"effective_max_sessions_per_replica"`
		EffectiveTarget float64 `json:"effective_autoscale_target"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Sources.Fields["hibernate_timeout_minutes"].Effective != "5" || body.Sources.Fields["memory_limit_mb"].Source != "server_default" {
		t.Fatalf("missing resolved configuration sources: %+v", body.Sources)
	}
	if body.HostCores != 8 || body.CapacityScope != "local" {
		t.Fatalf("incorrect host capacity: %+v", body)
	}
	for key, want := range map[string]any{
		"hibernate_timeout_minutes": float64(45), "max_sessions_per_replica": float64(12),
		"autoscale_target": .65, "memory_limit_mb": float64(512), "cpu_quota_percent": float64(150),
		"worker_isolation": "multiplex", "schedule_timezone": "Review/Timezone",
	} {
		if body.Defaults[key] != want {
			t.Errorf("default %s = %v, want %v", key, body.Defaults[key], want)
		}
	}
	if body.EffectiveCap != 99 || body.EffectiveTarget != .9 {
		t.Fatalf("effective overrides lost: %+v", body)
	}
}
