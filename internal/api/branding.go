package api

import (
	"net/http"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/ui"
)

// appBrief is the minimal DTO exposed at /.shinyhub/apps.json. It deliberately
// omits internal fields (owner_id, status, replicas, etc.) so the endpoint can
// be served to unauthenticated callers without leaking operational data.
type appBrief struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
}

func toBriefs(apps []*db.App) []appBrief {
	out := make([]appBrief, 0, len(apps))
	for _, a := range apps {
		out = append(out, appBrief{Slug: a.Slug, Name: a.Name, Visibility: a.Access})
	}
	return out
}

// HandleBrandingJSON is always public (no auth required). Returns an empty
// object when branding is not configured.
func (s *Server) HandleBrandingJSON(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Branding.IsActive() {
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}
	writeJSON(w, http.StatusOK, ui.PublicBranding(s.cfg.Branding, s.cfg.Branding.ResolvedAssets()))
}

// listAppsVisibleTo returns exactly the apps a caller may see:
//   - anonymous -> public apps only (via ListPublicApps, separate query)
//   - admin/operator -> all apps (via ListApps)
//   - other authenticated users -> public + shared + owned + member apps
//
// It is shared by every endpoint that answers "which apps is this caller
// allowed to know about", so a change to the visibility rule cannot land on one
// surface and miss another.
//
// A scoped identity (a deploy token carrying an app allowlist) is narrowed to
// its allowlist last, matching the per-slug gates: the scope binds on every app
// surface regardless of role. It is applied after the query, so a scoped caller
// paging through the list can receive a short page; that is correct for an
// identity whose whole list is its allowlist, and the alternative is answering
// with apps it may not touch.
func (s *Server) listAppsVisibleTo(u *auth.ContextUser, limit, offset int) ([]*db.App, error) {
	var (
		apps []*db.App
		err  error
	)
	switch {
	case u == nil:
		apps, err = s.store.ListPublicApps(limit, offset)
	case isPrivilegedAppOperator(u):
		apps, err = s.store.ListApps(limit, offset)
	default:
		apps, err = s.store.ListAppsVisibleToUser(u.ID, limit, offset)
	}
	if err != nil || u == nil || len(u.AppScope) == 0 {
		return apps, err
	}
	// A fresh slice rather than a filter in place: this helper does not own the
	// slice the store handed back, and reusing its backing array would rewrite
	// the caller's data for a store that ever returns anything shared.
	scoped := make([]*db.App, 0, len(apps))
	for _, a := range apps {
		if u.AppInScope(a.Slug) {
			scoped = append(scoped, a)
		}
	}
	return scoped, nil
}

// HandleAppsJSON returns the minimal DTO for exactly the apps the caller may
// see. See listAppsVisibleTo for the selection rule.
func (s *Server) HandleAppsJSON(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)
	apps, err := s.listAppsVisibleTo(auth.UserFromContext(r.Context()), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, toBriefs(apps))
}
