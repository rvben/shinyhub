package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/spf13/cobra"
)

// fleetStatusSchemaVersion is the stable --json envelope version for
// `fleet status`. It is independent of the plan/apply envelopes because the
// shape differs; consumers pin on this field.
const fleetStatusSchemaVersion = 2

// fleetStatusApp is one row of the read-only overview.
type fleetStatusApp struct {
	Slug          string `json:"slug"`
	ManagedBy     string `json:"managed_by"`     // "" when unmanaged
	FleetManaged  bool   `json:"fleet_managed"`  // true iff a real marker is set
	ContentDigest string `json:"content_digest"` // "" when never succeeded
	Access        string `json:"access"`
	Status        string `json:"status"`
}

type fleetStatusSummary struct {
	Total        int `json:"total"`
	FleetManaged int `json:"fleet_managed"`
	Unmanaged    int `json:"unmanaged"`
}

type fleetStatusEnvelope struct {
	SchemaVersion int                `json:"schema_version"`
	Server        string             `json:"server"`
	GeneratedAt   string             `json:"generated_at"`
	Apps          []fleetStatusApp   `json:"items"`
	Total         int                `json:"total"`
	Limit         int                `json:"limit"`
	Offset        int                `json:"offset"`
	Summary       fleetStatusSummary `json:"summary"`
}

// buildFleetStatus maps the apps payload into the overview, sorted by slug for
// stable output. An app counts as fleet-managed only when managed_by is a
// non-empty marker; a nil or empty string is unmanaged (the safe direction,
// matching the prune predicate in fleet apply).
func buildFleetStatus(host string, apps []db.App) fleetStatusEnvelope {
	rows := make([]fleetStatusApp, 0, len(apps))
	managed := 0
	for _, a := range apps {
		mb := ""
		if a.ManagedBy != nil {
			mb = *a.ManagedBy
		}
		fm := mb != ""
		if fm {
			managed++
		}
		rows = append(rows, fleetStatusApp{
			Slug:          a.Slug,
			ManagedBy:     mb,
			FleetManaged:  fm,
			ContentDigest: a.ContentDigest,
			Access:        a.Access,
			Status:        a.Status,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Slug < rows[j].Slug })
	return fleetStatusEnvelope{
		SchemaVersion: fleetStatusSchemaVersion,
		Server:        host,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Apps:          rows,
		Total:         len(rows),
		Summary: fleetStatusSummary{
			Total:        len(rows),
			FleetManaged: managed,
			Unmanaged:    len(rows) - managed,
		},
	}
}

// writeFleetStatusJSON emits the envelope as a single newline-terminated JSON
// object.
func writeFleetStatusJSON(out io.Writer, st fleetStatusEnvelope) error {
	b, err := json.Marshal(st)
	if err != nil {
		return &ExitCodeError{Code: 1, Err: fmt.Errorf("marshal status json: %w", err)}
	}
	_, err = out.Write(append(b, '\n'))
	return err
}

// renderFleetStatus prints the overview. Glyphs are stable ASCII so the
// output is color-free and CI/log friendly: '*' = fleet-managed, '-' =
// unmanaged. quiet collapses to just the one-line summary.
func renderFleetStatus(out io.Writer, st fleetStatusEnvelope, quiet bool) {
	summary := fmt.Sprintf("Fleet: %d app(s), %d fleet-managed, %d unmanaged.",
		st.Summary.Total, st.Summary.FleetManaged, st.Summary.Unmanaged)
	if quiet {
		fmt.Fprintln(out, summary)
		return
	}
	fmt.Fprintf(out, "shinyhub fleet status  ·  server=%s\n\n", st.Server)
	fmt.Fprintf(out, "Apps (%d)   legend: * fleet-managed  - unmanaged\n", st.Summary.Total)

	wSlug, wOwner := 0, len("unmanaged")
	for _, a := range st.Apps {
		if len(a.Slug) > wSlug {
			wSlug = len(a.Slug)
		}
		if len(a.ManagedBy) > wOwner {
			wOwner = len(a.ManagedBy)
		}
	}
	for _, a := range st.Apps {
		glyph, owner := "-", "unmanaged"
		if a.FleetManaged {
			glyph, owner = "*", a.ManagedBy
		}
		fmt.Fprintf(out, "  %s  %-*s  %-*s  %s  %s\n",
			glyph, wSlug, a.Slug, wOwner, owner, shortDigest(a.ContentDigest), a.Status)
	}
	fmt.Fprintf(out, "\n%s\n", summary)
}

type fleetStatusFlags struct {
	jsonOutput bool
	limit      int
	offset     int
	fields     string
}

func newFleetStatusCmd() *cobra.Command {
	f := &fleetStatusFlags{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Read-only fleet overview (no manifest): ownership and live digest",
		Long: "Lists every app the server knows with its fleet ownership marker\n" +
			"and live deployment digest. Requires no manifest; makes one GET.\n\n" +
			"Exit codes:\n" +
			"  0  overview printed\n" +
			"  3  transport / auth error\n\n" +
			"Example:\n" +
			"  shinyhub fleet status --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFleetStatus(cmd, f)
		},
	}
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Emit the machine-readable JSON envelope")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "Maximum number of items to return (0 = all)")
	cmd.Flags().IntVar(&f.offset, "offset", 0, "Number of items to skip")
	cmd.Flags().StringVar(&f.fields, "fields", "", "Comma-separated item fields to include")
	return cmd
}

func runFleetStatus(cmd *cobra.Command, f *fleetStatusFlags) error {
	// fleet status is a document command; NDJSON is not a valid output mode.
	// -o json behaves like --json (both select the machine-readable envelope).
	if format, err := resolveFormat(f.jsonOutput, false); err != nil {
		return err
	} else if format == formatJSON {
		f.jsonOutput = true
	}
	errOut := cmd.ErrOrStderr()
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(errOut, "  ✗ not authenticated: %v\n     run 'shinyhub login' or pass --config\n", err)
		return &ExitCodeError{Code: 3, Err: err, Reported: true}
	}
	apps, err := fetchApps(cfg)
	if err != nil {
		fmt.Fprintf(errOut, "  ✗ cannot reach server %s: %v\n     check the URL / run 'shinyhub login'\n", cfg.Host, err)
		return &ExitCodeError{Code: 3, Err: err, Reported: true}
	}
	// buildFleetStatus computes summary over ALL apps; slicing happens after.
	st := buildFleetStatus(cfg.Host, apps)
	out := cmd.OutOrStdout()
	if f.jsonOutput {
		// Convert to maps so sliceAndProject can apply limit/offset/fields.
		items := make([]map[string]any, len(st.Apps))
		for i, a := range st.Apps {
			items[i] = map[string]any{
				"slug":           a.Slug,
				"managed_by":     a.ManagedBy,
				"fleet_managed":  a.FleetManaged,
				"content_digest": a.ContentDigest,
				"access":         a.Access,
				"status":         a.Status,
			}
		}
		lf := &listFlags{limit: f.limit, offset: f.offset, fields: f.fields}
		sliced, serr := sliceAndProject(items, lf)
		if serr != nil {
			return serr
		}
		st.Limit = f.limit
		st.Offset = f.offset
		env := map[string]any{
			"schema_version": st.SchemaVersion,
			"server":         st.Server,
			"generated_at":   st.GeneratedAt,
			"items":          sliced,
			"total":          st.Total,
			"limit":          st.Limit,
			"offset":         st.Offset,
			"summary":        st.Summary,
		}
		b, merr := json.Marshal(env)
		if merr != nil {
			return &ExitCodeError{Code: 1, Err: fmt.Errorf("marshal status json: %w", merr)}
		}
		_, werr := out.Write(append(b, '\n'))
		return werr
	}
	// Table mode: apply limit/offset (fields are ignored; table renders fixed columns).
	// Validate bounds via sliceAndProject to share the same error contract.
	lf := &listFlags{limit: f.limit, offset: f.offset}
	if f.limit != 0 || f.offset != 0 {
		// Dummy single-key items so sliceAndProject can validate and slice.
		dummy := make([]map[string]any, len(st.Apps))
		for i := range st.Apps {
			dummy[i] = map[string]any{"i": i}
		}
		sliced, serr := sliceAndProject(dummy, lf)
		if serr != nil {
			return serr
		}
		filtered := make([]fleetStatusApp, 0, len(sliced))
		for _, it := range sliced {
			if idx, ok := it["i"].(int); ok {
				filtered = append(filtered, st.Apps[idx])
			}
		}
		st.Apps = filtered
	}
	renderFleetStatus(out, st, quietFlag)
	return nil
}
