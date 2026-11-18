package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/deploy"
	policy "github.com/rvben/shinyhub/internal/entitlementpolicy"
	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestFleetEntitlementsProjectionAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "shinyhub.toml")
	mustWrite(t, file, "[app]\nidentity_headers = false\n\n[entitlements.power]\ngroups = ['Finance']\n")
	projected, desired, err := resolveFleetEntitlements(bundleBuildSpec{Dir: dir}, nil)
	if err != nil || len(desired["power"].Groups) != 1 {
		t.Fatalf("policy: %+v %v", desired, err)
	}
	const expected = "[app]\n  identity_headers = false\n"
	if string(projected.Manifest) != expected {
		t.Fatalf("projected manifest = %q; want %q", projected.Manifest, expected)
	}
	before, err := previewBundleSpec(projected)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, file, "[app]\nidentity_headers = false\n\n[entitlements]\n")
	projected, desired, err = resolveFleetEntitlements(bundleBuildSpec{Dir: dir}, nil)
	if err != nil || desired == nil || len(desired) != 0 {
		t.Fatalf("clear policy: %+v %v", desired, err)
	}
	after, err := previewBundleSpec(projected)
	if err != nil || after.Digest != before.Digest {
		t.Fatalf("grant-only edit changed bundle digest: %v", err)
	}

	manifest, problems := fleet.ParseManifest([]byte(`
fleet_id = "test"
[environments.sbx]
host = "https://sbx.example.org"
[[app]]
slug = "app"
source = "."
[app.environments.sbx.entitlements]
`), "fleet.toml")
	if len(problems) > 0 {
		t.Fatalf("environment: %v", problems)
	}
	override := manifest.Apps[0].Environments["sbx"].Entitlements
	if override == nil || len(override) != 0 {
		t.Fatalf("explicit empty environment override lost: %#v", override)
	}
	mustWrite(t, file, "[entitlements.power]\ngroups = ['Finance']\n")
	projected, desired, err = resolveFleetEntitlements(bundleBuildSpec{Dir: dir}, override)
	if err != nil || desired == nil || len(desired) != 0 || string(projected.Manifest) != "sparse = true\n" {
		t.Fatalf("empty override policy = %#v, runtime = %q, err = %v", desired, projected.Manifest, err)
	}
	if _, err := deploymentBundleSpec(dir, nil); err == nil || !strings.Contains(err.Error(), "require fleet apply") {
		t.Fatalf("direct deploy accepted a fleet policy: %v", err)
	}
	mustWrite(t, filepath.Join(dir, ".shinyhubignore"), "shinyhub.toml\n")
	_, desired, err = resolveFleetEntitlements(bundleBuildSpec{Dir: dir}, nil)
	if err != nil || desired != nil {
		t.Fatalf("ignored source policy loaded: %#v %v", desired, err)
	}
}

func TestFleetEntitlementsApplyAdoptsAndRevokesWithoutRedeploy(t *testing.T) {
	store := dbtest.New(t)
	appsDir := t.TempDir()
	runtime := &recoveryRuntime{nextPID: 900000, stops: map[int]chan struct{}{}}
	mgr := process.NewManager(appsDir, runtime)
	t.Cleanup(func() { _ = mgr.StopAll() })
	cfg := &config.Config{Auth: config.AuthConfig{Secret: "test-secret"}, Storage: config.StorageConfig{AppsDir: appsDir, AppDataDir: t.TempDir(), VersionRetention: 5}}
	srv := api.New(cfg, store, mgr, proxy.New())
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
		return deploy.Run(p)
	})
	server := httptest.NewServer(srv.Router())
	t.Cleanup(server.Close)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "unused", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.IssueJWT(owner.ID, owner.Username, owner.Role, cfg.Auth.Secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHINYHUB_HOST", server.URL)
	t.Setenv("SHINYHUB_TOKEN", token)
	t.Setenv("SHINYHUB_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	configPathOverride = ""
	t.Cleanup(func() { configPathOverride = "" })

	dir := t.TempDir()
	appDir := filepath.Join(dir, "app")
	policyFile := filepath.Join(appDir, "shinyhub.toml")
	mustWrite(t, filepath.Join(appDir, "app.py"), "print('finance')\n")
	mustWrite(t, policyFile, "[entitlements.bootstrap]\ngroups = ['Bootstrap']\n")
	manifest := writeFleetManifest(t, dir, "fleet_id='finance'\n[[app]]\nslug='app'\nsource='./app'\nvisibility='private'\n")
	run := func(action string, opts ...string) (string, error) {
		t.Helper()
		args := append([]string{"fleet", action, "-f", manifest, "--json"}, opts...)
		out, stderr, err := execCLISplit(t, args...)
		if err != nil {
			t.Logf("%s: %v\nstdout=%s\nstderr=%s", action, err, out, stderr)
		}
		return out, err
	}
	if _, err := run("apply", "--yes"); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("app")
	if err != nil {
		t.Fatal(err)
	}
	if grants, err := store.ListAppEntitlementGrants(app.ID, 0); err != nil || len(grants) != 1 || grants[0].Group != "Bootstrap" {
		t.Fatalf("create did not apply initial policy: %+v %v", grants, err)
	}
	mustWrite(t, policyFile, "[entitlements]\n")
	if _, err := run("apply", "--yes"); err != nil {
		t.Fatal(err)
	}
	audit := db.AuditEventParams{UserID: &owner.ID}
	if err := store.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: "power"}, audit); err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"Finance", "Exception"} {
		if err := store.SetAppEntitlementGrant(app.ID, "power", db.EntitlementPrincipal{Group: group}, true, audit); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite(t, policyFile, "[entitlements.power]\nadopt = true\ngroups = ['Finance']\nadopt_groups = ['Finance']\n")
	out, err := run("plan", "--detailed-exitcode")
	var code *ExitCodeError
	if !errors.As(err, &code) || code.Code != 2 {
		t.Fatalf("wanted pending changes, got %v", err)
	}
	var plan jsonEnvelope
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Apps) != 1 || plan.Apps[0].Action != "update(entitlements)" ||
		plan.Apps[0].Entitlements == nil || len(plan.Apps[0].Entitlements.Changes) != 2 ||
		plan.Apps[0].Digest.Local != plan.Apps[0].Digest.Server {
		t.Fatalf("unexpected plan: %s", out)
	}
	text, _, err := execCLISplit(t, "fleet", "plan", "-f", manifest)
	if err != nil || !strings.Contains(text, "entitlement power for group Finance") {
		t.Fatalf("plan did not show entitlement adoption: %s %v", text, err)
	}
	if dryRun, err := run("apply", "--dry-run"); err != nil || !strings.Contains(dryRun, `"action":"adopt"`) {
		t.Fatalf("dry-run did not preview adoption: %s %v", dryRun, err)
	}
	grants, err := store.ListAppEntitlementGrants(app.ID, 0)
	if err != nil || len(grants) != 2 || grants[1].ManagedBy != policy.Manual {
		t.Fatalf("dry-run changed grants: %+v %v", grants, err)
	}
	if _, err := run("apply", "--yes"); err != nil {
		t.Fatal(err)
	}
	grants, err = store.ListAppEntitlementGrants(app.ID, 0)
	if err != nil || len(grants) != 2 || grants[1].ManagedBy != "fleet:finance" {
		t.Fatalf("adoption not applied: %+v %v", grants, err)
	}
	mustWrite(t, policyFile, "")
	if omitted, err := run("plan"); err != nil || !strings.Contains(omitted, "existing managed entitlements are retained") {
		t.Fatalf("omission warning: %s %v", omitted, err)
	}
	mustWrite(t, policyFile, "[entitlements]\n")
	if out, err := run("apply", "--yes"); err != nil || !strings.Contains(out, `"action":"revoke"`) {
		t.Fatalf("revocation apply: %v %s", err, out)
	}
	after, err := store.GetAppBySlug("app")
	if err != nil || after.ReleaseNumber != app.ReleaseNumber || after.ContentDigest != app.ContentDigest {
		t.Fatalf("policy changed release: before=%+v after=%+v err=%v", app, after, err)
	}
	grants, err = store.ListAppEntitlementGrants(app.ID, 0)
	if err != nil || len(grants) != 1 || grants[0].Group != "Exception" || grants[0].ManagedBy != policy.Manual {
		t.Fatalf("manual exception not retained: %+v %v", grants, err)
	}
	if _, err := run("plan", "--detailed-exitcode"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppFleetOwner(app.ID, "fleet:prior"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, policyFile, "[entitlements.power]\ngroups = ['Operations']\n")
	if out, err := run("plan"); err != nil || !strings.Contains(out, "entitlement policy preview requires --adopt") {
		t.Fatalf("plan for unowned app: %v %s", err, out)
	}
	if _, err := run("apply", "--yes", "--adopt"); err != nil {
		t.Fatal(err)
	}
	adopted, err := store.GetAppBySlug("app")
	if err != nil || adopted.ManagedBy == nil || *adopted.ManagedBy != "fleet:finance" || adopted.ReleaseNumber != app.ReleaseNumber {
		t.Fatalf("ownership-only adoption: %+v %v", adopted, err)
	}
	grants, err = store.ListAppEntitlementGrants(app.ID, 0)
	if err != nil || len(grants) != 2 || grants[1].Group != "Operations" || grants[1].ManagedBy != "fleet:finance" {
		t.Fatalf("ownership-only adoption did not apply policy: %+v %v", grants, err)
	}
	mustWrite(t, manifest, "fleet_id='replacement'\n[[app]]\nslug='app'\nsource='./app'\nvisibility='private'\n")
	if _, err := run("apply", "--yes", "--adopt"); exitCode(err) != 1 || !strings.Contains(err.Error(), "fleet:finance") {
		t.Fatalf("takeover of a fleet with live policy: %v", err)
	}
	afterBlockedAdoption, err := store.GetAppBySlug("app")
	if err != nil || afterBlockedAdoption.ManagedBy == nil || *afterBlockedAdoption.ManagedBy != "fleet:finance" {
		t.Fatalf("blocked takeover changed ownership: %+v %v", afterBlockedAdoption, err)
	}
}

func TestFleetEntitlementsRejectOldServerBeforeMutation(t *testing.T) {
	_, requests := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			json.NewEncoder(w).Encode(map[string]any{"capabilities": map[string]bool{"fleet_preconditions": true}})
		case "/api/apps":
			json.NewEncoder(w).Encode([]db.App{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	file := writeFleetTree(t, "fleet_id='test'\n[[app]]\nslug='demo'\nsource='./demo'\n", map[string]string{"demo": "[entitlements]\n"})
	_, stderr, err := execCLISplit(t, "fleet", "apply", "-f", file, "--yes")
	if exitCode(err) != 1 || !strings.Contains(stderr, "does not support fleet entitlement policies") {
		t.Fatalf("old server: %v %s", err, stderr)
	}
	for _, req := range *requests {
		if req.Method != http.MethodGet {
			t.Fatalf("old server mutated: %s %s", req.Method, req.Path)
		}
	}
}

func TestFleetEntitlementsReportsLostApplyResponse(t *testing.T) {
	_, requests := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/apps/demo/entitlement-policy" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	result := convergeAppFromSpec(cfg, fleet.AppDiff{Slug: "demo", Action: fleet.ActionUnchanged,
		Entitlements: &policy.Preview{Token: "preview", AppID: 1}}, fleet.AppEntry{},
		fleet.ObservedApp{}, bundleBuildSpec{Entitlements: policy.Policy{}}, convergeOpts{}, "fleet:test", io.Discard)
	if result.failureKind != "entitlement_policy_outcome_unknown" || result.mutation != mutationUnknown || len(*requests) != 1 {
		t.Fatalf("lost response: %v %v %d", result.failureKind, result.mutation, len(*requests))
	}
}

func TestFleetEntitlementsCreateConflictDoesNotRecordStateForReplacement(t *testing.T) {
	_, requests := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/apps" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		w.WriteHeader(http.StatusConflict)
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	result := convergeAppFromSpec(cfg, fleet.AppDiff{Slug: "demo", Action: fleet.ActionCreate,
		Entitlements: &policy.Preview{}}, fleet.AppEntry{}, fleet.ObservedApp{},
		bundleBuildSpec{Entitlements: policy.Policy{}}, convergeOpts{fleetState: true}, "fleet:test", io.Discard)
	if result.status != statusConflict || result.mutation != mutationNone || len(*requests) != 1 {
		t.Fatalf("create conflict: %v %v %d", result.status, result.mutation, len(*requests))
	}
}
