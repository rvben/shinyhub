package proxy

import (
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/envui"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnvironmentInjection(t *testing.T) {
	shell := `<!doctype html><html><head><title>FinOps &amp; costs</title><link rel="icon" href="/authored.ico"></head><body>App</body></html>`
	for _, tc := range []struct {
		name, policy, meta string
		script, icon       bool
	}{
		{"normal", "default-src 'self'; style-src 'self'", "", true, true},
		{"scripts refused", "script-src 'none'", "", false, true},
		{"images refused", "script-src 'self'; img-src 'none'", "", true, false},
		{"meta refused", "", `<meta http-equiv="Content-Security-Policy" content="default-src 'self'">`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New()
			p.SetEnvironment(&config.EnvironmentConfig{Label: "Acceptance", ProductionURL: "https://production.example"})
			resp := htmlResponse("finops", strings.Replace(shell, "<head>", "<head>"+tc.meta, 1))
			resp.Header.Set("Content-Security-Policy", tc.policy)
			if err := p.modifyResponseFor("finops")(resp); err != nil {
				t.Fatal(err)
			}
			body := readBody(t, resp)
			if !strings.Contains(body, "[Acceptance] FinOps &amp; costs") {
				t.Fatalf("title absent: %s", body)
			}
			if strings.Contains(body, "shinyhub-environment-loader") != tc.script {
				t.Fatal("script admission")
			}
			if strings.Contains(body, `href="/authored.ico"`) == tc.icon {
				t.Fatal("icon admission")
			}
			if tc.script && tc.policy != "" && !strings.Contains(resp.Header.Get("Content-Security-Policy"), envui.CSPHash) {
				t.Fatal("missing CSP hash")
			}
			if !tc.icon && strings.Contains(body, `data-icon="/app/`) {
				t.Fatal("client would override blocked icon")
			}
		})
	}
	p := New()
	p.SetEnvironment(&config.EnvironmentConfig{Label: "Test"})
	for _, oversize := range []bool{false, true} {
		resp := htmlResponse("finops", shell)
		if oversize {
			resp.ContentLength = overlayMaxBodyBytes + 1
		} else {
			resp.Header.Set("Content-Encoding", "gzip")
		}
		if err := p.modifyResponseFor("finops")(resp); err != nil {
			t.Fatal(err)
		}
		if readBody(t, resp) != shell {
			t.Fatal("unrewritable body changed")
		}
	}
}
func TestEnvironmentUnknownAppRecovery(t *testing.T) {
	p := New()
	p.SetDashboardURL("https://control.example/")
	p.SetAppFavicon(true)
	p.SetEnvironment(&config.EnvironmentConfig{Label: "Test"})
	for _, browser := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodGet, "/app/missing/", nil)
		if browser {
			r.Header.Set("Accept", "text/html")
		}
		w := httptest.NewRecorder()
		p.writeUnknownApp(w, r, "missing")
		if w.Code != 404 {
			t.Fatal(w.Code)
		}
		if browser {
			if !strings.Contains(w.Body.String(), "[Test] App not found") || strings.Contains(w.Body.String(), "/app/missing/.shinyhub/favicon") {
				t.Fatal(w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `href="https://control.example/"`) || !strings.Contains(w.Body.String(), "[Test]") {
				t.Fatal(w.Body.String())
			}
		} else if w.Body.String() != `{"error":"unknown app","slug":"missing"}` {
			t.Fatal(w.Body.String())
		}
	}
}
