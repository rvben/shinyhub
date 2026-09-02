package api

import (
	"errors"
	"net/http"

	"github.com/rvben/shinyhub/internal/appnav"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
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

type appVersionPayload struct {
	ActiveGeneration string `json:"active_generation"`
}

func (s *Server) authorizeAppVersion(w http.ResponseWriter, r *http.Request, slug string) (*db.App, bool) {
	app, err := s.store.GetAppBySlug(slug)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return nil, false
	}
	u := auth.UserFromContext(r.Context())
	if u == nil {
		if app.Access == "public" {
			return app, true
		}
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	ok, err := s.canViewApp(u, app)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return nil, false
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return app, true
}

// HandleAppVersionJSON returns only the opaque active generation token. The
// page already carries the exact token that rendered it and compares the two
// client-side; no deployment sequence or version metadata is disclosed.
func (s *Server) HandleAppVersionJSON(w http.ResponseWriter, r *http.Request, slug string) {
	app, ok := s.authorizeAppVersion(w, r, slug)
	if !ok {
		return
	}
	if s.proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "version status unavailable")
		return
	}
	token, ok := s.proxy.ActiveGenerationActivationToken(app.Slug)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "active version is not locally routable")
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, http.StatusOK, appVersionPayload{ActiveGeneration: token})
}

// HandleAppVersionSwitch clears this browser's signed affinity pin. Requiring
// a non-simple custom header keeps cross-site forms from forcing a switch; the
// endpoint sends no CORS permission, so a foreign origin cannot add it.
func (s *Server) HandleAppVersionSwitch(w http.ResponseWriter, r *http.Request, slug string) {
	if _, ok := s.authorizeAppVersion(w, r, slug); !ok {
		return
	}
	if r.Header.Get("X-ShinyHub-Version-Switch") != "1" {
		writeError(w, http.StatusForbidden, "version switch confirmation required")
		return
	}
	if s.proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "version switching unavailable")
		return
	}
	s.proxy.ClearGenerationAffinity(w, r, slug)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
