package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func uploadDraft(t *testing.T, s *Server, token, slug string, files map[string]string) db.DeploymentDraft {
	t.Helper()
	body, ctype := buildMultiFileBundleUpload(t, files)
	req := httptest.NewRequest("POST", "/api/apps/"+slug+"/drafts", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("create draft: %d %s", rec.Code, rec.Body.String())
	}
	var d db.DeploymentDraft
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func draftAction(s *Server, token, slug, id, action string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/apps/"+slug+"/drafts/"+id+"/"+action, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	return rec
}

func TestDraftReviewAndPromotion(t *testing.T) {
	s, store, token := newManifestE2EServer(t)
	parent := seedStoppedTestApp(t, store, "production", "stopped", 1)
	files := map[string]string{"app.py": "from shiny import App\n"}
	d := uploadDraft(t, s, token, parent.Slug, files)
	after, err := store.GetAppBySlug(parent.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if appResourceRevision(parent) != appResourceRevision(after) {
		t.Fatal("draft upload changed production")
	}
	if got := draftAction(s, token, parent.Slug, d.ID, "promote"); got.Code != 409 {
		t.Fatalf("unreviewed promotion: %d", got.Code)
	}
	rec := draftAction(s, token, parent.Slug, d.ID, "preview")
	if rec.Code != 200 {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	preview, err := store.GetAppBySlug(d.PreviewSlug)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Access != "private" || preview.ContentDigest != d.ContentDigest {
		t.Fatalf("preview: %+v", preview)
	}
	after, _ = store.GetAppBySlug(parent.Slug)
	if appResourceRevision(parent) != appResourceRevision(after) {
		t.Fatal("preview changed production")
	}
	rec = draftAction(s, token, parent.Slug, d.ID, "promote")
	if rec.Code != 200 {
		t.Fatalf("promote: %d %s", rec.Code, rec.Body.String())
	}
	after, _ = store.GetAppBySlug(parent.Slug)
	if after.ContentDigest != d.ContentDigest || after.Status != "stopped" {
		t.Fatalf("production: %+v", after)
	}
	saved, err := store.GetDeploymentDraft(parent.ID, d.ID)
	if err != nil || saved.PromotedAt == nil {
		t.Fatalf("promotion record: %+v %v", saved, err)
	}
	if got := draftAction(s, token, parent.Slug, d.ID, "promote"); got.Code != 409 {
		t.Fatalf("repeated promotion: %d", got.Code)
	}
}

func TestDraftPreviewCannotPublishOrReplaceCode(t *testing.T) {
	s, store, token := newManifestE2EServer(t)
	parent := seedStoppedTestApp(t, store, "production", "stopped", 0)
	d := uploadDraft(t, s, token, parent.Slug, map[string]string{"app.py": "from shiny import App\n"})
	rec := draftAction(s, token, parent.Slug, d.ID, "preview")
	if rec.Code != 200 {
		t.Fatalf("preview: %s", rec.Body.String())
	}
	json.Unmarshal(rec.Body.Bytes(), &d)
	for _, tc := range []struct{ method, path, body string }{
		{"PATCH", "/access", `{"access":"public"}`},
		{"PATCH", "", `{"access":"shared"}`},
		{"POST", "/deploy", ""}, {"POST", "/rollback", ""}, {"POST", "/drafts", ""},
		{"POST", "/schedules", `{}`}, {"POST", "/shared-data", `{}`},
	} {
		req := httptest.NewRequest(tc.method, "/api/apps/"+d.PreviewSlug+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, req)
		if rec.Code != 409 {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	preview, _ := store.GetAppBySlug(d.PreviewSlug)
	if preview.Access != "private" {
		t.Fatal("preview became public")
	}
}

func TestDraftPromotionRejectsStaleOrCorruptBundle(t *testing.T) {
	for _, kind := range []string{"stale", "corrupt", "expired", "wrong-parent"} {
		t.Run(kind, func(t *testing.T) {
			s, store, token := newManifestE2EServer(t)
			parent := seedStoppedTestApp(t, store, "production", "stopped", 0)
			d := uploadDraft(t, s, token, parent.Slug, map[string]string{"app.py": "from shiny import App\n"})
			rec := draftAction(s, token, parent.Slug, d.ID, "preview")
			if rec.Code != 200 {
				t.Fatalf("preview: %s", rec.Body.String())
			}
			switch kind {
			case "stale":
				store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: parent.Slug, Status: "crashed"})
			case "corrupt":
				if err := os.WriteFile(s.draftPath(parent.Slug, d.ID), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "expired":
				// The expiry is persisted, independent of the process clock and restart.
				stale := db.DeploymentDraft{ID: "expired", AppID: parent.ID, ContentDigest: d.ContentDigest, BaseRevision: appResourceRevision(parent), CreatedAt: time.Now().Add(-2 * time.Hour).Unix(), ExpiresAt: time.Now().Add(-time.Hour).Unix()}
				if err := store.CreateDeploymentDraft(stale); err != nil {
					t.Fatal(err)
				}
				d = stale
			case "wrong-parent":
				parent = seedStoppedTestApp(t, store, "other", "stopped", 0)
			}
			rec = draftAction(s, token, parent.Slug, d.ID, "promote")
			expected := 409
			if kind == "expired" {
				expected = 410
			}
			if kind == "wrong-parent" {
				expected = 404
			}
			if rec.Code != expected {
				t.Fatalf("promotion: %d %s", rec.Code, rec.Body.String())
			}
			deps, _ := store.ListDeployments(parent.ID)
			if len(deps) != 0 {
				t.Fatal("rejected promotion changed production")
			}
		})
	}
}

func TestDraftRejectsPreviewSideEffects(t *testing.T) {
	for _, manifest := range []string{
		"[[hook]]\non='post-deploy'\ncommand=['touch','marker']\n",
		"[[schedule]]\nname='job'\ncron='0 0 * * *'\ncmd='python job.py'\n",
		"[access]\nviewer_groups=['all']\n",
	} {
		t.Run(manifest, func(t *testing.T) {
			s, store, token := newManifestE2EServer(t)
			parent := seedStoppedTestApp(t, store, "production", "stopped", 0)
			d := uploadDraft(t, s, token, parent.Slug, map[string]string{"app.py": "from shiny import App\n", "shinyhub.toml": manifest})
			rec := draftAction(s, token, parent.Slug, d.ID, "preview")
			if rec.Code != 422 {
				t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
			}
			saved, _ := store.GetDeploymentDraft(parent.ID, d.ID)
			if saved.PreviewAppID != nil {
				t.Fatal("created side-effecting preview")
			}
		})
	}
}

func TestDraftViewerCannotInspectOrPromote(t *testing.T) {
	s, store, token := newManifestE2EServer(t)
	parent := seedStoppedTestApp(t, store, "production", "stopped", 0)
	d := uploadDraft(t, s, token, parent.Slug, map[string]string{"app.py": "from shiny import App\n"})
	if err := store.CreateUser(db.CreateUserParams{Username: "reviewer", PasswordHash: "unused", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	user, _ := store.GetUserByUsername("reviewer")
	store.GrantAppAccess(parent.Slug, user.ID)
	reviewerToken, _ := auth.IssueJWT(user.ID, user.Username, user.Role, "test-secret")
	for _, action := range []string{"preview", "promote"} {
		rec := draftAction(s, reviewerToken, parent.Slug, d.ID, action)
		if rec.Code != 403 {
			t.Errorf("%s: %d %s", action, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/apps/"+parent.Slug+"/drafts", nil)
	req.Header.Set("Authorization", "Bearer "+reviewerToken)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("list: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.Storage.AppsDir, parent.Slug, "drafts", d.ID+".zip")); err != nil {
		t.Fatal(err)
	}
}

func TestDraftPreviewHasIndependentCredentialsAndReviewers(t *testing.T) {
	s, store, token := newManifestE2EServer(t)
	parent := seedStoppedTestApp(t, store, "production", "stopped", 0)
	if err := store.UpsertAppEnvVar(parent.ID, "PRODUCTION_ONLY", []byte("private-value"), false); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: "unused", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	user, _ := store.GetUserByUsername("viewer")
	if err := store.GrantAppAccess(parent.Slug, user.ID); err != nil {
		t.Fatal(err)
	}
	d := uploadDraft(t, s, token, parent.Slug, map[string]string{"app.py": "from shiny import App\n"})
	rec := draftAction(s, token, parent.Slug, d.ID, "preview")
	if rec.Code != 200 {
		t.Fatalf("preview: %s", rec.Body.String())
	}
	json.Unmarshal(rec.Body.Bytes(), &d)
	env, err := store.ListAppEnvVars(*d.PreviewAppID)
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 0 {
		t.Fatal("production environment copied to preview")
	}
	allowed, err := store.UserCanAccessApp(d.PreviewSlug, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("production viewer can access draft")
	}
	req := httptest.NewRequest("POST", "/api/apps/"+d.PreviewSlug+"/members", strings.NewReader(`{"username":"viewer"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("grant reviewer: %d %s", rec.Code, rec.Body.String())
	}
	allowed, err = store.UserCanAccessApp(d.PreviewSlug, user.ID)
	if err != nil || !allowed {
		t.Fatalf("reviewer access: %v %v", allowed, err)
	}
}

func TestDraftDeletionAndExpiryReclaimRetainedBundles(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "expire"}[expire], func(t *testing.T) {
			s, store, token := newManifestE2EServer(t)
			parent := seedStoppedTestApp(t, store, "production", "stopped", 0)
			d := uploadDraft(t, s, token, parent.Slug, map[string]string{"app.py": "from shiny import App\n"})
			rec := draftAction(s, token, parent.Slug, d.ID, "preview")
			if rec.Code != 200 {
				t.Fatalf("preview: %s", rec.Body.String())
			}
			json.Unmarshal(rec.Body.Bytes(), &d)
			if expire {
				s.reapExpiredDevelopmentApps(context.Background(), time.Unix(d.ExpiresAt+1, 0))
			} else {
				req := httptest.NewRequest("DELETE", "/api/apps/"+parent.Slug+"/drafts/"+d.ID, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				rec = httptest.NewRecorder()
				s.Router().ServeHTTP(rec, req)
				if rec.Code != 200 {
					t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
				}
			}
			if _, err := store.GetDeploymentDraft(parent.ID, d.ID); !errors.Is(err, db.ErrNotFound) {
				t.Fatalf("draft remains: %v", err)
			}
			if _, err := store.GetAppBySlug(d.PreviewSlug); !errors.Is(err, db.ErrNotFound) {
				t.Fatalf("preview remains: %v", err)
			}
			if _, err := os.Stat(s.draftPath(parent.Slug, d.ID)); !os.IsNotExist(err) {
				t.Fatalf("retained archive remains: %v", err)
			}
			if _, err := store.GetAppBySlug(parent.Slug); err != nil {
				t.Fatalf("production removed: %v", err)
			}
		})
	}
}

func TestConcurrentDraftReviewAndPromotion(t *testing.T) {
	s, store, token := newManifestE2EServer(t)
	parent := seedStoppedTestApp(t, store, "production", "stopped", 0)
	d := uploadDraft(t, s, token, parent.Slug, map[string]string{"app.py": "from shiny import App\n"})
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { results <- draftAction(s, token, parent.Slug, d.ID, "preview") }()
	}
	var previews []db.DeploymentDraft
	for range 2 {
		rec := <-results
		if rec.Code != 200 {
			t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
		}
		var preview db.DeploymentDraft
		json.Unmarshal(rec.Body.Bytes(), &preview)
		previews = append(previews, preview)
	}
	if previews[0].PreviewSlug != previews[1].PreviewSlug {
		t.Fatal("concurrent previews created different apps")
	}
	deps, err := store.ListDeployments(*previews[0].PreviewAppID)
	if err != nil || len(deps) != 1 {
		t.Fatalf("duplicate preview deployment: %d %v", len(deps), err)
	}
	for range 2 {
		go func() { results <- draftAction(s, token, parent.Slug, d.ID, "promote") }()
	}
	codes := map[int]int{}
	for range 2 {
		rec := <-results
		codes[rec.Code]++
	}
	if codes[200] != 1 || codes[409] != 1 {
		t.Fatalf("promotion outcomes: %v", codes)
	}
	deps, err = store.ListDeployments(parent.ID)
	if err != nil || len(deps) != 1 {
		t.Fatalf("duplicate production deployment: %d %v", len(deps), err)
	}
}
