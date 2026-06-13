package api

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
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

	var (
		apps []*db.App
		err  error
	)
	if isPrivilegedAppOperator(u) {
		apps, err = s.store.ListApps(limit, offset)
	} else {
		apps, err = s.store.ListAppsVisibleToUser(u.ID, limit, offset)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if apps == nil {
		apps = []*db.App{}
	}
	writeJSON(w, http.StatusOK, apps)
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
	if len(req.Name) > 128 {
		writeError(w, http.StatusBadRequest, "name must be 128 characters or fewer")
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

	if err := storage.RequireFreeSlug(s.cfg, req.Slug); err != nil {
		if errors.Is(err, storage.ErrSlugInUse) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := s.store.CreateApp(db.CreateAppParams{
		Slug:        req.Slug,
		Name:        req.Name,
		ProjectSlug: req.ProjectSlug,
		OwnerID:     u.ID,
		Access:      access,
	}); err != nil {
		if errors.Is(err, db.ErrSlugTaken) {
			writeError(w, http.StatusConflict, "slug already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
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

	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       &u.ID,
		Action:       "create_app",
		ResourceType: "app",
		ResourceID:   req.Slug,
		IPAddress:    s.ClientIP(r),
	})
	writeJSON(w, http.StatusCreated, app)
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, u, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return
	}

	replicas, err := s.store.ListReplicas(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if replicas == nil {
		replicas = []*db.Replica{}
	}

	// Merge live process state into DB rows when the manager is available.
	if s.manager != nil {
		live := s.manager.AllForSlug(slug)
		for i, rep := range replicas {
			if rep.Index < len(live) && live[rep.Index] != nil {
				replicas[i].Status = string(live[rep.Index].Status)
				if live[rep.Index].PID != 0 {
					pid := live[rep.Index].PID
					replicas[i].PID = &pid
				}
				if live[rep.Index].Port != 0 {
					port := live[rep.Index].Port
					replicas[i].Port = &port
				}
			}
		}
	}

	// Derive a presentation-only reason for replicas lost to a dead worker.
	for i, rep := range replicas {
		if rep.Status == db.ReplicaStatusLost {
			replicas[i].Reason = s.lostReplicaReason(rep.Tier)
		}
	}

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
	canManage := canManageApp(u, app)
	if !canManage {
		if role, ok, err := s.effectiveAppMemberRole(u, app); err == nil && ok && role == "manager" {
			canManage = true
		}
	}

	envelope := map[string]any{
		"app":                                app,
		"replicas_status":                    replicas,
		"effective_max_sessions_per_replica": effectiveCap,
		"effective_autoscale_target":         effectiveTarget,
		"redeploy_in_flight":                 s.isRedeployInFlight(slug),
		"can_manage":                         canManage,
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
	asEvent, asFound, asErr := s.store.LatestAutoscaleEvent(slug)
	if asErr != nil {
		slog.Warn("autoscale status query", "err", asErr)
	}
	asStatus := buildAutoscaleStatus(asEvent, asFound, s.cfg.Runtime.Autoscale.Cooldown)
	envelope["autoscale_status"] = asStatus
	envelope["global_autoscale_enabled"] = s.cfg.Runtime.Autoscale.Enabled
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
		newMinWarmReplicas  int
		setMinWarmReplicas  bool
		newManagedBy        *string
		setManagedBy        bool
		placementKeyPresent bool
		setPlacement        bool // a non-null placement object was provided
		clearPlacement      bool // an explicit null placement was provided
		placementJSON       string
		placementTotal      int
		setAutoscale        bool
		autoEnabled         bool
		autoMin             int
		autoMax             int
		autoTarget          float64
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
		name = strings.TrimSpace(name)
		if len(name) < 1 || len(name) > 128 {
			writeError(w, http.StatusBadRequest, "name must be between 1 and 128 characters")
			return
		}
		newName, setName = name, true
	}

	if rawVal, present := raw["project_slug"]; present {
		var projectSlug string
		if err := json.Unmarshal(rawVal, &projectSlug); err != nil {
			writeError(w, http.StatusBadRequest, "project_slug must be a string")
			return
		}
		newProjectSlug, setProjectSlug = strings.TrimSpace(projectSlug), true
	}

	if rawVal, present := raw["memory_limit_mb"]; present {
		var v *int
		if err := json.Unmarshal(rawVal, &v); err != nil {
			writeError(w, http.StatusBadRequest, "memory_limit_mb must be an integer or null")
			return
		}
		if v != nil && *v < 0 {
			http.Error(w, "memory_limit_mb must be non-negative", http.StatusBadRequest)
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
		if v != nil && (*v < 0 || *v > 100) {
			http.Error(w, "cpu_quota_percent must be between 0 and 100", http.StatusBadRequest)
			return
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
		}
	}

	// replicas and placement both describe the pool shape, so a single request
	// may carry only one of them.
	if placementKeyPresent && setReplicas {
		writeError(w, http.StatusBadRequest, "set either replicas or placement, not both")
		return
	}
	// Changing the bare replica count on an app that already uses tier placement
	// would drift the stored placement from the replica count. Require the caller
	// to update (or clear) placement instead.
	if setReplicas && len(app.PlacementMap()) > 0 {
		writeError(w, http.StatusBadRequest, "app uses tier placement; update placement instead of replicas")
		return
	}

	// Fail fast: CPU/memory limits are only enforced by the Docker runtime.
	// Under native mode they would be silently ignored, giving a false sense
	// of containment. Reject the write rather than store an unenforceable
	// limit. Clearing a limit (null) or setting it to 0 is always allowed.
	if s.cfg.Runtime.Mode == "native" {
		if setMemoryLimitMB && memoryLimitMB != nil && *memoryLimitMB > 0 {
			writeError(w, http.StatusBadRequest,
				"memory_limit_mb is unenforceable under runtime.mode=native; switch to docker runtime or unset the limit")
			return
		}
		if setCPUQuotaPercent && cpuQuotaPercent != nil && *cpuQuotaPercent > 0 {
			writeError(w, http.StatusBadRequest,
				"cpu_quota_percent is unenforceable under runtime.mode=native; switch to docker runtime or unset the limit")
			return
		}
	}

	// Write-time rejection for single-tier Fargate deployments: a per-app
	// memory or CPU limit that exceeds the task-definition ceiling would cause
	// a cryptic RunTask error. Reject it here when every declared tier uses the
	// fargate runtime so the operator gets a clear message. For mixed-tier
	// deployments (some tiers are docker/native), the RunTask clamp in
	// fargate.buildContainerOverride handles the enforcement silently because a
	// single ceiling answer does not exist at the API layer.
	if s.allTiersFargate() {
		fargateCfg := s.cfg.Runtime.Fargate
		if setMemoryLimitMB && memoryLimitMB != nil && *memoryLimitMB > 0 &&
			fargateCfg.TaskMemoryMB > 0 && *memoryLimitMB > fargateCfg.TaskMemoryMB {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("memory_limit_mb %d exceeds the Fargate task ceiling of %d MiB (runtime.fargate.task_memory_mb); reduce the limit or raise the task definition",
					*memoryLimitMB, fargateCfg.TaskMemoryMB))
			return
		}
		// CPU: convert quota percent to ECS units for the ceiling comparison using
		// integer division (conservative: rejects only when the truncated units
		// clearly exceed the ceiling; the clamp in buildContainerOverride rounds
		// so the write-time check is never more restrictive than the actual cap).
		if setCPUQuotaPercent && cpuQuotaPercent != nil && *cpuQuotaPercent > 0 &&
			fargateCfg.TaskCPUUnits > 0 {
			cpuUnits := (*cpuQuotaPercent * 1024) / 100
			if cpuUnits > fargateCfg.TaskCPUUnits {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("cpu_quota_percent %d%% (%d units) exceeds the Fargate task ceiling of %d units (runtime.fargate.task_cpu_units)",
						*cpuQuotaPercent, cpuUnits, fargateCfg.TaskCPUUnits))
				return
			}
		}
	}

	if checkAppPreconditions(w, r, app) {
		return
	}

	// Apply core settings in a single transaction so a storage failure mid-write
	// never leaves the row half-updated. The managed_by marker is a separate
	// follow-up write (SetAppManagedBy) that runs after this transaction commits;
	// the post-patch refetch exposes the final consistent state to the caller.
	priorStatus, _, err := s.store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug:               slug,
		SetHibernate:       setHibernateTimeout,
		HibernateMinutes:   hibernateTimeout,
		SetName:            setName,
		Name:               newName,
		SetProjectSlug:     setProjectSlug,
		ProjectSlug:        newProjectSlug,
		SetMemoryLimitMB:   setMemoryLimitMB,
		MemoryLimitMB:      memoryLimitMB,
		SetCPUQuotaPercent: setCPUQuotaPercent,
		CPUQuotaPercent:    cpuQuotaPercent,
		SetReplicas:        setReplicas,
		Replicas:           newReplicas,
		SetMaxSessions:     setMaxSessions,
		MaxSessions:        newMaxSessions,
		SetMinWarmReplicas: setMinWarmReplicas,
		MinWarmReplicas:    newMinWarmReplicas,
	})
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
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

	// Post-commit side effects. These only take effect once the settings are
	// durably persisted.
	if setMaxSessions && s.proxy != nil {
		s.proxy.SetPoolCap(slug,
			deploy.ResolveMaxSessionsPerReplica(newMaxSessions, s.cfg.Runtime.DefaultMaxSessionsPerReplica))
	}
	if (setReplicas || setPlacement || clearPlacement) && priorStatus == "running" {
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
	if u := auth.UserFromContext(r.Context()); u != nil {
		detail := patchAppAuditDetail(setMinWarmReplicas, newMinWarmReplicas)
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID: &u.ID, Action: "update_app", ResourceType: "app",
			ResourceID: slug, Detail: detail, IPAddress: s.ClientIP(r),
		})
	}
	writeJSON(w, http.StatusOK, app)
}

// patchAppAuditDetail builds a JSON detail blob for the update_app audit event,
// including only the fields that were actually changed in this PATCH. Currently
// records the pre-warming floor when it is the changed field.
func patchAppAuditDetail(setMinWarmReplicas bool, minWarmReplicas int) string {
	d := map[string]any{}
	if setMinWarmReplicas {
		d["min_warm_replicas"] = minWarmReplicas
	}
	if len(d) == 0 {
		return ""
	}
	b, _ := json.Marshal(d)
	return string(b)
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

func (s *Server) handleDeployApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	if s.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "process manager not available")
		return
	}

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
	keepFiles := false
	defer func() {
		if !keepFiles {
			_ = os.RemoveAll(bundleDir)
			_ = os.Remove(bundleZip)
		}
	}()

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
		fmt.Fprintf(os.Stderr, "extract bundle %s: %v\n", slug, err)
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
	defer func() {
		if !keepFiles {
			_ = os.RemoveAll(bundleDir)
			_ = os.Remove(bundleZip)
		}
	}()

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

	// Capture the current live deployment so a failed deploy can restore the
	// previous pool, then durably record the new deployment as 'pending'
	// BEFORE the running pool is touched. ListDeployments excludes pending
	// rows, so recovery/watcher/scheduler/rollback keep pointing at the
	// previous bundle until PromoteDeployment confirms the new pool is live.
	var prevActive *db.Deployment
	if existing, lerr := s.store.ListDeployments(app.ID); lerr == nil && len(existing) > 0 {
		prevActive = existing[0]
	}
	pendingDep, err := s.store.BeginDeployment(app.ID, version, bundleDir)
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

	if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
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
			s.restorePreviousPool(slug, app, prevActive)
			writeError(w, http.StatusInternalServerError, "manifest apply failed")
			return
		}
		manifestApplied = true
		manifestSummary.App = manifestAppliedSummary(manifest.App)
		// Refresh so deploy.Run sees the updated replicas / max_sessions.
		if fresh, ferr := s.store.GetAppBySlug(slug); ferr == nil {
			app = fresh
		}
	}

	deployDefaultMem, deployDefaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
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
	}, app))
	if err != nil {
		reason := deployFailureMessage(err)
		fmt.Fprintf(os.Stderr, "deploy.Run %s: %v\n", slug, err)
		_ = s.store.FailDeploymentWithReason(pendingDep.ID, reason)
		// Revert manifest [app] settings so the restored old pool runs under
		// the settings it was deployed with, not the failed bundle's.
		if manifestApplied {
			if _, _, rerr := s.store.PatchAppSettings(db.PatchAppSettingsParams{
				Slug:             slug,
				SetHibernate:     true,
				HibernateMinutes: preManifestApp.HibernateTimeoutMinutes,
				SetReplicas:      true,
				Replicas:         preManifestApp.Replicas,
				SetMaxSessions:   true,
				MaxSessions:      preManifestApp.MaxSessionsPerReplica,
			}); rerr != nil {
				slog.Error("deploy: revert manifest [app] settings after failed deploy", "slug", slug, "err", rerr)
			}
			if s.proxy != nil {
				s.proxy.SetPoolCap(slug,
					deploy.ResolveMaxSessionsPerReplica(preManifestApp.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica))
			}
			if rerr := s.store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
				AppID: preManifestApp.ID, Slug: slug,
				SetIdentityHeaders: true, IdentityHeaders: preManifestApp.IdentityHeaders,
			}); rerr != nil {
				slog.Error("deploy: revert identity_headers after failed deploy", "slug", slug, "err", rerr)
			}
			if s.proxy != nil {
				s.proxy.SetPoolIdentityHeaders(slug,
					deploy.ResolveIdentityHeaders(preManifestApp.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()))
			}
		}
		s.restorePreviousPool(slug, &preManifestApp, prevActive)
		s.recordDeploy("failure")
		writeError(w, http.StatusInternalServerError, reason)
		return
	}
	// The pool is now serving the new bundle; from here onwards the on-disk
	// artefacts must survive any subsequent error so a follow-up rollback or
	// recovery still has the directory to point at.
	keepFiles = true

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
			fmt.Fprintf(os.Stderr, "upsert replica %s[%d]: %v\n", slug, r.Index, err)
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
			fmt.Fprintf(os.Stderr, "upsert failed replica %s[%d]: %v\n", slug, idx, err)
		}
	}
	// Bookkeeping after the proxy switch. Two writes here are different
	// kinds of important and are handled differently:
	//
	//  1. UpdateAppStatus("running") and IncrementDeployCount are
	//     soft state. The watchdog reconciles status; the never-deployed
	//     gate keys off the durable deployments row (HasAnyDeployment),
	//     not deploy_count. Log+continue is safe — neither failure traps
	//     the user out of an app whose pool is already live.
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
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
	}); err != nil {
		slog.Error("deploy: persist running status failed; pool is live", "slug", slug, "err", err)
	}

	if err := s.store.IncrementDeployCount(slug); err != nil {
		slog.Error("deploy: increment deploy_count failed; pool is live", "slug", slug, "err", err)
	}

	if err := s.store.PromoteDeployment(pendingDep.ID); err != nil {
		slog.Error("deploy: promote deployment failed; pool is live but next restart/wake/schedule would silently revert to the previous bundle — failing the request so the caller retries", "slug", slug, "version", version, "err", err)
		s.recordDeploy("failure")
		writeError(w, http.StatusInternalServerError, "deploy succeeded but recording it failed; retry to commit")
		return
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
			writeError(w, http.StatusInternalServerError, "schedule apply failed: "+err.Error())
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
			writeError(w, http.StatusInternalServerError, "access apply failed: "+err.Error())
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
			s.store.LogAuditEvent(db.AuditEventParams{
				UserID:       &u.ID,
				Action:       "reconcile_group_access",
				ResourceType: "app",
				ResourceID:   slug,
				Detail:       fmt.Sprintf("applied=%d skipped=%d", applied, len(agResults)-applied),
				IPAddress:    s.ClientIP(r),
			})
		}
	}

	// Prune old version directories beyond the retention limit.
	go func() {
		if err := deploy.PruneOldVersions(s.cfg.Storage.AppsDir, slug, s.cfg.Storage.VersionRetention, bundleDir); err != nil {
			fmt.Fprintf(os.Stderr, "prune old versions %s: %v\n", slug, err)
		}
	}()

	updatedApp, err := s.store.GetAppBySlug(slug)
	if err != nil {
		s.recordDeploy("failure")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "deploy",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.ClientIP(r),
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
	}{App: updatedApp, HooksSkipped: result.HooksSkipped}
	if !manifestSummary.IsEmpty() {
		resp.Manifest = &manifestSummary
	}
	// Record success only here, after every remaining error path has passed, so
	// shinyhub_deploys_total{result} matches the client-visible HTTP result.
	s.recordDeploy("success")
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRollbackApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

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

	// Capture the current live deployment for restore-on-failure, then record
	// the rollback target as a pending deployment BEFORE tearing down the pool
	// (same durability contract as a forward deploy).
	var prevActive *db.Deployment
	if existing, lerr := s.store.ListDeployments(app.ID); lerr == nil && len(existing) > 0 {
		prevActive = existing[0]
	}
	pendingDep, err := s.store.BeginDeployment(app.ID, prev.Version, prev.BundleDir)
	if err != nil {
		slog.Error("rollback: record pending deployment failed; running pool untouched", "slug", slug, "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
		writeError(w, http.StatusConflict, err.Error())
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
	}, app))
	if err != nil {
		fmt.Fprintf(os.Stderr, "rollback %s: %v\n", slug, err)
		_ = s.store.FailDeployment(pendingDep.ID)
		s.restorePreviousPool(slug, app, prevActive)
		writeError(w, http.StatusInternalServerError, "rollback failed")
		return
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
			fmt.Fprintf(os.Stderr, "upsert replica %s[%d]: %v\n", slug, r.Index, err)
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
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "rollback",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.ClientIP(r),
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
	}, app))
	if err != nil {
		fmt.Fprintf(os.Stderr, "restart %s: %v\n", slug, err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
			fmt.Fprintf(os.Stderr, "update app status for %s: %v\n", slug, err)
		}
		writeError(w, http.StatusInternalServerError, "restart failed")
		return
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
			fmt.Fprintf(os.Stderr, "upsert replica %s[%d]: %v\n", slug, r.Index, err)
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
		s.store.LogAuditEvent(db.AuditEventParams{
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
		// External secret-backend cleanup (Fargate Secrets Manager entries +
		// per-app task-def revisions) failed: keep the tombstone so reconcile
		// retries and neither secrets nor revisions orphan.
		detail = "deferred secret cleanup: " + secErr.Error()
		slog.Error("app delete secret cleanup failed; tombstone retained for reconcile", "slug", slug, "err", secErr)
	} else if err := s.store.DeleteApp(slug); err != nil && !errors.Is(err, db.ErrNotFound) {
		// Bytes are gone; only the tombstone row remains. Reconcile will drop
		// it on next startup, so this is not a client-visible failure.
		detail = "row delete deferred: " + err.Error()
		slog.Error("app delete: row removal failed after cleanup; tombstone retained", "slug", slug, "err", err)
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "delete_app",
			ResourceType: "app",
			ResourceID:   slug,
			Detail:       detail,
			IPAddress:    s.ClientIP(r),
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
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "stop",
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
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID: &u.ID, Action: "set_access", ResourceType: "app",
			ResourceID: slug, Detail: string(accessDetail), IPAddress: s.ClientIP(r),
		})
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
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "grant_access",
			ResourceType: "app",
			ResourceID:   slug,
			Detail:       auditDetail,
			IPAddress:    s.ClientIP(r),
		})
	}
	// Advertise the app's visibility so the CLI can warn that a grant on a
	// private app has no effect until it is shared (the 204 carries no body).
	w.Header().Set("X-Shinyhub-App-Access", app.Access)
	w.WriteHeader(http.StatusNoContent)
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
		s.store.LogAuditEvent(db.AuditEventParams{
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
		s.store.LogAuditEvent(db.AuditEventParams{
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
	writeJSON(w, http.StatusOK, resp)
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
		s.store.LogAuditEvent(db.AuditEventParams{
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
		s.store.LogAuditEvent(db.AuditEventParams{
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
	members, err := s.store.ListAppMembers(slug, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	resp := make([]appMemberResponse, len(members))
	for i, m := range members {
		resp[i] = appMemberResponse{UserID: m.UserID, Username: m.Username, Role: m.Role}
	}
	writeJSON(w, http.StatusOK, resp)
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

type replicaMetrics struct {
	Index      int     `json:"index"`
	Status     string  `json:"status"`
	PID        int     `json:"pid,omitempty"`
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	RSSBytes   int64   `json:"rss_bytes,omitempty"`
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
	MetricsAvailable bool   `json:"metrics_available"`
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
	// Status is the app-level status: "running" if any replica is running,
	// otherwise the dominant replica status (or the DB-recorded status if
	// no replicas are tracked yet).
	Status string `json:"status"`
	// SessionsCap is the per-replica session cap currently in effect for
	// this pool. 0 means uncapped.
	SessionsCap      int              `json:"sessions_cap"`
	Replicas         []replicaMetrics `json:"replicas"`
	MetricsAvailable bool             `json:"metrics_available"`
	AutoscaleStatus  *autoscaleStatus `json:"autoscale_status"`
	// Legacy fields preserved so existing clients (dashboard card poller)
	// keep working while they adopt the per-replica view. These mirror the
	// first running replica.
	PID        int     `json:"pid,omitempty"`
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	RSSBytes   int64   `json:"rss_bytes,omitempty"`
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

	resp := metricsResponse{Status: app.Status, Replicas: []replicaMetrics{}}

	if s.manager == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	var sessionCounts []int64
	if s.proxy != nil {
		sessionCounts = s.proxy.ReplicaSessionCounts(slug)
		resp.SessionsCap = s.proxy.PoolCap(slug)
	}

	infos := s.manager.AllForSlug(slug)

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
		rm := replicaMetrics{Index: i, Sessions: sessionAt(i)}
		if info == nil {
			rm.Status = string(process.StatusStopped)
			resp.Replicas = append(resp.Replicas, rm)
			continue
		}
		rm.Status = string(info.Status)
		rm.PID = info.PID
		rm.Tier = info.Tier
		rm.Provider = info.Provider
		if info.Status == process.StatusRunning {
			if handle, ok := s.manager.HandleReplica(slug, i); ok {
				if stats, err := s.sampler.Sample(handle); err == nil {
					rm.CPUPercent = stats.CPUPercent
					rm.RSSBytes = stats.RSSBytes
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

	// Overlay DB lost-status onto the live, manager-sourced replica list. "lost"
	// is a DB-only concept the manager pool does not track, so without this the
	// poll would render a lost replica as "stopped" (or omit it when the pool is
	// empty) and drop the worker-unavailable reason the app envelope derives.
	// Overlay onto the matching slot when present, else append.
	if dbReplicas, derr := s.store.ListReplicas(app.ID); derr == nil {
		for _, rep := range dbReplicas {
			if rep.Status != db.ReplicaStatusLost {
				continue
			}
			reason := s.lostReplicaReason(rep.Tier)
			if rep.Index < len(resp.Replicas) {
				resp.Replicas[rep.Index].Status = string(db.ReplicaStatusLost)
				resp.Replicas[rep.Index].Reason = reason
				resp.Replicas[rep.Index].Tier = rep.Tier
				resp.Replicas[rep.Index].Provider = rep.Provider
				resp.Replicas[rep.Index].MetricsAvailable = false
			} else {
				resp.Replicas = append(resp.Replicas, replicaMetrics{
					Index:    rep.Index,
					Status:   string(db.ReplicaStatusLost),
					Reason:   reason,
					Tier:     rep.Tier,
					Provider: rep.Provider,
					Sessions: -1,
				})
			}
		}
	}

	metricsEvent, metricsFound, metricsErr := s.store.LatestAutoscaleEvent(slug)
	if metricsErr != nil {
		slog.Warn("autoscale status metrics query", "err", metricsErr)
	}
	metricsAS := buildAutoscaleStatus(metricsEvent, metricsFound, s.cfg.Runtime.Autoscale.Cooldown)
	resp.AutoscaleStatus = &metricsAS

	writeJSON(w, http.StatusOK, resp)
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
	writeJSON(w, http.StatusOK, deployments)
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
	s.store.LogAuditEvent(db.AuditEventParams{
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
