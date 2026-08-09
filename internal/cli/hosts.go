package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// newHostsCmd returns the `hosts` command: the saved servers and which one
// commands currently target. It answers entirely from the credentials file, so
// it still works when every one of those servers is unreachable - which is when
// "where am I pointed?" is most often asked.
func newHostsCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:   "hosts",
		Short: "List saved ShinyHub servers and show which one is current",
		Long: `Hosts lists the servers ` + "`shinyhub login`" + ` has saved credentials for and
marks the one commands target by default. Switch with ` + "`shinyhub use`" + `, or
target a different one for a single command with ` + "`--host`" + `.

No token is ever printed. The report is built from the local credentials file
only, so no server is contacted.`,
		Args: cobra.NoArgs,
	}
	addListFlags(cmd, f)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := rejectHostFlag("hosts lists every saved server",
			"drop --host; to see one server's login, use `shinyhub whoami --host <name|url>`"); err != nil {
			return err
		}
		st, err := loadStore()
		if err != nil {
			return err
		}
		items := make([]map[string]any, 0, len(st.Hosts))
		for _, host := range st.sortedHosts() {
			cred := st.Hosts[host]
			items = append(items, map[string]any{
				"host":     host,
				"name":     cred.Name,
				"user":     cred.User,
				"current":  host == st.CurrentHost,
				"saved_at": cred.SavedAt,
			})
		}
		return renderList(cmd, f, items,
			map[string]any{"current_host": st.CurrentHost},
			func(w io.Writer, items []map[string]any) {
				if len(items) == 0 {
					fmt.Fprintln(w, "No saved hosts. Run `shinyhub login --host <url>` to add one.")
					return
				}
				fmt.Fprintf(w, "%-2s %-16s %-40s %s\n", "", "NAME", "HOST", "USER")
				for _, h := range items {
					marker := ""
					if h["current"] == true {
						marker = "*"
					}
					fmt.Fprintf(w, "%-2s %-16s %-40s %s\n",
						marker, dashIfEmpty(h["name"]), dashIfEmpty(h["host"]), dashIfEmpty(h["user"]))
				}
			})
	}
	return cmd
}

// dashIfEmpty renders an unset optional field as "-" so a blank column reads as
// "not recorded" rather than as an alignment glitch. Fields that --fields has
// projected away are absent rather than empty, and render the same way.
func dashIfEmpty(v any) string {
	s, _ := v.(string)
	if s == "" {
		return "-"
	}
	return s
}

// rejectHostFlag refuses the global --host for a command that acts on local
// state. The flag means "target this server for one command", which has no
// meaning for a command that contacts no server, and silently ignoring it would
// let the user believe they had scoped something they had not.
func rejectHostFlag(what, hint string) error {
	if hostFlagOverride == "" {
		return nil
	}
	return validationErr(fmt.Sprintf("--host does not apply here: %s", what), hint)
}

// newUseCmd returns the `use` command: switch which saved server subsequent
// commands target. It is deliberately local-only - no login, no network - so
// switching away from a server that is down always works.
func newUseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "use <host>",
		Short: "Switch the current ShinyHub server to a saved host",
		Long: `Use selects which saved server subsequent commands target. The argument is
either a server's short name (set with ` + "`shinyhub login --name`" + `) or its
URL. The server must already have saved credentials; run ` + "`shinyhub login`" + `
first to add one.`,
		// A custom arity check so the most likely mistake - reaching for the
		// global --host, which every other command uses to pick a server - is
		// answered with the command that works rather than a bare arity error.
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return nil
			}
			if len(args) == 0 && hostFlagOverride != "" {
				return validationErr("use takes the server as its argument, not --host",
					fmt.Sprintf("run `shinyhub use %s`", hostFlagOverride))
			}
			return validationErr(fmt.Sprintf("use takes exactly one server, got %d", len(args)),
				"run `shinyhub use <name|url>`; `shinyhub hosts` lists what is saved")
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectHostFlag("use switches the current server named by its argument",
				fmt.Sprintf("drop --host, or run `shinyhub use %s` to switch to that one instead", hostFlagOverride)); err != nil {
				return err
			}
			st, err := loadStore()
			if err != nil {
				return err
			}
			host, err := st.resolveSelector(args[0])
			if err != nil {
				return err
			}
			cred, ok := st.Hosts[host]
			if !ok {
				return authErr(fmt.Sprintf("not logged in to %s", host),
					fmt.Sprintf("run `shinyhub login --host %s` first; %s", host, st.knownHostsHint()))
			}
			if st.CurrentHost == host {
				return renderAction(cmd, "unchanged",
					map[string]any{"host": host, "name": cred.Name, "user": cred.User},
					fmt.Sprintf("Already using %s", st.label(host)))
			}
			st.CurrentHost = host
			if err := saveStore(st); err != nil {
				return err
			}
			return renderAction(cmd, "switched",
				map[string]any{"host": host, "name": cred.Name, "user": cred.User},
				fmt.Sprintf("Now using %s", st.label(host)))
		},
	}
	return cmd
}
