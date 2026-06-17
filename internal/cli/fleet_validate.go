package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/spf13/cobra"
)

type fleetValidateFlags struct {
	file string
}

func newFleetValidateCmd() *cobra.Command {
	f := &fleetValidateFlags{}
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a fleet manifest locally (no server contact)",
		Long: "Validate parses fleet.toml and runs every cheap, local check\n" +
			"the same way `fleet plan` does, but WITHOUT contacting the server: no\n" +
			"host, no token, no network. It is the fleet-level analogue of\n" +
			"`manifest validate`, intended for a pre-merge CI gate.\n\n" +
			"Checks: TOML parses; fleet_id is present and valid; every slug is valid\n" +
			"and unique; each [[app]] source resolves to a local directory (git URLs\n" +
			"are syntax-checked, not cloned); and each local source's shinyhub.toml\n" +
			"parses (delegating to the same parser used at deploy time).\n\n" +
			"Exit codes:\n" +
			"  0  manifest is valid\n" +
			"  1  manifest is invalid (every problem is listed)\n\n" +
			"Example:\n" +
			"  shinyhub fleet validate",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFleetValidate(cmd, f)
		},
	}
	cmd.Flags().StringVarP(&f.file, "file", "f", defaultFleetManifest, "Path to the fleet manifest")
	return cmd
}

func runFleetValidate(cmd *cobra.Command, f *fleetValidateFlags) error {
	// fleet validate is a document command; NDJSON is not a valid output mode.
	if _, err := resolveFormat(false, false); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	f.file = resolveFleetManifest(cmd, f.file, errOut)
	data, err := os.ReadFile(f.file)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(errOut, "no %s found. Run 'shinyhub fleet init' to generate one from your\n"+
				"deployed apps, or pass -f <path> to point at an existing manifest.\n",
				filepath.Base(f.file))
			return &ExitCodeError{Code: 1, Err: fmt.Errorf("manifest not found: %s", f.file), Reported: true}
		}
		return &ExitCodeError{Code: 1, Err: fmt.Errorf("read %s: %w", f.file, err)}
	}

	m, probs := validateFleetManifest(f.file, data)
	if len(probs) > 0 {
		fmt.Fprintf(errOut, "shinyhub fleet validate: validating %s\n\n", f.file)
		for _, p := range probs {
			fmt.Fprintf(errOut, "  ✗ %s\n", p)
		}
		fmt.Fprintf(errOut, "\n%d problem(s) found. Fix these and re-run.\n", len(probs))
		return &ExitCodeError{Code: 1, Err: fmt.Errorf("%d manifest problem(s)", len(probs)), Reported: true}
	}

	if quietFlag {
		fmt.Fprintf(out, "%s: OK (%d app(s))\n", f.file, len(m.Apps))
		return nil
	}
	fmt.Fprintf(out, "%s: OK (valid)\n", f.file)
	fmt.Fprintf(out, "  fleet_id: %s\n", m.FleetID)
	if len(m.Apps) == 0 {
		// An empty fleet is accepted (it matches what `fleet apply --prune`
		// accepts: a converge that removes every managed app). Surface it
		// plainly so a manifest that lost its [[app]] blocks to a typo is
		// noticed rather than silently passing as "valid".
		fmt.Fprintf(out, "  note: manifest declares no apps\n")
	}
	for _, app := range m.Apps {
		fmt.Fprintf(out, "  app %q: source %s\n", app.Slug, app.Source)
	}
	return nil
}

// validateFleetManifest runs every offline check on a fleet manifest and
// returns the parsed manifest (nil only on a hard TOML parse failure) plus a
// rendered, compiler-style list of all problems found. It reuses the exact
// primitives `fleet plan`/`apply` use - fleet.ParseManifest for structure,
// fleet.ParseSource for local-source existence and git-URL syntax, and
// deploy.LoadManifest for the per-app shinyhub.toml - so what validates here is
// exactly what applies later. It performs filesystem I/O but never any network
// call.
func validateFleetManifest(file string, data []byte) (*fleet.Manifest, []string) {
	m, probs := fleet.ParseManifest(data, file)
	var msgs []string
	for _, p := range probs {
		msgs = append(msgs, p.Error())
	}
	// A nil manifest means a hard TOML parse failure: there is no struct to run
	// source checks against, so the parse error is the whole story.
	if m == nil {
		return nil, msgs
	}

	manifestDir := filepath.Dir(file)
	for _, app := range m.Apps {
		if app.Source == "" {
			// Already reported as "source is required" by ParseManifest.
			continue
		}
		ps, sp := fleet.ParseSource(app.Source, manifestDir)
		if sp != nil {
			msgs = append(msgs, fmt.Sprintf("app %q: %s", app.Slug, sp.Msg))
			continue
		}
		// Git sources are syntax-only here (no clone). For a local source,
		// parse its shinyhub.toml exactly as the server does at deploy time.
		if ps.Kind == fleet.SourceLocal {
			if _, err := deploy.LoadManifest(ps.LocalPath); err != nil {
				msgs = append(msgs, fmt.Sprintf("app %q: shinyhub.toml: %v", app.Slug, err))
			}
		}
	}
	return m, msgs
}
