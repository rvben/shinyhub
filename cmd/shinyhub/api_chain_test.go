package main

import (
	"archive/zip"
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// newAPIChainFixture builds the production /api/ handler chain (apiHandler, the
// same function serve mounts) over a real api.Server with one admin and one
// app, and returns the chain plus a bearer token for the admin.
func newAPIChainFixture(t *testing.T, slug string) (http.Handler, string) {
	t.Helper()
	store := dbtest.New(t)
	hash, err := auth.HashPassword("chain-fixture-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	admin, err := store.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("lookup admin: %v", err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: admin.ID, Access: "private"}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv := api.New(cfg, store, nil, nil)
	token, err := auth.IssueJWT(admin.ID, admin.Username, admin.Role, "test-secret")
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}
	return apiHandler(srv), token
}

// bundleUploadBody returns a multipart "bundle" upload whose zip holds app.R
// plus enough incompressible filler to exceed size bytes on the wire.
func bundleUploadBody(t *testing.T, size int) (*bytes.Buffer, string) {
	t.Helper()
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, err := zw.Create("app.R")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("library(shiny)\nshinyApp(fluidPage(), function(input, output) {})\n")); err != nil {
		t.Fatal(err)
	}
	// Store (not deflate) so the filler keeps its size inside the archive.
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: "filler.bin", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	filler := make([]byte, size)
	for i := range filler {
		filler[i] = byte(i*7 + i>>8)
	}
	if _, err := fw.Write(filler); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(zipBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	if body.Len() <= size {
		t.Fatalf("upload body is %d bytes, want more than %d", body.Len(), size)
	}
	return &body, mw.FormDataContentType()
}

// TestAPIChainDraftUploadExceedsJSONBodyCap proves a draft bundle larger than
// the global JSON body cap reaches the drafts handler through the production
// /api/ middleware chain. The handler applies its own bundle-size limit, so the
// outer 4 MiB cap must step aside exactly as it does for deploy.
func TestAPIChainDraftUploadExceedsJSONBodyCap(t *testing.T) {
	h, token := newAPIChainFixture(t, "bigdraft")
	body, ctype := bundleUploadBody(t, maxAPIJSONBody+(1<<20))
	size := body.Len()

	req := httptest.NewRequest(http.MethodPost, "/api/apps/bigdraft/drafts", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST drafts with %d-byte upload through the /api chain: %d %s", size, rec.Code, rec.Body.String())
	}
}
