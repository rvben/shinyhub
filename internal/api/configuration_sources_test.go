package api

import (
	"encoding/json"
	"github.com/BurntSushi/toml"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fleet"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigurationSourcesServingBundleAndSafeExport(t *testing.T) {
	cfg := &config.Config{}
	cfg.Lifecycle.HibernateTimeout = 30 * time.Minute
	srv, app := newScaleTestServer(t, "sources", 2, cfg)
	dep, _ := srv.store.GetServingDeployment(app.ID)
	manifest := `[app]
name = "From file"
cpu_quota_percent = 150
hibernate_timeout_minutes = -1
command = ["secret-launch-command", "{port}"]
[app.env]
TOKEN = "secret-env-value"
REGION = "europe"
`
	if err := os.WriteFile(filepath.Join(dep.BundleDir, deploy.ManifestFilename), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	out := srv.configurationSources(app)
	if out.BundleStatus != "available" {
		t.Fatalf("%+v", out)
	}
	if f := out.Fields["name"]; f.Source != "bundle" || !f.Differs || f.Declared != `"From file"` {
		t.Fatalf("name: %+v", f)
	}
	if f := out.Fields["hibernate_timeout_minutes"]; f.Source != "bundle" || f.Effective != "30" || f.Differs {
		t.Fatalf("inheritance: %+v", f)
	}
	if f := out.Fields["memory_limit_mb"]; f.Source != "server_default" {
		t.Fatalf("memory: %+v", f)
	}
	if len(out.BundleEnvKeys) != 2 || out.BundleEnvKeys[0] != "REGION" {
		t.Fatalf("env: %v", out.BundleEnvKeys)
	}
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"secret-env-value", "secret-launch-command", "europe"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if _, err := deploy.ParseManifest([]byte(out.SavedTOML)); err != nil {
		t.Fatalf("invalid export: %v\n%s", err, out.SavedTOML)
	}
	// A newer unpromoted bundle must not become the source of truth.
	pending, err := srv.store.BeginDeployment(app.ID, "candidate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending.BundleDir, deploy.ManifestFilename), []byte(`[app]
name="Wrong candidate"`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := srv.configurationSources(app); got.Fields["name"].Declared != `"From file"` {
		t.Fatalf("candidate treated as serving: %+v", got)
	}
}

func TestConfigurationSourcesUnavailableIsNotAbsence(t *testing.T) {
	for _, mode := range []string{"absent", "missing-directory", "invalid", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			srv, app := newScaleTestServer(t, "sources", 1, &config.Config{})
			dep, _ := srv.store.GetServingDeployment(app.ID)
			file := filepath.Join(dep.BundleDir, deploy.ManifestFilename)
			switch mode {
			case "missing-directory":
				os.RemoveAll(dep.BundleDir)
			case "invalid":
				os.WriteFile(file, []byte("invalid ["), 0600)
			case "symlink":
				os.Symlink("/nonexistent", file)
			}
			want := "unavailable"
			if mode == "absent" {
				want = "absent"
			}
			if got := srv.configurationSources(app).BundleStatus; got != want {
				t.Fatalf("%s, want %s", got, want)
			}
		})
	}
}

func TestConfigurationSourcesFleetBaselineAndIncompleteApply(t *testing.T) {
	srv, app := newScaleTestServer(t, "sources", 1, &config.Config{})
	marker := "fleet:team"
	app.ManagedBy = &marker
	if got := srv.configurationSources(app); got.FleetStatus != "unavailable" || got.Fields["name"].Source != "unconfirmed" {
		t.Fatalf("invented baseline: %+v", got)
	}
	_, _, err := srv.store.CreateFleetRun(db.CreateFleetRunParams{ID: "source-run", FleetID: "team", Kind: "apply"})
	if err != nil {
		t.Fatal(err)
	}
	err = srv.store.RecordAppFleetSuccess(app.ID, "source-run", "", []db.FleetDeclaredValue{{Key: "name", Desired: quoted(app.Name)}, {Key: "cpu_quota_percent", Desired: "200"}})
	if err != nil {
		t.Fatal(err)
	}
	got := srv.configurationSources(app)
	if got.FleetStatus != "recorded" || got.Fields["name"].Source != "fleet" || got.Fields["name"].Differs || !got.Fields["cpu_quota_percent"].Differs {
		t.Fatalf("wrong baseline: %+v", got)
	}
	_, _, err = srv.store.CreateFleetRun(db.CreateFleetRunParams{ID: "source-failed", FleetID: "team", Kind: "apply"})
	if err != nil {
		t.Fatal(err)
	}
	if err = srv.store.RecordAppFleetIncomplete(app.ID, "source-failed", "failed"); err != nil {
		t.Fatal(err)
	}
	got = srv.configurationSources(app)
	if got.FleetStatus != "incomplete" || got.Fields["name"].Source != "fleet" {
		t.Fatalf("lost proven baseline: %+v", got)
	}
	// Decode the merge fragment into the real fleet schema and reject unsupported fields.
	var fragment struct {
		App []struct {
			Slug   string       `toml:"slug"`
			Config fleet.Config `toml:"config"`
		} `toml:"app"`
	}
	metadata, err := toml.Decode(got.SavedFleetTOML, &fragment)
	if err != nil || len(metadata.Undecoded()) != 0 {
		t.Fatalf("invalid fleet fragment %v %v\n%s", err, metadata.Undecoded(), got.SavedFleetTOML)
	}
	marker = "fleet:new-owner"
	if got = srv.configurationSources(app); got.FleetStatus != "unavailable" {
		t.Fatalf("stale ownership trusted: %+v", got)
	}
}

func TestConfigurationSnippetRoundTripsExplicitValues(t *testing.T) {
	_, app := newScaleTestServer(t, "source-export", 2, &config.Config{})
	cpu, mem := 125, 1024
	app.CPUQuotaPercent = &cpu
	app.MemoryLimitMB = &mem
	app.Description = "A \"quoted\" description\nwith another line"
	snippet := configurationSavedTOML(app, false)
	parsed, err := deploy.ParseManifest([]byte(snippet))
	if err != nil {
		t.Fatalf("%v\n%s", err, snippet)
	}
	if parsed.App.CPUQuotaPercent == nil || *parsed.App.CPUQuotaPercent != cpu || parsed.App.MemoryLimitMB == nil || *parsed.App.MemoryLimitMB != mem || parsed.App.Description == nil || *parsed.App.Description != app.Description {
		t.Fatalf("export changed saved values: %+v", parsed.App)
	}
	// Unlimited is explicit and must survive export; inheritance must stay omitted.
	cpu = 0
	app.MemoryLimitMB = nil
	parsed, err = deploy.ParseManifest([]byte(configurationSavedTOML(app, false)))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.App.CPUQuotaPercent == nil || *parsed.App.CPUQuotaPercent != 0 || parsed.App.MemoryLimitMB != nil {
		t.Fatalf("lost zero/inherit distinction: %+v", parsed.App)
	}
}

func TestConfigurationSourcesResolveInheritedAutoscaleTarget(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Autoscale.DefaultTarget = .65
	srv, app := newScaleTestServer(t, "source-target", 1, cfg)
	app.AutoscaleEnabled = true
	app.AutoscaleMinReplicas = 1
	app.AutoscaleMaxReplicas = 4
	app.AutoscaleTarget = 0
	field := srv.configurationSources(app).Fields["autoscale"]
	if field.Current != "on(1-4 @ default)" || field.Effective != "on(1-4 @ 0.65)" {
		t.Fatalf("inherited target hidden: %+v", field)
	}
}
