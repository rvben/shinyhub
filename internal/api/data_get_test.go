package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// dataGetReq builds a GET /api/apps/{slug}/data/{rel} request.
func dataGetReq(t *testing.T, slug, rel, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/apps/"+slug+"/data/"+rel, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// seedDataFile writes a file directly into the app's data dir, bypassing the
// API, so a download test is not resting on the upload path being correct.
func seedDataFile(t *testing.T, dataDir, slug, rel string, content []byte) {
	t.Helper()
	full := filepath.Join(dataDir, slug, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
}

// TestDataGet_ReturnsExactBytes is the whole point of the endpoint: after a
// restore an operator can see from the listing that a file has the right name
// and size, and only the bytes tell them the file itself came back. A download
// that is off by a byte, or that has been re-encoded on the way out, would
// answer that question wrongly, so this compares the digest rather than the
// length.
func TestDataGet_ReturnsExactBytes(t *testing.T) {
	appsDir, dataDir := t.TempDir(), t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)
	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	// Deliberately not plain ASCII: a NUL and a high byte catch a handler that
	// treats the payload as text somewhere along the way.
	content := []byte("id,value\n1,\x00\xffbin\n")
	seedDataFile(t, dataDir, "demo", "seed.csv", content)

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, dataGetReq(t, "demo", "seed.csv", token))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	got := rr.Body.Bytes()
	wantSum := sha256.Sum256(content)
	gotSum := sha256.Sum256(got)
	if hex.EncodeToString(gotSum[:]) != hex.EncodeToString(wantSum[:]) {
		t.Errorf("body digest = %s (%d bytes), want %s (%d bytes): the download is not the stored file",
			hex.EncodeToString(gotSum[:]), len(got), hex.EncodeToString(wantSum[:]), len(content))
	}
}

// TestDataGet_ServesAsOpaqueAttachment pins the headers that stop stored data
// from executing in the dashboard's own origin. These bytes are whatever a
// developer chose to upload; served as sniffable content from this origin, an
// uploaded .html or .svg would run as the dashboard.
func TestDataGet_ServesAsOpaqueAttachment(t *testing.T) {
	appsDir, dataDir := t.TempDir(), t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)
	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	seedDataFile(t, dataDir, "demo", "report.html", []byte(`<script>alert(1)</script>`))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, dataGetReq(t, "demo", "report.html", token))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream: an uploaded document must not be served as a renderable type from this origin", ct)
	}
	if nos := rr.Header().Get("X-Content-Type-Options"); nos != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff: without it the browser may sniff the octet-stream back into HTML", nos)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q, want an attachment disposition", cd)
	}
}

// TestDataGet_ContentDispositionEscapesQuotes covers a filename that would
// otherwise close the header's quoted parameter early and let the rest of the
// name be read as further header syntax.
func TestDataGet_ContentDispositionEscapesQuotes(t *testing.T) {
	appsDir, dataDir := t.TempDir(), t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)
	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	const name = `we"ird.csv`
	seedDataFile(t, dataDir, "demo", name, []byte("x"))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, dataGetReq(t, "demo", "we%22ird.csv", token))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `\"`) {
		t.Errorf("Content-Disposition = %q, want the embedded quote backslash-escaped so it cannot end the parameter", cd)
	}
}

// TestDataGet_ViewerDeniedContentsButAllowedListing is the access boundary this
// endpoint had to choose. The listing deliberately admits an explicit viewer,
// because names and sizes are inventory. Contents are the data itself and sit
// with the permission that can also overwrite it. Both halves are asserted here
// so a future change that "aligns" the two by loosening the download is a
// failure rather than a silent widening of who can read an app's data.
func TestDataGet_ViewerDeniedContentsButAllowedListing(t *testing.T) {
	appsDir, dataDir := t.TempDir(), t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)
	seedOwnerAndApp(t, store, "owner", "demo")
	seedDataFile(t, dataDir, "demo", "seed.csv", []byte("secret"))

	viewer, viewerToken := seedVisitor(t, store, "viewer")
	if err := store.GrantAppAccess("demo", viewer.ID); err != nil {
		t.Fatalf("GrantAppAccess: %v", err)
	}

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, dataListReq(t, "demo", viewerToken))
	if rr.Code != http.StatusOK {
		t.Fatalf("listing: expected 200 for an explicit viewer, got %d; the fixture is wrong, not the boundary", rr.Code)
	}

	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, dataGetReq(t, "demo", "seed.csv", viewerToken))
	if rr.Code != http.StatusForbidden {
		t.Errorf("download: got %d, want 403; a viewer may see that a file exists but not read what is in it", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "secret") {
		t.Error("the refused response contains the file's contents")
	}
}

// TestDataGet_UnauthenticatedDenied is the outer bound: the checks above all
// use some authenticated principal, so none of them would notice a route that
// had been registered outside the authenticated group entirely.
func TestDataGet_UnauthenticatedDenied(t *testing.T) {
	appsDir, dataDir := t.TempDir(), t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)
	seedOwnerAndApp(t, store, "owner", "demo")
	seedDataFile(t, dataDir, "demo", "seed.csv", []byte("secret"))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, dataGetReq(t, "demo", "seed.csv", ""))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401 for an unauthenticated download", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "secret") {
		t.Error("the refused response contains the file's contents")
	}
}

// TestDataGet_RejectsTraversalAndMissing covers the paths that must not resolve
// to a file. The percent-encoded traversal matters most: it is decoded by the
// handler before sanitization, so a handler that sanitized first would let it
// through as one opaque segment.
func TestDataGet_RejectsTraversalAndMissing(t *testing.T) {
	appsDir, dataDir := t.TempDir(), t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)
	_, token := seedOwnerAndApp(t, store, "owner", "demo")

	// A file one level above the app's data dir, which traversal would reach.
	if err := os.WriteFile(filepath.Join(dataDir, "outside.txt"), []byte("not yours"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	seedDataFile(t, dataDir, "demo", "sub/inner.csv", []byte("ok"))

	for _, tc := range []struct {
		name string
		rel  string
		want int
	}{
		{"encoded traversal", "..%2Foutside.txt", http.StatusBadRequest},
		{"missing file", "nope.csv", http.StatusNotFound},
		{"directory", "sub", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			srv.Router().ServeHTTP(rr, dataGetReq(t, "demo", tc.rel, token))
			if rr.Code != tc.want {
				t.Errorf("got %d, want %d (body: %s)", rr.Code, tc.want, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "not yours") {
				t.Error("the response contains the contents of a file outside the app's data dir")
			}
		})
	}
}

// TestDataGet_NestedPathResolves is the positive control for the case above: a
// legitimate nested path has to keep working, or "traversal is rejected" could
// be satisfied by a handler that rejects every path containing a slash.
func TestDataGet_NestedPathResolves(t *testing.T) {
	appsDir, dataDir := t.TempDir(), t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)
	_, token := seedOwnerAndApp(t, store, "owner", "demo")
	seedDataFile(t, dataDir, "demo", "datasets/2026/q1.csv", []byte("nested ok"))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, dataGetReq(t, "demo", "datasets/2026/q1.csv", token))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for a nested path, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "nested ok" {
		t.Errorf("body = %q, want %q", rr.Body.String(), "nested ok")
	}
}

// TestDataGet_AuditsTheRead pins the one thing a read does change. A download
// discloses an app's data, and the audit trail exists to answer who saw it, not
// only who altered it.
func TestDataGet_AuditsTheRead(t *testing.T) {
	appsDir, dataDir := t.TempDir(), t.TempDir()
	srv, store := newDataTestServer(t, appsDir, dataDir, 0)
	_, token := seedOwnerAndApp(t, store, "owner", "demo")
	seedDataFile(t, dataDir, "demo", "seed.csv", []byte("hello"))

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, dataGetReq(t, "demo", "seed.csv", token))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	events, err := store.ListAuditEvents(db.AuditDataPull, 50, 0)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d %s audit events for one download, want exactly 1", len(events), db.AuditDataPull)
	}
	found := events[0]
	if found.ResourceID != "demo" {
		t.Errorf("audit resource = %q, want the app slug", found.ResourceID)
	}
	if !strings.Contains(found.Detail, "seed.csv") {
		t.Errorf("audit detail = %q, want it to name the file that was read", found.Detail)
	}
}
