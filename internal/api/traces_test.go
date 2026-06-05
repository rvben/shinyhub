package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/tracing"
)

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
	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: ownerName, PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, _ := store.GetUserByUsername(ownerName)
	if err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: owner.ID}); err != nil {
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
