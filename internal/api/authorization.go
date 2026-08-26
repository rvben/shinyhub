package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func isPrivilegedAppOperator(u *auth.ContextUser) bool {
	return u != nil && (u.Role == "admin" || u.Role == "operator")
}

// requireOperator returns the user from context and writes 403 unless they hold
// admin or operator. Use it for fleet-wide read endpoints whose inputs an
// operator can already reach per-app through canViewApp; requireAdmin remains
// correct for anything an operator cannot otherwise see.
//
// The returned user still carries its app scope: a fleet-wide handler must
// filter its rows with u.AppInScope, since role alone does not bound a scoped
// deploy token.
func requireOperator(w http.ResponseWriter, r *http.Request) (*auth.ContextUser, bool) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	if !isPrivilegedAppOperator(u) {
		writeError(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return u, true
}

func canCreateApps(u *auth.ContextUser) bool {
	if u == nil {
		return false
	}
	return isPrivilegedAppOperator(u) || u.Role == "developer"
}

// canUseAppsManagement decides whether the session should receive the Apps
// management surface. Global management roles always do; a viewer may also
// manage an individual app through ownership, direct membership, or group
// membership, which the browser cannot safely reconstruct on its own.
func (s *Server) canUseAppsManagement(u *auth.ContextUser) bool {
	if u == nil {
		return false
	}
	// Service credentials never inherit the shared principal's ownership or
	// membership. Their own role is the complete management boundary.
	if u.IsServiceAccount() {
		return u.Role != "viewer"
	}
	if u.Role != "viewer" {
		return true
	}
	ok, err := s.store.UserCanManageAnyApp(u.ID)
	if err != nil {
		slog.Warn("auth: check app management capability", "user_id", u.ID, "error", err)
		return false
	}
	return ok
}

// effectiveCanManageApp extends the global-role/ownership check with direct and
// group manager grants. It is used for presentation capabilities only; every
// mutation endpoint still performs its own authorization check.
func (s *Server) effectiveCanManageApp(u *auth.ContextUser, app *db.App) bool {
	if canManageApp(u, app) {
		return true
	}
	if u != nil && u.IsServiceAccount() {
		return false
	}
	role, ok, err := s.effectiveAppMemberRole(u, app)
	return err == nil && ok && role == "manager"
}

func (s *Server) canViewApp(u *auth.ContextUser, app *db.App) (bool, error) {
	if u == nil || app == nil {
		return false, nil
	}
	// A scoped identity (deploy token with an app allowlist) is checked before
	// role and visibility: scope beats both, so an out-of-scope app stays
	// invisible even to an admin-role token and even when the app is public.
	if !u.AppInScope(app.Slug) {
		return false, nil
	}
	// For a service credential the allowlist is both the explicit grant and the
	// ceiling. This keeps team automation independent of the shared service
	// account's owner/member rows while still letting viewer credentials inspect
	// the apps they were deliberately issued for.
	if u.IsServiceAccount() {
		return true, nil
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
	if !u.AppInScope(app.Slug) {
		return false
	}
	if u.IsServiceAccount() {
		return u.Role == "developer" || u.Role == "operator" || u.Role == "admin"
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

// effectiveAppMemberRole returns the highest-rank per-app role for u on app,
// combining the manual app_members role and any group-derived role. Returns
// ("", false, nil) when the user has neither. A non-nil error indicates a DB
// failure that callers must surface (not silently treat as no access).
func (s *Server) effectiveAppMemberRole(u *auth.ContextUser, app *db.App) (string, bool, error) {
	if u == nil || app == nil {
		return "", false, nil
	}
	best := ""
	role, err := s.store.GetMemberRole(app.Slug, u.ID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return "", false, err
	}
	if err == nil {
		best = db.HigherMemberRole(best, role)
	}
	groupRole, ok, err := s.store.GroupRoleForUserOnApp(app.Slug, u.ID)
	if err != nil {
		return "", false, err
	}
	if ok {
		best = db.HigherMemberRole(best, groupRole)
	}
	return best, best != "", nil
}

// hasExplicitAccess reports whether u has explicit (non-public, non-shared)
// access to app — i.e. operator/admin, owner, an explicit row in app_members,
// or a group rule that grants any role. Public or shared visibility on app does
// NOT qualify. Used by endpoints that need to reject public-only callers
// without writing a response themselves (the caller must already hold the app
// pointer).
//
// "Not a member" is the expected miss path; only DB errors propagate.
func (s *Server) hasExplicitAccess(u *auth.ContextUser, app *db.App) (bool, error) {
	if u == nil || app == nil {
		return false, nil
	}
	if !u.AppInScope(app.Slug) {
		return false, nil
	}
	if u.IsServiceAccount() {
		return true, nil
	}
	if isPrivilegedAppOperator(u) || app.OwnerID == u.ID {
		return true, nil
	}
	_, ok, err := s.effectiveAppMemberRole(u, app)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// requireExplicitAppAccess loads the named app and verifies the caller has
// explicit access. Unlike requireViewApp, public/shared visibility is NOT
// sufficient - only one of the following passes:
//   - admin or operator (platform-wide privilege)
//   - the app's owner (apps.owner_id == caller.id)
//   - an explicit row in app_members for this app (any role)
//   - a group rule that grants any role on this app (via app_group_access)
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

// jitOAuthRole returns the role to assign to a user being created via
// just-in-time OAuth/OIDC provisioning. It honors auth.oauth_default_role
// (validated in config.Load); the empty string falls back to "viewer" so
// callers don't need to special-case it. We default-deny to "viewer" rather
// than the historical "developer" so that strangers who happen to authenticate
// against an enabled IdP can't deploy code.
func (s *Server) jitOAuthRole() string {
	role := s.cfg.Auth.OAuthDefaultRole
	if role == "" {
		return "viewer"
	}
	return role
}

func (s *Server) requireManageApp(w http.ResponseWriter, r *http.Request, slug string) (*db.App, bool) {
	app, u, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return nil, false
	}
	if canManageApp(u, app) {
		return app, true
	}
	// A service credential that failed the role+scope check above must not fall
	// through to account-level membership: every credential shares the same
	// service-account user ID, so doing so would let a viewer borrow a sibling's
	// manager grant.
	if u.IsServiceAccount() {
		writeError(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	// A member OR group rule with role "manager" may also manage the app.
	role, ok, err := s.effectiveAppMemberRole(u, app)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return nil, false
	}
	if ok && role == "manager" {
		return app, true
	}
	writeError(w, http.StatusForbidden, "forbidden")
	return nil, false
}
