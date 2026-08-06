package process_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

// bundle writes a bundle layout: each entry is a slash-separated relative path
// mapped to its contents.
func bundle(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const renvProfile = `source("renv/activate.R")` + "\n"

// TestInterposedRLibrary covers who owns the project library for each bundle
// layout. Getting this wrong is silent in both directions: interposing on an
// renv-activated bundle shadows renv's own library, and failing to interpose
// on a bare lockfile leaves renv restoring into a read-only system library.
func TestInterposedRLibrary(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "bare lockfile gets an interposed library",
			files: map[string]string{"app.R": "", "renv.lock": "{}"},
			want:  process.RProjectLibraryDir,
		},
		{
			name: "renv-activated bundle owns its own library",
			files: map[string]string{
				"app.R": "", "renv.lock": "{}",
				".Rprofile": renvProfile, "renv/activate.R": "# renv",
			},
			want: "",
		},
		{
			name:  "no lockfile means nothing is restored",
			files: map[string]string{"app.R": ""},
			want:  "",
		},
		{
			name: "activate.R nothing sources does not activate renv",
			files: map[string]string{
				"app.R": "", "renv.lock": "{}", "renv/activate.R": "# renv",
			},
			want: process.RProjectLibraryDir,
		},
		{
			name: "an unrelated .Rprofile does not activate renv",
			files: map[string]string{
				"app.R": "", "renv.lock": "{}",
				".Rprofile": "options(stringsAsFactors = FALSE)\n", "renv/activate.R": "# renv",
			},
			want: process.RProjectLibraryDir,
		},
		{
			name: "a profile sourcing activate.R without the file does not activate renv",
			files: map[string]string{
				"app.R": "", "renv.lock": "{}", ".Rprofile": renvProfile,
			},
			want: process.RProjectLibraryDir,
		},
		{
			// A hand-written profile can call renv::activate() against a
			// host-installed renv, with no activate.R in the bundle at all.
			// renv still selects the project library, so interposing would
			// restore into one library and launch against another.
			name: "a profile calling renv::activate() owns its own library",
			files: map[string]string{
				"app.R": "", "renv.lock": "{}",
				".Rprofile": "renv::activate()\n",
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := process.InterposedRLibrary(bundle(t, tc.files)); got != tc.want {
				t.Errorf("InterposedRLibrary = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEnsureInterposedRLibrary_CreatesDir pins the directory's existence, not
// just the returned name: under the build sandbox a write rule for a missing
// directory is silently dropped, so renv could not create it itself.
func TestEnsureInterposedRLibrary_CreatesDir(t *testing.T) {
	dir := bundle(t, map[string]string{"app.R": "", "renv.lock": "{}"})
	lib, err := process.EnsureInterposedRLibrary(dir)
	if err != nil {
		t.Fatalf("EnsureInterposedRLibrary: %v", err)
	}
	if lib != process.RProjectLibraryDir {
		t.Fatalf("lib = %q, want %q", lib, process.RProjectLibraryDir)
	}
	fi, err := os.Stat(filepath.Join(dir, lib))
	if err != nil || !fi.IsDir() {
		t.Fatalf("library dir not created: stat err = %v", err)
	}
}

// TestEnsureInterposedRLibrary_NoOpForRenvBundle keeps an renv-activated
// bundle byte-identical to what it was before this feature existed.
func TestEnsureInterposedRLibrary_NoOpForRenvBundle(t *testing.T) {
	dir := bundle(t, map[string]string{
		"app.R": "", "renv.lock": "{}",
		".Rprofile": renvProfile, "renv/activate.R": "# renv",
	})
	lib, err := process.EnsureInterposedRLibrary(dir)
	if err != nil {
		t.Fatalf("EnsureInterposedRLibrary: %v", err)
	}
	if lib != "" {
		t.Fatalf("lib = %q, want empty", lib)
	}
	if _, err := os.Stat(filepath.Join(dir, process.RProjectLibraryDir)); err == nil {
		t.Fatal("created a library directory in an renv-activated bundle")
	}
}

// TestRenvRestoreArgv_TargetsTheInterposedLibrary pins that restore is told
// where to install AND that the same library is on the search path, so it
// resolves already-installed packages against the library it writes to.
func TestRenvRestoreArgv_TargetsTheInterposedLibrary(t *testing.T) {
	argv := process.RenvRestoreArgv(process.RProjectLibraryDir)
	if len(argv) != 3 || argv[0] != "Rscript" || argv[1] != "-e" {
		t.Fatalf("argv = %q, want Rscript -e <expr>", argv)
	}
	expr := argv[2]
	libPaths := strings.Index(expr, ".libPaths(")
	restore := strings.Index(expr, "renv::restore(")
	switch {
	case libPaths < 0:
		t.Fatalf("expr does not set .libPaths(): %s", expr)
	case restore < 0:
		t.Fatalf("expr does not call renv::restore(): %s", expr)
	case libPaths > restore:
		// A .libPaths() call after restore() runs too late to affect it.
		t.Fatalf(".libPaths() comes after renv::restore(): %s", expr)
	}
	if !strings.Contains(expr, "library = '"+process.RProjectLibraryDir+"'") {
		t.Fatalf("restore is not pointed at the interposed library: %s", expr)
	}
	if !strings.Contains(expr, "'"+process.RProjectLibraryDir+"', .libPaths()") {
		t.Fatalf("the interposed library is not FIRST on the search path: %s", expr)
	}
}

// TestRenvRestoreArgv_NoLibraryLeavesRenvInCharge pins that an renv-activated
// bundle's restore is unchanged: naming a library would override the one
// renv's own activation selected.
func TestRenvRestoreArgv_NoLibraryLeavesRenvInCharge(t *testing.T) {
	expr := process.RenvRestoreArgv("")[2]
	if strings.Contains(expr, ".libPaths(") || strings.Contains(expr, "library =") {
		t.Fatalf("expr interferes with renv's own library selection: %s", expr)
	}
	if !strings.Contains(expr, "renv::restore(prompt = FALSE)") {
		t.Fatalf("expr = %q, want a plain renv::restore(prompt = FALSE)", expr)
	}
}

// TestRProjectLibraryDirIsRelative pins the property that makes one name
// correct on the native host, in a Docker bind mount at /app, and in a task
// definition: .libPaths() resolves it against the process working directory,
// which every runtime sets to the bundle.
func TestRProjectLibraryDirIsRelative(t *testing.T) {
	if filepath.IsAbs(process.RProjectLibraryDir) {
		t.Fatalf("RProjectLibraryDir = %q, want a bundle-relative name", process.RProjectLibraryDir)
	}
}

// TestRenvPolicyEnv_DisablesTheSandbox is the regression for the undeletable-app
// defect at its source. renv's sandbox is a mode 0555 tree, and it is created
// while R sources the project profile - before any -e expression runs - so the
// setting has to travel as environment. Verified against R 4.6.1: with this
// variable set, activating renv creates no sandbox at all.
func TestRenvPolicyEnv_DisablesTheSandbox(t *testing.T) {
	var found bool
	for _, kv := range process.RenvPolicyEnv() {
		if kv == "RENV_CONFIG_SANDBOX_ENABLED=FALSE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("RenvPolicyEnv = %q, want RENV_CONFIG_SANDBOX_ENABLED=FALSE", process.RenvPolicyEnv())
	}
}

// TestRLibPathsExpr_KeepsAmbientEntries pins that interposing PREPENDS.
// Replacing the search path would cut off every ambient library, including
// the one renv itself is installed in, so the restore could not even start.
func TestRLibPathsExpr_KeepsAmbientEntries(t *testing.T) {
	expr := process.RLibPathsExpr("lib")
	if !strings.Contains(expr, ".libPaths()") {
		t.Fatalf("expr = %q, want the existing .libPaths() carried over", expr)
	}
	if !strings.HasPrefix(expr, ".libPaths(c('lib', .libPaths())") {
		t.Fatalf("expr = %q, want lib prepended to the existing search path", expr)
	}
}

// TestRLibPathsExpr_QuotesTheLibrary keeps the generated R syntactically valid
// for any path this package might pass.
func TestRLibPathsExpr_QuotesTheLibrary(t *testing.T) {
	if got := process.RLibPathsExpr(`a'b\c`); !strings.Contains(got, `'a\'b\\c'`) {
		t.Fatalf("RLibPathsExpr = %q, want the quote and backslash escaped", got)
	}
}
