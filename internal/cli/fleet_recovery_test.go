package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/fleet"
)

func TestFleetRecoveryExecutionFencesAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name       string
		adopt      bool
		config     bool
		transition string
		wantStatus applyStatus
		wantDeploy int
		wantOwner  bool
	}{
		{"owned recovery", false, false, "", statusUpdated, 1, true},
		{"recovery and config reassertion", false, true, "", statusUpdated, 1, true},
		{"adopt recovery", true, false, "", statusAdopted, 1, true},
		{"adopt state conflict releases reservation", true, false, "before-source", statusConflict, 0, false},
		{"adopt protocol error releases reservation", true, false, "before-metadata", statusFailed, 0, false},
		{"adopt rollback failure retains reservation", true, false, "rollback-error", statusConflict, 0, true},
		{"adopt unrelated release is not our promotion", true, false, "during-other-release", statusConflict, 0, false},
		{"adopt downtime refusal releases reservation", true, false, "downtime", statusFailed, 0, false},
		{"adoption requires authorization", true, false, "adopt-unauthorized", statusSkipped, 0, false},
		{"repaired before upload", false, false, "before-repair", statusUnchanged, 0, true},
		{"pending before upload", false, false, "before-pending", statusConflict, 0, true},
		{"stopped before upload", false, false, "before-stopped", statusConflict, 0, true},
		{"owner changed before upload", false, false, "before-owner", statusConflict, 0, false},
		{"source changed before upload", false, false, "before-source", statusConflict, 0, true},
		{"settings changed before upload", false, false, "before-config", statusConflict, 0, true},
		{"repair metadata missing in detail", false, false, "before-metadata", statusFailed, 0, true},
		{"same digest repair races upload", false, false, "during-repair", statusUnchanged, 0, true},
		{"adopt repair races upload", true, false, "during-repair", statusAdopted, 0, true},
		{"settings race upload", false, false, "during-config", statusConflict, 0, true},
		{"downtime refusal", false, false, "downtime", statusFailed, 0, true},
		{"adopt ambiguous committed repair", true, false, "committed-error", statusFailed, 1, true},
		{"adopt failed before promotion", true, false, "failed-error", statusFailed, 0, false},
		{"adopt promotion unreadable", true, false, "unknown-error", statusFailed, 0, true},
		{"owned ambiguous committed repair", false, false, "committed-error", statusFailed, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFleetFake(true)
			fake.nextDigest = "sha256:same"
			owner := stringPtr("fleet:eu")
			if tc.adopt {
				owner = nil
			}
			app := &fakeApp{Slug: "app", Name: "app", Access: "private", ContentDigest: "sha256:same", ManagedBy: owner, Replicas: 1, status: "failed",
				settings: map[string]any{"deployment_repair_required": true, "last_deployment_status": "failed", "desired_status": "failed", "release_number": 1}}
			fake.apps["app"] = app
			entry := fleet.AppEntry{Slug: "app", Visibility: "private", Config: fleet.Config{Name: stringPtr("app")}}
			if tc.config {
				entry.Config.Name = stringPtr("desired")
				fake.deployWrites = map[string]any{"name": "bundle-default"}
			}
			raw, _ := json.Marshal(app.view())
			var observed db.App
			_ = json.Unmarshal(raw, &observed)
			d := fleet.Diff(&fleet.Manifest{FleetID: "eu", Apps: []fleet.AppEntry{entry}}, map[string]string{"app": app.ContentDigest}, []fleet.ObservedApp{observedFromApp(observed)})[0]
			if d.RecoveryReason != fleet.RecoveryFailed {
				t.Fatalf("fixture does not plan recovery: %+v", d)
			}
			revision := "rev:before"
			reads, uploads := 0, 0
			repair := func() {
				app.settings["deployment_repair_required"] = false
				app.settings["last_deployment_status"] = "succeeded"
				app.settings["desired_status"] = "running"
				app.settings["release_number"] = 2
				app.status = "running"
				revision = "rev:after"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.transition == "rollback-error" && reads > 0 && r.Method == http.MethodPatch {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if r.Method == http.MethodGet && r.URL.Path == "/api/apps/app" {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					reads++
					if tc.transition == "unknown-error" && uploads > 0 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if reads == 1 {
						switch tc.transition {
						case "before-repair":
							repair()
						case "before-pending":
							app.settings["last_deployment_status"] = "pending"
						case "before-stopped":
							app.settings["desired_status"] = "stopped"
						case "before-owner":
							app.ManagedBy = stringPtr("fleet:other")
						case "before-source", "rollback-error":
							app.ContentDigest = "sha256:other"
						case "before-config":
							app.Name = "changed"
						case "before-metadata":
							delete(app.settings, "deployment_repair_required")
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"app": app.view(), "resource_revision": revision, "compatibility_quarantined": app.settings["deployment_repair_required"], "producer_repair_required": false})
					return
				}
				if r.Method == http.MethodPost && r.URL.Path == "/api/apps/app/deploy" {
					uploads++
					if r.Header.Get("X-Shinyhub-If-Resource-Revision") != "rev:before" || r.Header.Get("X-Shinyhub-If-Content-Digest") != "sha256:same" || r.Header.Get("X-Shinyhub-If-Managed-By") != "fleet:eu" {
						t.Errorf("corrective deploy lacks fresh fences: %v", r.Header)
					}
					switch tc.transition {
					case "during-other-release":
						fake.mu.Lock()
						app.settings["release_number"] = 2
						app.ContentDigest = "sha256:other"
						fake.mu.Unlock()
						w.WriteHeader(http.StatusConflict)
						return
					case "during-repair":
						fake.mu.Lock()
						repair()
						fake.mu.Unlock()
						w.WriteHeader(http.StatusConflict)
						_, _ = io.WriteString(w, `{"error":"precondition failed: app revision changed"}`)
						return
					case "during-config":
						fake.mu.Lock()
						app.Name = "changed"
						fake.mu.Unlock()
						w.WriteHeader(http.StatusConflict)
						return
					case "downtime":
						w.Header().Set(conflictHeader, conflictHandoffDeferred)
						w.WriteHeader(http.StatusConflict)
						_, _ = io.WriteString(w, handoffDeferralBody)
						return
					case "committed-error":
						fake.mu.Lock()
						repair()
						fake.deploys++
						fake.mu.Unlock()
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `{"error":"response failed after promotion"}`)
						return
					case "failed-error", "unknown-error":
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `{"error":"consumer failed"}`)
						return
					}
					fake.handle(w, r)
					fake.mu.Lock()
					repair()
					fake.mu.Unlock()
					return
				}
				fake.handle(w, r)
			}))
			t.Cleanup(srv.Close)
			result := convergeApp(&cliConfig{Host: srv.URL, Token: "shk_test"}, d, entry, observedFromApp(observed), sourceDir(t), convergeOpts{adopt: tc.transition != "adopt-unauthorized", preconditions: true, retries: 3, healthTimeout: time.Second}, "fleet:eu", io.Discard)
			if result.status != tc.wantStatus || fake.deploys != tc.wantDeploy || (app.ManagedBy != nil && *app.ManagedBy == "fleet:eu") != tc.wantOwner {
				t.Fatalf("result=%+v deploys=%d owner=%v", result, fake.deploys, app.ManagedBy)
			}
			if uploads > 1 {
				t.Fatalf("corrective deployment retried stale revision %d times", uploads)
			}
			if tc.config && app.Name != "desired" {
				t.Fatalf("fleet settings not reasserted after bundle: %s", app.Name)
			}
			if tc.transition == "downtime" && (resultFailureKind(result) != string(deployfail.DowntimeRequired) || result.mutation != mutationNone || !strings.Contains(applyRecoveryFor(recoveryCtx(), []applyResult{result}).Summary, "--allow-downtime")) {
				t.Fatalf("downtime refusal lost its remedy: %+v", result)
			}
			if tc.transition == "committed-error" && result.mutation != mutationPartial {
				t.Fatalf("same-digest promotion reported untouched: %+v", result)
			}
			if tc.adopt && (tc.transition == "before-source" || tc.transition == "before-metadata" || tc.transition == "during-other-release") && result.mutation != mutationNone {
				t.Fatalf("refused adoption reported a mutation after rollback: %+v", result)
			}
			if tc.transition == "rollback-error" && (result.mutation != mutationUnknown || !strings.Contains(result.err.Error(), "restore adoption reservation")) {
				t.Fatalf("failed rollback was hidden: %+v", result)
			}
		})
	}
}

func TestFleetRecoveryPlanReasonAndOwnershipOnlyPreflight(t *testing.T) {
	d := fleet.AppDiff{Slug: "app", Action: fleet.ActionUpdateSource, LocalDigest: "sha256:same", ServerDigest: "sha256:same", RecoveryReason: fleet.RecoveryFailed}
	resource := fleetAppPlanResource(d, "eu")
	if len(resource.Changes) != 0 || !strings.Contains(planResourceReason(resource), fleet.RecoveryFailed) || !strings.Contains(planResourceReason(resource), "--allow-downtime") {
		t.Fatalf("recovery rendered false source drift: %+v", resource)
	}
	var out bytes.Buffer
	m := &fleet.Manifest{FleetID: "eu", Apps: []fleet.AppEntry{{Slug: "app"}}}
	if err := writeFleetPlanJSON(&out, m, "test", []fleet.AppDiff{d}, nil, 0, ""); err != nil || !strings.Contains(out.String(), `"recovery_reason":"recover-failed"`) {
		t.Fatalf("plan JSON=%s err=%v", out.String(), err)
	}
	fake := newFleetFake(true)
	fake.deployPreflight = true
	fake.preflightReply = `{"valid":false,"problems":[{"stage":"deploy","message":"repair-blocked-no-producer"}]}`
	cfg := fake.httptest(t)
	d.Action, d.RecoveryReason = fleet.ActionAdopt, ""
	problems, err := fleetServerPreflight(cfg, serverCaps{FleetPreconditions: true, DeployPreflight: true}, m, []fleet.AppDiff{d}, map[string]fleetBundleFacts{"app": {Dir: t.TempDir(), AppType: "python"}})
	if err != nil || len(problems) != 0 || len(fake.preflights) != 0 {
		t.Fatalf("ownership-only adoption rehearsed a nonexistent deploy: problems=%v err=%v", problems, err)
	}
	if !ownershipOnlyAdopt(d, true) {
		t.Fatal("matching adoption no longer shares execution shortcut")
	}
}

func TestFleetRecoveryMissingAdvertisedStateAndOlderServer(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(map[bool]string{false: "older server", true: "missing advertised state"}[capable], func(t *testing.T) {
			fake := newFleetFake(true)
			app := &fakeApp{Slug: "app", Name: "app", Access: "private", ContentDigest: "", ManagedBy: stringPtr("fleet:eu"), status: "failed", settings: map[string]any{"last_deployment_status": "failed", "desired_status": "failed"}}
			fake.apps["app"] = app
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/server-info" {
					_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.20.1", "capabilities": map[string]bool{"fleet_preconditions": true, "content_digest": true, "deployment_repair_state": capable}})
					return
				}
				fake.handle(w, r)
			}))
			t.Cleanup(srv.Close)
			t.Setenv("SHINYHUB_HOST", srv.URL)
			t.Setenv("SHINYHUB_TOKEN", "shk_test")
			t.Setenv("SHINYHUB_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
			configPathOverride = ""
			t.Cleanup(func() { configPathOverride = "" })
			dir := t.TempDir()
			mustWrite(t, filepath.Join(dir, "app", "app.py"), "print(1)\n")
			file := writeFleetManifest(t, dir, "fleet_id='eu'\n[[app]]\nslug='app'\nsource='./app'\nvisibility='private'\n")
			preview, err := previewBundleSpec(bundleBuildSpec{Dir: filepath.Join(dir, "app")})
			if err != nil {
				t.Fatal(err)
			}
			app.ContentDigest = preview.Digest
			var stderr bytes.Buffer
			pf, err := fleetPreflight(file, &stderr, "plan", 0)
			if capable {
				if err == nil || !strings.Contains(err.Error(), "deployment_repair_required") {
					t.Fatalf("advertised missing state accepted: pf=%v err=%v stderr=%s", pf, err, stderr.String())
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer pf.cleanup()
				if pf.diff[0].Action != fleet.ActionUnchanged || len(pf.diff[0].Warnings) == 0 {
					t.Fatalf("older server silently trusted failed state: %+v", pf.diff[0])
				}
			}
		})
	}
}

func TestFleetRecoveryPreservesStoppedWithoutRuntimeStatus(t *testing.T) {
	repair := true
	app := db.App{Slug: "app", ContentDigest: "sha256:same", ManagedBy: stringPtr("fleet:eu"), Status: "stopped", LastDeploymentStatus: "failed", DeploymentRepairRequired: &repair}
	entry := fleet.AppEntry{Slug: "app"}
	d := fleet.Diff(&fleet.Manifest{FleetID: "eu", Apps: []fleet.AppEntry{entry}}, map[string]string{"app": app.ContentDigest}, []fleet.ObservedApp{observedFromApp(app)})[0]
	if d.Action != fleet.ActionUnchanged || d.RecoveryReason != "" || len(d.RecoveryWarnings) != 1 {
		t.Fatalf("stopped app planned a corrective deploy: %+v", d)
	}
	state := &fleetRecoverySnapshot{App: &app}
	if recoveryStillNeeded(state, d, entry, "fleet:eu") {
		t.Fatal("stopped app passed the recovery execution guard")
	}
}

func TestFleetRecoveryDoesNotCopyStaleRedeployWarnings(t *testing.T) {
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Name: "app", Access: "private", Replicas: 1, ContentDigest: "sha256:same"})
	f.launched, f.served, f.outcome, f.reason = 1, 1, "failed", "boom"
	obs := f.observe()
	warning := redeployWarning("app", obs.Redeploy)
	d := fleet.AppDiff{Slug: "app", Action: fleet.ActionUnchanged, LocalDigest: "sha256:same", ServerDigest: "sha256:same", Warnings: []string{warning}}
	result := convergeApp(cfg, d, fleet.AppEntry{Slug: "app"}, obs, sourceDir(t), outcomeOpts(), "fleet:eu", io.Discard)
	if len(result.warnings) != 1 || result.warnings[0] != warning {
		t.Fatalf("live redeploy warning duplicated: %+v", result)
	}
	f.outcome, f.reason = "completed", ""
	result = convergeApp(cfg, d, fleet.AppEntry{Slug: "app"}, f.observe(), sourceDir(t), outcomeOpts(), "fleet:eu", io.Discard)
	if len(result.warnings) != 0 {
		t.Fatalf("resolved plan-time warning copied into apply: %+v", result)
	}
}
