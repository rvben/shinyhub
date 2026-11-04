package api

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

type announcementCache struct {
	mu       sync.Mutex
	until    time.Time
	snapshot db.AnnouncementSnapshot
}

func (c *announcementCache) invalidate() { c.mu.Lock(); c.until = time.Time{}; c.mu.Unlock() }

type publicAnnouncement struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Message         string     `json:"message"`
	DetailsURL      string     `json:"details_url"`
	Severity        string     `json:"severity"`
	Dismissible     bool       `json:"dismissible"`
	EndsAt          *time.Time `json:"ends_at"`
	DisplayRevision int64      `json:"display_revision"`
}

// HandleActiveAnnouncements is also registered under /app/:slug/ so isolated
// app origins can read the same public feed without access to console APIs.
func (s *Server) HandleActiveAnnouncements(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	c := &s.announcements
	c.mu.Lock()
	now := time.Now().UTC()
	if !now.Before(c.until) {
		snap, err := s.store.ActiveAnnouncements(now)
		if err != nil {
			c.mu.Unlock()
			writeError(w, 503, "announcements temporarily unavailable")
			return
		}
		c.snapshot = snap
		c.until = now.Add(5 * time.Second)
		if snap.NextTransition != nil && snap.NextTransition.Before(c.until) {
			c.until = *snap.NextTransition
		}
	}
	snap := c.snapshot
	c.mu.Unlock()
	out := make([]publicAnnouncement, 0, len(snap.Announcements))
	for _, a := range snap.Announcements {
		if a.Status(now) != "active" {
			continue
		}
		out = append(out, publicAnnouncement{a.ID, a.Title, a.Message, a.DetailsURL, a.Severity, a.Dismissible, a.EndsAt, a.DisplayRevision})
	}
	writeJSON(w, 200, struct {
		Announcements  []publicAnnouncement `json:"announcements"`
		ServerTime     time.Time            `json:"server_time"`
		NextTransition *time.Time           `json:"next_transition"`
	}{out, now, snap.NextTransition})
}

func canManageAnnouncements(u *auth.ContextUser) bool {
	return u != nil && u.Role == "admin" && !u.AppScopeRestricted && len(u.AppScope) == 0 && u.SupportSession == nil
}
func requireAnnouncementManager(w http.ResponseWriter, r *http.Request) (*auth.ContextUser, bool) {
	if u := auth.UserFromContext(r.Context()); u != nil && u.LocalAnnouncements {
		return u, true
	}
	u, ok := requireAdmin(w, r)
	if !ok {
		return nil, false
	}
	if !canManageAnnouncements(u) {
		writeError(w, 403, "platform-wide administrator access required")
		return nil, false
	}
	return u, true
}

type managedAnnouncement struct {
	db.Announcement
	Status string `json:"status"`
}

func managed(a db.Announcement) managedAnnouncement {
	return managedAnnouncement{a, a.Status(time.Now().UTC())}
}
func (s *Server) handleListAnnouncements(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAnnouncementManager(w, r); !ok {
		return
	}
	limit, offset := parsePagination(r)
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	list, err := s.store.ListAnnouncements(limit+1, offset)
	if err != nil {
		writeError(w, 500, "failed to load announcements")
		return
	}
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	out := make([]managedAnnouncement, 0, len(list))
	for _, a := range list {
		out = append(out, managed(a))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"announcements": out, "has_more": more})
}
func (s *Server) handleGetAnnouncement(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAnnouncementManager(w, r); !ok {
		return
	}
	a, err := s.store.GetAnnouncement(chi.URLParam(r, "id"))
	if err != nil {
		s.announcementError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, managed(a))
}

type announcementRequest struct {
	Title            *string         `json:"title"`
	Message          *string         `json:"message"`
	DetailsURL       *string         `json:"details_url"`
	Severity         *string         `json:"severity"`
	Publication      *string         `json:"publication"`
	Dismissible      *bool           `json:"dismissible"`
	StartsAt         json.RawMessage `json:"starts_at"`
	EndsAt           json.RawMessage `json:"ends_at"`
	ExpectedRevision *int64          `json:"expected_revision"`
}

func (s *Server) handleSaveAnnouncement(w http.ResponseWriter, r *http.Request) {
	u, ok := requireAnnouncementManager(w, r)
	if !ok {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		writeError(w, 400, "invalid announcement request")
		return
	}
	var receipt *db.AnnouncementReceipt
	if values, supplied := r.Header["Idempotency-Key"]; supplied {
		key := r.Header.Get("Idempotency-Key")
		valid := len(values) == 1 && len(key) >= 1 && len(key) <= 200
		for _, char := range key {
			if char < 33 || char > 126 {
				valid = false
			}
		}
		if !valid {
			writeError(w, 400, "Idempotency-Key must be 1 to 200 printable ASCII characters without spaces")
			return
		}
		credentialType, credentialName := "", ""
		credentialID := int64(0)
		if c := auth.CredentialInfoFromContext(r.Context()); c != nil {
			credentialType, credentialID = c.Type, c.ID
			if c.ID == 0 {
				credentialName = c.Name
			}
		}
		scope, _ := json.Marshal([]any{u.ID, u.LocalAnnouncements, credentialType, credentialID, credentialName, r.Method, chi.URLParam(r, "id")})
		receipt = &db.AnnouncementReceipt{Scope: string(scope), Key: key, Fingerprint: fmt.Sprintf("%x", sha256.Sum256(data))}
	}
	var req announcementRequest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var requestErr error
	if err := dec.Decode(&req); err != nil {
		requestErr = announcementRequestError("invalid announcement request")
	} else if err := dec.Decode(&struct{}{}); err != io.EOF {
		requestErr = announcementRequestError("expected a single JSON object")
	}
	a := db.Announcement{ID: rand.Text(), Severity: "information", Publication: "draft", Dismissible: true}
	expected := int64(0)
	creating := r.Method == http.MethodPost
	if !creating {
		if req.ExpectedRevision == nil || *req.ExpectedRevision < 1 {
			if requestErr == nil {
				requestErr = announcementRequestError("expected_revision is required")
			}
		} else if requestErr == nil {
			expected = *req.ExpectedRevision
		}
		a.ID = chi.URLParam(r, "id")
	} else if req.ExpectedRevision != nil && requestErr == nil {
		requestErr = announcementRequestError("expected_revision is only used when editing")
	}

	var actor *int64
	if !u.LocalAnnouncements {
		actor = &u.ID
	}
	audit := db.AuditEventParams{UserID: actor, IPAddress: s.ClientIP(r)}
	if c := auth.CredentialInfoFromContext(r.Context()); c != nil {
		if c.ID != 0 {
			audit.CredentialID = &c.ID
		}
		audit.CredentialType = c.Type
		audit.CredentialName = c.Name
	}
	saved, err := s.store.SaveAnnouncementIdempotent(a, expected, audit, receipt, func(a db.Announcement) (db.Announcement, error) {
		if requestErr != nil {
			return a, requestErr
		}
		now := time.Now().UTC()
		prior := a.Publication
		if req.Title != nil {
			a.Title = *req.Title
		}
		if req.Message != nil {
			a.Message = *req.Message
		}
		if req.DetailsURL != nil {
			a.DetailsURL = *req.DetailsURL
		}
		if req.Severity != nil {
			a.Severity = *req.Severity
		}
		if req.Publication != nil {
			a.Publication = *req.Publication
		}
		if req.Dismissible != nil {
			a.Dismissible = *req.Dismissible
		} else if creating && a.Severity == "critical" {
			a.Dismissible = false
		}
		for _, field := range []struct {
			raw    json.RawMessage
			target **time.Time
		}{{req.StartsAt, &a.StartsAt}, {req.EndsAt, &a.EndsAt}} {
			if len(field.raw) > 0 {
				var t *time.Time
				if err := json.Unmarshal(field.raw, &t); err != nil {
					return a, announcementRequestError("dates must be RFC3339 timestamps with a timezone, or null")
				}
				if t != nil {
					utc := t.UTC()
					t = &utc
				}
				*field.target = t
			}
		}
		if prior == "archived" && a.Publication != "archived" {
			return a, announcementRequestError("archived announcements cannot be republished")
		}
		if a.Publication == "published" {
			if a.StartsAt == nil {
				a.StartsAt = &now
			}
			if a.PublishedAt == nil || prior != "published" {
				a.PublishedAt = &now
			}
		}
		if u.LocalAnnouncements && a.Publication == "published" {
			if a.EndsAt == nil {
				return a, announcementRequestError("host-local announcement publication requires ends_at")
			}
			if a.EndsAt.After(now.Add(auth.LocalNoticeMaxWindow)) {
				return a, announcementRequestError("host-local announcements must end within 24 hours of publication or update")
			}
		}
		if err := a.Validate(); err != nil {
			return a, announcementRequestError(err.Error())
		}
		a.UpdatedBy = actor
		return a, nil
	})
	if err != nil {
		s.announcementError(w, err)
		return
	}
	s.announcements.invalidate()
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	if saved.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, status, saved)
}

type announcementRequestError string

func (e announcementRequestError) Error() string { return string(e) }

func (s *Server) announcementError(w http.ResponseWriter, err error) {
	var invalid announcementRequestError
	switch {
	case errors.As(err, &invalid):
		writeError(w, 400, invalid.Error())
	case errors.Is(err, db.ErrAnnouncementIdempotencyConflict):
		writeJSON(w, 409, map[string]string{"error": err.Error(), "code": "idempotency_key_conflict"})
	case errors.Is(err, db.ErrNotFound):
		writeError(w, 404, "announcement not found")
	case errors.Is(err, db.ErrAnnouncementConflict):
		writeError(w, 409, err.Error())
	default:
		writeError(w, 500, "failed to save or load announcement")
	}
}
