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
// {items,total,limit,offset} envelope. extra carries command-specific
// envelope keys (e.g. data ls quota); tableFn renders the human view of the
// already sliced+projected items.
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
		env := map[string]any{
			"items": sliced, "total": total,
			"limit": f.limit, "offset": f.offset,
		}
		for k, v := range extra {
			env[k] = v
		}
		enc := json.NewEncoder(out)
		return enc.Encode(env)
	}
	if tableFn != nil {
		tableFn(out, sliced)
	}
	if len(sliced) < total {
		fmt.Fprintf(errOut, "showing %d of %d (use --limit/--offset to page)\n", len(sliced), total)
	}
	return nil
}

func sliceAndProject(items []map[string]any, f *listFlags) ([]map[string]any, error) {
	start := f.offset
	if start > len(items) {
		start = len(items)
	}
	end := len(items)
	if f.limit > 0 && start+f.limit < end {
		end = start + f.limit
	}
	sliced := items[start:end]
	if f.fields == "" {
		if sliced == nil {
			sliced = []map[string]any{}
		}
		return sliced, nil
	}
	want := map[string]bool{}
	for _, name := range strings.Split(f.fields, ",") {
		want[strings.TrimSpace(name)] = true
	}
	valid := map[string]bool{}
	for _, it := range items {
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
	projected := make([]map[string]any, 0, len(sliced))
	for _, it := range sliced {
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
