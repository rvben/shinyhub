package main

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// cfgWithDSNAndTiers builds a minimal *config.Config with the given DSN and
// tier list, leaving all other fields at zero/empty so the guard can be
// exercised without loading a file.
func cfgWithDSNAndTiers(dsn string, tiers []config.TierConfig) *config.Config {
	return &config.Config{
		Database: config.DatabaseConfig{DSN: dsn},
		Runtime:  config.RuntimeConfig{Tiers: tiers},
	}
}

// TestIsClustered verifies that isClustered returns true only for Postgres DSNs.
func TestIsClustered(t *testing.T) {
	cases := []struct {
		dsn  string
		want bool
	}{
		{"postgres://u:p@localhost:5432/db", true},
		{"postgresql://u:p@localhost/db?sslmode=disable", true},
		{":memory:", false},
		{"./data/shinyhub.db", false},
		{"file:test.db?mode=memory", false},
		{"data/shinyhub.db", false},
	}
	for _, tc := range cases {
		cfg := cfgWithDSNAndTiers(tc.dsn, nil)
		got := isClustered(cfg)
		if got != tc.want {
			t.Errorf("isClustered(DSN=%q) = %v, want %v", tc.dsn, got, tc.want)
		}
	}
}

// TestClusteredRuntimeGuard_RejectsLocalTiers asserts that checkClusteredRuntimeTiers
// returns an error naming the offending tier when a Postgres DSN is paired with
// a native or docker tier.
func TestClusteredRuntimeGuard_RejectsLocalTiers(t *testing.T) {
	pgDSN := "postgres://u:p@localhost:5432/db"

	cases := []struct {
		name    string
		tiers   []config.TierConfig
		wantErr string // non-empty substring expected in the error message
	}{
		{
			name:    "native tier rejected",
			tiers:   []config.TierConfig{{Name: "local", Runtime: "native"}},
			wantErr: "local",
		},
		{
			name:    "docker tier rejected",
			tiers:   []config.TierConfig{{Name: "burst", Runtime: "docker"}},
			wantErr: "burst",
		},
		{
			name: "mixed: native + fargate is rejected on the native tier",
			tiers: []config.TierConfig{
				{Name: "local", Runtime: "native"},
				{Name: "fargate-burst", Runtime: "fargate"},
			},
			wantErr: "local",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfgWithDSNAndTiers(pgDSN, tc.tiers)
			err := checkClusteredRuntimeTiers(cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain expected tier name %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestClusteredRuntimeGuard_AllowsOffHostTiers asserts that checkClusteredRuntimeTiers
// returns nil when all tiers use off-host runtimes (remote_docker or fargate),
// even with a Postgres DSN.
func TestClusteredRuntimeGuard_AllowsOffHostTiers(t *testing.T) {
	pgDSN := "postgres://u:p@localhost:5432/db"

	cases := []struct {
		name  string
		tiers []config.TierConfig
	}{
		{
			name:  "fargate only",
			tiers: []config.TierConfig{{Name: "default", Runtime: "fargate"}},
		},
		{
			name:  "remote_docker only",
			tiers: []config.TierConfig{{Name: "workers", Runtime: "remote_docker"}},
		},
		{
			name: "fargate + remote_docker mixed",
			tiers: []config.TierConfig{
				{Name: "primary", Runtime: "fargate"},
				{Name: "overflow", Runtime: "remote_docker"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfgWithDSNAndTiers(pgDSN, tc.tiers)
			if err := checkClusteredRuntimeTiers(cfg); err != nil {
				t.Fatalf("unexpected error for off-host tiers: %v", err)
			}
		})
	}
}

// TestClusteredRuntimeGuard_AllowsPostgresNoTiers asserts that
// checkClusteredRuntimeTiers returns nil when a Postgres DSN is configured but
// no tiers are declared. The loop's empty-slice path must not produce an error.
func TestClusteredRuntimeGuard_AllowsPostgresNoTiers(t *testing.T) {
	pgDSN := "postgres://u:p@localhost:5432/db"
	cfg := cfgWithDSNAndTiers(pgDSN, nil)
	if err := checkClusteredRuntimeTiers(cfg); err != nil {
		t.Fatalf("Postgres DSN with no tiers must not be rejected, got: %v", err)
	}
}

// TestClusteredRuntimeGuard_AllowsSingleNodeNative asserts that
// checkClusteredRuntimeTiers returns nil for a native tier when the DB is SQLite
// (single-node deployment - no change to existing behavior).
func TestClusteredRuntimeGuard_AllowsSingleNodeNative(t *testing.T) {
	sqliteDSN := "./data/shinyhub.db"
	cfg := cfgWithDSNAndTiers(sqliteDSN, []config.TierConfig{{Name: "local", Runtime: "native"}})
	if err := checkClusteredRuntimeTiers(cfg); err != nil {
		t.Errorf("native tier with SQLite must not be rejected, got: %v", err)
	}
}

// TestClusteredRuntimeGuard_AllowsSingleNodeDocker asserts that
// checkClusteredRuntimeTiers returns nil for a docker tier when the DB is SQLite
// (single-node deployment).
func TestClusteredRuntimeGuard_AllowsSingleNodeDocker(t *testing.T) {
	sqliteDSN := "./data/shinyhub.db"
	cfg := cfgWithDSNAndTiers(sqliteDSN, []config.TierConfig{{Name: "local", Runtime: "docker"}})
	if err := checkClusteredRuntimeTiers(cfg); err != nil {
		t.Errorf("docker tier with SQLite must not be rejected, got: %v", err)
	}
}
