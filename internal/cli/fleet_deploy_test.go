package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDeployAppBundle_DeploysThenReadsPromotedDigest(t *testing.T) {
	var deployHits, listHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			// First GET = ensureApp existence check AND health poll.
			// Return running so the poll completes; include the digest
			// only after a deploy has happened.
			if atomic.LoadInt32(&deployHits) > 0 {
				atomic.AddInt32(&listHits, 1)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"app": map[string]any{"status": "running"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"app": map[string]any{"status": "running"},
			})
		case r.Method == "POST" && r.URL.Path == "/api/apps/demo/deploy":
			atomic.AddInt32(&deployHits, 1)
			if r.Header.Get("X-Shinyhub-Run-Id") == "" {
				t.Error("deploy missing run id header")
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"slug": "demo", "access": "private", "content_digest": "sha256:PROMOTED"},
			})
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}

	dg, err := deployAppBundle(cfg, "demo", dir, "private", io.Discard, "run-1")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if dg != "sha256:PROMOTED" {
		t.Fatalf("promoted digest = %q, want sha256:PROMOTED", dg)
	}
	if atomic.LoadInt32(&deployHits) == 0 {
		t.Fatal("deploy endpoint never called")
	}
	if atomic.LoadInt32(&listHits) == 0 {
		t.Fatal("post-deploy health poll never reached the running-state branch")
	}
}

func TestDeployAppBundle_PropagatesDeployFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy") {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		if r.Method == "GET" && r.URL.Path == "/api/apps/demo" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	if _, err := deployAppBundle(cfg, "demo", dir, "private", io.Discard, "r"); err == nil {
		t.Fatal("expected deploy failure to propagate")
	}
}
