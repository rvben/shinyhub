package metrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// scrapeFresh returns the exposition text of a registry that has recorded
// nothing, which is exactly the state a just-started server is scraped in.
func scrapeFresh(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	New("test").Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", rec.Code)
	}
	return rec.Body.String()
}

// TestBoundedSeriesArePublishedAtZeroBeforeAnyActivity pins the difference
// between a metric that is zero and a metric that is absent. Absent is what an
// unseeded CounterVec exposes, and an alert on a failure counter cannot tell
// that apart from healthy, so it stays silent for exactly the build where the
// instrumentation is broken.
func TestBoundedSeriesArePublishedAtZeroBeforeAnyActivity(t *testing.T) {
	body := scrapeFresh(t)

	for _, spec := range boundedSeriesSpecs() {
		for _, v := range spec.Values {
			line := fmt.Sprintf("%s{%s=\"%s\"} 0", spec.Metric, spec.Label, v)
			if !strings.Contains(body, line) {
				t.Errorf("a freshly started server must already publish %q; without it an alert cannot distinguish zero from missing", line)
			}
		}
	}
}

// TestSeededValuesAreTheValuesTheCodeCanRecord walks the tree for literal
// recorder calls and fails when one passes a label value that is not seeded.
//
// The failure this catches is a new outcome added to a handler while the seed
// list stays behind: the new value's series then reappears only on first
// occurrence, which is the original defect returning through the back door for
// the one outcome most likely to matter.
func TestSeededValuesAreTheValuesTheCodeCanRecord(t *testing.T) {
	root := repoRoot(t)
	// Recorder methods whose label values are seeded, mapped to the seed list.
	recorders := map[string][]string{
		"RecordDeploy":                deployResults,
		"RecordGenerationHandoff":     generationHandoffOutcomes,
		"RecordStateTransition":       stateTransitionEvents,
		"RecordUsagePersistenceEvent": usagePersistenceResults,
		"RecordAutoscaleScale":        autoscaleDirections,
		"recordTransition":            stateTransitionEvents,
		"recordDeploy":                deployResults,
	}

	checked := 0
	for name, seeded := range recorders {
		re := regexp.MustCompile(name + `\("([a-z_]+)"`)
		for _, path := range goFiles(t, root) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for _, m := range re.FindAllStringSubmatch(string(src), -1) {
				checked++
				if !contains(seeded, m[1]) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s: %s(%q) records a value that is not seeded, so that series is absent until it first happens", rel, name, m[1])
				}
			}
		}
	}

	// A regex that matched nothing would pass forever, so prove the scan sees
	// the call sites it is judging.
	if checked < 8 {
		t.Fatalf("only %d literal recorder calls found; the scan is not matching the code it guards", checked)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("repo root %s has no go.mod: %v", dir, err)
	}
	return dir
}

func goFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "tmp", "bin", "data":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	return out
}
