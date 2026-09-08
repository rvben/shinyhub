package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/deploy"
)

func TestDoctorContainerDoesNotRequireControlPlanePython(t *testing.T) {
	isolatedCredentials(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			w.Write([]byte(`{"version":"dev","protocol_version":2,"capabilities":{"runtime_capabilities":true},"runtimes":{"python":false,"r":false}}`))
		case "/api/auth/me":
			w.Write([]byte(`{"user":{"username":"test","role":"developer"},"can_create_apps":true}`))
		case "/api/apps/app":
			w.Write([]byte(`{"can_manage":true}`))
		case "/api/apps/app/capabilities":
			w.Write([]byte(`{"isolation":"multiplex","requires_host_runtime":false,"features":{"multiplex":{"supported":true}}}`))
		case "/api/apps/app/schedules":
			w.Write([]byte(`{"items":[]}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	if err := saveConfig(&cliConfig{Host: server.URL, Token: "shk_test"}); err != nil {
		t.Fatal(err)
	}
	checks, _ := runRemoteDoctor(nil, "app", "python")
	found := false
	for _, check := range checks {
		if check.Status == "fail" {
			t.Fatalf("unexpected failure=%+v", check)
		}
		if check.Name == "remote-runtime" {
			found = true
			if check.Status != "warn" || !strings.Contains(check.Detail, "container") {
				t.Fatalf("check=%+v", check)
			}
		}
	}
	if !found {
		t.Fatal("missing runtime diagnosis")
	}
}

func TestDoctorRuntimePreflight(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		producer, roll, malformed, missing, newApp bool
		want                                       string
	}{
		{name: "ordinary Docker app", want: "pass"},
		{name: "producer on Docker", producer: true, want: "fail"},
		{name: "activation on Docker", roll: true, want: "fail"},
		{name: "new app uses defaults", newApp: true, want: "pass"},
		{name: "missing feature fails closed", producer: true, missing: true, want: "fail"},
		{name: "broken response", malformed: true, want: "fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Token shk_test" {
					t.Error("missing credential")
				}
				if tc.newApp && strings.Contains(r.URL.Path, "/apps/") {
					w.WriteHeader(404)
					return
				}
				if tc.malformed {
					w.Write([]byte("broken"))
					return
				}
				features := map[string]any{"multiplex": map[string]any{"supported": true}, "deploy_producers": map[string]any{"supported": false, "reason": "native required", "remedy": "choose native"}, "data_activation": map[string]any{"supported": false, "reason": "native required", "remedy": "choose native"}}
				if tc.missing {
					delete(features, "deploy_producers")
				}
				json.NewEncoder(w).Encode(map[string]any{"isolation": "multiplex", "features": features})
			}))
			defer server.Close()
			m := &deploy.Manifest{}
			if tc.producer {
				m.Schedules = append(m.Schedules, deploy.ScheduleSpec{DeployTrigger: "bundle_change"})
			}
			if tc.roll {
				m.Schedules = append(m.Schedules, deploy.ScheduleSpec{OnSuccess: "roll"})
			}
			result := doctorRuntimeCapabilities(&cliConfig{Host: server.URL, Token: "shk_test"}, "app", m)
			if result.Status != tc.want {
				t.Fatalf("check=%+v", result)
			}
			if tc.newApp && calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestDoctorProjectsManifestIsolationWithoutWriting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Get("isolation") != "per_session" {
			t.Errorf("request=%s %s", r.Method, r.URL)
		}
		w.Write([]byte(`{"isolation":"per_session","features":{"per_session":{"supported":false,"reason":"clustered","remedy":"use multiplex"}}}`))
	}))
	defer server.Close()
	isolation := "per_session"
	m := &deploy.Manifest{App: deploy.AppSettings{Worker: &deploy.WorkerManifest{Isolation: &isolation}}}
	result := doctorRuntimeCapabilities(&cliConfig{Host: server.URL, Token: "shk_test"}, "app", m)
	if result.Status != "fail" || !strings.Contains(result.Detail, "clustered") {
		t.Fatalf("check=%+v", result)
	}
}
