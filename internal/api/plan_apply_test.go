package api_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func planApplyBundle(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	zw := zip.NewWriter(&raw)
	for name, content := range map[string]string{
		"app.py":           "from shiny import App\napp = App()\n",
		"requirements.txt": "shiny\n",
	} {
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestDeployRejectsStalePlanRevisionBeforeMutation(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token, ownerID := seedUserAndJWT(t, store, "plan-owner", "admin")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "planned", Name: "Planned", OwnerID: ownerID, Access: "private"}); err != nil {
		t.Fatal(err)
	}

	getReq := authedRequest(t, http.MethodGet, "/api/apps/planned", nil, token)
	getRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", getRec.Code, getRec.Body.String())
	}
	var envelope struct {
		ResourceRevision string `json:"resource_revision"`
	}
	if err := json.NewDecoder(getRec.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ResourceRevision == "" || getRec.Header().Get("X-Shinyhub-Resource-Revision") != envelope.ResourceRevision {
		t.Fatalf("resource revision missing or inconsistent: body=%q header=%q", envelope.ResourceRevision, getRec.Header().Get("X-Shinyhub-Resource-Revision"))
	}

	patchReq := authedRequest(t, http.MethodPatch, "/api/apps/planned", []byte(`{"name":"Changed after plan"}`), token)
	patchRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", patchRec.Code, patchRec.Body.String())
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(planApplyBundle(t)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	deployReq := authedRequest(t, http.MethodPost, "/api/apps/planned/deploy", body.Bytes(), token)
	deployReq.Header.Set("Content-Type", mw.FormDataContentType())
	deployReq.Header.Set("X-Shinyhub-If-Resource-Revision", envelope.ResourceRevision)
	deployRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(deployRec, deployReq)
	if deployRec.Code != http.StatusConflict {
		t.Fatalf("stale deploy = %d, want 409: %s", deployRec.Code, deployRec.Body.String())
	}
	app, err := store.GetAppBySlug("planned")
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "Changed after plan" || app.DeployCount != 0 || app.CurrentVersion != "" || app.ContentDigest != "" {
		t.Fatalf("stale apply mutated deployment state: %+v", app)
	}
}
