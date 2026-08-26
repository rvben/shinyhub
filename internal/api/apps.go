package api

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/appmetaspec"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/fsx"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
	"github.com/rvben/shinyhub/internal/storage"
)

// maxStoredReplicas mirrors the CHECK on the autoscale_min_replicas /
// autoscale_max_replicas columns (migration 023). The handler validates against
// it so an out-of-range bound is rejected with a 400 rather than failing the DB
// constraint mid-PATCH after sibling fields were already committed.
const maxStoredReplicas = 1000

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	limit, offset := parsePagination(r)

	// Fetch the full (bounded) app set; writeList paginates in-memory so the
	// envelope carries an accurate total. The dashboard polls this list, so the
	// count stays small.
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
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// A scoped identity (deploy token with an app allowlist) sees only its
	// allowlisted apps, matching the per-slug gates.
	if u.HasAppScopeRestriction() {
		scoped := apps[:0]
		for _, a := range apps {
			if u.AppInScope(a.Slug) {
				scoped = append(scoped, a)
			}
		}
		apps = scoped
	}
	managedSlugs := map[string]struct{}{}
	if !u.IsServiceAccount() && !isPrivilegedAppOperator(u) {
		managedSlugs, err = s.store.ManagedAppSlugsForUser(u.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}
	// One project lookup for the whole page: decorate() is a map read, so the
	// per-app cost stays constant however many apps the page carries.
	appIDs := make([]int64, len(apps))
	for i, a := range apps {
		appIDs[i] = a.ID
	}
	replicasByApp, err := s.store.ListReplicasForApps(appIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	disp := s.loadProjectDisplay()
	for _, a := range apps {
		s.decorateApp(a)
		if u.IsServiceAccount() {
			a.CanManage = canManageApp(u, a)
		} else if isPrivilegedAppOperator(u) {
			a.CanManage = true
		} else {
			_, a.CanManage = managedSlugs[a.Slug]
		}
		replicas := s.liveReplicaView(a.Slug, replicasByApp[a.ID])
		pool := s.elasticObservation(a.Slug)
		// A server built without a process manager (unit tests and API-only
		// embeddings) has no live authority. Preserve the stored status when there
		// are no durable replica facts to contradict it.
		if s.manager != nil || len(replicas) > 0 || pool.Known {
			s.decorateAppObservation(a, replicas, pool)
		}
		a.ProjectName, a.ProjectIconEmoji = disp.decorate(a.ProjectSlug)
	}
	writeList(w, apps, limit, offset, nil)
}

// liveReplicaView returns a detached replica slice with the single-node process
// manager overlaid on durable rows. In native/SQLite mode the manager is the
// authority for whether a process exists: a DB row that still says running but
// has no manager entry is reported stopped, not counted as live. Clustered mode
// keeps the shared DB as authority because another server can own the process.
func (s *Server) liveReplicaView(slug string, stored []*db.Replica) []*db.Replica {
	byIndex := make(map[int]*db.Replica, len(stored))
	for _, rep := range stored {
		if rep == nil {
			continue
		}
		copy := *rep
		byIndex[copy.Index] = &copy
	}

	if s.manager != nil && !s.clustered {
		live := s.manager.AllForSlug(slug)
		for _, rep := range byIndex {
			if rep.Status == db.ReplicaStatusRunning && (rep.Index >= len(live) || live[rep.Index] == nil) {
				rep.Status = string(process.StatusStopped)
				rep.PID, rep.Port = nil, nil
			}
		}
		for index, info := range live {
			if info == nil {
				continue
			}
			rep := byIndex[index]
			if rep == nil {
				rep = &db.Replica{Index: index, DesiredState: "running"}
				byIndex[index] = rep
			}
			rep.Status = string(info.Status)
			rep.Provider, rep.Tier = info.Provider, info.Tier
			if info.PID != 0 {
				pid := info.PID
				rep.PID = &pid
			}
			if info.Port != 0 {
				port := info.Port
				rep.Port = &port
			}
			if verdict, ok := s.manager.LastExit(slug, index); ok {
				rep.ExitCode = verdict.ExitCode
				rep.Signal = verdict.Signal
				if verdict.RestartCount > rep.RestartCount {
					rep.RestartCount = verdict.RestartCount
				}
				if rep.Status == string(process.StatusCrashed) && verdict.Reason != "" {
					rep.Reason = verdict.Reason
				}
			}
		}
	}

	out := make([]*db.Replica, 0, len(byIndex))
	for _, rep := range byIndex {
		if rep.Status == db.ReplicaStatusLost {
			rep.Reason = s.lostReplicaReason(rep.Tier)
		}
		out = append(out, rep)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// elasticObservation reads the proxy's live worker view for slug into the
// shape decorateAppObservation consumes. Known stays false when the app is not
// a registered elastic pool.
func (s *Server) elasticObservation(slug string) elasticPool {
	if s.proxy == nil {
		return elasticPool{}
	}
	snap, ok := s.proxy.ElasticWorkersSnapshot(slug)
	if !ok {
		return elasticPool{}
	}
	pool := elasticPool{Known: true}
	for _, worker := range snap.Workers {
		pool.observe(worker.Status)
	}
	return pool
}

type createAppRequest struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	ProjectSlug string `json:"project_slug"`
	// Access sets the initial visibility. When empty the server applies
	// defaults.app_visibility from config (which defaults to "private").
	// Allowed values: "private", "shared", "public".
	Access string `json:"access"`
}

func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	var req createAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.Slug == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "slug and name are required")
		return
	}
	if !slugpkg.Valid(req.Slug) {
		writeError(w, http.StatusBadRequest, "slug must be "+slugpkg.HumanRule)
		return
	}
	name, verr := appmetaspec.NormalizeName(req.Name)
	if verr != nil {
		writeError(w, http.StatusBadRequest, verr.Error())
		return
	}
	req.Name = name

	// project_slug is optional: "" means the app belongs to no project. A
	// non-empty value must be a legal slug, because it becomes a projects row
	// and appears in URLs and manifests.
	req.ProjectSlug = strings.TrimSpace(req.ProjectSlug)
	if req.ProjectSlug != "" && !slugpkg.Valid(req.ProjectSlug) {
		writeError(w, http.StatusBadRequest, "project_slug must be "+slugpkg.HumanRule)
		return
	}

	// Resolve effective access: explicit request body > config default > "private".
	access := req.Access
	if access == "" {
		access = s.cfg.Defaults.AppVisibility
	}
	if access == "" {
		access = "private"
	}
	if !db.IsValidAppVisibility(access) {
		writeError(w, http.StatusBadRequest, "access must be one of "+strings.Join(db.ValidAppVisibilities, ", "))
		return
	}

	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !canCreateApps(u) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if !u.AppInScope(req.Slug) {
		writeError(w, http.StatusForbidden, "this credential is restricted to specific apps; "+req.Slug+" is not one of them")
		return
	}

	if err := storage.RequireFreeSlug(s.cfg, req.Slug); err != nil {
		if errors.Is(err, storage.ErrSlugInUse) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	projectCreated, err := s.store.CreateApp(db.CreateAppParams{
		Slug:        req.Slug,
		Name:        req.Name,
		ProjectSlug: req.ProjectSlug,
		OwnerID:     u.ID,
		Access:      access,
	})
	if err != nil {
		if errors.Is(err, db.ErrSlugTaken) {
			writeError(w, http.StatusConflict, "slug already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if projectCreated {
		s.audit(r, db.AuditProjectCreate, "project", req.ProjectSlug, `{"implicit":true}`)
	}

	// Apply the operator-configured default replica count when it exceeds the
	// SQL DEFAULT of 1. Zero and one are left alone (zero is invalid; one
	// matches the default).
	if s.cfg.Runtime.DefaultReplicas > 1 {
		created, err := s.store.GetAppBySlug(req.Slug)
		if err == nil {
			if err := s.store.UpdateAppReplicas(created.ID, s.cfg.Runtime.DefaultReplicas); err != nil {
				slog.Error("set default replicas on create", "slug", req.Slug, "err", err)
			}
		}
	}

	app, err := s.store.GetAppBySlug(req.Slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.logAuditEvent(r, db.AuditEventParams{
		UserID:       &u.ID,
		Action:       "create_app",
		ResourceType: "app",
		ResourceID:   req.Slug,
		IPAddress:    s.ClientIP(r),
		RunID:        s.knownFleetRunID(r),
	})
	w.Header().Set(hdrResourceRevision, appResourceRevision(app))
	writeJSON(w, http.StatusCreated, app)
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, u, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return
	}
	// Capture the durable revision before presentation decorators overlay live
	// process observations onto the response model.
	resourceRevision := appResourceRevision(app)
	// Same derived fields the list payload carries, so the detail header agrees
	// with the card.
	s.decorateApp(app)
	app.ProjectName, app.ProjectIconEmoji = s.loadProjectDisplay().decorate(app.ProjectSlug)

	replicas, err := s.store.ListReplicas(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if replicas == nil {
		replicas = []*db.Replica{}
	}

	replicas = s.liveReplicaView(slug, replicas)

	// Derive a presentation-only reason for replicas lost to a dead worker.
	for i, rep := range replicas {
		if rep.Status == db.ReplicaStatusLost {
			replicas[i].Reason = s.lostReplicaReason(rep.Tier)
		} else if rep.Status == "crashed" && rep.Reason == "" {
			replicas[i].Reason = "replica process exited unexpectedly"
		}
	}
	s.decorateAppObservation(app, replicas, s.elasticObservation(slug))

	// effective_max_sessions_per_replica resolves the app's own cap against the
	// runtime default (0 = inherit). Clients use it to render an honest
	// admission ceiling (replicas x effective cap) instead of a bare "0".
	effectiveCap := deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica)
	// effective_autoscale_target resolves the app's own target against the
	// runtime default (0 = inherit), so clients render the figure the controller
	// will actually use without re-deriving the fallback.
	effectiveTarget := app.AutoscaleTarget
	if effectiveTarget <= 0 {
		effectiveTarget = s.cfg.Runtime.Autoscale.DefaultTarget
	}
	// can_manage tells the UI whether this caller may manage the app, including
	// via a per-app member or group manager role (the client cannot derive this
	// from global role + ownership alone). A lookup error degrades to false; the
	// management endpoints enforce authorization regardless.
	canManage := s.effectiveCanManageApp(u, app)
	app.CanManage = canManage

	envelope := map[string]any{
		"app":                                 app,
		"resource_revision":                   resourceRevision,
		"replicas_status":                     replicas,
		"effective_max_sessions_per_replica":  effectiveCap,
		"effective_autoscale_target":          effectiveTarget,
		"effective_hibernate_timeout_minutes": app.EffectiveHibernateTimeoutMinutes,
		"redeploy_in_flight":                  s.isRedeployInFlight(slug),
		"can_manage":                          canManage,
	}
	if fleetState, err := s.appFleetState(app); err == nil && fleetState != nil {
		envelope["fleet_state"] = fleetState
	} else if err != nil {
		reqLog(r).Error("load app fleet state failed", "slug", slug, "err", err)
	}
	if p, err := s.store.CurrentDeploymentProvenance(app.ID); err == nil && p != nil {
		envelope["deployment_provenance"] = p
	}
	// render_pacing is the same advisory block PATCH returns, computed from the
	// app's stored render_seconds. It is here so a client can show the cap
	// suggestion on load rather than only as a side effect of saving: the
	// dashboard's pacing control needs to tell an operator what the current
	// setting implies before they change anything, and a suggestion that only
	// appears after a write is a suggestion nobody sees until it is too late.
	// Omitted when pacing is off, which is the same signal buildRenderPacingBlock
	// gives PATCH.
	if block := s.buildRenderPacingBlock(app.RenderSeconds, effectiveCap); block != nil {
		envelope["render_pacing"] = block
	}
	// rejects_by_reason is a rolling 10-minute rollup of platform rejections for
	// this app, keyed by reason. Omitted entirely when no proxy is wired or when
	// the app has had no rejections in the window.
	if s.proxy != nil {
		if counts := s.proxy.RejectsByReason(slug, 10*time.Minute); len(counts) > 0 {
			byReason := make(map[string]uint64, len(counts))
			for reason, n := range counts {
				byReason[string(reason)] = n
			}
			envelope["rejects_by_reason"] = map[string]any{
				"window_seconds": 600,
				"counts":         byReason,
			}
		}
	}
	// connectivity surfaces the app's realtime (WebSocket) health so the detail
	// page can warn when an app is serving pages but its WebSocket never connects
	// (typically a reverse proxy blocking the upgrade). Only meaningful while the
	// app is running; omitted otherwise so a stopped/hibernated app shows nothing.
	if s.proxy != nil && app.Status == "running" {
		everConnected, servingWithoutWS := s.proxy.ConnectivityHealth(slug)
		envelope["connectivity"] = map[string]any{
			"websocket_ok":       everConnected,
			"serving_without_ws": servingWithoutWS,
		}
	}
	// worker_pool is the live per-worker capacity view for elastic
	// (grouped/per_session) apps: which workers exist, their routing status,
	// and how many sessions each holds - the figures an operator tunes
	// grouped_size and max_workers against. Present only while the proxy holds
	// an elastic pool for this app; multiplex apps have no per-worker view, and
	// an elastic pool with zero workers reports an empty (not absent) array.
	if s.proxy != nil {
		if snap, ok := s.proxy.ElasticWorkersSnapshot(slug); ok {
			envelope["worker_pool"] = s.buildWorkerPool(slug, snap)
		}
	}
	asEvent, asFound, asErr := s.store.LatestAutoscaleEvent(slug)
	if asErr != nil {
		slog.Warn("autoscale status query", "err", asErr)
	}
	asStatus := buildAutoscaleStatus(asEvent, asFound, s.cfg.Runtime.Autoscale.Cooldown)
	envelope["autoscale_status"] = asStatus
	envelope["global_autoscale_enabled"] = s.cfg.Runtime.Autoscale.Enabled
	// runtime_mode + resource_enforcement let the UI render per-app limits
	// honestly. Limits are enforced in BOTH native and docker mode (native applies
	// them as cgroup v2 memory.max / cpu.max), but native enforcement is best-effort
	// and requires the controllers to be delegated to the service. resource_enforcement
	// reports whether each controller is actually delegated, so the Resources controls
	// can warn when a limit would be silently ignored.
	envelope["runtime_mode"] = s.cfg.Runtime.Mode
	if s.manager != nil {
		// Aggregate across the app's actual placement tiers, not just the default
		// tier, so a tier-placed app reports enforcement honestly.
		memEnf, cpuEnf := s.manager.ResourceEnforcement(s.tiersForApp(app)...)
		envelope["resource_enforcement"] = map[string]bool{"memory": memEnf, "cpu": cpuEnf}
	}
	// release_number/released_at drive the header's "vN · date" display (the
	// human-friendly version). Omitted entirely until the app has a succeeded
	// deploy, so the UI hides the version chip rather than showing "v0".
	if rel, releasedAt, relVersion, ok := s.store.CurrentRelease(app.ID); ok {
		envelope["release_number"] = rel
		envelope["released_at"] = releasedAt
		// The live succeeded bundle's epoch id, for the "bundle …" hover. NOT
		// current_version, which is the newest row regardless of status.
		envelope["released_version"] = relVersion
	}
	w.Header().Set(hdrResourceRevision, resourceRevision)
	writeJSON(w, http.StatusOK, envelope)
}

func (s *Server) handlePatchApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	var raw map[string]json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &raw); err != nil {
			writeError(w, http.StatusBadRequest, "bad request")
			return
		}
	}

	// Parse and validate all fields first so a bad request never causes a
	// partial write (e.g. hibernate_timeout persisted while name rejected).
	var (
		hibernateTimeout    *int
		setHibernateTimeout bool
		newName             string
		setName             bool
		newDescription      string
		setDescription      bool
		newIconEmoji        string
		setIconEmoji        bool
		newProjectSlug      string
		setProjectSlug      bool
		memoryLimitMB       *int
		setMemoryLimitMB    bool
		cpuQuotaPercent     *int
		setCPUQuotaPercent  bool
		newReplicas         int
		setReplicas         bool
		newMaxSessions      int
		setMaxSessions      bool
		newRenderSeconds    float64
		setRenderSeconds    bool
		newIdentityHeaders  *bool
		setIdentityHeaders  bool
		newMinWarmReplicas  int
		setMinWarmReplicas  bool
		newManagedBy        *string
		setManagedBy        bool
		placementKeyPresent bool
		setPlacement        bool // a non-null placement object was provided
		clearPlacement      bool // an explicit null placement was provided
		placementJSON       string
		placementTotal      int
		newPlacementTiers   []string // tiers (count>0) the new placement would run on
		setAutoscale        bool
		autoEnabled         bool
		autoMin             int
		autoMax             int
		autoTarget          float64

		newWorkerIsolation          string
		setWorkerIsolation          bool
		newWorkerGroupedSize        int
		setWorkerGroupedSize        bool
		newWorkerMaxWorkers         int
		setWorkerMaxWorkers         bool
		newWorkerWarmSpares         int
		setWorkerWarmSpares         bool
		newWorkerMaxSessionLifetime int
		setWorkerMaxSessionLifetime bool

		newEphemeralDataAck bool
		setEphemeralDataAck bool
	)

	if rawVal, present := raw["hibernate_timeout_minutes"]; present {
		var timeout *int
		if err := json.Unmarshal(rawVal, &timeout); err != nil {
			writeError(w, http.StatusBadRequest, "hibernate_timeout_minutes must be an integer or null")
			return
		}
		if timeout != nil && *timeout < 0 {
			writeError(w, http.StatusBadRequest, "hibernate_timeout_minutes must be >= 0")
			return
		}
		hibernateTimeout, setHibernateTimeout = timeout, true
	}

	if rawVal, present := raw["name"]; present {
		var name string
		if err := json.Unmarshal(rawVal, &name); err != nil {
			writeError(w, http.StatusBadRequest, "name must be a string")
			return
		}
		normalized, verr := appmetaspec.NormalizeName(name)
		if verr != nil {
			writeError(w, http.StatusBadRequest, verr.Error())
			return
		}
		newName, setName = normalized, true
	}

	if rawVal, present := raw["description"]; present {
		var desc string
		if err := json.Unmarshal(rawVal, &desc); err != nil {
			writeError(w, http.StatusBadRequest, "description must be a string")
			return
		}
		normalized, verr := appmetaspec.NormalizeDescription(desc)
		if verr != nil {
			writeError(w, http.StatusBadRequest, verr.Error())
			return
		}
		newDescription, setDescription = normalized, true
	}

	if rawVal, present := raw["icon_emoji"]; present {
		if err := json.Unmarshal(rawVal, &newIconEmoji); err != nil {
			writeError(w, http.StatusBadRequest, "icon_emoji must be a string")
			return
		}
		// "" means clear; only a non-empty value is validated as an emoji.
		if newIconEmoji != "" {
			if verr := deploy.ValidateIconEmoji(newIconEmoji); verr != nil {
				writeError(w, http.StatusBadRequest, verr.Error())
				return
			}
		}
		setIconEmoji = true
	}

	if rawVal, present := raw["project_slug"]; present {
		var projectSlug string
		if err := json.Unmarshal(rawVal, &projectSlug); err != nil {
			writeError(w, http.StatusBadRequest, "project_slug must be a string")
			return
		}
		newProjectSlug, setProjectSlug = strings.TrimSpace(projectSlug), true
		// Same rule as create: "" clears the project, anything else must be a
		// legal slug. Without this the PATCH path is a hole through which any
		// free text reaches the grouping key and the projects table.
		if newProjectSlug != "" && !slugpkg.Valid(newProjectSlug) {
			writeError(w, http.StatusBadRequest, "project_slug must be "+slugpkg.HumanRule)
			return
		}
	}

	if rawVal, present := raw["memory_limit_mb"]; present {
		var v *int
		if err := json.Unmarshal(rawVal, &v); err != nil {
			writeError(w, http.StatusBadRequest, "memory_limit_mb must be an integer or null")
			return
		}
		// Keep the historical >=0 floor (no breaking change; the stricter >=16
		// floor is manifest/UI-only) but bound the top so an absurd value cannot
		// overflow when a runtime multiplies MiB into bytes.
		if v != nil && (*v < 0 || *v > deploy.MaxMemoryLimitMB) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("memory_limit_mb must be between 0 and %d", deploy.MaxMemoryLimitMB))
			return
		}
		memoryLimitMB, setMemoryLimitMB = v, true
	}

	if rawVal, present := raw["cpu_quota_percent"]; present {
		var v *int
		if err := json.Unmarshal(rawVal, &v); err != nil {
			writeError(w, http.StatusBadRequest, "cpu_quota_percent must be an integer or null")
			return
		}
		if v != nil {
			if err := deploy.ValidateCPUQuotaPercent(*v); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		cpuQuotaPercent, setCPUQuotaPercent = v, true
	}

	if rawVal, present := raw["replicas"]; present {
		var n int
		if err := json.Unmarshal(rawVal, &n); err != nil {
			writeError(w, http.StatusBadRequest, "replicas must be an integer")
			return
		}
		if n < 1 {
			writeError(w, http.StatusBadRequest, "replicas must be >= 1")
			return
		}
		if s.cfg.Runtime.MaxReplicas > 0 && n > s.cfg.Runtime.MaxReplicas {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("replicas must be between 1 and %d", s.cfg.Runtime.MaxReplicas))
			return
		}
		newReplicas, setReplicas = n, true
	}

	if rawVal, present := raw["max_sessions_per_replica"]; present {
		var n int
		if err := json.Unmarshal(rawVal, &n); err != nil {
			writeError(w, http.StatusBadRequest, "max_sessions_per_replica must be an integer")
			return
		}
		// 0 explicitly means "fall back to the runtime default"; upper bound
		// mirrors the DB CHECK constraint (migration 012).
		if n < 0 || n > 1000 {
			writeError(w, http.StatusBadRequest, "max_sessions_per_replica must be between 0 and 1000")
			return
		}
		newMaxSessions, setMaxSessions = n, true
	}

	if rawVal, present := raw["render_seconds"]; present {
		var v float64
		if err := json.Unmarshal(rawVal, &v); err != nil {
			writeError(w, http.StatusBadRequest, "render_seconds must be a number")
			return
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			writeError(w, http.StatusBadRequest, "render_seconds must be a finite number")
			return
		}
		if v < 0 || v > 600 {
			writeError(w, http.StatusBadRequest, "render_seconds must be between 0 and 600")
			return
		}
		newRenderSeconds, setRenderSeconds = v, true
	}

	if rawVal, present := raw["identity_headers"]; present {
		var v *bool
		if err := json.Unmarshal(rawVal, &v); err != nil {
			writeError(w, http.StatusBadRequest, "identity_headers must be a boolean or null")
			return
		}
		newIdentityHeaders, setIdentityHeaders = v, true
	}

	if rawVal, present := raw["min_warm_replicas"]; present {
		var n int
		if err := json.Unmarshal(rawVal, &n); err != nil {
			writeError(w, http.StatusBadRequest, "min_warm_replicas must be an integer")
			return
		}
		if n < 0 || n > 1000 {
			writeError(w, http.StatusBadRequest, "min_warm_replicas must be between 0 and 1000")
			return
		}
		newMinWarmReplicas, setMinWarmReplicas = n, true
	}
	if rawVal, present := raw["ephemeral_data_ack"]; present {
		var b bool
		if err := json.Unmarshal(rawVal, &b); err != nil {
			writeError(w, http.StatusBadRequest, "ephemeral_data_ack must be a boolean")
			return
		}
		newEphemeralDataAck, setEphemeralDataAck = b, true
	}

	if rawVal, present := raw["managed_by"]; present {
		var v *string
		if err := json.Unmarshal(rawVal, &v); err != nil {
			writeError(w, http.StatusBadRequest, "managed_by must be a string or null")
			return
		}
		newManagedBy, setManagedBy = v, true
	}

	if rawVal, present := raw["autoscale"]; present {
		var patch struct {
			Enabled     *bool    `json:"enabled"`
			MinReplicas *int     `json:"min_replicas"`
			MaxReplicas *int     `json:"max_replicas"`
			Target      *float64 `json:"target"`
		}
		if err := json.Unmarshal(rawVal, &patch); err != nil {
			writeError(w, http.StatusBadRequest, "autoscale must be an object")
			return
		}
		// Merge over the current values so a caller can update fields one at a
		// time; the DB write replaces all four columns atomically.
		autoEnabled = app.AutoscaleEnabled
		autoMin = app.AutoscaleMinReplicas
		autoMax = app.AutoscaleMaxReplicas
		autoTarget = app.AutoscaleTarget
		if patch.Enabled != nil {
			autoEnabled = *patch.Enabled
		}
		if patch.MinReplicas != nil {
			autoMin = *patch.MinReplicas
		}
		if patch.MaxReplicas != nil {
			autoMax = *patch.MaxReplicas
		}
		if patch.Target != nil {
			autoTarget = *patch.Target
		}
		// target is validated regardless of enabled so a stored value is never
		// out of range; 0 means "inherit the runtime default".
		if autoTarget < 0 || autoTarget > 1 {
			writeError(w, http.StatusBadRequest, "autoscale.target must be in [0,1] (0 inherits the runtime default)")
			return
		}
		// Bounds are persisted even while disabled (so a re-enable restores the
		// operator's last choice), so they must satisfy the stored column range
		// regardless of the enabled flag. Without this a value outside [0,1000]
		// would pass the handler and only fail the DB CHECK, returning a 500 after
		// sibling fields in the same PATCH were already committed.
		if autoMin < 0 || autoMin > maxStoredReplicas {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("autoscale.min_replicas must be between 0 and %d", maxStoredReplicas))
			return
		}
		if autoMax < 0 || autoMax > maxStoredReplicas {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("autoscale.max_replicas must be between 0 and %d", maxStoredReplicas))
			return
		}
		if autoEnabled {
			if autoMin < 1 {
				writeError(w, http.StatusBadRequest, "autoscale.min_replicas must be >= 1 when enabled")
				return
			}
			if autoMax < autoMin {
				writeError(w, http.StatusBadRequest, "autoscale.max_replicas must be >= min_replicas")
				return
			}
			if s.cfg.Runtime.MaxReplicas > 0 && autoMax > s.cfg.Runtime.MaxReplicas {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("autoscale.max_replicas must be <= %d", s.cfg.Runtime.MaxReplicas))
				return
			}
		}
		setAutoscale = true
	}

	if rawVal, present := raw["worker_isolation"]; present {
		var v string
		if err := json.Unmarshal(rawVal, &v); err != nil {
			writeError(w, http.StatusBadRequest, "worker_isolation must be a string")
			return
		}
		newWorkerIsolation, setWorkerIsolation = v, true
	}

	if rawVal, present := raw["worker_grouped_size"]; present {
		var n int
		if err := json.Unmarshal(rawVal, &n); err != nil {
			writeError(w, http.StatusBadRequest, "worker_grouped_size must be an integer")
			return
		}
		newWorkerGroupedSize, setWorkerGroupedSize = n, true
	}

	if rawVal, present := raw["worker_max_workers"]; present {
		var n int
		if err := json.Unmarshal(rawVal, &n); err != nil {
			writeError(w, http.StatusBadRequest, "worker_max_workers must be an integer")
			return
		}
		newWorkerMaxWorkers, setWorkerMaxWorkers = n, true
	}

	if rawVal, present := raw["worker_warm_spares"]; present {
		var n int
		if err := json.Unmarshal(rawVal, &n); err != nil {
			writeError(w, http.StatusBadRequest, "worker_warm_spares must be an integer")
			return
		}
		newWorkerWarmSpares, setWorkerWarmSpares = n, true
	}

	if rawVal, present := raw["worker_max_session_lifetime_secs"]; present {
		var n int
		if err := json.Unmarshal(rawVal, &n); err != nil {
			writeError(w, http.StatusBadRequest, "worker_max_session_lifetime_secs must be an integer")
			return
		}
		newWorkerMaxSessionLifetime, setWorkerMaxSessionLifetime = n, true
	}

	// Validate the fully-resolved worker settings when any worker field was set.
	// orString/orInt merge the incoming value (when set) with the app's current
	// value so the validator sees the complete dial even when only one field is
	// being changed. A memory-limit change re-runs the same math: on an elastic
	// app it moves the per-worker budget term (multiplex no-ops inside the
	// validator), so a raise that busts the host budget cannot slip in alone.
	// Advisories are collected here and attached only to the success response:
	// a header set before a later validation step would ride along on the
	// error reply that step writes.
	var warnings []string
	if setWorkerIsolation || setWorkerGroupedSize || setWorkerMaxWorkers || setWorkerWarmSpares || setWorkerMaxSessionLifetime || setMemoryLimitMB {
		ws := config.WorkerSettings{
			// Resolve through the fleet default exactly like the runtime does
			// (SetPoolMode below): an app with empty stored isolation inherits
			// an elastic fleet default and must be budget-checked as elastic.
			Isolation: config.WorkerIsolationMode(deploy.ResolveWorkerIsolation(
				orString(newWorkerIsolation, setWorkerIsolation, app.WorkerIsolation),
				s.cfg.Runtime.DefaultWorkerIsolation)),
			GroupedSize:        orInt(newWorkerGroupedSize, setWorkerGroupedSize, app.WorkerGroupedSize),
			MaxWorkers:         orInt(newWorkerMaxWorkers, setWorkerMaxWorkers, app.WorkerMaxWorkers),
			WarmSpares:         orInt(newWorkerWarmSpares, setWorkerWarmSpares, app.WorkerWarmSpares),
			MaxSessionLifetime: orInt(newWorkerMaxSessionLifetime, setWorkerMaxSessionLifetime, app.WorkerMaxSessionLifetimeSecs),
		}
		memMB, _ := s.cfg.Runtime.DefaultResourcesForApp(app)
		// Validate and warn against the POST-patch memory limit: a combined
		// request can change the limit and the worker dial together, and the
		// budget math must see the state the patch is about to persist.
		patchedLimit := app.MemoryLimitMB
		if setMemoryLimitMB {
			patchedLimit = memoryLimitMB
		}
		effMemMB := deploy.ResolveMemoryLimitMB(patchedLimit, memMB)
		if err := config.ValidateWorkerSettings(ws, s.clustered, effMemMB, s.cfg.HostBudgetMB()); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Valid but unguarded elastic isolation is accepted with a warning so
		// the operator learns the host has no memory backstop (the CLI relays
		// this header to stderr).
		if warn := config.WorkerBudgetWarning(ws, effMemMB, s.cfg.HostBudgetMB(), s.cfg.MinAvailableMemoryMB()); warn != "" {
			warnings = append(warnings, warn)
		}
	}

	// A keep-warm floor on an elastic pool is accepted but inert (see
	// config.MinWarmReplicasInertWarning). Warn on the change that produces the
	// combination, whichever side of it this request sets, and evaluate the
	// post-patch state so a request that sets both is judged as a whole.
	if setMinWarmReplicas || setWorkerIsolation {
		iso := config.WorkerIsolationMode(deploy.ResolveWorkerIsolation(
			orString(newWorkerIsolation, setWorkerIsolation, app.WorkerIsolation),
			s.cfg.Runtime.DefaultWorkerIsolation))
		if warn := config.MinWarmReplicasInertWarning(orInt(newMinWarmReplicas, setMinWarmReplicas, app.MinWarmReplicas), iso); warn != "" {
			warnings = append(warnings, warn)
		}
	}

	if rawVal, present := raw["placement"]; present {
		placementKeyPresent = true
		if string(rawVal) == "null" {
			// Explicit null clears placement: all replicas fall back to the
			// default tier, keeping the current replica count.
			clearPlacement = true
		} else {
			var pm map[string]int
			if err := json.Unmarshal(rawVal, &pm); err != nil {
				writeError(w, http.StatusBadRequest,
					"placement must be an object mapping tier names to replica counts, or null")
				return
			}
			known := make(map[string]bool)
			for _, name := range s.cfg.Runtime.TierOrder() {
				known[name] = true
			}
			total := 0
			for tier, count := range pm {
				if !known[tier] {
					writeError(w, http.StatusBadRequest,
						fmt.Sprintf("placement: %q is not a configured tier", tier))
					return
				}
				if count < 0 {
					writeError(w, http.StatusBadRequest,
						fmt.Sprintf("placement: tier %q count must be >= 0", tier))
					return
				}
				total += count
			}
			if total < 1 {
				writeError(w, http.StatusBadRequest, "placement: total replica count must be >= 1")
				return
			}
			if s.cfg.Runtime.MaxReplicas > 0 && total > s.cfg.Runtime.MaxReplicas {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("placement: total replicas must be between 1 and %d", s.cfg.Runtime.MaxReplicas))
				return
			}
			b, _ := json.Marshal(pm)
			placementJSON, placementTotal, setPlacement = string(b), total, true
			for tier, count := range pm {
				if count > 0 {
					newPlacementTiers = append(newPlacementTiers, tier)
				}
			}
		}
	}

	// replicas and placement both describe the pool shape, so a single request
	// may carry only one of them.
	if placementKeyPresent && setReplicas {
		writeError(w, http.StatusBadRequest, "set either replicas or placement, not both")
		return
	}

	// Durable-data guard: a placement change must not move a data-using app onto
	// an ephemeral tier — SetAppPlacement + the async redeploy it triggers would
	// otherwise silently move the live pool (and its data) onto storage that is
	// lost on restart. Evaluated against the NEW tiers, before any write.
	if setPlacement || clearPlacement {
		newTiers := newPlacementTiers
		if clearPlacement {
			newTiers = []string{s.cfg.Runtime.DefaultTierName()}
		}
		if tier, blocked, gerr := s.ephemeralDataBlockForTiers(app, nil, newTiers); gerr != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		} else if blocked {
			writeError(w, http.StatusUnprocessableEntity, ephemeralDataDeployMsg(tier))
			return
		}
	}
	// Changing the bare replica count on an app that already uses tier placement
	// would drift the stored placement from the replica count. Require the caller
	// to update (or clear) placement instead.
	if setReplicas && len(app.PlacementMap()) > 0 {
		writeError(w, http.StatusBadRequest, "app uses tier placement; update placement instead of replicas")
		return
	}

	// In native mode, memory_limit_mb and cpu_quota_percent are enforced via a
	// per-app cgroup v2 (memory.max / cpu.max). Enforcement requires the relevant
	// controller to be delegated (systemd Delegate=memory / Delegate=cpu); the
	// runtime logs a warning and runs the replica uncapped if a controller is not
	// delegated. Both are also enforced by the Docker runtime. No native-mode
	// rejection is needed.

	// Write-time rejection when a per-app memory/CPU limit exceeds the Fargate
	// task ceiling (gated on allTiersFargate, the platform's single-ceiling rule;
	// mixed-tier deployments are guarded at RunTask). Without it the limit would
	// persist and Fargate silently clamp it, leaving the DB/UI/audit claiming a
	// higher limit than is enforced. The manifest deploy path runs the same check.
	{
		var memArg, cpuArg *int
		if setMemoryLimitMB {
			memArg = memoryLimitMB
		}
		if setCPUQuotaPercent {
			cpuArg = cpuQuotaPercent
		}
		if msg := s.fargateLimitViolation(memArg, cpuArg); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
	}

	if checkAppPreconditions(w, r, app) {
		return
	}

	// Capture old worker values before the update so the audit record can include
	// old/new pairs, consistent with how memory_limit_mb and cpu_quota_percent are
	// recorded. The app object still holds the pre-update state at this point.
	oldWorkerIsolation := app.WorkerIsolation
	oldWorkerGroupedSize := app.WorkerGroupedSize
	oldWorkerMaxWorkers := app.WorkerMaxWorkers
	oldWorkerWarmSpares := app.WorkerWarmSpares
	oldWorkerMaxSessionLifetime := app.WorkerMaxSessionLifetimeSecs
	oldProjectSlug := app.ProjectSlug

	// Apply core settings in a single transaction so a storage failure mid-write
	// never leaves the row half-updated. The managed_by marker is a separate
	// follow-up write (SetAppManagedBy) that runs after this transaction commits;
	// the post-patch refetch exposes the final consistent state to the caller.
	priorStatus, _, priorMemoryLimitMB, priorCPUQuotaPercent, projectCreated, err := s.store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug:                         slug,
		SetHibernate:                 setHibernateTimeout,
		HibernateMinutes:             hibernateTimeout,
		SetName:                      setName,
		Name:                         newName,
		SetProjectSlug:               setProjectSlug,
		ProjectSlug:                  newProjectSlug,
		SetMemoryLimitMB:             setMemoryLimitMB,
		MemoryLimitMB:                memoryLimitMB,
		SetCPUQuotaPercent:           setCPUQuotaPercent,
		CPUQuotaPercent:              cpuQuotaPercent,
		SetReplicas:                  setReplicas,
		Replicas:                     newReplicas,
		SetMaxSessions:               setMaxSessions,
		MaxSessions:                  newMaxSessions,
		SetRenderSeconds:             setRenderSeconds,
		RenderSeconds:                newRenderSeconds,
		SetIdentityHeaders:           setIdentityHeaders,
		IdentityHeaders:              newIdentityHeaders,
		SetMinWarmReplicas:           setMinWarmReplicas,
		MinWarmReplicas:              newMinWarmReplicas,
		SetWorkerIsolation:           setWorkerIsolation,
		WorkerIsolation:              newWorkerIsolation,
		SetWorkerGroupedSize:         setWorkerGroupedSize,
		WorkerGroupedSize:            newWorkerGroupedSize,
		SetWorkerMaxWorkers:          setWorkerMaxWorkers,
		WorkerMaxWorkers:             newWorkerMaxWorkers,
		SetWorkerWarmSpares:          setWorkerWarmSpares,
		WorkerWarmSpares:             newWorkerWarmSpares,
		SetWorkerMaxSessionLifetime:  setWorkerMaxSessionLifetime,
		WorkerMaxSessionLifetimeSecs: newWorkerMaxSessionLifetime,
	})
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		reqLog(r).Error("patch app settings failed", "slug", slug, "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if setManagedBy {
		if err := s.store.SetAppManagedBy(slug, newManagedBy); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}

	if setDescription {
		if err := s.store.SetAppDescription(slug, newDescription); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}

	if setIconEmoji {
		// Non-empty is explicit intent to replace any uploaded image; "" clears
		// the emoji only, so a retained image resurfaces in the display order.
		var ierr error
		if newIconEmoji == "" {
			ierr = s.store.SetAppIconEmoji(slug, "")
		} else {
			ierr = s.store.SetAppIconEmojiExclusive(slug, newIconEmoji)
		}
		if ierr != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		s.auditIcon(r, db.AuditAppIconEmoji, slug, map[string]any{"emoji": newIconEmoji})
	}

	if setEphemeralDataAck {
		if err := s.store.UpdateAppEphemeralDataAck(app.ID, newEphemeralDataAck); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}

	// Placement is the authoritative writer for replica_placement + the derived
	// replica count, so it runs after the core settings transaction. Clearing
	// keeps the current replica count (all replicas on the default tier).
	if setPlacement || clearPlacement {
		total := placementTotal
		if clearPlacement {
			total, placementJSON = app.Replicas, ""
		}
		if err := s.store.SetAppPlacement(app.ID, placementJSON, total); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}

	// Autoscale config is independent of the pool shape, so it never triggers a
	// redeploy; the controller picks up the change on its next scan.
	if setAutoscale {
		if err := s.store.SetAppAutoscale(db.SetAppAutoscaleParams{
			AppID:       app.ID,
			Enabled:     autoEnabled,
			MinReplicas: autoMin,
			MaxReplicas: autoMax,
			Target:      autoTarget,
		}); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}

	// A resource-limit change must reach the running replicas: the cgroup/
	// container ceiling is set at spawn, so the pool is cycled (same as a
	// replica-count change). The prior values come from inside PatchAppSettings'
	// transaction (not the pre-write app snapshot), so detection is free of a
	// time-of-check/time-of-use race with a concurrent PATCH. Per-field "changed"
	// (not merely "present in the PATCH") gates both the redeploy and the audit
	// entry, so a no-op PATCH neither restarts nor logs a phantom change.
	oldMemoryLimitMB, oldCPUQuotaPercent := priorMemoryLimitMB, priorCPUQuotaPercent
	memChanged := setMemoryLimitMB && !intPtrEqual(oldMemoryLimitMB, memoryLimitMB)
	cpuChanged := setCPUQuotaPercent && !intPtrEqual(oldCPUQuotaPercent, cpuQuotaPercent)
	resourceChanged := memChanged || cpuChanged

	// Post-commit side effects. These only take effect once the settings are
	// durably persisted.
	if setMaxSessions && s.proxy != nil {
		s.proxy.SetPoolCap(slug,
			deploy.ResolveMaxSessionsPerReplica(newMaxSessions, s.cfg.Runtime.DefaultMaxSessionsPerReplica))
	}
	if setRenderSeconds && s.proxy != nil {
		s.proxy.ApplyRenderPacing(slug, newRenderSeconds)
	}
	if setIdentityHeaders && s.proxy != nil {
		s.proxy.SetPoolIdentityHeaders(slug,
			deploy.ResolveIdentityHeaders(newIdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()))
	}
	// SetPoolMode on any worker-field change so a live isolation reconfiguration
	// reshapes the pool without requiring a full redeploy cycle. The isolation
	// change will also trigger redeployApp (below), which calls deploy.Run and
	// therefore sets it again; this call covers the stopped-app case too.
	if (setWorkerIsolation || setWorkerGroupedSize || setWorkerMaxWorkers || setWorkerWarmSpares) && s.proxy != nil {
		effectiveIsolation := app.WorkerIsolation
		if setWorkerIsolation {
			effectiveIsolation = newWorkerIsolation
		}
		effectiveGroupedSize := app.WorkerGroupedSize
		if setWorkerGroupedSize {
			effectiveGroupedSize = newWorkerGroupedSize
		}
		effectiveMaxWorkers := app.WorkerMaxWorkers
		if setWorkerMaxWorkers {
			effectiveMaxWorkers = newWorkerMaxWorkers
		}
		effectiveWarmSpares := app.WorkerWarmSpares
		if setWorkerWarmSpares {
			effectiveWarmSpares = newWorkerWarmSpares
		}
		s.proxy.SetPoolMode(slug,
			config.WorkerIsolationMode(deploy.ResolveWorkerIsolation(effectiveIsolation, s.cfg.Runtime.DefaultWorkerIsolation)),
			effectiveGroupedSize, effectiveMaxWorkers)
		s.proxy.SetPoolWarmSpares(slug, effectiveWarmSpares)
		// A warm-target-only edit is hot: preserve assigned workers and converge
		// the spare floor in place. Structural worker changes redeploy below and
		// deploy.Run reconciles only after the bundle is prepared. Never provision
		// from a settings edit while the app is stopped.
		structuralWorkerChanged := setWorkerIsolation || setWorkerGroupedSize || setWorkerMaxWorkers
		if setWorkerWarmSpares && !structuralWorkerChanged && priorStatus == "running" {
			s.proxy.ReconcileElasticWarmSpares(slug)
		}
	}
	workerChanged := setWorkerIsolation || setWorkerGroupedSize || setWorkerMaxWorkers || setWorkerMaxSessionLifetime
	if (setReplicas || setPlacement || clearPlacement || resourceChanged || workerChanged) && priorStatus == "running" {
		// Mark in-flight synchronously before launching the goroutine so the
		// first GET after this PATCH returns observes the redeploy even though
		// the app row still reads "running". The redeploy goroutine clears it.
		s.markRedeployInFlight(slug)
		go s.redeployApp(slug)
	}

	var fetchErr error
	app, fetchErr = s.store.GetAppBySlug(slug)
	if fetchErr != nil {
		if errors.Is(fetchErr, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// Audit any non-resource field touch exactly as before (these log even with
	// an empty detail). For resource limits, audit only a real change, so a no-op
	// resource-only PATCH neither redeploys nor logs a phantom update_app event.
	nonResourceTouched := setHibernateTimeout || setName || setDescription || setProjectSlug || setIdentityHeaders ||
		setReplicas || setMaxSessions || setRenderSeconds || setMinWarmReplicas || setManagedBy ||
		setPlacement || clearPlacement || setAutoscale ||
		setWorkerIsolation || setWorkerGroupedSize || setWorkerMaxWorkers || setWorkerWarmSpares || setWorkerMaxSessionLifetime ||
		setEphemeralDataAck
	if u := auth.UserFromContext(r.Context()); u != nil && (nonResourceTouched || memChanged || cpuChanged) {
		detail := patchAppAuditDetail(
			setMinWarmReplicas, newMinWarmReplicas,
			setRenderSeconds, newRenderSeconds,
			memChanged, oldMemoryLimitMB, memoryLimitMB,
			cpuChanged, oldCPUQuotaPercent, cpuQuotaPercent,
			setWorkerIsolation, oldWorkerIsolation, newWorkerIsolation,
			setWorkerGroupedSize, oldWorkerGroupedSize, newWorkerGroupedSize,
			setWorkerMaxWorkers, oldWorkerMaxWorkers, newWorkerMaxWorkers,
			setWorkerWarmSpares, oldWorkerWarmSpares, newWorkerWarmSpares,
			setWorkerMaxSessionLifetime, oldWorkerMaxSessionLifetime, newWorkerMaxSessionLifetime,
			setProjectSlug, oldProjectSlug, newProjectSlug)
		s.logAuditEvent(r, db.AuditEventParams{
			UserID: &u.ID, Action: "update_app", ResourceType: "app",
			ResourceID: slug, Detail: detail, IPAddress: s.ClientIP(r), RunID: s.knownFleetRunID(r),
		})
	}
	if projectCreated {
		s.audit(r, db.AuditProjectCreate, "project", newProjectSlug, `{"implicit":true}`)
	}
	effectiveRenderSeconds := app.RenderSeconds
	if setRenderSeconds {
		effectiveRenderSeconds = newRenderSeconds
	}
	effectiveCap := deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica)
	resp := map[string]any{"app": app}
	if block := s.buildRenderPacingBlock(effectiveRenderSeconds, effectiveCap); block != nil {
		resp["render_pacing"] = block
	}
	for _, warn := range warnings {
		addWarningHeader(w, warn)
	}
	writeJSON(w, http.StatusOK, resp)
}

// intPtrEqual reports whether two *int hold the same nullness and value.
func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// orString returns newVal when set is true, otherwise returns fallback.
// Used to merge a PATCH field with the app's current value so a validator
// that needs the full dial sees the resolved setting even when only one
// field in a group was changed.
func orString(newVal string, set bool, fallback string) string {
	if set {
		return newVal
	}
	return fallback
}

// orInt returns newVal when set is true, otherwise returns fallback.
func orInt(newVal int, set bool, fallback int) int {
	if set {
		return newVal
	}
	return fallback
}

// addWarningHeader attaches msg to the response's X-ShinyHub-Warning header,
// the advisory channel for a request that succeeded but deserves a heads-up.
// The CLI and the dashboard read the header with Get, which returns only the
// first value, so a second warning on the same response is joined onto the
// first rather than sent as a separate value they would never see.
func addWarningHeader(w http.ResponseWriter, msg string) {
	if prev := w.Header().Get("X-ShinyHub-Warning"); prev != "" {
		msg = prev + "; " + msg
	}
	w.Header().Set("X-ShinyHub-Warning", msg)
}

// patchAppAuditDetail builds a JSON detail blob for the update_app audit event,
// recording only the fields that were actually changed in this PATCH. Resource
// limits are recorded as {old,new} (nil renders as JSON null = inherit).
func patchAppAuditDetail(
	setMinWarmReplicas bool, minWarmReplicas int,
	setRenderSeconds bool, renderSeconds float64,
	setMemoryLimitMB bool, oldMem, newMem *int,
	setCPUQuotaPercent bool, oldCPU, newCPU *int,
	setWorkerIsolation bool, oldWorkerIsolation, newWorkerIsolation string,
	setWorkerGroupedSize bool, oldWorkerGroupedSize, newWorkerGroupedSize int,
	setWorkerMaxWorkers bool, oldWorkerMaxWorkers, newWorkerMaxWorkers int,
	setWorkerWarmSpares bool, oldWorkerWarmSpares, newWorkerWarmSpares int,
	setWorkerMaxSessionLifetime bool, oldWorkerMaxSessionLifetime, newWorkerMaxSessionLifetime int,
	setProjectSlug bool, oldProject, newProject string,
) string {
	d := map[string]any{}
	if setMinWarmReplicas {
		d["min_warm_replicas"] = minWarmReplicas
	}
	if setRenderSeconds {
		d["render_seconds"] = renderSeconds
	}
	if setMemoryLimitMB {
		d["memory_limit_mb"] = map[string]any{"old": oldMem, "new": newMem}
	}
	if setCPUQuotaPercent {
		d["cpu_quota_percent"] = map[string]any{"old": oldCPU, "new": newCPU}
	}
	if setWorkerIsolation {
		d["worker_isolation"] = map[string]any{"old": oldWorkerIsolation, "new": newWorkerIsolation}
	}
	if setWorkerGroupedSize {
		d["worker_grouped_size"] = map[string]any{"old": oldWorkerGroupedSize, "new": newWorkerGroupedSize}
	}
	if setWorkerMaxWorkers {
		d["worker_max_workers"] = map[string]any{"old": oldWorkerMaxWorkers, "new": newWorkerMaxWorkers}
	}
	if setWorkerWarmSpares {
		d["worker_warm_spares"] = map[string]any{"old": oldWorkerWarmSpares, "new": newWorkerWarmSpares}
	}
	if setWorkerMaxSessionLifetime {
		d["worker_max_session_lifetime_secs"] = map[string]any{"old": oldWorkerMaxSessionLifetime, "new": newWorkerMaxSessionLifetime}
	}
	if setProjectSlug {
		d["project_slug"] = map[string]any{"old": oldProject, "new": newProject}
	}
	if len(d) == 0 {
		return ""
	}
	b, _ := json.Marshal(d)
	return string(b)
}

// activationPreparation picks the preparation mode for bringing an
// already-promoted bundle back up on a user-initiated path (restart, rollback).
//
// A bundle recorded as prepared has a built environment and has already run its
// post-deploy hooks, so repeating either is wrong: hooks are app-controlled and
// nothing guarantees a second run is safe. A bundle whose preparation state
// predates the record gets the full treatment, which is exactly what it has been
// getting until now.
//
// It differs from the restore path in the fallback only. Restore is unattended
// recovery and must never fail, so it degrades to PrepareBestEffort. Restart and
// rollback are deliberate actions with someone waiting on the result, so a
// preparation failure should surface rather than be swallowed.
func activationPreparation(prepared bool) deploy.PreparationMode {
	if prepared {
		return deploy.PrepareSkip
	}
	return deploy.PrepareRequired
}

// restorePreviousPool brings the previous live bundle back up after a failed
// deploy/rollback that already tore down the running pool. prev is the
// deployment that was authoritative before the attempt (nil if the app had
// never been deployed). Best-effort: a restore failure marks the app degraded
// rather than masking the original error.
func (s *Server) restorePreviousPool(slug string, app *db.App, prev *db.Deployment) {
	if prev == nil {
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
			slog.Error("restore: mark stopped (no previous deployment)", "slug", slug, "err", err)
		}
		return
	}
	if info, err := os.Stat(prev.BundleDir); err != nil || !info.IsDir() {
		slog.Error("restore: previous bundle missing; cannot recover pool", "slug", slug, "bundle", prev.BundleDir)
		if uerr := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"}); uerr != nil {
			slog.Error("restore: mark degraded", "slug", slug, "err", uerr)
		}
		return
	}
	defaultMem, defaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
	// Restoring is an activation, not a promotion: this bundle already served,
	// so its hooks must not run again and a rebuild must not be able to fail the
	// recovery. A deployment recorded as prepared skips preparation outright; one
	// whose state predates that record (including an elastic bundle from before
	// elastic apps were prepared at all) gets a best-effort build that is logged
	// but never fatal.
	preparation := deploy.PrepareBestEffort
	if prev.Prepared {
		preparation = deploy.PrepareSkip
	}
	result, err := s.deployRun(s.withTierPlacement(deploy.Params{
		Slug:                  slug,
		BundleDir:             prev.BundleDir,
		Replicas:              app.Replicas,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, defaultMem),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, defaultCPU),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
		IdentityHeaders:       deploy.ResolveIdentityHeaders(app.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()),
		ContentDigest:         prev.ContentDigest,
		DeploymentID:          prev.ID,
		AppVersion:            prev.Version,
		Preparation:           preparation,
	}, app))
	if err != nil {
		slog.Error("restore: previous pool failed to start; app is down", "slug", slug, "err", err)
		if uerr := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"}); uerr != nil {
			slog.Error("restore: mark degraded", "slug", slug, "err", uerr)
		}
		return
	}
	for _, rep := range result.Replicas {
		pid, port := rep.PID, rep.Port
		depID := prev.ID
		if uerr := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:        app.ID,
			Index:        rep.Index,
			PID:          &pid,
			Port:         &port,
			Status:       "running",
			Provider:     rep.Provider,
			Tier:         rep.Tier,
			EndpointURL:  rep.EndpointURL,
			WorkerID:     rep.WorkerID,
			AppVersion:   prev.Version,
			DesiredState: "running",
			DeploymentID: &depID,
		}); uerr != nil {
			slog.Error("restore: upsert replica", "slug", slug, "idx", rep.Index, "err", uerr)
		}
	}
	if uerr := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "running"}); uerr != nil {
		slog.Error("restore: persist running status", "slug", slug, "err", uerr)
	}
	slog.Info("restore: rolled back to previous deployment after failed attempt", "slug", slug, "version", prev.Version)
}

// restoreAfterFailedDeploy recovers from a failed deploy, except for an app the
// operator had stopped. Booting the previous bundle to recover from a bad new
// one would put a deliberately withdrawn app back into service on the old
// version, which is the exact outcome stopping it was meant to prevent - and a
// failing CI pipeline would be the thing that did it. Nothing was serving for a
// kept-stopped deploy, so the recovery is to record that it is still down.
func (s *Server) restoreAfterFailedDeploy(slug string, app *db.App, prev *db.Deployment, keepStopped bool) {
	if keepStopped {
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
			slog.Error("deploy: persist stopped status after failed deploy", "slug", slug, "err", err)
		}
		return
	}
	s.restorePreviousPool(slug, app, prev)
}

func (s *Server) handleDeployApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}
	runID := s.knownFleetRunID(r)
	origin := deploymentOriginForRequest(r, runID, false)

	if s.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "process manager not available")
		return
	}

	// A stopped app stays stopped through a deploy: the operator took it out of
	// service and a deploy must not silently override that. ?start=true is the
	// explicit override for a pipeline that wants deploy to mean "make it live".
	// Read before the upload so the decision reflects the state the caller
	// deployed against, not one a concurrent start changed mid-upload. Whether
	// the app was ever successfully deployed is resolved further down, from the
	// deployments table.
	stoppedByOperator := false

	maxSize := maxBundleUploadSize
	if cap := int64(s.cfg.Storage.MaxBundleMB); cap > 0 {
		maxSize = cap * 1024 * 1024
	}
	file, cleanup, err := readBundleUpload(w, r, maxSize)
	defer cleanup()
	if err != nil {
		switch err {
		case errBundleTooLarge:
			capMB := s.cfg.Storage.MaxBundleMB
			if capMB == 0 {
				capMB = 128
			}
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("bundle exceeds %d MiB cap", capMB))
		case errBundleMissing:
			writeError(w, http.StatusBadRequest, "bundle file required")
		default:
			writeError(w, http.StatusBadRequest, "bad request")
		}
		return
	}

	// Compute paths up front so a single defer can clean up the on-disk
	// artefacts on any failure path before the deploy is committed.
	version := fmt.Sprintf("%d", time.Now().UnixMilli())
	bundleZip := filepath.Join(s.cfg.Storage.AppsDir, slug, "bundles", version+".zip")
	bundleDir := deploy.BundleDir(s.cfg.Storage.AppsDir, slug, version)

	// keepFiles is flipped to true only once deploy.Run succeeds and the new
	// pool is actually serving the bundle. Any earlier failure — write,
	// extract, quota, deploy — leaves the apps tree as we found it.
	// Removal goes through fsx.RemoveAll: a failed deploy can leave build
	// output the standard remove cannot descend into (renv's sandbox is mode
	// 0555), which would leak the whole version dir against the app's quota.
	keepFiles := false
	dropUncommitted := func() {
		if !keepFiles {
			_ = fsx.RemoveAll(bundleDir)
			_ = os.Remove(bundleZip)
		}
	}
	defer dropUncommitted()

	if err := os.MkdirAll(filepath.Dir(bundleZip), 0o750); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out, err := os.OpenFile(bundleZip, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out.Close()

	if err := deploy.ExtractBundle(bundleZip, bundleDir); err != nil {
		slog.Error("deploy_extract_bundle_failed", "slug", slug, "err", err)
		if errors.Is(err, deploy.ErrBundleRejected) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if errors.Is(err, deploy.ErrBundleTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bundle extracted size exceeds limit")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Serialize the mutation phase so a concurrent restart/rollback/stop on
	// the same slug can't tear down the pool we are about to bring up.
	release := s.acquireDeployLock(slug)
	defer release()

	// Registered AFTER the lock defer so LIFO order removes uncommitted
	// files before the lock is released. The broad defer above only covers
	// pre-lock failures; without this one a quota-rejected deploy's files
	// would still be on disk, counted by DirSize against a concurrent
	// same-slug deploy that takes the lock the instant we release it.
	defer dropUncommitted()

	// Refresh and fence the resource under the same lock that protects every
	// deployment mutation. Uploading and extracting above only touched temporary
	// candidate files; a stale saved plan is rejected here before quota checks,
	// manifest settings, deployment rows, or the running pool can change.
	app, err = s.store.GetAppBySlug(slug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusConflict, "precondition failed: app no longer exists (re-run plan)")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if checkAppPreconditions(w, r, app) {
		return
	}
	stoppedByOperator = app.Status == "stopped" && r.URL.Query().Get("start") != "true"

	// Enforce per-app disk quota INSIDE the lock: the new extracted version
	// has already been written, so DirSize now reflects the post-deploy
	// footprint. Two concurrent same-slug deploys must not both observe a
	// pre-commit footprint and both pass; serializing the check makes the
	// quota authoritative. The defer above rolls the new files back if we
	// reject here.
	if s.cfg.Storage.AppQuotaMB > 0 {
		used, qErr := deploy.CheckAppQuota(s.cfg.Storage.AppsDir, s.cfg.Storage.AppDataDir, slug, s.cfg.Storage.AppQuotaMB)
		if qErr != nil {
			if errors.Is(qErr, deploy.ErrQuotaExceeded) {
				s.logQuotaRejected(r, slug, used)
				writeQuotaExceeded(w, used, s.cfg.Storage.AppQuotaMB)
				return
			}
			slog.Warn("quota check failed", "slug", slug, "err", qErr)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	// Load + server-policy-validate the manifest BEFORE tearing down the
	// running pool. A manifest rejected by policy (e.g. replicas > MaxReplicas)
	// returns 400 with the live pool undisturbed. manifest is kept in scope so
	// Phase B can apply [[schedule]] rows after CreateDeployment commits.
	manifest, err := deploy.LoadManifest(bundleDir)
	if err != nil {
		writeError(w, http.StatusBadRequest, "shinyhub.toml: "+err.Error())
		return
	}
	if manifest != nil {
		if ve := s.validateManifestForServer(app, manifest.App); ve != nil {
			writeError(w, http.StatusBadRequest, ve.Error())
			return
		}
	}

	// Reject an R app placed on a Fargate tier before the running pool is
	// touched. The reference Fargate runner image is Python-only and
	// HostPreparesDeps() is false for the fargate runtime, so the task would
	// start and fail at exec with no R interpreter or restored renv. A clear
	// 400 here beats a cryptic task-startup failure later.
	if deploy.DetectAppType(bundleDir) == "r" && s.appTargetsFargate(app) {
		writeError(w, http.StatusBadRequest,
			"R apps are not supported on Fargate tiers: the Fargate runner image is Python-only. Place this app on a native or docker tier.")
		return
	}

	// Durable-data guard: refuse to deploy a data-using app onto a tier whose
	// storage is ephemeral (bare Fargate with no durable backend) unless the
	// operator explicitly acknowledged it, before the running pool is torn down.
	// Otherwise app-data would be silently lost on restart/hibernation and not
	// shared across replicas.
	if tier, blocked, gerr := s.ephemeralDataDeployBlock(app, manifestCommand(manifest)); gerr != nil {
		slog.Error("durable-data guard check failed", "slug", slug, "err", gerr)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	} else if blocked {
		writeError(w, http.StatusUnprocessableEntity, ephemeralDataDeployMsg(tier))
		return
	}

	// Colocated-shared placement check, like every validation above, runs
	// BEFORE the pending row is recorded and the pool is torn down: a
	// rejected deploy leaves no deployment record and no state change.
	if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// Capture the current live deployment so a failed deploy can restore the
	// previous pool, then durably record the new deployment as 'pending'
	// BEFORE the running pool is touched. ListDeployments excludes pending
	// rows, so recovery/watcher/scheduler/rollback keep pointing at the
	// previous bundle until PromoteDeployment confirms the new pool is live.
	var prevActive *db.Deployment
	if existing, lerr := s.store.ListDeployments(app.ID); lerr == nil && len(existing) > 0 {
		prevActive = existing[0]
	}
	// An app that has never deployed successfully is "stopped" by default
	// rather than by anyone's decision, so a deploy starts it. The test is the
	// durable deployments row, which ListDeployments already filters to
	// succeeded ones: deploy_count is soft state a failed increment can leave
	// at zero, and a first deploy that FAILED must not make the retry that
	// fixes it leave the app down.
	keepStopped := stoppedByOperator && prevActive != nil

	pendingDep, err := s.store.BeginDeploymentWithOrigin(app.ID, version, bundleDir, runID, nil, origin)
	if err != nil {
		slog.Error("deploy: record pending deployment failed; running pool untouched", "slug", slug, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Record the content digest on the pending deployment. Computed from the
	// same accepted entries the extractor validates, so it matches the digest
	// the CLI computes from the produced bundle. Becomes authoritative only
	// once PromoteDeployment runs; a failed deploy never exposes it.
	// digest is hoisted so deploy.Params can carry it to the runtime.
	var digest string
	if zr, derr := zip.OpenReader(bundleZip); derr == nil {
		d, derr := bundle.DigestZipReader(&zr.Reader)
		zr.Close()
		if derr != nil {
			slog.Warn("deploy: content digest computation rejected bundle",
				"slug", slug, "version", version, "err", derr)
		} else {
			digest = d
			if serr := s.store.SetDeploymentDigest(pendingDep.ID, digest); serr != nil {
				slog.Error("deploy: failed to record content digest (non-fatal; next deploy self-heals)",
					"slug", slug, "version", version, "err", serr)
			}
		}
	} else {
		slog.Warn("deploy: could not re-open bundle for digest (non-fatal)",
			"slug", slug, "version", version, "err", derr)
	}

	// Stop existing instance before re-deploying; ignore the error since the
	// app may not have been running yet.
	_ = s.manager.Stop(slug)

	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	// Snapshot the pre-manifest [app] settings. If Phase A applies manifest
	// changes but the new pool then fails to start, restorePreviousPool brings
	// the OLD bundle back; the persisted settings (replicas / max_sessions /
	// hibernate) must be reverted to match it, otherwise the running old
	// bundle is governed by the new bundle's intended settings.
	preManifestApp := *app
	manifestApplied := false

	// Phase A: apply [app] manifest settings atomically before starting the new
	// pool. manager.Stop has already run so no process holds a replica index
	// that may be pruned. Validation already passed above; any error here is a
	// storage failure that leaves the app in an inconsistent state — mark it
	// degraded so the operator notices.
	var manifestSummary ManifestApplied
	if manifest != nil {
		// applyManifestAppSettings reconciles identity_headers unconditionally
		// (even when manifest.App.IsZero()) so that removing the key from the
		// manifest reverts the column to NULL. The other fields (hibernate,
		// replicas, max_sessions) keep declared-only semantics inside the
		// function; IsZero manifests produce no DB writes for those fields and
		// no audit event.
		if err := s.applyManifestAppSettings(r, app, manifest.App); err != nil {
			slog.Error("manifest [app] apply failed", "slug", slug, "err", err)
			_ = s.store.FailDeployment(pendingDep.ID)
			s.restoreAfterFailedDeploy(slug, app, prevActive, keepStopped)
			writeError(w, http.StatusInternalServerError, "manifest apply failed")
			return
		}
		manifestApplied = true
		manifestSummary.App = manifestAppliedSummary(manifest.App)
		// Read from preManifestApp, not the post-Phase-A refresh below: this
		// must reflect whether an image was uploaded BEFORE this deploy, and
		// the refresh could in principle observe a value the manifest write
		// itself just changed.
		manifestSummary.IconShadowedUpload = manifest.App.Icon != nil &&
			*manifest.App.Icon != "" && preManifestApp.IconMime != ""
		// Refresh so deploy.Run sees the updated replicas / max_sessions.
		if fresh, ferr := s.store.GetAppBySlug(slug); ferr == nil {
			app = fresh
		}
		// A manifest that declares a keep-warm floor or the isolation mode
		// may have just produced a floor that elastic isolation ignores. The
		// check runs against the refreshed row so it sees the manifest's
		// value combined with whatever the other knob already held, and it
		// is scoped to manifests that touch one of the two so an unrelated
		// redeploy does not repeat the advisory forever.
		if manifest.App.MinWarmReplicas != nil || (manifest.App.Worker != nil && manifest.App.Worker.Isolation != nil) {
			iso := config.WorkerIsolationMode(deploy.ResolveWorkerIsolation(app.WorkerIsolation, s.cfg.Runtime.DefaultWorkerIsolation))
			if warn := config.MinWarmReplicasInertWarning(app.MinWarmReplicas, iso); warn != "" {
				manifestSummary.Warnings = append(manifestSummary.Warnings, warn)
			}
		}
	}

	deployDefaultMem, deployDefaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
	deployResponse := newDeployResponder(w, r)
	result, err := s.deployRun(s.withTierPlacement(deploy.Params{
		Slug:                  slug,
		BundleDir:             bundleDir,
		Replicas:              app.Replicas,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, deployDefaultMem),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, deployDefaultCPU),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
		IdentityHeaders:       deploy.ResolveIdentityHeaders(app.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()),
		ContentDigest:         digest,
		DeploymentID:          pendingDep.ID,
		AppVersion:            version,
		// A stopped app is built and validated but not booted, so a broken
		// bundle is still rejected here rather than at start time.
		PrepareOnly: keepStopped,
		Progress:    deployResponse.event,
	}, app))
	if err != nil {
		reason := deployFailureMessage(err)
		kind := deployfail.Classify(err)
		slog.Error("deploy_run_failed", "slug", slug, "err", err)
		_ = s.store.FailDeploymentWithReason(pendingDep.ID, reason)
		// Revert manifest [app] settings so the restored old pool runs under
		// the settings it was deployed with, not the failed bundle's.
		if manifestApplied {
			if _, _, _, _, _, rerr := s.store.PatchAppSettings(db.PatchAppSettingsParams{
				Slug:                         slug,
				SetHibernate:                 true,
				HibernateMinutes:             preManifestApp.HibernateTimeoutMinutes,
				SetReplicas:                  true,
				Replicas:                     preManifestApp.Replicas,
				SetMaxSessions:               true,
				MaxSessions:                  preManifestApp.MaxSessionsPerReplica,
				SetMemoryLimitMB:             true,
				MemoryLimitMB:                preManifestApp.MemoryLimitMB,
				SetCPUQuotaPercent:           true,
				CPUQuotaPercent:              preManifestApp.CPUQuotaPercent,
				SetWorkerIsolation:           true,
				WorkerIsolation:              preManifestApp.WorkerIsolation,
				SetWorkerGroupedSize:         true,
				WorkerGroupedSize:            preManifestApp.WorkerGroupedSize,
				SetWorkerMaxWorkers:          true,
				WorkerMaxWorkers:             preManifestApp.WorkerMaxWorkers,
				SetWorkerWarmSpares:          true,
				WorkerWarmSpares:             preManifestApp.WorkerWarmSpares,
				SetWorkerMaxSessionLifetime:  true,
				WorkerMaxSessionLifetimeSecs: preManifestApp.WorkerMaxSessionLifetimeSecs,
			}); rerr != nil {
				slog.Error("deploy: revert manifest [app] settings after failed deploy", "slug", slug, "err", rerr)
			}
			if s.proxy != nil {
				s.proxy.SetPoolCap(slug,
					deploy.ResolveMaxSessionsPerReplica(preManifestApp.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica))
				// Restore the pre-manifest pool mode so a revert brings the proxy
				// back to the mode the old pool was running under.
				s.proxy.SetPoolMode(slug,
					config.WorkerIsolationMode(deploy.ResolveWorkerIsolation(preManifestApp.WorkerIsolation, s.cfg.Runtime.DefaultWorkerIsolation)),
					preManifestApp.WorkerGroupedSize, preManifestApp.WorkerMaxWorkers)
				s.proxy.SetPoolWarmSpares(slug, preManifestApp.WorkerWarmSpares)
			}
			if _, rerr := s.store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
				AppID: preManifestApp.ID, Slug: slug,
				SetIdentityHeaders: true, IdentityHeaders: preManifestApp.IdentityHeaders,
				// Restore the pre-manifest autoscale policy. Unconditional like
				// identity_headers: a no-op when the failed manifest declared no
				// autoscale (writes back the same values), correct when it did.
				SetAutoscale:         true,
				AutoscaleEnabled:     preManifestApp.AutoscaleEnabled,
				AutoscaleMinReplicas: preManifestApp.AutoscaleMinReplicas,
				AutoscaleMaxReplicas: preManifestApp.AutoscaleMaxReplicas,
				AutoscaleTarget:      preManifestApp.AutoscaleTarget,
				// Restore the pre-manifest render pacing. Unconditional like
				// identity_headers/autoscale: a no-op when the failed manifest
				// declared no render_seconds, correct when it did.
				SetRenderSeconds: true,
				RenderSeconds:    preManifestApp.RenderSeconds,
			}); rerr != nil {
				slog.Error("deploy: revert identity_headers after failed deploy", "slug", slug, "err", rerr)
			}
			if s.proxy != nil {
				s.proxy.SetPoolIdentityHeaders(slug,
					deploy.ResolveIdentityHeaders(preManifestApp.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()))
				// Restore the pre-manifest render pacing on the live pool so the
				// rolled-back bundle is governed by its own pacing, not the
				// failed manifest's.
				s.proxy.ApplyRenderPacing(slug, preManifestApp.RenderSeconds)
			}
		}
		deployResponse.event(deployevent.Phase("recovery", deployevent.StatusStarted, "Restoring the previous deployment"))
		s.restoreAfterFailedDeploy(slug, &preManifestApp, prevActive, keepStopped)
		recoveryMessage := "Recovery finished"
		if recovered, rerr := s.store.GetAppBySlug(slug); rerr == nil {
			switch recovered.Status {
			case "running":
				recoveryMessage = "Previous deployment restored and running"
			case "stopped":
				recoveryMessage = "App remains safely stopped"
			case "degraded", "failed", "crashed":
				recoveryMessage = "Previous deployment could not be fully restored; app is " + recovered.Status
			}
		}
		deployResponse.event(deployevent.Phase("recovery", deployevent.StatusCompleted, recoveryMessage))
		s.recordDeploy("failure")
		deployResponse.fail(http.StatusInternalServerError, reason, kind, "deploy")
		return
	}
	// The pool is now serving the new bundle; from here onwards the on-disk
	// artefacts must survive any subsequent error so a follow-up rollback or
	// recovery still has the directory to point at.
	keepFiles = true
	deployResponse.event(deployevent.Phase("commit", deployevent.StatusStarted, "Recording the new deployment"))

	for _, r := range result.Replicas {
		pid, port := r.PID, r.Port
		depID := pendingDep.ID
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:        app.ID,
			Index:        r.Index,
			PID:          &pid,
			Port:         &port,
			Status:       "running",
			Provider:     r.Provider,
			Tier:         r.Tier,
			EndpointURL:  r.EndpointURL,
			WorkerID:     r.WorkerID,
			AppVersion:   version,
			DesiredState: "running",
			DeploymentID: &depID,
		}); err != nil {
			slog.Error("deploy_upsert_replica_failed", "slug", slug, "index", r.Index, "err", err)
		}
	}
	// Persist indices that failed to boot as crashed (no PID/port) so the
	// watchdog reconciles the pool back up to the desired replica count
	// instead of leaving the app silently under-replicated.
	for _, idx := range result.Failed {
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:  app.ID,
			Index:  idx,
			Status: "crashed",
		}); err != nil {
			slog.Error("deploy_upsert_failed_replica", "slug", slug, "index", idx, "err", err)
		}
	}
	// Bookkeeping after the proxy switch. Two writes here are different
	// kinds of important and are handled differently:
	//
	//  1. UpdateAppStatus and IncrementDeployCount are
	//     soft state. The watchdog reconciles status; the never-deployed
	//     gate keys off the durable deployments row (HasAnyDeployment),
	//     not deploy_count. Log+continue is safe — neither failure traps
	//     the user out of an app whose bundle is already deployed.
	//
	//  2. PromoteDeployment is authoritative: it flips the pre-recorded
	//     pending row to 'succeeded', which is the pointer the scheduler,
	//     watcher wake, restart, and rollback all consult to find the live
	//     bundle. If we let this failure pass silently, the next
	//     restart/wake/schedule run reads the previous deployment row and
	//     silently reverts the running pool to the OLD bundle. We therefore
	//     fail closed (500). The bundle stays on disk (keepFiles=true) so a
	//     follow-up deploy succeeds without re-uploading; PruneOldVersions
	//     sweeps any duplicate after the retry succeeds.
	//
	// A kept-stopped deploy booted nothing, so it writes back "stopped": the
	// bundle is on disk and promoted, and the app comes up on the new version
	// when an operator starts it.
	deployedStatus := "running"
	if keepStopped {
		deployedStatus = "stopped"
	}
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: deployedStatus,
	}); err != nil {
		slog.Error("deploy: persist status failed", "slug", slug, "status", deployedStatus, "err", err)
	}

	if err := s.store.IncrementDeployCount(slug); err != nil {
		slog.Error("deploy: increment deploy_count failed; bundle is deployed", "slug", slug, "err", err)
	}

	if err := s.store.PromoteDeployment(pendingDep.ID); err != nil {
		slog.Error("deploy: promote deployment failed; pool is live but next restart/wake/schedule would silently revert to the previous bundle — failing the request so the caller retries", "slug", slug, "version", version, "err", err)
		s.recordDeploy("failure")
		deployResponse.fail(http.StatusInternalServerError, "deploy succeeded but recording it failed; retry to commit", "", "commit")
		return
	}

	// Record that this bundle's environment is built and its post-deploy hooks
	// have run, so restoring it later is an activation rather than a rebuild.
	// Soft state: a failure here only costs a future restore its fast path,
	// which then re-attempts the build best-effort instead of skipping it.
	if err := s.store.MarkDeploymentPrepared(pendingDep.ID); err != nil {
		slog.Warn("deploy: recording preparation state failed; a later restore will rebuild best-effort",
			"slug", slug, "version", version, "err", err)
	}
	deployResponse.event(deployevent.Phase("commit", deployevent.StatusCompleted, "Deployment recorded"))
	if manifest != nil {
		deployResponse.event(deployevent.Phase("configuration", deployevent.StatusStarted, "Applying manifest configuration"))
	}

	// Phase B: upsert [[schedule]] rows from the manifest. Runs after
	// CreateDeployment is durable so a scheduler tick between Reload and this
	// write cannot fire a job against the previous bundle.
	if manifest != nil && len(manifest.Schedules) > 0 {
		scheduleResults, err := s.applyManifestSchedules(r, app, manifest.Schedules)
		if err != nil {
			// Phase B is post-commit: bundle is live, deployment row is durable.
			// Failure leaves schedules incomplete; the next deploy converges
			// (idempotent upserts). The client still sees HTTP 500, so the
			// deploy metric records failure to match the client-visible result.
			slog.Error("manifest [[schedule]] apply failed", "slug", slug, "err", err)
			s.recordDeploy("failure")
			deployResponse.fail(http.StatusInternalServerError, "manifest schedule apply failed", "", "configuration")
			return
		}
		manifestSummary.Schedules = scheduleResults
	}

	// Phase C: reconcile manifest-declared per-app group access. Runs whenever a
	// manifest is present so a removed [access] block drops its manifest rules
	// (declarative); manual rules are preserved by the store.
	if manifest != nil {
		agResults, err := s.applyManifestAccessGroups(app, manifest.Access)
		if err != nil {
			slog.Error("manifest [access] apply failed", "slug", slug, "err", err)
			s.recordDeploy("failure")
			deployResponse.fail(http.StatusInternalServerError, "manifest access apply failed", "", "configuration")
			return
		}
		if len(agResults) > 0 {
			manifestSummary.AccessGroups = agResults
		}
		if u := auth.UserFromContext(r.Context()); u != nil && len(agResults) > 0 {
			applied := 0
			for _, ag := range agResults {
				if !ag.Skipped {
					applied++
				}
			}
			s.logAuditEvent(r, db.AuditEventParams{
				UserID:       &u.ID,
				Action:       "reconcile_group_access",
				ResourceType: "app",
				ResourceID:   slug,
				Detail:       fmt.Sprintf("applied=%d skipped=%d", applied, len(agResults)-applied),
				IPAddress:    s.ClientIP(r),
			})
		}
	}
	if manifest != nil {
		deployResponse.event(deployevent.Phase("configuration", deployevent.StatusCompleted, "Manifest configuration applied"))
	}

	// Prune old version directories beyond the retention limit. Run synchronously
	// while the per-slug deploy lock is still held (via defer release above) so a
	// concurrent redeploy or rollback for the same slug cannot scan and delete the
	// same version/bundle directories at the same time. A detached goroutine would
	// outlive the handler's lock release and race the next lock holder.
	deployResponse.event(deployevent.Phase("cleanup", deployevent.StatusStarted, "Cleaning up old deployment files"))
	if err := deploy.PruneOldVersions(s.cfg.Storage.AppsDir, slug, s.cfg.Storage.VersionRetention, bundleDir); err != nil {
		slog.Error("prune_old_versions_failed", "slug", slug, "err", err)
	}
	deployResponse.event(deployevent.Phase("cleanup", deployevent.StatusCompleted, "Cleanup complete"))

	updatedApp, err := s.store.GetAppBySlug(slug)
	if err != nil {
		s.recordDeploy("failure")
		deployResponse.fail(http.StatusInternalServerError, "internal server error", "", "commit")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "deploy",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.ClientIP(r),
			RunID:        runID,
		})
	}

	// Top-level keys remain the *db.App fields (compat: CLI / scripts that
	// read deploy_count still work) and a new "manifest" key is added when
	// any [app] field or [[schedule]] was applied. omitempty keeps the wire
	// shape clean for bundles without a shinyhub.toml.
	resp := struct {
		*db.App
		Manifest *ManifestApplied `json:"manifest,omitempty"`
		// HooksSkipped is non-zero when the runtime prepared deps inside a
		// container and post-deploy hooks were therefore not run. omitempty
		// keeps the wire shape clean for the common case.
		HooksSkipped int `json:"hooks_skipped,omitempty"`
		// HooksDeclared/HooksRun distinguish "no hooks" from "all hooks ran".
		HooksDeclared int `json:"hooks_declared,omitempty"`
		HooksRun      int `json:"hooks_run,omitempty"`
		// KeptStopped reports that the app was left down because it was stopped
		// before this deploy. Stated outright so the CLI does not have to infer
		// it from a status field a concurrent start could have changed.
		KeptStopped bool `json:"kept_stopped,omitempty"`
	}{
		App:           updatedApp,
		HooksSkipped:  result.HooksSkipped,
		HooksDeclared: result.HooksDeclared,
		HooksRun:      result.HooksRun,
		KeptStopped:   keepStopped,
	}
	if !manifestSummary.IsEmpty() {
		resp.Manifest = &manifestSummary
	}
	// Record success only here, after every remaining error path has passed, so
	// shinyhub_deploys_total{result} matches the client-visible HTTP result.
	s.recordDeploy("success")
	deployResponse.result(resp)
}

func (s *Server) handleRollbackApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}
	runID := s.knownFleetRunID(r)
	origin := deploymentOriginForRequest(r, runID, true)

	// Parse optional body to support targeted rollback by deployment ID.
	var reqBody struct {
		DeploymentID *int64 `json:"deployment_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	var prev *db.Deployment

	if reqBody.DeploymentID != nil {
		// Targeted rollback: fetch the specific deployment and verify it belongs to this app.
		dep, err := s.store.GetDeploymentBySlugAndID(slug, *reqBody.DeploymentID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "deployment not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		prev = dep
	} else {
		// Default rollback: use the previous deployment (index 1, newest-first).
		deployments, err := s.store.ListDeployments(app.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if len(deployments) < 2 {
			// The succeeded-only history has no earlier bundle to roll back to.
			// If the most recent deploy ATTEMPT failed it was never promoted (a
			// failed deploy triggers restorePreviousPool), so the app is already
			// running the last succeeded deployment. Say so explicitly instead of
			// a bare "no previous deployment", which misleads an operator who sees
			// the failed attempt at the top of `apps deployments`.
			if all, aerr := s.store.ListDeploymentsBySlug(slug); aerr == nil &&
				len(all) > 0 && all[0].Status == db.DeploymentFailed && len(deployments) == 1 {
				writeError(w, http.StatusConflict, fmt.Sprintf(
					"the most recent deployment failed and was not promoted; %s is already running the last succeeded deployment (#%d). Use --to <id> to redeploy a specific version.",
					slug, deployments[0].ID))
				return
			}
			writeError(w, http.StatusConflict, "no previous deployment to roll back to")
			return
		}
		prev = deployments[1]
	}

	if s.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "process manager not available")
		return
	}

	// Serialize against concurrent deploy/restart/stop on the same slug.
	release := s.acquireDeployLock(slug)
	defer release()

	// Validate that the target bundle still exists on disk BEFORE we tear
	// down the running app. If the directory was pruned out from under us
	// (manual cleanup, disk failure, etc.) deploy.Run would fail and we'd
	// be left with the live app stopped and no path back to running.
	if info, err := os.Stat(prev.BundleDir); err != nil || !info.IsDir() {
		writeError(w, http.StatusConflict, "target deployment bundle is missing or invalid")
		return
	}

	// All validation runs BEFORE the pending row is recorded and the pool is
	// torn down: a rejected rollback leaves the running app untouched and no
	// deployment record.
	if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// Defense in depth: if the app's tier lost its durable backend since the last
	// deploy, refuse to re-run a data-using app onto now-ephemeral storage.
	if tier, blocked, gerr := s.ephemeralDataDeployBlock(app, nil); gerr != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	} else if blocked {
		writeError(w, http.StatusUnprocessableEntity, ephemeralDataDeployMsg(tier))
		return
	}

	// Capture the current live deployment for restore-on-failure, then record
	// the rollback target as a pending deployment BEFORE tearing down the pool
	// (same durability contract as a forward deploy).
	var prevActive *db.Deployment
	if existing, lerr := s.store.ListDeployments(app.ID); lerr == nil && len(existing) > 0 {
		prevActive = existing[0]
	}
	pendingDep, err := s.store.BeginDeploymentWithOrigin(app.ID, prev.Version, prev.BundleDir, runID, &prev.ID, origin)
	if err != nil {
		slog.Error("rollback: record pending deployment failed; running pool untouched", "slug", slug, "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Stop current instance; ignore the error if it wasn't running.
	_ = s.manager.Stop(slug)
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	rollbackDefaultMem, rollbackDefaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
	result, err := s.deployRun(s.withTierPlacement(deploy.Params{
		Slug:                  slug,
		BundleDir:             prev.BundleDir,
		Replicas:              app.Replicas,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, rollbackDefaultMem),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, rollbackDefaultCPU),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
		IdentityHeaders:       deploy.ResolveIdentityHeaders(app.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()),
		ContentDigest:         prev.ContentDigest,
		DeploymentID:          pendingDep.ID,
		AppVersion:            prev.Version,
		// Keyed off the historical row being rolled back TO, not the pending row
		// created for this rollback: the pending row is new and never prepared,
		// while prev is the deployment whose environment and hooks already ran.
		Preparation: activationPreparation(prev.Prepared),
	}, app))
	if err != nil {
		slog.Error("rollback_failed", "slug", slug, "err", err)
		_ = s.store.FailDeployment(pendingDep.ID)
		s.restorePreviousPool(slug, app, prevActive)
		writeErrorWithKind(w, http.StatusInternalServerError, deployFailureMessage(err), deployfail.Classify(err))
		return
	}

	// The pending row now represents a prepared bundle: either prev was already
	// prepared and preparation was skipped, or it was not and we just ran it.
	// Recording it keeps a future restore or rollback of THIS row on the fast
	// path instead of silently rebuilding.
	if err := s.store.MarkDeploymentPrepared(pendingDep.ID); err != nil {
		slog.Warn("rollback: recording preparation state failed; a later restore will rebuild best-effort",
			"slug", slug, "version", prev.Version, "err", err)
	}

	for _, r := range result.Replicas {
		pid, port := r.PID, r.Port
		depID := pendingDep.ID
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:        app.ID,
			Index:        r.Index,
			PID:          &pid,
			Port:         &port,
			Status:       "running",
			Provider:     r.Provider,
			Tier:         r.Tier,
			EndpointURL:  r.EndpointURL,
			WorkerID:     r.WorkerID,
			AppVersion:   prev.Version,
			DesiredState: "running",
			DeploymentID: &depID,
		}); err != nil {
			slog.Error("rollback_upsert_replica_failed", "slug", slug, "index", r.Index, "err", err)
		}
	}
	// UpdateAppStatus is soft state — the watchdog reconciles. PromoteDeployment
	// is authoritative: it is the pointer restart/wake/schedule consult to
	// find the live bundle. If we let it fail silently here, a later restart
	// would read the previous deployment row (the bundle we just rolled back
	// FROM) and silently un-roll-back the app. Fail closed (500) on that one;
	// the bundle on disk is unchanged so a retry is safe.
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
	}); err != nil {
		slog.Error("rollback: persist running status failed; pool is live", "slug", slug, "err", err)
	}

	// Copy the target's digest onto the pending row so the promoted live
	// deployment row carries the correct bundle identity. Must run before
	// PromoteDeployment so the update is visible to any concurrent reader.
	if prev.ContentDigest != "" {
		if err := s.store.SetDeploymentDigest(pendingDep.ID, prev.ContentDigest); err != nil {
			slog.Error("rollback: copy target digest to pending", "err", err)
		}
	}

	if err := s.store.PromoteDeployment(pendingDep.ID); err != nil {
		slog.Error("rollback: promote deployment failed; pool is live but next restart/wake/schedule would silently un-roll-back to the previous bundle — failing the request so the caller retries", "slug", slug, "version", prev.Version, "err", err)
		writeError(w, http.StatusInternalServerError, "rollback succeeded but recording it failed; retry to commit")
		return
	}

	updatedApp, err := s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "rollback",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.ClientIP(r),
			RunID:        runID,
		})
	}
	// Rollbacks are not counted as deploys — deploy_count tracks forward deployments only.
	writeJSON(w, http.StatusOK, updatedApp)
}

func (s *Server) handleRestartApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	// When ?if_not_running=true is set (sent by `apps start`), skip the cycle
	// if the app is already running AND at least one replica process is alive.
	// Trusting the DB status alone is not enough: the hibernation watchdog stops
	// processes before persisting the updated status, so there is a window where
	// status="running" in the DB but no live replica exists. In that case we
	// fall through to the normal restart path to bring the app back up.
	if r.URL.Query().Get("if_not_running") == "true" && app.Status == "running" && s.hasLiveReplica(slug) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "running",
			"note":   "already running",
		})
		return
	}

	if s.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "process manager not available")
		return
	}

	// Serialize against concurrent deploy/rollback/stop on the same slug.
	// The active deployment MUST be read inside the lock: a deploy that wins
	// the race promotes a newer row, and a read taken before the lock would
	// boot the stale bundle while the DB records the new one as succeeded.
	release := s.acquireDeployLock(slug)
	defer release()

	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if len(deployments) == 0 {
		writeError(w, http.StatusConflict,
			"app has no successful deployment - see: shinyhub apps deployments "+slug)
		return
	}
	current := deployments[0]

	if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// Stop current instance; ignore the error if it wasn't running.
	_ = s.manager.Stop(slug)
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	// Defense in depth: refuse to restart a data-using app onto a tier that lost
	// its durable backend since the last deploy.
	if tier, blocked, gerr := s.ephemeralDataDeployBlock(app, nil); gerr != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	} else if blocked {
		writeError(w, http.StatusUnprocessableEntity, ephemeralDataDeployMsg(tier))
		return
	}

	restartDefaultMem, restartDefaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
	result, err := s.deployRun(s.withTierPlacement(deploy.Params{
		Slug:                  slug,
		BundleDir:             current.BundleDir,
		Replicas:              app.Replicas,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, restartDefaultMem),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, restartDefaultCPU),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
		IdentityHeaders:       deploy.ResolveIdentityHeaders(app.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()),
		ContentDigest:         current.ContentDigest,
		DeploymentID:          current.ID,
		AppVersion:            current.Version,
		// A restart re-activates the deployment that is already live, so its
		// hooks have run and its environment is built.
		Preparation: activationPreparation(current.Prepared),
	}, app))
	if err != nil {
		slog.Error("restart_failed", "slug", slug, "err", err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
			slog.Error("restart_update_status_failed", "slug", slug, "err", err)
		}
		writeErrorWithKind(w, http.StatusInternalServerError, deployFailureMessage(err), deployfail.Classify(err))
		return
	}

	// A legacy row that has now been prepared for real converges to prepared, so
	// the next restart skips the work instead of repeating it forever.
	if !current.Prepared {
		if err := s.store.MarkDeploymentPrepared(current.ID); err != nil {
			slog.Warn("restart: recording preparation state failed; the next restart will prepare again",
				"slug", slug, "version", current.Version, "err", err)
		}
	}

	for _, r := range result.Replicas {
		pid, port := r.PID, r.Port
		depID := current.ID
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:        app.ID,
			Index:        r.Index,
			PID:          &pid,
			Port:         &port,
			Status:       "running",
			Provider:     r.Provider,
			Tier:         r.Tier,
			EndpointURL:  r.EndpointURL,
			WorkerID:     r.WorkerID,
			AppVersion:   current.Version,
			DesiredState: "running",
			DeploymentID: &depID,
		}); err != nil {
			slog.Error("restart_upsert_replica_failed", "slug", slug, "index", r.Index, "err", err)
		}
	}
	// Bookkeeping after the proxy switch: the restarted pool is already
	// serving traffic, so a transient DB hiccup here must NOT surface as
	// "restart failed" — that would push the caller into a retry loop on top
	// of an already-running restart. Log loudly so an operator notices the
	// reconciliation gap (status watchdog will eventually correct
	// running-status).
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
	}); err != nil {
		slog.Error("restart: persist running status failed; pool is live", "slug", slug, "err", err)
	}

	updatedApp, err := s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "restart",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.ClientIP(r),
		})
	}
	writeJSON(w, http.StatusOK, updatedApp)
}

func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	// Serialize against any in-flight deploy/restart on this slug so we don't
	// race the process manager into an inconsistent state mid-teardown.
	release := s.acquireDeployLock(slug)
	defer release()

	// app was loaded by requireManageApp before acquireDeployLock. Checking the
	// precondition here (under the deploy lock) serializes it against in-flight
	// deploys and restarts; the only residual race is two concurrent DELETEs on
	// the same slug, which the deleting-tombstone and ErrNotFound guard already
	// makes safe. The pre-lock snapshot is acceptable for this use case.
	if checkAppPreconditions(w, r, app) {
		return
	}

	// Stop the process if it is running; ignore the error (may not be running).
	if s.manager != nil {
		_ = s.manager.Stop(slug)
	}
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	// Tombstone first: mark the row 'deleting' BEFORE touching disk so a crash
	// (or a cleanup failure) mid-teardown is recoverable. ListRunningApps
	// excludes it, so recovery will not re-adopt a half-deleted app; startup
	// reconciliation (ReconcileDeletingApps) finishes any tombstone left
	// behind. Only after disk cleanup fully succeeds is the row removed.
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "deleting"}); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// The app is logically gone now (tombstoned). Drop its rejection history so
	// the rollup does not carry a deleted slug. Done here, not on the earlier
	// Deregister, because Deregister also fires on redeploy/restart/stop where
	// the app still exists.
	if s.proxy != nil {
		s.proxy.ForgetRejects(slug)
	}

	detail := ""
	if cleanupErr := storage.OnAppDelete(s.cfg, slug); cleanupErr != nil {
		// Disk cleanup failed: keep the 'deleting' tombstone so startup
		// reconciliation retries and the row is not lost with bytes still on
		// disk (which would orphan them with no owning row or quota). The app
		// is logically gone from the caller's perspective.
		detail = "deferred cleanup: " + cleanupErr.Error()
		slog.Error("app delete cleanup failed; tombstone retained for reconcile", "slug", slug, "err", cleanupErr)
	} else if secErr := s.cleanupAppSecrets(r.Context(), app.ID); secErr != nil {
		// External managed-resource cleanup failed: keep the tombstone so
		// reconciliation retries and no provider resources are orphaned.
		detail = "deferred provider cleanup: " + secErr.Error()
		slog.Error("app delete provider cleanup failed; tombstone retained for reconcile", "slug", slug, "err", secErr)
	} else if err := s.store.DeleteApp(slug); err != nil && !errors.Is(err, db.ErrNotFound) {
		// Bytes are gone; only the tombstone row remains. Reconcile will drop
		// it on next startup, so this is not a client-visible failure.
		detail = "row delete deferred: " + err.Error()
		slog.Error("app delete: row removal failed after cleanup; tombstone retained", "slug", slug, "err", err)
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "delete_app",
			ResourceType: "app",
			ResourceID:   slug,
			Detail:       detail,
			IPAddress:    s.ClientIP(r),
			RunID:        s.knownFleetRunID(r),
		})
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleStopApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	// Serialize with any in-flight deploy/restart on this slug.
	release := s.acquireDeployLock(slug)
	defer release()

	// Stop the process if managed; ignore error if already stopped.
	if s.manager != nil {
		_ = s.manager.Stop(slug)
	}
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	// Mark all replica rows as stopped so GET /api/apps/:slug reflects
	// consistent state immediately after the manual stop.
	if replicas, err := s.store.ListReplicas(app.ID); err != nil {
		slog.Error("list replicas on stop", "slug", slug, "err", err)
	} else {
		for _, rep := range replicas {
			if err := s.store.UpsertReplica(db.UpsertReplicaParams{
				AppID:        app.ID,
				Index:        rep.Index,
				Status:       "stopped",
				DesiredState: "stopped",
			}); err != nil {
				slog.Error("upsert replica on stop", "slug", slug, "index", rep.Index, "err", err)
			}
		}
	}

	// Update DB status and clear port/PID.
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "stopped",
		// Port and PID left nil to clear them in the DB.
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "stop",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.ClientIP(r),
		})
	}
	writeJSON(w, http.StatusOK, app)
}

// handleSleepApp puts a running app to sleep: the pool is released and the app
// becomes "hibernated", so the next request transparently wakes it. That is the
// difference from stop, which is terminal and serves visitors a stopped page
// until an operator starts the app again.
func (s *Server) handleSleepApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}
	if s.sleepNow == nil {
		writeError(w, http.StatusServiceUnavailable, "sleep is unavailable on this server")
		return
	}

	// Serialize with any in-flight deploy/restart/stop on this slug.
	release := s.acquireDeployLock(slug)
	defer release()

	if err := s.sleepNow(slug); err != nil {
		switch {
		case errors.Is(err, lifecycle.ErrAppNotRunning):
			writeError(w, http.StatusConflict, "app is not running")
		case errors.Is(err, lifecycle.ErrElasticNotSleepable):
			writeError(w, http.StatusConflict, "sleep is not supported for grouped or per-session worker isolation")
		case errors.Is(err, lifecycle.ErrSleepTeardownFailed):
			writeError(w, http.StatusConflict, "app did not stop; try again or use stop")
		default:
			slog.Error("sleep app", "slug", slug, "err", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "sleep",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.ClientIP(r),
		})
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) handleSetAppAccess(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}
	oldAccess := app.Access
	var req struct {
		Access string `json:"access"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if !db.IsValidAppVisibility(req.Access) {
		writeError(w, http.StatusBadRequest, "access must be one of "+strings.Join(db.ValidAppVisibilities, ", "))
		return
	}
	if checkAppPreconditions(w, r, app) {
		return
	}
	if err := s.store.SetAppAccess(slug, req.Access); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if u := auth.UserFromContext(r.Context()); u != nil {
		accessDetail, _ := json.Marshal(map[string]string{"from": oldAccess, "to": req.Access})
		s.logAuditEvent(r, db.AuditEventParams{
			UserID: &u.ID, Action: "set_access", ResourceType: "app",
			ResourceID: slug, Detail: string(accessDetail), IPAddress: s.ClientIP(r), RunID: s.knownFleetRunID(r),
		})
	}
	writeJSON(w, http.StatusOK, app)
}

// handleTransferAppOwnership reassigns apps.owner_id. The gate is stricter
// than requireManageApp: only the current owner or a platform admin/operator
// may transfer (a manager-role member manages the app but does not own it,
// matching the owner-vs-collaborator split in comparable platforms). This is
// the offboarding path: transfer a leaver's apps, then delete the account.
func (s *Server) handleTransferAppOwnership(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, u, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return
	}
	// A service credential's shared principal may own the app, but ownership is
	// not a credential-specific grant. Only an operator/admin credential may
	// perform this platform-level reassignment.
	if (u.IsServiceAccount() && !isPrivilegedAppOperator(u)) ||
		(!u.IsServiceAccount() && !isPrivilegedAppOperator(u) && app.OwnerID != u.ID) {
		writeError(w, http.StatusForbidden, "only the app's owner or a platform admin/operator can transfer ownership")
		return
	}
	var req struct {
		UserID   int64  `json:"user_id"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	var target *db.User
	var err error
	switch {
	case req.UserID != 0:
		target, err = s.store.GetUserByID(req.UserID)
	case req.Username != "":
		target, err = s.store.GetUserByUsername(req.Username)
	default:
		writeError(w, http.StatusBadRequest, "user_id or username is required")
		return
	}
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if target.PrincipalType == "service_account" {
		writeError(w, http.StatusForbidden, "cannot transfer ownership to a system user")
		return
	}
	if target.ID != app.OwnerID {
		if err := s.store.SetAppOwner(slug, target.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		s.logAuditEvent(r, db.AuditEventParams{
			UserID: &u.ID, Action: "transfer_ownership", ResourceType: "app",
			ResourceID: slug,
			Detail:     fmt.Sprintf("from_user_id=%d to_user_id=%d to_username=%s", app.OwnerID, target.ID, target.Username),
			IPAddress:  s.ClientIP(r),
		})
	}
	app, err = s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) handleGrantAppAccess(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}
	var req struct {
		UserID   int64   `json:"user_id"`
		Username string  `json:"username"`
		Role     *string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	// Resolve a username to its id server-side, under this manage-gated handler,
	// so granting access never requires a separate broadly-readable user-lookup
	// endpoint (the previous flow's enumeration primitive).
	userID := req.UserID
	switch {
	case userID != 0:
		// Verify the supplied id exists.
		if _, err := s.store.GetUserByID(userID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "user not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	case req.Username != "":
		// GetUserByUsername also confirms existence, so no second lookup is needed.
		u, err := s.store.GetUserByUsername(req.Username)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "user not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		userID = u.ID
	default:
		writeError(w, http.StatusBadRequest, "user_id or username is required")
		return
	}
	// POST is additive. With no role field we add the member (a NEW member
	// defaults to viewer) and never change an existing member's role - use
	// PATCH /members/{user_id} for that. An explicit role sets the role
	// (upsert) and, like PATCH, may not target the caller's own membership.
	auditDetail := fmt.Sprintf("user_id=%d", userID)
	if req.Role == nil {
		if err := s.store.GrantAppAccess(slug, userID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	} else {
		role := *req.Role
		if !db.IsValidMemberRole(role) {
			writeError(w, http.StatusBadRequest, "role must be one of "+strings.Join(db.ValidMemberRoles, ", "))
			return
		}
		if caller := auth.UserFromContext(r.Context()); caller != nil && caller.ID == userID {
			writeError(w, http.StatusForbidden, "cannot change your own role")
			return
		}
		if err := s.store.GrantAppAccessWithRole(slug, userID, role); err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		auditDetail = fmt.Sprintf("user_id=%d role=%s", userID, role)
	}
	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "grant_access",
			ResourceType: "app",
			ResourceID:   slug,
			Detail:       auditDetail,
			IPAddress:    s.ClientIP(r),
		})
	}
	// On a private app the grant is what admits the user, so it needs no
	// caveat. On a shared or public app viewing is already open to everyone,
	// so advertise (on the same header group grants use; the 204 carries no
	// body) what the grant still controls.
	if audience := openVisibilityAudience(app.Access); audience != "" {
		w.Header().Set("X-ShinyHub-Warning", fmt.Sprintf(
			"app is %s; %s can already view it (membership matters for the manager role and if the app is made private)",
			app.Access, audience))
	}
	w.WriteHeader(http.StatusNoContent)
}

// openVisibilityAudience names who can already view an app whose visibility
// admits everyone, or returns "" for private (where grants are the gate).
func openVisibilityAudience(access string) string {
	switch access {
	case "shared":
		return "all signed-in users"
	case "public":
		return "anyone"
	}
	return ""
}

func (s *Server) handleRevokeAppAccess(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	var userID int64

	// Prefer the path parameter when present (DELETE /api/apps/{slug}/members/{user_id}).
	// Fall back to parsing the JSON body for backward compatibility.
	if pathUserID := chi.URLParam(r, "user_id"); pathUserID != "" {
		id, err := strconv.ParseInt(pathUserID, 10, 64)
		if err != nil || id == 0 {
			writeError(w, http.StatusBadRequest, "invalid user_id")
			return
		}
		userID = id
	} else {
		var req struct {
			UserID   int64  `json:"user_id"`
			Username string `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request")
			return
		}
		if req.UserID == 0 && req.Username != "" {
			u, err := s.store.GetUserByUsername(req.Username)
			if err != nil {
				if errors.Is(err, db.ErrNotFound) {
					writeError(w, http.StatusNotFound, "user not found")
					return
				}
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			req.UserID = u.ID
		}
		if req.UserID == 0 {
			writeError(w, http.StatusBadRequest, "user_id or username is required")
			return
		}
		userID = req.UserID
	}

	// A caller cannot remove their own membership (mirrors the self-role-change
	// guard): self-removal is a footgun that can strand a manager out of an app.
	if caller := auth.UserFromContext(r.Context()); caller != nil && caller.ID == userID {
		writeError(w, http.StatusForbidden, "cannot remove your own access")
		return
	}

	if err := s.store.RevokeAppAccess(slug, userID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// The outcome (user has no access) is already in place; the repeat is
			// idempotent. Return 204 so callers can safely re-run revoke.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "revoke_access",
			ResourceType: "app",
			ResourceID:   slug,
			Detail:       fmt.Sprintf("user_id=%d", userID),
			IPAddress:    s.ClientIP(r),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetMemberRole(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}
	userID, err := strconv.ParseInt(chi.URLParam(r, "user_id"), 10, 64)
	if err != nil || userID == 0 {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	// A caller cannot change their own member role (mirrors handlePatchUser):
	// self-demotion is a footgun that can strand a manager out of an app.
	if caller := auth.UserFromContext(r.Context()); caller != nil && caller.ID == userID {
		writeError(w, http.StatusForbidden, "cannot change your own role")
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if !db.IsValidMemberRole(req.Role) {
		writeError(w, http.StatusBadRequest, "role must be one of "+strings.Join(db.ValidMemberRoles, ", "))
		return
	}
	if err := s.store.SetMemberRole(slug, userID, req.Role); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "member not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "set_member_role",
			ResourceType: "app",
			ResourceID:   slug,
			Detail:       fmt.Sprintf("user_id=%d role=%s", userID, req.Role),
			IPAddress:    s.ClientIP(r),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

type appGroupRuleResponse struct {
	Group  string `json:"group"`
	Role   string `json:"role"`
	Source string `json:"source"`
}

func (s *Server) handleGetAppGroupAccess(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}
	rules, err := s.store.ListAppGroupAccess(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	resp := make([]appGroupRuleResponse, len(rules))
	for i, rule := range rules {
		resp[i] = appGroupRuleResponse{Group: rule.Group, Role: rule.Role, Source: rule.Source}
	}
	limit, offset := parsePagination(r)
	writeList(w, resp, limit, offset, nil)
}

func (s *Server) handleGrantAppGroupAccess(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}
	var req struct {
		Group string `json:"group"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	req.Group = strings.TrimSpace(req.Group)
	if req.Group == "" {
		writeError(w, http.StatusBadRequest, "group is required")
		return
	}
	role := req.Role
	if role == "" {
		role = "viewer"
	}
	if !db.IsValidMemberRole(role) {
		writeError(w, http.StatusBadRequest, "role must be one of "+strings.Join(db.ValidMemberRoles, ", "))
		return
	}
	if err := s.store.GrantAppGroupAccess(slug, req.Group, role, "manual"); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID: &u.ID, Action: "grant_group_access", ResourceType: "app",
			ResourceID: slug, Detail: fmt.Sprintf("group=%s role=%s", req.Group, role),
			IPAddress: s.ClientIP(r),
		})
	}
	if app.Access == "public" || app.Access == "shared" {
		w.Header().Set("X-ShinyHub-Warning", "app is "+app.Access+"; group rules grant access but do not restrict viewing")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRevokeAppGroupAccess(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}
	group := chi.URLParam(r, "group")
	if group == "" {
		var req struct {
			Group string `json:"group"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request")
			return
		}
		group = strings.TrimSpace(req.Group)
	}
	if group == "" {
		writeError(w, http.StatusBadRequest, "group is required")
		return
	}
	// Manifest-sourced rules are managed by the bundle; the API must not delete
	// them (the next deploy would re-create them anyway). Direct callers get a
	// clear 409 instead of a surprising transient removal.
	rules, err := s.store.ListAppGroupAccess(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	for _, ru := range rules {
		if ru.Group == group && ru.Source == "manifest" {
			writeError(w, http.StatusConflict, "this group rule is managed by the bundle manifest; remove it from shinyhub.toml")
			return
		}
	}
	if err := s.store.RevokeAppGroupAccess(slug, group); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "group rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID: &u.ID, Action: "revoke_group_access", ResourceType: "app",
			ResourceID: slug, Detail: fmt.Sprintf("group=%s", group), IPAddress: s.ClientIP(r),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

type appMemberResponse struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func (s *Server) handleGetMembers(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}
	limit, offset := parsePagination(r)
	// Fetch the full (bounded) member set; writeList paginates in-memory so the
	// envelope carries an accurate total.
	members, err := s.store.ListAppMembers(slug, 0, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	resp := make([]appMemberResponse, len(members))
	for i, m := range members {
		resp[i] = appMemberResponse{UserID: m.UserID, Username: m.Username, Role: m.Role}
	}
	writeList(w, resp, limit, offset, nil)
}

type userLookupResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	// Username->id lookup is only needed by app operators granting access, so
	// restrict it to users who can manage apps. This stops a plain viewer (e.g.
	// an auto-provisioned OAuth account) from enumerating accounts.
	if !canCreateApps(auth.UserFromContext(r.Context())) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	username := chi.URLParam(r, "username")
	if username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	user, err := s.store.GetUserByUsername(username)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, userLookupResponse{ID: user.ID, Username: user.Username})
}

// workerPoolStatus is the live capacity view of an elastic app's worker pool,
// embedded in the app envelope as "worker_pool" and consumed by `apps show`
// and the dashboard. Ceiling is the admission ceiling:
// max_workers x sessions_per_worker.
type workerPoolStatus struct {
	Mode              string             `json:"mode"`
	SessionsPerWorker int                `json:"sessions_per_worker"`
	MaxWorkers        int                `json:"max_workers"`
	WarmSpareTarget   int                `json:"warm_spare_target"`
	Ceiling           int                `json:"ceiling"`
	Workers           []workerPoolWorker `json:"workers"`
}

type workerPoolWorker struct {
	SlotID int `json:"slot_id"`
	// Status is the proxy's routing view: booting, suspending, suspended,
	// resuming, running, or draining.
	Status    string `json:"status"`
	Sessions  int    `json:"sessions"`
	PID       int    `json:"pid,omitempty"`
	Port      int    `json:"port,omitempty"`
	WarmSpare bool   `json:"warm_spare,omitempty"`
}

// buildWorkerPool merges the proxy's live worker snapshot with the process
// manager's pid/port (keyed by slot: elastic spawns use the slot as the
// replica index). A worker whose process has not started yet omits pid/port
// rather than fabricating zeros.
func (s *Server) buildWorkerPool(slug string, snap proxy.ElasticPoolSnapshot) workerPoolStatus {
	var live []*process.ProcessInfo
	if s.manager != nil {
		live = s.manager.AllForSlug(slug)
	}
	out := workerPoolStatus{
		Mode:              snap.Mode,
		SessionsPerWorker: snap.SessionsPerWorker,
		MaxWorkers:        snap.MaxWorkers,
		WarmSpareTarget:   snap.WarmSpareTarget,
		Ceiling:           snap.MaxWorkers * snap.SessionsPerWorker,
		Workers:           make([]workerPoolWorker, 0, len(snap.Workers)),
	}
	for _, w := range snap.Workers {
		entry := workerPoolWorker{SlotID: w.SlotID, Status: w.Status, Sessions: w.Sessions, WarmSpare: w.WarmSpare}
		if w.SlotID >= 0 && w.SlotID < len(live) && live[w.SlotID] != nil {
			entry.PID = live[w.SlotID].PID
			entry.Port = live[w.SlotID].Port
		}
		out.Workers = append(out.Workers, entry)
	}
	// A process can briefly outlive its proxy slot while asynchronous teardown
	// runs (or expose an invariant violation if it persists). Do not make that
	// real host process disappear from observability: surface it as draining,
	// which truthfully says it is not admission capacity. This also makes a
	// manager/proxy divergence diagnosable without a host-level ps trace.
	seen := make(map[int]bool, len(out.Workers))
	for _, w := range out.Workers {
		seen[w.SlotID] = true
	}
	for idx, info := range live {
		if info == nil || seen[idx] {
			continue
		}
		out.Workers = append(out.Workers, workerPoolWorker{
			SlotID: idx, Status: "draining", PID: info.PID, Port: info.Port,
		})
	}
	sort.Slice(out.Workers, func(i, j int) bool { return out.Workers[i].SlotID < out.Workers[j].SlotID })
	return out
}

type replicaMetrics struct {
	Index        int    `json:"index"`
	Status       string `json:"status"`
	DesiredState string `json:"desired_state,omitempty"`
	PID          int    `json:"pid,omitempty"`
	// CPUPercent is the replica's CPU rate since the previous poll, where 100 is
	// one fully busy core. It is null when no rate is available yet, which covers
	// the first poll after a replica starts and every tier that cannot report one.
	// The key is always present, and never omitempty: an app sitting at a true 0%
	// is a different fact from an app whose usage is unknown, and omitempty would
	// render both as an absent key.
	CPUPercent *float64 `json:"cpu_percent"`
	RSSBytes   int64    `json:"rss_bytes,omitempty"`
	// Linux native replicas additionally expose proportional set size (PSS),
	// unique/private memory (USS), and proportionally attributed swap. These are
	// pointers so unsupported backends remain distinguishable from a real zero.
	PSSBytes                 *int64 `json:"pss_bytes"`
	USSBytes                 *int64 `json:"uss_bytes"`
	SwapPSSBytes             *int64 `json:"swap_pss_bytes"`
	MemoryAttributionPartial bool   `json:"memory_attribution_partial"`
	// Sessions is the proxy's best-effort live connection count for this
	// replica. Omitted (and -1 internally) when the replica slot is empty.
	Sessions int64  `json:"sessions"`
	Tier     string `json:"tier,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Reason is a presentation-only explanation for a degraded replica, e.g.
	// "worker unavailable" for a replica lost to a dead worker with no healthy
	// replacement. Empty for healthy replicas. Mirrors db.Replica.Reason so the
	// live poll stays consistent with the app envelope.
	Reason           string `json:"reason,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Signal           string `json:"signal,omitempty"`
	RestartCount     int    `json:"restart_count"`
	MetricsAvailable bool   `json:"metrics_available"`
	// Effective resource limits mirror the placement-aware pair deployment
	// resolves for the app. A zero limit is genuinely unlimited; it is not the
	// nullable "inherit" value stored on the app. EnforcementKnown distinguishes
	// an unenforced native cgroup limit from a test/API-only server that has no
	// runtime authority to answer.
	EffectiveMemoryLimitMB   int  `json:"effective_memory_limit_mb"`
	EffectiveCPUQuotaPercent int  `json:"effective_cpu_quota_percent"`
	MemoryLimitEnforced      bool `json:"memory_limit_enforced"`
	CPUQuotaEnforced         bool `json:"cpu_quota_enforced"`
	ResourceEnforcementKnown bool `json:"resource_enforcement_known"`
}

// autoscaleStatus is the live autoscale state returned in the metrics poll and
// app envelope. Timestamps are pointers so they marshal as null when absent.
type autoscaleStatus struct {
	LastActionAt  *time.Time `json:"last_action_at"`
	LastAction    string     `json:"last_action"`
	InCooldown    bool       `json:"in_cooldown"`
	CooldownUntil *time.Time `json:"cooldown_until"`
}

type metricsResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
	// Status is the app-level status: "running" if any replica is running,
	// otherwise the dominant replica status (or the DB-recorded status if
	// no replicas are tracked yet).
	Status string `json:"status"`
	// DesiredStatus preserves the database lifecycle intent when Status is an
	// observational value such as idle, degraded, or crashed.
	DesiredStatus string `json:"desired_status"`
	// Deploying is true while a deployment or rollback for this app is
	// actively executing on this instance (see Server.appDeploying). The
	// dashboard's 10s poll uses it to flip the card badge to "Deploying"
	// and back without a page reload. Not omitempty: the poll must be able
	// to clear the badge with an explicit false.
	Deploying bool `json:"deploying"`
	// LastDeploymentStatus mirrors the app's newest deployment-row status
	// ("succeeded"/"failed"/"pending", "" when never deployed) so the poll
	// can keep the badge model honest: a first deploy that fails while
	// watched flips the card to "Failed" instead of quietly reverting to
	// "Awaiting deploy".
	LastDeploymentStatus string `json:"last_deployment_status,omitempty"`
	// SessionsCap is the per-replica session cap currently in effect for
	// this pool. 0 means uncapped. For elastic (grouped/per_session) pools it
	// is the per-worker cap, so the admission ceiling is
	// MaxWorkers x SessionsCap.
	SessionsCap     int `json:"sessions_cap"`
	ReplicasDesired int `json:"replicas_desired"`
	ReplicasRunning int `json:"replicas_running"`
	WorkersRunning  int `json:"workers_running"`
	SessionsCeiling int `json:"sessions_ceiling"`
	// WorkerIsolation and MaxWorkers describe the elastic pool when the app
	// runs grouped/per_session isolation; omitted for multiplex.
	WorkerIsolation  string           `json:"worker_isolation,omitempty"`
	MaxWorkers       int              `json:"max_workers,omitempty"`
	Replicas         []replicaMetrics `json:"replicas"`
	MetricsAvailable bool             `json:"metrics_available"`
	AutoscaleStatus  *autoscaleStatus `json:"autoscale_status"`
	// Legacy fields preserved so existing clients (dashboard card poller)
	// keep working while they adopt the per-replica view. These mirror the
	// first running replica.
	PID                      int      `json:"pid,omitempty"`
	CPUPercent               *float64 `json:"cpu_percent"`
	RSSBytes                 int64    `json:"rss_bytes,omitempty"`
	PSSBytes                 *int64   `json:"pss_bytes"`
	USSBytes                 *int64   `json:"uss_bytes"`
	SwapPSSBytes             *int64   `json:"swap_pss_bytes"`
	MemoryAttributionPartial bool     `json:"memory_attribution_partial"`
}

// buildAutoscaleStatus computes the autoscale_status object from the latest
// audit event. When found is false (no scale events yet), returns a zero-state
// object with safe defaults so the client never has to branch on a missing key.
func buildAutoscaleStatus(event db.AuditEvent, found bool, cooldown time.Duration) autoscaleStatus {
	if !found {
		return autoscaleStatus{}
	}
	cooldownUntil := event.CreatedAt.Add(cooldown)
	inCooldown := time.Now().Before(cooldownUntil)
	action := "up"
	if event.Action == "autoscale_scale_down" {
		action = "down"
	}
	return autoscaleStatus{
		LastActionAt:  &event.CreatedAt,
		LastAction:    action,
		InCooldown:    inCooldown,
		CooldownUntil: &cooldownUntil,
	}
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, _, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.buildAppMetrics(slug, app))
}

// buildAppMetrics computes the live metrics envelope for one app: per-replica
// status/CPU/RAM/sessions sampled from the manager, the top-level rollup, the DB
// lost-status overlay, and the autoscale status. Shared by the single-app
// (GET /api/apps/{slug}/metrics) and batch (GET /api/apps/metrics) handlers so
// both report identical shapes.
// buildAppMetrics assembles one app's live metrics, fetching its DB replicas
// and latest autoscale event itself. Used by the single-app metrics endpoint;
// the batch endpoint prefetches those in bulk and calls buildAppMetricsFrom.
func (s *Server) buildAppMetrics(slug string, app *db.App) metricsResponse {
	dbReplicas, _ := s.store.ListReplicas(app.ID)
	ev, found, err := s.store.LatestAutoscaleEvent(slug)
	if err != nil {
		slog.Warn("autoscale status metrics query", "err", err)
	}
	return s.buildAppMetricsFrom(slug, app, dbReplicas, ev, found)
}

// buildAppMetricsFrom builds an app's live metrics from prefetched DB replicas
// and autoscale event, plus in-memory proxy/manager/sampler state. Splitting
// the DB reads out lets handleBatchMetrics fetch them for all cards in a few
// queries instead of three per card.
func (s *Server) buildAppMetricsFrom(slug string, app *db.App, dbReplicas []*db.Replica, autoscaleEvent db.AuditEvent, autoscaleFound bool) metricsResponse {
	// Work on a copy: batch metrics receives the same app pointers other handler
	// code may still use, and observational decoration must stay request-local.
	observedApp := *app
	s.decorateApp(&observedApp)
	resp := metricsResponse{
		GeneratedAt:          time.Now().UTC(),
		Status:               app.Status,
		DesiredStatus:        app.Status,
		Deploying:            s.appDeploying(app),
		LastDeploymentStatus: app.LastDeploymentStatus,
		ReplicasDesired:      app.Replicas,
		SessionsCeiling:      observedApp.SessionsCeiling,
		Replicas:             []replicaMetrics{},
	}

	if s.manager == nil {
		for _, rep := range dbReplicas {
			resp.Replicas = append(resp.Replicas, replicaMetrics{
				Index: rep.Index, Status: rep.Status, DesiredState: rep.DesiredState,
				Tier: rep.Tier, Provider: rep.Provider, Sessions: -1,
				Reason: rep.Reason, ExitCode: rep.ExitCode, Signal: rep.Signal,
				RestartCount: rep.RestartCount,
			})
		}
	}

	var sessionCounts []int64
	if s.proxy != nil {
		sessionCounts = s.proxy.ReplicaSessionCounts(slug)
		resp.SessionsCap = s.proxy.PoolCap(slug)
	}

	var infos []*process.ProcessInfo
	if s.manager != nil {
		infos = s.manager.AllForSlug(slug)
	}

	// Sessions-count slice may be shorter than infos if SetPoolSize raced
	// with a Deregister; clamp lookups to avoid out-of-range reads.
	sessionAt := func(i int) int64 {
		if i < len(sessionCounts) {
			return sessionCounts[i]
		}
		return -1
	}

	anyRunning := false
	for i, info := range infos {
		rm := replicaMetrics{Index: i, DesiredState: "running", Sessions: sessionAt(i)}
		if info == nil {
			rm.Status = string(process.StatusStopped)
			resp.Replicas = append(resp.Replicas, rm)
			continue
		}
		rm.Status = string(info.Status)
		rm.PID = info.PID
		rm.Tier = info.Tier
		rm.Provider = info.Provider
		if info.Status == process.StatusCrashed {
			if verdict, ok := s.manager.LastExit(slug, i); ok {
				rm.ExitCode, rm.Signal = verdict.ExitCode, verdict.Signal
				rm.RestartCount, rm.Reason = verdict.RestartCount, verdict.Reason
			}
		}
		if info.Status == process.StatusRunning {
			if handle, ok := s.manager.HandleReplica(slug, i); ok {
				if stats, err := s.sampler.Sample(handle); err == nil {
					rm.CPUPercent = stats.CPUPercent
					rm.RSSBytes = stats.RSSBytes
					rm.PSSBytes = stats.PSSBytes
					rm.USSBytes = stats.USSBytes
					rm.SwapPSSBytes = stats.SwapPSSBytes
					rm.MemoryAttributionPartial = stats.AttributionPartial
					// MetricsAvailable is true only when the sample succeeded for a
					// PID-backed handle; a zero PID (Fargate/remote_docker) or a
					// failed sample both mean live CPU/RAM are not available.
					rm.MetricsAvailable = handle.PID != 0
				} else {
					rm.Status = string(process.StatusStopped)
				}
			} else {
				rm.Status = string(process.StatusStopped)
			}
			if rm.Status == string(process.StatusRunning) && !anyRunning {
				anyRunning = true
				resp.PID = rm.PID
				resp.CPUPercent = rm.CPUPercent
				resp.RSSBytes = rm.RSSBytes
				resp.PSSBytes = rm.PSSBytes
				resp.USSBytes = rm.USSBytes
				resp.SwapPSSBytes = rm.SwapPSSBytes
				resp.MemoryAttributionPartial = rm.MemoryAttributionPartial
			}
		}
		resp.Replicas = append(resp.Replicas, rm)
		if rm.MetricsAvailable && rm.Status == string(process.StatusRunning) {
			resp.MetricsAvailable = true
		}
	}
	if anyRunning {
		resp.Status = string(process.StatusRunning)
	}

	// Elastic (grouped/per_session) pools: rebuild the rows from the proxy's
	// live capacity view. The multiplex session counters above never populate
	// for these pools (sessions stays -1), so per-worker bound-session counts
	// come from the pool snapshot; slots the proxy has reserved but whose
	// process has not started yet appear as "booting" rows, and manager
	// leftovers for terminated slots are dropped (elastic slot IDs are never
	// reused, so they would otherwise accumulate forever under worker churn).
	// sessions_cap becomes the per-worker cap: ceiling = max_workers x cap.
	elastic := false
	if s.proxy != nil {
		if snap, ok := s.proxy.ElasticWorkersSnapshot(slug); ok {
			elastic = true
			resp.SessionsCap = snap.SessionsPerWorker
			resp.WorkerIsolation = snap.Mode
			resp.MaxWorkers = snap.MaxWorkers
			managerRow := make(map[int]replicaMetrics, len(resp.Replicas))
			for _, rm := range resp.Replicas {
				managerRow[rm.Index] = rm
			}
			rows := make([]replicaMetrics, 0, len(snap.Workers))
			for _, w := range snap.Workers {
				rm, tracked := managerRow[w.SlotID]
				if !tracked {
					rm = replicaMetrics{Index: w.SlotID, DesiredState: "running"}
				}
				rm.Sessions = int64(w.Sessions)
				// The manager tracks process health; the proxy tracks routing.
				// Transitional routing states the manager cannot see (booting
				// before the process exists, draining) win the display;
				// otherwise the manager's health view stands.
				if w.Status != "running" || !tracked {
					rm.Status = w.Status
				}
				rows = append(rows, rm)
			}
			resp.Replicas = rows // snapshot order: sorted by slot
		}
	}

	// Overlay DB lost-status onto the live, manager-sourced replica list. "lost"
	// is a DB-only concept the manager pool does not track, so without this the
	// poll would render a lost replica as "stopped" (or omit it when the pool is
	// empty) and drop the worker-unavailable reason the app envelope derives.
	// Overlay onto the matching slot when present, else append. Multiplex only:
	// elastic workers are never persisted to the replicas table, so for an
	// elastic pool every DB replica row is a stale leftover - and the
	// positional indexing below does not hold on the compacted elastic rows.
	if elastic {
		dbReplicas = nil
	}
	for _, rep := range dbReplicas {
		if rep.Status != db.ReplicaStatusLost && rep.Status != "crashed" {
			continue
		}
		reason := rep.Reason
		if rep.Status == db.ReplicaStatusLost {
			reason = s.lostReplicaReason(rep.Tier)
		} else if reason == "" {
			reason = "replica process exited unexpectedly"
		}
		if rep.Index < len(resp.Replicas) {
			resp.Replicas[rep.Index].Status = rep.Status
			resp.Replicas[rep.Index].DesiredState = rep.DesiredState
			resp.Replicas[rep.Index].Reason = reason
			resp.Replicas[rep.Index].ExitCode = rep.ExitCode
			resp.Replicas[rep.Index].Signal = rep.Signal
			resp.Replicas[rep.Index].RestartCount = rep.RestartCount
			resp.Replicas[rep.Index].Tier = rep.Tier
			resp.Replicas[rep.Index].Provider = rep.Provider
			resp.Replicas[rep.Index].MetricsAvailable = false
		} else {
			resp.Replicas = append(resp.Replicas, replicaMetrics{
				Index:        rep.Index,
				Status:       rep.Status,
				DesiredState: rep.DesiredState,
				Reason:       reason,
				ExitCode:     rep.ExitCode,
				Signal:       rep.Signal,
				RestartCount: rep.RestartCount,
				Tier:         rep.Tier,
				Provider:     rep.Provider,
				Sessions:     -1,
			})
		}
	}

	// Attach the app's deployment-resolved capacity to each final display row
	// after elastic and lost-replica overlays establish its provider tier.
	// Overview can compare usage with the configured per-replica ceiling and use
	// the row's tier to verify whether that provider actually enforces it.
	for i := range resp.Replicas {
		s.decorateReplicaResources(app, &resp.Replicas[i])
	}

	observationReplicas := make([]*db.Replica, 0, len(resp.Replicas))
	pool := elasticPool{Known: elastic}
	for _, rep := range resp.Replicas {
		observationReplicas = append(observationReplicas, &db.Replica{
			Index: rep.Index, Status: rep.Status, DesiredState: rep.DesiredState,
			Reason: rep.Reason, ExitCode: rep.ExitCode, Signal: rep.Signal,
			RestartCount: rep.RestartCount,
		})
		if elastic {
			pool.observe(rep.Status)
		}
	}
	resp.WorkersRunning = pool.Running
	s.decorateAppObservation(&observedApp, observationReplicas, pool)
	resp.Status = observedApp.Status
	resp.DesiredStatus = observedApp.DesiredStatus
	resp.ReplicasRunning = observedApp.ReplicasRunning

	metricsAS := buildAutoscaleStatus(autoscaleEvent, autoscaleFound, s.cfg.Runtime.Autoscale.Cooldown)
	resp.AutoscaleStatus = &metricsAS

	return resp
}

func (s *Server) decorateReplicaResources(app *db.App, rm *replicaMetrics) {
	tier := rm.Tier
	if tier == "" {
		tier = s.cfg.Runtime.DefaultTierName()
	}
	// Deployment resolves one placement-aware pair for the app and passes it to
	// every replica. Match that exact behavior here; resolving each row from its
	// tier would overstate precision for the documented multi-tier fallback.
	defaultMem, defaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
	rm.EffectiveMemoryLimitMB = deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, defaultMem)
	rm.EffectiveCPUQuotaPercent = deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, defaultCPU)
	if s.manager != nil {
		rm.ResourceEnforcementKnown = true
		rm.MemoryLimitEnforced, rm.CPUQuotaEnforced = s.manager.ResourceEnforcement(tier)
	}
}

// handleBatchMetrics returns the live metrics for many apps in one response,
// keyed by slug, so the dashboard populates every card's CPU/RAM/status with a
// single request instead of one round-trip per app. The slugs to report are
// taken from the comma-separated ?slugs= query (the cards currently on screen);
// any the caller may not view are silently skipped. With no ?slugs= it reports
// all apps visible to the caller.
func (s *Server) handleBatchMetrics(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	apps, err := s.metricAppsForUser(u, r.URL.Query().Get("slugs"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Batch the two per-card DB reads (replicas, latest autoscale event) so the
	// whole poll costs a few queries instead of three per card.
	appIDs := make([]int64, 0, len(apps))
	slugs := make([]string, 0, len(apps))
	for _, app := range apps {
		appIDs = append(appIDs, app.ID)
		slugs = append(slugs, app.Slug)
	}
	replicasByApp, err := s.store.ListReplicasForApps(appIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	autoscaleBySlug, err := s.store.LatestAutoscaleEventForSlugs(slugs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	out := make(map[string]metricsResponse, len(apps))
	for _, app := range apps {
		ev, found := autoscaleBySlug[app.Slug]
		out[app.Slug] = s.buildAppMetricsFrom(app.Slug, app, replicasByApp[app.ID], ev, found)
	}
	body := map[string]any{"metrics": out, "generated_at": time.Now().UTC()}
	// The host block is the scale Overview measures against when no app carries
	// an enforced per-replica limit. Omitted when unknown, so the dashboard can
	// say so rather than infer a size.
	if s.hostCapacity != nil {
		body["host"] = s.hostCapacity
	}
	writeJSON(w, http.StatusOK, body)
}

// HostCapacity is the size of the box ShinyHub runs on: how much CPU and memory
// exist in total, and which source reported each. Cores come from the cgroup
// quota, CPU affinity, or server.host_capacity_cores; memory from the cgroup
// limit, the host total, or server.host_capacity_memory_mb.
//
// This is a reporting scale, not a budget. Nothing is admitted or rejected
// against it; it exists so an operator with no per-app limits set still sees
// where the fleet sits.
//
// The memory pair is omitted rather than zeroed when no source could answer:
// "this host has no memory" is a plausible-looking value for something that is
// simply not known, and it would render as a full meter.
type HostCapacity struct {
	Cores        float64 `json:"cores"`
	CoresSource  string  `json:"cores_source"`
	MemoryMB     int     `json:"memory_mb,omitempty"`
	MemorySource string  `json:"memory_source,omitempty"`
}

// metricAppsForUser resolves a batch-observability request to the exact apps
// the caller may view. Live metrics and history share it so neither endpoint
// can drift into exposing a broader fleet than the apps list.
func (s *Server) metricAppsForUser(u *auth.ContextUser, rawSlugs string) ([]*db.App, error) {
	var apps []*db.App
	if raw := strings.TrimSpace(rawSlugs); raw != "" {
		var slugs []string
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				slugs = append(slugs, s)
			}
		}
		fetched, err := s.store.GetAppsBySlugs(slugs)
		if err != nil {
			return nil, err
		}
		for _, app := range fetched {
			if ok, verr := s.canViewApp(u, app); verr == nil && ok {
				apps = append(apps, app)
			}
			// unknown or not-viewable slugs are silently skipped
		}
	} else {
		// What "every app" means depends on the caller, exactly as it does for
		// GET /api/apps: a privileged operator sees the whole server, everyone
		// else sees what they own or have been granted. Deriving both from the
		// same rule keeps a card and its metrics from disagreeing about which
		// apps exist, and keeps this branch consistent with ?slugs=, where an
		// admin naming an app it does not own has always been answered.
		var (
			visible []*db.App
			err     error
		)
		if isPrivilegedAppOperator(u) {
			visible, err = s.store.ListApps(0, 0)
		} else {
			visible, err = s.store.ListAppsVisibleToUser(u.ID, 0, 0)
		}
		if err != nil {
			return nil, err
		}
		// Neither query knows about token scope, and scope beats role and
		// visibility both. Without this filter a token scoped to one app could
		// read every public app's pids and resource usage by omitting ?slugs=,
		// which naming those apps explicitly would have refused.
		for _, app := range visible {
			if u.AppInScope(app.Slug) {
				apps = append(apps, app)
			}
		}
	}
	return apps, nil
}

func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, _, ok := s.requireViewApp(w, r, slug); !ok {
		return
	}
	deployments, err := s.store.ListDeploymentsBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	limit, offset := parsePagination(r)
	writeList(w, deployments, limit, offset, nil)
}

// writeQuotaExceeded returns a 413 with structured detail so callers can
// surface the measured footprint alongside the configured quota.
func writeQuotaExceeded(w http.ResponseWriter, usedBytes int64, quotaMB int) {
	writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
		"error":    "app disk quota exceeded",
		"used_mb":  usedBytes / deploy.MiB,
		"quota_mb": quotaMB,
	})
}

// logQuotaRejected emits an audit record so operators can see when a deploy
// was rejected for quota reasons (and by whom).
func (s *Server) logQuotaRejected(r *http.Request, slug string, usedBytes int64) {
	var userID *int64
	if u := auth.UserFromContext(r.Context()); u != nil {
		userID = &u.ID
	}
	s.logAuditEvent(r, db.AuditEventParams{
		UserID:       userID,
		Action:       "deploy_rejected_quota",
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       fmt.Sprintf("used=%d bytes, quota=%d MiB", usedBytes, s.cfg.Storage.AppQuotaMB),
		IPAddress:    s.ClientIP(r),
	})
}

// parsePagination extracts optional ?limit= and ?offset= query parameters.
// Returns 0 for both when absent, which callers interpret as "no pagination".
func parsePagination(r *http.Request) (limit, offset int) {
	if s := r.URL.Query().Get("limit"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			limit = v
		}
	}
	if s := r.URL.Query().Get("offset"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 {
			offset = v
		}
	}
	return limit, offset
}

// lostReplicaReason returns the presentation-only reason for a replica that is
// in the "lost" state, or "" when none applies. A lost replica whose tier has
// no healthy worker is stranded until one joins (the watchdog cannot re-place
// it), so surface "worker unavailable" to disambiguate that degraded state from
// a mid-heal lost slot. Shared by the app envelope and the live metrics poll so
// the two cannot diverge.
func (s *Server) lostReplicaReason(tier string) string {
	if s.workerReg == nil {
		return ""
	}
	if _, ok := s.workerReg.WorkerForTier(tier); !ok {
		return "worker unavailable"
	}
	return ""
}

// appTargetsFargate reports whether any tier this app is placed on uses the
// "fargate" runtime. Unlike allTiersFargate (which inspects the global config),
// this is per-app: it resolves the app's placement tiers and checks each one.
func (s *Server) appTargetsFargate(app *db.App) bool {
	for _, tier := range s.tiersForApp(app) {
		if rt, _ := s.cfg.Runtime.RuntimeForTier(tier); rt == "fargate" {
			return true
		}
	}
	return false
}

// allTiersFargate reports true when every declared runtime tier uses the
// "fargate" runtime. Used to scope write-time resource-ceiling enforcement to
// single-tier Fargate deployments where a single task-level ceiling applies to
// all replicas; mixed-tier deployments are guarded at RunTask time instead.
func (s *Server) allTiersFargate() bool {
	tiers := s.cfg.Runtime.TierOrder()
	if len(tiers) == 0 {
		return false
	}
	for _, t := range tiers {
		rt, _ := s.cfg.Runtime.RuntimeForTier(t)
		if rt != "fargate" {
			return false
		}
	}
	return true
}

// fargateLimitViolation returns a clear message when a per-app memory/CPU limit
// would exceed the Fargate task ceiling, or "" when there is none. It is gated on
// allTiersFargate (the platform's deliberate, single-answer rule: only an
// all-Fargate cluster has one task ceiling to check against; mixed-tier
// deployments are guarded at RunTask). Shared by the API PATCH and the manifest
// deploy paths so both reject up front rather than letting Fargate silently clamp
// (which would leave the DB/UI/audit claiming a higher limit than is enforced).
// nil mem/cpu are skipped (the field is not being set).
func (s *Server) fargateLimitViolation(memMB, cpuPct *int) string {
	if !s.allTiersFargate() {
		return ""
	}
	fc := s.cfg.Runtime.Fargate
	if memMB != nil && *memMB > 0 && fc.TaskMemoryMB > 0 && *memMB > fc.TaskMemoryMB {
		return fmt.Sprintf("memory_limit_mb %d exceeds the Fargate task ceiling of %d MiB (runtime.fargate.task_memory_mb); reduce the limit or raise the task definition", *memMB, fc.TaskMemoryMB)
	}
	if cpuPct != nil && *cpuPct > 0 && fc.TaskCPUUnits > 0 {
		// Integer division mirrors buildContainerOverride's conservative clamp.
		cpuUnits := (*cpuPct * 1024) / 100
		if cpuUnits > fc.TaskCPUUnits {
			return fmt.Sprintf("cpu_quota_percent %d%% (%d units) exceeds the Fargate task ceiling of %d units (runtime.fargate.task_cpu_units)", *cpuPct, cpuUnits, fc.TaskCPUUnits)
		}
	}
	return ""
}

// hasLiveReplica reports whether at least one replica process for slug is
// currently alive in the manager. When no manager is configured it returns
// true so callers that need a conservative default (e.g. the no-op branch of
// if_not_running) treat a missing manager as "running" and do not spuriously
// re-start the app.
func (s *Server) hasLiveReplica(slug string) bool {
	if s.manager == nil {
		return true
	}
	for _, p := range s.manager.AllForSlug(slug) {
		if p != nil {
			return true
		}
	}
	return false
}
