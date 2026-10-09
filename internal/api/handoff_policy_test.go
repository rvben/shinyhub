package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

func TestHandoffStagesPolicyAndSchedulesUntilCutover(t *testing.T) {
	for _, failure := range []string{"", "readiness", "promotion", "proxy"} {
		t.Run(failure, func(t *testing.T) {
			srv, store, token, _, _, rt := buildManifestE2EServer(t, config.RuntimeConfig{})
			defer srv.Close()
			rt.serveTraffic = true
			srv.cfg.Server.DrainTimeout = 20 * time.Millisecond
			srv.SetAvailableMemoryForTest(func() (int, error) { return 4096, nil })
			app := createGenerationTestApp(t, store, "staged-policy", 1, 16)
			old := `[app]
name="old"
memory_limit_mb=16
min_warm_replicas=1
hibernate_timeout_minutes=5
[app.autoscale]
enabled=false
min_replicas=1
max_replicas=2
target=0.7
[[schedule]]
name="refresh"
cron="0 5 * * *"
cmd="true"
`
			upload := func(manifest string) *httptest.ResponseRecorder {
				body, ct := buildMultiFileBundleUpload(t, map[string]string{"app.py": "print('app')", "shinyhub.toml": manifest})
				req := httptest.NewRequest("POST", "/api/apps/"+app.Slug+"/deploy", body)
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", ct)
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, req)
				return rec
			}
			if rec := upload(old); rec.Code != 200 {
				t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
			}
			changed := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(old, `name="old"`, `name="new"`), "memory_limit_mb=16", "memory_limit_mb=32"), "0 5 * * *", "0 6 * * *")
			changed = strings.ReplaceAll(changed, "min_warm_replicas=1", "min_warm_replicas=0")
			changed = strings.ReplaceAll(strings.ReplaceAll(changed, "hibernate_timeout_minutes=5", "hibernate_timeout_minutes=-1"), "enabled=false", "enabled=true")
			srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
				p.HealthCheck = func(string, time.Duration, http.RoundTripper) error {
					live, err := store.GetAppByID(app.ID)
					if err != nil {
						return err
					}
					sc, err := store.GetScheduleByName(app.ID, "refresh")
					if err != nil {
						return err
					}
					if live.Name != "old" || *live.MemoryLimitMB != 16 || live.MinWarmReplicas != 1 || live.AutoscaleEnabled || live.HibernateTimeoutMinutes == nil || *live.HibernateTimeoutMinutes != 5 || sc.CronExpr != "0 5 * * *" {
						return errors.New("candidate policy leaked before readiness")
					}
					if p.MemoryLimitMB != 32 {
						return errors.New("candidate started under previous limits")
					}
					if failure == "readiness" {
						return errors.New("unhealthy")
					}
					return nil
				}
				return deploy.Run(p)
			})
			if failure == "promotion" {
				srv.SetGenerationCutoverForTest(func(int64) error { return errors.New("promotion failure") }, nil)
			}
			if failure == "proxy" {
				srv.SetGenerationCutoverForTest(nil, func(string, int64) (int64, error) { return 0, errors.New("proxy failure") })
			}
			rec := upload(changed)
			want := 200
			if failure != "" {
				want = 500
			}
			if rec.Code != want {
				t.Fatalf("cutover %d want %d: %s", rec.Code, want, rec.Body.String())
			}
			live, _ := store.GetAppByID(app.ID)
			sc, _ := store.GetScheduleByName(app.ID, "refresh")
			if failure != "" {
				if live.Name != "old" || *live.MemoryLimitMB != 16 || live.MinWarmReplicas != 1 || live.AutoscaleEnabled || live.HibernateTimeoutMinutes == nil || *live.HibernateTimeoutMinutes != 5 || sc.CronExpr != "0 5 * * *" {
					t.Fatalf("failed handoff changed authority: %+v %+v", live, sc)
				}
			} else {
				if live.Name != "new" || *live.MemoryLimitMB != 32 || live.MinWarmReplicas != 0 || !live.AutoscaleEnabled || live.HibernateTimeoutMinutes != nil || sc.CronExpr != "0 6 * * *" {
					t.Fatal("successful handoff did not publish policy")
				}
				var response struct {
					Handoff bool `json:"handoff"`
					Drain   int  `json:"drain_timeout_seconds"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || !response.Handoff {
					t.Fatalf("missing handoff outcome: %s", rec.Body.String())
				}
				active, _ := store.GetActiveDeploymentGeneration(app.ID)
				if _, err := store.PatchAppSettings(db.PatchAppSettingsParams{Slug: app.Slug, SetName: true, Name: "later"}); err != nil {
					t.Fatal(err)
				}
				if err := store.PromoteDeployment(active.DeploymentID); err != nil {
					t.Fatal(err)
				}
				live, _ = store.GetAppByID(app.ID)
				if live.Name != "later" {
					t.Fatal("idempotent promotion reapplied stale policy")
				}
			}
		})
	}
}
