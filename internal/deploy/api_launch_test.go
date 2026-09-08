package deploy

import (
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func apiLaunchFixture(t *testing.T, framework, entry string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"), []byte("[app]\nframework = \""+framework+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if entry != "" {
		if err := os.WriteFile(filepath.Join(dir, entry), []byte("# API fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if framework == "fastapi" {
		if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("fastapi\nuvicorn\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestManagedFastAPILaunch(t *testing.T) {
	dir := apiLaunchFixture(t, "fastapi", "app.py")
	os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("fastapi\nuvicorn\n"), 0600)
	plan, err := ResolveLaunch(dir, LaunchOptions{Port: 8042, BindHost: "0.0.0.0", AppPath: "/app/private-preview", PrepHostDeps: true, Reload: true})
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(plan.Command, " ")
	if !strings.Contains(cmd, "uv run --no-project --with-requirements requirements.txt uvicorn app:app --host 0.0.0.0 --port 8042 --root-path /app/private-preview --reload") {
		t.Fatalf("command: %s", cmd)
	}
	if plan.AppType != "python" || plan.ReadyPath != "/openapi.json" || len(plan.DepPrep) != 2 {
		t.Fatalf("plan: %+v", plan)
	}
	if DetectAppType(dir) != "python" {
		t.Fatal("wrong runtime")
	}
}

func TestManagedFastAPIPreservesFrozenProjectEnvironment(t *testing.T) {
	dir := apiLaunchFixture(t, "fastapi", "app.py")
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname='api'\nversion='0.1.0'\n"), 0600)
	os.WriteFile(filepath.Join(dir, "uv.lock"), []byte("# fixture"), 0600)
	plan, err := ResolveLaunch(dir, LaunchOptions{Port: 8042, CommandHostDeps: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.Join(plan.Command, " "), "uv run --frozen --no-sync uvicorn app:app") {
		t.Fatalf("command: %v", plan.Command)
	}
}

func TestManagedPlumberLaunch(t *testing.T) {
	dir := apiLaunchFixture(t, "plumber", "plumber.R")
	plan, err := ResolveLaunch(dir, LaunchOptions{Port: 8042, BindHost: "0.0.0.0", AppPath: "/app/r-api", PrepHostDeps: true, Reload: true})
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(plan.Command, " ")
	for _, want := range []string{"Rscript", "--no-site-file", "--no-environ", "plumber::pr('plumber.R')", `options(plumber.apiURL="/app/r-api")`, "host='0.0.0.0'", "port=8042", "docs=TRUE"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %s in %s", want, cmd)
		}
	}
	if strings.Contains(cmd, "shiny::runApp") {
		t.Fatal("launched Plumber as Shiny")
	}
	if plan.AppType != "r" || plan.ReadyPath != "/openapi.json" || len(plan.DepPrep) != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	if DetectAppType(dir) != "r" {
		t.Fatal("wrong runtime")
	}
}

func TestManagedAPIRejectsMissingEntrypointsAndConflictingCommands(t *testing.T) {
	for _, framework := range []string{"fastapi", "plumber"} {
		dir := apiLaunchFixture(t, framework, "")
		if _, err := ResolveLaunch(dir, LaunchOptions{Port: 8042}); err == nil || !strings.Contains(err.Error(), "requires") {
			t.Fatalf("missing entrypoint: %v", err)
		}
		m, _ := LoadManifest(dir)
		if _, _, err := resolveBundleCommand(Params{BundleDir: dir}, m, false); err == nil {
			t.Fatal("server preparation accepted missing API entrypoint")
		}
		os.WriteFile(filepath.Join(dir, "shinyhub.toml"), []byte("[app]\nframework='"+framework+"'\ncommand=['python','app.py']\n"), 0600)
		if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("conflict: %v", err)
		}
	}
	dir := apiLaunchFixture(t, "typo", "app.py")
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("accepted unknown framework")
	}
}

func TestManagedAPIReadinessOverride(t *testing.T) {
	dir := apiLaunchFixture(t, "fastapi", "app.py")
	os.WriteFile(filepath.Join(dir, "shinyhub.toml"), []byte("[app]\nframework='fastapi'\nreadiness_path='/healthz'\nreadiness_status=204\n"), 0600)
	plan, err := ResolveLaunch(dir, LaunchOptions{Port: 8042})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ReadyPath != "/healthz" || plan.ReadyStatus != 204 {
		t.Fatalf("readiness: %+v", plan)
	}
}

func TestManagedAPIResumeUsesOpenAPIReadiness(t *testing.T) {
	dir := apiLaunchFixture(t, "fastapi", "app.py")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi.json" {
			t.Errorf("resume used %s instead of API readiness", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"openapi":"3.1.0"}`))
	}))
	defer server.Close()
	fake := &resumeFakeRuntime{resumeEP: process.ReplicaEndpoint{URL: server.URL, Provider: "fake", Handle: process.RunHandle{PID: 9}}}
	mgr := startAndSuspend(t, fake)
	prx := proxy.New()
	prx.SetPoolSize("app", 1)
	if _, err := ResumeReplica(Params{Slug: "app", BundleDir: dir, Manager: mgr, Proxy: prx}, 0); err != nil {
		t.Fatalf("resume API: %v", err)
	}
}
