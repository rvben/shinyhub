package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/process"
)

// collectEvents returns a Progress sink plus an accessor for what it saw.
func collectEvents() (func(deployevent.Event), func() []deployevent.Event) {
	var mu sync.Mutex
	var got []deployevent.Event
	return func(e deployevent.Event) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, e)
		}, func() []deployevent.Event {
			mu.Lock()
			defer mu.Unlock()
			return append([]deployevent.Event(nil), got...)
		}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// F1: an inferred R bundle with no renv.lock has no way to obtain shiny, so the
// dependency phase must fail while it is still the dependency phase - naming
// renv.lock - instead of reporting "Dependencies ready" and letting the app die
// in the readiness window with a generic crash message.
func TestBuildEnvironment_RWithoutLockfileFailsInDependencyPhase(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.R", "shinyApp(ui, server)\n")

	var restored bool
	restore := SetSyncHooksForTest(
		func(context.Context, string, []string) error { return nil },
		func(context.Context, string, []string) error { restored = true; return nil },
	)
	defer restore()
	// No shiny anywhere: neither the bundle nor the host library can supply it.
	// Pinned, or the outcome would depend on whether the machine running the
	// test happens to have R and shiny installed.
	defer SetRShinyInstalledForTest(false)()

	sink, events := collectEvents()
	err := buildEnvironment(Params{Slug: "x", BundleDir: dir, Progress: sink}, "r", time.Second)
	if err == nil {
		t.Fatal("an R bundle with no renv.lock must fail the dependency phase, not report success")
	}
	if restored {
		t.Error("renv restore must not run when there is no lockfile to restore from")
	}
	if !strings.Contains(err.Error(), "renv.lock") {
		t.Errorf("error must name renv.lock so the fix is actionable, got %q", err.Error())
	}
	if got := deployfail.Classify(err); got != deployfail.BuildFailed {
		t.Errorf("classify = %q, want build_failed (%q)", got, err.Error())
	}
	if strings.Count(err.Error(), "renv restore:") != 1 {
		t.Errorf("want exactly one 'renv restore:' prefix, got %q", err.Error())
	}

	// The phase must end failed and must never have claimed readiness: the
	// "Dependencies ready" line is the specific lie this fix removes.
	var sawFailed bool
	for _, e := range events() {
		if e.Phase != "dependencies" {
			continue
		}
		if e.Status == deployevent.StatusCompleted {
			t.Errorf("dependency phase reported completed (%q) for a bundle where nothing was installed", e.Message)
		}
		if e.Status == deployevent.StatusFailed {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Error("dependency phase must emit a failed event so the CLI shows the cause under that phase")
	}
}

// The fail-fast is scoped to the missing-lockfile case: a bundle that ships one
// still builds normally.
func TestBuildEnvironment_RWithLockfileStillBuilds(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.R", "shinyApp(ui, server)\n")
	writeFile(t, dir, "renv.lock", `{"R":{"Version":"4.4.1"},"Packages":{}}`)

	var restored bool
	restore := SetSyncHooksForTest(
		func(context.Context, string, []string) error { return nil },
		func(context.Context, string, []string) error { restored = true; return nil },
	)
	defer restore()
	// The lockfile alone must carry this, so the host is pinned to the hostile
	// value: the bundle is the only thing that can supply shiny here.
	defer SetRShinyInstalledForTest(false)()

	sink, events := collectEvents()
	if err := buildEnvironment(Params{Slug: "x", BundleDir: dir, Progress: sink}, "r", time.Second); err != nil {
		t.Fatalf("a bundle with renv.lock must build: %v", err)
	}
	if !restored {
		t.Error("renv restore must run for a bundle that ships a lockfile")
	}
	var sawCompleted bool
	for _, e := range events() {
		if e.Phase == "dependencies" && e.Status == deployevent.StatusCompleted {
			sawCompleted = true
		}
	}
	if !sawCompleted {
		t.Error("a real restore must still report the dependency phase completed")
	}
}

// The other side of the fail-fast: RLibPathsExpr keeps the host's own R library
// on the search path behind the bundle's, so an operator who installed shiny
// server-wide has been deploying lockfile-free bundles successfully. A missing
// lockfile is therefore not the fault by itself, and rejecting one on a host
// that can satisfy it would break a working deployment.
func TestBuildEnvironment_RWithoutLockfileBuildsWhenTheHostHasShiny(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.R", "shinyApp(ui, server)\n")

	restore := SetSyncHooksForTest(
		func(context.Context, string, []string) error { return nil },
		func(context.Context, string, []string) error { return nil },
	)
	defer restore()
	defer SetRShinyInstalledForTest(true)()

	sink, events := collectEvents()
	if err := buildEnvironment(Params{Slug: "x", BundleDir: dir, Progress: sink}, "r", time.Second); err != nil {
		t.Fatalf("a host carrying shiny must keep deploying lockfile-free bundles: %v", err)
	}
	for _, e := range events() {
		if e.Phase == "dependencies" && e.Status == deployevent.StatusFailed {
			t.Fatalf("dependency phase failed on a host that can supply shiny: %q", e.Message)
		}
	}
}

// A bundle declaring its own [app] command manages its packages itself and is
// resolved with an empty appType, so it never reaches the R dependency build -
// the escape hatch the failure message points at has to actually work.
func TestResolveBundleCommand_ManifestCommandSkipsRLockfileCheck(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.R", "shinyApp(ui, server)\n")
	writeFile(t, dir, "shinyhub.toml", "[app]\ncommand = [\"Rscript\", \"-e\", \"1\"]\n")

	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	cmd, appType, err := resolveBundleCommand(Params{Slug: "x", BundleDir: dir}, m, true)
	if err != nil {
		t.Fatalf("a bundle with its own [app] command must resolve without a lockfile: %v", err)
	}
	if appType != "" {
		t.Errorf("appType = %q, want empty (declared command, no type detection)", appType)
	}
	if len(cmd) == 0 {
		t.Error("the declared command must be returned")
	}
}

// F3: a bundle carrying both entrypoints deploys as Python by first-match
// precedence. That decision must be stated, not silent.
func TestAppTypeWarnings_AmbiguousBundle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "app = None\n")
	writeFile(t, dir, "app.R", "shinyApp(ui, server)\n")

	warnings := AppTypeWarnings(dir)
	if len(warnings) != 1 {
		t.Fatalf("want exactly one warning for an ambiguous bundle, got %#v", warnings)
	}
	for _, want := range []string{"app.py", "app.R", "precedence"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning must mention %q, got %q", want, warnings[0])
		}
	}
	// The warning is advisory: precedence itself is unchanged, so bundles that
	// already deploy as Python keep doing so.
	if got := DetectAppType(dir); got != "python" {
		t.Errorf("DetectAppType = %q, want python (precedence must not change)", got)
	}
}

// An unambiguous bundle, and one that declares its own command, produce no
// warning - a warning that fires on every deploy is one nobody reads.
func TestAppTypeWarnings_QuietWhenUnambiguous(t *testing.T) {
	rOnly := t.TempDir()
	writeFile(t, rOnly, "app.R", "shinyApp(ui, server)\n")
	pyOnly := t.TempDir()
	writeFile(t, pyOnly, "app.py", "app = None\n")
	declared := t.TempDir()
	writeFile(t, declared, "app.py", "app = None\n")
	writeFile(t, declared, "app.R", "shinyApp(ui, server)\n")
	writeFile(t, declared, "shinyhub.toml", "[app]\ncommand = [\"Rscript\", \"-e\", \"1\"]\n")

	for name, dir := range map[string]string{
		"R only":           rOnly,
		"Python only":      pyOnly,
		"declared command": declared,
	} {
		if got := AppTypeWarnings(dir); len(got) != 0 {
			t.Errorf("%s: want no warnings, got %#v", name, got)
		}
	}
}

// The ambiguity warning reaches the deploy stream, so `shinyhub deploy` and the
// dashboard both show it rather than only the server log.
func TestResolveBundleCommand_ReportsAmbiguityToTheDeployStream(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "app = None\n")
	writeFile(t, dir, "app.R", "shinyApp(ui, server)\n")

	restore := SetSyncHooksForTest(
		func(context.Context, string, []string) error { return nil },
		func(context.Context, string, []string) error { return nil },
	)
	defer restore()
	restoreEnsure := SetEnsureProjectForTest(func(context.Context, string) error { return nil })
	defer restoreEnsure()

	sink, events := collectEvents()
	p := Params{Slug: "x", BundleDir: dir, Progress: sink, Preparation: PrepareRequired}
	if _, _, err := resolveBundleCommand(p, nil, true); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var warned bool
	for _, e := range events() {
		if e.Status == deployevent.StatusWarning && strings.Contains(e.Message, "app.R") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no warning event for an ambiguous bundle; events = %#v", events())
	}
}

// F5: renv's coloured output is embedded verbatim in a structured error, so the
// escape bytes must be gone before they land in JSON.
func TestStripANSI_RemovesRenvEscapes(t *testing.T) {
	raw := []byte("\x1b[?25l  (0/1) Downloading: shiny \x1b[31m✖\x1b[0m shiny 999.999.999\x1b[?25h\nError: failed to install \"shiny\"\n")
	got := string(process.StripANSI(raw))
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("escape byte survived: %q", got)
	}
	for _, want := range []string{"Downloading: shiny", "shiny 999.999.999", `Error: failed to install "shiny"`} {
		if !strings.Contains(got, want) {
			t.Errorf("stripping must keep the message text %q, got %q", want, got)
		}
	}
}

// The production R build path is sandboxedRSync, not process.SyncR, so the
// strip has to be applied there too. The build step is stubbed with the exact
// bytes renv emitted in the reported failure.
func TestSandboxedRSync_ErrorTextCarriesNoEscapes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "renv.lock", `{"R":{"Version":"4.4.1"},"Packages":{}}`)

	renvOutput := []byte("\x1b[?25l\r  (0/1) Downloading: shiny \x1b[31m✖\x1b[0m shiny 999.999.999\x1b[?25h\n" +
		"Error: failed to install \"shiny\"\n")
	var sawEnv []string
	prev := buildStepRunner
	buildStepRunner = func(_ context.Context, _ string, _ []string, appEnv []string) ([]byte, error) {
		sawEnv = appEnv
		return renvOutput, errors.New("exit status 1")
	}
	defer func() { buildStepRunner = prev }()

	err := sandboxedRSync(context.Background(), dir, []string{"NO_COLOR=", "FORCE_COLOR=1"})
	if err == nil {
		t.Fatal("a failing restore must return an error")
	}
	if strings.ContainsRune(err.Error(), 0x1b) {
		t.Fatalf("escape byte reached the structured error: %q", err.Error())
	}
	if !strings.Contains(err.Error(), `failed to install "shiny"`) {
		t.Fatalf("the cause must survive stripping, got %q", err.Error())
	}

	// Suppression at the source is layered after the app's own environment, so
	// an app-set NO_COLOR cannot put the escapes back (os/exec is
	// last-occurrence-wins).
	last := map[string]string{}
	for _, kv := range sawEnv {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			last[kv[:i]] = kv[i+1:]
		}
	}
	if last["NO_COLOR"] != "1" {
		t.Errorf("NO_COLOR = %q in the build env %v, want 1", last["NO_COLOR"], sawEnv)
	}
}

func TestBuildEnvironment_PlumberDoesNotRequireShiny(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "shinyhub.toml", `[app]
framework = "plumber"
`)
	writeFile(t, dir, "plumber.R", "# API routes\n")
	defer SetRShinyInstalledForTest(false)()
	var restored bool
	restore := SetSyncHooksForTest(
		func(context.Context, string, []string) error { return nil },
		func(context.Context, string, []string) error { restored = true; return nil },
	)
	defer restore()
	if err := buildEnvironment(Params{Slug: "api", BundleDir: dir}, "r", time.Second); err != nil {
		t.Fatalf("Plumber must not require Shiny: %v", err)
	}
	if !restored {
		t.Fatal("Plumber must retain R dependency preparation")
	}
}
