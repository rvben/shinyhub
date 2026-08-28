package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/fleet"
)

// stateInt makes a *int literal for fleet.Config in tests.
func stateInt(v int) *int { return &v }

func TestConvergeApp_UnchangedIsNoNetwork(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	d := fleet.AppDiff{Slug: "a", Action: fleet.ActionUnchanged}
	r := convergeApp(cfg, d, fleet.AppEntry{Slug: "a"}, fleet.ObservedApp{}, "",
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusUnchanged {
		t.Fatalf("status = %s, want unchanged", r.status)
	}
	if hits != 0 {
		t.Fatalf("unchanged must do zero requests, got %d", hits)
	}
}

// A retry of an identical fleet apply must evaluate the deploy trigger as a
// level, not silently turn the previous deploy-triggered run failure into "unchanged".
func TestConvergeApp_UnchangedNeverSucceededFailsWarmGate(t *testing.T) {
	var postHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postHits++
		}
		switch r.URL.Path {
		case "/api/apps/reporting-app/schedules/reconcile":
			_, _ = io.WriteString(w, `[]`)
		case "/api/apps/reporting-app/schedules":
			_, _ = io.WriteString(w, `{"items":[{"id":7,"name":"refresh-data","enabled":true,"deploy_trigger":"bundle_change","last_run_id":703,"last_run_at":"2026-08-24T10:00:00Z","last_run_status":"failed","current_app_version":"v2","current_content_digest":"sha256:new","producer_app_version":"v1","producer_content_digest":"sha256:old","deploy_trigger_satisfied":false,"convergence_status":"failed"}]}`)
		case "/api/apps/reporting-app/schedules/7/runs":
			_, _ = io.WriteString(w, `{"items":[{"id":703,"status":"failed","started_at":"2026-08-24T10:00:00Z"}]}`)
		case "/api/apps/reporting-app/schedules/7/runs/703/logs":
			_, _ = io.WriteString(w, "Traceback (most recent call last):\nTABLE_NOT_FOUND\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "shinyhub.toml"), `[[schedule]]
name = "refresh-data"
cron = "0 5 * * *"
cmd = "python helpers/fetch_data.py"
deploy_trigger = "bundle_change"
`)
	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "shk_test"},
		fleet.AppDiff{Slug: "reporting-app", Action: fleet.ActionUnchanged},
		fleet.AppEntry{Slug: "reporting-app"}, fleet.ObservedApp{}, dir,
		convergeOpts{waitForWarm: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard,
	)
	if r.status != statusFailed || r.err == nil {
		t.Fatalf("result = %#v, want failed warm gate", r)
	}
	if !strings.Contains(r.err.Error(), "already unsatisfied before this apply") ||
		!strings.Contains(r.err.Error(), "producer convergence is not satisfied for app version") {
		t.Fatalf("error must identify the standing failure, got %v", r.err)
	}
	if len(r.warmGate) != 1 || r.warmGate[0].LastRunID != 703 {
		t.Fatalf("warm gate = %+v, want last run 703", r.warmGate)
	}
	if len(r.scheduleLogs) != 1 || !strings.Contains(strings.Join(r.scheduleLogs[0].Tail, "\n"), "TABLE_NOT_FOUND") {
		t.Fatalf("schedule logs = %+v, want failing run tail", r.scheduleLogs)
	}
	if code, _ := applyExitCode([]applyResult{r}); code != 4 {
		t.Fatalf("unchanged never-succeeded app exit = %d, want 4", code)
	}
	if postHits != 1 {
		t.Fatalf("verification must reconcile once without retrying failed producer work, got %d POST requests", postHits)
	}
}

func TestConvergeApp_UnchangedSucceededWarmGateStaysFree(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/apps/warm/schedules/reconcile" && r.Method == http.MethodPost {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		if r.URL.Path == "/api/apps/warm" && r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"compatibility_quarantined":false,"producer_repair_required":false}`)
			return
		}
		if r.URL.Path != "/api/apps/warm/schedules" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		_, _ = io.WriteString(w, `{"items":[{"id":7,"name":"refresh-data","enabled":true,"deploy_trigger":"bundle_change","last_success_at":"2026-08-24T10:00:00Z","deploy_trigger_satisfied":true}]}`)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "shinyhub.toml"), `[[schedule]]
name = "refresh-data"
cron = "0 5 * * *"
cmd = "true"
deploy_trigger = "bundle_change"
`)
	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "shk_test"},
		fleet.AppDiff{Slug: "warm", Action: fleet.ActionUnchanged},
		fleet.AppEntry{Slug: "warm"}, fleet.ObservedApp{}, dir,
		convergeOpts{waitForWarm: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard,
	)
	if r.status != statusUnchanged || r.err != nil {
		t.Fatalf("result = %#v, want unchanged", r)
	}
	if len(paths) != 4 {
		t.Fatalf("successful common path made %d requests, want reconcile + initial/final schedule snapshots + app compatibility snapshot: %v", len(paths), paths)
	}
}

func TestConvergeApp_UnchangedJoinsActiveWarmRun(t *testing.T) {
	var pollHits, postHits int
	var listHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postHits++
		}
		switch r.URL.Path {
		case "/api/apps/warm/schedules/reconcile":
			_, _ = io.WriteString(w, `[]`)
		case "/api/apps/warm/schedules":
			listHits++
			if listHits == 1 {
				_, _ = io.WriteString(w, `{"items":[{"id":7,"name":"refresh-data","enabled":true,"deploy_trigger":"bundle_change","current_content_digest":"sha256:new","deploy_trigger_satisfied":false,"convergence_status":"running","convergence_run_id":17,"last_run_id":17,"last_run_status":"running"}]}`)
			} else {
				_, _ = io.WriteString(w, `{"items":[{"id":7,"name":"refresh-data","enabled":true,"deploy_trigger":"bundle_change","current_content_digest":"sha256:new","deploy_trigger_satisfied":true,"convergence_status":"satisfied","convergence_run_id":17,"last_run_id":17,"last_run_status":"succeeded"}]}`)
			}
		case "/api/apps/warm/schedules/7/runs/17":
			pollHits++
			_, _ = io.WriteString(w, `{"status":"succeeded"}`)
		case "/api/apps/warm":
			_, _ = io.WriteString(w, `{"compatibility_quarantined":false,"producer_repair_required":false}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "shinyhub.toml"), `[[schedule]]
name = "refresh-data"
cron = "0 5 * * *"
cmd = "true"
deploy_trigger = "bundle_change"
`)
	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "test"},
		fleet.AppDiff{Slug: "warm", Action: fleet.ActionUnchanged},
		fleet.AppEntry{Slug: "warm"}, fleet.ObservedApp{}, dir,
		convergeOpts{waitForWarm: true, warmTimeout: time.Second, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard,
	)
	if r.status != statusUnchanged || r.err != nil {
		t.Fatalf("result = %#v, want unchanged after active run succeeds", r)
	}
	if pollHits != 1 || postHits != 1 {
		t.Fatalf("polls=%d posts=%d, want one join poll and one idempotent reconcile", pollHits, postHits)
	}
}

func TestConvergeApp_VerifySchedulesRejectsStaleNonDeployRun(t *testing.T) {
	var postHits, historyHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postHits++
		}
		switch r.URL.Path {
		case "/api/apps/projects/schedules":
			_, _ = io.WriteString(w, `{"items":[{"id":8,"name":"refresh-pend-data","enabled":true,"stale":true,"refreshing":false,"last_run_id":804,"last_run_at":"2026-08-24T10:00:00Z","last_run_status":"failed"}]}`)
		case "/api/apps/projects/schedules/8/runs":
			historyHits++
			_, _ = io.WriteString(w, `{"items":[{"id":804,"status":"failed","started_at":"2026-08-24T10:00:00Z"}]}`)
		case "/api/apps/projects/schedules/8/runs/804/logs":
			_, _ = io.WriteString(w, "Athena query failed\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "shk_test"},
		fleet.AppDiff{Slug: "projects", Action: fleet.ActionUnchanged},
		fleet.AppEntry{Slug: "projects"}, fleet.ObservedApp{}, t.TempDir(),
		convergeOpts{verifySchedules: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard,
	)
	if r.status != statusFailed || r.err == nil || !strings.Contains(r.err.Error(), "schedule verification gate unsatisfied") {
		t.Fatalf("result = %#v, want stale schedule failure", r)
	}
	if len(r.freshnessGate) != 1 || r.freshnessGate[0].Schedule != "refresh-pend-data" {
		t.Fatalf("freshness gate = %+v", r.freshnessGate)
	}
	if len(r.scheduleLogs) != 1 || r.scheduleLogs[0].RunID != 804 {
		t.Fatalf("schedule logs = %+v", r.scheduleLogs)
	}
	if postHits != 0 {
		t.Fatalf("--verify-schedules must be read-only, got %d POST requests", postHits)
	}
	if historyHits != 0 {
		t.Fatalf("atomic freshness metadata must avoid a run-history lookup, got %d", historyHits)
	}
}

func TestConvergeApp_VerifySchedulesIgnoresFreshAndDisabled(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/apps/projects/schedules" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		_, _ = io.WriteString(w, `{"items":[`+
			`{"id":8,"name":"fresh","enabled":true,"stale":false},`+
			`{"id":9,"name":"disabled-stale","enabled":false,"stale":true}]}`)
	}))
	t.Cleanup(srv.Close)

	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "shk_test"},
		fleet.AppDiff{Slug: "projects", Action: fleet.ActionUnchanged},
		fleet.AppEntry{Slug: "projects"}, fleet.ObservedApp{}, t.TempDir(),
		convergeOpts{verifySchedules: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard,
	)
	if r.status != statusUnchanged || r.err != nil {
		t.Fatalf("result = %#v, want unchanged", r)
	}
	if hits != 1 {
		t.Fatalf("freshness common path made %d requests, want one metadata read", hits)
	}
}

func TestConvergeApp_VerifySchedulesDetectsProducerMismatchReadOnly(t *testing.T) {
	var postHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postHits++
		}
		switch r.URL.Path {
		case "/api/apps/projects/schedules":
			_, _ = io.WriteString(w, `{"items":[{"id":8,"name":"refresh","enabled":true,"stale":false,"deploy_trigger":"bundle_change","deploy_trigger_satisfied":false,"current_app_version":"v2","current_content_digest":"sha256:new","producer_app_version":"v1","producer_content_digest":"sha256:old","convergence_status":"pending"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "test"},
		fleet.AppDiff{Slug: "projects", Action: fleet.ActionUnchanged},
		fleet.AppEntry{Slug: "projects"}, fleet.ObservedApp{}, t.TempDir(),
		convergeOpts{verifySchedules: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard,
	)
	if r.status != statusFailed || r.failureKind != failureScheduleProducer {
		t.Fatalf("result = %#v, want producer mismatch", r)
	}
	if len(r.freshnessGate) != 1 || r.freshnessGate[0].State != "producer_mismatch" {
		t.Fatalf("schedule verification = %+v", r.freshnessGate)
	}
	if postHits != 0 {
		t.Fatalf("--verify-schedules must remain read-only, got %d POST requests", postHits)
	}
}

func TestConvergeApp_VerifySchedulesRejectsDisabledNeverWriterRequiringRepair(t *testing.T) {
	var postHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postHits++
		}
		if r.URL.Path != "/api/apps/projects/schedules" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"items":[{"id":8,"name":"refresh","enabled":false,"stale":false,"deploy_trigger":"never","deploy_trigger_satisfied":false,"producer_repair_required":true,"last_run_id":804,"last_run_status":"interrupted"}]}`)
	}))
	t.Cleanup(srv.Close)
	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "test"},
		fleet.AppDiff{Slug: "projects", Action: fleet.ActionUnchanged},
		fleet.AppEntry{Slug: "projects"}, fleet.ObservedApp{}, t.TempDir(),
		convergeOpts{verifySchedules: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard,
	)
	if r.status != statusFailed || r.failureKind != failureScheduleProducer || r.err == nil ||
		!strings.Contains(r.err.Error(), "requires producer repair") {
		t.Fatalf("result = %#v, want disabled producer repair failure", r)
	}
	if len(r.freshnessGate) != 1 || r.freshnessGate[0].State != "producer_repair_required" {
		t.Fatalf("schedule verification = %+v", r.freshnessGate)
	}
	if postHits != 0 {
		t.Fatalf("--verify-schedules must remain read-only, got %d POST requests", postHits)
	}
}

func TestConvergeApp_VerifySchedulesReportsStaleRefreshingWithoutMisleadingLog(t *testing.T) {
	var logHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/projects/schedules":
			_, _ = io.WriteString(w, `{"items":[{"id":8,"name":"refresh","enabled":true,"stale":true,"refreshing":true,"last_run_id":805,"last_run_status":"running","last_run_at":"2026-08-24T10:00:00Z"}]}`)
		case "/api/apps/projects/schedules/8/runs/805/logs":
			logHits++
			_, _ = io.WriteString(w, "partial output\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "test"},
		fleet.AppDiff{Slug: "projects", Action: fleet.ActionUnchanged},
		fleet.AppEntry{Slug: "projects"}, fleet.ObservedApp{}, t.TempDir(),
		convergeOpts{verifySchedules: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard,
	)
	if r.status != statusFailed || r.failureKind != failureScheduleStale {
		t.Fatalf("result = %#v, want classified stale failure", r)
	}
	if len(r.freshnessGate) != 1 || r.freshnessGate[0].State != "stale_refreshing" || !r.freshnessGate[0].Refreshing {
		t.Fatalf("freshness gate = %+v, want stale_refreshing", r.freshnessGate)
	}
	if len(r.scheduleLogs) != 0 || logHits != 0 {
		t.Fatalf("a live run has no terminal failure log: logs=%+v hits=%d", r.scheduleLogs, logHits)
	}
}

func TestConvergeApp_VerifySchedulesFailsClosedWithoutServerStaleState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"items":[{"id":8,"name":"refresh","enabled":true}]}`)
	}))
	t.Cleanup(srv.Close)
	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "shk_test"},
		fleet.AppDiff{Slug: "projects", Action: fleet.ActionUnchanged},
		fleet.AppEntry{Slug: "projects"}, fleet.ObservedApp{}, t.TempDir(),
		convergeOpts{verifySchedules: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard,
	)
	if r.status != statusFailed || r.err == nil || !strings.Contains(r.err.Error(), "upgrade the server") {
		t.Fatalf("result = %#v, want actionable unsupported-server failure", r)
	}
	if len(r.freshnessGate) != 1 || r.freshnessGate[0].State != "unavailable" {
		t.Fatalf("freshness gate = %+v", r.freshnessGate)
	}
}

func TestConvergeApp_UnchangedRecordsFleetStateWhenAdvertised(t *testing.T) {
	var body struct {
		Status      string `json:"status"`
		Digest      string `json:"desired_content_digest"`
		Declaration []struct {
			Key     string `json:"key"`
			Desired string `json:"desired"`
		} `json:"declaration"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/apps/a/fleet-state" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	replicas := 2
	d := fleet.AppDiff{Slug: "a", Action: fleet.ActionUnchanged, LocalDigest: "sha256:local"}
	r := convergeApp(&cliConfig{Host: srv.URL, Token: "shk_test"}, d,
		fleet.AppEntry{Slug: "a", Visibility: "private", Config: fleet.Config{Replicas: &replicas}},
		fleet.ObservedApp{}, "", convergeOpts{fleetState: true, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusUnchanged || r.err != nil {
		t.Fatalf("result = %#v", r)
	}
	if body.Status != fleetConvergenceInSync || body.Digest != "sha256:local" {
		t.Fatalf("body = %#v", body)
	}
	if len(body.Declaration) != 2 || body.Declaration[0].Key != "visibility" || body.Declaration[1].Desired != "2" {
		t.Fatalf("declaration = %#v", body.Declaration)
	}
}

func TestConvergeApp_AdoptSkippedWithoutFlag(t *testing.T) {
	cfg := &cliConfig{Host: "http://unused", Token: "x"}
	d := fleet.AppDiff{Slug: "legacy", Action: fleet.ActionAdopt}
	r := convergeApp(cfg, d, fleet.AppEntry{Slug: "legacy"}, fleet.ObservedApp{}, "",
		convergeOpts{adopt: false, preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusSkipped || !strings.Contains(r.note, "--adopt") {
		t.Fatalf("want skipped w/ --adopt hint, got %s %q", r.status, r.note)
	}
}

func TestConvergeApp_AdoptMatchingAppPatchesOwnershipWithoutDeploy(t *testing.T) {
	var deploys int
	var gotDigest, gotManagedBy string
	var sawManagedByHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/apps/legacy":
			gotDigest = r.Header.Get("X-Shinyhub-If-Content-Digest")
			values, ok := r.Header["X-Shinyhub-If-Managed-By"]
			sawManagedByHeader = ok
			if len(values) > 0 {
				gotManagedBy = values[0]
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/deploy"):
			deploys++
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	d := fleet.AppDiff{
		Slug: "legacy", Action: fleet.ActionAdopt,
		LocalDigest: "sha256:SAME", ServerDigest: "sha256:SAME",
	}
	r := convergeApp(&cliConfig{Host: srv.URL, Token: "shk_test"}, d,
		fleet.AppEntry{Slug: "legacy", Visibility: "private"},
		fleet.ObservedApp{Slug: "legacy", Access: "private"}, "",
		convergeOpts{adopt: true, preconditions: true, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusAdopted || r.err != nil {
		t.Fatalf("result = %#v, want adopted", r)
	}
	if deploys != 0 {
		t.Fatalf("matching adoption issued %d deploys, want 0", deploys)
	}
	if gotDigest != "sha256:SAME" || !sawManagedByHeader || gotManagedBy != "" {
		t.Fatalf("ownership preconditions: digest=%q managed_by=%q present=%v", gotDigest, gotManagedBy, sawManagedByHeader)
	}
}

func TestConvergeApp_DeleteSkippedWithoutPrune(t *testing.T) {
	cfg := &cliConfig{Host: "http://unused", Token: "x"}
	d := fleet.AppDiff{Slug: "retired", Action: fleet.ActionDelete}
	r := convergeApp(cfg, d, fleet.AppEntry{}, fleet.ObservedApp{}, "",
		convergeOpts{prune: false, preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusSkipped || !strings.Contains(r.note, "--prune") {
		t.Fatalf("want skipped w/ --prune hint, got %s %q", r.status, r.note)
	}
}

func TestConvergeApp_DeleteDegradedRefused(t *testing.T) {
	cfg := &cliConfig{Host: "http://unused", Token: "x"}
	d := fleet.AppDiff{Slug: "retired", Action: fleet.ActionDelete}
	r := convergeApp(cfg, d, fleet.AppEntry{}, fleet.ObservedApp{}, "",
		convergeOpts{prune: true, preconditions: false, allowDegradedPrune: false, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusSkipped || !strings.Contains(r.note, "degraded") {
		t.Fatalf("want skipped (degraded), got %s %q", r.status, r.note)
	}
}

func TestConvergeApp_UpdateConfigPatchesWithServerDigestPrecondition(t *testing.T) {
	var gotDigest, gotMB string
	var patched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" && r.URL.Path == "/api/apps/cfg" {
			patched = true
			gotDigest = r.Header.Get("X-Shinyhub-If-Content-Digest")
			gotMB = r.Header.Get("X-Shinyhub-If-Managed-By")
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	d := fleet.AppDiff{
		Slug: "cfg", Action: fleet.ActionUpdateConfig, Owned: true,
		ServerDigest: "sha256:SERVER",
		ConfigDrift:  []fleet.ConfigDriftItem{{Key: "replicas", Server: "1", Desired: "2"}},
	}
	entry := fleet.AppEntry{Slug: "cfg", Config: fleet.Config{Replicas: stateInt(2)}}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "cfg"}, "",
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusUpdated {
		t.Fatalf("status = %s (%v), want updated", r.status, r.err)
	}
	if !patched || gotDigest != "sha256:SERVER" || gotMB != "fleet:eu" {
		t.Fatalf("precondition wrong: patched=%v digest=%q mb=%q", patched, gotDigest, gotMB)
	}
}

func TestConvergeApp_UpdateSourceConfigGatesOnPostDeployDigest(t *testing.T) {
	// The single most important correctness property: update(source+config)
	// must deploy first, then patch fleet config with a precondition built
	// from the FRESHLY promoted digest - never the stale pre-deploy one. If
	// the ordering were swapped, the patch would carry the stale digest and
	// 409 against the deployment this very run just performed.
	const staleDigest = "sha256:STALE"
	const promotedDigest = "sha256:PROMOTED"

	var deployedAt, patchedAt int
	var seq int
	var patchDigest string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			seq++
			deployedAt = seq
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/srccfg":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "srccfg", "content_digest": promotedDigest}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/srccfg":
			seq++
			patchedAt = seq
			patchDigest = r.Header.Get("X-Shinyhub-If-Content-Digest")
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{
		Slug: "srccfg", Action: fleet.ActionUpdateSourceConfig, Owned: true,
		ServerDigest: staleDigest,
		ConfigDrift:  []fleet.ConfigDriftItem{{Key: "replicas", Server: "1", Desired: "2"}},
	}
	entry := fleet.AppEntry{Slug: "srccfg", Source: "./x", Visibility: "private",
		Config: fleet.Config{Replicas: stateInt(2)}}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "srccfg"}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusUpdated {
		t.Fatalf("status = %s (%v), want updated", r.status, r.err)
	}
	if deployedAt == 0 || patchedAt == 0 || deployedAt > patchedAt {
		t.Fatalf("ordering wrong: deployedAt=%d patchedAt=%d (deploy must precede patch)", deployedAt, patchedAt)
	}
	if patchDigest != promotedDigest {
		t.Fatalf("config patch precondition = %q, want post-deploy promoted digest %q (not stale %q)",
			patchDigest, promotedDigest, staleDigest)
	}
}

func TestConvergeApp_UpdateSourceReassertsAutoscaleOnly(t *testing.T) {
	// A source-only deploy can overwrite the autoscale columns from the new
	// bundle's shinyhub.toml. convergeApp reasserts ONLY autoscale (gated on the
	// promoted digest): autoscale does not trigger a redeploy, so fleet
	// precedence is restored in one pass. Crucially, `replicas` (declared here)
	// must NOT be re-PATCHed - that would cycle the pool a second time.
	var patchBody map[string]any
	var patchDigest string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/srconly":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "srconly", "content_digest": "sha256:PROMOTED"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/srconly":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &patchBody)
			patchDigest = r.Header.Get("X-Shinyhub-If-Content-Digest")
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")

	en := true
	entry := fleet.AppEntry{Slug: "srconly", Source: "./x", Visibility: "private",
		Config: fleet.Config{Replicas: stateInt(2), Autoscale: &fleet.AutoscaleConfig{Enabled: &en, MinReplicas: 1, MaxReplicas: 8, Target: 0.8}}}
	d := fleet.AppDiff{Slug: "srconly", Action: fleet.ActionUpdateSource, Owned: true, ServerDigest: "sha256:OLD"}

	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "srconly"}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusUpdated {
		t.Fatalf("status = %s (%v), want updated", r.status, r.err)
	}
	if _, ok := patchBody["autoscale"]; !ok {
		t.Errorf("expected an autoscale reassert PATCH, body = %#v", patchBody)
	}
	if _, ok := patchBody["replicas"]; ok {
		t.Error("replicas must NOT be re-PATCHed on a source-only deploy (would cause a second pool cycle)")
	}
	if patchDigest != "sha256:PROMOTED" {
		t.Errorf("reassert precondition = %q, want the promoted digest", patchDigest)
	}
}

func TestConvergeApp_UpdateSourceConfigReassertsAutoscale(t *testing.T) {
	// Source+config change where autoscale matched at plan time (so it is NOT in
	// d.ConfigDrift) but another key (replicas) drifted. The redeployed bundle can
	// still overwrite autoscale, so it must be reasserted after the deploy even
	// though it was absent from the pre-deploy drift list.
	var sawAutoscalePatch bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/srccfg":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "srccfg", "content_digest": "sha256:PROMOTED"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/srccfg":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if _, ok := body["autoscale"]; ok {
				sawAutoscalePatch = true
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")

	en := true
	entry := fleet.AppEntry{Slug: "srccfg", Source: "./x", Visibility: "private",
		Config: fleet.Config{Replicas: stateInt(2), Autoscale: &fleet.AutoscaleConfig{Enabled: &en, MinReplicas: 1, MaxReplicas: 8, Target: 0.8}}}
	// Pre-deploy drift is replicas only; autoscale matched at plan time.
	d := fleet.AppDiff{Slug: "srccfg", Action: fleet.ActionUpdateSourceConfig, Owned: true,
		ServerDigest: "sha256:OLD", ConfigDrift: []fleet.ConfigDriftItem{{Key: "replicas", Server: "1", Desired: "2"}}}

	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "srccfg"}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusUpdated {
		t.Fatalf("status = %s (%v), want updated", r.status, r.err)
	}
	if !sawAutoscalePatch {
		t.Error("autoscale must be reasserted after a source+config deploy even when absent from the pre-deploy drift")
	}
}

func TestConvergeApp_UpdateSourceConfigReassertFailureIsPartial(t *testing.T) {
	var patches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case r.Method == "GET" && r.URL.Path == "/api/apps/sc":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "sc", "content_digest": "sha256:NEW"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/sc":
			patches++
			if patches == 2 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":"reassert failed"}`)
			}
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	enabled := true
	entry := fleet.AppEntry{Slug: "sc", Config: fleet.Config{
		Replicas:  stateInt(2),
		Autoscale: &fleet.AutoscaleConfig{Enabled: &enabled, MinReplicas: 1, MaxReplicas: 3, Target: .8},
	}}
	d := fleet.AppDiff{Slug: "sc", Action: fleet.ActionUpdateSourceConfig, LocalDigest: "sha256:NEW",
		ConfigDrift: []fleet.ConfigDriftItem{{Key: "replicas", Server: "1", Desired: "2"}}}
	r := convergeApp(&cliConfig{Host: srv.URL, Token: "tok"}, d, entry, fleet.ObservedApp{}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusFailed || r.failureKind != failureConfigReassertFailed || r.mutation != mutationPartial {
		t.Fatalf("result = %+v, want failed/config_reassert_failed/partial", r)
	}
	if code, _ := applyExitCode([]applyResult{r}); code != 4 {
		t.Fatalf("exit = %d, want 4", code)
	}
}

func TestConvergeApp_UnknownActionFailsClosed(t *testing.T) {
	r := convergeApp(&cliConfig{}, fleet.AppDiff{Slug: "mystery", Action: fleet.Action("future")},
		fleet.AppEntry{}, fleet.ObservedApp{}, "", convergeOpts{}, "fleet:eu", io.Discard)
	if r.status != statusFailed || r.failureKind != failureInvalidAction {
		t.Fatalf("unknown action result = %+v, want failed/invalid_action", r)
	}
}

func TestConvergeApp_AdoptReservesOwnershipBeforeDeployAndReleasesOnFailure(t *testing.T) {
	// Two properties at once:
	//  1. Ownership is RESERVED (preconditioned stamp) BEFORE the bundle is
	//     uploaded, so a concurrent ownership change is rejected before any
	//     deploy can overwrite an app we no longer own.
	//  2. If the redeploy then fails, the reservation is RELEASED (managed_by
	//     restored to its observed prior value) so the app is never left
	//     "owned but undeployed" and the next plan proposes a clean adopt.
	var mbValues []any // ordered managed_by values seen on PATCH /api/apps/legacy
	var deployedBeforeReserve bool
	var reserved bool
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			mu.Lock()
			if !reserved {
				deployedBeforeReserve = true
			}
			mu.Unlock()
			// 400 = clean client-side rejection: nothing committed, so the
			// reservation is safe to release.
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"bundle rejected"}`))
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/legacy":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if v, ok := body["managed_by"]; ok {
				mu.Lock()
				mbValues = append(mbValues, v)
				if v == "fleet:eu" {
					reserved = true
				}
				mu.Unlock()
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "legacy", Action: fleet.ActionAdopt}
	entry := fleet.AppEntry{Slug: "legacy", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "legacy"}, dir,
		convergeOpts{adopt: true, preconditions: true, retries: 0, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	mu.Lock()
	defer mu.Unlock()
	if deployedBeforeReserve {
		t.Fatalf("bundle was deployed before ownership was reserved (TOCTOU overwrite risk)")
	}
	// Net effect: reserve then release. First managed_by stamp is the marker,
	// last is the cleared (nil) value - so ownership is not left stamped.
	if len(mbValues) < 2 {
		t.Fatalf("want a reserve + release pair, got managed_by sequence %v", mbValues)
	}
	if mbValues[0] != "fleet:eu" {
		t.Fatalf("first managed_by must reserve the marker, got %v", mbValues[0])
	}
	if last := mbValues[len(mbValues)-1]; last != nil {
		t.Fatalf("ownership must be released (managed_by=null) after a failed adopt deploy, got %v", last)
	}
}

func TestConvergeApp_AdoptDegradedDoesNotReleaseUnguarded(t *testing.T) {
	// In degraded mode (no precondition support) the release PATCH would carry
	// no If-Managed-By guard, so it could clear or overwrite a new owner that
	// took the app between our reservation and the deploy failure. We therefore
	// must NOT issue an unguarded release in degraded mode - the documented
	// degraded race is accepted rather than risking a clobber. Matching digests
	// deliberately do not enable metadata-only adoption here: without a digest
	// precondition, the observation could already be stale, so deploy remains
	// the conservative path.
	var mbValues []any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"bundle rejected"}`))
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/legacy":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if v, ok := body["managed_by"]; ok {
				mu.Lock()
				mbValues = append(mbValues, v)
				mu.Unlock()
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{
		Slug: "legacy", Action: fleet.ActionAdopt,
		LocalDigest: "sha256:SAME", ServerDigest: "sha256:SAME",
	}
	entry := fleet.AppEntry{Slug: "legacy", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "legacy"}, dir,
		convergeOpts{adopt: true, preconditions: false, retries: 0, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(mbValues); i++ {
		t.Fatalf("degraded mode must not issue a release PATCH; managed_by sequence = %v", mbValues)
	}
}

func TestConvergeApp_AdoptDoesNotReleaseAfterCommittedDeploy(t *testing.T) {
	// If the bundle POST is accepted (deploy committed) but the post-deploy
	// health wait then fails, this fleet's source is now running on the app.
	// Releasing ownership back to the prior owner would leave the app marked
	// as theirs while running OUR bundle - an inconsistent state worse than
	// keeping the marker. The reservation must be kept once the deploy commits.
	var mbValues []any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(200) // deploy COMMITS
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/legacy":
			// Health wait sees a terminal crash -> deploy fails post-commit.
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "crashed"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/legacy":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if v, ok := body["managed_by"]; ok {
				mu.Lock()
				mbValues = append(mbValues, v)
				mu.Unlock()
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "legacy", Action: fleet.ActionAdopt}
	entry := fleet.AppEntry{Slug: "legacy", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "legacy"}, dir,
		convergeOpts{adopt: true, preconditions: true, retries: 0, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(mbValues) != 1 || mbValues[0] != "fleet:eu" {
		t.Fatalf("after a committed deploy, ownership must be reserved and NOT released; managed_by sequence = %v", mbValues)
	}
}

func TestConvergeApp_AdoptKeepsOwnershipWhenBundleWentLive(t *testing.T) {
	// The deploy endpoint returns 500 on post-promotion paths (e.g. manifest
	// schedule apply) with the new bundle already live. The HTTP status cannot
	// distinguish that from a pre-promotion 500, so the adopt path reads back
	// the live digest: here it advanced past the pre-deploy digest, proving the
	// bundle went live, so the ownership reservation must be KEPT (not released).
	var mbValues []any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(500) // ambiguous: post-promotion failure
			_, _ = w.Write([]byte(`{"error":"schedule apply failed: boom"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			// Live digest advanced past the pre-deploy one -> bundle went live.
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "legacy", "content_digest": "sha256:NEW"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/legacy":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if v, ok := body["managed_by"]; ok {
				mu.Lock()
				mbValues = append(mbValues, v)
				mu.Unlock()
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "legacy", Action: fleet.ActionAdopt, ServerDigest: "sha256:OLD"}
	entry := fleet.AppEntry{Slug: "legacy", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "legacy"}, dir,
		convergeOpts{adopt: true, preconditions: true, retries: 0, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(mbValues) != 1 || mbValues[0] != "fleet:eu" {
		t.Fatalf("a bundle that went live must keep its ownership reservation; managed_by sequence = %v", mbValues)
	}
}

func TestConvergeApp_ConflictRecordedNotRetried(t *testing.T) {
	var patchCalls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			mu.Lock()
			patchCalls++
			mu.Unlock()
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"precondition failed (re-run plan)"}`))
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	d := fleet.AppDiff{Slug: "cfg", Action: fleet.ActionUpdateConfig, Owned: true,
		ServerDigest: "sha256:S", ConfigDrift: []fleet.ConfigDriftItem{{Key: "replicas", Desired: "2"}}}
	entry := fleet.AppEntry{Slug: "cfg", Config: fleet.Config{Replicas: stateInt(2)}}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{}, "",
		convergeOpts{preconditions: true, retries: 3, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusConflict {
		t.Fatalf("status = %s, want conflict", r.status)
	}
	if patchCalls != 1 {
		t.Fatalf("conflict must NOT be retried, patch called %d times", patchCalls)
	}
}

// TestConvergeApp_FailedDeployAttachesLogTail verifies that when a deploy fails
// its health check (the app crashed on startup), the per-app result and the
// JSON envelope carry the app's log tail - including the exception line - so
// the operator does not have to SSH to the host to read app-0.log.
func TestConvergeApp_FailedDeployAttachesLogTail(t *testing.T) {
	const crashLine = "ModuleNotFoundError: No module named 'pandas'"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"deploy failed: the app did not pass its health check - it likely crashed on startup. Check the app logs for the cause."}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo/logs":
			_, _ = io.WriteString(w, "Traceback (most recent call last):\n  File \"app.py\", line 12, in <module>\n"+crashLine+"\n")
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "crashed"}})
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "demo", Action: fleet.ActionCreate}
	entry := fleet.AppEntry{Slug: "demo", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)

	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	if !strings.Contains(strings.Join(r.logTail, "\n"), crashLine) {
		t.Fatalf("result log tail must contain the crash exception; got %q", r.logTail)
	}

	// The JSON envelope must carry log_tail too, so explicit machine-mode
	// callers get the cause without a second call.
	var buf strings.Builder
	m := &fleet.Manifest{FleetID: "eu"}
	if err := writeFleetApplyJSON(&buf, m, cfg.Host, []fleet.AppDiff{d}, nil, applyOutcome{apps: []applyResult{r}}, 4, "PARTIAL"); err != nil {
		t.Fatalf("writeFleetApplyJSON: %v", err)
	}
	if !strings.Contains(buf.String(), crashLine) {
		t.Fatalf("JSON envelope must include log_tail with the exception; got %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"log_tail"`) {
		t.Fatalf("JSON envelope must use the log_tail key; got %s", buf.String())
	}
}

// TestConvergeApp_PostDeployPatchFailureHasNoLogTail verifies the log tail is
// attached only when the deploy itself failed. When the bundle deploys fine but
// a follow-up config PATCH fails, the app is running, so its log tail would be
// misleading and must NOT be fetched or attached.
func TestConvergeApp_PostDeployPatchFailureHasNoLogTail(t *testing.T) {
	var logsHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "sc", "content_digest": "sha256:NEW"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/sc":
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"patch boom"}`))
		case r.URL.Path == "/api/apps/sc/logs":
			logsHit = true
			_, _ = io.WriteString(w, "should not be fetched\n")
		case r.Method == "GET" && r.URL.Path == "/api/apps/sc":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{
		Slug: "sc", Action: fleet.ActionUpdateSourceConfig, Owned: true,
		ServerDigest: "sha256:OLD",
		ConfigDrift:  []fleet.ConfigDriftItem{{Key: "replicas", Server: "1", Desired: "2"}},
	}
	entry := fleet.AppEntry{Slug: "sc", Config: fleet.Config{Replicas: stateInt(2)}}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "sc"}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)

	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	if r.mutation != mutationPartial {
		t.Fatalf("mutation = %s, want partial after deploy succeeded and config patch failed", r.mutation)
	}
	if len(r.logTail) != 0 {
		t.Fatalf("post-deploy patch failure must not attach a log tail, got %v", r.logTail)
	}
	if logsHit {
		t.Errorf("the logs endpoint must not be queried for a post-deploy patch failure")
	}
}

func TestConvergeApp_CreateDeploysThenStampsMarker(t *testing.T) {
	var deployed, stamped bool
	var stampBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			deployed = true
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/new":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "new", "content_digest": "sha256:NEW"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/new":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &stampBody)
			stamped = true
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "new", Action: fleet.ActionCreate}
	entry := fleet.AppEntry{Slug: "new", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusCreated {
		t.Fatalf("status = %s (%v), want created", r.status, r.err)
	}
	if len(r.logTail) != 0 {
		t.Fatalf("a successful create must not attach a log tail, got %v", r.logTail)
	}
	if !deployed || !stamped {
		t.Fatalf("deployed=%v stamped=%v, want both", deployed, stamped)
	}
	if v, _ := stampBody["managed_by"].(string); v != "fleet:eu" {
		t.Fatalf("marker body = %#v", stampBody["managed_by"])
	}
}

func TestConvergeApp_CreateWithoutDeployRunRefFailsWarmPostcondition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/deploy"):
			// Even when the response omits deploy_run, waiting must evaluate the
			// declared schedule's bundle-specific postcondition.
			_, _ = io.WriteString(w, `{"status":"ok","manifest":{"schedules":[{"name":"warm","schedule_id":7}]}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/new":
			_, _ = io.WriteString(w, `{"app":{"status":"running"},"compatibility_quarantined":false,"producer_repair_required":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps":
			_, _ = io.WriteString(w, `[{"slug":"new","content_digest":"sha256:NEW"}]`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/apps/new":
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/new/schedules/reconcile":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/new/schedules":
			_, _ = io.WriteString(w, `{"items":[{"id":7,"name":"warm","enabled":true,"deploy_trigger":"bundle_change","stale":false,"refreshing":false,"current_app_version":"v2","current_content_digest":"sha256:NEW","deploy_trigger_satisfied":false,"convergence_status":"failed"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	mustWrite(t, filepath.Join(dir, "shinyhub.toml"), `[[schedule]]
name = "warm"
cron = "0 5 * * *"
cmd = "true"
deploy_trigger = "bundle_change"
`)
	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "test"},
		fleet.AppDiff{Slug: "new", Action: fleet.ActionCreate},
		fleet.AppEntry{Slug: "new", Source: "./x", Visibility: "private"},
		fleet.ObservedApp{}, dir,
		convergeOpts{preconditions: true, waitForWarm: true, warmTimeout: time.Second, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard,
	)
	if r.status != statusFailed || r.err == nil || !strings.Contains(r.err.Error(), "after this apply") {
		t.Fatalf("result = %#v, want current-apply warm postcondition failure", r)
	}
	if r.failureKind != failureWarmNeverSucceeded {
		t.Fatalf("failure kind = %q, want %q", r.failureKind, failureWarmNeverSucceeded)
	}
}

func TestConvergeApp_RestartsOnlyAfterWarmLevelPostconditionPasses(t *testing.T) {
	var restartHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/deploy"):
			_, _ = io.WriteString(w, `{"status":"ok","manifest":{"schedules":[{"name":"warm","schedule_id":7,"deploy_run":{"run_id":9}}]}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/new/schedules/7/runs/9":
			_, _ = io.WriteString(w, `{"status":"succeeded"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/new/schedules":
			_, _ = io.WriteString(w, `{"items":[{"id":7,"name":"warm","enabled":true,"last_run_id":9,"last_run_status":"succeeded","last_success_at":"2026-08-24T12:00:00Z","producer_content_digest":"sha256:NEW","current_content_digest":"sha256:NEW","deploy_trigger_satisfied":true,"stale":false,"refreshing":false}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/new":
			_, _ = io.WriteString(w, `{"app":{"status":"running"},"compatibility_quarantined":false,"producer_repair_required":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps":
			_, _ = io.WriteString(w, `[{"slug":"new","content_digest":"sha256:NEW"}]`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/apps/new":
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/new/schedules/reconcile":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/new/restart":
			restartHits++
			_, _ = io.WriteString(w, `{"status":"running"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	mustWrite(t, filepath.Join(dir, "shinyhub.toml"), `[[schedule]]
name = "warm"
cron = "0 5 * * *"
cmd = "true"
deploy_trigger = "bundle_change"
`)
	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "test"},
		fleet.AppDiff{Slug: "new", Action: fleet.ActionCreate},
		fleet.AppEntry{Slug: "new", Source: "./x", Visibility: "private"},
		fleet.ObservedApp{}, dir,
		convergeOpts{preconditions: true, waitForWarm: true, restartAfterWarm: true, warmTimeout: time.Second, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard,
	)
	if r.status != statusCreated || r.err != nil {
		t.Fatalf("result = %#v, want created", r)
	}
	if restartHits != 1 || !r.warmRestarted {
		t.Fatalf("restart hits=%d warm_restarted=%v, want 1/true", restartHits, r.warmRestarted)
	}
}

// A deploy that fails its first attempt with a readiness timeout, then succeeds
// on retry, must still record WHY attempt 1 failed - that is the motivating case
// (an app that eventually came up but flaked once).
func TestConvergeApp_RetriedSuccessRecordsFailedAttemptKind(t *testing.T) {
	var deployHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			if atomic.AddInt32(&deployHits, 1) == 1 {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"error":"deploy failed: ...","failure_kind":"readiness_timeout"}`))
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/flaky":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "flaky", "content_digest": "sha256:NEW"}})
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "flaky", Action: fleet.ActionCreate}
	entry := fleet.AppEntry{Slug: "flaky", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{}, dir,
		convergeOpts{preconditions: true, retries: 1, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)

	if r.status != statusCreated {
		t.Fatalf("status = %s (%v), want created (second attempt succeeds)", r.status, r.err)
	}
	if len(r.attemptsDetail) != 1 {
		t.Fatalf("want exactly one failed-attempt record, got %d: %+v", len(r.attemptsDetail), r.attemptsDetail)
	}
	if r.attemptsDetail[0].Kind != deployfail.ReadinessTimeout || r.attemptsDetail[0].Attempt != 1 {
		t.Fatalf("attempt 1 record = %+v, want {Attempt:1 Kind:readiness_timeout}", r.attemptsDetail[0])
	}
}

func TestDeployWithRetry_CommittedAttemptDoesNotUploadAgain(t *testing.T) {
	var deployHits atomic.Int32
	var deployed atomic.Bool
	var healthHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo" && !deployed.Load():
			_, _ = io.WriteString(w, `{"app":{"status":"running"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/deploy":
			deployHits.Add(1)
			deployed.Store(true)
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			if healthHits.Add(1) == 1 {
				time.Sleep(20 * time.Millisecond)
				_, _ = io.WriteString(w, `{"app":{"status":"starting"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"app":{"status":"running"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps":
			_, _ = io.WriteString(w, `[{"slug":"demo","content_digest":"sha256:new"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	promoted, attempts, committed, _, failed, err := deployWithRetry(
		&cliConfig{Host: srv.URL, Token: "test"}, "demo", bundleBuildSpec{Dir: dir},
		"private", "", convergeOpts{retries: 1, preconditions: true, healthTimeout: 5 * time.Millisecond, runID: "run"},
		io.Discard, "sha256:old", "fleet:prod",
	)
	if err != nil || promoted != "sha256:new" || !committed || attempts != 2 || len(failed) != 1 {
		t.Fatalf("result promoted=%q attempts=%d committed=%v failed=%+v err=%v", promoted, attempts, committed, failed, err)
	}
	if deployHits.Load() != 1 {
		t.Fatalf("deploy uploads = %d, want 1; committed retry must only re-check convergence", deployHits.Load())
	}
}

func TestConvergeApp_UpdateSourceDeployUsesPlanPreconditions(t *testing.T) {
	var digest, managedBy string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			_, _ = io.WriteString(w, `{"app":{"status":"running"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/deploy":
			digest = r.Header.Get("X-Shinyhub-If-Content-Digest")
			managedBy = r.Header.Get("X-Shinyhub-If-Managed-By")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"precondition failed: content changed"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	r := convergeApp(
		&cliConfig{Host: srv.URL, Token: "test"},
		fleet.AppDiff{Slug: "demo", Action: fleet.ActionUpdateSource, ServerDigest: "sha256:planned"},
		fleet.AppEntry{Slug: "demo", Visibility: "private"}, fleet.ObservedApp{}, dir,
		convergeOpts{preconditions: true, fleetID: "prod", runID: "run"}, "fleet:prod", io.Discard,
	)
	if r.status != statusConflict || resultMutationState(r) != mutationNone {
		t.Fatalf("result = %#v, want a pre-mutation conflict", r)
	}
	if digest != "sha256:planned" || managedBy != "fleet:prod" {
		t.Fatalf("deploy preconditions digest=%q managed_by=%q", digest, managedBy)
	}
}

func TestRetryableDeployFailure_RejectsDeterministicFailures(t *testing.T) {
	for _, kind := range []deployfail.Kind{
		deployfail.RuntimeMissing, deployfail.BuildFailed, deployfail.InterpreterUnavailable,
		deployfail.HookFailed, deployfail.BundleInvalid, deployfail.Crashed, deployfail.ZipError,
	} {
		if retryableDeployFailure(kind) {
			t.Errorf("%s is deterministic and must not be retried implicitly", kind)
		}
	}
	for _, kind := range []deployfail.Kind{deployfail.ReadinessTimeout, deployfail.ServerError, deployfail.TransportError} {
		if !retryableDeployFailure(kind) {
			t.Errorf("%s should remain retryable", kind)
		}
	}
}

func TestConvergeApp_DeterministicDeployFailureIsNotRetried(t *testing.T) {
	var deployHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy") {
			atomic.AddInt32(&deployHits, 1)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"dependency resolution failed","failure_kind":"build_failed"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	r := convergeApp(cfg,
		fleet.AppDiff{Slug: "broken", Action: fleet.ActionCreate},
		fleet.AppEntry{Slug: "broken", Source: "./x", Visibility: "private"},
		fleet.ObservedApp{}, dir,
		convergeOpts{preconditions: true, retries: 3, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusFailed || r.attempts != 1 || atomic.LoadInt32(&deployHits) != 1 {
		t.Fatalf("status=%s attempts=%d deploy_hits=%d; deterministic failure must stop after one attempt",
			r.status, r.attempts, deployHits)
	}
	if len(r.attemptsDetail) != 1 || r.attemptsDetail[0].Kind != deployfail.BuildFailed {
		t.Fatalf("attempt detail = %+v, want one build_failed", r.attemptsDetail)
	}
}

// buildConcurrencyPreflight makes a preflightResult of n ActionUpdateSource apps
// (app0..app(n-1)), all sourced from dir, for exercising convergeFleet.
func buildConcurrencyPreflight(n int, dir string) *preflightResult {
	apps := make([]fleet.AppEntry, n)
	diff := make([]fleet.AppDiff, n)
	observed := make(map[string]fleet.ObservedApp, n)
	bundles := make(map[string]bundleBuildSpec, n)
	for i := 0; i < n; i++ {
		s := fmt.Sprintf("app%d", i)
		apps[i] = fleet.AppEntry{Slug: s, Source: "./x", Visibility: "private"}
		diff[i] = fleet.AppDiff{Slug: s, Action: fleet.ActionUpdateSource, Owned: true, ServerDigest: "sha256:OLD"}
		observed[s] = fleet.ObservedApp{Slug: s}
		bundles[s] = bundleBuildSpec{Dir: dir}
	}
	return &preflightResult{
		manifest: &fleet.Manifest{FleetID: "eu", Apps: apps},
		diff:     diff, observed: observed, bundles: bundles,
	}
}

// concurrencyTestServer answers the deploy/health/list calls convergeApp makes
// for an ActionUpdateSource app, tracking the max number of concurrent deploys.
func concurrencyTestServer(t *testing.T, n int, maxInflight *atomic.Int32, sleep time.Duration) *httptest.Server {
	t.Helper()
	var inflight atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			cur := inflight.Add(1)
			for {
				m := maxInflight.Load()
				if cur <= m || maxInflight.CompareAndSwap(m, cur) {
					break
				}
			}
			time.Sleep(sleep)
			inflight.Add(-1)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			apps := make([]map[string]any, n)
			for i := 0; i < n; i++ {
				apps[i] = map[string]any{"slug": fmt.Sprintf("app%d", i), "content_digest": "sha256:NEW"}
			}
			_ = json.NewEncoder(w).Encode(apps)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/apps/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConvergeFleet_BoundedConcurrency(t *testing.T) {
	const n, limit = 6, 4
	var maxInflight atomic.Int32
	srv := concurrencyTestServer(t, n, &maxInflight, 40*time.Millisecond)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")

	pf := buildConcurrencyPreflight(n, dir)
	opt := convergeOpts{preconditions: true, concurrency: limit, fleetID: "eu", runID: "r"}
	results := convergeFleet(cfg, pf, opt, io.Discard)

	if got := maxInflight.Load(); got <= 1 || int(got) > limit {
		t.Fatalf("max concurrent deploys = %d, want >1 and <=%d", got, limit)
	}
	if len(results) != n {
		t.Fatalf("got %d results, want %d", len(results), n)
	}
	for i, r := range results {
		if want := fmt.Sprintf("app%d", i); r.slug != want {
			t.Fatalf("results[%d].slug = %q, want %q (manifest order preserved)", i, r.slug, want)
		}
		if r.status != statusUpdated {
			t.Fatalf("app%d status = %s (%v), want updated", i, r.status, r.err)
		}
	}
}

func TestConvergeFleet_SerialWhenConcurrencyOne(t *testing.T) {
	const n = 4
	var maxInflight atomic.Int32
	srv := concurrencyTestServer(t, n, &maxInflight, 10*time.Millisecond)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")

	pf := buildConcurrencyPreflight(n, dir)
	opt := convergeOpts{preconditions: true, concurrency: 1, fleetID: "eu", runID: "r"}
	results := convergeFleet(cfg, pf, opt, io.Discard)

	if got := maxInflight.Load(); got != 1 {
		t.Fatalf("concurrency 1 must be serial; max concurrent deploys = %d, want 1", got)
	}
	if len(results) != n {
		t.Fatalf("got %d results, want %d", len(results), n)
	}
}

// Parallelism must not change the exit code: a mixed diff (one app fails its
// deploy, the rest succeed) must tally to the same applyExitCode under serial
// and parallel convergence (spec section 8).
func TestConvergeFleet_ExitCodeParityParallelSerial(t *testing.T) {
	const n = 4
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	// app0 always fails its deploy (500); app1..app3 succeed.
	mkServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/apps/app0/"):
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"error":"deploy app0 failed"}`))
			case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			case r.URL.Path == "/api/apps":
				apps := make([]map[string]any, n)
				for i := 0; i < n; i++ {
					apps[i] = map[string]any{"slug": fmt.Sprintf("app%d", i), "content_digest": "sha256:NEW"}
				}
				_ = json.NewEncoder(w).Encode(apps)
			case strings.HasSuffix(r.URL.Path, "/logs"):
				_, _ = io.WriteString(w, "boom\n")
			case strings.HasPrefix(r.URL.Path, "/api/apps/"):
				_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
			default:
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{}`))
			}
		}))
	}
	run := func(concurrency int) (int, string) {
		srv := mkServer()
		defer srv.Close()
		cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
		opt := convergeOpts{preconditions: true, concurrency: concurrency, fleetID: "eu", runID: "r"}
		return applyExitCode(convergeFleet(cfg, buildConcurrencyPreflight(n, dir), opt, io.Discard))
	}
	sCode, sReason := run(1)
	pCode, pReason := run(4)
	if sCode != pCode || sReason != pReason {
		t.Fatalf("exit differs: serial=(%d,%q) parallel=(%d,%q)", sCode, sReason, pCode, pReason)
	}
	if sCode != 4 {
		t.Fatalf("one failing app must yield exit 4, got %d (%q)", sCode, sReason)
	}
}

func TestApplyConfigDriftSendsProjectSlug(t *testing.T) {
	var got map[string]any
	cfg := fleetProjectSrv(t, func(w http.ResponseWriter, r *http.Request) {
		got = jsonBody(t, r)
		w.WriteHeader(http.StatusOK)
	})
	drift := []fleet.ConfigDriftItem{{Key: "project", Server: `"old"`, Desired: `"new"`}}
	if err := applyConfigDrift(cfg, "a", drift, fleet.Config{Project: strp("new")}, nil, nil, "run"); err != nil {
		t.Fatalf("applyConfigDrift: %v", err)
	}
	// The API field is project_slug even though the drift key is project; the
	// drift item's Desired is a quoted display string, never a value to send.
	if got["project_slug"] != "new" {
		t.Errorf("body = %v, want project_slug=new", got)
	}
	if _, wrong := got["project"]; wrong {
		t.Error("body must not carry the manifest key name")
	}

	// A declared empty project is a real value (ungroup the app), not an
	// absent key, so it must still be sent: keying off *Project != "" would
	// silently drop this PATCH and leave the app grouped.
	drift = []fleet.ConfigDriftItem{{Key: "project", Server: `"old"`, Desired: `""`}}
	if err := applyConfigDrift(cfg, "a", drift, fleet.Config{Project: strp("")}, nil, nil, "run"); err != nil {
		t.Fatalf("applyConfigDrift: %v", err)
	}
	v, present := got["project_slug"]
	if !present || v != "" {
		t.Errorf(`body = %#v (present=%v), want an explicit empty project_slug`, v, present)
	}
}

func TestReassertFleetConfigIncludesProject(t *testing.T) {
	var got map[string]any
	cfg := fleetProjectSrv(t, func(w http.ResponseWriter, r *http.Request) {
		got = jsonBody(t, r)
		w.WriteHeader(http.StatusOK)
	})
	// Without this, a bundle declaring [app] project = "bundle-project" wins
	// over a fleet manifest declaring project = "fleet-project": the deploy
	// applies the bundle's value and nothing corrects it, inverting the
	// documented "fleet manifest is the outer authority" order.
	if err := reassertFleetConfig(cfg, "a", fleet.Config{Project: strp("fleet-project")}, nil, nil, "run"); err != nil {
		t.Fatalf("reassertFleetConfig: %v", err)
	}
	if got["project_slug"] != "fleet-project" {
		t.Errorf("body = %v, want project_slug=fleet-project", got)
	}

	// A fleet manifest declaring project = "" means the fleet wants the app
	// ungrouped, and that must be reasserted like any other declared value;
	// keying off *Project != "" would treat it as undeclared and skip it.
	if err := reassertFleetConfig(cfg, "a", fleet.Config{Project: strp("")}, nil, nil, "run"); err != nil {
		t.Fatalf("reassertFleetConfig: %v", err)
	}
	v, present := got["project_slug"]
	if !present || v != "" {
		t.Errorf(`body = %#v (present=%v), want an explicit empty project_slug`, v, present)
	}
}

func TestDeclaredStringProject(t *testing.T) {
	if v := declaredString(fleet.Config{Project: strp("p")}, "project"); v == nil || *v != "p" {
		t.Errorf("declaredString(project) = %v, want p", v)
	}
	if v := declaredString(fleet.Config{}, "project"); v != nil {
		t.Errorf("an undeclared project must return nil, got %v", v)
	}
}

func TestResolveDeployRuns_DefersRestartUntilLevelPostcondition(t *testing.T) {
	var restartHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo/schedules/7/runs/9":
			_, _ = io.WriteString(w, `{"status":"succeeded"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			_, _ = io.WriteString(w, `{"app":{"status":"running"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/restart":
			restartHits++
			_, _ = io.WriteString(w, `{"status":"running"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res := applyResult{}
	err := resolveDeployRuns(
		&cliConfig{Host: srv.URL, Token: "test"}, "demo",
		[]deployRunRef{{Schedule: "warm", ScheduleID: 7, RunID: 9}},
		convergeOpts{waitForWarm: true, restartAfterWarm: true, healthTimeout: time.Second},
		&res, io.Discard,
	)
	if err != nil {
		t.Fatalf("resolveDeployRuns: %v", err)
	}
	if restartHits != 0 || res.warmRestarted {
		t.Fatalf("restartHits=%d warmRestarted=%v, want deferred 0/false", restartHits, res.warmRestarted)
	}
}

func TestResolveDeployRuns_FailureAttachesScheduleLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/demo/schedules/7/runs/9":
			_, _ = io.WriteString(w, `{"status":"failed"}`)
		case "/api/apps/demo/schedules/7/runs/9/logs":
			_, _ = io.WriteString(w, "Traceback\nTABLE_NOT_FOUND\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res := applyResult{}
	err := resolveDeployRuns(
		&cliConfig{Host: srv.URL, Token: "test"}, "demo",
		[]deployRunRef{{Schedule: "warm", ScheduleID: 7, RunID: 9}},
		convergeOpts{waitForWarm: true, healthTimeout: time.Second},
		&res, io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "deploy-triggered run failed") {
		t.Fatalf("error = %v, want deploy-triggered run failure", err)
	}
	if len(res.scheduleLogs) != 1 || res.scheduleLogs[0].RunID != 9 {
		t.Fatalf("schedule logs = %+v, want run 9", res.scheduleLogs)
	}
	if !strings.Contains(strings.Join(res.scheduleLogs[0].Tail, "\n"), "TABLE_NOT_FOUND") {
		t.Fatalf("schedule tail = %q, want cause", res.scheduleLogs[0].Tail)
	}
}

func TestResolveDeployRuns_TimeoutIsFatalAndClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/demo/schedules/7/runs/9":
			_, _ = io.WriteString(w, `{"status":"running"}`)
		case "/api/apps/demo/schedules/7/runs/9/logs":
			_, _ = io.WriteString(w, "still fetching\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	res := applyResult{}
	started := time.Now()
	err := resolveDeployRuns(
		&cliConfig{Host: srv.URL, Token: "test"}, "demo",
		[]deployRunRef{{Schedule: "warm", ScheduleID: 7, RunID: 9}},
		convergeOpts{waitForWarm: true, warmTimeout: 20 * time.Millisecond},
		&res, io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("error = %v, want fatal unconfirmed warm-up", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("20ms warm deadline took %v", elapsed)
	}
	if res.failureKind != failureWarmWaitTimeout {
		t.Fatalf("failure kind = %q, want %q", res.failureKind, failureWarmWaitTimeout)
	}
	if len(res.scheduleLogs) != 1 || res.scheduleLogs[0].RunID != 9 {
		t.Fatalf("schedule logs = %+v, want timed-out run 9", res.scheduleLogs)
	}
}

func TestFleetDeployRunLabelQualifiesScheduleWithApp(t *testing.T) {
	got := fleetDeployRunLabel("reporting", "refresh-database")
	if got != "reporting/refresh-database" {
		t.Fatalf("fleetDeployRunLabel = %q, want %q", got, "reporting/refresh-database")
	}
}

func TestResolveDeployRuns_OverlapRequiresLaterLevelPostcondition(t *testing.T) {
	var restartHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo/schedules/7/runs/9" {
			_, _ = io.WriteString(w, `{"status":"skipped_overlap"}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/restart") {
			restartHits++
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	res := applyResult{}
	err := resolveDeployRuns(
		&cliConfig{Host: srv.URL, Token: "test"}, "demo",
		[]deployRunRef{{Schedule: "warm", ScheduleID: 7, RunID: 9}},
		convergeOpts{waitForWarm: true, restartAfterWarm: true, healthTimeout: time.Second},
		&res, io.Discard,
	)
	if err != nil {
		t.Fatalf("overlap should be recorded for the authoritative level check, got %v", err)
	}
	if restartHits != 0 || res.warmRestarted {
		t.Fatalf("restartHits=%d warmRestarted=%v, want 0/false", restartHits, res.warmRestarted)
	}
	if len(res.deployRuns) != 1 || res.deployRuns[0].Status != "skipped_overlap" {
		t.Fatalf("first fires = %+v, want recorded overlap", res.deployRuns)
	}
}
