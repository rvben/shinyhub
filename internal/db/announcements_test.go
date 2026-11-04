package db_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestAnnouncementBoundariesRevisionsAndAuditRollback(t *testing.T) {
	s := dbtest.New(t)
	start := time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	a := db.Announcement{ID: "scheduled", Title: "Maintenance", Message: "Save work", Severity: "warning", Publication: "published", StartsAt: &start, EndsAt: &end, PublishedAt: &start, Dismissible: true}
	saved, err := s.SaveAnnouncement(a, 0, db.AuditEventParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		now    time.Time
		count  int
		status string
	}{{start.Add(-time.Millisecond), 0, "scheduled"}, {start, 1, "active"}, {end.Add(-time.Millisecond), 1, "active"}, {end, 0, "expired"}} {
		snap, err := s.ActiveAnnouncements(tc.now)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Announcements) != tc.count || saved.Status(tc.now) != tc.status {
			t.Fatalf("%s: %+v / %s", tc.now, snap, saved.Status(tc.now))
		}
	}
	noop, err := s.SaveAnnouncement(saved, saved.Revision, db.AuditEventParams{})
	if err != nil {
		t.Fatal(err)
	}
	if noop.DisplayRevision != saved.DisplayRevision {
		t.Fatal("no-op reset dismissal")
	}
	noop.Message = "New instructions"
	edited, err := s.SaveAnnouncement(noop, noop.Revision, db.AuditEventParams{})
	if err != nil {
		t.Fatal(err)
	}
	if edited.DisplayRevision != noop.DisplayRevision+1 {
		t.Fatal("edit did not reset dismissal")
	}
	badActor := int64(999999)
	edited.Title = "Should roll back"
	_, err = s.SaveAnnouncement(edited, edited.Revision, db.AuditEventParams{UserID: &badActor})
	if err == nil {
		t.Fatal("invalid audit actor succeeded")
	}
	actual, err := s.GetAnnouncement(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Title != a.Title || actual.Revision != edited.Revision {
		t.Fatal("audit failure did not roll back mutation")
	}
}
func TestAnnouncementConcurrentEditors(t *testing.T) {
	s := dbtest.New(t)
	a, err := s.SaveAnnouncement(db.Announcement{ID: "edit", Title: "Notice", Message: "Original", Severity: "information", Publication: "draft"}, 0, db.AuditEventParams{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, message := range []string{"First edit", "Second edit"} {
		wg.Add(1)
		go func(message string) {
			defer wg.Done()
			copy := a
			copy.Message = message
			_, err := s.SaveAnnouncement(copy, a.Revision, db.AuditEventParams{})
			results <- err
		}(message)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, db.ErrAnnouncementConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("%d successes, %d conflicts", success, conflict)
	}
}
func TestAnnouncementSeverityOrderAndNextBoundary(t *testing.T) {
	s := dbtest.New(t)
	now := time.Now().UTC()
	start := now.Add(-time.Hour)
	end := now.Add(time.Hour)
	future := now.Add(10 * time.Minute)
	for _, severity := range []string{"information", "warning", "critical"} {
		_, err := s.SaveAnnouncement(db.Announcement{ID: severity, Title: severity, Message: "Details", Severity: severity, Publication: "published", StartsAt: &start, EndsAt: &end, PublishedAt: &start}, 0, db.AuditEventParams{})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.SaveAnnouncement(db.Announcement{ID: "future", Title: "Future", Message: "Details", Severity: "critical", Publication: "published", StartsAt: &future, PublishedAt: &now}, 0, db.AuditEventParams{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.ActiveAnnouncements(now)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"critical", "warning", "information"} {
		if snap.Announcements[i].Severity != want {
			t.Fatal("incorrect priority")
		}
	}
	if snap.NextTransition == nil || snap.NextTransition.UnixMilli() != future.UnixMilli() {
		t.Fatal("future publication boundary missing")
	}
}

func TestAnnouncementReceiptsRollbackAndConcurrentPatch(t *testing.T) {
	s := dbtest.New(t)
	receipt := &db.AnnouncementReceipt{Scope: "admin:1:POST", Key: "create", Fingerprint: "original-body"}
	a := db.Announcement{ID: "receipt", Title: "Notice", Message: "Details", Severity: "information", Publication: "draft"}
	badActor := int64(999999)
	if _, err := s.SaveAnnouncementIdempotent(a, 0, db.AuditEventParams{UserID: &badActor}, receipt, nil); err == nil {
		t.Fatal("invalid audit actor succeeded")
	}
	var count int
	for _, table := range []string{"announcements", "announcement_save_receipts", "audit_events"} {
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed transaction left %s=%d err=%v", table, count, err)
		}
	}
	first, err := s.SaveAnnouncementIdempotent(a, 0, db.AuditEventParams{}, receipt, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A different payload cannot claim a successful request's key.
	changed := *receipt
	changed.Fingerprint = "changed-body"
	if _, err := s.SaveAnnouncementIdempotent(a, 0, db.AuditEventParams{}, &changed, nil); !errors.Is(err, db.ErrAnnouncementIdempotencyConflict) {
		t.Fatalf("changed body: %v", err)
	}
	patchReceipt := &db.AnnouncementReceipt{Scope: "admin:1:PATCH:receipt", Key: "patch", Fingerprint: "patch-body"}
	const workers = 8
	results := make(chan db.AnnouncementSaveResult, workers)
	failures := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			edited := first.Announcement
			edited.Message = "New instructions"
			saved, err := s.SaveAnnouncementIdempotent(edited, first.Revision, db.AuditEventParams{}, patchReceipt, nil)
			if err != nil {
				failures <- err
			} else {
				results <- saved
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	replayed := 0
	var original []byte
	for result := range results {
		if result.Replayed {
			replayed++
		}
		raw, _ := json.Marshal(result)
		if original == nil {
			original = raw
		} else if string(original) != string(raw) {
			t.Fatal("concurrent PATCH replay responses differ")
		}
		if result.Revision != 2 || result.DisplayRevision != 2 {
			t.Fatalf("duplicate revision: %+v", result)
		}
	}
	if replayed != workers-1 {
		t.Fatalf("got %d replays", replayed)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE resource_type='announcement'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM announcement_save_receipts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("receipt count=%d err=%v", count, err)
	}
	// Replaying the original creation still returns revision 1, not current state.
	replay, err := s.SaveAnnouncementIdempotent(db.Announcement{ID: "unused"}, 0, db.AuditEventParams{}, receipt, nil)
	if err != nil || !replay.Replayed || replay.Revision != 1 || replay.ID != a.ID {
		t.Fatalf("create snapshot replay: %+v %v", replay, err)
	}
}

func TestAnnouncementReceiptSurvivesDatabaseReopen(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	path := filepath.Join(t.TempDir(), "announcements.db")
	dbtest.WriteSQLiteFile(t, path)
	s, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	receipt := &db.AnnouncementReceipt{Scope: "durable-actor:POST", Key: "lost-response", Fingerprint: "published-body"}
	start := time.Now().UTC().Add(-time.Hour)
	a := db.Announcement{ID: "durable", Title: "Durable notice", Message: "Details", Severity: "information", Publication: "published", StartsAt: &start}
	first, err := s.SaveAnnouncementIdempotent(a, 0, db.AuditEventParams{}, receipt, nil)
	if err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Deliberately invalid input proves replay happens before preparation and
	// validation; only the durable receipt supplies this successful response.
	replay, err := s.SaveAnnouncementIdempotent(db.Announcement{ID: "different-id"}, 0, db.AuditEventParams{}, receipt, func(db.Announcement) (db.Announcement, error) {
		t.Fatal("replay called prepare")
		return db.Announcement{}, nil
	})
	if err != nil || !replay.Replayed {
		t.Fatalf("reopened replay: %+v %v", replay, err)
	}
	firstJSON, _ := json.Marshal(first)
	replayJSON, _ := json.Marshal(replay)
	if string(firstJSON) != string(replayJSON) {
		t.Fatalf("reopen changed response: %s vs %s", firstJSON, replayJSON)
	}
}
