package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

func newScheduleRunsCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:   "runs <slug> <name>",
		Short: "List run history for a scheduled job",
		Args:  cobra.ExactArgs(2),
	}
	addListFlags(cmd, f)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug, name := args[0], args[1]
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		id, err := lookupScheduleID(cfg, slug, name)
		if err != nil {
			return err
		}
		// Run history is paginated server-side (it can be large); the CLI forwards
		// --limit/--offset and renders the returned page with an accurate total.
		path := fmt.Sprintf("/api/apps/%s/schedules/%d/runs", slug, id)
		runs, total, err := getPaginatedList(cfg, "list schedule runs", path, f)
		if err != nil {
			return err
		}
		return renderServerList(cmd, f, runs, total, nil, func(w io.Writer, items []map[string]any) {
			if len(items) == 0 {
				fmt.Fprintln(w, "No runs yet.")
				return
			}
			fmt.Fprintf(w, "%-6s  %-10s  %-9s  %-5s  %-19s  %s\n",
				"ID", "STATUS", "TRIGGER", "EXIT", "STARTED", "DURATION")
			for _, r := range items {
				finished, _ := r["finished_at"].(string)
				// exit_code is null while running and stays null for an
				// interrupted run, so show "-" unless a real code is present.
				exit := "-"
				if finished != "" && r["exit_code"] != nil {
					exit = fmt.Sprintf("%v", r["exit_code"])
				}
				fmt.Fprintf(w, "%-6v  %-10v  %-9v  %-5s  %-19s  %s\n",
					r["id"], r["status"], r["trigger"], exit,
					fmtRunTime(r["started_at"]), fmtRunDuration(r["started_at"], r["finished_at"]))
			}
		})
	}
	return cmd
}

// fmtRunTime renders an RFC3339 timestamp as a compact local-free wall string,
// falling back to the raw value (or "-") when it is empty or unparseable.
func fmtRunTime(v any) string {
	s, _ := v.(string)
	if s == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

// fmtRunDuration computes finished-started as a rounded duration; "-" when the
// run has not finished or either timestamp is missing/unparseable.
func fmtRunDuration(start, finish any) string {
	ss, _ := start.(string)
	fs, _ := finish.(string)
	if ss == "" || fs == "" {
		return "-"
	}
	st, e1 := time.Parse(time.RFC3339Nano, ss)
	ft, e2 := time.Parse(time.RFC3339Nano, fs)
	if e1 != nil || e2 != nil {
		return "-"
	}
	d := ft.Sub(st)
	if d < 0 {
		return "-"
	}
	return d.Round(time.Millisecond).String()
}
