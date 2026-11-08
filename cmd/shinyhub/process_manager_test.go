package main

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/identity"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/secrets"
)

const processManagerTestSecret = "process-manager-test-secret-process-manager"

func processManagerTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Auth:    config.AuthConfig{Secret: processManagerTestSecret},
		Runtime: config.RuntimeConfig{Mode: "native"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir(), AppCacheDir: t.TempDir()},
	}
}

func newProcessManagerForTest(t *testing.T, cfg *config.Config, store *db.Store) (*process.Manager, error) {
	t.Helper()
	return newProcessManager(context.Background(), processManagerDeps{
		cfg:            cfg,
		store:          store,
		bundleTokenKey: deriveBundleTokenKey(cfg.Auth.Secret),
		secretsKey:     secrets.DeriveKey(cfg.Auth.Secret),
		absAppDataDir:  cfg.Storage.AppDataDir,
	})
}

// createTestApp inserts an owner and one app and returns the app's ID.
func createTestApp(t *testing.T, store *db.Store, slug string) int64 {
	t.Helper()
	if _, err := store.GetUserByUsername("owner"); errors.Is(err, db.ErrNotFound) {
		if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, ProjectSlug: "default", OwnerID: owner.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	return app.ID
}

// Processes that outlived the previous server are adopted later, by the
// owner. Until then an absent manager entry means "not looked at yet", so the
// manager must carry that flag from the moment anything can read it: before
// the constructor returns.
func TestNewProcessManagerDeclaresRecoveryPending(t *testing.T) {
	cfg := processManagerTestConfig(t)
	mgr, err := newProcessManagerForTest(t, cfg, dbtest.New(t))
	if err != nil {
		t.Fatalf("newProcessManager: %v", err)
	}
	if !mgr.RecoveryPending() {
		t.Fatal("the manager is returned without recovery pending, so a reader reports surviving replicas as stopped until recovery runs")
	}
	if mgr.RuntimeForTier(cfg.Runtime.DefaultTierName()) == nil {
		t.Fatal("no runtime registered for the default tier")
	}
}

func TestNewProcessManagerRequiresWorkerHostingForRemoteTiers(t *testing.T) {
	for name, tiers := range map[string][]config.TierConfig{
		"default tier":    {{Name: "remote", Runtime: "remote_docker"}},
		"additional tier": {{Name: "local", Runtime: "native"}, {Name: "remote", Runtime: "remote_docker"}},
	} {
		cfg := processManagerTestConfig(t)
		cfg.Runtime.Tiers = tiers
		_, err := newProcessManagerForTest(t, cfg, dbtest.New(t))
		if err == nil || !strings.Contains(err.Error(), `tier "remote" requires worker hosting`) {
			t.Errorf("%s: err = %v, want the remote tier to require worker hosting", name, err)
		}
	}
}

// The log-run recorder mirrors each replica run into app_log_runs. Starts
// that are not backed by an app row have nothing to attach history to and are
// skipped.
func TestAppLogRunRecorderRecordsTheRunLifecycle(t *testing.T) {
	store := dbtest.New(t)
	appID := createTestApp(t, store, "alpha")
	rec := appLogRunRecorder(store)
	started := time.Now().UTC().Truncate(time.Second)
	run := process.LogRun{RunID: "run-1", AppID: appID, ReplicaIndex: 0, AppVersion: "v1", Tier: "local", Status: process.StatusStarting, StartedAt: started}

	if err := rec.Begin(run); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	got, err := store.GetAppLogRun(appID, "run-1")
	if err != nil {
		t.Fatalf("run row after Begin: %v", err)
	}
	if got.Status != string(process.StatusStarting) || got.DeploymentID != nil {
		t.Fatalf("after Begin: status %q, deployment %v; want starting with no deployment", got.Status, got.DeploymentID)
	}

	run.Provider = "fargate"
	run.ExternalLogs = &process.ExternalLogs{Provider: "fargate", Resource: "task/abc", LogGroup: "/shinyhub/alpha"}
	if err := rec.Running(run); err != nil {
		t.Fatalf("Running: %v", err)
	}
	got, err = store.GetAppLogRun(appID, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "fargate" || !strings.Contains(got.ExternalLogs, `"log_group":"/shinyhub/alpha"`) {
		t.Fatalf("after Running: provider %q, external logs %q; want the provider and the encoded handoff", got.Provider, got.ExternalLogs)
	}

	exit := 3
	run.Status, run.FinishedAt, run.ExitCode, run.ExitReason = process.StatusCrashed, started.Add(time.Minute), &exit, "boom"
	if err := rec.Finish(run); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	got, err = store.GetAppLogRun(appID, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != string(process.StatusCrashed) || got.ExitCode == nil || *got.ExitCode != 3 || got.ExitReason != "boom" || got.FinishedAt == nil {
		t.Fatalf("after Finish: %+v; want crashed with exit 3, reason boom and a finish time", got)
	}
}

func TestAppLogRunRecorderSkipsRunsWithoutAnApp(t *testing.T) {
	store := dbtest.New(t)
	appID := createTestApp(t, store, "alpha")
	rec := appLogRunRecorder(store)
	run := process.LogRun{RunID: "orphan", Status: process.StatusStarting, StartedAt: time.Now()}
	for name, fn := range map[string]func(process.LogRun) error{"Begin": rec.Begin, "Running": rec.Running, "Finish": rec.Finish} {
		if err := fn(run); err != nil {
			t.Errorf("%s for a run with no app: %v", name, err)
		}
	}
	runs, err := store.ListAppLogRuns(appID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("a run with no app wrote %d rows", len(runs))
	}
}

// Plain variables reach the process environment as written; secrets are
// decrypted and kept apart so they never appear in a logged environment.
func TestAppEnvResolverSeparatesSecrets(t *testing.T) {
	store := dbtest.New(t)
	appID := createTestApp(t, store, "alpha")
	key := secrets.DeriveKey(processManagerTestSecret)
	if err := store.UpsertAppEnvVar(appID, "PLAIN", []byte("value"), false); err != nil {
		t.Fatal(err)
	}
	sealed, err := secrets.Encrypt(key, []byte("s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAppEnvVar(appID, "TOKEN", sealed, true); err != nil {
		t.Fatal(err)
	}

	env, secretEnv, err := appEnvResolver(store, key)("alpha")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !slices.Contains(env, "PLAIN=value") || slices.ContainsFunc(env, func(s string) bool { return strings.HasPrefix(s, "TOKEN=") }) {
		t.Fatalf("env = %q; want PLAIN=value and no TOKEN", env)
	}
	if !slices.Contains(secretEnv, "TOKEN=s3cret") {
		t.Fatalf("secret env = %q; want the decrypted TOKEN", secretEnv)
	}

	if _, _, err := appEnvResolver(store, key)("missing"); err == nil {
		t.Fatal("resolving an unknown app succeeded; want an error rather than an empty environment")
	}
}

// Every app process is told its own slug and the key that verifies identity
// tokens minted for it. The key is scoped by the app's numeric ID, so an app
// recreated under the same slug does not inherit its predecessor's key.
func TestPlatformDefaultEnvResolverInjectsIdentity(t *testing.T) {
	store := dbtest.New(t)
	appID := createTestApp(t, store, "alpha")
	cfg := processManagerTestConfig(t)
	resolve := platformDefaultEnvResolver(cfg, store)

	env := resolve("alpha", 0)
	wantKey := "SHINYHUB_IDENTITY_KEY=" + hex.EncodeToString(identity.DeriveKey(processManagerTestSecret, appID))
	if !slices.Contains(env, wantKey) || !slices.Contains(env, "SHINYHUB_APP_SLUG=alpha") {
		t.Fatalf("env = %q; want the app's identity key and slug", env)
	}

	for _, kv := range resolve("missing", 0) {
		if strings.HasPrefix(kv, "SHINYHUB_IDENTITY_KEY=") || strings.HasPrefix(kv, "SHINYHUB_APP_SLUG=") {
			t.Fatalf("an unknown app received identity variables: %q", kv)
		}
	}
}
