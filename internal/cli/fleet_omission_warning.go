package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rvben/shinyhub/internal/fleet"
)

type fleetOmissionCommand string

const (
	fleetOmissionRun    fleetOmissionCommand = "run"
	fleetOmissionPlan   fleetOmissionCommand = "plan"
	fleetOmissionDeploy fleetOmissionCommand = "deploy"
)

type fleetCompositionOmission struct {
	Manifest     string
	Consumers    []string
	Destinations []string
}

// discoverFleetCompositionOmission is deliberately advisory. Any filesystem
// or manifest problem yields no result: plain commands keep their established
// behavior, while a valid nearest-parent fleet manifest can explain that the
// selected source normally receives extra bundle files.
func discoverFleetCompositionOmission(sourceDir string) *fleetCompositionOmission {
	canonicalSource, ok := canonicalExistingDirectory(sourceDir)
	if !ok {
		return nil
	}

	manifestFile := ""
	for dir := canonicalSource; ; dir = filepath.Dir(dir) {
		var inspectErr error
		path, found, _ := chooseFleetManifestInDir(dir, func(candidate string) bool {
			info, err := os.Stat(candidate)
			if err == nil {
				return !info.IsDir()
			}
			if !os.IsNotExist(err) {
				inspectErr = err
			}
			return false
		})
		if inspectErr != nil {
			return nil
		}
		if found {
			manifestFile = path
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
	}

	data, err := os.ReadFile(manifestFile)
	if err != nil {
		return nil
	}
	m, problems := fleet.ParseManifest(data, manifestFile)
	if m == nil || len(problems) != 0 || len(m.BundleFiles) == 0 {
		return nil
	}

	destinationsByConsumer := make(map[string]map[string]bool)
	for _, declaration := range m.BundleFiles {
		for _, consumer := range declaration.Consumers {
			if destinationsByConsumer[consumer] == nil {
				destinationsByConsumer[consumer] = make(map[string]bool)
			}
			destinationsByConsumer[consumer][declaration.To] = true
		}
	}

	manifestDir := filepath.Dir(manifestFile)
	consumerSet := make(map[string]bool)
	destinationSet := make(map[string]bool)
	for _, app := range m.Apps {
		destinations := destinationsByConsumer[app.Slug]
		if len(destinations) == 0 {
			continue
		}
		parsed, problem := fleet.ParseSource(app.Source, manifestDir)
		if problem != nil || parsed.Kind != fleet.SourceLocal {
			continue
		}
		canonicalApp, ok := canonicalExistingDirectory(parsed.LocalPath)
		if !ok || canonicalApp != canonicalSource {
			continue
		}
		consumerSet[app.Slug] = true
		for destination := range destinations {
			destinationSet[destination] = true
		}
	}
	if len(consumerSet) == 0 {
		return nil
	}

	result := &fleetCompositionOmission{Manifest: manifestFile}
	for consumer := range consumerSet {
		result.Consumers = append(result.Consumers, consumer)
	}
	for destination := range destinationSet {
		result.Destinations = append(result.Destinations, destination)
	}
	sort.Strings(result.Consumers)
	sort.Strings(result.Destinations)
	return result
}

func canonicalExistingDirectory(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return filepath.Clean(canonical), true
}

func warnFleetCompositionOmission(w io.Writer, sourceDir string, command fleetOmissionCommand, quiet bool) {
	if quiet {
		return
	}
	omission := discoverFleetCompositionOmission(sourceDir)
	if omission == nil {
		return
	}
	replacement := fleetCompositionReplacement(command, omission)
	fmt.Fprintf(w,
		"warning: shinyhub %s omits fleet bundle inputs for consumers %s declared in %s (destinations: %s); use `%s`\n",
		command, strings.Join(omission.Consumers, ", "), omission.Manifest,
		strings.Join(omission.Destinations, ", "), replacement)
}

func fleetCompositionReplacement(command fleetOmissionCommand, omission *fleetCompositionOmission) string {
	manifest := shellQuote(omission.Manifest)
	switch command {
	case fleetOmissionRun:
		slug := "<app>"
		if len(omission.Consumers) == 1 {
			slug = omission.Consumers[0]
		}
		return "shinyhub fleet dev " + slug + " -f " + manifest
	case fleetOmissionPlan:
		return "shinyhub fleet plan -f " + manifest
	default:
		return "shinyhub fleet apply -f " + manifest
	}
}
