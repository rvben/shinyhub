package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/protocol"
)

func TestDiagnoseCompatibilityCoversUpgradeDirections(t *testing.T) {
	tests := []struct {
		name       string
		cli        string
		server     string
		protocol   int
		wantLevel  compatibilityLevel
		wantDetail string
		wantFix    string
	}{
		{"same pre-1.0 minor", "v0.11.2", "v0.11.9", 1, compatibilityCompatible, "compatible", ""},
		{"older CLI newer server", "v0.11.9", "v0.12.0", 1, compatibilityWarning, "CLI v0.11.9 is older", "uv tool upgrade shinyhub"},
		{"newer CLI older server", "v0.12.0", "v0.11.9", 1, compatibilityWarning, "server v0.11.9 is older", "Upgrade the ShinyHub server"},
		{"stable major tolerates minor drift", "v1.4.0", "v1.9.0", 1, compatibilityCompatible, "compatible", ""},
		{"new server protocol", "v0.11.9", "v0.12.0", protocol.CurrentVersion + 1, compatibilityIncompatible, "newer than this CLI supports", "uv tool upgrade shinyhub"},
		{"legacy server remains capability gated", "v0.12.0", "v0.11.0", 0, compatibilityWarning, "capability-gated", "Upgrade the ShinyHub server"},
		{"dev build trusts matching protocol", "dev", "v0.12.0", 1, compatibilityCompatible, "share API protocol", ""},
		{"missing server version is visible", "v0.12.0", "", 1, compatibilityWarning, "does not report its version", "Upgrade the server"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := diagnoseCompatibility(tc.cli, serverInfo{Version: tc.server, ProtocolVersion: tc.protocol})
			if d.Level != tc.wantLevel || !strings.Contains(d.Detail, tc.wantDetail) || !strings.Contains(d.Fix, tc.wantFix) {
				t.Fatalf("diagnosis = %+v, want level=%s detail~%q fix~%q", d, tc.wantLevel, tc.wantDetail, tc.wantFix)
			}
		})
	}
}

func TestConnectRejectsNewerProtocolBeforeRequestingAuthorization(t *testing.T) {
	isolatedCredentials(t)
	meCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"v99.0.0","protocol_version":99,"capabilities":{"cli_connect":true}}`)
		case "/api/auth/me":
			meCalls++
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	cmd, _, progress := connectTestCommand()
	err := runConnect(cmd, []string{srv.URL}, &connectFlags{token: "shk_unused", timeout: defaultConnectTimeout})
	if err == nil || !strings.Contains(err.Error(), "newer than this CLI supports") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(hintOf(err), "uv tool upgrade shinyhub") {
		t.Errorf("hint = %q", hintOf(err))
	}
	if meCalls != 0 {
		t.Fatalf("incompatible connect made %d authenticated request(s)", meCalls)
	}
	if !strings.Contains(progress.String(), "ShinyHub v99.0.0 is ready") {
		t.Errorf("progress should still distinguish readiness from compatibility: %q", progress.String())
	}
}

func TestConnectWarnsButContinuesAcrossCompatibleReleaseDrift(t *testing.T) {
	isolatedCredentials(t)
	previous := version
	SetVersion("v0.11.0")
	t.Cleanup(func() { SetVersion(previous) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"v0.12.0","protocol_version":1,"runtimes":{"python":true}}`)
		case "/api/auth/me":
			_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true}`)
		}
	}))
	t.Cleanup(srv.Close)

	cmd, output, progress := connectTestCommand()
	if err := runConnect(cmd, []string{srv.URL}, &connectFlags{token: "shk_valid", timeout: defaultConnectTimeout}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(progress.String(), "CLI v0.11.0 is older") || !strings.Contains(progress.String(), "uv tool upgrade shinyhub") {
		t.Errorf("warning was not actionable:\n%s", progress.String())
	}
	for _, want := range []string{`"cli_version":"v0.11.0"`, `"server_version":"v0.12.0"`, `"compatibility":"warning"`} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("structured connect output missing %q: %s", want, output.String())
		}
	}
}

func TestProtocolHintPointsAtTheSideThatIsActuallyBehind(t *testing.T) {
	previous := version
	SetVersion("v0.12.0")
	t.Cleanup(func() { SetVersion(previous) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"version":"v0.11.0","protocol_version":1}`)
	}))
	t.Cleanup(srv.Close)

	hint := protocolHint(&cliConfig{Host: srv.URL})
	if !strings.Contains(hint, "Upgrade the ShinyHub server") || strings.Contains(hint, "upgrade the CLI to match") {
		t.Fatalf("protocol hint diagnosed the wrong side: %q", hint)
	}
}

func TestReportConnectCompatibilityPrintsOnlyWarnings(t *testing.T) {
	var out bytes.Buffer
	if err := reportConnectCompatibility(&out, compatibilityDiagnosis{Level: compatibilityCompatible, Detail: "fine"}); err != nil || out.Len() != 0 {
		t.Fatalf("compatible report = err %v output %q", err, out.String())
	}
	if err := reportConnectCompatibility(&out, compatibilityDiagnosis{Level: compatibilityWarning, Detail: "drift", Fix: "upgrade"}); err != nil || !strings.Contains(out.String(), "Fix: upgrade") {
		t.Fatalf("warning report = err %v output %q", err, out.String())
	}
}

func TestDoctorReportsAndStopsOnIncompatibleProtocol(t *testing.T) {
	isolatedCredentials(t)
	authCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"v99.0.0","protocol_version":99}`)
		case "/api/auth/me":
			authCalls++
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	if err := saveConfig(&cliConfig{Host: srv.URL, Token: "shk_doctor"}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := execCLISplit(t, "doctor", "--remote", "--output", "json")
	if err == nil {
		t.Fatal("doctor must block an unsupported protocol")
	}
	report := decodeDoctorReport(t, stdout)
	check := doctorCheckNamed(t, report, "version-compatibility")
	if check.Status != "fail" || !strings.Contains(check.Detail, "newer than this CLI supports") || !strings.Contains(check.Fix, "uv tool upgrade shinyhub") {
		t.Fatalf("version compatibility check = %+v", check)
	}
	if doctorCheckNamed(t, report, "authentication").Status != "skip" || authCalls != 0 {
		t.Fatalf("doctor used an incompatible authenticated API: calls=%d report=%+v", authCalls, report)
	}
}
