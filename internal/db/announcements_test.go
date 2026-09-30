package db_test

import (
	"errors"
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
