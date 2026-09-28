package process_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

// uvCall is one recorded invocation of the fake uv: its argv and the UV_*
// entries of its environment.
type uvCall struct {
	args []string
	env  map[string]string
}

// installFakeUV puts a recording `uv` first on PATH. It appends every
// invocation to a log, creates the pyproject.toml `uv init` would, and
// succeeds. The real uv is not needed: what these tests pin is what ShinyHub
// hands uv, and the uv-gated end-to-end test proves uv then honours it.
func installFakeUV(t *testing.T) func() []uvCall {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "uv.log")
	script := "#!/bin/sh\n" +
		"{ printf 'ARGS'; for a in \"$@\"; do printf ' %s' \"$a\"; done; printf '\\n'; env | grep '^UV_' | sort | sed 's/^/ENV /'; } >> '" + log + "'\n" +
		"if [ \"$1\" = init ]; then printf '[project]\\nname = \"x\"\\n' > pyproject.toml; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "uv"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, k := range []string{"UV_DEFAULT_INDEX", "UV_INDEX", "UV_INDEX_URL", "UV_EXTRA_INDEX_URL", "UV_FIND_LINKS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return func() []uvCall {
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("uv was never invoked: %v", err)
		}
		var calls []uvCall
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			switch {
			case strings.HasPrefix(line, "ARGS"):
				calls = append(calls, uvCall{args: strings.Fields(strings.TrimPrefix(line, "ARGS")), env: map[string]string{}})
			case strings.HasPrefix(line, "ENV ") && len(calls) > 0:
				k, v, _ := strings.Cut(strings.TrimPrefix(line, "ENV "), "=")
				calls[len(calls)-1].env[k] = v
			}
		}
		return calls
	}
}

func callsWithVerb(calls []uvCall, verb string) []uvCall {
	var out []uvCall
	for _, c := range calls {
		if len(c.args) > 0 && c.args[0] == verb {
			out = append(out, c)
		}
	}
	return out
}

// The conversion's `uv add` steps must resolve from the index the bundle's
// requirements.txt names, with the credential taken from the app's env, and
// must keep that credential out of argv. The per-app env reaches every step.
func TestEnsureProject_AppliesRequirementsIndex(t *testing.T) {
	calls := installFakeUV(t)
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt",
		"--index-url https://__token__:${PYPI_TOKEN}@private.example/simple\n"+
			"--extra-index-url https://extra.example/simple\n"+
			"shiny\n")

	appEnv := []string{"PYPI_TOKEN=s3cret", "UV_INDEX=https://app.example/simple"}
	if err := process.EnsureProject(context.Background(), dir, appEnv); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	adds := callsWithVerb(calls(), "add")
	if len(adds) != 2 {
		t.Fatalf("want `uv add -r` and `uv add pydantic`, got %d add calls", len(adds))
	}
	for _, c := range adds {
		if got := c.env["UV_DEFAULT_INDEX"]; got != "https://__token__:s3cret@private.example/simple" {
			t.Errorf("uv %v: UV_DEFAULT_INDEX = %q", c.args, got)
		}
		if got := c.env["UV_INDEX"]; got != "https://app.example/simple" {
			t.Errorf("uv %v: UV_INDEX = %q, want the app's own value", c.args, got)
		}
		if got := c.env["UV_EXTRA_INDEX_URL"]; got != "https://extra.example/simple" {
			t.Errorf("uv %v: UV_EXTRA_INDEX_URL = %q", c.args, got)
		}
		if strings.Contains(strings.Join(c.args, " "), "s3cret") {
			t.Errorf("credential in argv: %v", c.args)
		}
	}
}

// --no-index has no uv environment variable, so it travels as a flag; local
// --find-links locations travel as UV_FIND_LINKS.
func TestEnsureProject_NoIndexAndFindLinks(t *testing.T) {
	calls := installFakeUV(t)
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt", "--no-index\n--find-links ./wheels\nsix\n")

	if err := process.EnsureProject(context.Background(), dir, nil); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	adds := callsWithVerb(calls(), "add")
	if len(adds) != 1 {
		t.Fatalf("want one add call, got %d", len(adds))
	}
	if !strings.Contains(" "+strings.Join(adds[0].args, " ")+" ", " --no-index ") {
		t.Errorf("uv add args %v lack --no-index", adds[0].args)
	}
	if got := adds[0].env["UV_FIND_LINKS"]; got != "wheels" {
		t.Errorf("UV_FIND_LINKS = %q, want wheels", got)
	}
}

// An include that escapes the bundle fails the conversion before uv runs,
// rather than converting without the configuration it may carry.
func TestEnsureProject_IncludeOutsideBundleFailsClosed(t *testing.T) {
	installFakeUV(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "bundle")
	writeBundleFile(t, parent, "outside.txt", "-i https://outside.example/simple\n")
	writeBundleFile(t, dir, "requirements.txt", "-r ../outside.txt\nsix\n")

	if err := process.EnsureProject(context.Background(), dir, nil); err == nil {
		t.Fatal("want an error for an include outside the bundle")
	}
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); !os.IsNotExist(err) {
		t.Error("no project may be synthesized when the index configuration cannot be read")
	}
}

// A synthesized project locks against the bundle's index, so the sync that
// installs it must reach that index too, credentials included.
func TestSync_AppliesRequirementsIndexToSynthesizedProject(t *testing.T) {
	calls := installFakeUV(t)
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt", "-i https://private.example/simple\n--no-index\nsix\n")
	writeBundleFile(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	writeBundleFile(t, dir, process.SynthesizedProjectMarker, "1\n")

	if err := process.Sync(context.Background(), dir); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	syncs := callsWithVerb(calls(), "sync")
	if len(syncs) != 1 {
		t.Fatalf("want one sync call, got %d", len(syncs))
	}
	if got := syncs[0].env["UV_DEFAULT_INDEX"]; got != "https://private.example/simple" {
		t.Errorf("UV_DEFAULT_INDEX = %q", got)
	}
	if strings.Join(syncs[0].args, " ") != "sync --no-index" {
		t.Errorf("args = %v, want [sync --no-index]", syncs[0].args)
	}
}
