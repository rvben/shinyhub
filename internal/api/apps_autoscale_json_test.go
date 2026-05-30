package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestMetricsResponseMarshalsNewFields pins the metrics-poll JSON contract the UI
// grid card and per-replica panel consume: top-level metrics_available, per-replica
// tier/provider/metrics_available, and the embedded autoscale_status.
func TestMetricsResponseMarshalsNewFields(t *testing.T) {
	lastAt := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	coolUntil := time.Date(2026, 5, 30, 10, 2, 0, 0, time.UTC)
	as := autoscaleStatus{
		LastActionAt:  &lastAt,
		LastAction:    "up",
		InCooldown:    true,
		CooldownUntil: &coolUntil,
	}
	resp := metricsResponse{
		MetricsAvailable: false,
		AutoscaleStatus:  &as,
		Replicas: []replicaMetrics{{
			Index: 0, Status: "running",
			Tier: "burst", Provider: "fargate", MetricsAvailable: false,
		}},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal metricsResponse: %v", err)
	}
	s := string(b)
	for _, key := range []string{
		`"metrics_available"`, `"autoscale_status"`, `"tier":"burst"`,
		`"provider":"fargate"`, `"last_action":"up"`, `"in_cooldown":true`,
	} {
		if !strings.Contains(s, key) {
			t.Errorf("metricsResponse JSON missing %s; got %s", key, s)
		}
	}
}

// TestAutoscaleStatusComputed pins the autoscaleStatus struct JSON shape the UI reads.
func TestAutoscaleStatusComputed(t *testing.T) {
	lastAt := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	coolUntil := time.Date(2026, 5, 30, 10, 2, 0, 0, time.UTC)
	st := autoscaleStatus{
		LastActionAt:  &lastAt,
		LastAction:    "down",
		InCooldown:    true,
		CooldownUntil: &coolUntil,
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal autoscaleStatus: %v", err)
	}
	s := string(b)
	for _, key := range []string{`"last_action":"down"`, `"last_action_at"`, `"cooldown_until"`, `"in_cooldown":true`} {
		if !strings.Contains(s, key) {
			t.Errorf("autoscaleStatus JSON missing %s; got %s", key, s)
		}
	}
}
