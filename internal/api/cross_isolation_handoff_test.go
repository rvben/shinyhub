package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/lifecycle"
)

func crossIsolationManifest(mode string) string {
	return fmt.Sprintf("[app]\nreplicas = 2\nmemory_limit_mb = 16\n[app.worker]\nisolation = %q\ngrouped_size = 2\nmax_workers = 4\nwarm_spares = 0\n", mode)
}

func deployIsolationManifest(t *testing.T, srv *Server, token, slug, mode, source string) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": source, "shinyhub.toml": crossIsolationManifest(mode)})
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+slug+"/deploy", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func TestDeploy_CrossIsolationHandoff(t *testing.T) {
	for _, from := range []string{"grouped", "multiplex"} {
		to := "grouped"
		if from == "grouped" {
			to = "multiplex"
		}
		for _, failure := range []string{"", "readiness", "promotion", "publication", "capacity"} {
			t.Run(from+"-to-"+to+"/"+failure, func(t *testing.T) {
				srv, store, token, manager, _, runtime := buildManifestE2EServer(t, config.RuntimeConfig{})
				defer srv.Close()
				runtime.serveTraffic = true
				zero := 0
				srv.cfg.Server.MinAvailableMemoryMB = &zero
				srv.cfg.Server.DrainTimeout = 200 * time.Millisecond
				srv.SetAvailableMemoryForTest(func() (int, error) { return 1024, nil })
				app := createGenerationTestApp(t, store, "cross-isolation", 2, 16)
				if first := deployIsolationManifest(t, srv, token, app.Slug, from, "print('v1')"); first.Code != http.StatusOK {
					t.Fatalf("first deploy: %d %s", first.Code, first.Body.String())
				}
				old, err := store.GetServingDeployment(app.ID)
				if err != nil {
					t.Fatal(err)
				}
				spawner := &lifecycle.ElasticSpawner{Store: store, Manager: manager, Proxy: srv.proxy, HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil }}
				spawned := make(chan struct{}, 4)
				srv.proxy.SetSpawnFunc(func(slug string, slot int) { spawner.Spawn(slug, slot); spawned <- struct{}{} })
				srv.proxy.SetTerminateFunc(spawner.Terminate)
				srv.proxy.SetTerminateGenerationFunc(spawner.TerminateGeneration)
				request := func(cookies []*http.Cookie) *httptest.ResponseRecorder {
					r := httptest.NewRequest(http.MethodGet, "/app/"+app.Slug+"/", nil)
					for _, cookie := range cookies {
						r.AddCookie(cookie)
					}
					w := httptest.NewRecorder()
					srv.proxy.ServeHTTP(w, r)
					return w
				}
				initial := request(nil)
				cookies := initial.Result().Cookies()
				if from == "grouped" {
					select {
					case <-spawned:
					case <-time.After(5 * time.Second):
						t.Fatal("initial grouped worker did not start")
					}
				}
				if got := request(cookies); got.Code != http.StatusOK || got.Body.String() != old.Version {
					t.Fatalf("old route: %d %s", got.Code, got.Body.String())
				}
				var checked atomic.Bool
				srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
					if !p.PrepareOnly && p.GenerationScoped {
						p.HealthCheck = func(string, time.Duration, http.RoundTripper) error {
							checked.Store(true)
							live, _ := store.GetAppBySlug(app.Slug)
							if live.WorkerIsolation != from {
								return fmt.Errorf("target policy published before readiness: %s", live.WorkerIsolation)
							}
							if got := request(cookies); got.Body.String() != old.Version {
								return fmt.Errorf("old route lost during readiness: %s", got.Body.String())
							}
							if failure == "readiness" {
								return errors.New("candidate unhealthy")
							}
							return nil
						}
					}
					return deploy.Run(p)
				})
				if failure == "promotion" {
					srv.SetGenerationCutoverForTest(func(int64) error { return errors.New("promotion failed") }, nil)
				}
				if failure == "publication" {
					srv.SetGenerationCutoverForTest(nil, func(string, int64) (int64, error) { return 0, errors.New("publication failed") })
				}
				if failure == "capacity" {
					srv.SetAvailableMemoryForTest(func() (int, error) { return 0, nil })
				}
				result := deployIsolationManifest(t, srv, token, app.Slug, to, "print('v2')")
				want := http.StatusOK
				if failure != "" {
					want = http.StatusInternalServerError
				}
				if failure == "capacity" {
					want = http.StatusConflict
				}
				if result.Code != want {
					t.Fatalf("handoff: %d want %d: %s", result.Code, want, result.Body.String())
				}
				if failure != "capacity" && !checked.Load() {
					t.Fatal("candidate never reached readiness")
				}
				active, err := store.GetServingDeployment(app.ID)
				if err != nil {
					t.Fatal(err)
				}
				live, _ := store.GetAppBySlug(app.Slug)
				if failure != "" {
					if active.ID != old.ID || live.WorkerIsolation != from {
						t.Fatalf("failed candidate changed authority/policy: %+v %+v", active, live)
					}
					if got := request(cookies); got.Body.String() != old.Version {
						t.Fatalf("old client lost on failure: %s", got.Body.String())
					}
					rows, _ := store.ListReplicas(app.ID)
					if from == "multiplex" && len(rows) != 2 {
						t.Fatalf("failed grouped candidate lost fixed projection: %+v", rows)
					}
					return
				}
				if active.ID == old.ID || live.WorkerIsolation != to {
					t.Fatalf("target authority/policy not published: %+v %+v", active, live)
				}
				for _, dep := range []*db.Deployment{old, active} {
					recorded, err := store.GetDeploymentWorkerIsolation(dep.ID)
					expected := to
					if dep.ID == old.ID {
						expected = from
					}
					if err != nil || recorded != expected {
						t.Fatalf("generation policy: %q want %q err=%v", recorded, expected, err)
					}
				}
				if got := request(nil); got.Body.String() != active.Version {
					t.Fatalf("new client missed target: %d %s", got.Code, got.Body.String())
				}
				if from == "grouped" {
					if got := request(cookies); got.Body.String() != old.Version {
						t.Fatalf("grouped client lost drain affinity: %s", got.Body.String())
					}
				}
				rows, _ := store.ListReplicas(app.ID)
				if to == "grouped" && len(rows) != 0 {
					t.Fatalf("grouped target retained fixed rows: %+v", rows)
				}
				if to == "multiplex" && len(rows) != 2 {
					t.Fatalf("fixed target projection: %+v", rows)
				}
				deadline := time.Now().Add(5 * time.Second)
				for srv.proxy.HasDrainingGeneration(app.Slug) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if srv.proxy.HasDrainingGeneration(app.Slug) {
					t.Fatal("old generation never retired")
				}
				srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
					p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
					return deploy.Run(p)
				})
				if next := deployIsolationManifest(t, srv, token, app.Slug, to, "print('v3')"); next.Code != http.StatusOK {
					t.Fatalf("subsequent deploy: %d %s", next.Code, next.Body.String())
				}
			})
		}
	}
}

func TestDeploy_CrossIsolationIdleGroupedRollback(t *testing.T) {
	srv, store, token, _, _, runtime := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	runtime.serveTraffic = true
	app := createGenerationTestApp(t, store, "idle-grouped", 2, 16)
	if r := deployIsolationManifest(t, srv, token, app.Slug, "grouped", "print('v1')"); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	old, _ := store.GetServingDeployment(app.ID)
	srv.SetGenerationCutoverForTest(nil, func(string, int64) (int64, error) { return 0, errors.New("publication failed") })
	if r := deployIsolationManifest(t, srv, token, app.Slug, "multiplex", "print('v2')"); r.Code != 500 || !strings.Contains(r.Body.String(), "working version preserved") {
		t.Fatalf("idle rollback: %d %s", r.Code, r.Body.String())
	}
	active, _ := store.GetServingDeployment(app.ID)
	if active.ID != old.ID {
		t.Fatal("idle grouped authority changed")
	}
	mode, _ := srv.proxy.ServingMode(app.Slug)
	if string(mode) != "grouped" {
		t.Fatalf("idle grouped route changed: %s", mode)
	}
}

func TestDeploy_CrossIsolationAdmissionPreservesAuthority(t *testing.T) {
	for _, from := range []string{"grouped", "multiplex"} {
		to := "grouped"
		if from == "grouped" {
			to = "multiplex"
		}
		for _, refusal := range []string{"provider", "hooks", "unsupported-mode", "warm-capacity"} {
			if refusal == "warm-capacity" && to != "grouped" {
				continue
			}
			t.Run(from+"/"+refusal, func(t *testing.T) {
				srv, store, token, _, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
				defer srv.Close()
				zero := 0
				srv.cfg.Server.MinAvailableMemoryMB = &zero
				srv.SetAvailableMemoryForTest(func() (int, error) { return 1024, nil })
				app := createGenerationTestApp(t, store, "cross-refusal", 2, 16)
				if r := deployIsolationManifest(t, srv, token, app.Slug, from, "print('v1')"); r.Code != http.StatusOK {
					t.Fatalf("first deploy: %d %s", r.Code, r.Body.String())
				}
				old, _ := store.GetServingDeployment(app.ID)
				manifest := crossIsolationManifest(to)
				switch refusal {
				case "provider":
					srv.cfg.Runtime.Mode = "docker"
				case "hooks":
					manifest += "[[hook]]\non='post-deploy'\ncommand=['true']\n"
				case "unsupported-mode":
					manifest = strings.Replace(manifest, "isolation = \""+to+"\"", "isolation = \"per_session\"", 1)
				case "warm-capacity":
					manifest = strings.Replace(manifest, "warm_spares = 0", "warm_spares = 2", 1)
					// One worker fits; the full candidate of one worker and two spares does not.
					srv.SetAvailableMemoryForTest(func() (int, error) { return 32, nil })
				}
				body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": "print('v2')", "shinyhub.toml": manifest})
				req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/deploy", body)
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", contentType)
				r := httptest.NewRecorder()
				srv.Router().ServeHTTP(r, req)
				if r.Code != http.StatusConflict {
					t.Fatalf("refusal: %d %s", r.Code, r.Body.String())
				}
				active, _ := store.GetServingDeployment(app.ID)
				live, _ := store.GetAppBySlug(app.Slug)
				if active.ID != old.ID || live.WorkerIsolation != from {
					t.Fatal("refusal changed serving authority or policy")
				}
			})
		}
	}
}

func TestGenerationDrainExcludesTopologyMutation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	srv, app := newScaleTestServer(t, "generation-drain-fence", 1, cfg)
	old, err := srv.store.GetServingDeployment(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	srv.proxy.SetPoolSize(app.Slug, 1)
	if err := srv.proxy.EnsureServingGeneration(app.Slug, old.ID, config.IsolationMultiplex); err != nil {
		t.Fatal(err)
	}
	next := old.ID + 1
	if err := srv.proxy.StageGeneration(app.Slug, next, 1); err != nil {
		t.Fatal(err)
	}
	if err := srv.proxy.RegisterGenerationReplica(app.Slug, next, 0, "http://127.0.0.1:12345", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.proxy.ActivateGeneration(app.Slug, next); err != nil {
		t.Fatal(err)
	}
	if !srv.proxy.HasDrainingGeneration(app.Slug) {
		t.Fatal("old generation not retained")
	}
	if changed, err := srv.ScaleUp(app.Slug); changed || !errors.Is(err, errGenerationDraining) {
		t.Fatalf("scale during drain: %v %v", changed, err)
	}
	if changed, err := srv.WarmExpand(app.Slug); changed || !errors.Is(err, errGenerationDraining) {
		t.Fatalf("warm during drain: %v %v", changed, err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/apps/"+app.Slug+"/env?restart=true", nil)
	if changed, err := srv.maybeRestartForChange(req, app, app.Slug); changed || !errors.Is(err, errGenerationDraining) {
		t.Fatalf("restart during drain: %v %v", changed, err)
	}
}

func TestRecordedWakePolicyDoesNotPermitLiveIsolationReplacement(t *testing.T) {
	srv, store, token, _, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	app := createGenerationTestApp(t, store, "live-policy-replacement", 2, 16)
	if r := deployIsolationManifest(t, srv, token, app.Slug, "multiplex", "print('v1')"); r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	old, err := store.GetServingDeployment(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("UPDATE apps SET worker_isolation='grouped' WHERE id=?", app.ID); err != nil {
		t.Fatal(err)
	}
	app, err = store.GetAppByID(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.recordLaunchIsolation(app, old.ID, "grouped"); err == nil {
		t.Fatal("recorded wake fallback allowed live structural replacement")
	}
	if mode, err := store.GetDeploymentWorkerIsolation(old.ID); err != nil || mode != "multiplex" {
		t.Fatalf("old policy changed=%q %v", mode, err)
	}
	if err := srv.recordLaunchIsolation(app, old.ID, "multiplex"); err != nil {
		t.Fatalf("resuming recorded mode refused: %v", err)
	}
}
