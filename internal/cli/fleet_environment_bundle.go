package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fleet"
)

// resolveEnvironmentBundle materializes sizing before server admission. The same
// bytes feed the digest, rehearsal and upload; the source tree stays untouched.
func resolveEnvironmentBundle(spec bundleBuildSpec, c fleet.Config, overlays ...fleet.OverlayConfig) (bundleBuildSpec, *deploy.Manifest, error) {
	spec.Availability = c.Availability
	data, err := fleetSourceManifestData(spec.Dir)
	if err != nil && !os.IsNotExist(err) {
		return spec, nil, err
	}
	// Validate the source before overlaying: an override must never hide malformed TOML.
	if len(data) > 0 {
		if _, err := deploy.ParseManifest(data); err != nil {
			return spec, nil, err
		}
	}
	root := map[string]any{}
	if len(data) > 0 {
		if _, err := toml.Decode(string(data), &root); err != nil {
			return spec, nil, err
		}
	}
	var baseline bytes.Buffer
	if err := toml.NewEncoder(&baseline).Encode(root); err != nil {
		return spec, nil, err
	}
	app, _ := root["app"].(map[string]any)
	hadApp := app != nil
	if app == nil {
		app = map[string]any{}
		root["app"] = app
	}
	force := fleet.Config{}
	if len(overlays) > 0 {
		force = overlays[0].Merge(force)
	}
	// Replica count uses the existing online resize path. Remove a conflicting
	// source declaration so a deploy cannot undo the fleet's live replica count.
	if c.Replicas != nil && app["replicas"] != nil && fmt.Sprint(app["replicas"]) != fmt.Sprint(*c.Replicas) {
		delete(app, "replicas")
	}
	if c.MinWarmReplicas != nil && (app["min_warm_replicas"] != nil || force.MinWarmReplicas != nil) {
		app["min_warm_replicas"] = *c.MinWarmReplicas
	}
	if c.MaxSessionsPerReplica != nil && (app["max_sessions_per_replica"] != nil || force.MaxSessionsPerReplica != nil) {
		app["max_sessions_per_replica"] = *c.MaxSessionsPerReplica
	}
	if c.HibernateTimeoutMinutes != nil && (app["hibernate_timeout_minutes"] != nil || force.HibernateTimeoutMinutes != nil) {
		app["hibernate_timeout_minutes"] = *c.HibernateTimeoutMinutes
	}
	if c.Availability != nil && app["availability"] != nil {
		app["availability"] = *c.Availability
	}
	if c.RenderSeconds != nil && (app["render_seconds"] != nil || force.RenderSeconds != nil) {
		app["render_seconds"] = *c.RenderSeconds
	}
	if c.Autoscale != nil && (app["autoscale"] != nil || force.Autoscale != nil) {
		app["autoscale"] = autoscalePatchBody(c.Autoscale)
	}
	if len(c.Env) > 0 {
		env, _ := app["env"].(map[string]any)
		if env == nil {
			env = map[string]any{}
		}
		for key, value := range c.Env {
			env[key] = value
		}
		app["env"] = env
	}
	if len(app) == 0 && !hadApp {
		delete(root, "app")
	}
	// Preserve original bytes (and absence) when the resolved values are identical.
	var canonical bytes.Buffer
	if err := toml.NewEncoder(&canonical).Encode(root); err != nil {
		return spec, nil, err
	}
	if len(app) == 0 && len(data) == 0 {
		return spec, nil, nil
	}
	if canonical.String() == baseline.String() {
		manifest, err := deploy.ParseManifest(data)
		return spec, manifest, err
	}
	if len(data) == 0 {
		root["sparse"] = true
	}
	var out bytes.Buffer
	if err := toml.NewEncoder(&out).Encode(root); err != nil {
		return spec, nil, err
	}
	manifest, err := deploy.ParseManifest(out.Bytes())
	if err != nil {
		return spec, nil, fmt.Errorf("merged environment config: %w", err)
	}
	spec.Manifest = append([]byte(nil), out.Bytes()...)
	return spec, manifest, nil
}

// Source manifests follow the exact same ignore policy as the bundle walk.
// Resolved overrides can still create a sparse control file; excluded source
// hooks, access rules and schedules must never be copied into it.
func fleetSourceManifestData(dir string) ([]byte, error) {
	matcher, err := bundle.LoadIgnoreMatcher(dir)
	if err != nil {
		return nil, err
	}
	if matcher != nil && matcher.MatchesPath(deploy.ManifestFilename) {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, deploy.ManifestFilename))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}
func loadFleetSourceManifest(dir string) (*deploy.Manifest, error) {
	data, err := fleetSourceManifestData(dir)
	if err != nil || len(data) == 0 {
		return nil, err
	}
	return deploy.ParseManifest(data)
}
