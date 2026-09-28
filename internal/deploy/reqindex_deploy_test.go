package deploy

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/process"
)

// The host-side `uv sync` of a synthesized project must reach the index the
// bundle's requirements.txt names, with the credential resolved from the app's
// env, and --no-index as a flag since uv has no variable for it.
func TestSandboxedPythonSync_AppliesRequirementsIndex(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt",
		"--index-url https://__token__:${PYPI_TOKEN}@private.example/simple\n--no-index\nsix\n")
	writeFile(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	writeFile(t, dir, process.SynthesizedProjectMarker, "1\n")

	var gotArgv, gotEnv []string
	prev := buildStepRunner
	buildStepRunner = func(_ context.Context, _ string, argv []string, env []string) ([]byte, error) {
		gotArgv, gotEnv = argv, env
		return nil, nil
	}
	defer func() { buildStepRunner = prev }()

	if err := sandboxedPythonSync(context.Background(), dir, []string{"PYPI_TOKEN=s3cret"}); err != nil {
		t.Fatalf("sandboxedPythonSync: %v", err)
	}
	if want := []string{"uv", "sync", "--no-index"}; !reflect.DeepEqual(gotArgv, want) {
		t.Errorf("argv = %q, want %q", gotArgv, want)
	}
	want := "UV_DEFAULT_INDEX=https://__token__:s3cret@private.example/simple"
	if len(gotEnv) == 0 || gotEnv[len(gotEnv)-1] != want {
		t.Errorf("env = %q, want the app env followed by %q", gotEnv, want)
	}
	if gotEnv[0] != "PYPI_TOKEN=s3cret" {
		t.Errorf("the app's own env must still reach the step, got %q", gotEnv)
	}
}

// attrRecorder keeps each record's message with its attributes, so a test can
// read what a log line states.
type attrRecorder struct {
	mu   *sync.Mutex
	msgs *[]string
}

func (h attrRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (h attrRecorder) Handle(_ context.Context, r slog.Record) error {
	line := r.Message
	r.Attrs(func(a slog.Attr) bool { line += " " + a.String(); return true })
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.msgs = append(*h.msgs, line)
	return nil
}
func (h attrRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h attrRecorder) WithGroup(string) slog.Handler      { return h }

// The build log's index line states what the build resolves from, including
// the bundle's requirements options, with every credential masked.
func TestBuildEnvironment_LogsRequirementsIndex(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	prev := slog.Default()
	slog.SetDefault(slog.New(attrRecorder{mu: &mu, msgs: &msgs}))
	defer slog.SetDefault(prev)
	defer SetSyncHooksForTest(
		func(context.Context, string, []string) error { return nil },
		func(context.Context, string, []string) error { return nil },
	)()
	defer SetEnsureProjectForTest(func(context.Context, string, []string) error { return nil })()
	for _, k := range []string{"UV_DEFAULT_INDEX", "UV_INDEX", "UV_INDEX_URL", "UV_EXTRA_INDEX_URL", "UV_FIND_LINKS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt",
		"-i https://u:${PYPI_TOKEN}@private.example/simple\n"+
			"-f https://mirror.example/wheels\n-f https://w:${PYPI_TOKEN}@wheels.example/links\n--no-index\nsix\n")
	p := Params{Slug: "demo", BundleDir: dir,
		Manager: managerWithEnv(t, nil, []string{"PYPI_TOKEN=s3cret"}, nil)}
	if err := buildEnvironment(p, "python", time.Second); err != nil {
		t.Fatalf("buildEnvironment: %v", err)
	}
	mu.Lock()
	joined := strings.Join(msgs, "\n")
	mu.Unlock()
	for _, want := range []string{
		"UV_DEFAULT_INDEX=https://***@private.example/simple",
		"UV_FIND_LINKS=https://mirror.example/wheels,https://***@wheels.example/links",
		"--no-index",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("build log lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "s3cret") {
		t.Errorf("build log leaks the credential:\n%s", joined)
	}
}

// An author-shipped project owns its index configuration (in pyproject.toml);
// a requirements.txt shipped beside it changes nothing.
func TestSandboxedPythonSync_AuthorProjectUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt", "--index-url https://ignored.example/simple\n--no-index\n")
	writeFile(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")

	var gotArgv, gotEnv []string
	prev := buildStepRunner
	buildStepRunner = func(_ context.Context, _ string, argv []string, env []string) ([]byte, error) {
		gotArgv, gotEnv = argv, env
		return nil, nil
	}
	defer func() { buildStepRunner = prev }()

	if err := sandboxedPythonSync(context.Background(), dir, []string{"A=1"}); err != nil {
		t.Fatalf("sandboxedPythonSync: %v", err)
	}
	if !reflect.DeepEqual(gotArgv, []string{"uv", "sync"}) || !reflect.DeepEqual(gotEnv, []string{"A=1"}) {
		t.Errorf("argv=%q env=%q, want [uv sync] and the app env unchanged", gotArgv, gotEnv)
	}
}

// Requirements-mode launches install dependencies themselves, and
// `uv run --with-requirements` ignores the file's --no-index.
func TestBuildCommand_RequirementsModeHonoursNoIndex(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt", "--no-index\n--find-links wheels\nshiny\n")
	got := buildCommand(dir, 41000, 1, "127.0.0.1", nil, false)
	want := []string{
		"uv", "run", "--no-project", "--no-index", "--with-requirements", "requirements.txt",
		"shiny", "run", "app.py", "--host", "127.0.0.1", "--port", "41000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildCommand =\n  %q\nwant\n  %q", got, want)
	}
}

// A bundle whose requirements.txt names its own default index replaces the
// server's; the deploy says so, without the credential.
func TestResolveBundleCommand_WarnsWhenRequirementsReplaceTheIndex(t *testing.T) {
	t.Setenv("UV_INDEX_URL", "https://server.example/simple")
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "from shiny import App\n")
	writeFile(t, dir, "requirements.txt", "--index-url https://user:pass@private.example/simple\nshiny\n")

	var events []deployevent.Event
	p := Params{Slug: "demo", BundleDir: dir, Progress: func(e deployevent.Event) { events = append(events, e) }}
	if _, _, err := resolveBundleCommand(p, nil, false); err != nil {
		t.Fatalf("resolveBundleCommand: %v", err)
	}
	var warning string
	for _, e := range events {
		if e.Status == deployevent.StatusWarning && strings.Contains(e.Message, "private.example") {
			warning = e.Message
		}
	}
	if warning == "" {
		t.Fatalf("no index warning among %+v", events)
	}
	if !strings.Contains(warning, "UV_INDEX_URL") {
		t.Errorf("warning must name the replaced setting: %q", warning)
	}
	if strings.Contains(warning, "pass") {
		t.Errorf("warning leaks the credential: %q", warning)
	}

	// Negative control: the same index as the server's is not an override.
	t.Setenv("UV_INDEX_URL", "https://user:pass@private.example/simple")
	events = nil
	if _, _, err := resolveBundleCommand(p, nil, false); err != nil {
		t.Fatalf("resolveBundleCommand: %v", err)
	}
	for _, e := range events {
		if e.Status == deployevent.StatusWarning && strings.Contains(e.Message, "index") {
			t.Errorf("unexpected warning when nothing is replaced: %q", e.Message)
		}
	}
}

// A ${NAME} reference can expand a credential anywhere in a URL, such as a
// query-string token, where userinfo redaction does not reach. Every
// diagnostic that shows the index configuration masks what it expanded to.
func TestIndexDiagnostics_MaskExpandedSecrets(t *testing.T) {
	const token = "tok-9f8e7d"
	for _, k := range []string{"UV_DEFAULT_INDEX", "UV_INDEX", "UV_INDEX_URL", "UV_EXTRA_INDEX_URL", "UV_FIND_LINKS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "from shiny import App\n")
	writeFile(t, dir, "requirements.txt", "--index-url https://private.example/simple?token=${INDEX_TOKEN}\nsix\n")
	appEnv := []string{"INDEX_TOKEN=" + token}

	t.Run("build log", func(t *testing.T) {
		var mu sync.Mutex
		var msgs []string
		prev := slog.Default()
		slog.SetDefault(slog.New(attrRecorder{mu: &mu, msgs: &msgs}))
		defer slog.SetDefault(prev)
		defer SetSyncHooksForTest(
			func(context.Context, string, []string) error { return nil },
			func(context.Context, string, []string) error { return nil },
		)()
		defer SetEnsureProjectForTest(func(context.Context, string, []string) error { return nil })()
		p := Params{Slug: "demo", BundleDir: dir, Manager: managerWithEnv(t, nil, appEnv, nil)}
		if err := buildEnvironment(p, "python", time.Second); err != nil {
			t.Fatalf("buildEnvironment: %v", err)
		}
		mu.Lock()
		joined := strings.Join(msgs, "\n")
		mu.Unlock()
		if !strings.Contains(joined, "UV_DEFAULT_INDEX=https://private.example/simple?token=***") {
			t.Errorf("build log lacks the masked index:\n%s", joined)
		}
		if strings.Contains(joined, token) {
			t.Errorf("build log leaks the token:\n%s", joined)
		}
	})

	t.Run("override warning", func(t *testing.T) {
		t.Setenv("UV_INDEX_URL", "https://server.example/simple")
		var events []deployevent.Event
		p := Params{Slug: "demo", BundleDir: dir, Manager: managerWithEnv(t, nil, appEnv, nil),
			Progress: func(e deployevent.Event) { events = append(events, e) }}
		if _, _, err := resolveBundleCommand(p, nil, false); err != nil {
			t.Fatalf("resolveBundleCommand: %v", err)
		}
		var warning string
		for _, e := range events {
			if e.Status == deployevent.StatusWarning && strings.Contains(e.Message, "private.example") {
				warning = e.Message
			}
		}
		if !strings.Contains(warning, "token=***") {
			t.Errorf("no masked override warning among %+v", events)
		}
		for _, e := range events {
			if strings.Contains(e.Message, token) {
				t.Errorf("deploy event leaks the token: %q", e.Message)
			}
		}
	})

	t.Run("registry-miss hint", func(t *testing.T) {
		build, err := process.ReadRequirementsBuild(dir, appEnv)
		if err != nil {
			t.Fatal(err)
		}
		argv := []string{"sh", "-c", `echo "because private-package was not found in the package registry"; exit 1`}
		_, err = runSandboxedBuildStep(context.Background(), dir, argv, build.Env)
		if err == nil || !strings.Contains(err.Error(), "token=***") {
			t.Errorf("hint lacks the masked index: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), token) {
			t.Errorf("hint leaks the token: %v", err)
		}
	})
}

// A failed uv step's output can quote an index URL whose query or path carries
// a value a ${NAME} reference expanded (uv masks only URL userinfo), so the
// output is masked before it lands in the deploy error.
func TestSandboxedPythonSync_MasksExpandedSecretsInFailureOutput(t *testing.T) {
	const token = "zq-sync-9d41f"
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt", "--index-url https://private.example/simple?token=${INDEX_TOKEN}\nsix\n")
	writeFile(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	writeFile(t, dir, process.SynthesizedProjectMarker, "1\n")

	prev := buildStepRunner
	buildStepRunner = func(context.Context, string, []string, []string) ([]byte, error) {
		return []byte("error: Failed to fetch: `https://private.example/simple/six/?token=" + token + "`\n"), errors.New("exit status 2")
	}
	defer func() { buildStepRunner = prev }()

	err := sandboxedPythonSync(context.Background(), dir, []string{"INDEX_TOKEN=" + token})
	if err == nil {
		t.Fatal("sandboxedPythonSync succeeded on a failed step")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error leaks the token: %v", err)
	}
	if !strings.Contains(err.Error(), "simple/six/?token=***") {
		t.Errorf("error lost the masked output: %v", err)
	}
}

// A token written literally into an index URL is not an expanded value, but
// uv quotes the URL, query string included, when a step fails.
func TestSandboxedPythonSync_MasksLiteralQueryTokensInFailureOutput(t *testing.T) {
	const token = "zq-literal-3b8a"
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt", "--index-url https://private.example/simple?token="+token+"\nsix\n")
	writeFile(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	writeFile(t, dir, process.SynthesizedProjectMarker, "1\n")

	prev := buildStepRunner
	buildStepRunner = func(context.Context, string, []string, []string) ([]byte, error) {
		return []byte("error: Failed to fetch: `https://private.example/simple/six/?token=" + token + "`\n"), errors.New("exit status 2")
	}
	defer func() { buildStepRunner = prev }()

	err := sandboxedPythonSync(context.Background(), dir, nil)
	if err == nil {
		t.Fatal("sandboxedPythonSync succeeded on a failed step")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error leaks the token: %v", err)
	}
	if !strings.Contains(err.Error(), "simple/six/?token=***`") {
		t.Errorf("error lost the masked output: %v", err)
	}
}
