package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fleet"
)

type configurationField struct {
	Source    string `json:"source"`
	Current   string `json:"current"`
	Effective string `json:"effective"`
	Declared  string `json:"declared,omitempty"`
	Differs   bool   `json:"differs"`
}
type configurationSources struct {
	BundleStatus   string                        `json:"bundle_status"`
	BundleVersion  string                        `json:"bundle_version,omitempty"`
	FleetStatus    string                        `json:"fleet_status"`
	FleetID        string                        `json:"fleet_id,omitempty"`
	Fields         map[string]configurationField `json:"fields"`
	BundleEnvKeys  []string                      `json:"bundle_env_keys,omitempty"`
	SavedTOML      string                        `json:"saved_toml"`
	SavedFleetTOML string                        `json:"saved_fleet_toml,omitempty"`
}

// Reads only the published bundle, never the newest failed/pending upload.
// Exposes an allowlist of settings, not the manifest text (which may contain secrets).
func (s *Server) configurationSources(app *db.App) configurationSources {
	out := configurationSources{BundleStatus: "unavailable", FleetStatus: "unmanaged", Fields: map[string]configurationField{}}
	current := currentFleetValues(app)
	effective := s.configurationEffectiveValues(app)
	for key, value := range current {
		if key == "source" || key == "visibility" || key == "identity_headers" || key == "worker_max_session_lifetime_secs" {
			continue
		}
		source := "saved"
		if value == "(unset)" || value == "(default)" || (key == "worker_isolation" && app.WorkerIsolation == "") || (key == "max_sessions_per_replica" && app.MaxSessionsPerReplica == 0) {
			source = "server_default"
		}
		out.Fields[key] = configurationField{Source: source, Current: value, Effective: effective[key]}
	}
	dep, err := s.store.GetServingDeployment(app.ID)
	if err == nil && dep.Status == db.DeploymentSucceeded {
		out.BundleVersion = dep.Version
		// A missing directory is unavailable, not proof that the bundle has no manifest.
		if info, e := os.Stat(dep.BundleDir); e == nil && info.IsDir() {
			path := filepath.Join(dep.BundleDir, deploy.ManifestFilename)
			info, e := os.Lstat(path)
			if os.IsNotExist(e) {
				out.BundleStatus = "absent"
			} else if e == nil && info.Mode().IsRegular() && info.Size() <= 1<<20 {
				f, e := os.Open(path)
				if e == nil {
					data, re := io.ReadAll(io.LimitReader(f, (1<<20)+1))
					f.Close()
					if re == nil && len(data) <= 1<<20 {
						m, pe := deploy.ParseManifest(data)
						if pe == nil {
							out.BundleStatus = "available"
							for key, value := range configurationManifestValues(m.App) {
								if field, ok := out.Fields[key]; ok {
									field.Source = "bundle"
									field.Declared = value
									field.Differs = field.Current != value
									out.Fields[key] = field
								}
							}
							for key := range m.App.Env {
								out.BundleEnvKeys = append(out.BundleEnvKeys, key)
							}
							sort.Strings(out.BundleEnvKeys)
						}
					}
				}
			}
		}
	} else if err == db.ErrNotFound {
		out.BundleStatus = "not_deployed"
	}
	out.FleetID = fleetIDFromMarker(app.ManagedBy)
	if out.FleetID != "" {
		out.FleetStatus = "unavailable"
		state, e := s.store.GetAppFleetState(app.ID)
		if e == nil && state != nil && state.SuccessfulRun != nil && state.SuccessfulRun.FleetID == out.FleetID && state.AppliedAt != nil {
			out.FleetStatus = "recorded"
			if state.ConvergenceStatus == fleetConvergenceIncomplete {
				out.FleetStatus = "incomplete"
			}
			for _, declared := range state.Declaration {
				if field, ok := out.Fields[declared.Key]; ok {
					field.Source = "fleet"
					field.Declared = declared.Desired
					field.Differs = field.Current != declared.Desired
					out.Fields[declared.Key] = field
				}
			}
		} else {
			// Ownership alone does not prove which keys the fleet controls.
			for key, field := range out.Fields {
				field.Source = "unconfirmed"
				out.Fields[key] = field
			}
		}
	}
	out.SavedTOML = configurationSavedTOML(app, false)
	if out.FleetID != "" {
		out.SavedFleetTOML = configurationSavedTOML(app, true)
	}
	return out
}

func configurationManifestValues(m deploy.AppSettings) map[string]string {
	out := map[string]string{}
	for key, value := range manifestAppliedSummary(m) {
		if key == "command" {
			continue
		}
		if key == "worker" {
			for k, v := range value.(map[string]any) {
				out["worker_"+k] = configurationCanonical(v)
			}
			continue
		}
		if key == "autoscale" {
			a := m.Autoscale
			out[key] = fleet.AutoscaleDisplay(a.Enabled != nil && *a.Enabled, a.MinReplicas, a.MaxReplicas, a.Target)
			continue
		}
		if key == "hibernate_timeout_minutes" && value == nil {
			out[key] = "(default)"
			continue
		}
		out[key] = configurationCanonical(value)
	}
	return out
}
func configurationCanonical(value any) string {
	if s, ok := value.(string); ok {
		return quoted(s)
	}
	return fmt.Sprint(value)
}
func (s *Server) configurationEffectiveValues(app *db.App) map[string]string {
	values := currentFleetValues(app)
	mem, cpu := s.cfg.Runtime.DefaultResourcesForApp(app)
	values["memory_limit_mb"] = fmt.Sprint(deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, mem))
	values["cpu_quota_percent"] = fmt.Sprint(deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, cpu))
	if app.HibernateTimeoutMinutes == nil {
		values["hibernate_timeout_minutes"] = fmt.Sprint(s.cfg.Lifecycle.HibernateTimeout.Minutes())
	}
	if app.MaxSessionsPerReplica == 0 {
		values["max_sessions_per_replica"] = fmt.Sprint(s.cfg.Runtime.DefaultMaxSessionsPerReplica)
	}
	values["worker_isolation"] = quoted(deploy.ResolveWorkerIsolation(app.WorkerIsolation, s.cfg.Runtime.DefaultWorkerIsolation))
	target := app.AutoscaleTarget
	if target == 0 {
		target = s.cfg.Runtime.Autoscale.DefaultTarget
	}
	values["autoscale"] = fleet.AutoscaleDisplay(app.AutoscaleEnabled, app.AutoscaleMinReplicas, app.AutoscaleMaxReplicas, target)
	override := ""
	if app.UsageIdentityMode != nil {
		override = *app.UsageIdentityMode
	}
	mode, collect := config.EffectiveUsageIdentityMode(s.cfg.Usage.IdentityMode, override)
	values["usage_identity_mode"] = quoted(string(mode))
	if !s.cfg.Usage.Enabled || !collect {
		values["usage_identity_mode"] = quoted("disabled")
	}
	return values
}

// A merge snippet of saved explicit settings only. Never exports env values,
// secrets, launch commands or hooks. It is not a replacement configuration file.
func configurationSavedTOML(app *db.App, isFleet bool) string {
	values := currentFleetValues(app)
	config := map[string]any{}
	worker := map[string]any{}
	for key, raw := range values {
		if key == "source" || key == "visibility" || key == "identity_headers" || key == "autoscale" {
			continue
		}
		if raw == "(unset)" || raw == "(default)" || (key == "worker_isolation" && app.WorkerIsolation == "") {
			continue
		}
		if isFleet && !map[string]bool{"name": true, "description": true, "project": true, "hibernate_timeout_minutes": true, "replicas": true, "max_sessions_per_replica": true, "availability": true, "render_seconds": true, "min_warm_replicas": true}[key] {
			continue
		}
		var value any
		if strings.HasPrefix(raw, "\"") {
			decoded, err := strconv.Unquote(raw)
			if err != nil {
				continue
			}
			value = decoded
		} else if integer, err := strconv.Atoi(raw); err == nil {
			value = integer
		} else if json.Unmarshal([]byte(raw), &value) != nil {
			continue
		}
		if strings.HasPrefix(key, "worker_") && !isFleet {
			worker[strings.TrimPrefix(key, "worker_")] = value
		} else {
			config[key] = value
		}
	}
	if len(worker) > 0 {
		config["worker"] = worker
	}
	config["autoscale"] = map[string]any{"enabled": app.AutoscaleEnabled, "min_replicas": app.AutoscaleMinReplicas, "max_replicas": app.AutoscaleMaxReplicas, "target": app.AutoscaleTarget}
	// The disabled policy may have zero bounds; that is valid only when disabled.
	root := map[string]any{"app": config}
	if isFleet {
		root = map[string]any{"app": []any{map[string]any{"slug": app.Slug, "config": config}}}
	}
	var b bytes.Buffer
	b.WriteString("# Merge these saved settings into the existing app entry.\n# Nullable inherited settings and environment variables are omitted.\n# Omitting a key does not clear an existing saved value.\n")
	if toml.NewEncoder(&b).Encode(root) != nil {
		return ""
	}
	return b.String()
}
