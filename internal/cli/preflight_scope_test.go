package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightRespectsCredentialAppScope(t *testing.T) {
	for _, tc := range []struct {
		name, scope string
		allowed     bool
	}{
		{"outside allowlist", `,"app_scope":["other"]`, false},
		{"inside allowlist", `,"app_scope":["demo"]`, true},
		{"legacy unrestricted", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedCredentials(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Fatalf("preflight mutated server: %s", r.Method)
				}
				switch r.URL.Path {
				case "/api/server-info":
					fmt.Fprint(w, `{"version":"dev","capabilities":{"plan_apply":true,"content_digest":true},"runtimes":{"python":true}}`)
				case "/api/auth/me":
					fmt.Fprintf(w, `{"user":{"username":"test","role":"developer"},"can_create_apps":true%s}`, tc.scope)
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			if err := saveConfig(&cliConfig{Host: server.URL, Token: "shk_fake"}); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, err := execCLISplit(t, "doctor", "--remote", "--slug", "demo", "--output", "json")
			report := decodeDoctorReport(t, stdout)
			check := doctorCheckNamed(t, report, "deploy-permission")
			if tc.allowed {
				if err != nil || check.Status != "pass" {
					t.Fatalf("doctor: %+v %v %s", check, err, stderr)
				}
			} else if ExitCode(err) != 3 || check.Status != "fail" || !strings.Contains(check.Detail, "outside this credential") {
				t.Fatalf("doctor: %+v %v", check, err)
			}
			artifact := filepath.Join(t.TempDir(), "deployment.plan")
			_, _, err = execCLISplit(t, "plan", planTestBundle(t), "--slug", "demo", "--out", artifact, "--output", "json")
			if tc.allowed {
				if err != nil {
					t.Fatalf("plan: %v", err)
				}
			} else {
				_, code := classify(err)
				if code != 3 || !strings.Contains(err.Error(), "outside this credential") {
					t.Fatalf("plan: %v", err)
				}
				if _, err := os.Stat(artifact); !os.IsNotExist(err) {
					t.Fatal("unauthorized plan artifact was saved")
				}
			}
		})
	}
}
