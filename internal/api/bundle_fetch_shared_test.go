package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/worker"
)

// TestBundleFetchCrossInstanceSharedAppsDir models two coordinator instances
// sharing a common apps_dir (e.g. mounted network storage). "Instance A" writes
// the bundle zip and records the deployment; "Instance B" is a WorkerAPI
// constructed against the same store and the same apps_dir. A worker connecting
// to instance B can fetch the bundle by content digest with no dependency on
// which instance originally wrote the file.
func TestBundleFetchCrossInstanceSharedAppsDir(t *testing.T) {
	// Shared state: one store (same DB) and one directory (same filesystem path).
	sharedStore := dbtest.New(t)
	sharedAppsDir := t.TempDir()

	// --- Instance A: seed the bundle ---
	// Create the CA and registry for instance A, then seed the DB and write the zip.
	caA, err := worker.OpenCA(t.TempDir(), []string{"token-a"})
	if err != nil {
		t.Fatalf("ca A: %v", err)
	}
	regA, err := worker.NewRegistry(sharedStore)
	if err != nil {
		t.Fatalf("registry A: %v", err)
	}
	instanceA := NewWorkerAPI(sharedStore, regA, caA, sharedAppsDir)

	if err := instanceA.store.CreateUser(db.CreateUserParams{
		Username: "owner-shared", PasswordHash: "h", Role: "developer",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, err := instanceA.store.GetUserByUsername("owner-shared")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := instanceA.store.CreateApp(db.CreateAppParams{
		Slug:    "shared-app",
		Name:    "Shared App",
		OwnerID: owner.ID,
		Access:  "private",
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := instanceA.store.GetAppBySlug("shared-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	dep, err := instanceA.store.BeginDeployment(app.ID, "v1", "/bundles/shared-app/v1")
	if err != nil {
		t.Fatalf("begin deployment: %v", err)
	}
	const digest = "sha256:shared-cross-instance"
	if err := instanceA.store.SetDeploymentDigest(dep.ID, digest); err != nil {
		t.Fatalf("set digest: %v", err)
	}
	if err := instanceA.store.PromoteDeployment(dep.ID); err != nil {
		t.Fatalf("promote deployment: %v", err)
	}

	// Write the zip under the shared apps_dir as instance A would.
	zipDir := filepath.Join(sharedAppsDir, "shared-app", "bundles")
	if err := os.MkdirAll(zipDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	wantBytes := []byte("PK\x03\x04 shared-instance-zip-bytes")
	if err := os.WriteFile(filepath.Join(zipDir, "v1.zip"), wantBytes, 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	// --- Instance B: fetch the bundle ---
	// A separate WorkerAPI (different CA, different registry in-process) pointed
	// at the SAME store and SAME apps_dir. This is the cross-instance case: the
	// worker connects to a node that did not write the file.
	caB, err := worker.OpenCA(t.TempDir(), []string{"token-b"})
	if err != nil {
		t.Fatalf("ca B: %v", err)
	}
	regB, err := worker.NewRegistry(sharedStore)
	if err != nil {
		t.Fatalf("registry B: %v", err)
	}
	instanceB := NewWorkerAPI(sharedStore, regB, caB, sharedAppsDir)

	req := httptest.NewRequest(http.MethodGet, "/internal/bundles/"+digest, nil)
	req = withWorkerCert(t, req, instanceB, "node-instance-b")
	req = withURLParam(req, "digest", digest)
	w := httptest.NewRecorder()
	instanceB.HandleBundleFetch(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("instance B bundle fetch: status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), wantBytes) {
		t.Fatalf("instance B bundle fetch: body mismatch: got %q, want %q", w.Body.Bytes(), wantBytes)
	}
}
