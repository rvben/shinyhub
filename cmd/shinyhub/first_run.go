package main

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/cli"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

const (
	firstRunExampleSlug = "shinyhub-tour"
	firstRunExampleName = "ShinyHub Tour"
)

//go:embed example_app/*
var firstRunExampleFiles embed.FS

type serveOptions struct {
	FirstRun  *setupResult
	NoBrowser bool
	ErrOut    io.Writer
}

// startFirstRunExperience is intentionally narrower than "database is empty".
// It only runs after this process created a fresh local config and administrator,
// and only for a loopback SQLite server. Restarts, explicit configs, environment-
// driven deployments, Postgres, and remotely reachable servers are untouched.
func startFirstRunExperience(
	ctx context.Context,
	logger *slog.Logger,
	handler http.Handler,
	store *db.Store,
	cfg *config.Config,
	ownerReady func() bool,
	opts serveOptions,
) {
	if !eligibleForFirstRunExample(opts.FirstRun, cfg) {
		return
	}
	if opts.ErrOut == nil {
		opts.ErrOut = io.Discard
	}

	go func() {
		if opts.NoBrowser {
			fmt.Fprintf(opts.ErrOut, "Open ShinyHub: %s\n", opts.FirstRun.URL)
		} else if err := cli.OpenBrowserURL(opts.FirstRun.URL); err != nil {
			logger.Warn("first-run browser unavailable", "err", err)
			fmt.Fprintf(opts.ErrOut, "Browser could not be opened automatically: %v\nOpen ShinyHub: %s\n", err, opts.FirstRun.URL)
		}

		fmt.Fprintln(opts.ErrOut, "Installing the ShinyHub Tour through the real deployment pipeline…")
		if err := waitForOwner(ctx, ownerReady, 45*time.Second); err != nil {
			logger.Warn("first-run example skipped", "err", err)
			fmt.Fprintf(opts.ErrOut, "Example app was not installed: %v\n", err)
			return
		}
		installed, err := installFirstRunExample(ctx, handler, store, opts.FirstRun)
		if err != nil {
			logger.Warn("first-run example install failed", "err", err)
			fmt.Fprintf(opts.ErrOut, "Example app could not be installed: %v\nThe server is ready; you can deploy an app from the dashboard.\n", err)
			return
		}
		if !installed {
			logger.Info("first-run example skipped because an app already exists")
			return
		}
		appURL := strings.TrimRight(opts.FirstRun.URL, "/") + "/app/" + firstRunExampleSlug + "/"
		fmt.Fprintf(opts.ErrOut, "✓ %s is live: %s\n", firstRunExampleName, appURL)
	}()
}

func eligibleForFirstRunExample(result *setupResult, cfg *config.Config) bool {
	if result == nil || cfg == nil || !result.CreatedConfig || !result.CreatedAdmin || result.Username == "" {
		return false
	}
	if _, ok := setupSQLiteFilePath(result.DatabaseDSN); !ok {
		return false
	}
	host := strings.TrimSpace(cfg.Server.Host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func waitForOwner(ctx context.Context, ready func() bool, timeout time.Duration) error {
	if ready == nil {
		return fmt.Errorf("control plane is unavailable")
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return nil
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("timed out waiting for the local control plane")
		case <-ticker.C:
		}
	}
}

// installFirstRunExample uses the same authenticated create and deploy routes
// as the dashboard and CLI. The only shortcut is an in-process HTTP round trip;
// extraction, manifest validation, dependency build, runtime launch, database
// records, and audit events all follow the production path.
func installFirstRunExample(ctx context.Context, handler http.Handler, store *db.Store, result *setupResult) (bool, error) {
	apps, err := store.ListApps(0, 0)
	if err != nil {
		return false, fmt.Errorf("inspect existing apps: %w", err)
	}
	if len(apps) != 0 {
		return false, nil
	}

	user, err := store.GetUserByUsername(result.Username)
	if err != nil {
		return false, fmt.Errorf("find first administrator: %w", err)
	}
	createBody, err := json.Marshal(map[string]string{
		"slug":   firstRunExampleSlug,
		"name":   firstRunExampleName,
		"access": "private",
	})
	if err != nil {
		return false, fmt.Errorf("encode example app: %w", err)
	}
	createReq := httptest.NewRequest(http.MethodPost, "/api/apps", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	if err := serveFirstRunRequest(ctx, handler, user, createReq, http.StatusCreated); err != nil {
		return false, fmt.Errorf("create example app: %w", err)
	}

	bundle, err := buildFirstRunExampleBundle()
	if err != nil {
		return false, err
	}
	var upload bytes.Buffer
	mw := multipart.NewWriter(&upload)
	part, err := mw.CreateFormFile("bundle", firstRunExampleSlug+".zip")
	if err != nil {
		return false, fmt.Errorf("prepare example upload: %w", err)
	}
	if _, err := part.Write(bundle); err != nil {
		return false, fmt.Errorf("write example upload: %w", err)
	}
	if err := mw.Close(); err != nil {
		return false, fmt.Errorf("finish example upload: %w", err)
	}

	deployReq := httptest.NewRequest(http.MethodPost, "/api/apps/"+firstRunExampleSlug+"/deploy", &upload)
	deployReq.Header.Set("Content-Type", mw.FormDataContentType())
	if err := serveFirstRunRequest(ctx, handler, user, deployReq, http.StatusOK); err != nil {
		return false, fmt.Errorf("deploy example app: %w", err)
	}
	return true, nil
}

func serveFirstRunRequest(ctx context.Context, handler http.Handler, user *db.User, req *http.Request, wantStatus int) error {
	requestCtx := auth.WithUser(ctx, user.ContextUser())
	req = req.WithContext(requestCtx)
	// A non-empty Authorization header selects the API CSRF exemption. The
	// authenticated identity itself comes from the trusted in-process context.
	req.Header.Set("Authorization", "Bearer shinyhub-first-run")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code == wantStatus {
		return nil
	}
	detail := strings.TrimSpace(recorder.Body.String())
	if len(detail) > 500 {
		detail = detail[:500] + "…"
	}
	if detail == "" {
		detail = http.StatusText(recorder.Code)
	}
	return fmt.Errorf("server returned %d: %s", recorder.Code, detail)
}

func buildFirstRunExampleBundle() ([]byte, error) {
	entries, err := fs.ReadDir(firstRunExampleFiles, "example_app")
	if err != nil {
		return nil, fmt.Errorf("read embedded example: %w", err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		content, err := firstRunExampleFiles.ReadFile(path.Join("example_app", name))
		if err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("read embedded example file %s: %w", name, err)
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("bundle embedded example file %s: %w", name, err)
		}
		if _, err := writer.Write(content); err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("bundle embedded example file %s: %w", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finish embedded example bundle: %w", err)
	}
	return out.Bytes(), nil
}
