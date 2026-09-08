package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fsx"
)

type draftUploadKey struct{}
type draftPreviewKey struct{}
type draftDigestKey struct{}

func (s *Server) draftPath(slug, id string) string {
	return filepath.Join(s.cfg.Storage.AppsDir, slug, "drafts", id+".zip")
}

func (s *Server) handleCreateDraft(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	ttl := 24 * time.Hour
	if raw := r.URL.Query().Get("ttl"); raw != "" {
		var err error
		ttl, err = time.ParseDuration(raw)
		if err != nil || ttl < 15*time.Minute || ttl > 7*24*time.Hour {
			writeError(w, 400, "draft ttl must be between 15m and 168h")
			return
		}
	}
	maxSize := maxBundleUploadSize
	if s.cfg.Storage.MaxBundleMB > 0 {
		maxSize = int64(s.cfg.Storage.MaxBundleMB) * 1024 * 1024
	}
	file, cleanup, err := readBundleUpload(w, r, maxSize)
	defer cleanup()
	if err != nil {
		code := 400
		if errors.Is(err, errBundleTooLarge) {
			code = 413
		}
		writeError(w, code, "valid bundle upload required")
		return
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		writeError(w, 500, "generate draft ID")
		return
	}
	id := hex.EncodeToString(idBytes)
	path := s.draftPath(app.Slug, id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		writeError(w, 500, "create draft storage")
		return
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		writeError(w, 500, "create draft bundle")
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	_, copyErr := io.Copy(out, file)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		writeError(w, 500, "save draft bundle")
		return
	}
	scratch, err := os.MkdirTemp(filepath.Dir(path), ".validate-")
	if err != nil {
		writeError(w, 500, "validate draft bundle")
		return
	}
	defer fsx.RemoveAll(scratch)
	if err := deploy.ExtractBundle(path, scratch); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	manifest, err := deploy.LoadManifest(scratch)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if manifest != nil {
		if err := s.validateManifestForServer(app, manifest.App); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	// Only the retained archive counts toward storage, not validation files.
	if err := fsx.RemoveAll(scratch); err != nil {
		writeError(w, 500, "remove draft validation files")
		return
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		writeError(w, 422, "invalid draft archive")
		return
	}
	digest, err := bundle.DigestZipReader(&zr.Reader)
	_ = zr.Close()
	if err != nil || digest == "" {
		writeError(w, 422, "cannot establish draft digest")
		return
	}
	// Upload may be slow. Capture the production baseline under its mutation lock.
	release := s.acquireDeployLock(app.Slug)
	defer release()
	app, err = s.store.GetAppBySlug(app.Slug)
	if err != nil {
		writeError(w, 409, "app no longer exists")
		return
	}
	if checkAppPreconditions(w, r, app) {
		return
	}
	if s.cfg.Storage.AppQuotaMB > 0 {
		if _, err := deploy.CheckAppQuota(s.cfg.Storage.AppsDir, s.cfg.Storage.AppDataDir, app.Slug, s.cfg.Storage.AppQuotaMB); err != nil {
			writeError(w, 409, "draft exceeds app storage quota")
			return
		}
	}
	drafts, err := s.store.ListDeploymentDrafts(app.ID)
	if err != nil {
		writeError(w, 500, "count drafts")
		return
	}
	if len(drafts) >= 20 {
		writeError(w, 409, "an app may retain at most 20 drafts; delete an old draft first")
		return
	}
	uid := auth.UserFromContext(r.Context()).ID
	d := db.DeploymentDraft{ID: id, AppID: app.ID, ContentDigest: digest, BaseRevision: appResourceRevision(app), CreatedBy: &uid, CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(ttl).Unix()}
	if err := s.store.CreateDeploymentDraft(d); err != nil {
		writeError(w, 500, "record draft")
		return
	}
	keep = true
	s.audit(r, "draft_create", "app", app.Slug, draftDetail(d))
	writeJSON(w, http.StatusCreated, d)
}

func draftDetail(d db.DeploymentDraft) string {
	b, _ := json.Marshal(map[string]any{"draft_id": d.ID, "content_digest": d.ContentDigest, "preview_slug": d.PreviewSlug})
	return string(b)
}

func (s *Server) handleListDrafts(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	drafts, err := s.store.ListDeploymentDrafts(app.ID)
	if err != nil {
		writeError(w, 500, "list drafts")
		return
	}
	writeJSON(w, 200, map[string]any{"items": drafts})
}

func (s *Server) loadDraft(w http.ResponseWriter, r *http.Request) (*db.App, *db.DeploymentDraft, bool) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return nil, nil, false
	}
	d, err := s.store.GetDeploymentDraft(app.ID, chi.URLParam(r, "draftID"))
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, 404, "draft not found")
		return nil, nil, false
	}
	if err != nil {
		writeError(w, 500, "read draft")
		return nil, nil, false
	}
	if d.ExpiresAt <= time.Now().Unix() {
		writeError(w, 410, "draft expired; upload a new draft")
		return nil, nil, false
	}
	return app, d, true
}

// draftResponse captures the existing deployment handler's ordinary JSON result.
// Draft execution deliberately does not negotiate streaming responses.
type draftResponse struct {
	header http.Header
	code   int
	bytes.Buffer
}

func (w *draftResponse) Header() http.Header { return w.header }
func (w *draftResponse) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}
func (w *draftResponse) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = 200
	}
	return w.Buffer.Write(p)
}
func (w *draftResponse) forward(dst http.ResponseWriter) {
	for k, v := range w.header {
		dst.Header()[k] = v
	}
	if w.code == 0 {
		w.code = 500
	}
	dst.WriteHeader(w.code)
	_, _ = dst.Write(w.Bytes())
}

func (s *Server) deployDraftBundle(r *http.Request, parent *db.App, d *db.DeploymentDraft, target string, preview bool) (*draftResponse, error) {
	path := s.draftPath(parent.Slug, d.ID)
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("retained bundle unavailable: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(file, info.Size())
	if err != nil {
		return nil, err
	}
	digest, err := bundle.DigestZipReader(zr)
	if err != nil || digest != d.ContentDigest {
		return nil, fmt.Errorf("retained bundle digest changed; upload a new draft")
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("slug", target)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, route)
	ctx = context.WithValue(ctx, draftUploadKey{}, file)
	ctx = context.WithValue(ctx, draftDigestKey{}, d.ContentDigest)
	if preview {
		ctx = context.WithValue(ctx, draftPreviewKey{}, target)
	}
	req := r.Clone(ctx)
	req.Header = r.Header.Clone()
	req.Header.Del("Accept")
	req.Header.Del(hdrIfContentDigest)
	req.Header.Del(hdrIfManagedBy)
	req.Header.Del(hdrIfResourceRevision)
	req.Header.Del("X-Shinyhub-Development-Session")
	req.Header.Del("X-Shinyhub-Development-Target")
	req.URL.Path = "/api/apps/" + target + "/deploy"
	req.URL.RawQuery = "start=true"
	if !preview {
		req.Header.Set(hdrIfResourceRevision, d.BaseRevision)
		req.URL.RawQuery = r.URL.RawQuery
	}
	result := &draftResponse{header: make(http.Header)}
	s.handleDeployApp(result, req)
	return result, nil
}

func (s *Server) handlePreviewDraft(w http.ResponseWriter, r *http.Request) {
	parent, d, ok := s.loadDraft(w, r)
	if !ok {
		return
	}
	release := s.acquireDeployLock("draft:" + d.ID)
	defer release()
	d, err := s.store.GetDeploymentDraft(parent.ID, d.ID)
	if err != nil {
		writeError(w, 404, "draft not found")
		return
	}
	if d.ExpiresAt <= time.Now().Unix() {
		writeError(w, 410, "draft expired")
		return
	}
	if d.PreviewSlug != "" {
		deps, err := s.store.ListDeployments(*d.PreviewAppID)
		if err != nil {
			writeError(w, 500, "inspect preview")
			return
		}
		if len(deps) > 0 && deps[0].ContentDigest == d.ContentDigest {
			writeJSON(w, 200, d)
			return
		}
	}
	// Side-effecting declarations cannot be silently omitted from the reviewed
	// bundle. Reject them before creating an app or running any code.
	path := s.draftPath(parent.Slug, d.ID)
	scratch, err := os.MkdirTemp(filepath.Dir(path), ".preview-")
	if err != nil {
		writeError(w, 500, "prepare preview")
		return
	}
	defer fsx.RemoveAll(scratch)
	if err := deploy.ExtractBundle(path, scratch); err != nil {
		writeError(w, 422, "invalid retained bundle")
		return
	}
	manifest, err := deploy.LoadManifest(scratch)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	if manifest != nil && (len(manifest.Hooks) > 0 || len(manifest.Schedules) > 0 || !manifest.Access.IsZero()) {
		writeError(w, 422, "draft previews do not yet support hooks, schedules or access-group declarations; use a separate development app for this bundle")
		return
	}
	if d.PreviewSlug == "" {
		u := auth.UserFromContext(r.Context())
		expiry := time.Unix(d.ExpiresAt, 0).UTC()
		slug := "preview-" + d.ID
		_, err = s.store.CreateDevelopmentApp(db.CreateDevelopmentAppParams{
			App:     db.CreateAppParams{Slug: slug, Name: "Draft: " + parent.Slug, OwnerID: u.ID, Access: "private"},
			Session: db.UpsertDevelopmentSessionParams{ID: "draft-" + d.ID, TargetKind: db.DevelopmentTargetEphemeral, UserID: &u.ID, Actor: u.Username, ExpiresAt: &expiry},
			DraftID: d.ID,
		})
		if err != nil {
			writeError(w, 409, "cannot create draft preview")
			return
		}
		d, err = s.store.GetDeploymentDraft(parent.ID, d.ID)
		if err != nil {
			writeError(w, 500, "read preview")
			return
		}
		s.audit(r, "draft_preview_create", "app", parent.Slug, draftDetail(*d))
	}
	result, err := s.deployDraftBundle(r, parent, d, d.PreviewSlug, true)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if result.code >= 400 {
		result.header.Set("X-Shinyhub-Draft-Preview", d.PreviewSlug)
		result.forward(w)
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) handlePromoteDraft(w http.ResponseWriter, r *http.Request) {
	parent, d, ok := s.loadDraft(w, r)
	if !ok {
		return
	}
	release := s.acquireDeployLock("draft:" + d.ID)
	defer release()
	d, err := s.store.GetDeploymentDraft(parent.ID, d.ID)
	if err != nil {
		writeError(w, 404, "draft not found")
		return
	}
	if d.ExpiresAt <= time.Now().Unix() {
		writeError(w, 410, "draft expired")
		return
	}
	if d.PromotedAt != nil {
		writeError(w, 409, "draft was already promoted")
		return
	}
	if d.PreviewAppID == nil {
		writeError(w, 409, "start and review the draft preview before promotion")
		return
	}
	deps, err := s.store.ListDeployments(*d.PreviewAppID)
	if err != nil || len(deps) == 0 || deps[0].ContentDigest != d.ContentDigest {
		writeError(w, 409, "draft preview has not deployed successfully")
		return
	}
	result, err := s.deployDraftBundle(r, parent, d, parent.Slug, false)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if result.code < 400 {
		if err := s.store.MarkDraftPromoted(parent.ID, d.ID); err != nil {
			writeError(w, 500, "deployment committed but draft promotion record failed; inspect deployment history before retrying")
			return
		}
		s.audit(r, "draft_promote", "app", parent.Slug, draftDetail(*d))
	}
	result.forward(w)
}

// Preview code and visibility remain immutable. Environment, private reviewer
// grants and local data may be configured through existing app commands.
func (s *Server) draftMutationGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		slug := ""
		if len(parts) >= 3 && parts[0] == "api" && parts[1] == "apps" {
			slug = parts[2]
		}
		if slug == "" || r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}
		preview, err := s.store.IsDraftPreview(slug)
		if err != nil {
			writeError(w, 500, "check draft preview")
			return
		}
		if preview {
			if _, ok := s.requireManageApp(w, r, slug); !ok {
				return
			}
			base := "/api/apps/" + slug
			suffix := strings.TrimPrefix(r.URL.Path, base)
			allowed := (suffix == "" && r.Method == "DELETE") || suffix == "/restart" || suffix == "/stop" || suffix == "/sleep" || strings.HasPrefix(suffix, "/env") || (suffix == "/members" || strings.HasPrefix(suffix, "/members/")) || strings.HasPrefix(suffix, "/data")
			if !allowed {
				writeError(w, 409, "draft preview code and visibility are immutable; create a new draft")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleDeleteDraft(w http.ResponseWriter, r *http.Request) {
	parent, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	id := chi.URLParam(r, "draftID")
	release := s.acquireDeployLock("draft:" + id)
	defer release()
	d, err := s.store.GetDeploymentDraft(parent.ID, id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, 404, "draft not found")
		return
	}
	if err != nil {
		writeError(w, 500, "read draft")
		return
	}
	if d.PreviewAppID != nil {
		releasePreview := s.acquireDeployLock(d.PreviewSlug)
		preview, err := s.store.GetAppByID(*d.PreviewAppID)
		if err == nil {
			_, err = s.deleteAppLocked(r.Context(), preview)
		}
		releasePreview()
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			writeError(w, 409, "could not remove preview; retry draft deletion")
			return
		}
	}
	releaseParent := s.acquireDeployLock(parent.Slug)
	defer releaseParent()
	if err := os.Remove(s.draftPath(parent.Slug, id)); err != nil && !os.IsNotExist(err) {
		writeError(w, 500, "remove retained bundle")
		return
	}
	if err := s.store.DeleteDeploymentDraft(parent.ID, id); err != nil {
		writeError(w, 500, "remove draft record")
		return
	}
	s.audit(r, "draft_delete", "app", parent.Slug, draftDetail(*d))
	writeJSON(w, 200, map[string]string{"status": "deleted", "id": id})
}

func (s *Server) reapExpiredDraftBundles(ctx context.Context, now time.Time) {
	drafts, err := s.store.ExpiredDraftBundles(now)
	if err != nil {
		slog.Error("list expired draft bundles", "err", err)
		return
	}
	for _, d := range drafts {
		if ctx.Err() != nil {
			return
		}
		release := s.acquireDeployLock("draft:" + d.ID)
		err := os.Remove(s.draftPath(d.Slug, d.ID))
		if err == nil || os.IsNotExist(err) {
			err = s.store.DeleteDeploymentDraft(d.AppID, d.ID)
		}
		release()
		if err != nil {
			slog.Error("remove expired draft", "draft", d.ID, "err", err)
		}
	}
}
