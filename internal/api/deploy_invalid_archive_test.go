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

func TestDeployApp_InvalidArchiveRejectedAs422(t *testing.T) {
	var stored bytes.Buffer
	zw := zip.NewWriter(&stored)
	entry, err := zw.CreateHeader(&zip.FileHeader{Name: "app.py", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	entry.Write([]byte("unique-archive-content"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	damaged := bytes.Clone(stored.Bytes())
	damaged[bytes.Index(damaged, []byte("unique-archive-content"))] ^= 1

	for name, archive := range map[string][]byte{
		"not a ZIP":         []byte("invalid ZIP"),
		"truncated ZIP":     stored.Bytes()[:20],
		"checksum mismatch": damaged,
	} {
		t.Run(name, func(t *testing.T) {
			appsDir := t.TempDir()
			srv, store := newQuotaTestServer(t, appsDir, 0)
			hash, _ := testHashPassword("pass")
			store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
			token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")
			createApp(t, srv, token, "demo")
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			part, _ := mw.CreateFormFile("bundle", "bundle.zip")
			part.Write(archive)
			mw.Close()
			req := httptest.NewRequest(http.MethodPost, "/api/apps/demo/deploy", &body)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			srv.Router().ServeHTTP(rr, req)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "valid ZIP bundle") {
				t.Fatalf("missing recovery guidance: %s", rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), appsDir) {
				t.Fatalf("host path leaked: %s", rr.Body.String())
			}
		})
	}
}
