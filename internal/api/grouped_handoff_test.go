package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
)

func TestDeploy_GroupedHandoffPreservesClientsAndFailedCandidate(t *testing.T) {
	for _, failure := range []string{"", "readiness", "promotion", "proxy publication"} {
		t.Run("failure="+failure, func(t *testing.T) {
			fail := failure != ""
			srv, store, token, mgr, _, runtime := buildManifestE2EServer(t, config.RuntimeConfig{})
			defer srv.Close()
			runtime.serveTraffic = true
			srv.cfg.Server.DrainTimeout = 10 * time.Second
			app := createGenerationTestApp(t, store, "grouped", 1, 16)
			if _, err := store.DB().Exec("UPDATE apps SET worker_isolation='grouped', worker_grouped_size=4, worker_max_workers=2 WHERE id=?", app.ID); err != nil {
				t.Fatal(err)
			}
			if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
				t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
			}
			old, _ := store.GetActiveDeploymentGeneration(app.ID)
			oldDep, _ := store.GetDeploymentByID(old.DeploymentID)
			spawner := &lifecycle.ElasticSpawner{Store: store, Manager: mgr, Proxy: srv.proxy, HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil }}
			ready := make(chan struct{}, 1)
			srv.proxy.SetSpawnFunc(func(slug string, slot int) { spawner.Spawn(slug, slot); ready <- struct{}{} })
			srv.proxy.SetTerminateFunc(spawner.Terminate)
			request := func(cookies []*http.Cookie) *httptest.ResponseRecorder {
				req := httptest.NewRequest("GET", "/app/grouped/", nil)
				for _, c := range cookies {
					req.AddCookie(c)
				}
				rec := httptest.NewRecorder()
				srv.proxy.ServeHTTP(rec, req)
				return rec
			}
			initial := request(nil)
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("worker did not spawn")
			}
			cookies := initial.Result().Cookies()
			if rec := request(cookies); rec.Body.String() != oldDep.Version {
				t.Fatalf("old worker: %s", rec.Body.String())
			}
			if failure == "readiness" {
				srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
					p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return errors.New("candidate unhealthy") }
					return deploy.Run(p)
				})
			}
			if failure == "promotion" {
				srv.SetGenerationCutoverForTest(func(int64) error { return errors.New("promotion failed") }, nil)
			}
			if failure == "proxy publication" {
				srv.SetGenerationCutoverForTest(nil, func(string, int64) (int64, error) { return 0, errors.New("proxy publication failed") })
			}
			rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false)
			want := http.StatusOK
			if fail {
				want = http.StatusInternalServerError
			}
			if rec.Code != want {
				t.Fatalf("handoff: %d %s", rec.Code, rec.Body.String())
			}
			if rec := request(cookies); rec.Body.String() != oldDep.Version {
				t.Fatalf("existing client lost old version: %s", rec.Body.String())
			}
			active, _ := store.GetActiveDeploymentGeneration(app.ID)
			if fail {
				if active.DeploymentID != old.DeploymentID {
					t.Fatal("failed candidate became authoritative")
				}
				rows, err := store.ListDeploymentReplicas(app.ID)
				if err != nil || len(rows) == 0 {
					t.Fatalf("working grouped generation lost its durable identities: %v", err)
				}
				for _, row := range rows {
					if row.DeploymentID != old.DeploymentID {
						t.Fatal("failed candidate left a blocking ledger")
					}
				}
				srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
					p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
					return deploy.Run(p)
				})
				srv.SetGenerationCutoverForTest(store.PromoteDeployment, srv.proxy.ActivateGeneration)
				if retry := deployBareGeneration(t, srv, token, app.Slug, "print('v3')", false); retry.Code != http.StatusOK {
					t.Fatalf("recovery deploy: %d %s", retry.Code, retry.Body.String())
				}
			} else {
				if active.DeploymentID == old.DeploymentID {
					t.Fatal("candidate was not promoted")
				}
				newDep, _ := store.GetDeploymentByID(active.DeploymentID)
				if rec := request(nil); rec.Body.String() != newDep.Version {
					t.Fatalf("new client missed new version: %s", rec.Body.String())
				}
				// An old timer/termination must not address the candidate.
				spawner.Terminate(app.Slug, 0)
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					rows, err := store.ListDeploymentReplicas(app.ID)
					if err != nil {
						t.Fatal(err)
					}
					oldRemains := false
					for _, row := range rows {
						if row.DeploymentID == old.DeploymentID {
							oldRemains = true
						}
					}
					if !oldRemains {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				rows, err := store.ListDeploymentReplicas(app.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					if row.DeploymentID == old.DeploymentID {
						t.Fatal("disconnected old worker left a blocking cleanup ledger")
					}
				}
				if candidate, ok := mgr.GetGenerationReplica(app.Slug, active.DeploymentID, 1); !ok || candidate.Status != process.StatusRunning {
					t.Fatal("old termination stopped replacement")
				}
			}
		})
	}
}

func TestDeploy_GroupedHandoffUnchangedManifestAndConfigurationDrift(t *testing.T) {
	const manifest = `[app]
name = "Grouped dashboard"
memory_limit_mb = 16
[app.worker]
isolation = "grouped"
grouped_size = 4
max_workers = 2
`
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged manifest", true: "live policy drift"}[drift], func(t *testing.T) {
			srv, store, token, _, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
			defer srv.Close()
			app := createGenerationTestApp(t, store, "grouped-manifest", 1, 16)
			if rec := deployManifest(t, srv, token, app.Slug, manifest); rec.Code != http.StatusOK {
				t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
			}
			if drift {
				if _, err := store.DB().Exec("UPDATE apps SET worker_grouped_size=8 WHERE id=?", app.ID); err != nil {
					t.Fatal(err)
				}
			}
			body, ctype := buildMultiFileBundleUpload(t, map[string]string{"app.py": "print('v2')", "shinyhub.toml": manifest})
			req := httptest.NewRequest("POST", "/api/apps/"+app.Slug+"/deploy", body)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", ctype)
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			want := http.StatusOK
			if drift {
				want = http.StatusConflict
			}
			if rec.Code != want {
				t.Fatalf("manifest deploy: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDeploy_GroupedHandoffAdmissionPreservesAuthority(t *testing.T) {
	for _, reason := range []string{"capacity", "provider", "unknown-memory"} {
		t.Run(reason, func(t *testing.T) {
			srv, store, token, _, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
			defer srv.Close()
			app := createGenerationTestApp(t, store, "grouped-admission", 1, 16)
			if _, err := store.DB().Exec("UPDATE apps SET worker_isolation='grouped', worker_grouped_size=4, worker_max_workers=2 WHERE id=?", app.ID); err != nil {
				t.Fatal(err)
			}
			if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
				t.Fatalf("first deploy: %s", rec.Body.String())
			}
			old, _ := store.GetActiveDeploymentGeneration(app.ID)
			switch reason {
			case "capacity":
				srv.SetAvailableMemoryForTest(func() (int, error) { return 0, nil })
			case "provider":
				srv.cfg.Runtime.Mode = "docker"
			case "unknown-memory":
				if _, err := store.DB().Exec("UPDATE apps SET memory_limit_mb=NULL WHERE id=?", app.ID); err != nil {
					t.Fatal(err)
				}
			}
			rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false)
			if rec.Code != http.StatusConflict || rec.Header().Get("X-ShinyHub-Conflict") != "generation-handoff-deferred" {
				t.Fatalf("admission: %d %s", rec.Code, rec.Body.String())
			}
			active, _ := store.GetActiveDeploymentGeneration(app.ID)
			if active.DeploymentID != old.DeploymentID {
				t.Fatal("refused grouped update changed authority")
			}
		})
	}
}

func TestDeploy_GroupedHandoffReplenishesWarmSparesWithoutTraffic(t *testing.T) {
	srv, store, token, mgr, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	app := createGenerationTestApp(t, store, "grouped-warm", 1, 16)
	if _, err := store.DB().Exec("UPDATE apps SET worker_isolation='grouped', worker_grouped_size=4, worker_max_workers=4, worker_warm_spares=2 WHERE id=?", app.ID); err != nil {
		t.Fatal(err)
	}
	spawner := &lifecycle.ElasticSpawner{
		Store: store, Manager: mgr, Proxy: srv.proxy,
		AcquireAppOperation: srv.AcquireAppOperation,
		HealthCheck:         func(string, time.Duration, http.RoundTripper) error { return nil },
	}
	srv.proxy.SetSpawnFunc(spawner.Spawn)
	srv.proxy.SetTerminateFunc(spawner.Terminate)
	waitForSpares := func(deploymentID int64) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			snapshot, _ := srv.proxy.ElasticWorkersSnapshot(app.Slug)
			ready := 0
			for _, worker := range snapshot.Workers {
				if worker.DeploymentID == deploymentID && worker.WarmSpare &&
					(worker.Status == "running" || worker.Status == "suspended") {
					ready++
				}
			}
			if ready == 2 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		snapshot, _ := srv.proxy.ElasticWorkersSnapshot(app.Slug)
		t.Fatalf("deployment %d did not reach its two ready warm spares without client traffic: %+v", deploymentID, snapshot)
	}
	for _, code := range []string{"print('v1')", "print('v2')"} {
		if rec := deployBareGeneration(t, srv, token, app.Slug, code, false); rec.Code != http.StatusOK {
			t.Fatalf("deploy: %d %s", rec.Code, rec.Body.String())
		}
		active, err := store.GetActiveDeploymentGeneration(app.ID)
		if err != nil {
			t.Fatal(err)
		}
		waitForSpares(active.DeploymentID)
	}
}
