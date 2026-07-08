package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/metrics"
)

// scrapeRegistry returns the Prometheus exposition text for a registry.
func scrapeRegistry(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", rec.Code)
	}
	return rec.Body.String()
}

// deployForMetrics drives one deploy through the HTTP handler and returns the
// recorder, so a test can assert both the status and the recorded metric.
func deployForMetrics(t *testing.T, srv interface {
	Router() http.Handler
}, token, slug string) *httptest.ResponseRecorder {
	t.Helper()
	body, ctype := buildBundleUpload(t, "app.py", "print('v1')\n")
	req := httptest.NewRequest("POST", "/api/apps/"+slug+"/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

// TestDeploy_RecordsSuccessMetric proves a committed deploy increments
// shinyhub_deploys_total{result="success"}.
func TestDeploy_RecordsSuccessMetric(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	reg := metrics.New("test")
	srv.SetMetrics(reg)
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: 1, Port: 20001}}}, nil
	})

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "ok", Name: "OK", OwnerID: u.ID})
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")

	if rec := deployForMetrics(t, srv, token, "ok"); rec.Code != http.StatusOK {
		t.Fatalf("deploy returned %d: %s", rec.Code, rec.Body.String())
	}

	scrape := scrapeRegistry(t, reg)
	if !strings.Contains(scrape, `shinyhub_deploys_total{result="success"} 1`) {
		t.Fatalf("expected success deploy counter, got:\n%s", scrape)
	}
}

// TestDeploy_RecordsFailureMetric proves a deploy whose pool fails to come up
// increments shinyhub_deploys_total{result="failure"}.
func TestDeploy_RecordsFailureMetric(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	reg := metrics.New("test")
	srv.SetMetrics(reg)
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		return nil, errors.New("pool failed to start")
	})

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "bad", Name: "Bad", OwnerID: u.ID})
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")

	if rec := deployForMetrics(t, srv, token, "bad"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("deploy returned %d, want 500: %s", rec.Code, rec.Body.String())
	}

	scrape := scrapeRegistry(t, reg)
	if !strings.Contains(scrape, `shinyhub_deploys_total{result="failure"} 1`) {
		t.Fatalf("expected failure deploy counter, got:\n%s", scrape)
	}
}
