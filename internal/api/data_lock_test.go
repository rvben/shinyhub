package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// newDataLockTestServer is a minimal in-package server setup that exercises
// handleDataPut so the test can inspect the private dataLocks map.
func newDataLockTestServer(t *testing.T, quotaMB int) (*Server, string) {
	t.Helper()
	appsDir := t.TempDir()
	dataDir := t.TempDir()

	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := &config.Config{
		Auth: config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{
			AppsDir:    appsDir,
			AppDataDir: dataDir,
			AppQuotaMB: quotaMB,
		},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	prx := proxy.New()
	srv := New(cfg, store, mgr, prx)

	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "demo", OwnerID: u.ID}); err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	token, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return srv, token
}

func putOnce(t *testing.T, srv *Server, token, rel string, body []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/apps/demo/data/"+rel, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.ContentLength = int64(len(body))
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT %s expected 200, got %d: %s", rel, rr.Code, rr.Body.String())
	}
}

// TestDataPut_QuotaDisabled_DoesNotAcquirePerSlugLock proves that when
// AppQuotaMB == 0 the per-slug data lock is never taken. The dataLocks map
// is populated lazily by dataLockFor; an entry surviving after a successful
// PUT is direct evidence that the lock was acquired. With quotas disabled
// there is nothing to serialize, so concurrent uploads to the same slug must
// run in parallel — gated only by the OS file system, not by us.
func TestDataPut_QuotaDisabled_DoesNotAcquirePerSlugLock(t *testing.T) {
	srv, token := newDataLockTestServer(t, 0)

	putOnce(t, srv, token, "seed.txt", []byte("hello world"))

	srv.dataLocksMu.Lock()
	_, exists := srv.dataLocks["demo"]
	srv.dataLocksMu.Unlock()
	if exists {
		t.Fatal("per-slug data lock created with quotas disabled — handleDataPut must skip the lock when AppQuotaMB == 0")
	}
}

// TestDataPut_QuotaEnabled_AcquiresPerSlugLock is the positive counterpart:
// with quotas active the lock MUST be acquired so the quota-check + write
// phase stays serialized. This guards against an over-eager fix that drops
// the lock unconditionally.
func TestDataPut_QuotaEnabled_AcquiresPerSlugLock(t *testing.T) {
	srv, token := newDataLockTestServer(t, 5) // 5 MiB cap

	putOnce(t, srv, token, "seed.txt", []byte("hello world"))

	srv.dataLocksMu.Lock()
	_, exists := srv.dataLocks["demo"]
	srv.dataLocksMu.Unlock()
	if !exists {
		t.Fatal("per-slug data lock missing with quotas enabled — quota-check + write must be serialized")
	}
}
