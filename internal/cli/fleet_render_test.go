package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/spf13/cobra"
)

// renderPlanToString runs renderFleetPlan against an in-memory command and
// returns stdout. Errors from planExit (e.g. detailed-exitcode) are ignored;
// the tests here assert on rendered text only.
func renderPlanToString(t *testing.T, f *fleetPlanFlags, label string, m *fleet.Manifest, diff []fleet.AppDiff) string {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	_ = renderFleetPlan(cmd, f, label, m, "http://srv", serverCaps{}, diff, nil)
	return buf.String()
}

func fullPlanDiff() []fleet.AppDiff {
	return []fleet.AppDiff{
		{Slug: "newone", Action: fleet.ActionCreate},
		{Slug: "changed", Action: fleet.ActionUpdateSource, ServerDigest: "sha256:old", LocalDigest: "sha256:new"},
		{Slug: "takeover", Action: fleet.ActionAdopt},
		{Slug: "retired", Action: fleet.ActionDelete},
		{Slug: "stable", Action: fleet.ActionUnchanged},
	}
}

// A manifest with no [[project]] blocks must render byte-identical to the
// pre-project-support plan: no empty "Projects (0)" header, no stray section.
func TestRenderFleetPlan_NoProjectsOmitsSection(t *testing.T) {
	f := &fleetPlanFlags{file: defaultFleetManifest}
	out := renderPlanToString(t, f, "shinyhub fleet plan", &fleet.Manifest{FleetID: "eu"}, fullPlanDiff())
	if strings.Contains(out, "Projects") {
		t.Fatalf("a plan with no declared projects must omit the Projects section entirely:\n%s", out)
	}
}

func TestFleetPlan_ReportsSharedBundleFanoutWithoutCausality(t *testing.T) {
	m := &fleet.Manifest{
		FleetID: "analytics",
		BundleFiles: []fleet.BundleFileEntry{{
			From: "_shared/theme.py", To: "helpers/theme.py", Consumers: []string{"sales", "operations"},
		}},
	}
	diff := []fleet.AppDiff{
		{Slug: "sales", Action: fleet.ActionUpdateSource},
		{Slug: "operations", Action: fleet.ActionUpdateConfig},
	}
	out := renderPlanToString(t, &fleetPlanFlags{file: defaultFleetManifest}, "shinyhub fleet plan", m, diff)
	for _, want := range []string{"Shared bundle inputs", "_shared/theme.py", "helpers/theme.py", "consumers: 2", "1 has a planned source update"} {
		if !strings.Contains(out, want) {
			t.Fatalf("human fan-out missing %q\n%s", want, out)
		}
	}
	if strings.Contains(strings.ToLower(out), "caused") {
		t.Fatalf("plan must not claim unavailable file-level causality\n%s", out)
	}

	var buf bytes.Buffer
	if err := writeFleetPlanJSON(&buf, m, "http://s", diff, nil, 0, "report only"); err != nil {
		t.Fatal(err)
	}
	var env struct {
		SchemaVersion int `json:"schema_version"`
		BundleFiles   []struct {
			From             string   `json:"from"`
			Consumers        []string `json:"consumers"`
			PlannedConsumers []string `json:"planned_consumers"`
		} `json:"bundle_files"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.SchemaVersion != 3 || len(env.BundleFiles) != 1 || len(env.BundleFiles[0].Consumers) != 2 ||
		len(env.BundleFiles[0].PlannedConsumers) != 1 || env.BundleFiles[0].PlannedConsumers[0] != "sales" {
		t.Fatalf("bundle fan-out JSON = %+v", env)
	}
}

// Declared project drift must print in its own "Projects (%d)" section, ahead
// of the Apps table, so a project rename is visible without reaching for
// -o json.
func TestRenderFleetPlan_ProjectsSectionPrintedAboveApps(t *testing.T) {
	f := &fleetPlanFlags{file: defaultFleetManifest}
	projects := []fleet.ProjectDiff{
		{Slug: "sales", Action: fleet.ActionUpdateConfig, Drift: []fleet.ConfigDriftItem{{Key: "name"}}},
	}
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	_ = renderFleetPlan(cmd, f, "shinyhub fleet plan", &fleet.Manifest{FleetID: "eu"}, "http://srv", serverCaps{}, nil, projects)
	out := buf.String()
	if !strings.Contains(out, "Projects (1)") {
		t.Fatalf("plan output missing the Projects section:\n%s", out)
	}
	if !strings.Contains(out, "sales") || !strings.Contains(out, "name") {
		t.Fatalf("project row must show its slug and drifted key:\n%s", out)
	}
	if i, j := strings.Index(out, "Projects (1)"), strings.Index(out, "Apps ("); i < 0 || j < 0 || i > j {
		t.Fatalf("Projects section must print before the Apps table:\n%s", out)
	}
}

// The plan JSON envelope must carry a projects array alongside apps, so a CI
// consumer can detect a pending project rename the same way it detects an app
// change, without falling back to the human-readable text.
func TestWriteFleetPlanJSON_IncludesProjectsArray(t *testing.T) {
	projects := []fleet.ProjectDiff{
		{Slug: "sales", Action: fleet.ActionCreate, Drift: []fleet.ConfigDriftItem{{Key: "name", Desired: `"Sales"`}}},
	}
	var buf bytes.Buffer
	if err := writeFleetPlanJSON(&buf, &fleet.Manifest{FleetID: "eu"}, "http://s", nil, projects, 0, "report only"); err != nil {
		t.Fatalf("writeFleetPlanJSON: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	projs, ok := env["projects"].([]any)
	if !ok || len(projs) != 1 {
		t.Fatalf("projects array missing or wrong length: %v", env["projects"])
	}
	p0 := projs[0].(map[string]any)
	if p0["slug"] != "sales" || p0["action"] != "create" {
		t.Errorf("project entry = %v, want slug=sales action=create", p0)
	}
	plan, ok := env["plan"].(map[string]any)
	if !ok || plan["schema_version"] != float64(planModelSchemaVersion) || plan["scope"] != "fleet" {
		t.Fatalf("shared plan model missing from fleet JSON: %v", env["plan"])
	}
}

// FLT-5: the plan reason for an adopt must distinguish a genuinely unmanaged
// app from one currently owned by a DIFFERENT fleet (an ownership transfer).
func TestReasonText_AdoptDistinguishesForeignFleet(t *testing.T) {
	unmanaged := reasonText(fleet.AppDiff{Action: fleet.ActionAdopt, AdoptRequired: true})
	if !strings.Contains(unmanaged, "unmanaged") {
		t.Fatalf("unmanaged adopt reason = %q, want it to say 'unmanaged'", unmanaged)
	}
	if strings.Contains(strings.ToLower(unmanaged), "transfer") {
		t.Fatalf("unmanaged adopt reason = %q, must not threaten an ownership transfer", unmanaged)
	}
	foreign := reasonText(fleet.AppDiff{Action: fleet.ActionAdopt, AdoptRequired: true, AdoptFrom: "fleet:us"})
	if !strings.Contains(foreign, "fleet:us") {
		t.Fatalf("foreign adopt reason = %q, want it to name the current owner fleet:us", foreign)
	}
	if !strings.Contains(strings.ToLower(foreign), "transfer") {
		t.Fatalf("foreign adopt reason = %q, want it to warn of an ownership transfer", foreign)
	}
}

// FLT-5: `fleet apply --adopt` must warn before transferring apps owned by
// another fleet, listing each app and its current owner. Without --adopt, or
// with no foreign-owned adopt targets, the warning is empty.
func TestForeignAdoptWarning(t *testing.T) {
	diff := []fleet.AppDiff{
		{Slug: "a", Action: fleet.ActionAdopt, AdoptFrom: "fleet:us"},
		{Slug: "b", Action: fleet.ActionAdopt}, // unmanaged, not a transfer
		{Slug: "c", Action: fleet.ActionUpdateSource},
	}
	if w := foreignAdoptWarning(diff, false); w != "" {
		t.Fatalf("without --adopt the warning must be empty, got %q", w)
	}
	w := foreignAdoptWarning(diff, true)
	if !strings.Contains(w, "a") || !strings.Contains(w, "fleet:us") {
		t.Fatalf("warning must name the app and its current owner, got %q", w)
	}
	if strings.Contains(w, "\"b\"") || strings.Contains(w, " b ") {
		t.Fatalf("unmanaged app b must not be listed as a transfer, got %q", w)
	}
	if none := foreignAdoptWarning([]fleet.AppDiff{{Slug: "x", Action: fleet.ActionAdopt}}, true); none != "" {
		t.Fatalf("no foreign-owned targets => empty warning, got %q", none)
	}
}

// FLT-5: the structured plan/apply envelopes must expose adopt_from so JSON
// automation can gate on an ownership transfer, not just the human output.
func TestPlanJSON_ExposesAdoptFrom(t *testing.T) {
	diff := []fleet.AppDiff{
		{Slug: "a", Action: fleet.ActionAdopt, AdoptRequired: true, AdoptFrom: "fleet:us"},
		{Slug: "b", Action: fleet.ActionAdopt, AdoptRequired: true}, // unmanaged
	}
	var buf bytes.Buffer
	if err := writeFleetPlanJSON(&buf, &fleet.Manifest{FleetID: "eu"}, "http://s", diff, nil, 0, "report only"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"adopt_from":"fleet:us"`) {
		t.Fatalf("plan JSON must carry adopt_from for the transferred app:\n%s", buf.String())
	}
	// Unmanaged adopt has no prior owner; the field is omitted, never "".
	if strings.Contains(buf.String(), `"adopt_from":""`) {
		t.Fatalf("unmanaged adopt must omit adopt_from, not emit empty:\n%s", buf.String())
	}
}

func TestPlanOutput_ExposesUnmanagedConfigWithoutChangingAction(t *testing.T) {
	diff := []fleet.AppDiff{{
		Slug: "dash", Action: fleet.ActionUnchanged,
		Unmanaged: []fleet.UnmanagedConfigItem{{
			Key: "hibernate_timeout_minutes", Server: "0", Default: "(default)",
		}},
	}}
	out := renderPlanToString(t, &fleetPlanFlags{file: defaultFleetManifest}, "shinyhub fleet plan", &fleet.Manifest{FleetID: "eu"}, diff)
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "unchanged; unmanaged: hibernate_timeout_minutes=0 (default (default))") {
		t.Fatalf("human plan missing unmanaged signal:\n%s", out)
	}
	if !strings.Contains(out, "0 to update") || !strings.Contains(out, "1 unchanged") {
		t.Fatalf("unmanaged observation must not change action counts:\n%s", out)
	}

	var buf bytes.Buffer
	if err := writeFleetPlanJSON(&buf, &fleet.Manifest{FleetID: "eu"}, "http://s", diff, nil, 0, "report only"); err != nil {
		t.Fatal(err)
	}
	var env struct {
		SchemaVersion int `json:"schema_version"`
		Apps          []struct {
			Action    string              `json:"action"`
			Unmanaged []jsonUnmanagedItem `json:"unmanaged"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.SchemaVersion != 3 {
		t.Fatalf("schema_version = %d, want 3", env.SchemaVersion)
	}
	if len(env.Apps) != 1 || env.Apps[0].Action != "unchanged" || len(env.Apps[0].Unmanaged) != 1 {
		t.Fatalf("JSON unmanaged signal missing or changed action: %+v", env.Apps)
	}
	u := env.Apps[0].Unmanaged[0]
	if u.Key != "hibernate_timeout_minutes" || u.Server != "0" || u.Default != "(default)" {
		t.Fatalf("JSON unmanaged item = %+v", u)
	}
}

// FLT-4: the "Next:" block must offer ONE combined apply command, not a
// sequence of separate applies (--adopt already applies create/update too).
func TestApplySuggestion_CombinesAllPendingFlags(t *testing.T) {
	c := planCounts{Create: 1, Update: 2, Adopt: 1, Delete: 1}
	cmd, desc := applySuggestion(defaultFleetManifest, c)
	if cmd != "shinyhub fleet apply --adopt --prune" {
		t.Fatalf("combined command = %q", cmd)
	}
	for _, want := range []string{"adopt 1 app", "1 create", "2 update", "delete 1 app"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description %q missing %q", desc, want)
		}
	}
	if !strings.Contains(desc, "irreversible") {
		t.Fatalf("delete description must flag irreversibility: %q", desc)
	}
}

func TestApplySuggestion_OnlyCreateUpdateIsBareApply(t *testing.T) {
	cmd, _ := applySuggestion(defaultFleetManifest, planCounts{Create: 1, Update: 1})
	if cmd != "shinyhub fleet apply" {
		t.Fatalf("create/update only command = %q, want bare apply", cmd)
	}
}

// FLT-3: a non-default manifest path must be threaded into the suggested
// command so a copy-paste reconciles the SAME manifest.
func TestApplySuggestion_NonDefaultManifestIncludesFileFlag(t *testing.T) {
	cmd, _ := applySuggestion("envs/eu/fleet.toml", planCounts{Create: 1})
	if !strings.Contains(cmd, "-f envs/eu/fleet.toml") {
		t.Fatalf("non-default manifest not threaded into command: %q", cmd)
	}
}

func TestApplySuggestion_DefaultManifestOmitsFileFlag(t *testing.T) {
	cmd, _ := applySuggestion(defaultFleetManifest, planCounts{Create: 1})
	if strings.Contains(cmd, "-f") {
		t.Fatalf("default manifest must not add a -f flag: %q", cmd)
	}
}

// A manifest path with a space must be shell-quoted so the suggested command
// stays a single, copy-pastable argv word rather than splitting into two.
func TestApplySuggestion_ManifestPathWithSpaceIsShellQuoted(t *testing.T) {
	cmd, _ := applySuggestion("envs/my fleet.toml", planCounts{Create: 1})
	if !strings.Contains(cmd, "-f 'envs/my fleet.toml'") {
		t.Fatalf("manifest path with a space must be shell-quoted: %q", cmd)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"shinyhub-fleet.toml": "shinyhub-fleet.toml", // bare-safe, unquoted
		"envs/eu/fleet.toml":  "envs/eu/fleet.toml",  // slashes are safe
		"my fleet.toml":       "'my fleet.toml'",     // space -> quote
		"a'b.toml":            `'a'\''b.toml'`,       // embedded quote escaped
		"x;rm -rf.toml":       "'x;rm -rf.toml'",     // metacharacters -> quote
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// FLT-4 (rendered): the Next block must contain exactly one apply command.
func TestRenderFleetPlan_NextBlockIsSingleCombinedCommand(t *testing.T) {
	f := &fleetPlanFlags{file: defaultFleetManifest}
	out := renderPlanToString(t, f, "shinyhub fleet plan", &fleet.Manifest{FleetID: "eu"}, fullPlanDiff())
	next := out[strings.Index(out, "Next:"):]
	if n := strings.Count(next, "shinyhub fleet apply"); n != 1 {
		t.Fatalf("Next block must have exactly one apply command, found %d:\n%s", n, next)
	}
	if !strings.Contains(next, "--adopt") || !strings.Contains(next, "--prune") {
		t.Fatalf("combined command missing required flags:\n%s", next)
	}
	if strings.Contains(next, "--yes") {
		t.Fatalf("a suggested destructive command must not pre-confirm deletion:\n%s", next)
	}
}

// FLT-9: plan output carries an inline one-line glyph legend.
func TestRenderFleetPlan_HasGlyphLegend(t *testing.T) {
	f := &fleetPlanFlags{file: defaultFleetManifest}
	out := renderPlanToString(t, f, "shinyhub fleet plan", &fleet.Manifest{FleetID: "eu"}, fullPlanDiff())
	for _, want := range []string{"+ create", "~ update", "> adopt", "- delete", "= unchanged"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan legend missing %q:\n%s", want, out)
		}
	}
}

// FLT-9: status output carries its own inline legend (* / -).
func TestRenderFleetStatus_HasGlyphLegend(t *testing.T) {
	st := buildFleetStatus("http://srv", nil)
	var b strings.Builder
	renderFleetStatus(&b, st, false)
	out := b.String()
	for _, want := range []string{"* fleet-managed", "- unmanaged"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status legend missing %q:\n%s", want, out)
		}
	}
}

// FLT-10: renderFleetPlan stamps the originating command into the header so an
// apply --dry-run does not masquerade as "shinyhub fleet plan".
func TestRenderFleetPlan_HeaderUsesCommandLabel(t *testing.T) {
	f := &fleetPlanFlags{file: defaultFleetManifest}
	out := renderPlanToString(t, f, "shinyhub fleet apply --dry-run", &fleet.Manifest{FleetID: "eu"}, fullPlanDiff())
	if !strings.Contains(out, "shinyhub fleet apply --dry-run  ·  fleet_id=eu") {
		t.Fatalf("header did not use the supplied command label:\n%s", out)
	}
	if strings.Contains(out, "shinyhub fleet plan  ·") {
		t.Fatalf("apply --dry-run must not print the plan header:\n%s", out)
	}
}
