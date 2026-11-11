package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/availability"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fleet"
)

func TestAvailabilityFleetBundleOverridesWithoutEditingSource(t *testing.T) {
	for _, source := range []string{"", "[app]\navailability='public'\n"} {
		dir := t.TempDir()
		if source != "" {
			mustWrite(t, filepath.Join(dir, "shinyhub.toml"), source)
		}
		off := "off"
		spec, m, err := resolveEnvironmentBundle(bundleBuildSpec{Dir: dir}, fleet.Config{Availability: &off})
		if err != nil || spec.Availability == nil || *spec.Availability != "off" {
			t.Fatalf("overlay: %+v %v", m, err)
		}
		data, readErr := os.ReadFile(filepath.Join(dir, "shinyhub.toml"))
		if source == "" {
			if m != nil || len(spec.Manifest) != 0 {
				t.Fatal("fleet-only exposure changed bundle contents")
			}
			if !os.IsNotExist(readErr) {
				t.Fatal("created source manifest")
			}
		} else if readErr != nil || string(data) != source {
			t.Fatal("modified source manifest")
		}
		if source != "" {
			if got := bundleFleetConfig(m.App); got.Availability == nil || *got.Availability != "off" {
				t.Fatal("bundle config lost exposure")
			}
		}
	}
}

func TestAvailabilityFleetConfigPatchAndExport(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" || r.URL.Path != "/api/apps/demo" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"app":{"slug":"demo","availability":"off"}}`))
	}))
	defer srv.Close()
	off := "off"
	err := applyConfigDrift(&cliConfig{Host: srv.URL, Token: "test"}, "demo", []fleet.ConfigDriftItem{{Key: "availability", Server: `"public"`, Desired: `"off"`}}, fleet.Config{Availability: &off}, nil, nil, "")
	if err != nil || len(body) != 1 || body["availability"] != "off" {
		t.Fatalf("patch: %+v %v", body, err)
	}
	doc := emitFleetManifest("prod", "./apps", []db.App{{Slug: "demo", Name: "Demo", Access: "private", Availability: "public"}})
	m, problems := fleet.ParseManifest([]byte(doc), "fleet.toml")
	if len(problems) != 0 || m.Apps[0].Config.Availability == nil || *m.Apps[0].Config.Availability != "public" {
		t.Fatalf("export lost opt-in: %s %v", doc, problems)
	}
}

func TestAvailabilityFleetConfigConvergesWithoutDeploy(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Path != "/api/apps/demo" || (r.Method != "PATCH" && r.Method != "GET") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"app":{"slug":"demo","availability":"off","status":"running"}}`))
	}))
	defer srv.Close()
	off := "off"
	entry := fleet.AppEntry{Slug: "demo", Config: fleet.Config{Availability: &off}}
	diff := fleet.AppDiff{Slug: "demo", Action: fleet.ActionUpdateConfig, ConfigDrift: []fleet.ConfigDriftItem{{Key: "availability", Server: `"public"`, Desired: `"off"`}}}
	result := convergeApp(&cliConfig{Host: srv.URL, Token: "test"}, diff, entry, fleet.ObservedApp{}, t.TempDir(), convergeOpts{}, "", io.Discard)
	if result.status != statusUpdated || result.err != nil {
		t.Fatalf("converge: %+v", result)
	}
	if len(methods) != 2 || methods[0] != "PATCH" || methods[1] != "GET" {
		t.Fatalf("config-only change requested deployment: %v", methods)
	}
}

func TestAvailabilityFleetEnrollsAtCreateOnly(t *testing.T) {
	exists := false
	creates := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			if !exists {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write([]byte(`{"app":{"slug":"demo"}}`))
		case "POST":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if r.URL.Path != "/api/apps" || body["availability"] != "public" {
				t.Errorf("missing initial enrollment: %s %+v", r.URL.Path, body)
			}
			creates++
			exists = true
			w.WriteHeader(201)
		default:
			t.Errorf("unexpected mutation: %s", r.Method)
		}
	}))
	defer srv.Close()
	cfg := &cliConfig{Host: srv.URL, Token: "test"}
	public := "public"
	off := "off"
	if err := ensureAppCoreWithAvailability(cfg, "demo", "", "", &public, io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if err := ensureAppCoreWithAvailability(cfg, "demo", "", "", &off, io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if creates != 1 {
		t.Fatalf("creates=%d", creates)
	}
}

func TestAvailabilityFleetDeclarationPreservesUnrelatedBundleSettings(t *testing.T) {
	_, _ = setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"app_availability":true,"content_digest":true}}`))
		case "/api/apps":
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	file := writeFleetTree(t, "fleet_id='prod'\n[[app]]\nslug='demo'\nsource='./demo'\n[app.config]\nreplicas=3\nhibernate_timeout_minutes=20\n", map[string]string{"demo": "# original formatting\n[app]\nreplicas=1\nhibernate_timeout_minutes=10\n"})
	plan := func() *preflightResult {
		t.Helper()
		var stderr bytes.Buffer
		pf, err := fleetPreflight(file, &stderr, "plan", 0)
		if err != nil {
			t.Fatalf("plan: %v %s", err, stderr.String())
		}
		t.Cleanup(pf.cleanup)
		return pf
	}
	before := plan()
	manifest, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, file, string(manifest)+"availability='public'\n")
	after := plan()
	if before.diff[0].LocalDigest != after.diff[0].LocalDigest || len(after.bundles["demo"].Manifest) != 0 {
		t.Fatalf("monitoring rewrote unrelated settings: before=%s after=%s manifest=%s", before.diff[0].LocalDigest, after.diff[0].LocalDigest, after.bundles["demo"].Manifest)
	}
	if a := after.bundles["demo"].Availability; a == nil || *a != "public" {
		t.Fatal("initial exposure not carried to create")
	}
	if got := after.manifest.Apps[0].Bundle; got.Replicas == nil || *got.Replicas != 1 || got.HibernateTimeoutMinutes == nil || *got.HibernateTimeoutMinutes != 10 {
		t.Fatalf("bundle projection changed: %+v", got)
	}
}

func TestAvailabilityFleetSourceOnlyEnrollsBeforeUpload(t *testing.T) {
	var created map[string]string
	exists := false
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/server-info":
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"app_availability":true,"content_digest":true}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			if !exists {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write([]byte(`{"app":{"slug":"demo","availability":"public"}}`))
		case r.Method == "POST" && r.URL.Path == "/api/apps":
			exists = true
			w.WriteHeader(201)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	})
	file := writeFleetTree(t, "fleet_id='prod'\n[[app]]\nslug='demo'\nsource='./demo'\n", map[string]string{"demo": "[app]\navailability='public'\n"})
	var stderr bytes.Buffer
	pf, err := fleetPreflight(file, &stderr, "plan", 0)
	if err != nil {
		t.Fatalf("plan: %v %s", err, stderr.String())
	}
	defer pf.cleanup()
	spec := pf.bundles["demo"]
	if spec.Availability == nil || *spec.Availability != "public" || len(spec.Manifest) != 0 {
		t.Fatalf("source-only create declaration: %+v", spec)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureAppCoreWithAvailability(cfg, "demo", "", "", spec.Availability, io.Discard, false); err != nil {
		t.Fatal(err)
	}
	creation, ok := findRequest(*reqs, "POST", "/api/apps")
	if !ok || json.Unmarshal(creation.Body, &created) != nil {
		t.Fatal("missing or invalid create request")
	}
	// Enrollment must be durable even if no upload can subsequently complete.
	if !exists || created["availability"] != "public" {
		t.Fatalf("source opt-in not enrolled before upload: %+v", created)
	}
}

func TestAvailabilityFleetExportPreservesEveryIntent(t *testing.T) {
	for _, intent := range []string{"off", "inherit", "public"} {
		doc := emitFleetManifest("prod", "./apps", []db.App{{Slug: "demo", Name: "Demo", Access: "private", Availability: intent}})
		m, problems := fleet.ParseManifest([]byte(doc), "fleet.toml")
		if len(problems) != 0 || m.Apps[0].Config.Availability == nil || *m.Apps[0].Config.Availability != intent {
			t.Fatalf("export %s: %s %v", intent, doc, problems)
		}
	}
}

func TestAvailabilityAppsSetCapabilityGate(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{false: "old", true: "new"}[supported], func(t *testing.T) {
			_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/server-info" {
					_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.0.0", "capabilities": map[string]bool{"availability_inherit": supported}})
					return
				}
				_, _ = w.Write([]byte(`{"app":{"availability":"inherit"}}`))
			})
			out, err := execCLI(t, "apps", "set", "demo", "--availability", "inherit")
			patches := requestPaths(*reqs, "PATCH")
			if supported {
				if err != nil || len(patches) != 1 || !strings.Contains(out, `"availability":"inherit"`) {
					t.Fatalf("supported setter: %s %v %v", out, err, patches)
				}
			} else if err == nil || len(patches) != 0 {
				t.Fatalf("old server mutated: %v %v", err, patches)
			}
		})
	}
}

func TestAvailabilityFleetPlanRejectsUnsupportedInheritance(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/server-info" {
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"app_availability":true}}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	file := writeFleetTree(t, "fleet_id='prod'\n[[app]]\nslug='demo'\nsource='./demo'\n[app.config]\navailability='inherit'\n", map[string]string{"demo": ""})
	_, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
	if err == nil || !strings.Contains(stderr, "availability_inherit") || len(requestPaths(*reqs, "POST")) > 0 || len(requestPaths(*reqs, "PATCH")) > 0 {
		t.Fatalf("gate: err=%v stderr=%s requests=%v", err, stderr, *reqs)
	}
}

func TestAvailabilityFleetPlanExplainsEffectiveExposure(t *testing.T) {
	e := availability.Resolve("off", true, true, true)
	d := fleet.AppDiff{Slug: "demo", Action: fleet.ActionUnchanged, AvailabilityExposure: &e}
	resource := fleetAppPlanResource(d, "prod")
	if !strings.Contains(strings.Join(resource.Notes, " "), "public (intent off; source allowlist)") {
		t.Fatalf("human plan omitted override: %+v", resource)
	}
	var out bytes.Buffer
	if err := writeFleetPlanJSON(&out, &fleet.Manifest{FleetID: "prod"}, "http://server", []fleet.AppDiff{d}, nil, 0, "no_changes"); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Apps []struct {
			Exposure *availability.Exposure `json:"availability_exposure"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil || len(payload.Apps) != 1 || payload.Apps[0].Exposure == nil || payload.Apps[0].Exposure.Source != "allowlist" || !payload.Apps[0].Exposure.Exposed {
		t.Fatalf("JSON plan omitted effective exposure: %s %v", out.String(), err)
	}
}
