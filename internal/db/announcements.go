package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrAnnouncementConflict = errors.New("announcement changed; reload before saving")

type Announcement struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Message         string     `json:"message"`
	DetailsURL      string     `json:"details_url"`
	Severity        string     `json:"severity"`
	Publication     string     `json:"publication"`
	Dismissible     bool       `json:"dismissible"`
	StartsAt        *time.Time `json:"starts_at"`
	EndsAt          *time.Time `json:"ends_at"`
	PublishedAt     *time.Time `json:"published_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CreatedBy       *int64     `json:"created_by"`
	UpdatedBy       *int64     `json:"updated_by"`
	Revision        int64      `json:"revision"`
	DisplayRevision int64      `json:"display_revision"`
}

func (a Announcement) Status(now time.Time) string {
	if a.Publication != "published" {
		return a.Publication
	}
	if a.EndsAt != nil && !now.Before(*a.EndsAt) {
		return "expired"
	}
	if a.StartsAt != nil && now.Before(*a.StartsAt) {
		return "scheduled"
	}
	return "active"
}

func (a *Announcement) Validate() error {
	a.Title = strings.TrimSpace(a.Title)
	a.Message = strings.TrimSpace(a.Message)
	a.DetailsURL = strings.TrimSpace(a.DetailsURL)
	if !utf8.ValidString(a.Title+a.Message+a.DetailsURL) || strings.ContainsRune(a.Title+a.Message+a.DetailsURL, 0) {
		return errors.New("announcement contains invalid text")
	}
	if n := utf8.RuneCountInString(a.Title); n < 1 || n > 120 {
		return errors.New("title must be between 1 and 120 characters")
	}
	if n := utf8.RuneCountInString(a.Message); n < 1 || n > 600 {
		return errors.New("message must be between 1 and 600 characters")
	}
	if a.Severity != "information" && a.Severity != "warning" && a.Severity != "critical" {
		return errors.New("severity must be information, warning, or critical")
	}
	if a.Publication != "draft" && a.Publication != "published" && a.Publication != "disabled" && a.Publication != "archived" {
		return errors.New("invalid publication state")
	}
	if a.DetailsURL != "" {
		u, err := url.Parse(a.DetailsURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || len(a.DetailsURL) > 2048 {
			return errors.New("details link must be an absolute HTTP or HTTPS URL without credentials")
		}
	}
	if a.Publication == "published" && a.StartsAt == nil {
		return errors.New("published announcements need a start time")
	}
	if a.StartsAt != nil && a.EndsAt != nil && !a.EndsAt.After(*a.StartsAt) {
		return errors.New("end time must be after start time")
	}
	return nil
}

const announcementColumns = `id,title,message,details_url,severity,publication,dismissible,starts_at,ends_at,published_at,created_at,updated_at,created_by,updated_by,revision,display_revision`

func scanAnnouncement(row interface{ Scan(...any) error }) (Announcement, error) {
	var a Announcement
	var start, end, published, creator, updater sql.NullInt64
	var created, updated int64
	err := row.Scan(&a.ID, &a.Title, &a.Message, &a.DetailsURL, &a.Severity, &a.Publication, &a.Dismissible, &start, &end, &published, &created, &updated, &creator, &updater, &a.Revision, &a.DisplayRevision)
	a.StartsAt = announcementTime(start)
	a.EndsAt = announcementTime(end)
	a.PublishedAt = announcementTime(published)
	a.CreatedAt = time.UnixMilli(created).UTC()
	a.UpdatedAt = time.UnixMilli(updated).UTC()
	if creator.Valid {
		a.CreatedBy = &creator.Int64
	}
	if updater.Valid {
		a.UpdatedBy = &updater.Int64
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return a, err
}
func announcementTime(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.UnixMilli(n.Int64).UTC()
	return &t
}
func announcementMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}
func announcementBool(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) GetAnnouncement(id string) (Announcement, error) {
	return scanAnnouncement(s.db.QueryRow(`SELECT `+announcementColumns+` FROM announcements WHERE id=?`, id))
}
func (s *Store) ListAnnouncements(limit, offset int) ([]Announcement, error) {
	rows, err := s.db.Query(`SELECT `+announcementColumns+` FROM announcements ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Announcement{}
	for rows.Next() {
		a, e := scanAnnouncement(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AnnouncementSnapshot reads active and future publications together. A cache
// must expire at NextTransition, even when no announcement is active yet.
type AnnouncementSnapshot struct {
	Announcements  []Announcement
	NextTransition *time.Time
}

func (s *Store) ActiveAnnouncements(now time.Time) (AnnouncementSnapshot, error) {
	out := AnnouncementSnapshot{Announcements: []Announcement{}}
	rows, err := s.db.Query(`SELECT `+announcementColumns+` FROM announcements WHERE publication='published' AND (ends_at IS NULL OR ends_at>?) ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,published_at DESC,id DESC`, now.UnixMilli())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		a, e := scanAnnouncement(rows)
		if e != nil {
			return out, e
		}
		if a.Status(now) == "active" {
			out.Announcements = append(out.Announcements, a)
		}
		for _, t := range []*time.Time{a.StartsAt, a.EndsAt} {
			if t != nil && t.After(now) && (out.NextTransition == nil || t.Before(*out.NextTransition)) {
				out.NextTransition = t
			}
		}
	}
	return out, rows.Err()
}

// SaveAnnouncement serializes read/edit, checks the expected revision, and
// commits the mutation and its audit record together on either dialect.
func (s *Store) SaveAnnouncement(a Announcement, expected int64, audit AuditEventParams) (Announcement, error) {
	if err := a.Validate(); err != nil {
		return a, err
	}
	tx, err := s.d.beginWrite(context.Background(), s.db.real, 0)
	if err != nil {
		return a, err
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().UTC()
	action := "announcement.create"
	if expected == 0 {
		a.Revision = 1
		a.DisplayRevision = 1
		a.CreatedAt = now
		a.CreatedBy = a.UpdatedBy
		_, err = tx.Exec(`INSERT INTO announcements (`+announcementColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Title, a.Message, a.DetailsURL, a.Severity, a.Publication, announcementBool(a.Dismissible), announcementMillis(a.StartsAt), announcementMillis(a.EndsAt), announcementMillis(a.PublishedAt), now.UnixMilli(), now.UnixMilli(), a.CreatedBy, a.UpdatedBy, a.Revision, a.DisplayRevision)
	} else {
		old, e := scanAnnouncement(tx.QueryRow(`SELECT `+announcementColumns+` FROM announcements WHERE id=?`, a.ID))
		if e != nil {
			return a, e
		}
		if old.Revision != expected {
			return a, ErrAnnouncementConflict
		}
		if old.Publication == "archived" && a.Publication != "archived" {
			return a, errors.New("archived announcements cannot be republished")
		}
		a.CreatedAt = old.CreatedAt
		a.CreatedBy = old.CreatedBy
		a.Revision = old.Revision + 1
		a.DisplayRevision = old.DisplayRevision
		if old.Title != a.Title || old.Message != a.Message || old.DetailsURL != a.DetailsURL || old.Severity != a.Severity || old.Dismissible != a.Dismissible || old.Publication != a.Publication || !sameAnnouncementTime(old.StartsAt, a.StartsAt) || !sameAnnouncementTime(old.EndsAt, a.EndsAt) {
			a.DisplayRevision++
		}
		action = "announcement.update"
		if a.Publication != old.Publication {
			action = "announcement." + a.Publication
		}
		res, e := tx.Exec(`UPDATE announcements SET title=?,message=?,details_url=?,severity=?,publication=?,dismissible=?,starts_at=?,ends_at=?,published_at=?,updated_at=?,updated_by=?,revision=?,display_revision=? WHERE id=? AND revision=?`, a.Title, a.Message, a.DetailsURL, a.Severity, a.Publication, announcementBool(a.Dismissible), announcementMillis(a.StartsAt), announcementMillis(a.EndsAt), announcementMillis(a.PublishedAt), now.UnixMilli(), a.UpdatedBy, a.Revision, a.DisplayRevision, a.ID, expected)
		if e != nil {
			return a, e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return a, e
		}
		if n != 1 {
			return a, ErrAnnouncementConflict
		}
	}
	if err != nil {
		return a, fmt.Errorf("save announcement: %w", err)
	}
	if _, err = tx.Exec(`INSERT INTO audit_events (user_id,action,resource_type,resource_id,detail,ip_address,credential_id,credential_type,credential_name) VALUES (?,?,'announcement',?,?,?,?,?,?)`, audit.UserID, action, a.ID, AuditDetail(map[string]any{"title": a.Title, "publication": a.Publication, "revision": a.Revision, "severity": a.Severity}), audit.IPAddress, audit.CredentialID, audit.CredentialType, audit.CredentialName); err != nil {
		return a, err
	}
	if err = tx.Commit(); err != nil {
		return a, err
	}
	a.UpdatedAt = now
	return a, nil
}
func sameAnnouncementTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.UnixMilli() == b.UnixMilli()
}
