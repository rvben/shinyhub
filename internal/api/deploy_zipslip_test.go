package api_test

import (
	"archive/zip"
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// TestDeployApp_ZipSlipRejectedAs422 pins the status code for a bundle whose
// entry name escapes the extraction directory.
//
// The attack is blocked either way: the entry is never written. What is being
// asserted here is the classification. A 500 tells the uploader the server
// broke and invites them to retry an upload that can never succeed, and it puts
// a rejected bundle in the same bucket as a full disk for anyone reading logs
// or metrics. The refusal is a statement about their bundle, so it belongs in
// the 4xx range, alongside the symlink and data-directory rejections that
// already answer 422.
func TestDeployApp_ZipSlipRejectedAs422(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")
	createApp(t, srv, token, "demo")

	// app.R first so the bundle looks legitimate up to the offending entry.
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for _, name := range []string{"app.R", "../escape.txt"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("bundle", "bundle.zip")
	if _, err := part.Write(zipBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/apps/demo/deploy", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (the bundle is bad, not the server); body = %s", rr.Code, rr.Body.String())
	}

	// A 422 for the wrong reason would be a false pass: the response has to name
	// the escape, not some other rejection the entry happens to also trip.
	if got := rr.Body.String(); !strings.Contains(got, "escapes the destination directory") {
		t.Errorf("body = %s, want it to name the directory escape", got)
	}
}
