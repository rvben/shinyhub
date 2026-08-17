package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/fleet"
)

func TestPlanModelSingleAndFleetUseTheSameSemanticContract(t *testing.T) {
	single := deploymentPlanDocument(singlePlanModelFixture())
	fleetPlan := fleetPlanDocument(
		"shinyhub fleet plan", "fleets/eu.toml", &fleet.Manifest{FleetID: "eu"}, "https://hub.example.com",
		fleetPlanModelDiffFixture(),
		[]fleet.ProjectDiff{{
			Slug: "analytics", Action: fleet.ActionUpdateConfig,
			Drift: []fleet.ConfigDriftItem{{Key: "name", Server: "Analytics", Desired: "Revenue analytics"}},
		}},
	)

	for name, document := range map[string]planDocument{"single": single, "fleet": fleetPlan} {
		if document.SchemaVersion != planModelSchemaVersion {
			t.Fatalf("%s schema version = %d, want %d", name, document.SchemaVersion, planModelSchemaVersion)
		}
		if len(document.Resources) == 0 {
			t.Fatalf("%s plan has no typed resources", name)
		}
		for _, resource := range document.Resources {
			if planActionRank(resource.Action) >= planActionRank(planAction("invalid")) {
				t.Fatalf("%s resource %q uses non-canonical action %q", name, resource.Name, resource.Action)
			}
			for _, change := range resource.Changes {
				if change.Current == nil && change.Planned == nil {
					t.Fatalf("%s resource %q change %q has no typed values", name, resource.Name, change.Field)
				}
			}
		}
		if got := countsFromPlanResources(document.Resources); got != document.Counts {
			t.Fatalf("%s counts = %+v, resources produce %+v", name, document.Counts, got)
		}
	}
}

func TestPlanModelGoldenSingleApp(t *testing.T) {
	assertPlanModelGolden(t, "plan_model_single.golden.json", deploymentPlanDocument(singlePlanModelFixture()))
}

func TestRenderDeploymentPlanGoldenChanged(t *testing.T) {
	plan := singlePlanModelFixture()
	plan.Plan = deploymentPlanDocument(plan)
	var rendered bytes.Buffer
	renderDeploymentPlan(&rendered, plan)
	assertTextGolden(t, "plan_single_changed.golden", rendered.Bytes())
}

func TestPlanModelGoldenFleet(t *testing.T) {
	document := fleetPlanDocument(
		"shinyhub fleet plan", "fleets/eu.toml", &fleet.Manifest{FleetID: "eu"}, "https://hub.example.com",
		fleetPlanModelDiffFixture(),
		[]fleet.ProjectDiff{{
			Slug: "analytics", Action: fleet.ActionUpdateConfig,
			Drift: []fleet.ConfigDriftItem{{Key: "name", Server: "Analytics", Desired: "Revenue analytics"}},
		}},
	)
	assertPlanModelGolden(t, "plan_model_fleet.golden.json", document)
}

func TestPlanValueSecretClassificationIsSerialized(t *testing.T) {
	secret := planValue{Kind: planValueString, Display: "(sensitive)", Sensitive: true}
	raw, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"sensitive":true`)) {
		t.Fatalf("secret classification missing from JSON: %s", raw)
	}
}

func singlePlanModelFixture() deploymentPlan {
	changed := true
	return deploymentPlan{
		Status: "planned", Action: "update", ChangeStatus: "changed", Changes: &changed,
		Host: "https://hub.example.com", AppURL: "https://hub.example.com/app/sales-dashboard/", Slug: "sales-dashboard",
		Source: "/work/sales-dashboard", Permission: "manage existing app", Visibility: "private",
		Lifecycle: "deploy new version and replace running replicas",
		Remote: deploymentRemotePreview{
			Exists: true, Status: "running", Access: "private", DeployCount: 4,
			ContentDigest: "sha256:9f3b2e1a", LastDeploymentStatus: "succeeded",
		},
		Bundle: &bundlePreview{
			Digest: "sha256:7a1d9c0f", FileCount: 2, UncompressedBytes: 2048, CompressedBytes: 1024,
			Files: []string{"app.py", "requirements.txt"},
		},
		Launch: deploymentLaunchPreview{
			Runtime: "python", Command: []string{"uv", "run", "shiny", "run", "app.py", "--host", "{host}", "--port", "{port}"},
			CommandScope:  "bundle-resolved base command; server runtime and tracing policy may wrap it",
			ReadinessPath: "/", ReadinessStatus: "any 2xx or 3xx", StartupTimeoutSeconds: 120,
		},
		Manifest:      deploymentManifestPreview{Present: false, Effects: []string{}},
		Warnings:      []string{"two running replicas will be replaced"},
		DeployCommand: "shinyhub deploy /work/sales-dashboard --slug sales-dashboard --wait",
	}
}

func fleetPlanModelDiffFixture() []fleet.AppDiff {
	return []fleet.AppDiff{
		{Slug: "sales-dashboard", Action: fleet.ActionUpdateSourceConfig, ServerDigest: "sha256:9f3b2e1a", LocalDigest: "sha256:7a1d9c0f", ConfigDrift: []fleet.ConfigDriftItem{{Key: "min_replicas", Server: "1", Desired: "2"}}},
		{Slug: "revenue", Action: fleet.ActionAdopt, AdoptRequired: true, AdoptFrom: "fleet:legacy"},
		{Slug: "retired", Action: fleet.ActionDelete, Owned: true, PruneEligible: true},
		{Slug: "stable", Action: fleet.ActionUnchanged, Owned: true},
	}
}

func assertPlanModelGolden(t *testing.T, name string, document planDocument) {
	t.Helper()
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("golden mismatch for %s:\n--- got ---\n%s--- want ---\n%s", name, raw, want)
	}
}

func assertTextGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch for %s:\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}
