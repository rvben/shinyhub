package metrics

import (
	"database/sql"
	"testing"
	"time"
)

func TestDatabasePoolTelemetryTracksCurrentStats(t *testing.T) {
	reg := New("test")
	stats := sql.DBStats{OpenConnections: 3, InUse: 2, MaxOpenConnections: 8, WaitCount: 4, WaitDuration: 1500 * time.Millisecond}
	reg.RegisterDBStats(func() sql.DBStats { return stats })
	for name, want := range map[string]float64{"shinyhub_db_open_connections": 3, "shinyhub_db_in_use_connections": 2, "shinyhub_db_max_open_connections": 8, "shinyhub_db_wait_count_total": 4, "shinyhub_db_wait_duration_seconds_total": 1.5} {
		if got, ok := sampleValue(t, reg, name, nil); !ok || got != want {
			t.Errorf("%s=%v, present=%v; want %v", name, got, ok, want)
		}
	}
	stats.WaitCount = 9
	stats.InUse = 0
	if got, _ := sampleValue(t, reg, "shinyhub_db_wait_count_total", nil); got != 9 {
		t.Fatalf("stale wait count: %v", got)
	}
	if got, _ := sampleValue(t, reg, "shinyhub_db_in_use_connections", nil); got != 0 {
		t.Fatalf("stale active connections: %v", got)
	}
}
