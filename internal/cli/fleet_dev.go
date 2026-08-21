package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/rvben/shinyhub/internal/localrun"
	"github.com/spf13/cobra"
)

type fleetDevFlags struct {
	file string
	run  localRunFlags
}

func newFleetDevCmd() *cobra.Command {
	f := &fleetDevFlags{}
	cmd := &cobra.Command{
		Use:   "dev APP",
		Short: "Run one fleet app locally with its shared bundle inputs",
		Long: `Run one local-source fleet app through the same staged local runner as
'shinyhub run', with its declared [[bundle_file]] inputs composed into the
workspace. No server, login, or network connection is required.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectLocalRunGlobalFlags(cmd); err != nil {
				return err
			}
			return runFleetDev(cmd, args[0], f)
		},
	}
	cmd.Flags().StringVarP(&f.file, "file", "f", defaultFleetManifest, "Path to the fleet manifest")
	configureLocalRunCommand(cmd, &f.run, false)
	return cmd
}

func runFleetDev(cmd *cobra.Command, slug string, f *fleetDevFlags) error {
	f.file = resolveFleetManifest(cmd, f.file, cmd.ErrOrStderr())
	data, err := os.ReadFile(f.file)
	if err != nil {
		return &ExitCodeError{Code: 1, Kind: KindValidation, Err: fmt.Errorf("read fleet manifest: %w", err)}
	}
	m, problems := fleet.ParseManifest(data, f.file)
	if len(problems) != 0 {
		messages := make([]string, 0, len(problems))
		for _, problem := range problems {
			messages = append(messages, problem.Error())
		}
		return &ExitCodeError{Code: 1, Kind: KindValidation,
			Err: fmt.Errorf("invalid fleet manifest:\n%s", strings.Join(messages, "\n"))}
	}

	var selected *fleet.AppEntry
	for i := range m.Apps {
		if m.Apps[i].Slug == slug {
			selected = &m.Apps[i]
			break
		}
	}
	if selected == nil {
		return &ExitCodeError{Code: 1, Kind: KindValidation,
			Err: fmt.Errorf("app %q is not declared in %s", slug, f.file)}
	}
	parsed, sourceProblem := fleet.ParseSource(selected.Source, filepath.Dir(f.file))
	if sourceProblem != nil {
		return &ExitCodeError{Code: 1, Kind: KindValidation,
			Err: fmt.Errorf("app %q: %s", slug, sourceProblem.Msg)}
	}
	if parsed.Kind != fleet.SourceLocal {
		return &ExitCodeError{Code: 1, Kind: KindValidation,
			Err: fmt.Errorf("app %q uses a git+ source; fleet dev supports local app sources only", slug)}
	}

	var inputs []bundle.FileInputSpec
	for _, declaration := range m.BundleFiles {
		for _, consumer := range declaration.Consumers {
			if consumer == slug {
				inputs = append(inputs, bundle.FileInputSpec{From: declaration.From, To: declaration.To})
				break
			}
		}
	}
	return executeLocalRun(cmd, parsed.LocalPath, slug, &f.run, func(options *localrun.Options) {
		options.ManifestPath = f.file
		options.BundleInputs = inputs
	})
}
