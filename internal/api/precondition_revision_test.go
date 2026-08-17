package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestAppResourceRevisionIsStableAndOpaque(t *testing.T) {
	app := &db.App{
		ID: 7, Slug: "sales", Name: "Sales", OwnerID: 3, Access: "private",
		Status: "running", UpdatedAt: time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC),
		ContentDigest: "sha256:old", Replicas: 2,
	}
	first := appResourceRevision(app)
	second := appResourceRevision(app)
	if first != second || !strings.HasPrefix(first, "rev:app:") || len(first) != len("rev:app:")+64 {
		t.Fatalf("revision is not stable and opaque: %q / %q", first, second)
	}
	app.Access = "public"
	if changed := appResourceRevision(app); changed == first {
		t.Fatal("revision did not change with durable app state")
	}
}

func TestResourceRevisionPreconditionConflict(t *testing.T) {
	app := &db.App{ID: 1, Slug: "demo", Name: "Demo", Access: "private", UpdatedAt: time.Now()}
	req := httptest.NewRequest(http.MethodPatch, "/api/apps/demo", nil)
	req.Header.Set(hdrIfResourceRevision, "rev:app:stale")
	rec := httptest.NewRecorder()
	if !checkAppPreconditions(rec, req, app) {
		t.Fatal("stale resource revision was accepted")
	}
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "re-run plan") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}
