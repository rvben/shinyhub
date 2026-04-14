package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/rvben/shinyhost/internal/api"
	"github.com/rvben/shinyhost/internal/auth"
	"github.com/rvben/shinyhost/internal/config"
	"github.com/rvben/shinyhost/internal/db"
	"github.com/rvben/shinyhost/internal/process"
	"github.com/rvben/shinyhost/internal/proxy"
)

func main() {
	cfgPath := "shinyhost.yaml"
	if v := os.Getenv("SHINYHOST_CONFIG"); v != "" {
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
	if adminUser := os.Getenv("SHINYHOST_ADMIN_USER"); adminUser != "" {
		adminPass := os.Getenv("SHINYHOST_ADMIN_PASSWORD")
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

	mgr := process.NewManager()
	prx := proxy.New()
	srv := api.New(cfg, store, mgr, prx)

	mux := http.NewServeMux()
	// API routes
	mux.Handle("/api/", srv.Router())
	// App proxy routes
	mux.Handle("/app/", prx)
	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("shinyhost listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
