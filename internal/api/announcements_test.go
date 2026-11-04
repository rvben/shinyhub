package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
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
		if strings.HasPrefix(token, "shk_") || strings.HasPrefix(token, auth.LocalTokenPrefix) {
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

// keyedAnnouncementRequest deliberately preserves exact bytes between retries.
func keyedAnnouncementRequest(s *api.Server, method, path, token, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	scheme := "Bearer "
	if strings.HasPrefix(token, "shk_") || strings.HasPrefix(token, auth.LocalTokenPrefix) {
		scheme = "Token "
	}
	r.Header.Set("Authorization", scheme+token)
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, r)
	return w
}

func TestAnnouncementIdempotentCreateAndUpdate(t *testing.T) {
	s, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "receipt-admin", "admin")
	body := `{"title":"Maintenance","message":"Save work","publication":"published"}`
	first := keyedAnnouncementRequest(s, "POST", "/api/announcements", token, "create-once", body)
	if first.Code != 201 {
		t.Fatalf("create: %d %s", first.Code, first.Body)
	}
	a := decodeAnnouncement(t, first)
	// A fresh server has no process-local state from the response that was lost.
	restarted := api.New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}, Storage: config.StorageConfig{AppsDir: t.TempDir()}}, store, nil, nil)
	replay := keyedAnnouncementRequest(restarted, "POST", "/api/announcements", token, "create-once", body)
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("create replay: %d %s; original %s", replay.Code, replay.Body, first.Body)
	}
	path := "/api/announcements/" + a.ID
	patch := fmt.Sprintf(`{"expected_revision":%d,"message":"Updated instructions"}`, a.Revision)
	updated := keyedAnnouncementRequest(s, "PATCH", path, token, "update-once", patch)
	if updated.Code != 200 {
		t.Fatalf("update: %d %s", updated.Code, updated.Body)
	}
	// Advance again so a replay must precede both stale-revision and lifecycle
	// checks, and must return its original snapshot rather than the current row.
	latest := decodeAnnouncement(t, updated)
	archived := announcementRequest(t, s, "PATCH", path, token, map[string]any{"expected_revision": latest.Revision, "publication": "archived"})
	if archived.Code != 200 {
		t.Fatal(archived.Body)
	}
	replay = keyedAnnouncementRequest(restarted, "PATCH", path, token, "update-once", patch)
	if replay.Code != 200 || replay.Body.String() != updated.Body.String() {
		t.Fatalf("stale update replay: %d %s; original %s", replay.Code, replay.Body, updated.Body)
	}
	for _, changed := range []string{strings.Replace(patch, "Updated instructions", "Different instructions", 1), `{"message":"Missing revision"}`, `{"unknown":true}`} {
		conflict := keyedAnnouncementRequest(s, "PATCH", path, token, "update-once", changed)
		if conflict.Code != 409 || !strings.Contains(conflict.Body.String(), `"code":"idempotency_key_conflict"`) {
			t.Fatalf("key reuse: %d %s", conflict.Code, conflict.Body)
		}
	}
	// Even semantically equivalent JSON must use identical bytes.
	conflict := keyedAnnouncementRequest(s, "POST", "/api/announcements", token, "create-once", body+" ")
	if conflict.Code != 409 {
		t.Fatalf("changed bytes: %d %s", conflict.Code, conflict.Body)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM announcements`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("announcements=%d err=%v", count, err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE resource_type='announcement'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("audits=%d err=%v", count, err)
	}
	actual, err := store.GetAnnouncement(a.ID)
	if err != nil || actual.Revision != 3 || actual.Publication != "archived" {
		t.Fatalf("replay mutated latest: %+v %v", actual, err)
	}
}

func TestAnnouncementIdempotencyScopeAndAuthorization(t *testing.T) {
	s, store := newTestServer(t)
	first, firstID := seedUserAndJWT(t, store, "first-receipt-admin", "admin")
	second, _ := seedUserAndJWT(t, store, "second-receipt-admin", "admin")
	body := `{"title":"Notice","message":"Details"}`
	ids := map[string]bool{}
	resourceIDs := []string{}
	tokens := []string{first, second}
	for i := range 2 {
		token := fmt.Sprintf("shk_receipt_scope_%d", i)
		_, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: firstID, KeyHash: auth.HashAPIKey(token), Name: fmt.Sprintf("receipt-%d", i), CredentialType: "personal", Unrestricted: true})
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	for _, token := range tokens {
		w := keyedAnnouncementRequest(s, "POST", "/api/announcements", token, "shared-key", body)
		if w.Code != 201 {
			t.Fatalf("scope create: %d %s", w.Code, w.Body)
		}
		a := decodeAnnouncement(t, w)
		if ids[a.ID] {
			t.Fatal("receipt leaked across actor/credential scope")
		}
		ids[a.ID] = true
		resourceIDs = append(resourceIDs, a.ID)
	}
	for _, id := range resourceIDs[:2] {
		w := keyedAnnouncementRequest(s, "PATCH", "/api/announcements/"+id, second, "shared-key", `{"expected_revision":1,"message":"Edited"}`)
		if w.Code != 200 || decodeAnnouncement(t, w).ID != id {
			t.Fatalf("method/resource scope: %d %s", w.Code, w.Body)
		}
	}
	// Permission is checked on every request before any successful receipt replay.
	if err := store.SetManualRole(firstID, "viewer"); err != nil {
		t.Fatal(err)
	}
	denied := keyedAnnouncementRequest(s, "POST", "/api/announcements", first, "shared-key", body)
	if denied.Code != 403 {
		t.Fatalf("demoted actor replay: %d %s", denied.Code, denied.Body)
	}
}

func TestAnnouncementIdempotentConcurrentRequestsAndUnicodeLimits(t *testing.T) {
	s, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "concurrent-receipt-admin", "admin")
	data, _ := json.Marshal(map[string]any{"title": strings.Repeat("界", 120), "message": strings.Repeat("🚀", 600)})
	const workers = 8
	results := make(chan *httptest.ResponseRecorder, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- keyedAnnouncementRequest(s, "POST", "/api/announcements", token, "unicode-once", string(data))
		}()
	}
	wg.Wait()
	close(results)
	original := ""
	for w := range results {
		if w.Code != 201 {
			t.Fatalf("concurrent create: %d %s", w.Code, w.Body)
		}
		if original == "" {
			original = w.Body.String()
		} else if original != w.Body.String() {
			t.Fatal("same-key concurrent responses differed")
		}
	}
	var count int
	for _, table := range []string{"announcements", "announcement_save_receipts", "audit_events"} {
		if err := store.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s=%d err=%v", table, count, err)
		}
	}
	// No-key clients retain normal create behavior, including Unicode boundaries.
	w := announcementRequest(t, s, "POST", "/api/announcements", token, map[string]any{"title": strings.Repeat("界", 120), "message": strings.Repeat("🚀", 600)})
	if w.Code != 201 {
		t.Fatalf("unkeyed unicode: %d %s", w.Code, w.Body)
	}
	for _, key := range []string{"", strings.Repeat("k", 201), "contains space", "雪"} {
		w := keyedAnnouncementRequest(s, "POST", "/api/announcements", token, key, string(data))
		if w.Code != 400 {
			t.Fatalf("invalid key %q: %d", key, w.Code)
		}
	}
}
