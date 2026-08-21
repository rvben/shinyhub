package cli

import (
	"fmt"
	"path/filepath"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/fleet"
)

// resolveLocalFleetBundleSpecs expands fleet declarations per local consumer,
// validates every destination against that consumer's source tree, and
// snapshots each canonical source path once for the invocation.
func resolveLocalFleetBundleSpecs(m *fleet.Manifest, manifestFile string, localSources map[string]string) (map[string]bundleBuildSpec, []string) {
	result := make(map[string]bundleBuildSpec, len(localSources))
	for slug, dir := range localSources {
		result[slug] = bundleBuildSpec{Dir: dir}
	}
	if m == nil || len(m.BundleFiles) == 0 {
		return result, nil
	}

	apps := make(map[string]fleet.AppEntry, len(m.Apps))
	for _, app := range m.Apps {
		apps[app.Slug] = app
	}
	perApp := make(map[string][]bundle.FileInputSpec)
	var problems []string
	manifestRoot := filepath.Dir(manifestFile)
	for _, declaration := range m.BundleFiles {
		for _, consumer := range declaration.Consumers {
			app, ok := apps[consumer]
			if !ok {
				// Structural validation already reports unknown consumers.
				continue
			}
			parsed, sourceProblem := fleet.ParseSource(app.Source, manifestRoot)
			if sourceProblem != nil {
				continue
			}
			if parsed.Kind == fleet.SourceGit {
				problems = append(problems, fmt.Sprintf(
					"app %q: bundle input %q uses a git+ source; shared bundle inputs support local app sources only in V1",
					consumer, declaration.From))
				continue
			}
			perApp[consumer] = append(perApp[consumer], bundle.FileInputSpec{
				From: declaration.From,
				To:   declaration.To,
			})
		}
	}

	resolvedByApp := make(map[string][]bundle.ResolvedFileInput, len(perApp))
	for _, app := range m.Apps {
		specs := perApp[app.Slug]
		if len(specs) == 0 {
			continue
		}
		dir, ok := localSources[app.Slug]
		if !ok {
			continue
		}
		resolved, err := bundle.ResolveFileInputs(manifestRoot, dir, specs)
		if err != nil {
			problems = append(problems, fmt.Sprintf("app %q: bundle inputs: %v", app.Slug, err))
			continue
		}
		resolvedByApp[app.Slug] = resolved
	}

	// Cache content by canonical source path, not destination. Repeating one
	// canonical file at multiple destinations still reads it only once.
	snapshotsBySource := make(map[string]bundle.FileInputSnapshot)
	for _, app := range m.Apps {
		resolved := resolvedByApp[app.Slug]
		if len(resolved) == 0 {
			continue
		}
		build := result[app.Slug]
		for _, input := range resolved {
			base, ok := snapshotsBySource[input.SourcePath]
			if !ok {
				snapshots, err := bundle.SnapshotFileInputs([]bundle.ResolvedFileInput{input})
				if err != nil {
					problems = append(problems, fmt.Sprintf("app %q: bundle inputs: %v", app.Slug, err))
					continue
				}
				base = snapshots[0]
				snapshotsBySource[input.SourcePath] = base
			}
			snapshot := base
			snapshot.From = input.From
			snapshot.To = input.To
			build.Inputs = append(build.Inputs, snapshot)
		}
		result[app.Slug] = build
	}
	return result, problems
}
