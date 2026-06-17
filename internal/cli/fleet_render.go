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
const fleetPlanSchemaVersion = 1

// planLegend is the one-line glyph key printed under the plan's app list so the
// column glyphs are self-describing.
const planLegend = "+ create  ~ update  > adopt  - delete  = unchanged"

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
	switch d.Action {
	case fleet.ActionCreate:
		return "new"
	case fleet.ActionAdopt:
		if d.AdoptFrom != "" {
			return fmt.Sprintf("owned by %s; --adopt will TRANSFER ownership to this fleet", d.AdoptFrom)
		}
		return "unmanaged, not owned by this fleet (needs --adopt)"
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
// --detailed-exitcode exit code 2).
func pending(diff []fleet.AppDiff) bool {
	for _, d := range diff {
		if d.Action != fleet.ActionUnchanged {
			return true
		}
	}
	return false
}

// shellQuote returns s safe to paste as a single POSIX-shell argv word. A
// string built only from unreserved characters (alphanumerics and the path
// punctuation a manifest path normally uses) is returned bare; anything else is
// wrapped in single quotes with embedded single quotes escaped as '\'' so a
// path with spaces or shell metacharacters survives a copy-paste intact.
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
		parts = append(parts, "--prune", "--yes")
		actions = append(actions, "delete "+plural(c.Delete, "app")+" (irreversible: removes data dir)")
	}
	if file != "" && file != defaultFleetManifest {
		parts = append(parts, "-f", shellQuote(file))
	}
	return strings.Join(parts, " "), strings.Join(actions, ", ")
}

func renderFleetPlan(cmd *cobra.Command, f *fleetPlanFlags, cmdLabel string, m *fleet.Manifest, host string, caps serverCaps, diff []fleet.AppDiff) error {
	out := cmd.OutOrStdout()
	_ = caps // threaded for fleet apply; the plan command is read-only and does not consume it

	if f.jsonOutput {
		code, reason := planExitInfo(f, diff)
		if err := writeFleetPlanJSON(out, m, host, diff, code, reason); err != nil {
			return &ExitCodeError{Code: 1, Err: err}
		}
		return planExit(f, diff)
	}

	c := countDiff(diff)
	summary := fmt.Sprintf(
		"Plan: %d to create, %d to update, %d to adopt, %d to delete, %d unchanged.",
		c.Create, c.Update, c.Adopt, c.Delete, c.Unchanged)

	if quietFlag {
		fmt.Fprintln(out, summary)
		return planExit(f, diff)
	}

	fmt.Fprintf(out, "%s  ·  fleet_id=%s  ·  server=%s\n\n", cmdLabel, m.FleetID, host)
	fmt.Fprintf(out, "Apps (%d)   legend: %s\n", len(diff), planLegend)

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

	// Actionable Next block: ONE combined apply command covering every pending
	// action, with the human description of what it will do.
	if c.Create+c.Update+c.Adopt+c.Delete > 0 {
		applyCmd, desc := applySuggestion(f.file, c)
		fmt.Fprintf(out, "\nNext:\n  %s\n      (%s)\n", applyCmd, desc)
	}
	return planExit(f, diff)
}

// planExitInfo computes the process exit code and a human reason for a plan
// run. Default plan exit is 0 ("report only"). With --detailed-exitcode it is
// 2 ("changes are pending") when the diff has pending actions, else 0 ("no
// changes"). The JSON summary and planExit both derive from this so the
// reported exit_code always matches the process exit code.
func planExitInfo(f *fleetPlanFlags, diff []fleet.AppDiff) (int, string) {
	if f.detailedExitcode {
		if pending(diff) {
			return 2, "changes are pending"
		}
		return 0, "no changes"
	}
	return 0, "report only"
}

// planExit maps the diff to the process exit code.
func planExit(f *fleetPlanFlags, diff []fleet.AppDiff) error {
	code, reason := planExitInfo(f, diff)
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
	AdoptFrom     string          `json:"adopt_from,omitempty"`
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

func writeFleetPlanJSON(out interface{ Write([]byte) (int, error) }, m *fleet.Manifest, host string, diff []fleet.AppDiff, exitCode int, exitReason string) error {
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
			AdoptRequired: d.AdoptRequired, AdoptFrom: d.AdoptFrom, PruneEligible: d.PruneEligible,
		})
	}
	c := countDiff(diff)
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
			ExitCode:   exitCode,
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
