package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envValue(env []string, key string) (string, bool) {
	val, found := "", false
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			val, found = v, true
		}
	}
	return val, found
}

// A requirements-mode launch installs dependencies itself through
// `uv run --with-requirements`, which ignores the file's index options, so
// Start supplies them. An entry carrying a credential travels as a secret,
// and replaces any same-named entry in either slice so the runtime's
// Env-then-SecretEnv ordering cannot bring a stale value back.
func TestStart_AppliesRequirementsIndexToUVLaunch(t *testing.T) {
	for _, k := range []string{"UV_DEFAULT_INDEX", "UV_INDEX", "UV_INDEX_URL", "UV_EXTRA_INDEX_URL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	dir := t.TempDir()
	reqs := "--index-url https://__token__:${PYPI_TOKEN}@private.example/simple\n" +
		"--extra-index-url https://extra.example/simple\nshiny\n"
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(reqs), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	m.SetEnvResolver(func(string) ([]string, []string, error) {
		return []string{"UV_DEFAULT_INDEX=https://app.example/simple"},
			[]string{"PYPI_TOKEN=s3cret", "UV_EXTRA_INDEX_URL=https://app-secret.example/simple"}, nil
	})
	if _, err := m.Start(StartParams{
		Slug: "reqidx", Dir: dir, Port: 19931,
		Command: []string{"uv", "run", "--no-project", "--with-requirements", "requirements.txt", "shiny", "run", "app.py"},
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := rt.captured
	if p == nil {
		t.Fatal("runtime.Start was never called")
	}

	wantDefault := "https://__token__:s3cret@private.example/simple"
	if v, _ := envValue(p.SecretEnv, "UV_DEFAULT_INDEX"); v != wantDefault {
		t.Errorf("SecretEnv UV_DEFAULT_INDEX = %q, want %q", v, wantDefault)
	}
	if v, ok := envValue(p.Env, "UV_DEFAULT_INDEX"); ok {
		t.Errorf("Env still carries UV_DEFAULT_INDEX=%q; it must be removed so the secret entry is the only one", v)
	}
	// UV_EXTRA_INDEX_URL merges a value that came from SecretEnv, so it
	// stays secret, and exactly one entry remains.
	wantIndex := "https://app-secret.example/simple https://extra.example/simple"
	if v, _ := envValue(p.SecretEnv, "UV_EXTRA_INDEX_URL"); v != wantIndex {
		t.Errorf("SecretEnv UV_EXTRA_INDEX_URL = %q, want %q", v, wantIndex)
	}
	if v, ok := envValue(p.Env, "UV_EXTRA_INDEX_URL"); ok {
		t.Errorf("Env carries UV_EXTRA_INDEX_URL=%q, exposing a secret-derived value", v)
	}
	if n := strings.Count(strings.Join(p.SecretEnv, "\n"), "UV_EXTRA_INDEX_URL="); n != 1 {
		t.Errorf("SecretEnv holds %d UV_EXTRA_INDEX_URL entries, want 1: %q", n, p.SecretEnv)
	}
	for _, e := range p.Env {
		if strings.Contains(e, "s3cret") && !strings.HasPrefix(e, "PYPI_TOKEN=") {
			t.Errorf("credential reached plaintext Env: %q", e)
		}
	}
}

// A merged value stays secret whenever any part of it was secret: a key that
// came from SecretEnv, and a reference that expanded a secret's value, even
// where the credential is not URL userinfo (a signed URL's query token).
func TestStart_RequirementsIndexKeepsSecretsSecret(t *testing.T) {
	for _, k := range []string{"UV_DEFAULT_INDEX", "UV_INDEX", "UV_INDEX_URL", "UV_FIND_LINKS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	dir := t.TempDir()
	reqs := "--index-url https://private.example/simple?token=${SIGNING_TOKEN}\n" +
		"--find-links https://wheels.example/extra\n"
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(reqs), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	m.SetEnvResolver(func(string) ([]string, []string, error) {
		return nil, []string{"SIGNING_TOKEN=sig-abc123", "UV_FIND_LINKS=https://signed.example/w?sig=zzz"}, nil
	})
	if _, err := m.Start(StartParams{
		Slug: "reqsec", Dir: dir, Port: 19934,
		Command: []string{"uv", "run", "--no-project", "--with-requirements", "requirements.txt", "app.py"},
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := rt.captured
	for _, key := range []string{"UV_DEFAULT_INDEX", "UV_FIND_LINKS"} {
		if v, ok := envValue(p.Env, key); ok {
			t.Errorf("%s reached plaintext Env: %q", key, v)
		}
		if _, ok := envValue(p.SecretEnv, key); !ok {
			t.Errorf("%s missing from SecretEnv", key)
		}
	}
	if v, _ := envValue(p.SecretEnv, "UV_FIND_LINKS"); v != "https://signed.example/w?sig=zzz,https://wheels.example/extra" {
		t.Errorf("SecretEnv UV_FIND_LINKS = %q", v)
	}
}

// A literal signed URL in requirements.txt carries its credential in the
// query string, with no userinfo and no secret to match, and still travels as
// a secret rather than in a plaintext task override.
func TestStart_RequirementsIndexLiteralQueryTokenIsSecret(t *testing.T) {
	for _, k := range []string{"UV_DEFAULT_INDEX", "UV_INDEX", "UV_INDEX_URL", "UV_EXTRA_INDEX_URL", "UV_FIND_LINKS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	dir := t.TempDir()
	reqs := "--index-url https://index.example/simple?token=lit-q1\n--extra-index-url https://plain.example/simple\n"
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(reqs), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	if _, err := m.Start(StartParams{
		Slug: "reqlit", Dir: dir, Port: 19936,
		Command: []string{"uv", "run", "--with-requirements", "requirements.txt", "app.py"},
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := rt.captured
	if v, ok := envValue(p.Env, "UV_DEFAULT_INDEX"); ok {
		t.Errorf("UV_DEFAULT_INDEX reached plaintext Env: %q", v)
	}
	if v, _ := envValue(p.SecretEnv, "UV_DEFAULT_INDEX"); v != "https://index.example/simple?token=lit-q1" {
		t.Errorf("SecretEnv UV_DEFAULT_INDEX = %q", v)
	}
	if v, _ := envValue(p.Env, "UV_EXTRA_INDEX_URL"); v != "https://plain.example/simple" {
		t.Errorf("Env UV_EXTRA_INDEX_URL = %q, want the credential-free URL left in plaintext Env", v)
	}
}

// --no-index has no environment variable, so a uv launch whose requirements
// set it gets the flag on its command line: a manifest's own `uv run`
// command reaches Start without passing through the default command builder.
// uv rejects the flag twice, so a command that already has it is unchanged.
func TestRequirementsLaunch_NoIndexReachesTheCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("--no-index\n--find-links wheels\nsix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{"custom uv run", []string{"uv", "run", "--with-requirements", "requirements.txt", "python", "app.py"},
			[]string{"uv", "run", "--no-index", "--with-requirements", "requirements.txt", "python", "app.py"}},
		{"already present", []string{"/usr/bin/uv", "run", "--no-project", "--no-index", "app.py"},
			[]string{"/usr/bin/uv", "run", "--no-project", "--no-index", "app.py"}},
		{"not uv", []string{"python", "app.py"}, []string{"python", "app.py"}},
		{"global flag before run", []string{"uv", "--offline", "run", "app.py"},
			[]string{"uv", "--offline", "run", "--no-index", "app.py"}},
		{"global option with a value", []string{"uv", "--directory", "run", "-q", "run", "app.py"},
			[]string{"uv", "--directory", "run", "-q", "run", "--no-index", "app.py"}},
		{"global option with an attached value", []string{"uv", "--color=never", "run", "app.py"},
			[]string{"uv", "--color=never", "run", "--no-index", "app.py"}},
		// Only uv's own options count: the same flag in the app's arguments
		// belongs to the app.
		{"app argument of the same name", []string{"uv", "run", "python", "app.py", "--no-index"},
			[]string{"uv", "run", "--no-index", "python", "app.py", "--no-index"}},
		{"after the separator", []string{"uv", "run", "--", "app.py", "--no-index"},
			[]string{"uv", "run", "--no-index", "--", "app.py", "--no-index"}},
		{"after a run option value", []string{"uv", "run", "-p", "3.12", "--with-requirements", "requirements.txt", "--no-index", "app.py"},
			[]string{"uv", "run", "-p", "3.12", "--with-requirements", "requirements.txt", "--no-index", "app.py"}},
	}
	for _, c := range cases {
		argv, _, err := RequirementsLaunch(dir, c.argv, nil)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if strings.Join(argv, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s: argv = %q, want %q", c.name, argv, c.want)
		}
	}
	// A uv launch that is not `uv run` has no place for the flag, so it
	// fails rather than resolving from a registry the bundle disabled.
	for _, argv := range [][]string{{"uv", "tool", "run", "app"}, {"uv", "--offline", "tool", "run", "app"}, {"uv", "--project", "run"}} {
		if _, _, err := RequirementsLaunch(dir, argv, nil); err == nil {
			t.Errorf("%q: want an error for a uv launch that cannot take --no-index", argv)
		}
	}

	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	if _, err := m.Start(StartParams{Slug: "noidx", Dir: dir, Port: 19935,
		Command: []string{"uv", "run", "--with-requirements", "requirements.txt", "app.py"}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := rt.captured.Command; len(got) < 3 || got[2] != "--no-index" {
		t.Errorf("Start launched %q without --no-index", got)
	}
}

// Only a uv launch reads requirements.txt; any other command's env is left as
// the resolver and deploy composed it.
func TestStart_NonUVLaunchIgnoresRequirementsIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("--index-url https://private.example/simple\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	if _, err := m.Start(StartParams{Slug: "plain", Dir: dir, Port: 19932, Command: []string{"python", "app.py"}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if v, ok := envValue(append(rt.captured.Env, rt.captured.SecretEnv...), "UV_DEFAULT_INDEX"); ok {
		t.Errorf("a non-uv launch got UV_DEFAULT_INDEX=%q", v)
	}
}

// An include that leaves the bundle fails the launch rather than starting
// without the configuration it may carry.
func TestStart_RequirementsIncludeOutsideBundleFailsClosed(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "bundle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("-r ../outside.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("six\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(t.TempDir(), &captureRuntime{})
	if _, err := m.Start(StartParams{Slug: "escape", Dir: dir, Port: 19933, Command: []string{"uv", "run", "app.py"}}); err == nil {
		t.Fatal("want an error for an include outside the bundle")
	}
}
