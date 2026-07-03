package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// listFlags are the bounded-output controls every list command registers.
type listFlags struct {
	jsonOutput bool // legacy --json alias
	limit      int
	offset     int
	fields     string
}

// addListFlags registers --json (legacy alias), --limit, --offset, --fields.
func addListFlags(cmd *cobra.Command, f *listFlags) {
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "Output as JSON (alias for --output json)")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "Maximum number of items to return (0 = all)")
	cmd.Flags().IntVar(&f.offset, "offset", 0, "Number of items to skip")
	cmd.Flags().StringVar(&f.fields, "fields", "", "Comma-separated item fields to include")
}

// renderList resolves the format and renders items with the standard
// {items,total,limit,offset} envelope, paginating client-side. Used by list
// commands whose server endpoint returns the full result set. extra carries
// command-specific envelope keys (e.g. data ls quota); tableFn renders the
// human view of the already sliced+projected items.
func renderList(cmd *cobra.Command, f *listFlags, items []map[string]any,
	extra map[string]any, tableFn func(io.Writer, []map[string]any)) error {
	format, err := resolveFormat(f.jsonOutput, false)
	if err != nil {
		return err
	}
	return renderListTo(cmd.OutOrStdout(), cmd.ErrOrStderr(), format, f, items, extra, tableFn)
}

func renderListTo(out, errOut io.Writer, format outputFormat, f *listFlags,
	items []map[string]any, extra map[string]any,
	tableFn func(io.Writer, []map[string]any)) error {
	total := len(items)
	sliced, err := sliceAndProject(items, f)
	if err != nil {
		return err
	}
	if format == formatJSON {
		return json.NewEncoder(out).Encode(listEnvelope(sliced, total, f.limit, f.offset, extra))
	}
	if tableFn != nil {
		tableFn(out, sliced)
	}
	if len(sliced) < total {
		fmt.Fprintf(errOut, "showing %d of %d (use --limit/--offset to page)\n", len(sliced), total)
	}
	return nil
}

// renderServerList renders a page the server already paginated: the CLI sent
// limit/offset, the server returned {items:page, total:N}, so there is no
// client-side re-slicing. --fields projection still applies to the returned
// page. total is the server's full-result-set count (drives the "showing X of
// Y" hint), never len(page).
func renderServerList(cmd *cobra.Command, f *listFlags, page []map[string]any,
	total int, extra map[string]any, tableFn func(io.Writer, []map[string]any)) error {
	format, err := resolveFormat(f.jsonOutput, false)
	if err != nil {
		return err
	}
	return renderServerListTo(cmd.OutOrStdout(), cmd.ErrOrStderr(), format, f, page, total, extra, tableFn)
}

func renderServerListTo(out, errOut io.Writer, format outputFormat, f *listFlags,
	page []map[string]any, total int, extra map[string]any,
	tableFn func(io.Writer, []map[string]any)) error {
	projected, err := projectFields(page, page, f.fields)
	if err != nil {
		return err
	}
	if format == formatJSON {
		return json.NewEncoder(out).Encode(listEnvelope(projected, total, f.limit, f.offset, extra))
	}
	if tableFn != nil {
		tableFn(out, projected)
	}
	if len(page) < total {
		fmt.Fprintf(errOut, "showing %d of %d (use --limit/--offset to page)\n", len(page), total)
	}
	return nil
}

// listEnvelope builds the standard {items,total,limit,offset} envelope, folding
// in command-specific extra keys. The standard fields are authoritative: a
// colliding key in extra is ignored so it can never corrupt total/limit/offset.
func listEnvelope(items []map[string]any, total, limit, offset int, extra map[string]any) map[string]any {
	env := map[string]any{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	}
	for k, v := range extra {
		switch k {
		case "items", "total", "limit", "offset":
			// Standard fields win; ignore collisions.
		default:
			env[k] = v
		}
	}
	return env
}

func sliceAndProject(items []map[string]any, f *listFlags) ([]map[string]any, error) {
	if f.offset < 0 {
		return nil, validationErr("--offset must be >= 0", "")
	}
	if f.limit < 0 {
		return nil, validationErr("--limit must be >= 0", "")
	}
	start := f.offset
	if start > len(items) {
		start = len(items)
	}
	end := len(items)
	if f.limit > 0 && start+f.limit < end {
		end = start + f.limit
	}
	sliced := items[start:end]
	// Validate the requested fields against the full item set (the complete
	// schema), then project the sliced page.
	return projectFields(items, sliced, f.fields)
}

// projectFields validates the --fields CSV against validationSet's keys (the
// complete schema for the result set) and returns page projected to those
// fields. An empty fields string returns page unchanged (non-nil). When
// validationSet is empty there is no schema to validate against, so validation
// is skipped and an empty page is returned - this lets paginated last-pages and
// --fields compose without a spurious "unknown field" error.
func projectFields(validationSet, page []map[string]any, fields string) ([]map[string]any, error) {
	if fields == "" {
		if page == nil {
			return []map[string]any{}, nil
		}
		return page, nil
	}
	if len(validationSet) == 0 {
		return []map[string]any{}, nil
	}
	want := map[string]bool{}
	for _, name := range strings.Split(fields, ",") {
		want[strings.TrimSpace(name)] = true
	}
	valid := map[string]bool{}
	for _, it := range validationSet {
		for k := range it {
			valid[k] = true
		}
	}
	var unknown, validNames []string
	for name := range want {
		if !valid[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		for k := range valid {
			validNames = append(validNames, k)
		}
		sort.Strings(unknown)
		sort.Strings(validNames)
		return nil, validationErr(
			fmt.Sprintf("unknown field(s): %s; valid fields: %s",
				strings.Join(unknown, ", "), strings.Join(validNames, ", ")),
			"use --fields with one of the listed field names")
	}
	projected := make([]map[string]any, 0, len(page))
	for _, it := range page {
		p := map[string]any{}
		for k, v := range it {
			if want[k] {
				p[k] = v
			}
		}
		projected = append(projected, p)
	}
	return projected, nil
}
