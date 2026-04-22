package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/data"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// dataListMaxEntries is the upper bound on files returned by handleDataList.
// List returns ErrTooManyFiles when the count would exceed this cap.
const dataListMaxEntries = 10000

// appUsedBytes returns the combined on-disk usage (apps dir + data dir) for the slug.
func (s *Server) appUsedBytes(slug string) (int64, error) {
	appsUsed, err := deploy.DirSize(filepath.Join(s.cfg.Storage.AppsDir, slug))
	if err != nil {
		return 0, err
	}
	dataUsed, err := data.DirSize(data.AppDataDir(s.cfg.Storage.AppDataDir, slug))
	if err != nil {
		return 0, err
	}
	return appsUsed + dataUsed, nil
}

// handleDataList handles GET /api/apps/{slug}/data — lists files in the
// per-app data directory and returns a quota envelope.
//
// Access is gated by requireExplicitAppAccess: public/shared visibility alone
// is not sufficient; only the owner, admins/operators, or explicit app_members
// rows pass.
func (s *Server) handleDataList(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	_, _, ok := s.requireExplicitAppAccess(w, r, slug)
	if !ok {
		return
	}

	appDataDir := data.AppDataDir(s.cfg.Storage.AppDataDir, slug)

	files, err := data.List(appDataDir, dataListMaxEntries)
	if err != nil {
		if errors.Is(err, data.ErrTooManyFiles) {
			writeError(w, http.StatusUnprocessableEntity,
				"too many files: directory exceeds the cap of 10000 entries")
			return
		}
		writeError(w, http.StatusInternalServerError, "list files")
		return
	}
	if files == nil {
		files = []data.FileInfo{}
	}

	used, err := s.appUsedBytes(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "measure disk usage")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"files":      files,
		"quota_mb":   s.cfg.Storage.AppQuotaMB,
		"used_bytes": used,
	})
}

// handleDataDelete handles DELETE /api/apps/{slug}/data/* — removes a single
// file from the per-app data directory. Directories and reserved-prefix paths
// are refused. Responds 204 No Content on success.
func (s *Server) handleDataDelete(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	_, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	rawRel := chi.URLParam(r, "*")
	rel, err := url.PathUnescape(rawRel)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	cleanRel, err := data.SanitizeRelPath(rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	appDataDir := data.AppDataDir(s.cfg.Storage.AppDataDir, slug)

	if delErr := data.Delete(appDataDir, cleanRel); delErr != nil {
		switch {
		case errors.Is(delErr, data.ErrFileNotFound):
			writeError(w, http.StatusNotFound, "file not found")
		case errors.Is(delErr, data.ErrNotAFile):
			writeError(w, http.StatusBadRequest, "directory deletion not supported")
		case errors.Is(delErr, data.ErrInvalidPath):
			writeError(w, http.StatusBadRequest, "invalid path")
		default:
			writeError(w, http.StatusInternalServerError, "delete failed")
		}
		return
	}

	detail, _ := json.Marshal(map[string]any{
		"slug": slug,
		"path": cleanRel,
	})
	u := auth.UserFromContext(r.Context())
	var userID *int64
	if u != nil {
		userID = &u.ID
	}
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       userID,
		Action:       db.AuditDataDelete,
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       string(detail),
		IPAddress:    s.clientIP(r),
	})

	w.WriteHeader(http.StatusNoContent)
}

// handleDataPut handles PUT /api/apps/{slug}/data/* — streams a file body into
// the per-app data directory with quota enforcement and an audit event.
//
// The route wildcard is the relative path inside the app's data dir. A known
// Content-Length is required; chunked bodies are rejected with 411. Quota is
// evaluated with awareness of an existing file at the destination so that
// in-place overwrites are always allowed if the replacement is smaller.
func (s *Server) handleDataPut(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	// Require a known Content-Length — chunked encoding makes quota math
	// impossible without buffering the entire body first.
	if r.ContentLength <= 0 {
		writeError(w, http.StatusLengthRequired, "Content-Length required")
		return
	}

	// URL-decode the wildcard segment before sanitization so that percent-encoded
	// traversal attempts (e.g. "..%2Fetc%2Fpasswd") are caught by SanitizeRelPath.
	rawRel := chi.URLParam(r, "*")
	rel, err := url.PathUnescape(rawRel)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	cleanRel, err := data.SanitizeRelPath(rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	appDataDir := data.AppDataDir(s.cfg.Storage.AppDataDir, slug)

	// Serialize the quota-check + write phase per slug. Without this, two
	// concurrent uploads each see the same pre-write used_bytes, both pass
	// their quota check, and the on-disk total exceeds the cap. The lock is
	// released before maybeRestartForChange so a slow restart does not block
	// other uploads.
	releaseDataLock := s.acquireDataLock(slug)
	dataLockHeld := true
	releaseOnce := func() {
		if dataLockHeld {
			releaseDataLock()
			dataLockHeld = false
		}
	}
	defer releaseOnce()

	// Quota check: measure current combined usage (app bundles + data dir), then
	// account for any existing file at the destination (overwrite-aware).
	quotaBytes := int64(s.cfg.Storage.AppQuotaMB) << 20
	if quotaBytes > 0 {
		used, err := s.appUsedBytes(slug)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "measure app size")
			return
		}

		// Determine the size of the existing destination file (0 for new files).
		var existingDestSize int64
		if fi, statErr := os.Stat(filepath.Join(appDataDir, cleanRel)); statErr == nil {
			existingDestSize = fi.Size()
		}

		if err := data.QuotaCheck(used, existingDestSize, r.ContentLength, quotaBytes); err != nil {
			var qe *data.QuotaError
			if errors.As(err, &qe) {
				writeJSON(w, http.StatusRequestEntityTooLarge, qe)
				return
			}
			writeError(w, http.StatusInternalServerError, "quota check")
			return
		}
	}

	// Cap the reader to Content-Length to prevent over-reads.
	body := http.MaxBytesReader(w, r.Body, r.ContentLength)

	fi, putErr := data.Put(appDataDir, cleanRel, body, r.ContentLength)
	// Release the per-slug data lock as soon as the write commits so the
	// follow-up restart (which acquires its own deploy lock) does not stall
	// other uploads. Audit logging and the restart hop run lock-free.
	releaseOnce()
	if putErr != nil {
		// Distinguish disk-full from other I/O errors.
		var pathErr *os.PathError
		if errors.As(putErr, &pathErr) && errors.Is(pathErr.Err, syscall.ENOSPC) {
			writeError(w, http.StatusInsufficientStorage, "insufficient storage")
			return
		}
		writeError(w, http.StatusInternalServerError, "write file")
		return
	}

	restarted, restartErr := s.maybeRestartForChange(r, app, slug)

	// Audit: log the data push with slug, path, size, sha256, and restart outcome.
	detail, _ := json.Marshal(map[string]any{
		"slug":      slug,
		"path":      fi.Path,
		"size":      fi.Size,
		"sha256":    fi.SHA256,
		"restarted": restarted,
	})
	u := auth.UserFromContext(r.Context())
	var userID *int64
	if u != nil {
		userID = &u.ID
	}
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       userID,
		Action:       db.AuditDataPush,
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       string(detail),
		IPAddress:    s.clientIP(r),
	})

	resp := map[string]any{
		"path":      fi.Path,
		"size":      fi.Size,
		"sha256":    fi.SHA256,
		"restarted": restarted,
	}
	if restartErr != nil {
		resp["restart_error"] = restartErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}
