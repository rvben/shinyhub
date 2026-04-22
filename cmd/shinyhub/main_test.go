package main

import (
	"net/http"
	"testing"
)

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
