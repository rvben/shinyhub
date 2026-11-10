package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/metrics"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/upgrade"
)

func TestFleetCountsReadsTheStore(t *testing.T) {
	store := dbtest.New(t)
	runningID := createTestApp(t, store, "alpha")
	for _, slug := range []string{"beta", "gamma", "delta", "epsilon"} {
		createTestApp(t, store, slug)
	}
	for slug, status := range map[string]string{"alpha": "running", "beta": "running", "gamma": "running", "delta": "crashed"} {
		if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2 {
		if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: runningID, Index: i, Status: db.ReplicaStatusRunning}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := fleetCounts(store)()
	if err != nil {
		t.Fatalf("fleet counts: %v", err)
	}
	if want := (metrics.FleetCounts{AppsRunning: 3, ReplicasRunning: 2, AppsCrashed: 1}); got != want {
		t.Fatalf("fleet counts = %+v, want %+v", got, want)
	}
}

// A store that cannot answer yields an error, so the gauges go absent rather
// than reading as an empty, healthy fleet.
func TestFleetCountsReportsAFailedQuery(t *testing.T) {
	store := dbtest.New(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := fleetCounts(store)(); err == nil {
		t.Fatalf("fleet counts on a closed store = %+v with no error, want an error", got)
	}
}

func TestScheduleSampleLeavesNeverHappenedEventsAbsent(t *testing.T) {
	got := scheduleSample(db.ScheduleFreshness{
		Slug: "report", Name: "nightly", Enabled: true,
		ConsecutiveFailures: 2, ActivationStatus: "pending", ActivationCapacityDeferrals: 4,
	}, time.UTC, time.Now())
	want := metrics.ScheduleSample{
		Slug: "report", Name: "nightly", Enabled: true,
		ConsecutiveFailures: 2, ActivationStatus: "pending", CapacityDeferrals: 4,
	}
	if got != want {
		t.Fatalf("sample = %+v, want %+v", got, want)
	}
}

func TestScheduleSampleCarriesEveryRecordedTime(t *testing.T) {
	at := func(sec int64) *time.Time { ts := time.Unix(sec, 0); return &ts }
	gen := int64(7)
	got := scheduleSample(db.ScheduleFreshness{
		Slug: "report", Name: "nightly",
		LastSuccessAt:                at(1001),
		LastFailureAt:                at(1002),
		LastInterruptedAt:            at(1003),
		ActivationCreatedAt:          at(1004),
		ActivationCapacityDeferredAt: at(1005),
		ActivationGeneration:         &gen,
	}, time.UTC, time.Now())
	want := metrics.ScheduleSample{
		Slug: "report", Name: "nightly",
		LastSuccessUnix: 1001, OK: true,
		LastFailureUnix:       1002,
		LastInterruptedUnix:   1003,
		ActivationCreatedUnix: 1004,
		CapacityDeferredUnix:  1005,
		ActivationGeneration:  7,
		FreshnessKnown:        true,
	}
	if got != want {
		t.Fatalf("sample = %+v, want %+v", got, want)
	}
}

func TestSessionSamplesCopiesEveryPool(t *testing.T) {
	samples := sessionSamples(func() map[string]proxy.PoolSessionStat {
		return map[string]proxy.PoolSessionStat{
			"alpha": {Sessions: 3, Cap: 10, Replicas: 2},
			"beta":  {Sessions: 1, Cap: 0, Replicas: 1},
		}
	})()
	slices.SortFunc(samples, func(a, b metrics.SessionSample) int { return strings.Compare(a.Slug, b.Slug) })
	want := []metrics.SessionSample{
		{Slug: "alpha", Sessions: 3, Cap: 10, Replicas: 2},
		{Slug: "beta", Sessions: 1, Cap: 0, Replicas: 1},
	}
	if !slices.Equal(samples, want) {
		t.Fatalf("samples = %+v, want %+v", samples, want)
	}
}

// newServerMetrics builds what the scrape endpoint serves, so the test reads
// the scrape: every source wired there must show up in it.
func TestNewServerMetricsWiresTheServerIntoOneRegistry(t *testing.T) {
	store := dbtest.New(t)
	createTestApp(t, store, "broken")
	appID := createTestApp(t, store, "scheduled")
	if _, err := store.CreateSchedule(db.CreateScheduleParams{AppID: appID, Name: "daily", CronExpr: "0 6 * * *", CommandJSON: `["true"]`, Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip"}); err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "broken", Status: "crashed"}); err != nil {
		t.Fatal(err)
	}
	prx := proxy.New()
	cfg := &config.Config{
		Server:  config.ServerConfig{BaseURL: "http://127.0.0.1"},
		Auth:    config.AuthConfig{Secret: "server-metrics-test-secret-server-metrics"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}
	srv := api.New(cfg, store, nil, prx)
	// A lease dir that is a file cannot be listed, so the retired-state gauge
	// must read unknown rather than an empty, verified set.
	notADir := filepath.Join(t.TempDir(), "leases")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	reg := newServerMetrics(serverMetricsDeps{
		version:           "v-test",
		logger:            slog.New(slog.DiscardHandler),
		store:             store,
		srv:               srv,
		prx:               prx,
		relayLeases:       &upgrade.RelayLeases{Dir: notADir},
		schedulerLocation: location,
	})

	rec := httptest.NewRecorder()
	srv.Observe(srv.Router()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/server-info", nil))

	scrape := httptest.NewRecorder()
	reg.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := scrape.Body.String()
	for _, want := range []string{
		"\nshinyhub_apps_crashed 1\n",
		"\nshinyhub_fleet_collector_up 1\n",
		"\nshinyhub_retired_state_unknown 1\n",
		"\nshinyhub_db_max_open_connections ",
		`shinyhub_http_requests_total{method="GET",route="/api/server-info",status="200"} 1`,
		`shinyhub_schedule_freshness_known{schedule="daily",slug="scheduled"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape lacks %q", want)
		}
	}
	foundNext := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, `shinyhub_schedule_next_run_timestamp_seconds{schedule="daily",slug="scheduled"} `) {
			fields := strings.Fields(line)
			value, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				t.Fatal(err)
			}
			if next := time.Unix(int64(value), 0).UTC(); next.Hour() != 21 || next.Minute() != 0 {
				t.Fatalf("next fire=%s, want 06:00 Tokyo / 21:00 UTC", next)
			}
			foundNext = true
		}
	}
	if !foundNext {
		t.Fatal("next-run metric missing from server scrape")
	}
}
