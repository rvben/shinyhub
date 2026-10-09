package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
)

func TestDeploy_MultiplexManifestHandoff(t *testing.T) {
	const baseManifest = `[app]
replicas = 16
min_warm_replicas = 16
memory_limit_mb = 16
[app.worker]
isolation = "multiplex"
[[schedule]]
name = "refresh"
cron = "0 5 * * *"
cmd = "true"
deploy_trigger = "first_deploy"
on_success = "roll"
`
	const manifestRefusal = "manifest whose configuration must be reconciled"
	const producerRefusal = "changes shared producer state"
	for _, tc := range []struct {
		name          string
		status        int
		handoff       bool
		allowDowntime bool
		refusal       string
	}{
		{name: "code-only", status: http.StatusOK, handoff: true},
		{name: "code-only-with-downtime", status: http.StatusOK, handoff: true, allowDowntime: true},
		{name: "format-only", status: http.StatusOK, handoff: true},
		{name: "live-drift", status: http.StatusOK, handoff: true},
		{name: "omitted-identity-drift", status: http.StatusConflict, refusal: manifestRefusal},
		{name: "omitted-usage-policy-drift", status: http.StatusConflict, refusal: manifestRefusal},
		{name: "changed-manifest", status: http.StatusOK, handoff: true},
		{name: "changed-schedule", status: http.StatusOK, handoff: true},
		{name: "changed-manifest-with-downtime", status: http.StatusOK, handoff: true, allowDowntime: true},
		{name: "missing-previous-manifest", status: http.StatusConflict, refusal: manifestRefusal},
		{name: "malformed-previous-manifest", status: http.StatusConflict, refusal: manifestRefusal},
		{name: "hooks", status: http.StatusConflict, refusal: manifestRefusal},
		{name: "bundle-change", status: http.StatusConflict, refusal: producerRefusal},
		{name: "missing-producer-state", status: http.StatusConflict, refusal: producerRefusal},
		{name: "capacity", status: http.StatusConflict, refusal: "need 256 MiB for 16 parallel-generation replicas"},
		{name: "provider", status: http.StatusConflict, refusal: "uses provider \"docker\""},
		{name: "readiness", status: http.StatusInternalServerError, handoff: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, token, runtime := newManifestE2EServerWithJobs(t)
			defer srv.Close()
			runtime.serveTraffic = true
			zeroFloor := 0
			srv.cfg.Server.MinAvailableMemoryMB = &zeroFloor
			// Admit exactly the complete 16-replica candidate pool.
			srv.SetAvailableMemoryForTest(func() (int, error) { return 16 * 16, nil })
			app := createGenerationTestApp(t, store, "multiplex-manifest", 16, 16)
			manifest := baseManifest
			if tc.name == "bundle-change" {
				manifest = strings.Replace(manifest, "first_deploy", "bundle_change", 1)
			}
			if tc.name == "hooks" {
				manifest += "[[hook]]\non = \"post-deploy\"\ncommand = [\"true\"]\n"
			}
			upload := func(source, manifest string, allowDowntime bool) *httptest.ResponseRecorder {
				t.Helper()
				body, contentType := buildMultiFileBundleUpload(t, map[string]string{
					"app.py": source, "shinyhub.toml": manifest,
				})
				req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/deploy", body)
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", contentType)
				if allowDowntime {
					req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
				}
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, req)
				return rec
			}
			if rec := upload("print('app') # v1", manifest, false); rec.Code != http.StatusOK {
				t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
			}
			previous, err := store.GetActiveDeploymentGeneration(app.ID)
			if err != nil {
				t.Fatal(err)
			}
			previousDep, err := store.GetDeploymentByID(previous.DeploymentID)
			if err != nil {
				t.Fatal(err)
			}
			var driftSQL string
			switch tc.name {
			case "format-only":
				manifest = "# Same declarations\n\n" + manifest
			case "live-drift":
				driftSQL = "UPDATE apps SET min_warm_replicas=8 WHERE id=?"
			case "omitted-identity-drift":
				driftSQL = "UPDATE apps SET identity_headers=TRUE WHERE id=?"
			case "omitted-usage-policy-drift":
				driftSQL = "UPDATE apps SET usage_identity_mode='disabled' WHERE id=?"
			case "changed-manifest", "changed-manifest-with-downtime":
				manifest = strings.Replace(manifest, "min_warm_replicas = 16", "min_warm_replicas = 8", 1)
			case "changed-schedule":
				manifest = strings.Replace(manifest, "0 5 * * *", "0 6 * * *", 1)
			case "missing-previous-manifest":
				if err := os.Remove(filepath.Join(previousDep.BundleDir, deploy.ManifestFilename)); err != nil {
					t.Fatal(err)
				}
			case "malformed-previous-manifest":
				if err := os.WriteFile(filepath.Join(previousDep.BundleDir, deploy.ManifestFilename), []byte("[invalid"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-producer-state":
				id := scheduleIDByName(t, store, app.ID, "refresh")
				if _, err := store.DB().Exec("DELETE FROM schedule_producer_state WHERE schedule_id=?", id); err != nil {
					t.Fatal(err)
				}
			case "capacity":
				// A single surge replica would fit, but the full candidate pool cannot.
				srv.SetAvailableMemoryForTest(func() (int, error) { return 16*16 - 1, nil })
			case "provider":
				if _, err := store.DB().Exec("UPDATE replicas SET provider='docker' WHERE app_id=?", app.ID); err != nil {
					t.Fatal(err)
				}
			}
			if driftSQL != "" {
				if _, err := store.DB().Exec(driftSQL, app.ID); err != nil {
					t.Fatal(err)
				}
			}
			before, err := store.GetAppBySlug(app.Slug)
			if err != nil {
				t.Fatal(err)
			}
			revision := appResourceRevision(before)
			var readinessChecks atomic.Int32
			var oldRouteLost atomic.Bool
			srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
				if p.GenerationScoped != tc.handoff {
					return nil, fmt.Errorf("generation-scoped deploy = %v, want %v", p.GenerationScoped, tc.handoff)
				}
				p.HealthCheck = func(string, time.Duration, http.RoundTripper) error {
					readinessChecks.Add(1)
					if tc.handoff {
						status, body := proxyGenerationBody(srv, app.Slug, "")
						if status != http.StatusOK || body != previousDep.Version {
							oldRouteLost.Store(true)
							return fmt.Errorf("old route unavailable during candidate boot: %d %s", status, body)
						}
					}
					if tc.name == "readiness" {
						return errors.New("candidate unhealthy")
					}
					return nil
				}
				return deploy.Run(p)
			})
			runtime.mu.Lock()
			beforeProducers := len(runtime.producerCommands)
			runtime.mu.Unlock()
			rec := upload("print('app') # v2", manifest, tc.allowDowntime)
			if rec.Code != tc.status {
				t.Fatalf("redeploy: %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if oldRouteLost.Load() {
				t.Fatal("old route was unavailable during candidate readiness")
			}
			if tc.name == "readiness" && readinessChecks.Load() == 0 {
				t.Fatal("failure did not reach candidate readiness")
			}
			active, err := store.GetActiveDeploymentGeneration(app.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.status == http.StatusOK {
				if active.DeploymentID == previous.DeploymentID {
					t.Fatal("candidate not promoted")
				}
				if got := readinessChecks.Load(); got != 16 {
					t.Fatalf("candidate readiness checks = %d, want 16", got)
				}
				candidate, err := store.GetDeploymentByID(active.DeploymentID)
				if err != nil {
					t.Fatal(err)
				}
				if status, body := proxyGenerationBody(srv, app.Slug, ""); status != http.StatusOK || body != candidate.Version {
					t.Fatalf("candidate route unavailable: %d %s", status, body)
				}
			} else {
				if active.DeploymentID != previous.DeploymentID {
					t.Fatal("refused or failed deploy changed authority")
				}
				if status, body := proxyGenerationBody(srv, app.Slug, ""); status != http.StatusOK || body != previousDep.Version {
					t.Fatalf("old route lost: %d %s", status, body)
				}
				if tc.status == http.StatusConflict {
					var response struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(response.Error, tc.refusal) {
						t.Fatalf("refusal: %s, want reason containing %q", response.Error, tc.refusal)
					}
					if readinessChecks.Load() != 0 {
						t.Fatal("refused deploy reached candidate readiness")
					}
					if rec.Header().Get("X-ShinyHub-Conflict") != "generation-handoff-deferred" {
						t.Fatal("missing handoff deferral header")
					}
					after, err := store.GetAppBySlug(app.Slug)
					if err != nil {
						t.Fatal(err)
					}
					if appResourceRevision(after) != revision {
						t.Fatal("refused deploy changed live app settings")
					}
				}
			}
			runtime.mu.Lock()
			afterProducers := len(runtime.producerCommands)
			runtime.mu.Unlock()
			if beforeProducers != 1 || afterProducers != beforeProducers {
				t.Fatalf("producer runs: before=%d after=%d, want one bootstrap only", beforeProducers, afterProducers)
			}
		})
	}
}
