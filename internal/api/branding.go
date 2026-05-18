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

// handleBrandingJSON is always public (no auth required). Returns an empty
// object when branding is not configured.
func (s *Server) handleBrandingJSON(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Branding.IsActive() {
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}
	writeJSON(w, http.StatusOK, ui.PublicBranding(s.cfg.Branding, s.cfg.Branding.ResolvedAssets()))
}

// handleAppsJSON returns the minimal DTO for exactly the apps the caller may
// see:
//   - anonymous -> public apps only (via ListPublicApps, separate query)
//   - admin/operator -> all apps (via ListApps)
//   - other authenticated users -> public + shared + owned + member apps
func (s *Server) handleAppsJSON(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	limit, offset := parsePagination(r)
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
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, toBriefs(apps))
}
