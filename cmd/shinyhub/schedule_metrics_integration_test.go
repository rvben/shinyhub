package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/metrics"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestScheduleMetricsAgreeWithAPIForStoredSchedules(t *testing.T) {
	for _, zone := range []string{"", "Asia/Tokyo"} {
		t.Run("default="+zone, func(t *testing.T) {
			store := dbtest.New(t)
			appID := createTestApp(t, store, "scheduled")
			cfg := &config.Config{
				Server:  config.ServerConfig{BaseURL: "https://hub.example.test"},
				Auth:    config.AuthConfig{Secret: "schedule-metrics-test-secret"},
				Storage: config.StorageConfig{AppsDir: t.TempDir()},
			}
			if zone != "" {
				var err error
				cfg.Scheduler.Location, err = time.LoadLocation(zone)
				if err != nil {
					t.Fatal(err)
				}
			}
			// Relative to an anchor twelve hours ago, this fire is still ahead
			// in UTC but six hours overdue in Tokyo. A missing timezone wire
			// therefore changes stale, not just a displayed timestamp.
			now := time.Now().UTC()
			cron := fmt.Sprintf("0 %d * * *", (now.Hour()+3)%24)
			utc, invalidZone := "UTC", "Not/A_Timezone"
			for _, item := range []struct {
				name, cron string
				timezone   *string
			}{
				{"inherited", cron, nil}, {"override", cron, &utc},
				{"impossible", "0 6 30 2 *", nil}, {"bad-zone", cron, &invalidZone},
				{"retired", cron, nil},
			} {
				id, _, err := store.UpsertScheduleByName(db.UpsertScheduleByNameParams{
					AppID: appID, Name: item.name, CronExpr: item.cron, Timezone: item.timezone,
					CommandJSON: `["true"]`, Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", ManagedBy: db.ScheduleManagedByBundle,
				})
				if err != nil {
					t.Fatal(err)
				}
				if item.name == "retired" {
					schedule, err := store.GetSchedule(id)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.ReconcileManifestSchedules(appID, nil, []*db.Schedule{schedule}); err != nil {
						t.Fatal(err)
					}
				} else if item.name == "inherited" || item.name == "override" {
					at := now.Add(-12 * time.Hour)
					run, err := store.InsertScheduleRun(db.InsertScheduleRunParams{ScheduleID: id, Status: "running", Trigger: "manual", StartedAt: at})
					if err != nil {
						t.Fatal(err)
					}
					exit := 0
					if err := store.FinishScheduleRun(db.FinishScheduleRunParams{RunID: run, Status: "succeeded", ExitCode: &exit, FinishedAt: at}); err != nil {
						t.Fatal(err)
					}
				}
			}
			owner, err := store.GetUserByUsername("owner")
			if err != nil {
				t.Fatal(err)
			}
			token, err := auth.IssueJWT(owner.ID, owner.Username, owner.Role, cfg.Auth.Secret)
			if err != nil {
				t.Fatal(err)
			}
			srv := api.New(cfg, store, nil, proxy.New())
			req := httptest.NewRequest(http.MethodGet, "/api/fleet/schedules/status", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("API status=%d body=%s", rec.Code, rec.Body.String())
			}
			var response struct {
				Items []struct {
					Schedule string `json:"schedule"`
					Stale    *bool  `json:"stale"`
					Error    string `json:"freshness_error"`
				} `json:"items"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			reg := metrics.New("test")
			if err := reg.Register(metrics.NewScheduleFreshnessCollector(scheduleSamples(store, cfg.Scheduler.Location))); err != nil {
				t.Fatal(err)
			}
			scrape := httptest.NewRecorder()
			reg.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			if scrape.Code != http.StatusOK {
				t.Fatalf("scrape=%d %s", scrape.Code, scrape.Body.String())
			}
			body := scrape.Body.String()
			series := func(metric, name string) string {
				return fmt.Sprintf("%s{schedule=%q,slug=\"scheduled\"}", metric, name)
			}
			if len(response.Items) != 5 {
				t.Fatalf("API items=%+v", response.Items)
			}
			for _, item := range response.Items {
				known := 1
				if item.Stale == nil {
					known = 0
					if item.Error == "" {
						t.Errorf("unknown %s has no error", item.Schedule)
					}
					if strings.Contains(body, series("shinyhub_schedule_stale", item.Schedule)) {
						t.Errorf("unknown %s emitted stale", item.Schedule)
					}
				} else {
					want := item.Schedule == "inherited" && zone == "Asia/Tokyo"
					if *item.Stale != want {
						t.Errorf("API %s stale=%t want=%t", item.Schedule, *item.Stale, want)
					}
					value := 0
					if *item.Stale {
						value = 1
					}
					if !strings.Contains(body, series("shinyhub_schedule_stale", item.Schedule)+fmt.Sprintf(" %d\n", value)) {
						t.Errorf("API/metric mismatch for %s", item.Schedule)
					}
				}
				if !strings.Contains(body, series("shinyhub_schedule_freshness_known", item.Schedule)+fmt.Sprintf(" %d\n", known)) {
					t.Errorf("known mismatch for %s", item.Schedule)
				}
				if known == 0 || item.Schedule == "retired" {
					for _, metric := range []string{"shinyhub_schedule_expected_success_timestamp_seconds", "shinyhub_schedule_next_run_timestamp_seconds"} {
						if strings.Contains(body, series(metric, item.Schedule)) {
							t.Errorf("%s emitted for %s", metric, item.Schedule)
						}
					}
				}
			}
			if !strings.Contains(body, series("shinyhub_schedule_staleness_seconds", "retired")+" 0\n") {
				t.Error("retired schedule has nonzero staleness")
			}
		})
	}
}
