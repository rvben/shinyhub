package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// classicRBundle writes the layout every published Shiny tutorial starts from:
// ui.R and server.R side by side, with no app.R. renv.lock is included because
// an R bundle without one is rejected later in the deploy for a different
// reason, and this test is about the entrypoint decision only.
func classicRBundle(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "classic-r")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"ui.R":      "library(shiny)\nfluidPage(titlePanel(\"hi\"))\n",
		"server.R":  "function(input, output) {}\n",
		"renv.lock": "{\"R\": {\"Version\": \"4.3.1\"}, \"Packages\": {}}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// The reported symptom: `shinyhub doctor` on a classic two-file R bundle failed
// its entrypoint check with "no app.py or app.R found", so the layout looked
// unsupported. Doctor is where a developer goes first, and this pins the JSON
// body they read, not the helper underneath it.
func TestDoctorAcceptsClassicTwoFileRLayout(t *testing.T) {
	isolatedCredentials(t)
	previousLookPath := doctorLookPath
	doctorLookPath = func(name string) (string, error) {
		if name == "Rscript" {
			return "/usr/local/bin/Rscript", nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { doctorLookPath = previousLookPath })

	dir := classicRBundle(t)

	stdout, stderr, err := execCLISplit(t, "doctor", dir, "--local", "--output", "json")
	if err != nil {
		t.Fatalf("doctor --local on a ui.R + server.R bundle: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	report := decodeDoctorReport(t, stdout)
	entrypoint := doctorCheckNamed(t, report, "entrypoint")
	if entrypoint.Status != "pass" {
		t.Fatalf("entrypoint check = %+v, want pass for a classic ui.R + server.R bundle", entrypoint)
	}
	// Second bound: passing is not enough if it passed as the wrong runtime.
	// The detail names the resolved launch command, which must be the R one.
	if !strings.Contains(entrypoint.Detail, "Rscript") {
		t.Errorf("entrypoint detail = %q, want the resolved R launch command", entrypoint.Detail)
	}
}

// looksLikeRApp gates the pre-flight "this server has no R runtime" warning. A
// classic bundle needs R just as much as a single-file one does, so leaving it
// out sent exactly those deploys to a server that could not run them with no
// warning at all.
func TestLooksLikeRApp_RecognizesBothLayouts(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{"single file", []string{"app.R"}, true},
		{"classic two file", []string{"ui.R", "server.R"}, true},
		{"ui.R alone is not runnable", []string{"ui.R"}, false},
		{"python bundle", []string{"app.py"}, false},
		// The heuristic exists to avoid warning when uncertain: a bundle that
		// declares its own command may not launch R at all, and one carrying a
		// stray app.py deploys as Python. Both must stay excluded, or the fix
		// would have traded a missing warning for a wrong one.
		{"classic layout with a manifest", []string{"ui.R", "server.R", "shinyhub.toml"}, false},
		{"classic layout with a stray app.py", []string{"ui.R", "server.R", "app.py"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("# fixture\n"), 0o644); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			if got := looksLikeRApp(dir); got != tc.want {
				t.Errorf("looksLikeRApp(%v) = %v, want %v", tc.files, got, tc.want)
			}
		})
	}
}
