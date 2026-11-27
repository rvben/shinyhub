package api

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// A conditional DELETE that queued behind a deploy is checked against the app
// as the deploy left it: an If-Content-Digest naming the old bundle must not
// delete the new one.
func TestDeleteApp_PreconditionCheckedAfterQueuedDeploy(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	app := createGenerationTestApp(t, store, "queued", 1, 16)
	promoteDigest(t, store, app.ID, "v1", "sha256:old")

	release := srv.acquireDeployLock("queued")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := tombstoneRequest(t, http.MethodDelete, "/api/apps/queued", nil, token)
		req.Header.Set(hdrIfContentDigest, "sha256:old")
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, req)
		done <- rr
	}()
	// Wait until the DELETE is queued on the lock, which it reaches only
	// after authorization has read the app. The digest changes after that
	// read, so the request holds a stale snapshot.
	waitForGoroutineIn(t, "(*Server).handleDeleteApp", "(*Server).acquireDeployLock")

	promoteDigest(t, store, app.ID, "v2", "sha256:new")
	release()

	rr := <-done
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale conditional DELETE = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := store.GetAppBySlug("queued"); err != nil {
		t.Fatalf("stale conditional DELETE removed the redeployed app: %v", err)
	}
}

// promoteDigest records a succeeded deployment serving digest, which becomes
// the app's content_digest.
func promoteDigest(t *testing.T, store *db.Store, appID int64, version, digest string) {
	t.Helper()
	dep, err := store.BeginDeployment(appID, version, "/tmp/tombstone-bundle-"+version)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeploymentDigest(dep.ID, digest); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
}

// waitForGoroutineIn blocks until some goroutine's stack contains every one of
// frames, or fails the test after five seconds.
func waitForGoroutineIn(t *testing.T, frames ...string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stacks := string(buf[:runtime.Stack(buf, true)])
		for _, g := range strings.Split(stacks, "\n\n") {
			found := true
			for _, f := range frames {
				if !strings.Contains(g, f) {
					found = false
					break
				}
			}
			if found {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no goroutine reached %v", frames)
}
