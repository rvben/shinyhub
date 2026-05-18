package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/spf13/cobra"
)

// fleetPlanSchemaVersion is the stable --json envelope version (spec §10).
const fleetPlanSchemaVersion = 1

func glyphWord(a fleet.Action) (string, string) {
	switch a {
	case fleet.ActionCreate:
		return "+", "create"
	case fleet.ActionUpdateSource, fleet.ActionUpdateConfig, fleet.ActionUpdateSourceConfig:
		return "~", "update"
	case fleet.ActionAdopt:
		return ">", "adopt"
	case fleet.ActionUnchanged:
		return "=", "ok"
	case fleet.ActionDelete:
		return "-", "delete"
	}
	return "?", string(a)
}

func reasonText(d fleet.AppDiff) string {
	switch d.Action {
	case fleet.ActionCreate:
		return "new"
	case fleet.ActionAdopt:
		return "present, not owned by this fleet (needs --adopt)"
	case fleet.ActionUnchanged:
		return "unchanged"
	case fleet.ActionDelete:
		return "fleet-managed, absent from manifest (prune candidate)"
	}
	var parts []string
	if d.Action == fleet.ActionUpdateSource || d.Action == fleet.ActionUpdateSourceConfig {
		sv := d.ServerDigest
		if sv == "" {
			sv = "(none)"
		}
		parts = append(parts, fmt.Sprintf("source %s -> %s", shortDigest(sv), shortDigest(d.LocalDigest)))
	}
	if d.Action == fleet.ActionUpdateConfig || d.Action == fleet.ActionUpdateSourceConfig {
		for _, c := range d.ConfigDrift {
			parts = append(parts, fmt.Sprintf("%s %s -> %s", c.Key, c.Server, c.Desired))
		}
	}
	return strings.Join(parts, ", ")
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

type planCounts struct {
	Create, Update, Adopt, Delete, Unchanged int
}

func countDiff(diff []fleet.AppDiff) planCounts {
	var c planCounts
	for _, d := range diff {
		switch d.Action {
		case fleet.ActionCreate:
			c.Create++
		case fleet.ActionUpdateSource, fleet.ActionUpdateConfig, fleet.ActionUpdateSourceConfig:
			c.Update++
		case fleet.ActionAdopt:
			c.Adopt++
		case fleet.ActionDelete:
			c.Delete++
		case fleet.ActionUnchanged:
			c.Unchanged++
		}
	}
	return c
}

// pending reports whether the diff has any non-unchanged action (drives
// --detailed-exitcode: spec §10 code 2).
func pending(diff []fleet.AppDiff) bool {
	for _, d := range diff {
		if d.Action != fleet.ActionUnchanged {
			return true
		}
	}
	return false
}

func renderFleetPlan(cmd *cobra.Command, f *fleetPlanFlags, m *fleet.Manifest, host string, caps serverCaps, diff []fleet.AppDiff) error {
	out := cmd.OutOrStdout()
	_ = caps // recorded for Plan 3; plan is read-only

	if f.jsonOutput {
		if err := writeFleetPlanJSON(out, m, host, diff); err != nil {
			return &ExitCodeError{Code: 1, Err: err}
		}
		return planExit(f, diff)
	}

	c := countDiff(diff)
	summary := fmt.Sprintf(
		"Plan: %d to create, %d to update, %d to adopt, %d to delete, %d unchanged.",
		c.Create, c.Update, c.Adopt, c.Delete, c.Unchanged)

	if f.quiet {
		fmt.Fprintln(out, summary)
		return planExit(f, diff)
	}

	fmt.Fprintf(out, "shinyhub fleet plan  ·  fleet_id=%s  ·  server=%s\n\n", m.FleetID, host)
	fmt.Fprintf(out, "Apps (%d)\n", len(diff))

	// Aligned columns: glyph word slug reason.
	wWord, wSlug := 0, 0
	for _, d := range diff {
		_, word := glyphWord(d.Action)
		if len(word) > wWord {
			wWord = len(word)
		}
		if len(d.Slug) > wSlug {
			wSlug = len(d.Slug)
		}
	}
	for _, d := range diff {
		g, word := glyphWord(d.Action)
		fmt.Fprintf(out, "  %s  %-*s  %-*s  %s\n", g, wWord, word, wSlug, d.Slug, reasonText(d))
	}
	fmt.Fprintf(out, "\n%s\n", summary)

	// Actionable Next block (spec §9.3): exact command per pending category.
	var next []string
	if c.Adopt > 0 {
		next = append(next, fmt.Sprintf("  • adopt %d app(s)            shinyhub fleet apply --adopt", c.Adopt))
	}
	if c.Delete > 0 {
		next = append(next, fmt.Sprintf("  • delete %d app(s)           shinyhub fleet apply --prune        (irreversible: removes data dir)", c.Delete))
	}
	if c.Create+c.Update > 0 {
		next = append(next, "  • apply everything else    shinyhub fleet apply")
	}
	if len(next) > 0 {
		fmt.Fprintf(out, "\nNext:\n%s\n", strings.Join(next, "\n"))
	}
	return planExit(f, diff)
}

// planExit maps the diff to the process exit code (spec §10). Default plan
// exit is 0 (report). With --detailed-exitcode: 0 none / 2 pending.
func planExit(f *fleetPlanFlags, diff []fleet.AppDiff) error {
	if f.detailedExitcode && pending(diff) {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("changes are pending")}
	}
	return nil
}

// JSON envelope (spec §10).

type jsonDriftItem struct {
	Key     string `json:"key"`
	Server  string `json:"server"`
	Desired string `json:"desired"`
}

type jsonDigest struct {
	Local  string `json:"local"`
	Server string `json:"server"`
}

type jsonApp struct {
	Slug          string          `json:"slug"`
	Action        string          `json:"action"`
	Owned         bool            `json:"owned"`
	Digest        jsonDigest      `json:"digest"`
	ConfigDrift   []jsonDriftItem `json:"config_drift"`
	AdoptRequired bool            `json:"adopt_required"`
	PruneEligible bool            `json:"prune_eligible"`
}

type jsonSummary struct {
	Counts     map[string]int `json:"counts"`
	ExitCode   int            `json:"exit_code"`
	ExitReason string         `json:"exit_reason"`
}

type jsonEnvelope struct {
	SchemaVersion int         `json:"schema_version"`
	FleetID       string      `json:"fleet_id"`
	Server        string      `json:"server"`
	GeneratedAt   string      `json:"generated_at"`
	Apps          []jsonApp   `json:"apps"`
	Summary       jsonSummary `json:"summary"`
}

func writeFleetPlanJSON(out interface{ Write([]byte) (int, error) }, m *fleet.Manifest, host string, diff []fleet.AppDiff) error {
	apps := make([]jsonApp, 0, len(diff))
	// Stable ordering for machine consumers: by slug.
	sorted := append([]fleet.AppDiff(nil), diff...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Slug < sorted[j].Slug })
	for _, d := range sorted {
		drift := make([]jsonDriftItem, 0, len(d.ConfigDrift))
		for _, c := range d.ConfigDrift {
			drift = append(drift, jsonDriftItem{Key: c.Key, Server: c.Server, Desired: c.Desired})
		}
		apps = append(apps, jsonApp{
			Slug: d.Slug, Action: string(d.Action), Owned: d.Owned,
			Digest:        jsonDigest{Local: d.LocalDigest, Server: d.ServerDigest},
			ConfigDrift:   drift,
			AdoptRequired: d.AdoptRequired, PruneEligible: d.PruneEligible,
		})
	}
	c := countDiff(diff)
	exitReason := "report only"
	env := jsonEnvelope{
		SchemaVersion: fleetPlanSchemaVersion,
		FleetID:       m.FleetID,
		Server:        host,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Apps:          apps,
		Summary: jsonSummary{
			Counts: map[string]int{
				"create": c.Create, "update": c.Update, "adopt": c.Adopt,
				"delete": c.Delete, "unchanged": c.Unchanged,
			},
			ExitCode:   0,
			ExitReason: exitReason,
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal plan json: %w", err)
	}
	_, err = out.Write(append(b, '\n'))
	return err
}
