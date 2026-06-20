package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRootCmd_HasServeSubcommand(t *testing.T) {
	for _, sub := range buildRoot().Commands() {
		if sub.Name() == "serve" {
			return
		}
	}
	t.Fatalf("rootCmd does not have a `serve` subcommand; has: %v", buildRoot().Commands())
}

func TestRootCmd_HasDeploySubcommand(t *testing.T) {
	for _, sub := range buildRoot().Commands() {
		if sub.Name() == "deploy" {
			return
		}
	}
	t.Fatalf("rootCmd does not have a `deploy` subcommand (CLI tree not grafted); has: %v", buildRoot().Commands())
}

func TestRootCmd_UseIsShinyhub(t *testing.T) {
	if buildRoot().Use != "shinyhub" {
		t.Fatalf("rootCmd.Use = %q, want \"shinyhub\"", buildRoot().Use)
	}
}

func TestRootCmd_VersionMatchesLdflags(t *testing.T) {
	// main's `version` var is "dev" in tests (no ldflags). Confirm it's wired
	// into the cobra Version field so `shinyhub --version` reports something.
	if buildRoot().Version == "" {
		t.Fatal("rootCmd.Version is empty; should be set to the ldflags-injected version")
	}
}

func TestRootCmd_HelpMentionsShinyhub(t *testing.T) {
	root := buildRoot()
	out := root.Short + " " + root.Long
	if !strings.Contains(strings.ToLower(out), "shinyhub") {
		t.Fatalf("rootCmd help does not mention shinyhub: Short=%q Long=%q", root.Short, root.Long)
	}
}

// TestIsLongLivedAPIRoute documents which routes bypass the 30s API
// timeout. The data PUT path streams large user files and must be
// exempt — the timeout would corrupt the upload (TimeoutHandler swaps
// the writer mid-stream, the handler keeps writing to a disconnected
// recorder, and the client receives an ambiguous timeout response
// while the file may still complete on disk).
func TestIsLongLivedAPIRoute(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		// SSE log stream — long-lived by design.
		{"logs SSE", http.MethodGet, "/api/apps/myapp/logs", true},

		// Bundle upload — large body.
		{"deploy POST", http.MethodPost, "/api/apps/myapp/deploy", true},

		// Lifecycle swaps — long-lived under the deploy lock.
		{"restart POST", http.MethodPost, "/api/apps/myapp/restart", true},
		{"rollback POST", http.MethodPost, "/api/apps/myapp/rollback", true},
		{"rollback PUT", http.MethodPut, "/api/apps/myapp/rollback", true},
		{"stop POST", http.MethodPost, "/api/apps/myapp/stop", true},

		// Schedule run log stream — long-lived by design.
		{"schedule run logs GET", http.MethodGet, "/api/apps/myapp/schedules/7/runs/42/logs", true},

		// Wrong method on a lifecycle verb must NOT bypass the timeout.
		{"restart GET", http.MethodGet, "/api/apps/myapp/restart", false},
		{"deploy GET", http.MethodGet, "/api/apps/myapp/deploy", false},
		{"stop DELETE", http.MethodDelete, "/api/apps/myapp/stop", false},
		{"logs POST", http.MethodPost, "/api/apps/myapp/logs", false},

		// Old broad suffix match false positives — these must stay bounded.
		{"schedule named stop", http.MethodPost, "/api/apps/myapp/schedules/stop", false},
		{"env key restart", http.MethodPut, "/api/apps/myapp/env/restart", false},
		{"schedule run logs wrong method", http.MethodPost, "/api/apps/myapp/schedules/7/runs/42/logs", false},
		{"schedule run logs missing run id", http.MethodGet, "/api/apps/myapp/schedules/7/runs//logs", false},
		{"non-app path ending stop", http.MethodPost, "/api/cluster/stop", false},

		// Per-app data PUT — streams arbitrary-size user files.
		{"data PUT root", http.MethodPut, "/api/apps/myapp/data/file.bin", true},
		{"data PUT nested", http.MethodPut, "/api/apps/myapp/data/sub/dir/file.bin", true},
		{"data PUT slug with dashes", http.MethodPut, "/api/apps/my-cool-app/data/x", true},

		// Data list/delete are quick metadata ops — keep the timeout.
		{"data list GET", http.MethodGet, "/api/apps/myapp/data", false},
		{"data delete DELETE", http.MethodDelete, "/api/apps/myapp/data/file.bin", false},

		// Other API ops keep the timeout.
		{"app status GET", http.MethodGet, "/api/apps/myapp", false},
		{"replicas patch", http.MethodPatch, "/api/apps/myapp/replicas", false},
		{"login POST", http.MethodPost, "/api/auth/login", false},

		// Defense in depth: a non-data path that contains "/data/" by
		// coincidence (e.g. an app slug "data") must not slip through.
		{"slug literally 'data'", http.MethodGet, "/api/apps/data/replicas", false},

		// Defense in depth: PUT outside /api/apps/{slug}/data/ stays bounded.
		{"put outside apps", http.MethodPut, "/api/users/42/profile", false},
		{"put without slug", http.MethodPut, "/api/apps/", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isLongLivedAPIRoute(tc.method, tc.path)
			if got != tc.want {
				t.Errorf("isLongLivedAPIRoute(%q, %q) = %v, want %v",
					tc.method, tc.path, got, tc.want)
			}
		})
	}
}

// TestNeedsUnboundedDeadline documents which surfaces must run without the
// server's connection-level ReadTimeout/WriteTimeout: the reverse proxy
// (Shiny WebSockets + streaming app responses), the Fargate bundle stream,
// and the long-lived /api routes. Everything else keeps the deadlines so a
// slow-read or slow-body client cannot pin a connection indefinitely.
func TestNeedsUnboundedDeadline(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		// Reverse proxy: WebSockets + streaming app responses.
		{"proxy app GET", http.MethodGet, "/app/myapp/", true},
		{"proxy app websocket", http.MethodGet, "/app/myapp/websocket", true},
		{"proxy app POST", http.MethodPost, "/app/myapp/submit", true},
		{"proxy root", http.MethodGet, "/app/", true},

		// Fargate bundle stream — large response body.
		{"fargate bundle", http.MethodGet, "/internal/fargate-bundle/sha256-abc", true},

		// Delegates to the /api long-lived matrix.
		{"api logs SSE", http.MethodGet, "/api/apps/myapp/logs", true},
		{"api deploy upload", http.MethodPost, "/api/apps/myapp/deploy", true},
		{"api data PUT", http.MethodPut, "/api/apps/myapp/data/f.bin", true},
		{"api status GET", http.MethodGet, "/api/apps/myapp", false},
		{"api login", http.MethodPost, "/api/auth/login", false},

		// Bounded control-plane surfaces.
		{"static asset", http.MethodGet, "/static/app.js", false},
		{"healthz", http.MethodGet, "/healthz", false},
		{"dashboard root", http.MethodGet, "/", false},
		{"other internal route", http.MethodGet, "/internal/other", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := needsUnboundedDeadline(tc.method, tc.path)
			if got != tc.want {
				t.Errorf("needsUnboundedDeadline(%q, %q) = %v, want %v",
					tc.method, tc.path, got, tc.want)
			}
		})
	}
}

// TestIsLargeUploadRoute documents which API routes are exempt from the global
// JSON body cap because they legitimately stream large bodies and apply their
// own (much larger) limit downstream: bundle deploy and per-app data upload.
func TestIsLargeUploadRoute(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"deploy POST", http.MethodPost, "/api/apps/myapp/deploy", true},
		{"data PUT", http.MethodPut, "/api/apps/myapp/data/big.csv", true},
		{"data PUT nested", http.MethodPut, "/api/apps/myapp/data/a/b/c.bin", true},

		// Small-body mutations stay capped.
		{"deploy GET", http.MethodGet, "/api/apps/myapp/deploy", false},
		{"restart POST", http.MethodPost, "/api/apps/myapp/restart", false},
		{"patch app", http.MethodPatch, "/api/apps/myapp", false},
		{"data list GET", http.MethodGet, "/api/apps/myapp/data", false},
		{"data delete", http.MethodDelete, "/api/apps/myapp/data/x", false},
		{"login", http.MethodPost, "/api/auth/login", false},
		{"slug literally data", http.MethodPut, "/api/apps/data/replicas", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLargeUploadRoute(tc.method, tc.path); got != tc.want {
				t.Errorf("isLargeUploadRoute(%q, %q) = %v, want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

// TestBodyLimitHandler proves the middleware caps ordinary request bodies while
// letting bulk-upload routes through unbounded for their own downstream limit.
func TestBodyLimitHandler(t *testing.T) {
	var readErr error
	var readN int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		readN, readErr = len(b), err
		w.WriteHeader(http.StatusOK)
	})
	h := bodyLimitHandler(next)

	oversize := bytes.Repeat([]byte("a"), maxAPIJSONBody+1)

	// Over-cap body on an ordinary route is rejected before it can exhaust memory.
	req := httptest.NewRequest(http.MethodPatch, "/api/apps/myapp", bytes.NewReader(oversize))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if readErr == nil {
		t.Errorf("ordinary route: expected a read error for a %d-byte body over the %d cap", len(oversize), maxAPIJSONBody)
	}

	// The same body on a bulk-upload route passes through for the handler's own limit.
	readErr, readN = nil, 0
	req = httptest.NewRequest(http.MethodPost, "/api/apps/myapp/deploy", bytes.NewReader(oversize))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if readErr != nil {
		t.Errorf("upload route: body must pass through uncapped, got read error: %v", readErr)
	}
	if readN != len(oversize) {
		t.Errorf("upload route: read %d bytes, want %d", readN, len(oversize))
	}
}
