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

	// Quota check: measure current combined usage (app bundles + data dir), then
	// account for any existing file at the destination (overwrite-aware).
	quotaBytes := int64(s.cfg.Storage.AppQuotaMB) << 20
	if quotaBytes > 0 {
		appsUsed, err := deploy.DirSize(filepath.Join(s.cfg.Storage.AppsDir, slug))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "measure app size")
			return
		}
		dataUsed, err := data.DirSize(appDataDir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "measure data size")
			return
		}
		used := appsUsed + dataUsed

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
