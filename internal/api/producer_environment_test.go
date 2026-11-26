package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

const uvProducerSchedule = `
[[schedule]]
name = "refresh-data"
cron = "0 2 * * *"
cmd = "uv run --with-requirements requirements.txt python fetch.py"
deploy_trigger = "bundle_change"
`

// isEnvironmentProbe reports whether a RunOnce is a producer environment
// check rather than a producer run.
func isEnvironmentProbe(p process.StartParams) bool {
	n := len(p.Command)
	return n >= 2 && p.Command[n-2] == "--" && filepath.IsAbs(p.Command[n-1]) && filepath.Base(p.Command[n-1]) == "true"
}

// failEnvironmentProbes makes every environment check fail as uv does on an
// unsatisfiable requirement set, and counts the checks. Each check first
// tries uv's cache offline, which misses, and then resolves through the
// index, which fails; it counts once.
func failEnvironmentProbes(rt *manifestFakeRuntime, probes *int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.runOnceHook = func(p process.StartParams, w io.Writer) (bool, process.ExitInfo, error) {
		if !isEnvironmentProbe(p) {
			return false, process.ExitInfo{}, nil
		}
		if slices.Contains(p.Command, "--offline") {
			_, _ = io.WriteString(w, "error: the cache has no python-dateutil==4.0.0\n")
			return true, process.ExitInfo{Code: 1}, nil
		}
		*probes++
		_, _ = io.WriteString(w, "  × No solution found when resolving `--with` dependencies:\n"+
			"  ╰─▶ Because python-dateutil==4.0.0 has no matching distribution, we can conclude that your requirements are unsatisfiable.\n")
		return true, process.ExitInfo{Code: 1}, nil
	}
}

func passEnvironmentProbes(rt *manifestFakeRuntime, probes *int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.runOnceHook = func(p process.StartParams, _ io.Writer) (bool, process.ExitInfo, error) {
		if !isEnvironmentProbe(p) {
			return false, process.ExitInfo{}, nil
		}
		*probes++
		return true, process.ExitInfo{}, nil
	}
}

func decodeEnvironmentEvents(t *testing.T, rec *httptest.ResponseRecorder) []deployevent.Event {
	t.Helper()
	var events []deployevent.Event
	for _, line := range bytes.Split(bytes.TrimSpace(rec.Body.Bytes()), []byte("\n")) {
		var event deployevent.Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func deployBundleFiles(t *testing.T, srv *Server, token, slug string, files map[string]string, accept string) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := buildMultiFileBundleUpload(t, files)
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+slug+"/deploy", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

// assertPreviousReleaseServing checks that a failed operation left the
// previous release serving: same deployment, app running, no quarantine, no
// repair blocker and no producer run.
func assertPreviousReleaseServing(t *testing.T, srv *Server, store *db.Store, rt *manifestFakeRuntime, slug string, serving int64) {
	t.Helper()
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "running" {
		t.Errorf("app status = %q (%s), want running", app.Status, app.LastError)
	}
	current, err := store.GetServingDeployment(app.ID)
	if err != nil || current.ID != serving {
		t.Errorf("serving deployment = %+v err=%v, want %d", current, err, serving)
	}
	if quarantined, err := store.AppDeploymentCompatibilityQuarantined(app.ID); err != nil || quarantined {
		t.Errorf("quarantined = %v err=%v, want the app untouched", quarantined, err)
	}
	if blocker, err := store.AppDeploymentRepairBlocker(app.ID); err != nil || blocker != nil {
		t.Errorf("repair blocker = %+v err=%v", blocker, err)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.producerCommands) != 0 {
		t.Errorf("producers ran: %v", rt.producerCommands)
	}
	if !srv.proxy.HasServingBackends(slug) {
		t.Errorf("serving pool was deregistered")
	}
}

// A producer the deploy's own build does not prepare (a bundle with an
// explicit [app] command, or an R app) and whose uv environment cannot be
// built fails the deploy as a build failure, before the producer barrier, so
// the previous release serves again. Without the check, such a deploy
// entered the barrier, failed the producer and quarantined the app.
func TestDeploy_UnbuildableProducerEnvironmentFailsTheBuild(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
	}{
		{"explicit app command", map[string]string{
			"app.py":           "print('v2')",
			"fetch.py":         "print('fetch')",
			"requirements.txt": "python-dateutil==4.0.0\n",
			"shinyhub.toml":    "[app]\ncommand = [\"python\", \"app.py\", \"--port\", \"{port}\"]\n" + uvProducerSchedule,
		}},
		{"R app with a Python producer", map[string]string{
			"app.R":            "library(shiny)",
			"fetch.py":         "print('fetch')",
			"requirements.txt": "python-dateutil==4.0.0\n",
			"shinyhub.toml":    uvProducerSchedule,
		}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, store, token, rt := newManifestE2EServerWithJobs(t)
			t.Cleanup(func() { srv.jobs.Stop(context.Background()); _ = srv.manager.StopAll() })
			slug := fmt.Sprintf("env-refusal-%d", i)
			if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: 1, Access: "private"}); err != nil {
				t.Fatal(err)
			}
			baseline := deployBundleFiles(t, srv, token, slug, map[string]string{"app.py": "print('v1')"}, "")
			if baseline.Code != http.StatusOK {
				t.Fatalf("baseline deploy: %d %s", baseline.Code, baseline.Body.String())
			}
			app, _ := store.GetAppBySlug(slug)
			serving, err := store.GetServingDeployment(app.ID)
			if err != nil {
				t.Fatal(err)
			}
			probes := 0
			failEnvironmentProbes(rt, &probes)

			rec := deployBundleFiles(t, srv, token, slug, c.files, "")
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("deploy: %d %s, want 500, as for any failed build", rec.Code, rec.Body.String())
			}
			var body struct {
				Error string `json:"error"`
				Kind  string `json:"failure_kind"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Kind != string(deployfail.BuildFailed) {
				t.Errorf("failure_kind = %q, want build_failed (%s)", body.Kind, rec.Body.String())
			}
			for _, want := range []string{`schedule "refresh-data" cannot build its environment`, "unsatisfiable"} {
				if !strings.Contains(body.Error, want) {
					t.Errorf("error lacks %q: %s", want, body.Error)
				}
			}
			// The diagnostic is the index resolution's, not the cache miss's.
			if strings.Contains(body.Error, "the cache has no") {
				t.Errorf("error carries the offline attempt's output: %s", body.Error)
			}
			if probes != 1 {
				t.Errorf("environment checks = %d, want 1", probes)
			}
			assertPreviousReleaseServing(t, srv, store, rt, slug, serving.ID)
			history, err := store.ListDeploymentsBySlug(slug)
			if err != nil {
				t.Fatal(err)
			}
			refused, err := store.GetDeploymentByID(history[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if refused.Status != db.DeploymentFailed || refused.ProducerBarrierEntered {
				t.Errorf("refused deployment = %+v, want failed without a barrier", refused)
			}
		})
	}
}

// A producer whose environment resolves passes; one whose uv launch the
// check cannot reproduce is deployed with a warning, never reported as
// checked; one that does not launch uv is not mentioned.
func TestDeploy_ProducerEnvironmentOutcomesAreReported(t *testing.T) {
	srv, store, token, rt := newManifestE2EServerWithJobs(t)
	t.Cleanup(func() { srv.jobs.Stop(context.Background()); _ = srv.manager.StopAll() })
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "env-report", Name: "env-report", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	probes := 0
	passEnvironmentProbes(rt, &probes)
	manifest := uvProducerSchedule + `
[[schedule]]
name = "lint-data"
cron = "0 3 * * *"
cmd = "uvx data-linter"
deploy_trigger = "bundle_change"

[[schedule]]
name = "plain"
cron = "0 4 * * *"
cmd = "python fetch.py"
deploy_trigger = "bundle_change"
`
	files := map[string]string{"app.py": "print('v1')", "fetch.py": "print('fetch')", "requirements.txt": "six\n", "shinyhub.toml": manifest}

	rec := deployBundleFiles(t, srv, token, "env-report", files, deployevent.MediaType)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body.String())
	}
	if probes != 1 {
		t.Errorf("environment checks = %d, want 1 (only the uv run producer)", probes)
	}
	var phases []deployevent.Event
	var result json.RawMessage
	for _, event := range decodeEnvironmentEvents(t, rec) {
		if event.Type == deployevent.TypeError {
			t.Fatalf("deploy failed: %+v", event)
		}
		if event.Phase == "producer_environment" {
			phases = append(phases, event)
		}
		if event.Type == deployevent.TypeResult {
			result = event.Result
		}
	}
	if len(phases) != 2 ||
		phases[0].Status != deployevent.StatusCompleted || !strings.Contains(phases[0].Message, `"refresh-data"`) ||
		phases[1].Status != deployevent.StatusWarning || !strings.Contains(phases[1].Message, `"lint-data"`) || !strings.Contains(phases[1].Message, "uvx") {
		t.Fatalf("producer_environment events = %+v", phases)
	}
	if !bytes.Contains(result, []byte(`schedule \"lint-data\": environment not checked`)) || bytes.Contains(result, []byte(`\"refresh-data\": environment`)) {
		t.Errorf("result warnings: %s", result)
	}
	rt.mu.Lock()
	ran := len(rt.producerCommands)
	rt.mu.Unlock()
	if ran != 3 {
		t.Errorf("producer runs = %d, want all 3 after the check", ran)
	}
}

// The check outlives the deploy request, as the deploy's build does: a client
// that disconnects while a producer's environment builds does not fail a
// candidate whose environment builds.
func TestDeploy_ProducerEnvironmentCheckOutlivesTheRequest(t *testing.T) {
	srv, store, token, rt := newManifestE2EServerWithJobs(t)
	t.Cleanup(func() { srv.jobs.Stop(context.Background()); _ = srv.manager.StopAll() })
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "env-detach", Name: "env-detach", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	ctx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	probes := 0
	rt.mu.Lock()
	rt.runOnceHook = func(p process.StartParams, _ io.Writer) (bool, process.ExitInfo, error) {
		if !isEnvironmentProbe(p) {
			return false, process.ExitInfo{}, nil
		}
		probes++
		disconnect()
		return true, process.ExitInfo{}, nil
	}
	rt.mu.Unlock()

	files := map[string]string{"app.py": "print('v1')", "fetch.py": "print('fetch')", "requirements.txt": "six\n", "shinyhub.toml": uvProducerSchedule}
	body, contentType := buildMultiFileBundleUpload(t, files)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/apps/env-detach/deploy", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if probes != 1 || ctx.Err() == nil {
		t.Fatalf("probes = %d ctx = %v, want one check during which the client disconnected", probes, ctx.Err())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s, want the deploy to finish after the disconnect", rec.Code, rec.Body.String())
	}
}

// A rollback rebuilds its target's producers on this host, so one whose
// environment no longer builds fails the rollback and the current release
// serves again.
func TestRollback_UnbuildableProducerEnvironmentFailsTheRollback(t *testing.T) {
	srv, store, token, rt := newManifestE2EServerWithJobs(t)
	t.Cleanup(func() { srv.jobs.Stop(context.Background()); _ = srv.manager.StopAll() })
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "env-rollback", Name: "env-rollback", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	probes := 0
	passEnvironmentProbes(rt, &probes)
	v1 := deployBundleFiles(t, srv, token, "env-rollback", map[string]string{
		"app.py": "print('v1')", "fetch.py": "print('fetch')", "requirements.txt": "six\n", "shinyhub.toml": uvProducerSchedule,
	}, "")
	if v1.Code != http.StatusOK {
		t.Fatalf("v1 deploy: %d %s", v1.Code, v1.Body.String())
	}
	// v2 keeps the producer, so its data now carries v2's bundle and a
	// rollback to v1 must run v1's producer again.
	v2 := deployBundleFiles(t, srv, token, "env-rollback", map[string]string{
		"app.py": "print('v2')", "fetch.py": "print('fetch')", "requirements.txt": "six\n", "shinyhub.toml": uvProducerSchedule,
	}, "")
	if v2.Code != http.StatusOK {
		t.Fatalf("v2 deploy: %d %s", v2.Code, v2.Body.String())
	}
	app, _ := store.GetAppBySlug("env-rollback")
	serving, err := store.GetServingDeployment(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.ListDeploymentsBySlug("env-rollback")
	if err != nil {
		t.Fatal(err)
	}
	var target int64
	for _, d := range history {
		if d.Version != serving.Version && d.Status != db.DeploymentFailed {
			target = d.ID
		}
	}
	if target == 0 {
		t.Fatalf("no rollback target in %+v", history)
	}
	rt.mu.Lock()
	rt.producerCommands = nil
	rt.mu.Unlock()
	probes = 0
	failEnvironmentProbes(rt, &probes)

	payload, _ := json.Marshal(map[string]any{"deployment_id": target})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/env-rollback/rollback", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "unsatisfiable") {
		t.Fatalf("rollback: %d %s, want 500 with uv's diagnostic", rec.Code, rec.Body.String())
	}
	if probes != 1 {
		t.Errorf("environment checks = %d, want 1", probes)
	}
	assertPreviousReleaseServing(t, srv, store, rt, "env-rollback", serving.ID)
}

// A producer that only becomes required when convergence revalidates the
// plan (a writer published between planning and the consumer fence) is
// checked before the barrier is recorded, as the first plan's producers are:
// its unbuildable environment fails the deploy without entering the barrier
// or running it.
func TestConvergence_ChecksALateProducerBeforeTheBarrier(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "late-owner", PasswordHash: "unused", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("late-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "late-producer", Name: "late-producer", OwnerID: owner.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("late-producer")
	if err != nil {
		t.Fatal(err)
	}
	command := `["uv", "run", "--with-requirements", "requirements.txt", "python", "fetch.py"]`
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "refresh-data", CronExpr: "0 2 * * *", CommandJSON: command,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
		DeployTrigger: schedulespec.DeployTriggerBundleChange,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, fingerprint, err := schedulespec.ProducerIdentity(command)
	if err != nil {
		t.Fatal(err)
	}
	newDeployment := func(version, digest string) *db.Deployment {
		t.Helper()
		dir := t.TempDir()
		for name, body := range map[string]string{"fetch.py": "print('fetch')", "requirements.txt": "python-dateutil==4.0.0\n"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		deployment, err := store.BeginDeployment(app.ID, version, dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetDeploymentDigest(deployment.ID, digest); err != nil {
			t.Fatal(err)
		}
		deployment.ContentDigest = digest
		return deployment
	}
	publish := func(deployment *db.Deployment, finishedAt time.Time) {
		t.Helper()
		id := deployment.ID
		runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
			ScheduleID: scheduleID, Status: "running", Trigger: "deploy", StartedAt: finishedAt.Add(-time.Second),
			DeploymentID: &id, AppVersion: deployment.Version, ContentDigest: deployment.ContentDigest,
			ProducerFingerprint: fingerprint, ProducerCommandJSON: canonical, PublishesData: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		zero := 0
		if _, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
			RunID: runID, Status: "succeeded", ExitCode: &zero, FinishedAt: finishedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	target := newDeployment("target", "sha256:target")
	publish(target, base)

	rt := newManifestFakeRuntime()
	var probes int
	failEnvironmentProbes(rt, &probes)
	sharedData := t.TempDir()
	jobsManager, err := jobs.NewManager(process.NewManager(t.TempDir(), rt), nil, process.DefaultTier, store, nil, sharedData, sharedData)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store, jobs: jobsManager}
	plan, err := srv.planPrestartSchedules(app, nil, target.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.producers) != 0 {
		t.Fatalf("initial plan producers=%d, want satisfied on entry", len(plan.producers))
	}
	release := jobsManager.AcquireProducerGates(plan.gateIDs)
	defer release()

	// Another writer publishes after planning, so convergence finds the
	// producer required again.
	publish(newDeployment("intervening", "sha256:intervening"), base.Add(time.Second))

	entered := false
	releaseConsumer, err := srv.convergePrestartAndFenceConsumer(plan, target.ContentDigest, app, target, &entered, true)
	if releaseConsumer != nil {
		releaseConsumer()
	}
	var envErr *jobs.ProducerEnvironmentError
	if !errors.As(err, &envErr) || envErr.Schedule != "refresh-data" {
		t.Fatalf("convergence err = %v, want the producer's environment error", err)
	}
	if probes != 1 {
		t.Errorf("environment checks = %d, want 1", probes)
	}
	if entered {
		t.Error("the producer barrier was recorded")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.producerCommands) != 0 {
		t.Errorf("producers ran: %v", rt.producerCommands)
	}
}
