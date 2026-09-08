package config

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var envLookupLiteral = regexp.MustCompile(`(?:os\.Getenv|os\.LookupEnv)\(\s*"(SHINYHUB_[A-Z0-9_]*)"`)

// skippedScanDirs are directories with no Go source of ours in them.
var skippedScanDirs = map[string]bool{
	".git": true, ".claude": true, ".worktrees": true, "node_modules": true,
	"tmp": true, "bin": true, "data": true, "dist": true,
}

// The known-name list only helps if it matches the lookups it claims to
// describe. Rebuild it from the source so a new os.Getenv("SHINYHUB_...") that
// nobody adds to the list fails here rather than producing a warning about a
// variable that does in fact work.
func TestKnownEnvNamesMatchesEverySHINYHUBLookup(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skippedScanDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range envLookupLiteral.FindAllStringSubmatch(string(src), -1) {
			found[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 100 {
		t.Fatalf("scan found only %d SHINYHUB_ lookups; the scan, not the list, is what broke", len(found))
	}

	var missing, stale []string
	for name := range found {
		if !knownEnvSet[name] {
			missing = append(missing, name)
		}
	}
	for _, name := range knownEnvNames {
		if !found[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("read by the code but absent from knownEnvNames (setting one would warn even though it works): %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("in knownEnvNames but read nowhere (setting one is silently inert): %v", stale)
	}
}

// Names must also be sorted, so a hand edit lands in one predictable place.
func TestKnownEnvNamesIsSortedAndUnique(t *testing.T) {
	for i := 1; i < len(knownEnvNames); i++ {
		if knownEnvNames[i-1] >= knownEnvNames[i] {
			t.Fatalf("knownEnvNames is not sorted and unique at %q, %q", knownEnvNames[i-1], knownEnvNames[i])
		}
	}
}

func TestUnrecognizedEnvNames(t *testing.T) {
	cases := []struct {
		name        string
		environ     []string
		wantNames   []string
		wantSuggest map[string][]string
	}{
		{
			name:        "the natural guess for the listen port is named and corrected",
			environ:     []string{"SHINYHUB_PORT=9333"},
			wantNames:   []string{"SHINYHUB_PORT"},
			wantSuggest: map[string][]string{"SHINYHUB_PORT": {"SHINYHUB_SERVER_PORT"}},
		},
		{
			name:      "a name the code reads is not reported",
			environ:   []string{"SHINYHUB_SERVER_PORT=9333", "SHINYHUB_AUTH_SECRET=x"},
			wantNames: nil,
		},
		{
			name:      "variables outside the prefix are none of our business",
			environ:   []string{"PORT=9333", "PATH=/usr/bin", "SHINY_HUB_PORT=1"},
			wantNames: nil,
		},
		{
			name:        "a typo suggests the name it was a typo of",
			environ:     []string{"SHINYHUB_APP_ORGIN=https://apps.example.com"},
			wantNames:   []string{"SHINYHUB_APP_ORGIN"},
			wantSuggest: map[string][]string{"SHINYHUB_APP_ORGIN": {"SHINYHUB_APP_ORIGIN"}},
		},
		{
			name:        "a name resembling nothing is still reported, without a guess",
			environ:     []string{"SHINYHUB_QQQQQQQQQQQQQQQQQQ=1"},
			wantNames:   []string{"SHINYHUB_QQQQQQQQQQQQQQQQQQ"},
			wantSuggest: map[string][]string{"SHINYHUB_QQQQQQQQQQQQQQQQQQ": nil},
		},
		{
			name:      "a bare name with no value is not treated as a variable",
			environ:   []string{"SHINYHUB_NOT_A_VARIABLE"},
			wantNames: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UnrecognizedEnvNames(tc.environ)
			var names []string
			for _, u := range got {
				names = append(names, u.Name)
			}
			if strings.Join(names, ",") != strings.Join(tc.wantNames, ",") {
				t.Fatalf("names = %v, want %v", names, tc.wantNames)
			}
			for _, u := range got {
				want, ok := tc.wantSuggest[u.Name]
				if !ok {
					continue
				}
				if strings.Join(u.Suggest, ",") != strings.Join(want, ",") {
					t.Fatalf("suggestion for %s = %v, want %v", u.Name, u.Suggest, want)
				}
			}
		})
	}
}

// The end that matters: a server started with a misspelt variable has to say so
// in its own startup log, since that log is the only thing the operator reads
// before concluding the setting took effect.
func TestLoadWarnsAboutAnUnrecognizedEnvName(t *testing.T) {
	t.Setenv("SHINYHUB_PORT", "9333")
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 40))

	var log bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The variable really is inert: the server would listen on the default.
	if cfg.Server.Port != 8080 {
		t.Fatalf("server port = %d, want the untouched default 8080", cfg.Server.Port)
	}
	out := log.String()
	if !strings.Contains(out, "SHINYHUB_PORT") {
		t.Fatalf("startup log never names the variable that was set:\n%s", out)
	}
	if !strings.Contains(out, "SHINYHUB_SERVER_PORT") {
		t.Fatalf("startup log names the variable but not the one that works:\n%s", out)
	}
}

// The negative control: a correctly spelled environment must produce no warning
// at all, or the warning is noise operators learn to skip.
func TestLoadIsSilentWhenEveryEnvNameIsRecognized(t *testing.T) {
	t.Setenv("SHINYHUB_SERVER_PORT", "9333")
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 40))

	var log bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 9333 {
		t.Fatalf("server port = %d, want 9333", cfg.Server.Port)
	}
	if strings.Contains(log.String(), "unrecognized") {
		t.Fatalf("a correct environment produced a warning:\n%s", log.String())
	}
}
