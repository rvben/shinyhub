package api

import (
	"errors"
	"net/http"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func isPrivilegedAppOperator(u *auth.ContextUser) bool {
	return u != nil && (u.Role == "admin" || u.Role == "operator")
}

func canCreateApps(u *auth.ContextUser) bool {
	if u == nil {
		return false
	}
	return isPrivilegedAppOperator(u) || u.Role == "developer"
}

func (s *Server) canViewApp(u *auth.ContextUser, app *db.App) (bool, error) {
	if u == nil || app == nil {
		return false, nil
	}
	if isPrivilegedAppOperator(u) || app.Access == "public" || app.Access == "shared" || app.OwnerID == u.ID {
		return true, nil
	}
	return s.store.UserCanAccessApp(app.Slug, u.ID)
}

func canManageApp(u *auth.ContextUser, app *db.App) bool {
	if u == nil || app == nil {
		return false
	}
	return isPrivilegedAppOperator(u) || app.OwnerID == u.ID
}

func (s *Server) loadApp(slug string) (*db.App, error) {
	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, db.ErrNotFound
		}
		return nil, err
	}
	return app, nil
}

// requireViewApp loads the named app and verifies the caller has at least view
// access.  It returns the app and the authenticated user so callers can make
// further authorization decisions without a second context lookup.
func (s *Server) requireViewApp(w http.ResponseWriter, r *http.Request, slug string) (*db.App, *auth.ContextUser, bool) {
	app, err := s.loadApp(slug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return nil, nil, false
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return nil, nil, false
	}

	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, nil, false
	}
	ok, err := s.canViewApp(u, app)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return nil, nil, false
	}
	if !ok {
		// Return 404 to avoid confirming that the slug exists to unauthorized users.
		writeError(w, http.StatusNotFound, "not found")
		return nil, nil, false
	}

	return app, u, true
}

// hasExplicitAccess reports whether u has explicit (non-public, non-shared)
// access to app — i.e. operator/admin, owner, or an explicit row in
// app_members. Public or shared visibility on app does NOT qualify. Used by
// endpoints that need to reject public-only callers without writing a
// response themselves (the caller must already hold the app pointer).
//
// "Not a member" is the expected miss path; only DB errors propagate.
func (s *Server) hasExplicitAccess(u *auth.ContextUser, app *db.App) (bool, error) {
	if u == nil || app == nil {
		return false, nil
	}
	if isPrivilegedAppOperator(u) || app.OwnerID == u.ID {
		return true, nil
	}
	role, err := s.store.GetMemberRole(app.Slug, u.ID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return role != "", nil
}

// requireExplicitAppAccess loads the named app and verifies the caller has
// explicit access. Unlike requireViewApp, public/shared visibility is NOT
// sufficient — only one of the following passes:
//   - admin or operator (platform-wide privilege)
//   - the app's owner (apps.owner_id == caller.id)
//   - an explicit row in app_members for this app (any role)
//
// This is the guard for endpoints that must not leak via the public surface
// (e.g. the per-app data API). On 401/404 the response is already written.
func (s *Server) requireExplicitAppAccess(w http.ResponseWriter, r *http.Request, slug string) (*db.App, *auth.ContextUser, bool) {
	app, err := s.loadApp(slug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return nil, nil, false
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return nil, nil, false
	}
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, nil, false
	}
	ok, err := s.hasExplicitAccess(u, app)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return nil, nil, false
	}
	if ok {
		return app, u, true
	}
	// 404 to avoid confirming slug existence to unauthorized users
	// (matches requireViewApp's convention).
	writeError(w, http.StatusNotFound, "not found")
	return nil, nil, false
}

func (s *Server) requireManageApp(w http.ResponseWriter, r *http.Request, slug string) (*db.App, bool) {
	app, u, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return nil, false
	}
	if canManageApp(u, app) {
		return app, true
	}
	// A member with role="manager" may also manage the app.
	role, err := s.store.GetMemberRole(app.Slug, u.ID)
	if err == nil && role == "manager" {
		return app, true
	}
	writeError(w, http.StatusForbidden, "forbidden")
	return nil, false
}
