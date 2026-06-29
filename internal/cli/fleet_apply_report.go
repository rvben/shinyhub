package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

// applyStatus is the terminal per-app outcome of a convergence run.
type applyStatus string

const (
	statusCreated   applyStatus = "created"
	statusUpdated   applyStatus = "updated"
	statusDeleted   applyStatus = "deleted"
	statusUnchanged applyStatus = "unchanged"
	statusAdopted   applyStatus = "adopted"
	statusSkipped   applyStatus = "skipped"  // adopt w/o --adopt, prune w/o --prune, degraded prune
	statusConflict  applyStatus = "conflict" // precondition 409
	statusFailed    applyStatus = "failed"   // error after all retries
)

// applyResult is one app's outcome. note carries a short human reason for
// skipped / self-healed states; err carries the failure/conflict cause.
// firstFires holds the per-schedule run_on_register outcomes for this deploy.
type applyResult struct {
	slug       string
	action     fleet.Action
	status     applyStatus
	attempts   int
	duration   time.Duration
	err        error
	note       string
	firstFires []firstFireOutcome
	// logTail holds the failing app's last process-log lines, populated only
	// when a deploy-bearing action fails (e.g. the app crashed on startup), so
	// the operator sees the cause without a second round-trip to the host.
	logTail []string
	// attemptsDetail holds one record per FAILED deploy attempt (empty when the
	// first attempt succeeded). It is populated even on a retried-then-succeeded
	// deploy so the operator can see why an earlier attempt failed.
	attemptsDetail []attemptOutcome
}

type applyTally struct {
	created, updated, deleted, unchanged, adopted, skipped, failed, conflicts int
}

func tallyResults(res []applyResult) applyTally {
	var t applyTally
	for _, r := range res {
		switch r.status {
		case statusCreated:
			t.created++
		case statusUpdated:
			t.updated++
		case statusDeleted:
			t.deleted++
		case statusUnchanged:
			t.unchanged++
		case statusAdopted:
			t.adopted++
		case statusSkipped:
			t.skipped++
		case statusFailed:
			t.failed++
		case statusConflict:
			t.conflicts++
		}
	}
	return t
}

// applyExitCode maps results to (code, reason-in-words). Conflicts (5)
// outrank failures (4) as the numeric code, but when both occur the reason
// enumerates both classes so the operator is never left guessing.
// Skipped prune/adopt candidates are not failures: they do not raise the code.
func applyExitCode(res []applyResult) (int, string) {
	t := tallyResults(res)
	switch {
	case t.failed > 0 && t.conflicts > 0:
		return 5, fmt.Sprintf("PARTIAL - %d failed after retries, %d conflict(s); re-run plan", t.failed, t.conflicts)
	case t.conflicts > 0:
		return 5, fmt.Sprintf("CONFLICTS - %d app(s) changed under us; re-run plan", t.conflicts)
	case t.failed > 0:
		return 4, fmt.Sprintf("PARTIAL - %d app(s) failed after retries", t.failed)
	default:
		return 0, "OK - all converged"
	}
}

// applyExitErr is returned after the apply report (or its JSON envelope) has
// already been written, so the reason is flagged Reported: the RunE wrapper
// must not re-print it as an "error:" line.
func applyExitErr(code int, reason string) error {
	if code == 0 {
		return nil
	}
	return &ExitCodeError{Code: code, Err: fmt.Errorf("%s", reason), Reported: true}
}

func statusGlyph(r applyResult) string {
	switch r.status {
	case statusFailed, statusConflict:
		return "✗"
	case statusSkipped:
		return "•"
	}
	g, _ := glyphWord(r.action)
	return g
}

// renderApplyReport prints the final table + summary + exit reason and
// returns the ExitCodeError implied by the results (nil for exit 0). Quiet
// collapses to the summary + result line only. Color is intentionally never
// emitted: the glyph + status word is always the signal, so --no-color needs
// no special-casing here.
func renderApplyReport(out io.Writer, fleetID string, res []applyResult, quiet bool) error {
	code, reason := applyExitCode(res)
	t := tallyResults(res)
	summary := fmt.Sprintf(
		"Applied: %d created, %d updated, %d deleted, %d unchanged, %d adopted, %d skipped, %d failed, %d conflicts.",
		t.created, t.updated, t.deleted, t.unchanged, t.adopted, t.skipped, t.failed, t.conflicts)

	if quiet {
		fmt.Fprintln(out, summary)
		fmt.Fprintf(out, "Result: %s. Exit %d.\n", reason, code)
		return applyExitErr(code, reason)
	}

	fmt.Fprintf(out, "shinyhub fleet apply  ·  fleet_id=%s\n\n", fleetID)
	for _, r := range res {
		line := fmt.Sprintf("  %s  %-24s %s", statusGlyph(r), r.slug, string(r.status))
		if r.attempts > 1 {
			line += fmt.Sprintf(" (attempt %d)", r.attempts)
		}
		if r.note != "" {
			line += "  " + r.note
		}
		if r.duration > 0 {
			line += fmt.Sprintf("   %s", r.duration.Round(100*time.Millisecond))
		}
		fmt.Fprintln(out, line)
		for _, ff := range r.firstFires {
			if ff.Status == "" {
				fmt.Fprintf(out, "     %s: first-fire triggered (run #%d)\n", ff.Schedule, ff.RunID)
			} else if ff.Status == "skipped_overlap" {
				fmt.Fprintf(out, "     %s: first-fire skipped (already warming)\n", ff.Schedule)
			} else {
				fmt.Fprintf(out, "     %s: first-fire %s\n", ff.Schedule, ff.Status)
			}
		}
	}
	fmt.Fprintf(out, "\n%s\nResult: %s. Exit %d.\n", summary, reason, code)

	// Failures end with the single most useful next command; conflicts point
	// back at plan.
	for _, r := range res {
		switch r.status {
		case statusFailed:
			fmt.Fprintf(out, "  %s: %v\n", r.slug, r.err)
			if len(r.logTail) > 0 {
				fmt.Fprintf(out, "    last %d lines of app log:\n", len(r.logTail))
				for _, l := range r.logTail {
					fmt.Fprintf(out, "      %s\n", l)
				}
			}
			fmt.Fprintf(out, "    -> shinyhub apps logs %s --tail 200\n", r.slug)
		case statusConflict:
			fmt.Fprintf(out, "  %s: %v\n    -> shinyhub fleet plan   (re-review before re-applying)\n", r.slug, r.err)
		}
	}
	return applyExitErr(code, reason)
}

// JSON envelope: extends the plan envelope (same schema_version) with a
// per-app result and summary exit fields.

type jsonResult struct {
	Status     string   `json:"status"`
	Attempts   int      `json:"attempts"`
	DurationMS int64    `json:"duration_ms"`
	Error      string   `json:"error,omitempty"`
	LogTail    []string `json:"log_tail,omitempty"`
}

type applyJSONApp struct {
	Slug          string             `json:"slug"`
	AppURL        string             `json:"app_url"`
	Action        string             `json:"action"`
	Owned         bool               `json:"owned"`
	Digest        jsonDigest         `json:"digest"`
	ConfigDrift   []jsonDriftItem    `json:"config_drift"`
	AdoptRequired bool               `json:"adopt_required"`
	AdoptFrom     string             `json:"adopt_from,omitempty"`
	PruneEligible bool               `json:"prune_eligible"`
	Result        *jsonResult        `json:"result,omitempty"`
	FirstFires    []firstFireOutcome `json:"first_fires,omitempty"`
}

type applyJSONEnvelope struct {
	SchemaVersion int            `json:"schema_version"`
	FleetID       string         `json:"fleet_id"`
	Server        string         `json:"server"`
	GeneratedAt   string         `json:"generated_at"`
	Apps          []applyJSONApp `json:"apps"`
	Summary       jsonSummary    `json:"summary"`
}

func writeFleetApplyJSON(out io.Writer, m *fleet.Manifest, host string, diff []fleet.AppDiff, res []applyResult, code int, reason string) error {
	bySlug := make(map[string]applyResult, len(res))
	for _, r := range res {
		bySlug[r.slug] = r
	}
	sorted := append([]fleet.AppDiff(nil), diff...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Slug < sorted[j].Slug })

	apps := make([]applyJSONApp, 0, len(sorted))
	for _, d := range sorted {
		drift := make([]jsonDriftItem, 0, len(d.ConfigDrift))
		for _, c := range d.ConfigDrift {
			drift = append(drift, jsonDriftItem{Key: c.Key, Server: c.Server, Desired: c.Desired})
		}
		aj := applyJSONApp{
			Slug: d.Slug, AppURL: host + "/app/" + d.Slug + "/", Action: string(d.Action), Owned: d.Owned,
			Digest:        jsonDigest{Local: d.LocalDigest, Server: d.ServerDigest},
			ConfigDrift:   drift,
			AdoptRequired: d.AdoptRequired, AdoptFrom: d.AdoptFrom, PruneEligible: d.PruneEligible,
		}
		if r, ok := bySlug[d.Slug]; ok {
			jr := &jsonResult{
				Status:     string(r.status),
				Attempts:   r.attempts,
				DurationMS: r.duration.Milliseconds(),
				LogTail:    r.logTail,
			}
			if r.err != nil {
				jr.Error = r.err.Error()
			}
			aj.Result = jr
			aj.FirstFires = r.firstFires
		}
		apps = append(apps, aj)
	}
	t := tallyResults(res)
	env := applyJSONEnvelope{
		SchemaVersion: fleetPlanSchemaVersion,
		FleetID:       m.FleetID,
		Server:        host,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Apps:          apps,
		Summary: jsonSummary{
			Counts: map[string]int{
				"created": t.created, "updated": t.updated, "deleted": t.deleted,
				"unchanged": t.unchanged, "adopted": t.adopted, "skipped": t.skipped,
				"failed": t.failed, "conflicts": t.conflicts,
			},
			ExitCode:   code,
			ExitReason: reason,
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal apply json: %w", err)
	}
	_, err = out.Write(append(b, '\n'))
	return err
}
