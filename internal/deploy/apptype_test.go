package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBundleFiles creates a bundle directory holding exactly the named files.
// The contents are irrelevant to detection, which is why they are not
// parameterized: what is under test is which filenames make a directory a
// recognizable app.
func writeBundleFiles(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# fixture\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// The classic Shiny layout splits an app into ui.R and server.R with no app.R.
// It predates app.R, RStudio still scaffolds it, and shiny::runApp on a
// directory runs it exactly as it runs the single-file form. Detection used to
// look only for app.R, so a classic bundle was reported as containing no R app
// at all and could not be deployed without a hand-written [app] command.
//
// The negative cases are what stop this from passing on a detector that simply
// answers "r" more often: a ui.R on its own is not runnable, and a Python
// bundle must not become an R one because it happens to ship a server.R.
func TestDetectAppType_ClassicTwoFileLayout(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"classic ui.R and server.R", []string{"ui.R", "server.R"}, "r"},
		{"server.R alone", []string{"server.R"}, "r"},
		{"single file app.R", []string{"app.R"}, "r"},
		{"ui.R alone is not runnable", []string{"ui.R"}, ""},
		{"empty bundle", nil, ""},
		{"python wins over a classic R layout", []string{"app.py", "ui.R", "server.R"}, "python"},
		{"python alone", []string{"app.py"}, "python"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeBundleFiles(t, tc.files...)
			if got := DetectAppType(dir); got != tc.want {
				t.Errorf("DetectAppType(%v) = %q, want %q", tc.files, got, tc.want)
			}
		})
	}
}

// Detection feeds ResolveLaunch, which is the single path doctor, plan, run and
// dev all reach the entrypoint decision through. This pins the user-visible
// consequence rather than the helper: a classic bundle resolves to a real R
// launch command instead of failing with "no app entrypoint found".
func TestResolveLaunch_ClassicTwoFileLayoutLaunchesR(t *testing.T) {
	dir := writeBundleFiles(t, "ui.R", "server.R")

	plan, err := ResolveLaunch(dir, LaunchOptions{Port: 4000, BindHost: "127.0.0.1"})
	if err != nil {
		t.Fatalf("ResolveLaunch on a ui.R + server.R bundle: %v", err)
	}
	if plan.AppType != "r" {
		t.Errorf("AppType = %q, want %q", plan.AppType, "r")
	}
	joined := strings.Join(plan.Command, " ")
	// Two independent bounds: the command must be Rscript (not a Python
	// launcher that happens to carry the port) and must actually run the
	// bundle. runApp('.') is what makes the single command work for both R
	// layouts, so a fix that special-cased the classic layout into some other
	// entrypoint would fail here.
	if !strings.Contains(joined, "Rscript") {
		t.Errorf("command = %q, want it to launch Rscript", joined)
	}
	if !strings.Contains(joined, "runApp('.'") {
		t.Errorf("command = %q, want it to run the bundle directory via runApp", joined)
	}
}

// A bundle with no entrypoint in any layout still has to fail, and its message
// is the only place a user learns what would have been accepted. Leaving the
// old wording would send someone with ui.R and server.R off to write an app.R
// they do not need.
func TestResolveLaunch_MissingEntrypointNamesEveryAcceptedLayout(t *testing.T) {
	_, err := ResolveLaunch(t.TempDir(), LaunchOptions{Port: 4000, BindHost: "127.0.0.1"})
	if err == nil {
		t.Fatal("a bundle with no entrypoint must not resolve to a launch command")
	}
	for _, want := range []string{"app.py", "app.R", "server.R", "shinyhub.toml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q as an accepted entrypoint", err.Error(), want)
		}
	}
}

// A stray app.py silently takes precedence over a classic R bundle exactly as
// it does over an app.R one, so the ambiguity warning has to cover it. It names
// the R file actually present: telling the owner of a ui.R + server.R bundle to
// look at "app.R" describes a file they do not have.
func TestAppTypeWarnings_AmbiguousClassicBundle(t *testing.T) {
	dir := writeBundleFiles(t, "app.py", "ui.R", "server.R")

	warnings := AppTypeWarnings(dir)
	if len(warnings) != 1 {
		t.Fatalf("AppTypeWarnings = %v, want exactly one warning", warnings)
	}
	if !strings.Contains(warnings[0], "server.R") {
		t.Errorf("warning %q does not name the R entrypoint present in the bundle", warnings[0])
	}
	if strings.Contains(warnings[0], "app.R") {
		t.Errorf("warning %q names app.R, which this bundle does not contain", warnings[0])
	}
}
