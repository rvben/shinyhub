package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// deployManifest uploads a one-file bundle carrying manifest and returns the
// recorder, so each step below reads as one deploy rather than six lines of
// multipart plumbing.
func deployManifest(t *testing.T, srv *Server, token, slug, manifest string) *httptest.ResponseRecorder {
	t.Helper()
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/"+slug+"/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

// A declared name/description reconciles on deploy; an absent key leaves the
// stored value alone (so a rename made in the dashboard survives); a declared
// empty description clears it.
func TestManifestAppMetaDeclaredOnly(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if err := store.CreateApp(db.CreateAppParams{
		Slug: "metaapp", Name: "Original Name", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	if rec := deployManifest(t, srv, token, "metaapp",
		"[app]\nname = \"Quarterly Revenue\"\ndescription = \"Regional roll-up\"\n"); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	app, err := store.GetAppBySlug("metaapp")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Name != "Quarterly Revenue" {
		t.Fatalf("name = %q, want %q", app.Name, "Quarterly Revenue")
	}
	if app.Description != "Regional roll-up" {
		t.Fatalf("description = %q, want %q", app.Description, "Regional roll-up")
	}
	// The slug is the URL identifier and is never touched by the display name.
	if app.Slug != "metaapp" {
		t.Fatalf("slug = %q, want it unchanged", app.Slug)
	}

	// Rename out of band through the same PATCH the dashboard uses, then
	// redeploy a manifest that declares neither key: the rename must survive. A
	// harmless sibling field keeps the manifest non-empty so
	// applyManifestAppSettings actually runs.
	patchReq := httptest.NewRequest("PATCH", "/api/apps/metaapp",
		strings.NewReader(`{"name":"Renamed In UI","description":"Described in UI"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("Authorization", "Bearer "+token)
	patchRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("rename: expected 200, got %d: %s", patchRec.Code, patchRec.Body.String())
	}
	if rec := deployManifest(t, srv, token, "metaapp",
		"[app]\nmax_sessions_per_replica = 5\n"); rec.Code != http.StatusOK {
		t.Fatalf("second deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	app2, err := store.GetAppBySlug("metaapp")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app2.Name != "Renamed In UI" {
		t.Errorf("name after undeclared deploy = %q, want unchanged %q", app2.Name, "Renamed In UI")
	}
	if app2.Description != "Described in UI" {
		t.Errorf("description after undeclared deploy = %q, want unchanged %q", app2.Description, "Described in UI")
	}

	// Declaring the keys again reasserts them over the out-of-band rename, and a
	// declared "" clears the description.
	if rec := deployManifest(t, srv, token, "metaapp",
		"[app]\nname = \"Quarterly Revenue\"\ndescription = \"\"\n"); rec.Code != http.StatusOK {
		t.Fatalf("third deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	app3, err := store.GetAppBySlug("metaapp")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app3.Name != "Quarterly Revenue" {
		t.Errorf("name after redeclared deploy = %q, want %q", app3.Name, "Quarterly Revenue")
	}
	if app3.Description != "" {
		t.Errorf("description after declared empty = %q, want it cleared", app3.Description)
	}
}

// An invalid declared name fails Phase A, which aborts the deploy. Pinned
// because the manifest layer is the only place the value is checked before it
// reaches a NOT NULL column that every surface renders.
func TestManifestAppMetaInvalidNameRejected(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if err := store.CreateApp(db.CreateAppParams{
		Slug: "metabad", Name: "Keep Me", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	rec := deployManifest(t, srv, token, "metabad", "[app]\nname = \"   \"\n")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a whitespace-only name, got %d: %s", rec.Code, rec.Body.String())
	}
	app, err := store.GetAppBySlug("metabad")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Name != "Keep Me" {
		t.Errorf("name = %q, want the stored name untouched by a rejected deploy", app.Name)
	}
}

// The two renderers that make an applied [app] name/description visible outside
// the DB: manifestAppliedSummary for the deploy response and manifestAppDetail
// for the audit blob. Also pins the IsZero oracle end to end - a manifest
// declaring ONLY display metadata must still produce a non-nil manifest.app in
// the deploy response.
func TestManifestAppMetaReportedEverywhere(t *testing.T) {
	name := "Quarterly Revenue"
	desc := "Regional roll-up"

	summary := manifestAppliedSummary(deploy.AppSettings{Name: &name, Description: &desc})
	if summary["name"] != name {
		t.Errorf("manifestAppliedSummary[\"name\"] = %v, want %q", summary["name"], name)
	}
	if summary["description"] != desc {
		t.Errorf("manifestAppliedSummary[\"description\"] = %v, want %q", summary["description"], desc)
	}

	detail := manifestAppDetail(deploy.AppSettings{Name: &name, Description: &desc})
	if !strings.Contains(detail, "name") || !strings.Contains(detail, "description") {
		t.Errorf("manifestAppDetail = %q, want it to mention name and description", detail)
	}

	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")
	if err := store.CreateApp(db.CreateAppParams{
		Slug: "metareport", Name: "Meta Report", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	rec := deployManifest(t, srv, token, "metareport", "[app]\nname = \""+name+"\"\n")
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	manifestResp, ok := resp["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("manifest missing from response: %s", rec.Body.String())
	}
	appSummary, ok := manifestResp["app"].(map[string]any)
	if !ok {
		t.Fatalf("manifest.app missing from response (name-only manifest treated as zero): %v", manifestResp)
	}
	if appSummary["name"] != name {
		t.Errorf("manifest.app.name = %v, want %q", appSummary["name"], name)
	}

	events, _ := store.ListAuditEvents("update_app", 10, 0)
	if !auditEventsContain(events, "update_app", "metareport") {
		t.Error("expected an update_app audit event for a name-only manifest deploy")
	}
}
