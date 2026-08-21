package api

import (
	"net/http"

	"github.com/rvben/shinyhub/internal/appnav"
	"github.com/rvben/shinyhub/internal/auth"
)

// HandleAppNavJSON serves the app switcher's list at
// /app/<slug>/.shinyhub/nav.json.
//
// The slug in the path is addressing, not input: the response is a function of
// the caller's identity alone and this handler never reads it. That is what
// lets the same rail work on an access-denied page - a visitor refused one app
// is still offered the apps they do hold - and it means the endpoint adds no
// slug-enumeration surface, because the answer is identical for every slug.
//
// The path is per-app anyway because the isolated app origin admits nothing
// outside /app/ (see cmd/shinyhub/app_origin.go), so a single fleet-wide nav
// path would be unreachable from the very pages that need it.
//
// Visibility is the same rule as /.shinyhub/apps.json, shared through
// listAppsVisibleTo: anonymous callers see public apps, operators see all,
// everyone else sees what is theirs. Serving it from the app origin discloses
// nothing new, since the same caller can already read the same list from the
// control origin.
func (s *Server) HandleAppNavJSON(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	// One past the cap, so hitting it is distinguishable from a fleet that
	// happens to end exactly there.
	apps, err := s.listAppsVisibleTo(u, appnav.MaxApps+1, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	payload := appnav.Payload{Apps: make([]appnav.App, 0, len(apps))}
	if u != nil {
		payload.Username = u.Username
	}
	if len(apps) > appnav.MaxApps {
		apps = apps[:appnav.MaxApps]
		payload.Truncated = true
	}

	// Project name and icon are not columns: they live on the project row and
	// are joined in per request. One map read per app, one query per response,
	// the same way the dashboard's own list builds them.
	disp := s.loadProjectDisplay()
	for _, a := range apps {
		projectName, projectIcon := disp.decorate(a.ProjectSlug)
		payload.Apps = append(payload.Apps, appnav.App{
			Slug:             a.Slug,
			Name:             a.Name,
			IconEmoji:        a.IconEmoji,
			ProjectSlug:      a.ProjectSlug,
			ProjectName:      projectName,
			ProjectIconEmoji: projectIcon,
			Openable:         appnav.Openable(a.Status),
		})
	}

	// The list turns over as apps deploy, stop and crash, and it is read on
	// every app page load. Caching it would pin a visitor's rail to whatever
	// was true when they first opened an app this session.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, payload)
}
