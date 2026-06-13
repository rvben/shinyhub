package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		// Fetch up to the server's max so the client-side --limit/--offset can
		// page over real history rather than only the server's default page.
		url := fmt.Sprintf("%s/api/apps/%s/schedules/%d/runs?limit=200", cfg.Host, slug, id)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", authHeader(cfg.Token))
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return httpError(cfg.Token, "list schedule runs", resp, out)
		}
		var runs []map[string]any
		if err := json.Unmarshal(out, &runs); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return renderList(cmd, f, runs, nil, func(w io.Writer, items []map[string]any) {
			if len(items) == 0 {
				fmt.Fprintln(w, "No runs yet.")
				return
			}
			fmt.Fprintf(w, "%-6s  %-10s  %-9s  %-5s  %-19s  %s\n",
				"ID", "STATUS", "TRIGGER", "EXIT", "STARTED", "DURATION")
			for _, r := range items {
				finished, _ := r["finished_at"].(string)
				exit := "-"
				if finished != "" {
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
