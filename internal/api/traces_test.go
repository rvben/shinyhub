package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/tracing"
)

func TestTracesPythonInstrumentationUsesServingDeploymentAndManagePermission(t *testing.T) {
	store := dbtest.New(t)
	dir := t.TempDir()
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: dir, AppDataDir: t.TempDir()},
		Tracing: config.TracingConfig{Enabled: true},
	}
	mgr := process.NewManager(dir, process.NewNativeRuntime())
	mgr.SetAutoInstrumentAppsDefault(true)
	srv := api.New(cfg, store, mgr, nil)
	owner := seedTracesApp(t, store, "demo", "owner")
	viewer, viewerID := seedUserAndJWT(t, store, "viewer", "developer")
	if err := store.GrantAppAccess("demo", viewerID); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "deployed")
	if err := os.MkdirAll(filepath.Join(bundle, ".venv", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{"pyproject.toml": 0o644, "uv.lock": 0o644, ".venv/bin/python": 0o755} {
		if err := os.WriteFile(filepath.Join(bundle, name), nil, mode); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v1", BundleDir: bundle}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v2", BundleDir: t.TempDir(), Status: db.DeploymentFailed}); err != nil {
		t.Fatal(err)
	}
	const traces = "/api/apps/demo/traces"
	readState := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodGet, traces, nil, viewer))
		var body struct {
			State string `json:"python_instrumentation"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("get traces: status %d, decode %v", rec.Code, err)
		}
		return body.State
	}
	if state := readState(); state != "not_prepared" {
		t.Fatalf("expected status of serving deployment, got %q", state)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodPost, traces+"/instrumentation", nil, viewer))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer preparation returned %d: %s", rec.Code, rec.Body.String())
	}
	cfg.Tracing.Enabled = false
	if state := readState(); state != "" {
		t.Fatalf("tracing disabled but preparation status is %q", state)
	}
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodPost, traces+"/instrumentation", nil, owner))
	if rec.Code != http.StatusConflict {
		t.Fatalf("preparation with tracing disabled returned %d: %s", rec.Code, rec.Body.String())
	}
}

// newTracesTestServer builds a server with a tracing buffer wired in. The
// returned config can be mutated by the caller before issuing requests so each
// test controls Enabled / TraceLinkTemplate independently.
func newTracesTestServer(t *testing.T) (*api.Server, *db.Store, *tracing.Buffer, *config.Config) {
	t.Helper()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	buf := tracing.NewBuffer(10, time.Second)
	srv := api.New(cfg, store, nil, nil)
	srv.SetTraceBuffer(buf)
	return srv, store, buf, cfg
}

func seedTracesApp(t *testing.T, store *db.Store, slug, ownerName string) (token string) {
	t.Helper()
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: ownerName, PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, _ := store.GetUserByUsername(ownerName)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: owner.ID}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	tok, _ := auth.IssueJWT(owner.ID, ownerName, "developer", "test-secret")
	return tok
}

func TestGetTraces_Unauthenticated(t *testing.T) {
	srv, store, _, _ := newTracesTestServer(t)
	seedTracesApp(t, store, "demo", "owner")

	req := httptest.NewRequest("GET", "/api/apps/demo/traces", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", rec.Code)
	}
}

func TestGetTraces_ForbiddenForOtherUser(t *testing.T) {
	srv, store, _, _ := newTracesTestServer(t)
	seedTracesApp(t, store, "demo", "owner")
	// Second user has no access to demo.
	otherToken, _ := seedUserAndJWT(t, store, "stranger", "developer")

	req := authedRequest(t, "GET", "/api/apps/demo/traces", nil, otherToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Errorf("expected 403/404 for non-owner, got %d", rec.Code)
	}
}

func TestGetTraces_DisabledReturnsEmpty(t *testing.T) {
	srv, store, _, _ := newTracesTestServer(t)
	tok := seedTracesApp(t, store, "demo", "owner")
	// Tracing config left disabled (default).
	req := authedRequest(t, "GET", "/api/apps/demo/traces", nil, tok)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Enabled           bool           `json:"enabled"`
		TraceLinkTemplate string         `json:"trace_link_template,omitempty"`
		Spans             []tracing.Span `json:"spans"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Enabled {
		t.Errorf("expected enabled=false")
	}
	if resp.Spans == nil {
		t.Errorf("spans must be a JSON array (non-nil), got nil")
	}
	if len(resp.Spans) != 0 {
		t.Errorf("expected empty spans, got %d", len(resp.Spans))
	}
}

func TestGetTraces_EnabledWithLinkTemplate(t *testing.T) {
	srv, store, buf, cfg := newTracesTestServer(t)
	cfg.Tracing = config.TracingConfig{
		Enabled:           true,
		OTLPEndpoint:      "http://collector:4318",
		OTLPProtocol:      "http/protobuf",
		SampleRatio:       0.5,
		SlowRequestMS:     1000,
		RingBufferSize:    10,
		TraceLinkTemplate: "https://tempo.example/{trace_id}",
	}
	tok := seedTracesApp(t, store, "demo", "owner")

	// Seed two retained spans: one error, one slow. Recorded oldest-first.
	buf.Record(tracing.Span{
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "1111111111111111",
		AppSlug: "demo", Replica: 0, Method: "GET", Path: "/oops",
		Status: 502, DurationMS: 12, StartedAt: time.Unix(1700000000, 0),
		Error: "bad gateway",
	})
	buf.Record(tracing.Span{
		TraceID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", SpanID: "2222222222222222",
		AppSlug: "demo", Replica: 1, Method: "GET", Path: "/slow",
		Status: 200, DurationMS: 2500, StartedAt: time.Unix(1700000001, 0),
		Sampled: true,
	})

	req := authedRequest(t, "GET", "/api/apps/demo/traces", nil, tok)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Enabled           bool           `json:"enabled"`
		TraceLinkTemplate string         `json:"trace_link_template"`
		Spans             []tracing.Span `json:"spans"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Enabled {
		t.Errorf("expected enabled=true")
	}
	if resp.TraceLinkTemplate != "https://tempo.example/{trace_id}" {
		t.Errorf("trace_link_template = %q", resp.TraceLinkTemplate)
	}
	if len(resp.Spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(resp.Spans))
	}
	// Newest-first ordering.
	if resp.Spans[0].Path != "/slow" {
		t.Errorf("expected newest span (path=/slow) first, got %q", resp.Spans[0].Path)
	}
	if resp.Spans[1].Path != "/oops" {
		t.Errorf("expected older span (path=/oops) second, got %q", resp.Spans[1].Path)
	}
}

func TestGetTraces_PerAppIsolation(t *testing.T) {
	srv, store, buf, cfg := newTracesTestServer(t)
	cfg.Tracing.Enabled = true
	cfg.Tracing.OTLPEndpoint = "http://collector:4318"
	tok := seedTracesApp(t, store, "demo", "owner")

	buf.Record(tracing.Span{AppSlug: "other", Status: 500, DurationMS: 1})
	buf.Record(tracing.Span{AppSlug: "demo", Status: 500, DurationMS: 1, Path: "/mine"})

	req := authedRequest(t, "GET", "/api/apps/demo/traces", nil, tok)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Spans []tracing.Span `json:"spans"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Spans) != 1 || resp.Spans[0].Path != "/mine" {
		t.Errorf("expected only demo's span, got %+v", resp.Spans)
	}
}
