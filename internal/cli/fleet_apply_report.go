package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/rvben/shinyhub/internal/deployfail"
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
	// warmRestarted records the opt-in post-first-fire replica cycle.
	warmRestarted bool
	// logTail holds the failing app's last process-log lines, populated only
	// when a deploy-bearing action fails (e.g. the app crashed on startup), so
	// the operator sees the cause without a second round-trip to the host.
	logTail []string
	// attemptsDetail holds one record per FAILED deploy attempt (empty when the
	// first attempt succeeded). It is populated even on a retried-then-succeeded
	// deploy so the operator can see why an earlier attempt failed.
	attemptsDetail []attemptOutcome
	// deployFailed is true only when the deploy step itself failed (not a
	// post-deploy config patch / ownership stamp / first-fire failure). The
	// top-level failure_kind is emitted only when this is set, so a post-deploy
	// failure never inherits a deploy attempt's kind.
	deployFailed bool
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
		return 4, fmt.Sprintf("PARTIAL - %d failed after retries", t.failed)
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

// slugColumnWidth sizes the slug column to the longest slug in the run. The
// whole report is in hand before a line is printed, so the width comes from the
// data rather than from a constant that a longer slug silently overflows.
func slugColumnWidth(res []applyResult) int {
	w := 0
	for _, r := range res {
		if n := utf8.RuneCountInString(r.slug); n > w {
			w = n
		}
	}
	return w
}

// renderResultRows prints one line per result plus its attempt/first-fire
// detail lines. Shared by the projects and apps sections of renderApplyReport
// so both use the same glyph and status-word rules; each section sizes its own
// slug column since projects and apps are never aligned against each other.
func renderResultRows(out io.Writer, s styler, res []applyResult, wSlug int) {
	for _, r := range res {
		statusWord := s.status(string(r.status))
		if r.status == statusFailed && r.deployFailed {
			if k := finalFailureKind(r); k != "" {
				statusWord += " " + s.dim("["+string(k)+"]")
			}
		}
		// The slug is the only padded field, and it is never painted, so no
		// escape can enter a column width.
		line := fmt.Sprintf("  %s  %-*s %s", s.glyphPaint(statusGlyph(r)), wSlug, r.slug, statusWord)
		if r.attempts > 1 {
			line += s.dim(fmt.Sprintf(" (attempt %d)", r.attempts))
		}
		if r.note != "" {
			line += "  " + s.dim(r.note)
		}
		if r.duration > 0 {
			line += s.dim(fmt.Sprintf("   %s", r.duration.Round(100*time.Millisecond)))
		}
		fmt.Fprintln(out, line)
		for _, a := range r.attemptsDetail {
			fmt.Fprintf(out, "     %s\n", s.dim(fmt.Sprintf("attempt %d: %s", a.Attempt, a.Kind)))
		}
		for _, ff := range r.firstFires {
			if ff.Status == "" {
				fmt.Fprintf(out, "     %s: first-fire triggered (run #%d)\n", ff.Schedule, ff.RunID)
			} else if ff.Status == "skipped_overlap" {
				fmt.Fprintf(out, "     %s: first-fire skipped (already warming)\n", ff.Schedule)
			} else {
				fmt.Fprintf(out, "     %s: first-fire %s\n", ff.Schedule, ff.Status)
			}
		}
		if r.warmRestarted {
			fmt.Fprintln(out, "     replicas restarted after first-fire warm-up")
		}
	}
}

// renderApplyReport prints the final table + summary + exit reason and
// returns the ExitCodeError implied by the results (nil for exit 0). Quiet
// collapses to the summary + result line only. The glyph and the status word
// always carry the signal on their own, so color only weights what is already
// legible: strip it and the report reads identically.
func renderApplyReport(out io.Writer, fleetID string, o applyOutcome, quiet bool) error {
	s := stylerFor(out)
	all := o.all()
	code, reason := applyExitCode(all)
	t := tallyResults(all)
	summary := fmt.Sprintf(
		"Applied: %d created, %d updated, %d deleted, %d unchanged, %d adopted, %d skipped, %d failed, %d conflicts.",
		t.created, t.updated, t.deleted, t.unchanged, t.adopted, t.skipped, t.failed, t.conflicts)

	if quiet {
		fmt.Fprintln(out, summary)
		fmt.Fprintf(out, "Result: %s. Exit %d.\n", reason, code)
		return applyExitErr(code, reason)
	}

	fmt.Fprintf(out, "shinyhub fleet apply  ·  fleet_id=%s\n\n", fleetID)

	// Omitted entirely when the manifest declares no projects, so existing
	// output stays byte-identical for manifests that do not use the feature.
	if len(o.projects) > 0 {
		fmt.Fprintf(out, "Projects (%d)\n", len(o.projects))
		renderResultRows(out, s, o.projects, slugColumnWidth(o.projects))
		fmt.Fprintln(out)
	}

	renderResultRows(out, s, o.apps, slugColumnWidth(o.apps))
	fmt.Fprintf(out, "\n%s\nResult: %s. Exit %d.\n", summary, reason, code)

	// Failures end with the single most useful next command; conflicts point
	// back at plan.
	for _, r := range all {
		switch r.status {
		case statusFailed:
			fmt.Fprintf(out, "  %s: %v\n", s.red(r.slug), r.err)
			if len(r.logTail) > 0 {
				fmt.Fprintf(out, "    %s\n", s.dim(fmt.Sprintf("last %d lines of app log:", len(r.logTail))))
				for _, l := range r.logTail {
					fmt.Fprintf(out, "      %s\n", s.dim(l))
				}
			}
			fmt.Fprintf(out, "    %s\n", s.dim(fmt.Sprintf("-> shinyhub apps logs %s --tail 200", r.slug)))
		case statusConflict:
			fmt.Fprintf(out, "  %s: %v\n    %s\n", s.red(r.slug), r.err,
				s.dim("-> shinyhub fleet plan   (re-review before re-applying)"))
		}
	}
	return applyExitErr(code, reason)
}

// JSON envelope: extends the plan envelope (same schema_version) with a
// per-app result and summary exit fields.

type jsonAttempt struct {
	Attempt     int    `json:"attempt"`
	FailureKind string `json:"failure_kind"`
	Error       string `json:"error,omitempty"`
}

type jsonResult struct {
	Status         string        `json:"status"`
	Attempts       int           `json:"attempts"`
	FailureKind    string        `json:"failure_kind,omitempty"`
	AttemptDetails []jsonAttempt `json:"attempt_details,omitempty"`
	DurationMS     int64         `json:"duration_ms"`
	Error          string        `json:"error,omitempty"`
	LogTail        []string      `json:"log_tail,omitempty"`
}

// finalFailureKind returns the kind to report at the top level for a failed
// result: the last failed attempt's kind, or Unknown for a non-deploy failure
// (config patch, ownership stamp) that recorded no attempt detail.
func finalFailureKind(r applyResult) deployfail.Kind {
	if n := len(r.attemptsDetail); n > 0 {
		return r.attemptsDetail[n-1].Kind
	}
	return deployfail.Unknown
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
	WarmRestarted bool               `json:"warm_restarted,omitempty"`
}

// applyJSONProject is a project row in the apply JSON envelope: display
// metadata plus its convergence result. It carries no digest, adopt or prune
// fields - a project has none of those - which is why it is not applyJSONApp.
type applyJSONProject struct {
	Slug   string          `json:"slug"`
	Action string          `json:"action"`
	Drift  []jsonDriftItem `json:"drift"`
	Result *jsonResult     `json:"result,omitempty"`
}

type applyJSONEnvelope struct {
	SchemaVersion int                `json:"schema_version"`
	FleetID       string             `json:"fleet_id"`
	Server        string             `json:"server"`
	GeneratedAt   string             `json:"generated_at"`
	Projects      []applyJSONProject `json:"projects"`
	Apps          []applyJSONApp     `json:"apps"`
	Summary       jsonSummary        `json:"summary"`
}

// resultToJSON maps one applyResult onto the shared jsonResult shape used by
// both apps and projects.
func resultToJSON(r applyResult) *jsonResult {
	jr := &jsonResult{
		Status:     string(r.status),
		Attempts:   r.attempts,
		DurationMS: r.duration.Milliseconds(),
		LogTail:    r.logTail,
	}
	if r.err != nil {
		jr.Error = r.err.Error()
	}
	for _, a := range r.attemptsDetail {
		jr.AttemptDetails = append(jr.AttemptDetails, jsonAttempt{
			Attempt: a.Attempt, FailureKind: string(a.Kind), Error: a.Err,
		})
	}
	if r.status == statusFailed && r.deployFailed {
		jr.FailureKind = string(finalFailureKind(r))
	}
	return jr
}

func writeFleetApplyJSON(out io.Writer, m *fleet.Manifest, host string, diff []fleet.AppDiff, projects []fleet.ProjectDiff, o applyOutcome, code int, reason string) error {
	bySlug := make(map[string]applyResult, len(o.apps))
	for _, r := range o.apps {
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
			aj.Result = resultToJSON(r)
			aj.FirstFires = r.firstFires
			aj.WarmRestarted = r.warmRestarted
		}
		apps = append(apps, aj)
	}

	projectsBySlug := make(map[string]applyResult, len(o.projects))
	for _, r := range o.projects {
		projectsBySlug[r.slug] = r
	}
	sortedProjects := append([]fleet.ProjectDiff(nil), projects...)
	sort.Slice(sortedProjects, func(i, j int) bool { return sortedProjects[i].Slug < sortedProjects[j].Slug })

	jsonProjects := make([]applyJSONProject, 0, len(sortedProjects))
	for _, d := range sortedProjects {
		drift := make([]jsonDriftItem, 0, len(d.Drift))
		for _, c := range d.Drift {
			drift = append(drift, jsonDriftItem{Key: c.Key, Server: c.Server, Desired: c.Desired})
		}
		pj := applyJSONProject{Slug: d.Slug, Action: string(d.Action), Drift: drift}
		if r, ok := projectsBySlug[d.Slug]; ok {
			pj.Result = resultToJSON(r)
		}
		jsonProjects = append(jsonProjects, pj)
	}

	t := tallyResults(o.all())
	env := applyJSONEnvelope{
		SchemaVersion: fleetPlanSchemaVersion,
		FleetID:       m.FleetID,
		Server:        host,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Projects:      jsonProjects,
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
