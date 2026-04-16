package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/oauth"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/ui"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
// It defaults to "dev" for local builds.
var version = "dev"

func main() {
	cfgPath := "shinyhub.yaml"
	if v := os.Getenv("SHINYHUB_CONFIG"); v != "" {
		cfgPath = v
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if err := os.MkdirAll(cfg.Storage.AppsDir, 0755); err != nil {
		log.Fatalf("create apps dir: %v", err)
	}

	store, err := db.Open(cfg.Database.DSN)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("warn: store close: %v", err)
		}
	}()
	if err := store.Migrate(); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	// Bootstrap admin user from env if provided and no users exist
	if adminUser := os.Getenv("SHINYHUB_ADMIN_USER"); adminUser != "" {
		adminPass := os.Getenv("SHINYHUB_ADMIN_PASSWORD")
		if adminPass == "" {
			log.Fatal("SHINYHUB_ADMIN_PASSWORD must not be empty when SHINYHUB_ADMIN_USER is set")
		}
		_, err := store.GetUserByUsername(adminUser)
		if errors.Is(err, db.ErrNotFound) {
			hash, err := auth.HashPassword(adminPass)
			if err != nil {
				log.Fatalf("hash admin password: %v", err)
			}
			if err := store.CreateUser(db.CreateUserParams{
				Username:     adminUser,
				PasswordHash: hash,
				Role:         "admin",
			}); err != nil {
				log.Printf("warn: could not create admin user: %v", err)
			} else {
				log.Printf("created admin user: %s", adminUser)
			}
		} else if err != nil {
			log.Fatalf("check admin user: %v", err)
		}
	}

	var rt process.Runtime
	switch cfg.Runtime.Mode {
	case "docker":
		dockerRT, err := process.NewDockerRuntime(
			cfg.Runtime.Docker.Socket,
			cfg.Runtime.Docker.Images.Python,
			cfg.Runtime.Docker.Images.R,
		)
		if err != nil {
			log.Fatalf("docker runtime: %v", err)
		}
		rt = dockerRT
		log.Printf("runtime: docker (socket=%s)", cfg.Runtime.Docker.Socket)
	default:
		rt = process.NewNativeRuntime()
		log.Printf("runtime: native")
	}
	mgr := process.NewManager(cfg.Storage.AppsDir, rt)
	prx := proxy.New()
	srv := api.New(cfg, store, mgr, prx)

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
			log.Fatalf("oidc init: %v", err)
		}
		srv.SetOIDCProvider(p)
		log.Printf("OIDC configured: %s (%s)", cfg.OAuth.OIDC.DisplayName, cfg.OAuth.OIDC.IssuerURL)
	}

	deployFn := func(slug, bundleDir string) (*deploy.Result, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, fmt.Errorf("get app for deploy: %w", err)
		}
		return deploy.Run(deploy.Params{
			Slug:            slug,
			BundleDir:       bundleDir,
			Manager:         mgr,
			Proxy:           prx,
			MemoryLimitMB:   deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, cfg.Runtime.Docker.DefaultMemoryMB),
			CPUQuotaPercent: deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, cfg.Runtime.Docker.DefaultCPUPercent),
		})
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watcher.Start(ctx)

	mux := http.NewServeMux()
	// API routes
	mux.Handle("/api/", apiTimeoutHandler(srv.Router()))
	// App proxy routes
	appHandler := access.Middleware(store, cfg.Auth.Secret)(prx)
	mux.Handle("/app/", appHandler)
	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	// Static UI assets
	mux.Handle("/static/", ui.Handler())
	// Serve index.html at root
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
		// No global ReadTimeout or WriteTimeout: deploy uploads (up to 128 MB)
		// and SSE log streams need unbounded time. Per-handler timeouts are
		// applied by apiTimeoutHandler.
	}
	log.Printf("shinyhub %s listening on %s", version, addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		cancel()
		log.Fatal(err)
	}
}

// apiTimeoutHandler wraps the API router with a 30s per-request timeout,
// exempting the long-lived SSE log-stream route and the large-file deploy
// upload route so neither is prematurely cut off.
func apiTimeoutHandler(h http.Handler) http.Handler {
	timed := http.TimeoutHandler(h, 30*time.Second, `{"error":"request timeout"}`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasSuffix(p, "/logs") || strings.HasSuffix(p, "/deploy") {
			h.ServeHTTP(w, r)
			return
		}
		timed.ServeHTTP(w, r)
	})
}
