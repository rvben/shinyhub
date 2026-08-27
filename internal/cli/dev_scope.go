package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/fleet"
)

type devTarget struct {
	Slug          string
	Dir           string
	Visibility    string
	Manifest      string
	BundleInputs  []bundle.FileInputSpec
	ExternalFiles []string
}

type devScope struct {
	Manifest   string
	FleetID    string
	Targets    []devTarget
	SkippedGit []string
}

func (s *devScope) fleet() bool { return s != nil && s.Manifest != "" }

func resolveDevScope(source, explicitManifest string, manifestExplicit, standalone, all bool, selectedApps []string) (*devScope, error) {
	sourceDir, sourceManifest, err := normalizeDevSource(source)
	if err != nil {
		return nil, err
	}
	if standalone {
		if manifestExplicit || all || len(selectedApps) > 0 {
			return nil, validationErr("--standalone cannot be combined with --file, --all, or --app", "remove --standalone to use fleet context")
		}
		if sourceManifest != "" {
			return nil, validationErr("--standalone requires an app directory, not a fleet manifest", "pass the app directory or remove --standalone")
		}
		return &devScope{}, nil
	}
	if all && len(selectedApps) > 0 {
		return nil, validationErr("--all and --app are mutually exclusive", "omit --all or remove the explicit app selections")
	}

	for _, selected := range selectedApps {
		if strings.TrimSpace(selected) == "" {
			return nil, validationErr("--app requires a non-empty fleet app slug", "remove the empty --app value or name an app from fleet.toml")
		}
	}
	manifest := explicitManifest
	if sourceManifest != "" {
		if manifestExplicit {
			return nil, validationErr("a fleet manifest path and --file cannot be used together", "pass the fleet directory or remove --file")
		}
		manifest = sourceManifest
		manifestExplicit = true
	}
	if manifestExplicit {
		manifest, err = filepath.Abs(manifest)
		if err != nil {
			return nil, fmt.Errorf("resolve fleet manifest: %w", err)
		}
	} else {
		manifest = nearestFleetManifest(sourceDir)
	}
	if manifest == "" {
		if all || len(selectedApps) > 0 {
			return nil, validationErr("no fleet.toml found for "+sourceDir, "run from a fleet checkout, pass --file <manifest>, or omit the fleet selector")
		}
		return &devScope{}, nil
	}

	data, err := os.ReadFile(manifest)
	if err != nil {
		return nil, &ExitCodeError{Code: 1, Kind: KindValidation, Err: fmt.Errorf("read fleet manifest: %w", err)}
	}
	m, problems := fleet.ParseManifest(data, manifest)
	if len(problems) > 0 {
		messages := make([]string, 0, len(problems))
		for _, problem := range problems {
			messages = append(messages, problem.Error())
		}
		return nil, &ExitCodeError{Code: 1, Kind: KindValidation,
			Err: fmt.Errorf("invalid fleet manifest:\n%s", strings.Join(messages, "\n"))}
	}

	manifestRoot := filepath.Dir(manifest)
	canonicalSource, _ := canonicalExistingDirectory(sourceDir)
	atManifestRoot := sameExistingDirectory(sourceDir, manifestRoot)
	entries := make(map[string]fleet.AppEntry, len(m.Apps))
	localDirs := make(map[string]string, len(m.Apps))
	var matching []string
	for _, app := range m.Apps {
		entries[app.Slug] = app
		parsed, problem := fleet.ParseSource(app.Source, manifestRoot)
		if problem != nil {
			continue
		}
		if parsed.Kind != fleet.SourceLocal {
			continue
		}
		localDirs[app.Slug] = parsed.LocalPath
		canonicalApp, ok := canonicalExistingDirectory(parsed.LocalPath)
		if ok && canonicalApp == canonicalSource {
			matching = append(matching, app.Slug)
		}
	}

	requested := dedupeStrings(selectedApps)
	if len(requested) > 0 && !atManifestRoot && len(matching) == 1 {
		for _, slug := range requested {
			if slug != matching[0] {
				return nil, validationErr("directory is fleet app "+matching[0]+", but --app selects "+slug, "run from the fleet root to select a different app")
			}
		}
	}
	if all {
		for _, app := range m.Apps {
			requested = append(requested, app.Slug)
		}
	}
	if len(requested) == 0 && !all {
		switch {
		case !atManifestRoot && len(matching) == 1:
			requested = matching
		case !atManifestRoot && len(matching) > 1:
			return nil, validationErr("this directory is the source of multiple fleet apps: "+strings.Join(matching, ", "), "select one with --app <slug> or use --all")
		case !atManifestRoot && !manifestExplicit:
			// A directory beneath a fleet checkout is not automatically governed
			// unless the manifest actually declares it as an app source.
			return &devScope{}, nil
		default:
			for _, app := range m.Apps {
				requested = append(requested, app.Slug)
			}
		}
	}

	scope := &devScope{Manifest: manifest, FleetID: m.FleetID}
	for _, slug := range requested {
		entry, ok := entries[slug]
		if !ok {
			return nil, validationErr("app "+slug+" is not declared in "+manifest, "choose one of: "+strings.Join(fleetAppSlugs(m), ", "))
		}
		dir, local := localDirs[slug]
		if !local {
			if all || len(selectedApps) > 0 {
				return nil, validationErr("app "+slug+" uses a git+ source that cannot be watched", "point its manifest source at a local checkout, use --standalone from that checkout, or select only local-source apps")
			}
			scope.SkippedGit = append(scope.SkippedGit, slug)
			continue
		}
		inputs := fleetInputsForApp(m, slug)
		resolved, err := bundle.ResolveFileInputs(manifestRoot, dir, inputs)
		if err != nil {
			return nil, &ExitCodeError{Code: 1, Kind: KindValidation,
				Err: fmt.Errorf("app %q bundle inputs: %w", slug, err)}
		}
		external := make([]string, 0, len(resolved))
		for _, input := range resolved {
			external = append(external, input.SourcePath)
		}
		scope.Targets = append(scope.Targets, devTarget{
			Slug: slug, Dir: dir, Visibility: entry.Visibility, Manifest: manifest,
			BundleInputs: inputs, ExternalFiles: external,
		})
	}
	if len(scope.Targets) == 0 {
		return nil, validationErr("fleet "+m.FleetID+" has no local-source apps to develop", "check out an app locally or pass --standalone for an undeclared app directory")
	}
	return scope, nil
}

func normalizeDevSource(source string) (dir, manifest string, err error) {
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", "", fmt.Errorf("resolve development source: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", fmt.Errorf("development source: %w", err)
	}
	if info.IsDir() {
		return abs, "", nil
	}
	base := filepath.Base(abs)
	if base == defaultFleetManifest || base == legacyFleetManifest {
		return filepath.Dir(abs), abs, nil
	}
	return "", "", validationErr("development source must be an app directory or fleet.toml", "pass the directory containing the app or fleet manifest")
}

func nearestFleetManifest(sourceDir string) string {
	dir, ok := canonicalExistingDirectory(sourceDir)
	if !ok {
		return ""
	}
	for {
		path, found, _ := chooseFleetManifestInDir(dir, fileExists)
		if found {
			return path
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func sameExistingDirectory(a, b string) bool {
	ca, oka := canonicalExistingDirectory(a)
	cb, okb := canonicalExistingDirectory(b)
	return oka && okb && ca == cb
}

func fleetInputsForApp(m *fleet.Manifest, slug string) []bundle.FileInputSpec {
	var inputs []bundle.FileInputSpec
	for _, declaration := range m.BundleFiles {
		for _, consumer := range declaration.Consumers {
			if consumer == slug {
				inputs = append(inputs, bundle.FileInputSpec{From: declaration.From, To: declaration.To})
				break
			}
		}
	}
	return inputs
}

func fleetAppSlugs(m *fleet.Manifest) []string {
	result := make([]string, 0, len(m.Apps))
	for _, app := range m.Apps {
		result = append(result, app.Slug)
	}
	sort.Strings(result)
	return result
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
