package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/bundle"
)

// fakeApp is the server's view of one app.
type fakeApp struct {
	Slug          string  `json:"slug"`
	Name          string  `json:"name"`
	Access        string  `json:"access"`
	ContentDigest string  `json:"content_digest"`
	ManagedBy     *string `json:"managed_by"`
	Replicas      int     `json:"replicas"`
	status        string
}

// fleetFakeServer is a minimal but contract-accurate ShinyHub server: it
// enforces the X-Shinyhub-If-* preconditions exactly as the real server does
// (empty If-Content-Digest = no assertion; If-Managed-By header present even
// empty activates the check, empty value asserts unmanaged).
type fleetFakeServer struct {
	mu         sync.Mutex
	apps       map[string]*fakeApp
	preconds   bool
	nextDigest string // digest a deploy will promote to
	deploys    int
	url        string
}

func newFleetFake(preconds bool) *fleetFakeServer {
	return &fleetFakeServer{apps: map[string]*fakeApp{}, preconds: preconds, nextDigest: "sha256:DEPLOYED"}
}

func (s *fleetFakeServer) httptest(t *testing.T) *cliConfig {
	srv := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return &cliConfig{Host: srv.URL, Token: "shk_test"}
}

func (s *fleetFakeServer) precondFail(r *http.Request, a *fakeApp) bool {
	if !s.preconds {
		return false
	}
	if d := r.Header.Get("X-Shinyhub-If-Content-Digest"); d != "" {
		if a == nil || a.ContentDigest != d {
			return true
		}
	}
	if v, ok := r.Header["X-Shinyhub-If-Managed-By"]; ok {
		want := ""
		if len(v) > 0 {
			want = v[0]
		}
		cur := ""
		if a != nil && a.ManagedBy != nil {
			cur = *a.ManagedBy
		}
		if want != cur {
			return true
		}
	}
	return false
}

func (s *fleetFakeServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case r.URL.Path == "/api/server-info":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"capabilities": map[string]bool{"fleet_preconditions": s.preconds, "content_digest": true},
		})

	case r.Method == "GET" && r.URL.Path == "/api/apps":
		list := make([]fakeApp, 0, len(s.apps))
		for _, a := range s.apps {
			list = append(list, *a)
		}
		_ = json.NewEncoder(w).Encode(list)

	case r.Method == "POST" && r.URL.Path == "/api/apps":
		var body struct{ Slug, Name, Access string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := s.apps[body.Slug]; !ok {
			s.apps[body.Slug] = &fakeApp{Slug: body.Slug, Name: body.Name, Access: body.Access, status: "running"}
		}
		w.WriteHeader(201)

	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/apps/"):
		slug := strings.TrimPrefix(r.URL.Path, "/api/apps/")
		a, ok := s.apps[slug]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": a.status}})

	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
		s.deploys++
		slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/apps/"), "/deploy")
		a := s.apps[slug]
		if s.precondFail(r, a) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"precondition failed (re-run plan)"}`))
			return
		}
		if a == nil {
			a = &fakeApp{Slug: slug, status: "running"}
			s.apps[slug] = a
		}
		// Compute the real content digest from the uploaded bundle so that a
		// subsequent apply can detect unchanged source correctly.
		digest := s.nextDigest
		if raw, err := io.ReadAll(r.Body); err == nil {
			mr, merr := http.NewRequest(r.Method, r.URL.String(), bytes.NewReader(raw))
			if merr == nil {
				mr.Header = r.Header
				if mf, _, perr := mr.FormFile("bundle"); perr == nil {
					if data, rerr := io.ReadAll(mf); rerr == nil {
						if zr, zerr := zip.NewReader(bytes.NewReader(data), int64(len(data))); zerr == nil {
							if d, derr := bundle.DigestZipReader(zr); derr == nil {
								digest = d
							}
						}
					}
					mf.Close()
				}
			}
		}
		a.ContentDigest = digest
		a.status = "running"
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"ok"}`))

	case r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/access"):
		slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/apps/"), "/access")
		a := s.apps[slug]
		if s.precondFail(r, a) {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"precondition failed (re-run plan)"}`))
			return
		}
		var b struct{ Access string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		a.Access = b.Access
		w.WriteHeader(200)

	case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/api/apps/"):
		slug := strings.TrimPrefix(r.URL.Path, "/api/apps/")
		a := s.apps[slug]
		if s.precondFail(r, a) {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"precondition failed (re-run plan)"}`))
			return
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if mb, present := b["managed_by"]; present {
			if mb == nil {
				a.ManagedBy = nil
			} else {
				s := mb.(string)
				a.ManagedBy = &s
			}
		}
		if rv, present := b["replicas"]; present {
			a.Replicas = int(rv.(float64))
		}
		if name, present := b["name"].(string); present {
			a.Name = name
		}
		w.WriteHeader(200)

	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/apps/"):
		slug := strings.TrimPrefix(r.URL.Path, "/api/apps/")
		a := s.apps[slug]
		if s.precondFail(r, a) {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"precondition failed (re-run plan)"}`))
			return
		}
		delete(s.apps, slug)
		w.WriteHeader(200)

	default:
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}
}

func strp(s string) *string { return &s }

func writeCLIConfig(t *testing.T, fake *fleetFakeServer) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(cliConfig{Host: fake.url, Token: "shk_test"}); err != nil {
		t.Fatal(err)
	}
	return path
}

// applyManifest writes a manifest+source tree, points the CLI at fake, and
// runs `fleet apply` with extra args, returning combined output + error.
func applyManifest(t *testing.T, fake *fleetFakeServer, manifest string, args ...string) (string, error) {
	cfgFile := writeCLIConfig(t, fake)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "app.py"), "print(1)\n")
	mustWrite(t, filepath.Join(dir, "shinyhub-fleet.toml"), manifest)
	// Force table mode: execCLI is piped (not a TTY) so the default would be
	// JSON now that fleet apply calls resolveFormat. These acceptance tests
	// verify the human-readable report strings ("1 created", "1 deleted", etc.).
	full := append([]string{"--config", cfgFile, "fleet", "apply",
		"-f", filepath.Join(dir, "shinyhub-fleet.toml"), "-o", "table"}, args...)
	return execCLI(t, full...)
}

// FLT-7: --json must keep stdout a single valid JSON envelope. Per-app deploy
// progress (zip summary, health "still starting"/"healthy" lines) must go to
// stderr so machine consumers can pipe stdout straight into a JSON parser.
func TestFleetApply_JSONStdoutIsPureEnvelope(t *testing.T) {
	fake := newFleetFake(true)
	cfg := fake.httptest(t)
	cfgFile := writeCLIConfig(t, fake)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "app.py"), "print(1)\n")
	man := "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./src\"\nvisibility=\"private\"\n"
	mustWrite(t, filepath.Join(dir, "shinyhub-fleet.toml"), man)
	_ = cfg

	stdout, _, err := execCLISplit(t, "--config", cfgFile, "fleet", "apply",
		"-f", filepath.Join(dir, "shinyhub-fleet.toml"), "--json")
	if err != nil {
		t.Fatalf("apply --json: %v\nstdout=%s", err, stdout)
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &env); jerr != nil {
		t.Fatalf("stdout is not a single JSON document: %v\nstdout=%q", jerr, stdout)
	}
	if _, ok := env["apps"]; !ok {
		t.Fatalf("JSON envelope missing apps key:\n%s", stdout)
	}
}

func TestFleetApply_GitLabCIDefaultsToHumanReport(t *testing.T) {
	t.Setenv("GITLAB_CI", "true")
	fake := newFleetFake(true)
	_ = fake.httptest(t)
	cfgFile := writeCLIConfig(t, fake)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "app.py"), "print(1)\n")
	manifest := "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./src\"\nvisibility=\"private\"\n"
	manifestPath := filepath.Join(dir, "shinyhub-fleet.toml")
	mustWrite(t, manifestPath, manifest)

	stdout, _, err := execCLISplit(t, "--config", cfgFile, "fleet", "apply", "-f", manifestPath)
	if err != nil {
		t.Fatalf("apply in GitLab CI: %v\nstdout=%s", err, stdout)
	}
	if !strings.Contains(stdout, "shinyhub fleet apply") || !strings.Contains(stdout, "1 created") {
		t.Fatalf("GitLab CI output is not the human report:\n%s", stdout)
	}
	if json.Valid([]byte(strings.TrimSpace(stdout))) {
		t.Fatalf("GitLab CI unexpectedly defaulted to JSON:\n%s", stdout)
	}
}

func TestFleetApply_Acceptance_CreateThenIdempotent(t *testing.T) {
	fake := newFleetFake(true)
	_ = fake.httptest(t)
	man := "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./src\"\nvisibility=\"private\"\n"

	out, err := applyManifest(t, fake, man)
	if err != nil {
		t.Fatalf("first apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 created") {
		t.Fatalf("want 1 created:\n%s", out)
	}
	if mb := fake.apps["ops"].ManagedBy; mb == nil || *mb != "fleet:eu" {
		t.Fatalf("marker not stamped: %#v", mb)
	}

	out2, err2 := applyManifest(t, fake, man)
	if err2 != nil {
		t.Fatalf("second apply must be clean: %v\n%s", err2, out2)
	}
	if !strings.Contains(out2, "1 unchanged") || strings.Contains(out2, "created") && !strings.Contains(out2, "0 created") {
		t.Fatalf("second apply must be idempotent (all unchanged):\n%s", out2)
	}
}

func TestFleetApply_Acceptance_AdoptMatchingAppWithoutRedeploy(t *testing.T) {
	fake := newFleetFake(true)
	_ = fake.httptest(t)
	man := "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./src\"\nvisibility=\"private\"\n"

	if out, err := applyManifest(t, fake, man); err != nil {
		t.Fatalf("seed apply: %v\n%s", err, out)
	}
	fake.mu.Lock()
	fake.apps["ops"].ManagedBy = nil
	seedDeploys := fake.deploys
	fake.mu.Unlock()

	out, err := applyManifest(t, fake, man, "--adopt")
	if err != nil {
		t.Fatalf("adopt matching app: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 adopted") {
		t.Fatalf("want 1 adopted:\n%s", out)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.deploys != seedDeploys {
		t.Fatalf("matching adoption issued %d new deploys, want 0", fake.deploys-seedDeploys)
	}
	if mb := fake.apps["ops"].ManagedBy; mb == nil || *mb != "fleet:eu" {
		t.Fatalf("marker not stamped: %#v", mb)
	}
}

func TestFleetApply_Acceptance_SharedInputEditRestagesEveryConsumer(t *testing.T) {
	fake := newFleetFake(true)
	_ = fake.httptest(t)
	cfgFile := writeCLIConfig(t, fake)
	root := t.TempDir()
	for _, slug := range []string{"sales", "operations"} {
		mustWrite(t, filepath.Join(root, "apps", slug, "app.py"), "print('app')\n")
	}
	mustWrite(t, filepath.Join(root, "_shared", "theme.py"), "COLOR = 'orange'\n")
	manifestPath := writeFleetManifest(t, root, `fleet_id = "eu"

[[bundle_file]]
from = "_shared/theme.py"
to = "helpers/theme.py"
consumers = ["sales", "operations"]

[[app]]
slug = "sales"
source = "./apps/sales"
visibility = "private"

[[app]]
slug = "operations"
source = "./apps/operations"
visibility = "private"
`)
	run := func(args ...string) (string, error) {
		full := append([]string{"--config", cfgFile}, args...)
		return execCLI(t, full...)
	}

	if out, err := run("fleet", "validate", "-f", manifestPath, "-o", "table"); err != nil {
		t.Fatalf("validate composed fleet: %v\n%s", err, out)
	}
	plan, err := run("fleet", "plan", "-f", manifestPath, "-o", "table", "--no-color")
	if err != nil {
		t.Fatalf("initial plan: %v\n%s", err, plan)
	}
	for _, want := range []string{"_shared/theme.py -> helpers/theme.py", "consumers: 2 (2 have a planned source update)", "2 to create"} {
		if !strings.Contains(plan, want) {
			t.Fatalf("initial plan missing %q:\n%s", want, plan)
		}
	}
	if out, err := run("fleet", "apply", "-f", manifestPath, "-o", "table"); err != nil {
		t.Fatalf("initial apply: %v\n%s", err, out)
	} else if !strings.Contains(out, "2 created") {
		t.Fatalf("initial apply did not create both consumers:\n%s", out)
	}

	cleanPlan, err := run("fleet", "plan", "-f", manifestPath, "-o", "table", "--no-color")
	if err != nil {
		t.Fatalf("clean plan: %v\n%s", err, cleanPlan)
	}
	for _, want := range []string{"consumers: 2 (0 have a planned source update)", "2 unchanged"} {
		if !strings.Contains(cleanPlan, want) {
			t.Fatalf("clean plan missing %q:\n%s", want, cleanPlan)
		}
	}

	mustWrite(t, filepath.Join(root, "_shared", "theme.py"), "COLOR = 'green'\n")
	changedPlan, err := run("fleet", "plan", "-f", manifestPath, "-o", "table", "--no-color")
	if err != nil {
		t.Fatalf("changed plan: %v\n%s", err, changedPlan)
	}
	for _, want := range []string{"consumers: 2 (2 have a planned source update)", "2 to update"} {
		if !strings.Contains(changedPlan, want) {
			t.Fatalf("changed plan missing %q:\n%s", want, changedPlan)
		}
	}
	if out, err := run("fleet", "apply", "-f", manifestPath, "-o", "table"); err != nil {
		t.Fatalf("changed apply: %v\n%s", err, out)
	} else if !strings.Contains(out, "2 updated") {
		t.Fatalf("changed apply did not restage both consumers:\n%s", out)
	}

	finalPlan, err := run("fleet", "plan", "-f", manifestPath, "-o", "table", "--no-color")
	if err != nil {
		t.Fatalf("final plan: %v\n%s", err, finalPlan)
	}
	for _, want := range []string{"consumers: 2 (0 have a planned source update)", "2 unchanged"} {
		if !strings.Contains(finalPlan, want) {
			t.Fatalf("final plan missing %q:\n%s", want, finalPlan)
		}
	}
}

// TestFleetApply_Acceptance_CreateAppliesDeclaredConfig guards that a create
// applies the manifest's [app.config], not just the source bundle. Otherwise a
// freshly applied app runs with server-default config and the very next plan
// reports spurious "update(config)" drift — non-idempotent, and the app is
// silently misconfigured until a second apply.
func TestFleetApply_Acceptance_CreateAppliesDeclaredConfig(t *testing.T) {
	fake := newFleetFake(true)
	_ = fake.httptest(t)
	// replicas=2 differs from the server default, so a create that ignores
	// [app.config] leaves drift the next apply would have to fix.
	man := "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./src\"\nvisibility=\"private\"\n\n  [app.config]\n  replicas = 2\n"

	out, err := applyManifest(t, fake, man)
	if err != nil {
		t.Fatalf("first apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 created") {
		t.Fatalf("want 1 created:\n%s", out)
	}
	if got := fake.apps["ops"].Replicas; got != 2 {
		t.Fatalf("create must apply declared config: replicas = %d, want 2", got)
	}

	out2, err2 := applyManifest(t, fake, man)
	if err2 != nil {
		t.Fatalf("second apply: %v\n%s", err2, out2)
	}
	if !strings.Contains(out2, "1 unchanged") {
		t.Fatalf("second apply must be idempotent after a config-bearing create:\n%s", out2)
	}
}

func TestFleetApply_Acceptance_PruneRemovesAfterConfirm(t *testing.T) {
	fake := newFleetFake(true)
	_ = fake.httptest(t)
	fake.apps["gone"] = &fakeApp{Slug: "gone", Access: "private",
		ContentDigest: "sha256:OLD", ManagedBy: strp("fleet:eu"), status: "running"}
	man := "fleet_id=\"eu\"\n"

	out, err := applyManifest(t, fake, man, "--prune", "--yes")
	if err != nil {
		t.Fatalf("prune apply: %v\n%s", err, out)
	}
	if _, still := fake.apps["gone"]; still {
		t.Fatalf("prune did not delete the app:\n%s", out)
	}
	if !strings.Contains(out, "1 deleted") {
		t.Fatalf("want 1 deleted:\n%s", out)
	}
}

func TestFleetApply_Acceptance_LikelySlugReplacementRequiresPruneBeforeMutation(t *testing.T) {
	fake := newFleetFake(true)
	_ = fake.httptest(t)
	fake.apps["old-dashboard"] = &fakeApp{
		Slug: "old-dashboard", Name: "Operations dashboard", Access: "private",
		ContentDigest: "sha256:OLD", ManagedBy: strp("fleet:eu"), status: "running",
	}
	manifest := `fleet_id="eu"
[[app]]
slug="new-dashboard"
source="./src"
[app.config]
name="Operations dashboard"
`

	out, err := applyManifest(t, fake, manifest)
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("slug replacement without prune error = %v, want exit 1\n%s", err, out)
	}
	if !strings.Contains(out, "old-dashboard") || !strings.Contains(out, "new-dashboard") || !strings.Contains(hintOf(err), "--prune") {
		t.Fatalf("replacement refusal must name both slugs and the recovery command:\nout=%s\nhint=%s", out, hintOf(err))
	}
	if fake.deploys != 0 {
		t.Fatalf("replacement refusal deployed %d time(s); want pre-mutation failure", fake.deploys)
	}
	if _, exists := fake.apps["new-dashboard"]; exists {
		t.Fatal("replacement refusal created the new slug")
	}

	out, err = applyManifest(t, fake, manifest, "--prune", "--yes")
	if err != nil {
		t.Fatalf("explicit replacement apply: %v\n%s", err, out)
	}
	if _, exists := fake.apps["old-dashboard"]; exists {
		t.Fatalf("old slug survived explicit prune:\n%s", out)
	}
	if created := fake.apps["new-dashboard"]; created == nil || created.Name != "Operations dashboard" {
		t.Fatalf("new slug was not created with the friendly name: %+v\n%s", created, out)
	}
}

func TestFleetApply_Acceptance_DegradedPruneRefusedExitsZero(t *testing.T) {
	fake := newFleetFake(false) // server WITHOUT preconditions
	_ = fake.httptest(t)
	fake.apps["gone"] = &fakeApp{Slug: "gone", ManagedBy: strp("fleet:eu"), status: "running"}
	out, err := applyManifest(t, fake, "fleet_id=\"eu\"\n", "--prune", "--yes")
	if err != nil {
		t.Fatalf("degraded prune must exit 0 (skipped, not failed): %v\n%s", err, out)
	}
	if _, still := fake.apps["gone"]; !still {
		t.Fatal("degraded mode must NOT delete the app")
	}
	if !strings.Contains(out, "degraded") {
		t.Fatalf("must explain the degraded refusal:\n%s", out)
	}
}
