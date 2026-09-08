package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/data"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// storageWriteStatus classifies a data.Put error into an HTTP status and
// client-facing message. A disk-full write (ENOSPC, possibly wrapped in an
// *os.PathError) becomes 507 Insufficient Storage so a client/operator can tell
// "out of space" apart from a generic write failure; anything else is 500.
func storageWriteStatus(err error) (int, string) {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && errors.Is(pathErr.Err, syscall.ENOSPC) {
		return http.StatusInsufficientStorage, "insufficient storage"
	}
	if errors.Is(err, syscall.ENOSPC) {
		return http.StatusInsufficientStorage, "insufficient storage"
	}
	return http.StatusInternalServerError, "write file"
}

// dataListMaxEntries is the upper bound on files returned by handleDataList.
// List returns ErrTooManyFiles when the count would exceed this cap.
const dataListMaxEntries = 10000

// appUsageBreakdown returns the app's on-disk usage split into the two things
// that make it up: the bundle, its versions and its restored dependency library
// under apps_dir, and the files under the per-app data dir.
//
// The quota counts the sum, so appUsedBytes is what admission decisions read.
// The data listing needs the data half on its own: a reader who has pushed one
// small CSV should not be told the data directory holds hundreds of megabytes,
// which is what a single reported total does once an R library is restored.
func (s *Server) appUsageBreakdown(slug string) (appsUsed, dataUsed int64, err error) {
	appsUsed, err = deploy.DirSize(filepath.Join(s.cfg.Storage.AppsDir, slug))
	if err != nil {
		return 0, 0, err
	}
	dataUsed, err = data.DirSize(data.AppDataDir(s.cfg.Storage.AppDataDir, slug))
	if err != nil {
		return 0, 0, err
	}
	return appsUsed, dataUsed, nil
}

// appUsedBytes returns the combined on-disk usage (apps dir + data dir) for the
// slug. This is the quota denominator; see appUsageBreakdown for the split.
func (s *Server) appUsedBytes(slug string) (int64, error) {
	appsUsed, dataUsed, err := s.appUsageBreakdown(slug)
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
// rows pass. Explicit viewer-members may list data-file names/sizes (not
// contents) - a deliberate distinction from env vars, which hold secrets and are
// manager-only. See TestDataList_ExplicitViewerAllowed.
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

	appsUsed, dataUsed, err := s.appUsageBreakdown(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "measure disk usage")
		return
	}

	limit, offset := parsePagination(r)
	writeList(w, files, limit, offset, map[string]any{
		"quota_mb": s.cfg.Storage.AppQuotaMB,
		// used_bytes is what the quota is measured against, so it counts the
		// bundle and the restored dependency library as well as the pushed
		// files. data_bytes is the part this listing is actually about.
		"used_bytes": appsUsed + dataUsed,
		"data_bytes": dataUsed,
	})
}

// handleDataGet handles GET /api/apps/{slug}/data/* — streams one file back out
// of the per-app data directory.
//
// This is the read half of `data push`, and it exists for one job in
// particular: after a restore, an operator can see from the listing that a file
// has the right name and size, and needs to confirm the bytes are the ones they
// pushed. Without it that check requires filesystem access on the server, which
// is the thing the CLI is for.
//
// Access is manager-level, not the listing's explicit-viewer level. That gap is
// deliberate and is stated in handleDataList: a viewer may see what files exist
// and how big they are, because names and sizes are inventory, while the
// contents are the data itself and belong with the same permission that can
// overwrite or delete them.
//
// The response is always an opaque attachment. These bytes are whatever a
// developer chose to upload, and the dashboard is served from this same origin,
// so a sniffed text/html or image/svg+xml would execute in it. Serving
// octet-stream with nosniff and an attachment disposition means the browser
// stores the file instead of rendering it.
func (s *Server) handleDataGet(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	// URL-decode before sanitizing so percent-encoded traversal ("..%2F") is
	// caught by SanitizeRelPath rather than slipping through as an opaque
	// segment. Same order as the PUT and DELETE handlers.
	rawRel := chi.URLParam(r, "*")
	rel, err := url.PathUnescape(rawRel)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	appDataDir := data.AppDataDir(s.cfg.Storage.AppDataDir, slug)

	f, fi, err := data.Open(appDataDir, rel)
	if err != nil {
		switch {
		case errors.Is(err, data.ErrFileNotFound):
			writeError(w, http.StatusNotFound, "file not found")
		case errors.Is(err, data.ErrNotAFile):
			writeError(w, http.StatusBadRequest, "directory download not supported")
		case errors.Is(err, data.ErrInvalidPath):
			writeError(w, http.StatusBadRequest, "invalid path")
		default:
			writeError(w, http.StatusInternalServerError, "read file")
		}
		return
	}
	defer f.Close()

	cleanRel, _ := data.SanitizeRelPath(rel) // already validated by data.Open
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Quote and escape the filename: a data path may legitimately contain a
	// space or a quote, and an unescaped one would let the value break out of
	// the header parameter.
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+strings.NewReplacer(`"`, `\"`, `\`, `\\`).Replace(filepath.Base(cleanRel))+`"`)

	// ServeContent handles Range and conditional requests, so a partial or
	// resumed download of a large dataset works. It sets Content-Length itself
	// and does nothing to a body on a HEAD.
	http.ServeContent(w, r, "", fi.ModTime(), f)

	// Audited after the fact, and unconditionally: reading an app's persistent
	// data is a disclosure of that data, so the trail records it whether or not
	// the transfer completed. A partial read discloses a prefix.
	detail, _ := json.Marshal(map[string]any{
		"slug": slug,
		"path": cleanRel,
		"size": fi.Size(),
	})
	u := auth.UserFromContext(r.Context())
	var userID *int64
	if u != nil {
		userID = &u.ID
	}
	s.logAuditEvent(r, db.AuditEventParams{
		UserID:       userID,
		Action:       db.AuditDataPull,
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       string(detail),
		IPAddress:    s.ClientIP(r),
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
	s.logAuditEvent(r, db.AuditEventParams{
		UserID:       userID,
		Action:       db.AuditDataDelete,
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       string(detail),
		IPAddress:    s.ClientIP(r),
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

	// Durable-data guard: pushing data is an explicit intent to persist, so refuse
	// it when a placement tier has ephemeral storage (bare Fargate) and the
	// operator has not acknowledged ephemeral data. Fires before the body is read.
	if tier, blocked := s.ephemeralDataPushBlock(app); blocked {
		writeError(w, http.StatusUnprocessableEntity, ephemeralDataPushMsg(slug, tier))
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
	// other uploads. When quotas are disabled there is nothing to serialize,
	// so the lock is skipped entirely — concurrent uploads to different files
	// in the same slug then proceed in parallel.
	quotaBytes := int64(s.cfg.Storage.AppQuotaMB) << 20
	releaseOnce := func() {}
	if quotaBytes > 0 {
		releaseDataLock := s.acquireDataLock(slug)
		dataLockHeld := true
		releaseOnce = func() {
			if dataLockHeld {
				releaseDataLock()
				dataLockHeld = false
			}
		}
		defer releaseOnce()

		// Quota check: measure current combined usage (app bundles + data dir), then
		// account for any existing file at the destination (overwrite-aware).
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
		status, msg := storageWriteStatus(putErr)
		writeError(w, status, msg)
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
	s.logAuditEvent(r, db.AuditEventParams{
		UserID:       userID,
		Action:       db.AuditDataPush,
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       string(detail),
		IPAddress:    s.ClientIP(r),
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
