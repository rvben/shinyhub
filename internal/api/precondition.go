package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

const (
	hdrIfContentDigest    = "X-Shinyhub-If-Content-Digest"
	hdrIfManagedBy        = "X-Shinyhub-If-Managed-By"
	hdrIfResourceRevision = "X-Shinyhub-If-Resource-Revision"
	hdrResourceRevision   = "X-Shinyhub-Resource-Revision"
)

// appResourceRevision is an opaque, deterministic fingerprint of the durable
// app state a plan is allowed to rely on. Clients must compare it as an opaque
// string; fields may be added in later server versions without changing the
// protocol. Presentation-only fields are deliberately excluded.
func appResourceRevision(app *db.App) string {
	snapshot := struct {
		ID                       int64     `json:"id"`
		Slug                     string    `json:"slug"`
		Name                     string    `json:"name"`
		Description              string    `json:"description"`
		IconMime                 string    `json:"icon_mime"`
		IconEmoji                string    `json:"icon_emoji"`
		OwnerID                  int64     `json:"owner_id"`
		Access                   string    `json:"access"`
		Status                   string    `json:"status"`
		UpdatedAt                time.Time `json:"updated_at"`
		DeployCount              int       `json:"deploy_count"`
		ReleaseNumber            int       `json:"release_number"`
		CurrentVersion           string    `json:"current_version"`
		ContentDigest            string    `json:"content_digest"`
		LastDeploymentStatus     string    `json:"last_deployment_status"`
		ManagedBy                *string   `json:"managed_by"`
		Replicas                 int       `json:"replicas"`
		MaxSessionsPerReplica    int       `json:"max_sessions_per_replica"`
		HibernateTimeoutMinutes  *int      `json:"hibernate_timeout_minutes"`
		MemoryLimitMB            *int      `json:"memory_limit_mb"`
		CPUQuotaPercent          *int      `json:"cpu_quota_percent"`
		ProjectSlug              string    `json:"project_slug"`
		ReplicaPlacement         string    `json:"replica_placement"`
		AutoscaleEnabled         bool      `json:"autoscale_enabled"`
		AutoscaleMinReplicas     int       `json:"autoscale_min_replicas"`
		AutoscaleMaxReplicas     int       `json:"autoscale_max_replicas"`
		AutoscaleTarget          float64   `json:"autoscale_target"`
		LastAutoscaleAt          int64     `json:"last_autoscale_at"`
		IdentityHeaders          *bool     `json:"identity_headers"`
		UsageIdentityMode        *string   `json:"usage_identity_mode"`
		MinWarmReplicas          int       `json:"min_warm_replicas"`
		WorkerIsolation          string    `json:"worker_isolation"`
		WorkerGroupedSize        int       `json:"worker_grouped_size"`
		WorkerMaxWorkers         int       `json:"worker_max_workers"`
		WorkerMaxSessionLifetime int       `json:"worker_max_session_lifetime_secs"`
		EphemeralDataAck         bool      `json:"ephemeral_data_ack"`
		RenderSeconds            float64   `json:"render_seconds"`
		LastError                string    `json:"last_error"`
		CrashedAt                int64     `json:"crashed_at"`
	}{
		ID: app.ID, Slug: app.Slug, Name: app.Name, Description: app.Description,
		IconMime: app.IconMime, IconEmoji: app.IconEmoji,
		OwnerID: app.OwnerID, Access: app.Access,
		Status: app.Status, UpdatedAt: app.UpdatedAt.UTC(), DeployCount: app.DeployCount,
		ReleaseNumber: app.ReleaseNumber, CurrentVersion: app.CurrentVersion,
		ContentDigest: app.ContentDigest, LastDeploymentStatus: app.LastDeploymentStatus,
		ManagedBy: app.ManagedBy, Replicas: app.Replicas,
		MaxSessionsPerReplica:   app.MaxSessionsPerReplica,
		HibernateTimeoutMinutes: app.HibernateTimeoutMinutes, MemoryLimitMB: app.MemoryLimitMB,
		CPUQuotaPercent: app.CPUQuotaPercent, ProjectSlug: app.ProjectSlug,
		ReplicaPlacement: app.ReplicaPlacement, AutoscaleEnabled: app.AutoscaleEnabled,
		AutoscaleMinReplicas: app.AutoscaleMinReplicas, AutoscaleMaxReplicas: app.AutoscaleMaxReplicas,
		AutoscaleTarget: app.AutoscaleTarget, IdentityHeaders: app.IdentityHeaders,
		UsageIdentityMode: app.UsageIdentityMode,
		LastAutoscaleAt:   app.LastAutoscaleAt,
		MinWarmReplicas:   app.MinWarmReplicas, WorkerIsolation: app.WorkerIsolation,
		WorkerGroupedSize: app.WorkerGroupedSize, WorkerMaxWorkers: app.WorkerMaxWorkers,
		WorkerMaxSessionLifetime: app.WorkerMaxSessionLifetimeSecs,
		EphemeralDataAck:         app.EphemeralDataAck, RenderSeconds: app.RenderSeconds,
		LastError: app.LastError, CrashedAt: app.CrashedAt,
	}
	b, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(b)
	return "rev:app:" + hex.EncodeToString(sum[:])
}

// checkAppPreconditions returns true and writes a 409 if any present
// precondition header does not match the app's current state. Absent headers
// impose no condition (backward compatible). An If-Managed-By header present
// with an empty value asserts the app is currently unmanaged (NULL).
func checkAppPreconditions(w http.ResponseWriter, r *http.Request, app *db.App) (conflict bool) {
	if want := r.Header.Get(hdrIfResourceRevision); want != "" {
		if current := appResourceRevision(app); current != want {
			writeError(w, http.StatusConflict,
				"precondition failed: app revision changed (re-run plan)")
			return true
		}
	}
	// An empty value is treated as absent: a real content digest is always
	// non-empty, so there is no "assert empty digest" case to honor (unlike
	// If-Managed-By, where empty means "assert currently unmanaged").
	if want := r.Header.Get(hdrIfContentDigest); want != "" {
		if app.ContentDigest != want {
			writeError(w, http.StatusConflict,
				"precondition failed: content_digest changed (re-run plan)")
			return true
		}
	}
	if _, present := r.Header[hdrIfManagedBy]; present {
		want := r.Header.Get(hdrIfManagedBy)
		cur := ""
		if app.ManagedBy != nil {
			cur = *app.ManagedBy
		}
		if cur != want {
			writeError(w, http.StatusConflict,
				"precondition failed: managed_by changed (re-run plan)")
			return true
		}
	}
	return false
}
