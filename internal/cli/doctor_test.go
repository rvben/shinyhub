package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func doctorTestApp(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "hello-doctor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("# shiny app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func stubDoctorRuntime(t *testing.T) {
	t.Helper()
	previous := doctorLookPath
	doctorLookPath = func(name string) (string, error) {
		if name == "uv" {
			return "/opt/bin/uv", nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { doctorLookPath = previous })
}

func decodeDoctorReport(t *testing.T, raw string) doctorReport {
	t.Helper()
	var report doctorReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("decode doctor output %q: %v", raw, err)
	}
	return report
}

func doctorCheckNamed(t *testing.T, report doctorReport, name string) doctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("doctor report has no %q check: %+v", name, report.Checks)
	return doctorCheck{}
}

func TestDoctorLocalReadyProvidesRunCommand(t *testing.T) {
	isolatedCredentials(t)
	stubDoctorRuntime(t)
	dir := doctorTestApp(t)

	stdout, stderr, err := execCLISplit(t, "doctor", dir, "--local", "--output", "json")
	if err != nil {
		t.Fatalf("doctor --local: %v (stderr=%q)", err, stderr)
	}
	report := decodeDoctorReport(t, stdout)
	if report.Status != "ready" || report.Scope != "local" || report.Summary.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	if got := doctorCheckNamed(t, report, "local-runtime"); got.Status != "pass" || !strings.Contains(got.Detail, "/opt/bin/uv") {
		t.Errorf("local-runtime = %+v", got)
	}
	if len(report.NextSteps) != 1 || !strings.Contains(report.NextSteps[0], "shinyhub run") || !strings.Contains(report.NextSteps[0], "--check") {
		t.Errorf("next_steps = %v", report.NextSteps)
	}
}

func TestDoctorLocalMissingRuntimeNamesTheInstall(t *testing.T) {
	isolatedCredentials(t)
	previous := doctorLookPath
	doctorLookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { doctorLookPath = previous })

	stdout, _, err := execCLISplit(t, "doctor", doctorTestApp(t), "--local", "--output", "json")
	if err == nil {
		t.Fatal("missing uv must block local readiness")
	}
	report := decodeDoctorReport(t, stdout)
	check := doctorCheckNamed(t, report, "local-runtime")
	if check.Status != "fail" || !strings.Contains(check.Fix, "Install uv") {
		t.Errorf("local-runtime = %+v", check)
	}
}

func TestDoctorWithoutConnectionStillReportsLocalReadiness(t *testing.T) {
	isolatedCredentials(t)
	stubDoctorRuntime(t)
	dir := doctorTestApp(t)

	stdout, _, err := execCLISplit(t, "doctor", dir, "--output", "json")
	if err == nil {
		t.Fatal("doctor without a remote must exit non-zero")
	}
	report := decodeDoctorReport(t, stdout)
	if report.Status != "not_ready" || doctorCheckNamed(t, report, "entrypoint").Status != "pass" {
		t.Fatalf("local results were lost: %+v", report)
	}
	credential := doctorCheckNamed(t, report, "credentials")
	if credential.Status != "fail" || !strings.Contains(credential.Fix, "shinyhub connect") {
		t.Errorf("credential guidance = %+v", credential)
	}
	if doctorCheckNamed(t, report, "server").Status != "skip" {
		t.Error("server check should be skipped without credentials")
	}
}

func TestDoctorRemoteVerifiesExistingAppManagement(t *testing.T) {
	isolatedCredentials(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/server-info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"version":"1.7.0","runtimes":{"python":true,"r":false}}`)
	})
	mux.HandleFunc("/api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token shk_doctor" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"viewer"},"can_create_apps":false}`)
	})
	mux.HandleFunc("/api/apps/existing", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"app":{"slug":"existing"},"can_manage":true}`)
	})
	mux.HandleFunc("/api/apps/existing/schedules", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"items":[{"id":7,"name":"nightly","roll_feasibility_advisory":"Surge roll is currently infeasible; activation will defer."}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if err := saveConfig(&cliConfig{Host: srv.URL, Token: "shk_doctor"}); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := execCLISplit(t, "doctor", "--remote", "--slug", "existing", "--output", "json")
	if err != nil {
		t.Fatalf("doctor --remote: %v (stderr=%q)", err, stderr)
	}
	report := decodeDoctorReport(t, stdout)
	if report.Status != "ready" || report.Host != srv.URL {
		t.Fatalf("report = %+v", report)
	}
	permission := doctorCheckNamed(t, report, "deploy-permission")
	if permission.Status != "pass" || !strings.Contains(permission.Detail, "updates") {
		t.Errorf("permission = %+v", permission)
	}
	roll := doctorCheckNamed(t, report, "roll-feasibility")
	if roll.Status != "warn" || !strings.Contains(roll.Detail, "nightly") || !strings.Contains(roll.Detail, "infeasible") {
		t.Errorf("roll feasibility = %+v", roll)
	}
}

func TestDoctorRuntimeMismatchIsBlockingAndActionable(t *testing.T) {
	isolatedCredentials(t)
	stubDoctorRuntime(t)
	dir := doctorTestApp(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/server-info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"version":"1.7.0","runtimes":{"python":false,"r":true}}`)
	})
	mux.HandleFunc("/api/auth/me", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true}`)
	})
	mux.HandleFunc("/api/apps/hello-doctor", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if err := saveConfig(&cliConfig{Host: srv.URL, Token: "shk_doctor"}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := execCLISplit(t, "doctor", dir, "--output", "json")
	if err == nil {
		t.Fatal("runtime mismatch must fail doctor")
	}
	report := decodeDoctorReport(t, stdout)
	runtime := doctorCheckNamed(t, report, "remote-runtime")
	if runtime.Status != "fail" || !strings.Contains(runtime.Fix, "install uv") {
		t.Errorf("remote-runtime = %+v", runtime)
	}
}

func TestDoctorRejectsConflictingScopes(t *testing.T) {
	isolatedCredentials(t)
	_, _, err := execCLISplit(t, "doctor", "--local", "--remote")
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("error = %v", err)
	}
}

func TestDoctorRejectsInputsThatWouldBeIgnored(t *testing.T) {
	isolatedCredentials(t)
	for _, args := range [][]string{
		{"doctor", ".", "--remote"},
		{"doctor", "--local", "--slug", "demo"},
		{"doctor", "--local", "--host", "https://hub.example.com"},
		{"doctor", "--local", "--config", "/tmp/credentials.json"},
	} {
		_, _, err := execCLISplit(t, args...)
		if err == nil || !strings.Contains(err.Error(), "does not apply") {
			t.Errorf("%v error = %v, want ignored-input guidance", args, err)
		}
	}
}

func TestRemoteOnboardingJourneyConnectDoctorDeployAndDiagnose(t *testing.T) {
	isolatedCredentials(t)
	stubDoctorRuntime(t)
	dir := doctorTestApp(t)

	var mu sync.Mutex
	exists := false
	failDeploy := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/server-info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"version":"1.7.0","capabilities":{"cli_connect":true},"runtimes":{"python":true,"r":false}}`)
	})
	mux.HandleFunc("/api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token shk_journey" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"user":{"username":"clean-room","role":"developer"},"can_create_apps":true}`)
	})
	mux.HandleFunc("/api/apps", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		exists = true
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"slug":"hello-doctor"}`)
	})
	mux.HandleFunc("/api/apps/hello-doctor", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		present := exists
		mu.Unlock()
		if !present {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"app":{"slug":"hello-doctor","status":"running","deploy_count":1,"current_version":"v1"},"can_manage":true}`)
	})
	mux.HandleFunc("/api/apps/hello-doctor/deploy", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		fail := failDeploy
		mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"deploy failed: deliberate startup failure"}`)
			return
		}
		_, _ = io.WriteString(w, `{"slug":"hello-doctor","status":"running","deploy_count":1,"current_version":"v1"}`)
	})
	mux.HandleFunc("/api/apps/hello-doctor/logs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "booting\ndeliberate failure: missing DEMO_KEY\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	connectOut, connectErr, err := execCLISplit(t, "connect", srv.URL, "--token", "shk_journey", "--name", "clean", "--output", "json")
	if err != nil {
		t.Fatalf("connect: %v (stdout=%q stderr=%q)", err, connectOut, connectErr)
	}
	var connectResult map[string]any
	if json.Unmarshal([]byte(connectOut), &connectResult) != nil || connectResult["user"] != "clean-room" {
		t.Fatalf("connect result = %q", connectOut)
	}
	if strings.Contains(connectOut+connectErr, "shk_journey") {
		t.Fatal("connect output exposed the raw credential")
	}

	doctorOut, doctorErr, err := execCLISplit(t, "doctor", dir, "--output", "json")
	if err != nil {
		t.Fatalf("doctor: %v (stdout=%q stderr=%q)", err, doctorOut, doctorErr)
	}
	if report := decodeDoctorReport(t, doctorOut); report.Status != "ready" {
		t.Fatalf("doctor report = %+v", report)
	}
	if strings.Contains(doctorOut+doctorErr, "shk_journey") {
		t.Fatal("doctor output exposed the raw credential")
	}

	deployOut, deployErr, err := execCLISplit(t, "deploy", dir, "--wait", "--output", "json")
	if err != nil {
		t.Fatalf("deploy: %v (stdout=%q stderr=%q)", err, deployOut, deployErr)
	}
	var deployResult map[string]any
	if json.Unmarshal([]byte(deployOut), &deployResult) != nil || deployResult["status"] != "deployed" {
		t.Fatalf("deploy result = %q", deployOut)
	}

	mu.Lock()
	failDeploy = true
	mu.Unlock()
	_, failureOutput, err := execCLISplit(t, "deploy", dir, "--output", "json")
	if err == nil {
		t.Fatal("deliberately broken redeploy unexpectedly succeeded")
	}
	if !strings.Contains(failureOutput, "deliberate failure: missing DEMO_KEY") {
		t.Fatalf("failure did not inline actionable logs: %q", failureOutput)
	}

	info, statErr := os.Stat(configPath())
	if statErr != nil {
		t.Fatalf("stat credentials: %v", statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestInsecureRemoteHostOnlyFlagsNonLoopbackHTTP(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://example.com":    true,
		"http://10.0.0.8:8080":  true,
		"http://127.0.0.1:8080": false,
		"http://localhost:8080": false,
		"https://example.com":   false,
	} {
		if got := insecureRemoteHost(raw); got != want {
			t.Errorf("insecureRemoteHost(%q) = %v, want %v", raw, got, want)
		}
	}
}
