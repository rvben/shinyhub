package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/spf13/cobra"
)

// fleetPlanSchemaVersion is the stable --json envelope version.
const fleetPlanSchemaVersion = 2

// planLegend is the one-line glyph key printed under the plan's app list so the
// column glyphs are self-describing.
const planLegend = "+ create  ~ update  > adopt  - delete  = unchanged"

func glyphWord(a fleet.Action) (string, string) {
	return planActionGlyphWord(canonicalFleetAction(a))
}

func planActionGlyphWord(a planAction) (string, string) {
	switch a {
	case planActionCreate:
		return "+", "create"
	case planActionUpdate:
		return "~", "update"
	case planActionAdopt:
		return ">", "adopt"
	case planActionUnchanged:
		return "=", "ok"
	case planActionDelete:
		return "-", "delete"
	}
	return "?", string(a)
}

// foreignAdoptWarning returns a multi-line warning naming every app that
// --adopt would TRANSFER away from another fleet, or "" when adopt is off or
// no adopt target is foreign-owned. Adopting an unmanaged app is silent (it
// has no prior owner); transferring one another fleet believes it owns is the
// surprising, destructive-to-the-other-fleet case worth flagging.
func foreignAdoptWarning(diff []fleet.AppDiff, adopt bool) string {
	if !adopt {
		return ""
	}
	var lines []string
	for _, d := range diff {
		if d.Action == fleet.ActionAdopt && d.AdoptFrom != "" {
			lines = append(lines, fmt.Sprintf("    %s (currently %s)", d.Slug, d.AdoptFrom))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("warning: --adopt will TRANSFER %s from another fleet to this one:\n%s",
		plural(len(lines), "app"), strings.Join(lines, "\n"))
}

func reasonText(d fleet.AppDiff) string {
	return planResourceReason(fleetAppPlanResource(d, ""))
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 8 {
		return d[:8]
	}
	if d == "" {
		return "(none)"
	}
	return d
}

// pending reports whether the diff has any non-unchanged action (drives
// --detailed-exitcode exit code 2).
func pending(diff []fleet.AppDiff, projects []fleet.ProjectDiff) bool {
	for _, d := range diff {
		if d.Action != fleet.ActionUnchanged {
			return true
		}
	}
	for _, p := range projects {
		if p.Action != fleet.ActionUnchanged {
			return true
		}
	}
	return false
}

// shellQuote returns s safe to paste as a single POSIX-shell argv word. A
// string built only from unreserved characters (alphanumerics and the path
// punctuation a manifest path normally uses) is returned bare; anything else is
// wrapped in single quotes, with every embedded single quote replaced by the
// four-character sequence
//
//	'\''
//
// so a path with spaces or shell metacharacters survives a copy-paste intact.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("-_./=:", r):
		default:
			safe = false
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// plural renders "1 app" / "3 apps" - a small singular/plural helper for the
// human-readable Next block.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// applySuggestion builds the SINGLE combined `fleet apply` command that
// converges every pending action, plus a human description of what it does.
// One command, not a sequence: --adopt already applies create/update on the
// same run, and --prune folds in deletes, so emitting separate per-category
// applies would mislead an operator into running apply several times. -f is
// echoed only for a non-default manifest so a copy-paste targets the same file.
func applySuggestion(file string, c planCounts) (cmd, desc string) {
	parts := []string{"shinyhub fleet apply"}
	var actions []string
	if c.Adopt > 0 {
		parts = append(parts, "--adopt")
		actions = append(actions, "adopt "+plural(c.Adopt, "app"))
	}
	if c.Create > 0 || c.Update > 0 {
		var cu []string
		if c.Create > 0 {
			cu = append(cu, fmt.Sprintf("%d create", c.Create))
		}
		if c.Update > 0 {
			cu = append(cu, fmt.Sprintf("%d update", c.Update))
		}
		actions = append(actions, "apply "+strings.Join(cu, " + "))
	}
	if c.Delete > 0 {
		parts = append(parts, "--prune")
		actions = append(actions, "delete "+plural(c.Delete, "app")+" (irreversible; confirmation required)")
	}
	if file != "" && file != defaultFleetManifest {
		parts = append(parts, "-f", shellQuote(file))
	}
	return strings.Join(parts, " "), strings.Join(actions, ", ")
}

func renderFleetPlan(cmd *cobra.Command, f *fleetPlanFlags, cmdLabel string, m *fleet.Manifest, host string, caps serverCaps, diff []fleet.AppDiff, projects []fleet.ProjectDiff) error {
	out := cmd.OutOrStdout()
	_ = caps // threaded for fleet apply; the plan command is read-only and does not consume it

	if f.jsonOutput {
		code, reason := planExitInfo(f, diff, projects)
		if err := writeFleetPlanJSONWithFile(out, m, host, f.file, diff, projects, code, reason); err != nil {
			return &ExitCodeError{Code: 1, Err: err}
		}
		return planExit(f, diff, projects)
	}

	model := fleetPlanDocument(cmdLabel, f.file, m, host, diff, projects)
	c := model.Counts
	summary := fmt.Sprintf(
		"Plan: %d to create, %d to update, %d to adopt, %d to delete, %d unchanged.",
		c.Create, c.Update, c.Adopt, c.Delete, c.Unchanged)

	if quietFlag {
		fmt.Fprintln(out, summary)
		return planExit(f, diff, projects)
	}

	fmt.Fprintf(out, "%s\n%s  ·  fleet_id=%s  ·  server=%s\n\n", model.Outcome, cmdLabel, m.FleetID, host)

	// Omitted entirely when the manifest declares no projects, so existing
	// output stays byte-identical for manifests that do not use the feature.
	projectResources := planResourcesByKind(model, "project")
	if len(projectResources) > 0 {
		fmt.Fprintf(out, "Projects (%d)\n", len(projectResources))
		wSlug := 0
		for _, resource := range projectResources {
			if len(resource.Name) > wSlug {
				wSlug = len(resource.Name)
			}
		}
		for _, resource := range projectResources {
			g, word := planActionGlyphWord(resource.Action)
			keys := make([]string, 0, len(resource.Changes))
			for _, change := range resource.Changes {
				keys = append(keys, change.Field)
			}
			reason := ""
			if len(keys) > 0 {
				reason = strings.Join(keys, ", ")
			}
			fmt.Fprintf(out, "  %s  %-*s  %-*s  %s\n", g, 9, word, wSlug, resource.Name, reason)
		}
		fmt.Fprintln(out)
	}

	appResources := planResourcesByKind(model, "app")
	fmt.Fprintf(out, "Apps (%d)   legend: %s\n", len(appResources), planLegend)

	// Aligned columns: glyph word slug reason.
	wWord, wSlug := 0, 0
	for _, resource := range appResources {
		_, word := planActionGlyphWord(resource.Action)
		if len(word) > wWord {
			wWord = len(word)
		}
		if len(resource.Name) > wSlug {
			wSlug = len(resource.Name)
		}
	}
	for _, resource := range appResources {
		g, word := planActionGlyphWord(resource.Action)
		fmt.Fprintf(out, "  %s  %-*s  %-*s  %s\n", g, wWord, word, wSlug, resource.Name, planResourceReason(resource))
	}
	fmt.Fprintf(out, "\n%s\n", summary)

	// Actionable Next block: ONE combined apply command covering every pending
	// action, with the human description of what it will do.
	if c.Create+c.Update+c.Adopt+c.Delete > 0 {
		next := model.NextActions[0]
		fmt.Fprintf(out, "\nNext:\n  %s\n      (%s)\n", next.Command, next.Description)
	}
	return planExit(f, diff, projects)
}

// planExitInfo computes the process exit code and a human reason for a plan
// run. Default plan exit is 0 ("report only"). With --detailed-exitcode it is
// 2 ("changes are pending") when the diff has pending actions, else 0 ("no
// changes"). The JSON summary and planExit both derive from this so the
// reported exit_code always matches the process exit code.
func planExitInfo(f *fleetPlanFlags, diff []fleet.AppDiff, projects []fleet.ProjectDiff) (int, string) {
	if f.detailedExitcode {
		if pending(diff, projects) {
			return 2, "changes are pending"
		}
		return 0, "no changes"
	}
	return 0, "report only"
}

// planExit maps the diff to the process exit code.
func planExit(f *fleetPlanFlags, diff []fleet.AppDiff, projects []fleet.ProjectDiff) error {
	code, reason := planExitInfo(f, diff, projects)
	if code != 0 {
		// Detailed-exitcode is a status signal (the plan was already printed),
		// not an error to surface; flag Reported so the wrapper stays silent.
		return &ExitCodeError{Code: code, Err: errors.New(reason), Reported: true}
	}
	return nil
}

// JSON envelope types for the --json output.

type jsonDriftItem struct {
	Key     string `json:"key"`
	Server  string `json:"server"`
	Desired string `json:"desired"`
}

type jsonUnmanagedItem struct {
	Key     string `json:"key"`
	Server  string `json:"server"`
	Default string `json:"default"`
}

type jsonDigest struct {
	Local  string `json:"local"`
	Server string `json:"server"`
}

type jsonApp struct {
	Slug          string              `json:"slug"`
	Action        string              `json:"action"`
	Owned         bool                `json:"owned"`
	Digest        jsonDigest          `json:"digest"`
	ConfigDrift   []jsonDriftItem     `json:"config_drift"`
	Unmanaged     []jsonUnmanagedItem `json:"unmanaged"`
	AdoptRequired bool                `json:"adopt_required"`
	AdoptFrom     string              `json:"adopt_from,omitempty"`
	PruneEligible bool                `json:"prune_eligible"`
}

type jsonProject struct {
	Slug   string          `json:"slug"`
	Action string          `json:"action"`
	Drift  []jsonDriftItem `json:"drift"`
}

type jsonSummary struct {
	Counts     map[string]int `json:"counts"`
	ExitCode   int            `json:"exit_code"`
	ExitReason string         `json:"exit_reason"`
}

type jsonEnvelope struct {
	SchemaVersion int           `json:"schema_version"`
	FleetID       string        `json:"fleet_id"`
	Server        string        `json:"server"`
	GeneratedAt   string        `json:"generated_at"`
	Projects      []jsonProject `json:"projects"`
	Apps          []jsonApp     `json:"apps"`
	Summary       jsonSummary   `json:"summary"`
	Plan          planDocument  `json:"plan"`
}

// jsonProjectsFromDiff maps the project diff to its JSON shape, sorted by slug
// for the same stable-ordering reason the apps array is. Shared by the plan
// and apply JSON envelopes so both sort and shape the projects array the same
// way.
func jsonProjectsFromDiff(projects []fleet.ProjectDiff) []jsonProject {
	out := make([]jsonProject, 0, len(projects))
	sorted := append([]fleet.ProjectDiff(nil), projects...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Slug < sorted[j].Slug })
	for _, p := range sorted {
		drift := make([]jsonDriftItem, 0, len(p.Drift))
		for _, c := range p.Drift {
			drift = append(drift, jsonDriftItem{Key: c.Key, Server: c.Server, Desired: c.Desired})
		}
		out = append(out, jsonProject{Slug: p.Slug, Action: string(p.Action), Drift: drift})
	}
	return out
}

func writeFleetPlanJSON(out interface{ Write([]byte) (int, error) }, m *fleet.Manifest, host string, diff []fleet.AppDiff, projects []fleet.ProjectDiff, exitCode int, exitReason string) error {
	return writeFleetPlanJSONWithFile(out, m, host, defaultFleetManifest, diff, projects, exitCode, exitReason)
}

func writeFleetPlanJSONWithFile(out interface{ Write([]byte) (int, error) }, m *fleet.Manifest, host, file string, diff []fleet.AppDiff, projects []fleet.ProjectDiff, exitCode int, exitReason string) error {
	apps := make([]jsonApp, 0, len(diff))
	// Stable ordering for machine consumers: by slug.
	sorted := append([]fleet.AppDiff(nil), diff...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Slug < sorted[j].Slug })
	for _, d := range sorted {
		drift := make([]jsonDriftItem, 0, len(d.ConfigDrift))
		for _, c := range d.ConfigDrift {
			drift = append(drift, jsonDriftItem{Key: c.Key, Server: c.Server, Desired: c.Desired})
		}
		unmanaged := make([]jsonUnmanagedItem, 0, len(d.Unmanaged))
		for _, u := range d.Unmanaged {
			unmanaged = append(unmanaged, jsonUnmanagedItem{Key: u.Key, Server: u.Server, Default: u.Default})
		}
		apps = append(apps, jsonApp{
			Slug: d.Slug, Action: string(d.Action), Owned: d.Owned,
			Digest:        jsonDigest{Local: d.LocalDigest, Server: d.ServerDigest},
			ConfigDrift:   drift,
			Unmanaged:     unmanaged,
			AdoptRequired: d.AdoptRequired, AdoptFrom: d.AdoptFrom, PruneEligible: d.PruneEligible,
		})
	}
	model := fleetPlanDocument("shinyhub fleet plan", file, m, host, diff, projects)
	c := model.Counts
	env := jsonEnvelope{
		SchemaVersion: fleetPlanSchemaVersion,
		FleetID:       m.FleetID,
		Server:        host,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Projects:      jsonProjectsFromDiff(projects),
		Apps:          apps,
		Summary: jsonSummary{
			Counts: map[string]int{
				"create": c.Create, "update": c.Update, "adopt": c.Adopt,
				"delete": c.Delete, "unchanged": c.Unchanged,
			},
			ExitCode:   exitCode,
			ExitReason: exitReason,
		},
		Plan: model,
	}
	b, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal plan json: %w", err)
	}
	_, err = out.Write(append(b, '\n'))
	return err
}
