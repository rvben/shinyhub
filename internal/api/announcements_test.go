package api_test

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

func announcementRequest(t *testing.T, s *api.Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	if token != "" {
		scheme := "Bearer "
		if strings.HasPrefix(token, "shk_") {
			scheme = "Token "
		}
		r.Header.Set("Authorization", scheme+token)
	}
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, r)
	return w
}
func decodeAnnouncement(t *testing.T, w *httptest.ResponseRecorder) db.Announcement {
	t.Helper()
	var a db.Announcement
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}
func TestAnnouncementsLifecycleAndPublicPrivacy(t *testing.T) {
	s, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "notice-admin", "admin")
	req := map[string]any{"title": "Maintenance", "message": "Save your work.", "severity": "warning"}
	w := announcementRequest(t, s, "POST", "/api/announcements", token, req)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	a := decodeAnnouncement(t, w)
	feed := func() string {
		w := announcementRequest(t, s, "GET", "/api/announcements/active", "", nil)
		if w.Code != 200 {
			t.Fatalf("feed: %d %s", w.Code, w.Body)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable feed")
		}
		return w.Body.String()
	}
	if strings.Contains(feed(), "Maintenance") {
		t.Fatal("draft leaked")
	}
	start := time.Now().UTC().Add(time.Hour)
	w = announcementRequest(t, s, "PATCH", "/api/announcements/"+a.ID, token, map[string]any{"expected_revision": a.Revision, "publication": "published", "starts_at": start})
	if w.Code != 200 {
		t.Fatalf("schedule: %d %s", w.Code, w.Body)
	}
	a = decodeAnnouncement(t, w)
	if strings.Contains(feed(), "Maintenance") {
		t.Fatal("future notice leaked")
	}
	if !strings.Contains(feed(), "next_transition") {
		t.Fatal("missing boundary")
	}
	start = time.Now().UTC().Add(-time.Minute)
	w = announcementRequest(t, s, "PATCH", "/api/announcements/"+a.ID, token, map[string]any{"expected_revision": a.Revision, "starts_at": start})
	if w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body)
	}
	a = decodeAnnouncement(t, w)
	public := feed()
	if !strings.Contains(public, "Maintenance") {
		t.Fatal("notice missing")
	}
	for _, key := range []string{"created_by", "updated_by", "publication", "published_at", "notice-admin"} {
		if strings.Contains(public, key) {
			t.Fatalf("public field leaked: %s", key)
		}
	}
	w = announcementRequest(t, s, "PATCH", "/api/announcements/"+a.ID, token, map[string]any{"expected_revision": a.Revision, "message": "Maintenance begins soon."})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	edited := decodeAnnouncement(t, w)
	if edited.DisplayRevision <= a.DisplayRevision {
		t.Fatal("dismissal revision did not advance")
	}
	w = announcementRequest(t, s, "PATCH", "/api/announcements/"+a.ID, token, map[string]any{"expected_revision": a.Revision, "message": "Stale edit"})
	if w.Code != 409 {
		t.Fatalf("stale edit: %d", w.Code)
	}
	w = announcementRequest(t, s, "PATCH", "/api/announcements/"+a.ID, token, map[string]any{"expected_revision": edited.Revision, "publication": "disabled"})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	if strings.Contains(feed(), "Maintenance") {
		t.Fatal("withdrawal stayed cached")
	}
	events, err := store.ListAuditEvents("", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.ResourceType == "announcement" {
			count++
		}
	}
	if count != 5 {
		t.Fatalf("expected 5 successful mutation audits, got %d", count)
	}
}
func TestAnnouncementsPermissionsAndValidation(t *testing.T) {
	s, store := newTestServer(t)
	admin, _ := seedUserAndJWT(t, store, "notice-admin", "admin")
	for _, role := range []string{"viewer", "developer", "operator"} {
		token, _ := seedUserAndJWT(t, store, "notice-"+role, role)
		for _, method := range []string{"GET", "POST"} {
			w := announcementRequest(t, s, method, "/api/announcements", token, map[string]any{})
			if w.Code != 403 {
				t.Fatalf("%s %s: %d", role, method, w.Code)
			}
		}
	}
	account, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.CreateAPIKey(db.CreateAPIKeyParams{UserID: account.ID, KeyHash: auth.HashAPIKey("shk_notice_scoped_key"), Name: "scoped", CredentialType: "service", CredentialRole: "admin", AppScope: []string{"sales"}, Unrestricted: false})
	if err != nil {
		t.Fatal(err)
	}
	if w := announcementRequest(t, s, "POST", "/api/announcements", "shk_notice_scoped_key", map[string]any{}); w.Code != 403 {
		t.Fatalf("scoped admin escaped: %d %s", w.Code, w.Body)
	}
	for _, change := range []map[string]any{{"details_url": "javascript:alert(1)"}, {"details_url": "https://user:secret@example.com/"}, {"severity": "urgent"}, {"message": strings.Repeat("x", 601)}, {"title": " "}, {"starts_at": "2026-10-03T22:00:00"}, {"starts_at": "2026-10-03T22:00:00Z", "ends_at": "2026-10-03T21:00:00Z"}, {"unknown": true}} {
		body := map[string]any{"title": "Notice", "message": "Details"}
		for k, v := range change {
			body[k] = v
		}
		w := announcementRequest(t, s, "POST", "/api/announcements", admin, body)
		if w.Code != 400 {
			t.Fatalf("%v: %d %s", change, w.Code, w.Body)
		}
	}
	w := announcementRequest(t, s, "POST", "/api/announcements", admin, map[string]any{"title": "Critical", "message": "Save work", "severity": "critical", "publication": "published"})
	if w.Code != 201 {
		t.Fatal(w.Body)
	}
	if decodeAnnouncement(t, w).Dismissible {
		t.Fatal("critical defaults to dismissible")
	}
}
func TestAnnouncementCacheAcrossServersAndScheduleBoundary(t *testing.T) {
	s, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "notice-admin", "admin")
	w := announcementRequest(t, s, "POST", "/api/announcements", token, map[string]any{"title": "Active", "message": "Details", "publication": "published"})
	if w.Code != 201 {
		t.Fatal(w.Body)
	}
	// A new server reads durable publications without an activation job.
	second := api.New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}, Storage: config.StorageConfig{AppsDir: t.TempDir()}}, store, nil, nil)
	w = announcementRequest(t, second, "GET", "/api/announcements/active", "", nil)
	if !strings.Contains(w.Body.String(), "Active") {
		t.Fatal(w.Body)
	}
	// A scheduled transition cuts through the normal five-second cache lifetime.
	at := time.Now().UTC().Add(300 * time.Millisecond)
	w = announcementRequest(t, s, "POST", "/api/announcements", token, map[string]any{"title": "Boundary", "message": "Details", "publication": "published", "starts_at": at})
	if w.Code != 201 {
		t.Fatal(w.Body)
	}
	w = announcementRequest(t, s, "GET", "/api/announcements/active", "", nil)
	if strings.Contains(w.Body.String(), "Boundary") {
		t.Fatal("started early")
	}
	time.Sleep(350 * time.Millisecond)
	w = announcementRequest(t, s, "GET", "/api/announcements/active", "", nil)
	if !strings.Contains(w.Body.String(), "Boundary") {
		t.Fatal("boundary pinned by cache")
	}
}
