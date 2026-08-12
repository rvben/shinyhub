package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func appOpenServer(t *testing.T, status, access string, deployCount int, routeStatus int, routeHits *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/demo", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Token shk_test" {
			t.Errorf("Authorization = %q, want CLI token", got)
		}
		_, _ = fmt.Fprintf(w, `{"app":{"slug":"demo","status":%q,"access":%q,"deploy_count":%d}}`, status, access, deployCount)
	})
	mux.HandleFunc("/app/demo/", func(w http.ResponseWriter, r *http.Request) {
		if routeHits != nil {
			*routeHits++
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("public route received CLI Authorization header %q", got)
		}
		w.WriteHeader(routeStatus)
		_, _ = w.Write([]byte("app"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func stubBrowser(t *testing.T, fn func(string) error) {
	t.Helper()
	original := openBrowserURL
	openBrowserURL = fn
	t.Cleanup(func() { openBrowserURL = original })
}

func decodeOpenResult(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("decode output %q: %v", stdout, err)
	}
	return result
}

func TestRemoteAppURLIsCanonical(t *testing.T) {
	for _, host := range []string{"https://hub.example.com", "https://hub.example.com/"} {
		if got, want := remoteAppURL(host, "sales-app"), "https://hub.example.com/app/sales-app/"; got != want {
			t.Errorf("remoteAppURL(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestAppsOpen_PrivateAppOpensCanonicalURLWithoutRouteProbe(t *testing.T) {
	var routeHits int
	srv := appOpenServer(t, "running", "private", 2, http.StatusOK, &routeHits)
	writeTestCLIConfig(t, srv.URL)
	var opened string
	stubBrowser(t, func(target string) error { opened = target; return nil })

	stdout, stderr, err := execCLISplit(t, "apps", "open", "demo", "-o", "json")
	if err != nil {
		t.Fatalf("apps open: %v (stderr=%q)", err, stderr)
	}
	wantURL := srv.URL + "/app/demo/"
	if opened != wantURL {
		t.Errorf("opened URL = %q, want %q", opened, wantURL)
	}
	if routeHits != 0 {
		t.Errorf("private route was probed %d times; browser authentication must own this request", routeHits)
	}
	result := decodeOpenResult(t, stdout)
	if result["url"] != wantURL || result["opened"] != true || result["status"] != "ready" {
		t.Errorf("result = %#v", result)
	}
}

func TestAppsOpen_PublicAppVerifiesRouteBeforeBrowser(t *testing.T) {
	var routeHits int
	srv := appOpenServer(t, "healthy", "public", 1, http.StatusOK, &routeHits)
	writeTestCLIConfig(t, srv.URL)
	var opened bool
	stubBrowser(t, func(string) error { opened = true; return nil })

	_, _, err := execCLISplit(t, "apps", "open", "demo", "-o", "json")
	if err != nil {
		t.Fatalf("apps open: %v", err)
	}
	if routeHits != 1 || !opened {
		t.Fatalf("route hits = %d, browser opened = %v; want verification followed by browser", routeHits, opened)
	}
}

func TestAppsOpen_NoBrowserPrintsCopyableURL(t *testing.T) {
	srv := appOpenServer(t, "hibernated", "private", 1, http.StatusOK, nil)
	writeTestCLIConfig(t, srv.URL)
	stubBrowser(t, func(string) error { t.Fatal("--no-browser launched a browser"); return nil })

	stdout, _, err := execCLISplit(t, "apps", "open", "demo", "--no-browser", "-o", "table")
	if err != nil {
		t.Fatalf("apps open: %v", err)
	}
	if !strings.Contains(stdout, srv.URL+"/app/demo/") || !strings.Contains(stdout, "demo is ready") {
		t.Errorf("output lacks ready state and copyable URL: %q", stdout)
	}
}

func TestAppsOpen_BrowserFailureIsNonFatalAndStructured(t *testing.T) {
	srv := appOpenServer(t, "running", "private", 1, http.StatusOK, nil)
	writeTestCLIConfig(t, srv.URL)
	stubBrowser(t, func(string) error { return errors.New("no graphical session") })

	stdout, stderr, err := execCLISplit(t, "apps", "open", "demo", "-o", "json")
	if err != nil {
		t.Fatalf("browser convenience failure must not fail a ready app: %v", err)
	}
	result := decodeOpenResult(t, stdout)
	if result["opened"] != false {
		t.Errorf("opened = %v, want false", result["opened"])
	}
	if !strings.Contains(stderr, "no graphical session") || !strings.Contains(stderr, srv.URL+"/app/demo/") {
		t.Errorf("stderr should explain fallback and print URL: %q", stderr)
	}
}

func TestAppsOpen_PublicRouteFailureDoesNotLaunchBrowser(t *testing.T) {
	var routeHits int
	srv := appOpenServer(t, "running", "public", 1, http.StatusBadGateway, &routeHits)
	writeTestCLIConfig(t, srv.URL)
	stubBrowser(t, func(string) error { t.Fatal("browser launched despite failed route check"); return nil })

	_, _, err := execCLISplit(t, "apps", "open", "demo", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "public route check failed") {
		t.Fatalf("error = %v, want public route diagnosis", err)
	}
	if routeHits != 1 || !strings.Contains(hintOf(err), "app was not changed") {
		t.Errorf("route hits = %d, hint = %q", routeHits, hintOf(err))
	}
}

func TestAppsOpen_NonOpenableStatesGiveExactRecovery(t *testing.T) {
	tests := []struct {
		name, status string
		deployCount  int
		want         string
	}{
		{name: "never deployed", status: "stopped", deployCount: 0, want: "shinyhub deploy . --slug demo --open"},
		{name: "stopped", status: "stopped", deployCount: 2, want: "shinyhub apps start demo"},
		{name: "crashed", status: "crashed", deployCount: 2, want: "shinyhub apps logs demo --no-follow"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := appOpenServer(t, tc.status, "private", tc.deployCount, http.StatusOK, nil)
			writeTestCLIConfig(t, srv.URL)
			_, _, err := execCLISplit(t, "apps", "open", "demo", "-o", "json")
			if err == nil || !strings.Contains(hintOf(err), tc.want) {
				t.Fatalf("error = %v, hint = %q, want %q", err, hintOf(err), tc.want)
			}
		})
	}
}

func TestAppStatusOpenableMatchesLaunchpadContract(t *testing.T) {
	for _, status := range []string{"running", "healthy", "hibernated", "suspended", "deploying", "waking", "degraded"} {
		if !appStatusOpenable(status) {
			t.Errorf("%q should be openable", status)
		}
	}
	for _, status := range []string{"", "unknown", "stopped", "crashed", "failed"} {
		if appStatusOpenable(status) {
			t.Errorf("%q should not be openable", status)
		}
	}
}
