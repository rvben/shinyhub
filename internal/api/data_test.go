package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// newDataTestServer wires a Server with a custom appsDir, appDataDir, and
// quota so data-push tests can control all three config knobs independently.
func newDataTestServer(t *testing.T, appsDir, appDataDir string, quotaMB int) (*api.Server, *db.Store) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth: config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{
			AppsDir:    appsDir,
			AppDataDir: appDataDir,
			AppQuotaMB: quotaMB,
		},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	prx := proxy.New()
	srv := api.New(cfg, store, mgr, prx)
	t.Cleanup(func() { _ = store.Close() })
	return srv, store
}

// seedOwnerAndApp creates a user and an app owned by that user. It returns the
// user and a JWT token that can be used in requests.
func seedOwnerAndApp(t *testing.T, store *db.Store, username, slug string) (*db.User, string) {
	t.Helper()
	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: username, PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u, err := store.GetUserByUsername(username)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: u.ID}); err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	token, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return u, token
}

// dataPutReq builds a PUT /api/apps/{slug}/data/{rel} request with the given body.
func dataPutReq(t *testing.T, slug, rel string, body []byte, token string) *http.Request {
	t.Helper()
	path := "/api/apps/" + slug + "/data/" + rel
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.ContentLength = int64(len(body))
	return req
}

// TestDataPut_HappyPath verifies that an owner can push a file, the response
// JSON is correct, and the file lands on disk with the expected content.
func TestDataPut_HappyPath(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	content := []byte("hello world")
	req := dataPutReq(t, "demo", "seed.txt", content, token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Path      string `json:"path"`
		Size      int64  `json:"size"`
		SHA256    string `json:"sha256"`
		Restarted bool   `json:"restarted"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Path != "seed.txt" {
		t.Errorf("path = %q, want %q", resp.Path, "seed.txt")
	}
	if resp.Size != int64(len(content)) {
		t.Errorf("size = %d, want %d", resp.Size, len(content))
	}
	if resp.SHA256 == "" {
		t.Error("sha256 should not be empty")
	}
	if resp.Restarted {
		t.Error("restarted should be false (app is stopped)")
	}

	// Verify file is on disk.
	dest := filepath.Join(dataDir, "demo", "seed.txt")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read file on disk: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("disk content = %q, want %q", got, content)
	}
}

// TestDataPut_MissingContentLength verifies that chunked requests (no known
// Content-Length) are rejected with 411 Length Required.
func TestDataPut_MissingContentLength(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	req := httptest.NewRequest(http.MethodPut, "/api/apps/demo/data/file.txt",
		strings.NewReader("some content"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusLengthRequired {
		t.Fatalf("expected 411, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDataPut_PathTraversal verifies that a path containing ".." is rejected
// with 400 Bad Request.
func TestDataPut_PathTraversal(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	// Use URL-encoded traversal attempt.
	req := httptest.NewRequest(http.MethodPut,
		"/api/apps/demo/data/..%2Fetc%2Fpasswd",
		strings.NewReader("bad"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.ContentLength = 3

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDataPut_ReservedPrefix verifies that paths starting with ".shinyhub-"
// are rejected with 400 Bad Request.
func TestDataPut_ReservedPrefix(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	req := httptest.NewRequest(http.MethodPut,
		"/api/apps/demo/data/.shinyhub-evil",
		strings.NewReader("bad"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.ContentLength = 3

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDataPut_QuotaOverwriteAware exercises the overwrite-aware quota logic:
//   - a.bin (900 KiB) → 200 OK
//   - b.bin (200 KiB) → 413 (would exceed 1 MiB cap)
//   - overwrite a.bin with 50 KiB → 200 OK (replaces existing, stays under cap)
func TestDataPut_QuotaOverwriteAware(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 1) // 1 MiB cap

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	send := func(t *testing.T, rel string, sizeBytes int) int {
		t.Helper()
		body := bytes.Repeat([]byte("x"), sizeBytes)
		req := dataPutReq(t, "demo", rel, body, token)
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, req)
		return rr.Code
	}

	const KiB = 1024

	// First write: 900 KiB → should succeed.
	if code := send(t, "a.bin", 900*KiB); code != http.StatusOK {
		t.Fatalf("a.bin (900 KiB): expected 200, got %d", code)
	}

	// Second write: 200 KiB → 900+200=1100 KiB > 1024 KiB quota → should fail.
	if code := send(t, "b.bin", 200*KiB); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("b.bin (200 KiB): expected 413, got %d", code)
	}

	// Overwrite a.bin with 50 KiB → (900-900)+50=50 KiB → should succeed.
	if code := send(t, "a.bin", 50*KiB); code != http.StatusOK {
		t.Fatalf("overwrite a.bin (50 KiB): expected 200, got %d", code)
	}
}

// TestDataPut_QuotaConcurrentWritesDoNotExceed exercises the per-slug quota
// race: without serialization, N concurrent uploads each read used_bytes
// before any of them have written, every one passes its quota check, and the
// final on-disk usage blows past the cap. With the per-slug data lock the
// reads and writes are serialized so at least one upload is rejected with
// 413 and the on-disk total stays within the quota.
func TestDataPut_QuotaConcurrentWritesDoNotExceed(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 1) // 1 MiB cap

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	const KiB = 1024
	const concurrency = 8
	const writeSize = 200 * KiB // 8 * 200 KiB = 1600 KiB > 1 MiB cap

	var wg sync.WaitGroup
	codes := make([]int, concurrency)
	body := bytes.Repeat([]byte("x"), writeSize)
	for i := 0; i < concurrency; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := dataPutReq(t, "demo", fmt.Sprintf("a%d.bin", i), body, token)
			rr := httptest.NewRecorder()
			srv.Router().ServeHTTP(rr, req)
			codes[i] = rr.Code
		}()
	}
	wg.Wait()

	// At least one upload must have been rejected — 8 * 200 KiB = 1600 KiB
	// exceeds the 1 MiB cap, so the quota gate must reject some writes.
	rejected := 0
	for _, c := range codes {
		if c == http.StatusRequestEntityTooLarge {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatalf("expected at least one 413 with %d concurrent writes summing %d KiB > 1 MiB; got codes=%v",
			concurrency, concurrency*writeSize/KiB, codes)
	}

	// Final on-disk usage must respect the 1 MiB cap.
	used, err := deploy.DirSize(filepath.Join(dataDir, "demo"))
	if err != nil {
		t.Fatalf("measure dataDir: %v", err)
	}
	const quotaBytes = 1 << 20
	if used > quotaBytes {
		t.Fatalf("on-disk usage %d bytes exceeds quota %d bytes; codes=%v",
			used, quotaBytes, codes)
	}
}

// TestDataPut_StrangerRejected verifies that a non-owner non-member receives
// 404 (requireManageApp calls requireViewApp which returns 404 for strangers,
// to avoid leaking slug existence).
func TestDataPut_StrangerRejected(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	// Create the app owned by "owner".
	seedOwnerAndApp(t, store, "owner", "demo")

	// Create a separate user "stranger" with no access.
	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "stranger", PasswordHash: hash, Role: "developer"})
	stranger, _ := store.GetUserByUsername("stranger")
	strangerToken, _ := auth.IssueJWT(stranger.ID, stranger.Username, stranger.Role, "test-secret")

	req := dataPutReq(t, "demo", "file.txt", []byte("data"), strangerToken)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	// requireManageApp → requireViewApp returns 404 for strangers on
	// non-public/non-shared apps, to avoid leaking slug existence.
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDataPut_AuditRecorded verifies that a successful push produces exactly
// one "data.push" audit event whose Detail JSON contains the expected path.
func TestDataPut_AuditRecorded(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	content := []byte("hello world")
	req := dataPutReq(t, "demo", "seed.txt", content, token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	events, err := store.ListAuditEvents(10, 0)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}

	var found []db.AuditEvent
	for _, e := range events {
		if e.Action == db.AuditDataPush {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 data.push audit event, got %d", len(found))
	}

	var detail struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(found[0].Detail), &detail); err != nil {
		t.Fatalf("parse audit detail JSON: %v", err)
	}
	if detail.Path != "seed.txt" {
		t.Errorf("audit detail path = %q, want %q", detail.Path, "seed.txt")
	}
}

// TestDataPut_ZeroContentLength verifies that Content-Length: 0 is rejected
// with 411 Length Required.
func TestDataPut_ZeroContentLength(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	req := httptest.NewRequest(http.MethodPut, "/api/apps/demo/data/file.txt", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	req.ContentLength = 0

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusLengthRequired {
		t.Fatalf("expected 411, got %d: %s", rr.Code, rr.Body.String())
	}
}

// dataListReq builds a GET /api/apps/{slug}/data request.
func dataListReq(t *testing.T, slug, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/apps/"+slug+"/data", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// seedVisitor creates an unprivileged user with no app memberships and returns
// a valid JWT for that user.
func seedVisitor(t *testing.T, store *db.Store, username string) (*db.User, string) {
	t.Helper()
	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: username, PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u, err := store.GetUserByUsername(username)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	token, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return u, token
}

// TestDataList_OwnerSeesEnvelope verifies that the app owner gets a 200 with a
// populated files list, correct quota_mb, and used_bytes >= the seeded content.
func TestDataList_OwnerSeesEnvelope(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 5) // 5 MiB quota

	owner, token := seedOwnerAndApp(t, store, "owner", "demo")
	_ = owner

	// Pre-seed two files in the app data dir.
	appDataDir := filepath.Join(dataDir, "demo")
	if err := os.MkdirAll(appDataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDataDir, "a.txt"), []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDataDir, "b.txt"), []byte("world"), 0o640); err != nil {
		t.Fatal(err)
	}

	req := dataListReq(t, "demo", token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Files     []any  `json:"files"`
		QuotaMB   int    `json:"quota_mb"`
		UsedBytes int64  `json:"used_bytes"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(resp.Files))
	}
	if resp.QuotaMB != 5 {
		t.Errorf("quota_mb = %d, want 5", resp.QuotaMB)
	}
	if resp.UsedBytes < 5 {
		t.Errorf("used_bytes = %d, want >= 5", resp.UsedBytes)
	}
}

// TestDataList_PublicVisitorRejected verifies that a user with no membership is
// rejected with 404 even when the app is public, because requireExplicitAppAccess
// is stricter than requireViewApp.
func TestDataList_PublicVisitorRejected(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	seedOwnerAndApp(t, store, "owner", "demo")
	// Make the app public.
	if err := store.SetAppAccess("demo", "public"); err != nil {
		t.Fatalf("SetAppAccess: %v", err)
	}

	_, visitorToken := seedVisitor(t, store, "visitor")

	req := dataListReq(t, "demo", visitorToken)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDataList_ExplicitViewerAllowed verifies that a user with an explicit
// app_members row (viewer role) receives 200 from the list endpoint.
func TestDataList_ExplicitViewerAllowed(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	seedOwnerAndApp(t, store, "owner", "demo")

	viewer, viewerToken := seedVisitor(t, store, "viewer")

	// Grant the viewer explicit access.
	if err := store.GrantAppAccess("demo", viewer.ID); err != nil {
		t.Fatalf("GrantAppAccess: %v", err)
	}

	req := dataListReq(t, "demo", viewerToken)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDataList_MissingDataDir verifies that when the per-app data dir does not
// yet exist the handler responds 200 with an empty (non-null) files array.
func TestDataList_MissingDataDir(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	// Deliberately do NOT create <dataDir>/demo — directory is absent.

	req := dataListReq(t, "demo", token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Files json.RawMessage `json:"files"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// files must be [] not null
	if string(resp.Files) == "null" {
		t.Error("files must be an empty array [], got null")
	}
}

// TestDataList_TooManyFiles verifies that a data dir with more than
// dataListMaxEntries files results in a 422 Unprocessable Entity response.
func TestDataList_TooManyFiles(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	// Seed dataListMaxEntries+1 files.
	appDataDir := filepath.Join(dataDir, "demo")
	if err := os.MkdirAll(appDataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= 10000; i++ { // 10001 files > dataListMaxEntries (10000)
		name := filepath.Join(appDataDir, fmt.Sprintf("f%05d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0o640); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}

	req := dataListReq(t, "demo", token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rr.Code, rr.Body.String())
	}
}

// dataDeleteReq builds a DELETE /api/apps/{slug}/data/{rel} request.
func dataDeleteReq(t *testing.T, slug, rel string, token string) *http.Request {
	t.Helper()
	path := "/api/apps/" + slug + "/data/" + rel
	req := httptest.NewRequest(http.MethodDelete, path, http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// TestDataDelete_HappyPath verifies that a pre-seeded file is removed and the
// handler responds 204 No Content.
func TestDataDelete_HappyPath(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	// Pre-seed the file on disk.
	appDataDir := filepath.Join(dataDir, "demo")
	if err := os.MkdirAll(appDataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(appDataDir, "x.txt")
	if err := os.WriteFile(dest, []byte("content"), 0o640); err != nil {
		t.Fatal(err)
	}

	req := dataDeleteReq(t, "demo", "x.txt", token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("expected file to be gone after DELETE, but it still exists")
	}
}

// TestDataDelete_NotFound verifies that deleting a non-existent file returns 404.
func TestDataDelete_NotFound(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	req := dataDeleteReq(t, "demo", "does-not-exist.txt", token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDataDelete_RefusesDirectory verifies that attempting to delete a directory
// returns 400 Bad Request.
func TestDataDelete_RefusesDirectory(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	// Pre-create a subdirectory.
	subDir := filepath.Join(dataDir, "demo", "sub")
	if err := os.MkdirAll(subDir, 0o750); err != nil {
		t.Fatal(err)
	}

	req := dataDeleteReq(t, "demo", "sub", token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDataDelete_RefusesReservedPrefix verifies that paths beginning with the
// reserved ".shinyhub-" prefix are rejected with 400 Bad Request.
func TestDataDelete_RefusesReservedPrefix(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	req := dataDeleteReq(t, "demo", ".shinyhub-upload-tmp/x", token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDataDelete_AuditRecorded verifies that a successful DELETE produces
// exactly one "data.delete" audit event whose Detail JSON contains the
// expected path.
func TestDataDelete_AuditRecorded(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	// Pre-seed the file.
	appDataDir := filepath.Join(dataDir, "demo")
	if err := os.MkdirAll(appDataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDataDir, "tracked.txt"), []byte("data"), 0o640); err != nil {
		t.Fatal(err)
	}

	req := dataDeleteReq(t, "demo", "tracked.txt", token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	events, err := store.ListAuditEvents(10, 0)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}

	var found []db.AuditEvent
	for _, e := range events {
		if e.Action == db.AuditDataDelete {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 data.delete audit event, got %d", len(found))
	}

	var detail struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(found[0].Detail), &detail); err != nil {
		t.Fatalf("parse audit detail JSON: %v", err)
	}
	if detail.Path != "tracked.txt" {
		t.Errorf("audit detail path = %q, want %q", detail.Path, "tracked.txt")
	}
}

// seedRunningApp marks an existing app as running and inserts a deployment row
// so maybeRestartForChange can find a bundle directory to re-launch.
// It returns the fake PID used for the running status.
func seedRunningApp(t *testing.T, store *db.Store, slug, bundleDir string) int {
	t.Helper()
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("GetAppBySlug(%q): %v", slug, err)
	}
	_, err = store.CreateDeployment(db.CreateDeploymentParams{
		AppID:     app.ID,
		Version:   "v1",
		BundleDir: bundleDir,
	})
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	fakePID := 42
	fakePort := 20001
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
	}); err != nil {
		t.Fatalf("UpdateAppStatus: %v", err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:  app.ID,
		Index:  0,
		PID:    &fakePID,
		Port:   &fakePort,
		Status: "running",
	}); err != nil {
		t.Fatalf("UpsertReplica: %v", err)
	}
	return fakePID
}

// TestDataPut_RestartTrue_RestartsRunningApp verifies that PUT with ?restart=true
// calls the deploy hook and the response reports restarted=true with the new
// PID/port reflected in the database.
func TestDataPut_RestartTrue_RestartsRunningApp(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")
	bundleDir := t.TempDir()
	seedRunningApp(t, store, "demo", bundleDir)

	called := make(chan struct{}, 1)
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		called <- struct{}{}
		return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: 1234, Port: 9999}}}, nil
	})

	req := dataPutReq(t, "demo", "seed.txt", []byte("hi"), token)
	req.URL.RawQuery = "restart=true"
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Restarted    bool   `json:"restarted"`
		RestartError string `json:"restart_error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Restarted {
		t.Errorf("expected restarted=true, got false (restart_error=%q)", resp.RestartError)
	}

	// The deploy stub must have been called.
	select {
	case <-called:
		// good
	default:
		t.Error("deploy hook was not called")
	}

	// DB must reflect the new PID and port.
	app, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatalf("GetAppBySlug: %v", err)
	}
	if app.Status != "running" {
		t.Errorf("app.Status = %q, want %q", app.Status, "running")
	}
	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("ListReplicas: %v", err)
	}
	if len(reps) == 0 {
		t.Fatal("expected at least one replica row")
	}
	if reps[0].PID == nil || *reps[0].PID != 1234 {
		t.Errorf("replica PID = %v, want 1234", reps[0].PID)
	}
	if reps[0].Port == nil || *reps[0].Port != 9999 {
		t.Errorf("replica Port = %v, want 9999", reps[0].Port)
	}
}

// TestDataPut_RestartFailure_SurfacedInResponse verifies that when the deploy
// hook fails the response still returns 200 with restarted=false and a
// non-empty restart_error, and the app's DB status is reset to "stopped".
func TestDataPut_RestartFailure_SurfacedInResponse(t *testing.T) {
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)

	_, token := seedOwnerAndApp(t, store, "owner", "demo")
	bundleDir := t.TempDir()
	seedRunningApp(t, store, "demo", bundleDir)

	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		return nil, errors.New("boom")
	})

	req := dataPutReq(t, "demo", "seed.txt", []byte("hi"), token)
	req.URL.RawQuery = "restart=true"
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Restarted    bool   `json:"restarted"`
		RestartError string `json:"restart_error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Restarted {
		t.Error("expected restarted=false, got true")
	}
	if resp.RestartError != "boom" {
		t.Errorf("restart_error = %q, want %q", resp.RestartError, "boom")
	}

	// DB status must be "stopped" after the failed re-launch.
	app, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatalf("GetAppBySlug: %v", err)
	}
	if app.Status != "stopped" {
		t.Errorf("app.Status = %q, want %q", app.Status, "stopped")
	}
}

