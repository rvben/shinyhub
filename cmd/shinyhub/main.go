package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/data"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/logging"
	"github.com/rvben/shinyhub/internal/oauth"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/secrets"
	"github.com/rvben/shinyhub/internal/ui"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
// It defaults to "dev" for local builds.
var version = "dev"

func main() {
	logger := logging.New()
	slog.SetDefault(logger)

	cfgPath := "shinyhub.yaml"
	if v := os.Getenv("SHINYHUB_CONFIG"); v != "" {
		cfgPath = v
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(cfg.Storage.AppsDir, 0755); err != nil {
		slog.Error("create apps dir", "err", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(cfg.Storage.AppDataDir, 0o755); err != nil {
		slog.Error("create app-data dir", "err", err)
		os.Exit(1)
	}

	sweepOrphanTempfiles(cfg.Storage.AppDataDir)

	store, err := db.Open(cfg.Database.DSN)
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := store.Close(); err != nil {
			slog.Warn("store close", "err", err)
		}
	}()
	if err := store.Migrate(); err != nil {
		slog.Error("db migrate", "err", err)
		os.Exit(1)
	}

	secretsKey := secrets.DeriveKey(cfg.Auth.Secret)

	// readyCh is closed once HTTP listener is live. /readyz returns 503 until then.
	readyCh := make(chan struct{})

	// Bootstrap admin user from env if provided and no users exist
	if adminUser := os.Getenv("SHINYHUB_ADMIN_USER"); adminUser != "" {
		adminPass := os.Getenv("SHINYHUB_ADMIN_PASSWORD")
		if adminPass == "" {
			slog.Error("SHINYHUB_ADMIN_PASSWORD must not be empty when SHINYHUB_ADMIN_USER is set")
			os.Exit(1)
		}
		_, err := store.GetUserByUsername(adminUser)
		if errors.Is(err, db.ErrNotFound) {
			hash, err := auth.HashPassword(adminPass)
			if err != nil {
				slog.Error("hash admin password", "err", err)
				os.Exit(1)
			}
			if err := store.CreateUser(db.CreateUserParams{
				Username:     adminUser,
				PasswordHash: hash,
				Role:         "admin",
			}); err != nil {
				slog.Warn("could not create admin user", "err", err)
			} else {
				slog.Info("admin user created", "username", adminUser)
			}
		} else if err != nil {
			slog.Error("check admin user", "err", err)
			os.Exit(1)
		}
	}

	var rt process.Runtime
	switch cfg.Runtime.Mode {
	case "docker":
		dockerRT, err := process.NewDockerRuntime(
			cfg.Runtime.Docker.Socket,
			cfg.Runtime.Docker.Images.Python,
			cfg.Runtime.Docker.Images.R,
			cfg.Runtime.Docker.NetworkMode,
		)
		if err != nil {
			slog.Error("docker runtime", "err", err)
			os.Exit(1)
		}
		rt = dockerRT
		slog.Info("runtime configured", "mode", "docker", "socket", cfg.Runtime.Docker.Socket, "network_mode", cfg.Runtime.Docker.NetworkMode)
	case "native":
		rt = process.NewNativeRuntime()
		slog.Info("runtime configured", "mode", "native")
	default:
		// Unreachable: config.Load validates Runtime.Mode before we get here.
		slog.Error("unsupported runtime mode", "mode", cfg.Runtime.Mode)
		os.Exit(1)
	}
	mgr := process.NewManager(cfg.Storage.AppsDir, rt)
	mgr.SetEnvResolver(func(slug string) ([]string, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, err
		}
		vars, err := store.ListAppEnvVars(app.ID)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(vars))
		for _, v := range vars {
			val := string(v.Value)
			if v.IsSecret {
				pt, err := secrets.Decrypt(secretsKey, v.Value)
				if err != nil {
					return nil, fmt.Errorf("decrypt env %s for app %s: %w", v.Key, slug, err)
				}
				val = string(pt)
			}
			out = append(out, fmt.Sprintf("%s=%s", v.Key, val))
		}
		return out, nil
	})
	mgr.SetSharedMountResolver(func(slug string) ([]process.SharedMount, error) {
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
				HostPath:   filepath.Join(cfg.Storage.AppDataDir, m.SourceSlug),
			})
		}
		return out, nil
	})
	mgr.SetAppDataRoot(cfg.Storage.AppDataDir)
	prx := proxy.New()
	srv := api.New(cfg, store, mgr, prx)
	srv.SetSecretsKey(secretsKey)

	// Emit a structured access log for every proxied app request. Using the
	// Server's trusted-proxy-aware ClientIP keeps the "client" field honest
	// when shinyhub itself sits behind an edge proxy; this is independent of
	// anything the backend app (uvicorn/httpuv) chooses to print in its own
	// log and gives operators a reliable per-slug audit trail.
	prx.SetClientIPResolver(srv.ClientIP)
	prx.SetAccessLogger(func(e proxy.AccessLogEntry) {
		attrs := []any{
			"slug", e.Slug,
			"method", e.Method,
			"path", e.Path,
			"status", e.Status,
			"bytes", e.Bytes,
			"duration_ms", e.Duration.Milliseconds(),
			"client_ip", e.ClientIP,
			"peer", e.Peer,
		}
		if e.ReplicaIndex >= 0 {
			attrs = append(attrs, "replica", e.ReplicaIndex, "sticky", e.Sticky)
		}
		slog.Info("proxy_access", attrs...)
	})

	if cfg.Runtime.Mode == "docker" {
		srv.SetSampler(&process.RuntimeSampler{Runtime: rt})
	}

	if cfg.OAuth.OIDC.IssuerURL != "" {
		oidcCtx, oidcCancel := context.WithTimeout(context.Background(), 15*time.Second)
		p, err := oauth.NewOIDCProvider(oidcCtx,
			cfg.OAuth.OIDC.IssuerURL,
			cfg.OAuth.OIDC.ClientID,
			cfg.OAuth.OIDC.ClientSecret,
			cfg.OAuth.OIDC.CallbackURL,
			cfg.OAuth.OIDC.DisplayName,
		)
		oidcCancel()
		if err != nil {
			slog.Error("oidc init", "err", err)
			os.Exit(1)
		}
		srv.SetOIDCProvider(p)
		slog.Info("oidc configured", "display_name", cfg.OAuth.OIDC.DisplayName, "issuer", cfg.OAuth.OIDC.IssuerURL)
	}

	deployFn := func(slug, bundleDir string, index int) (*deploy.Result, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, fmt.Errorf("get app for deploy: %w", err)
		}
		return deploy.RunReplica(deploy.Params{
			Slug:            slug,
			BundleDir:       bundleDir,
			Manager:         mgr,
			Proxy:           prx,
			MemoryLimitMB:   deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, cfg.Runtime.Docker.DefaultMemoryMB),
			CPUQuotaPercent: deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, cfg.Runtime.Docker.DefaultCPUPercent),
		}, index)
	}

	lcCfg := lifecycle.Config{
		WatchInterval:      cfg.Lifecycle.WatchInterval,
		RestartMaxAttempts: cfg.Lifecycle.RestartMaxAttempts,
		HibernateTimeout:   cfg.Lifecycle.HibernateTimeout,
	}
	watcher := lifecycle.New(lcCfg, mgr, prx, store, deployFn)

	// Re-adopt any processes that survived a server restart.
	var lister lifecycle.ContainerLister
	if dockerRT, ok := rt.(lifecycle.ContainerLister); ok {
		lister = dockerRT
	}
	lifecycle.RecoverProcesses(store, mgr, prx, lister)

	rootCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	watcherCtx, cancelWatcher := context.WithCancel(context.Background())
	defer cancelWatcher()
	watcherDone := make(chan struct{})
	go func() {
		watcher.Start(watcherCtx)
		close(watcherDone)
	}()

	jobsMgr := jobs.NewManager(rt, store, secretsKey, cfg.Storage.AppsDir, cfg.Storage.AppDataDir)
	sched := scheduler.New(jobsMgr, store)

	schedCtx, cancelSched := context.WithCancel(context.Background())
	defer cancelSched()
	if err := sched.Start(schedCtx); err != nil {
		slog.Error("start scheduler", "err", err)
		os.Exit(1)
	}
	slog.Info("scheduler started")

	srv.SetJobs(jobsMgr, sched)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiTimeoutHandler(srv.Router()))
	emptyState := access.NeverDeployedMiddleware(store, cfg.Auth.Secret, store.IsTokenRevoked)(prx)
	appHandler := access.Middleware(store, cfg.Auth.Secret, store.IsTokenRevoked)(emptyState)
	mux.Handle("/app/", appHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-readyCh:
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ready":false,"reason":"starting"}`))
			return
		}
		pingCtx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
		defer cancel()
		if err := store.PingContext(pingCtx); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ready":false,"reason":"db"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ready":true}`))
	})
	mux.Handle("/static/", ui.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, ui.Static(), "index.html")
	})

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("listen", "addr", addr, "err", err)
		os.Exit(1)
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "version", version, "addr", addr)
		close(readyCh)
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			cancelSched()
			cancelWatcher()
			slog.Error("http server", "err", err)
			os.Exit(1)
		}
	case <-rootCtx.Done():
		slog.Info("shutdown signal received, draining")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown", "err", err)
	}
	cancelSched()
	sched.Stop()
	cancelWatcher()
	<-watcherDone
	slog.Info("shutdown complete")
}

// sweepOrphanTempfiles removes stale entries from each app's
// .shinyhub-upload-tmp/ directory left behind by interrupted PUT uploads.
// Failures are logged and otherwise ignored — startup must succeed even when
// a single app's data dir is unreadable.
func sweepOrphanTempfiles(appDataRoot string) {
	entries, err := os.ReadDir(appDataRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("sweep app-data dir", "err", err)
		}
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		appDir := filepath.Join(appDataRoot, e.Name())
		if err := data.CleanupUploadTemp(appDir, time.Hour); err != nil {
			slog.Warn("sweep upload temp", "slug", e.Name(), "err", err)
		}
	}
}

// apiTimeoutHandler wraps the API router with a 30s per-request timeout,
// exempting routes that are either long-lived by design or stream a
// large request body. See isLongLivedAPIRoute for the matrix.
func apiTimeoutHandler(h http.Handler) http.Handler {
	timed := http.TimeoutHandler(h, 30*time.Second, `{"error":"request timeout"}`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLongLivedAPIRoute(r.Method, r.URL.Path) {
			h.ServeHTTP(w, r)
			return
		}
		timed.ServeHTTP(w, r)
	})
}

// isLongLivedAPIRoute reports whether method+path identifies an API
// route that must bypass the per-request timeout. Three cases qualify:
//
//   - GET .../logs — server-sent log stream that stays open by design.
//   - POST .../deploy — bundle upload, body can be hundreds of MB.
//   - PUT /api/apps/{slug}/data/<rel> — per-app data upload, also
//     arbitrary-size. Without this exemption http.TimeoutHandler swaps
//     the response writer mid-stream at 30s; the handler keeps writing
//     to a now-disconnected recorder, the file may still complete on
//     disk, and the client sees an ambiguous "request timeout" body
//     instead of either a clean success or a clean failure.
//
// All other API routes keep the 30s timeout so a slow handler cannot
// pin a server goroutine indefinitely.
func isLongLivedAPIRoute(method, path string) bool {
	if strings.HasSuffix(path, "/logs") || strings.HasSuffix(path, "/deploy") {
		return true
	}
	if method == http.MethodPut && isAppDataUploadPath(path) {
		return true
	}
	return false
}

// isAppDataUploadPath returns true for paths of the form
// "/api/apps/<slug>/data/<rel>" where <slug> and <rel> are both
// non-empty. The leading slug must contain at least one character so a
// bare "/api/apps/data/foo" (slug == "data") cannot impersonate the
// data-upload route.
func isAppDataUploadPath(path string) bool {
	const prefix = "/api/apps/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := path[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return false
	}
	afterSlug := rest[slash+1:]
	return strings.HasPrefix(afterSlug, "data/") && len(afterSlug) > len("data/")
}
