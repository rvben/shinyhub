package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/appmetaspec"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
)

// handleListProjects returns the projects the caller may see. A privileged app
// operator (admin or operator) sees every project including ones with no apps,
// because they are the audience for the "create it before you deploy into it"
// flow. Everyone else sees exactly the projects their visible apps reference,
// through the SAME predicate the apps list uses (internal/db.appVisibleToUserWhere).
//
// A deploy token restricted to specific apps is NOT a privileged operator even
// when its role says otherwise: scope beats role, so a scoped identity takes the
// first branch whatever its role. The resolution order is scope, then role, then
// per-app access, matching internal/api/authorization.go:26.
//
// app_count follows the same split: the privileged branch counts every app, the
// other two count only the apps that caller can reach. The three callers
// therefore see different counts for the same project, which is correct - a
// count over unreachable apps would disclose them.
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var (
		items []*db.ProjectListItem
		err   error
	)
	switch {
	case u.HasAppScopeRestriction():
		items, err = s.scopedProjects(u)
	case isPrivilegedAppOperator(u):
		items, err = s.store.ListProjects(0, 0)
	default:
		items, err = s.store.ListProjectsVisibleToUser(u.ID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if items == nil {
		items = []*db.ProjectListItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// scopedProjects builds the list for an identity carrying an app allowlist.
//
// The allowlist lives on the request identity, not in the database, so NO store
// query can apply it - which is why this aggregates in Go instead of adding a
// third store method. It mirrors GET /api/apps, which likewise runs its query
// first and filters the result by scope (apps.go:71-81).
//
// A scoped identity therefore never sees an empty project (an empty project
// contains no in-scope app), and its app_count counts only allowlisted apps.
// Without this branch a deploy token scoped to one app but carrying the admin
// role would enumerate every project name on the server.
func (s *Server) scopedProjects(u *auth.ContextUser) ([]*db.ProjectListItem, error) {
	// Which apps the scope narrows DOWN FROM still depends on the role: an
	// admin token starts from every app, anyone else from their visible set.
	var (
		apps []*db.App
		err  error
	)
	if u.IsServiceAccount() || isPrivilegedAppOperator(u) {
		apps, err = s.store.ListApps(0, 0)
	} else {
		apps, err = s.store.ListAppsVisibleToUser(u.ID, 0, 0)
	}
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, a := range apps {
		if a.ProjectSlug == "" || !u.AppInScope(a.Slug) {
			continue
		}
		counts[a.ProjectSlug]++
	}
	if len(counts) == 0 {
		return nil, nil
	}
	meta, err := s.store.ListProjects(0, 0)
	if err != nil {
		return nil, err
	}
	bySlug := make(map[string]*db.ProjectListItem, len(meta))
	for _, p := range meta {
		bySlug[p.Slug] = p
	}
	slugs := make([]string, 0, len(counts))
	for sl := range counts {
		slugs = append(slugs, sl)
	}
	// Sorted by slug so this branch orders identically to the two store
	// queries; map iteration order would reshuffle the list on every request.
	sort.Strings(slugs)
	out := make([]*db.ProjectListItem, 0, len(slugs))
	for _, sl := range slugs {
		// AppCount comes from the scoped tally, NEVER from the ListProjects
		// row, whose count is over every app on the server.
		it := &db.ProjectListItem{Slug: sl, AppCount: counts[sl]}
		if p, ok := bySlug[sl]; ok {
			it.Name, it.Description, it.IconEmoji = p.Name, p.Description, p.IconEmoji
		}
		out = append(out, it)
	}
	return out, nil
}

type projectWriteRequest struct {
	Slug        string  `json:"slug"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	IconEmoji   *string `json:"icon_emoji"`
}

// normalizeProjectWrite validates and normalizes the shared POST/PATCH body.
// Pointers distinguish "omitted" from "explicitly cleared": PATCH needs that,
// and POST reuses it so the two paths cannot validate differently.
func normalizeProjectWrite(req *projectWriteRequest) error {
	if req.Name != nil {
		v, err := appmetaspec.NormalizeProjectName(*req.Name)
		if err != nil {
			return err
		}
		req.Name = &v
	}
	if req.Description != nil {
		v, err := appmetaspec.NormalizeDescription(*req.Description)
		if err != nil {
			return err
		}
		req.Description = &v
	}
	if req.IconEmoji != nil {
		v := strings.TrimSpace(*req.IconEmoji)
		// ValidateIconEmoji rejects "" because for an app "" means "clear the
		// emoji" and must not reach it. Here the same "" is a legal cleared
		// value, so short-circuit before calling it.
		if v != "" {
			if err := deploy.ValidateIconEmoji(v); err != nil {
				return err
			}
		}
		req.IconEmoji = &v
	}
	return nil
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !isPrivilegedAppOperator(u) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	// Project metadata is shared by every app in the project and cannot be
	// safely bounded by an app-slug allowlist. Scoped automation may deploy its
	// allowed apps into pre-provisioned projects, but project-catalog writes
	// require an unrestricted operator or admin credential.
	if u.HasAppScopeRestriction() {
		writeError(w, http.StatusForbidden, "project management requires unrestricted app access")
		return
	}
	var req projectWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	req.Slug = strings.TrimSpace(req.Slug)
	if !slugpkg.Valid(req.Slug) {
		writeError(w, http.StatusBadRequest, "slug must be "+slugpkg.HumanRule)
		return
	}
	if err := normalizeProjectWrite(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.store.UpsertProject(db.Project{
		Slug:        req.Slug,
		Name:        derefStringOrEmpty(req.Name),
		Description: derefStringOrEmpty(req.Description),
		IconEmoji:   derefStringOrEmpty(req.IconEmoji),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	p, err := s.store.GetProject(req.Slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if created {
		s.audit(r, db.AuditProjectCreate, "project", req.Slug, "")
		writeJSON(w, http.StatusCreated, map[string]any{"project": p})
		return
	}
	// Already existed: 200 with the STORED project, not the request body. The
	// endpoint is idempotent, so a repeat call from a fleet apply or a CI job
	// is a no-op rather than an unannounced rename.
	writeJSON(w, http.StatusOK, map[string]any{"project": p})
}

func (s *Server) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !isPrivilegedAppOperator(u) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if u.HasAppScopeRestriction() {
		writeError(w, http.StatusForbidden, "project management requires unrestricted app access")
		return
	}
	slug := chi.URLParam(r, "slug")
	var req projectWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if err := normalizeProjectWrite(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	err := s.store.UpdateProject(db.UpdateProjectParams{
		Slug:           slug,
		SetName:        req.Name != nil,
		Name:           derefStringOrEmpty(req.Name),
		SetDescription: req.Description != nil,
		Description:    derefStringOrEmpty(req.Description),
		SetIconEmoji:   req.IconEmoji != nil,
		IconEmoji:      derefStringOrEmpty(req.IconEmoji),
	})
	if errors.Is(err, db.ErrProjectNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	p, err := s.store.GetProject(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	s.audit(r, db.AuditProjectUpdate, "project", slug, "")
	writeJSON(w, http.StatusOK, map[string]any{"project": p})
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !isPrivilegedAppOperator(u) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if u.HasAppScopeRestriction() {
		writeError(w, http.StatusForbidden, "project management requires unrestricted app access")
		return
	}
	slug := chi.URLParam(r, "slug")
	// The reference count spans ALL apps, not just the ones this caller can
	// see: otherwise a project could be deleted out from under an app that is
	// merely invisible to the operator running the request.
	n, err := s.store.CountAppsInProject(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if n > 0 {
		writeError(w, http.StatusConflict, "project still has apps; move or delete them first")
		return
	}
	err = s.store.DeleteProject(slug)
	if errors.Is(err, db.ErrProjectNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	s.audit(r, db.AuditProjectDelete, "project", slug, "")
	w.WriteHeader(http.StatusNoContent)
}

// projectDisplay is the slug -> display metadata map used to decorate app
// payloads. It is loaded once per request: an N-app response must not become N
// project lookups.
type projectDisplay map[string]*db.ProjectListItem

func (s *Server) loadProjectDisplay() projectDisplay {
	out := projectDisplay{}
	ps, err := s.store.ListProjects(0, 0)
	if err != nil {
		// Display metadata is decoration. A failure here degrades cards to
		// their bare slug rather than failing the whole apps list.
		slog.Warn("load project display metadata", "err", err)
		return out
	}
	for _, p := range ps {
		out[p.Slug] = p
	}
	return out
}

// decorate returns the two flat display keys for an app's project. Both keys
// are always present, empty when there is no project or no metadata, so a
// client never has to tell "absent" from "empty".
func (d projectDisplay) decorate(projectSlug string) (name, iconEmoji string) {
	if p, ok := d[projectSlug]; ok && projectSlug != "" {
		return p.Name, p.IconEmoji
	}
	return "", ""
}
