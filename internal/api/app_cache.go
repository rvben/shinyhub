package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/storage"
)

// handleClearAppCache handles DELETE /api/apps/{slug}/cache: it removes every
// result cache namespace of the app. It refuses while any process or job may
// have the cache open, because removing a directory under a live cache breaks
// caching for that process until it restarts. The write side of the cache
// fence keeps a start from provisioning a namespace between that check and
// the removal.
//
// The database check also covers a schedule container orphaned by a
// control-plane restart: the inherited run stays running, and so refuses the
// clear, until the startup fence has removed its container. A serving
// container recovery could not adopt is parked (row stopped, no port), so
// no traffic reaches it and removing its cache affects no session.
func (s *Server) handleClearAppCache(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	fence := storage.CacheFence(slug)
	fence.Lock()
	defer fence.Unlock()

	inUse, err := s.store.AppCacheInUse(app.ID)
	if err != nil {
		slog.Error("result cache: clear: check in use", "slug", slug, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if inUse || (s.manager != nil && s.manager.HoldsLiveProcess(slug)) {
		writeError(w, http.StatusConflict, "app "+slug+" has a process or scheduled run that may be using its result cache; stop the app and wait for running schedules to finish, then clear")
		return
	}
	// With no cache root configured nothing was ever cached, so the clear
	// succeeds without touching the disk.
	cleared := []int64{}
	if root := s.cfg.Storage.AppCacheDir; root != "" {
		ids, err := storage.CacheNamespaceIDs(root, slug)
		if err != nil {
			slog.Error("result cache: clear: list", "slug", slug, "err", err)
			writeError(w, http.StatusInternalServerError, "clear failed")
			return
		}
		if ids != nil {
			cleared = ids
		}
		if err := storage.RemoveAppCache(root, slug); err != nil {
			slog.Error("result cache: clear", "slug", slug, "err", err)
			writeError(w, http.StatusInternalServerError, "clear failed")
			return
		}
	}

	var userID *int64
	if u := auth.UserFromContext(r.Context()); u != nil {
		userID = &u.ID
	}
	s.logAuditEvent(r, db.AuditEventParams{
		UserID:       userID,
		Action:       db.AuditCacheClear,
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       auditDetailJSON(map[string]any{"deployment_ids": cleared}),
		IPAddress:    s.ClientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
}
