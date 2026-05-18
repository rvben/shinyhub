package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/fleet"
)

func TestPatchManagedBy_SendsBodyHeadersAndPrecondition(t *testing.T) {
	var gotBody map[string]any
	var gotDigest, gotMB, gotRun, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		gotDigest = r.Header.Get("X-Shinyhub-If-Content-Digest")
		gotMB = r.Header.Get("X-Shinyhub-If-Managed-By")
		gotRun = r.Header.Get("X-Shinyhub-Run-Id")
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}

	marker := "fleet:eu"
	dg := "sha256:abc"
	empty := ""
	if err := patchManagedBy(cfg, "demo", &marker, &dg, &empty, "run-9"); err != nil {
		t.Fatalf("patchManagedBy: %v", err)
	}
	if v, _ := gotBody["managed_by"].(string); v != "fleet:eu" {
		t.Fatalf("managed_by body = %#v", gotBody["managed_by"])
	}
	if gotDigest != "sha256:abc" {
		t.Fatalf("digest precondition = %q", gotDigest)
	}
	if gotMB != "" {
		t.Fatalf("managed-by precondition = %q, want empty (asserts unmanaged)", gotMB)
	}
	if gotRun != "run-9" {
		t.Fatalf("run id = %q", gotRun)
	}
	if gotUA == "" {
		t.Fatal("user-agent not set")
	}
}

func TestPatchManagedBy_NilClearsToNull(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	if err := patchManagedBy(cfg, "demo", nil, nil, nil, "r"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if raw != `{"managed_by":null}` {
		t.Fatalf("body = %s, want managed_by null", raw)
	}
}

func TestSendFleetMutation_409IsConflictError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":"precondition failed: digest mismatch (re-run plan)"}`))
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	err := deleteFleetApp(cfg, "demo", nil, nil, "r")
	if err == nil || !isConflictError(err) {
		t.Fatalf("want conflictError, got %v", err)
	}
}

func TestSendFleetMutation_500IsPlainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	err := patchAppAccess(cfg, "demo", "public", nil, nil, "r")
	if err == nil || isConflictError(err) {
		t.Fatalf("want non-conflict error, got %v", err)
	}
}

func TestFleetConfigBody_OnlyDeclaredKeys(t *testing.T) {
	h := 30
	body := fleetConfigBody(fleet.Config{HibernateTimeoutMinutes: &h})
	if len(body) != 1 || body["hibernate_timeout_minutes"] != 30 {
		t.Fatalf("body = %#v, want only hibernate_timeout_minutes=30", body)
	}
	if len(fleetConfigBody(fleet.Config{})) != 0 {
		t.Fatal("empty config must yield empty body")
	}
}
