package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/rvben/shinyhub/internal/appenv"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/identity"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/tracing"
	"github.com/rvben/shinyhub/internal/worker"
)

// processManagerDeps is what the process manager is built from. workerReg and
// dialer are nil unless worker hosting is enabled.
type processManagerDeps struct {
	cfg            *config.Config
	store          *db.Store
	workerReg      *worker.Registry
	dialer         worker.AgentDialer
	bundleTokenKey []byte
	secretsKey     []byte
	absAppDataDir  string
}

// newProcessManager builds the manager that starts and stops app replicas:
// one runtime per configured tier, the app-log run history, and the
// resolvers that give each replica its environment, shared mounts and
// platform defaults. The manager it returns is already marked recovery
// pending.
func newProcessManager(ctx context.Context, d processManagerDeps) (*process.Manager, error) {
	cfg, store, workerReg, dialer := d.cfg, d.store, d.workerReg, d.dialer

	// Build one runtime per configured tier. The first tier is the default
	// (config.Load synthesizes a single "local" tier from Mode when none are
	// declared, so single-node behavior is unchanged). The default tier's
	// runtime backs the Manager and every runtime-typed consumer (sampler,
	// jobs, recovery lister, sweeper); additional tiers are registered so
	// placement can route replicas to them.
	defaultTierCfg := defaultTierConfig(cfg)
	defaultTier := defaultTierCfg.Name
	var rt process.Runtime
	if defaultTierCfg.Runtime == "remote_docker" {
		if workerReg == nil || dialer == nil {
			return nil, fmt.Errorf("tier %q requires worker hosting to be enabled", defaultTier)
		}
		rt = worker.NewRemoteRuntime(workerReg, defaultTier, dialer)
	} else {
		var err error
		rt, err = buildRuntime(ctx, defaultTierCfg, cfg, d.bundleTokenKey)
		if err != nil {
			return nil, err
		}
	}
	slog.Info("runtime configured", "tier", defaultTier, "mode", defaultTierCfg.Runtime, "max_concurrent_builds", cfg.Runtime.MaxConcurrentBuilds)
	deploy.SetMaxConcurrentBuilds(cfg.Runtime.MaxConcurrentBuilds)
	mgr := process.NewManager(cfg.Storage.AppsDir, rt)
	// Processes that survived the last shutdown are still running and still
	// serving, but this Manager has not adopted them yet: RecoverProcesses does
	// that from ownerRuntime.run, after ownership is acquired, while the listener is
	// already answering. Declare the pass outstanding here - before anything can
	// read the Manager - so a reader knows an absent entry means "not looked at"
	// rather than "not running". RecoverProcesses clears it on every exit path.
	mgr.MarkRecoveryPending()
	mgr.SetDefaultTier(defaultTier)
	mgr.SetStopGrace(cfg.Server.StopGrace)
	mgr.SetLogMaxSize(int64(cfg.Storage.AppLogMaxSizeMB) << 20)
	mgr.SetLogRunRecorder(appLogRunRecorder(store))
	if store.IsPostgres() {
		mgr.SetLogRunSinkFactory(func(run process.LogRun) (io.WriteCloser, error) {
			if run.AppID == 0 {
				return nil, nil
			}
			return store.NewAppLogWriter(run.RunID, db.AppLogRetentionBytes)
		})
	}
	for _, tierCfg := range cfg.Runtime.Tiers {
		if tierCfg.Name == defaultTier {
			continue
		}
		if tierCfg.Runtime == "remote_docker" {
			if workerReg == nil || dialer == nil {
				return nil, fmt.Errorf("tier %q requires worker hosting to be enabled", tierCfg.Name)
			}
			mgr.RegisterRuntime(tierCfg.Name, worker.NewRemoteRuntime(workerReg, tierCfg.Name, dialer))
			slog.Info("remote runtime tier registered", "tier", tierCfg.Name, "mode", tierCfg.Runtime)
			continue
		}
		tierRT, err := buildRuntime(ctx, tierCfg, cfg, d.bundleTokenKey)
		if err != nil {
			return nil, err
		}
		mgr.RegisterRuntime(tierCfg.Name, tierRT)
		slog.Info("runtime tier registered", "tier", tierCfg.Name, "mode", tierCfg.Runtime)
	}
	mgr.SetEnvResolver(appEnvResolver(store, d.secretsKey))
	mgr.SetSharedMountResolver(sharedMountResolver(store, d.absAppDataDir))
	if err := mgr.SetAppDataRoot(d.absAppDataDir); err != nil {
		return nil, fmt.Errorf("set app data root: %w", err)
	}
	if err := mgr.SetAppCache(cfg.Storage.AppCacheDir, cfg.Storage.CacheMaxMB()); err != nil {
		return nil, fmt.Errorf("set app cache: %w", err)
	}

	// Platform-default OTEL_* env vars are injected into every app process so
	// Shiny's built-in OpenTelemetry exporter reaches the configured backend
	// without per-app configuration. They are absent when cfg.Tracing.Enabled
	// is false.
	mgr.SetPlatformDefaultEnvResolver(platformDefaultEnvResolver(cfg, store))
	// The Enabled conjunct is belt-and-braces on top of config validation:
	// wrapping apps that would export nowhere must be impossible.
	mgr.SetAutoInstrumentAppsDefault(cfg.Tracing.Enabled && cfg.Tracing.AutoInstrumentApps)
	mgr.SetAutoInstrumentExtraPackages(cfg.Tracing.AutoInstrumentExtraPackages)
	return mgr, nil
}

// defaultTierConfig returns the full TierConfig of the default tier, so fields
// such as LaunchType are carried through. With no tiers declared it is the
// legacy single tier backed by runtime.mode.
func defaultTierConfig(cfg *config.Config) config.TierConfig {
	defaultTier := cfg.Runtime.DefaultTierName()
	for _, t := range cfg.Runtime.Tiers {
		if t.Name == defaultTier {
			return t
		}
	}
	return config.TierConfig{Name: defaultTier, Runtime: cfg.Runtime.Mode}
}

// appLogRunRecorder mirrors each replica run into the app_log_runs history.
func appLogRunRecorder(store *db.Store) process.LogRunRecorder {
	return process.LogRunRecorder{
		Begin: func(run process.LogRun) error {
			// AppID is absent only in local/test-style starts that are not backed by
			// an app row. They still get immutable files, but cannot have DB history.
			if run.AppID == 0 {
				return nil
			}
			var deploymentID *int64
			if run.DeploymentID != 0 {
				id := run.DeploymentID
				deploymentID = &id
			}
			return store.CreateAppLogRun(db.CreateAppLogRunParams{
				RunID: run.RunID, AppID: run.AppID, ReplicaIndex: run.ReplicaIndex,
				DeploymentID: deploymentID, AppVersion: run.AppVersion,
				Tier: run.Tier, Status: string(run.Status), StartedAt: run.StartedAt,
			})
		},
		Running: func(run process.LogRun) error {
			if run.AppID == 0 {
				return nil
			}
			externalLogs := ""
			if run.ExternalLogs != nil {
				encoded, err := json.Marshal(run.ExternalLogs)
				if err != nil {
					return fmt.Errorf("encode external log handoff: %w", err)
				}
				externalLogs = string(encoded)
			}
			return store.MarkAppLogRunRunning(run.RunID, run.Provider, externalLogs)
		},
		Finish: func(run process.LogRun) error {
			if run.AppID == 0 {
				return nil
			}
			return store.FinishAppLogRunWithExit(run.RunID, string(run.Status), run.FinishedAt,
				run.OOMKilled, run.ExitCode, run.Signal, run.ExitReason)
		},
	}
}

// appEnvResolver resolves an app's configured variables into its process
// environment, with decrypted secrets returned separately.
func appEnvResolver(store *db.Store, secretsKey []byte) func(slug string) ([]string, []string, error) {
	return func(slug string) ([]string, []string, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, nil, err
		}
		vars, err := store.ListAppEnvVars(app.ID)
		if err != nil {
			return nil, nil, err
		}
		env, secretEnv, err := appenv.Resolve(vars, secretsKey)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve env for app %s: %w", slug, err)
		}
		return env, secretEnv, nil
	}
}

// sharedMountResolver lists the data directories other apps share with an
// app, as host paths under the app data root.
func sharedMountResolver(store *db.Store, absAppDataDir string) func(slug string) ([]process.SharedMount, error) {
	return func(slug string) ([]process.SharedMount, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, err
		}
		rows, err := store.ListSharedDataSources(app.ID)
		if err != nil {
			return nil, err
		}
		out := make([]process.SharedMount, 0, len(rows))
		for _, m := range rows {
			out = append(out, process.SharedMount{
				SourceSlug: m.SourceSlug,
				HostPath:   filepath.Join(absAppDataDir, m.SourceSlug),
			})
		}
		return out, nil
	}
}

// platformDefaultEnvResolver gives every replica the tracing defaults plus
// its identity: the key that verifies identity tokens minted for it and its
// own slug.
func platformDefaultEnvResolver(cfg *config.Config, store *db.Store) func(slug string, replica int) []string {
	return func(slug string, replica int) []string {
		env := tracing.EnvFor(cfg.Tracing, slug, replica)
		// Identity: every app receives its verification key and its own
		// slug, unconditionally (the key alone discloses nothing - it only
		// verifies tokens that are never minted while forwarding is
		// disabled - and unconditional injection means enabling the flag
		// later doesn't strand running apps without a key). The numeric ID
		// scopes the key so a delete-and-recreate under the same slug
		// cannot inherit its predecessor's key.
		app, err := store.GetApp(slug)
		if err != nil {
			slog.Warn("identity: resolve app for key derivation; skipping identity env", "slug", slug, "err", err)
			return env
		}
		key := identity.DeriveKey(cfg.Auth.Secret, app.ID)
		return append(env,
			"SHINYHUB_IDENTITY_KEY="+hex.EncodeToString(key),
			"SHINYHUB_APP_SLUG="+slug,
		)
	}
}
