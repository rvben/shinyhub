package cli

import (
	"bytes"
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
	_ = renderFleetPlan(cmd, f, label, m, "http://srv", serverCaps{}, diff)
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

// FLT-4: the "Next:" block must offer ONE combined apply command, not a
// sequence of separate applies (--adopt already applies create/update too).
func TestApplySuggestion_CombinesAllPendingFlags(t *testing.T) {
	c := planCounts{Create: 1, Update: 2, Adopt: 1, Delete: 1}
	cmd, desc := applySuggestion(defaultFleetManifest, c)
	if cmd != "shinyhub fleet apply --adopt --prune --yes" {
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
		"shinyhub-fleet.toml": "shinyhub-fleet.toml",   // bare-safe, unquoted
		"envs/eu/fleet.toml":  "envs/eu/fleet.toml",     // slashes are safe
		"my fleet.toml":       "'my fleet.toml'",        // space -> quote
		"a'b.toml":            `'a'\''b.toml'`,           // embedded quote escaped
		"x;rm -rf.toml":       "'x;rm -rf.toml'",         // metacharacters -> quote
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
	if !strings.Contains(next, "--adopt") || !strings.Contains(next, "--prune --yes") {
		t.Fatalf("combined command missing required flags:\n%s", next)
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
