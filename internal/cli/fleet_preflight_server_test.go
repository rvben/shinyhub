package cli

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// A bundle whose deploy depends on the server's topology: an elastic pool
// combined with a deploy-triggered producer schedule, which the server rejects
// on a tier whose processes do not inherit its lifetime locks.
const groupedProducerBundleManifest = `[app.worker]
isolation = "grouped"
grouped_size = 4
max_workers = 8

[[schedule]]
name = "refresh-data"
cron = "0 2 * * *"
cmd = "python refresh.py"
deploy_trigger = "first_deploy"
`

const groupedProducerRejection = `schedule "refresh-data": data-producing schedules need a local native tier, whose processes inherit the server's publication and consumer-lifetime locks; tier "local" uses docker`

// writeFleetTree writes a fleet manifest plus one source dir per app and
// returns the fleet manifest path. bundles maps slug -> shinyhub.toml content
// ("" writes no manifest).
func writeFleetTree(t *testing.T, fleetManifest string, bundles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for slug, manifest := range bundles {
		mustWrite(t, filepath.Join(dir, slug, "app.py"), "from shiny import App\n")
		if manifest != "" {
			mustWrite(t, filepath.Join(dir, slug, "shinyhub.toml"), manifest)
		}
	}
	return writeFleetManifest(t, dir, fleetManifest)
}

func requestPaths(reqs []capturedReq, method string) []string {
	var out []string
	for _, r := range reqs {
		if r.Method == method {
			out = append(out, r.Path)
		}
	}
	return out
}

func findRequest(reqs []capturedReq, method, path string) (capturedReq, bool) {
	for _, r := range reqs {
		if r.Method == method && r.Path == path {
			return r, true
		}
	}
	return capturedReq{}, false
}

// A server that advertises deploy_preflight is asked, per app the plan would
// deploy, whether the deploy would be accepted, and its rejection stops both
// plan and apply before anything is created, stamped, deployed, or patched.
func TestFleetPlan_ServerPreflightRejectsBeforeAnyChange(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/server-info":
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"deploy_preflight":true,"fleet_preconditions":true,"content_digest":true}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[{"slug":"reporting","access":"private","managed_by":"fleet:eu","content_digest":"sha256:stale","replicas":1}]`))
		case r.Method == "POST" && r.URL.Path == "/api/apps/reporting/deploy-preflight":
			reply := map[string]any{
				"valid": false, "isolation": "grouped",
				"problems": []map[string]string{{"stage": "deploy", "message": groupedProducerRejection}},
			}
			_ = json.NewEncoder(w).Encode(reply)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	file := writeFleetTree(t, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"reporting\"\nsource=\"./reporting\"\nvisibility=\"private\"\n\n  [app.config]\n  replicas = 2\n",
		map[string]string{"reporting": groupedProducerBundleManifest})

	stdout, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
	if exitCode(err) != 1 {
		t.Fatalf("plan exit=%d err=%v\nstdout=%s\nstderr=%s", exitCode(err), err, stdout, stderr)
	}
	for _, want := range []string{"checking with the server", `app "reporting": ` + groupedProducerRejection, "Nothing was changed"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("plan stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stdout, "update(source") {
		t.Errorf("plan printed a diff the server already refused:\n%s", stdout)
	}

	pre, ok := findRequest(*reqs, "POST", "/api/apps/reporting/deploy-preflight")
	if !ok {
		t.Fatalf("no preflight request; requests=%v", requestPaths(*reqs, "POST"))
	}
	var body struct {
		Manifest string `json:"manifest"`
		AppType  string `json:"app_type"`
		Settings struct {
			Replicas *int `json:"replicas"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(pre.Body, &body); err != nil {
		t.Fatalf("preflight body: %v (%s)", err, pre.Body)
	}
	if body.Manifest != groupedProducerBundleManifest {
		t.Errorf("preflight carried manifest %q, want the bundle's shinyhub.toml verbatim", body.Manifest)
	}
	if body.AppType != "python" {
		t.Errorf("app_type = %q, want python", body.AppType)
	}
	if body.Settings.Replicas == nil || *body.Settings.Replicas != 2 {
		t.Errorf("settings.replicas = %v, want the fleet [app.config] value 2", body.Settings.Replicas)
	}
	if pre.Auth == "" {
		t.Error("preflight request sent without credentials")
	}

	*reqs = nil
	stdout, stderr, err = execCLISplit(t, "fleet", "apply", "-f", file, "--yes")
	if exitCode(err) != 1 {
		t.Fatalf("apply exit=%d err=%v\nstdout=%s\nstderr=%s", exitCode(err), err, stdout, stderr)
	}
	if !strings.Contains(stderr, groupedProducerRejection) || !strings.Contains(stderr, "Nothing was changed") {
		t.Errorf("apply stderr lacks the server rejection:\n%s", stderr)
	}
	for _, r := range *reqs {
		if r.Method == "GET" || r.Path == "/api/apps/reporting/deploy-preflight" {
			continue
		}
		t.Errorf("apply mutated the server after a preflight rejection: %s %s", r.Method, r.Path)
	}
}

// A clean preflight is invisible: apply goes on to create and deploy, and the
// rehearsal happens before the first mutation.
func TestFleetApply_ServerPreflightPassesThenDeploys(t *testing.T) {
	fake := newFleetFake(true)
	fake.deployPreflight = true
	fake.preflightReply = `{"valid":true,"isolation":"multiplex","problems":[]}`
	fake.httptest(t)
	cfgFile := writeCLIConfig(t, fake)
	multiplex := strings.Replace(groupedProducerBundleManifest, `isolation = "grouped"`, `isolation = "multiplex"`, 1)
	file := writeFleetTree(t, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"reporting\"\nsource=\"./reporting\"\nvisibility=\"private\"\n",
		map[string]string{"reporting": multiplex})

	out, err := execCLI(t, "--config", cfgFile, "fleet", "apply", "-f", file, "-o", "table")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if fake.deploys != 1 {
		t.Fatalf("deploys = %d, want 1", fake.deploys)
	}
	if len(fake.preflights) != 1 || fake.preflights[0].slug != "reporting" {
		t.Fatalf("preflights = %+v, want exactly one for reporting", fake.preflights)
	}
	if !strings.Contains(string(fake.preflights[0].body), `isolation = \"multiplex\"`) {
		t.Errorf("preflight body lacks the bundle manifest: %s", fake.preflights[0].body)
	}
	if _, ok := fake.apps["reporting"]; !ok {
		t.Fatal("app was not created after a clean preflight")
	}
}

// A settings-stage problem names the fleet [app.config] block so the operator
// knows the bundle is fine and the fleet manifest is what to change.
func TestFleetPlan_ServerPreflightLabelsSettingsStage(t *testing.T) {
	_, _ = setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/server-info":
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"deploy_preflight":true}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy-preflight"):
			_, _ = w.Write([]byte(`{"valid":false,"isolation":"multiplex","problems":[{"stage":"settings","message":"replicas must be between 1 and 2"}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	file := writeFleetTree(t, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"reporting\"\nsource=\"./reporting\"\n\n  [app.config]\n  replicas = 5\n",
		map[string]string{"reporting": ""})

	_, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
	if exitCode(err) != 1 {
		t.Fatalf("exit=%d err=%v stderr=%s", exitCode(err), err, stderr)
	}
	if !strings.Contains(stderr, `app "reporting" [app.config]: replicas must be between 1 and 2`) {
		t.Fatalf("settings problem not labelled:\n%s", stderr)
	}
}

// Only actions that end in a deploy are rehearsed: a config-only drift is a
// PATCH the server validates in place, and an unchanged app uploads nothing.
func TestFleetPlan_ServerPreflightSkipsConfigOnlyAndUnchanged(t *testing.T) {
	file := writeFleetTree(t, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"drifted\"\nsource=\"./drifted\"\n\n  [app.config]\n  replicas = 2\n\n[[app]]\nslug=\"steady\"\nsource=\"./steady\"\n",
		map[string]string{"drifted": "", "steady": ""})
	dir := filepath.Dir(file)
	driftedDigest, err := digestLocalDir(filepath.Join(dir, "drifted"))
	if err != nil {
		t.Fatal(err)
	}
	steadyDigest, err := digestLocalDir(filepath.Join(dir, "steady"))
	if err != nil {
		t.Fatal(err)
	}
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/server-info":
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"deploy_preflight":true,"content_digest":true}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[{"slug":"drifted","access":"private","managed_by":"fleet:eu","content_digest":"` + driftedDigest + `","replicas":1},` +
				`{"slug":"steady","access":"private","managed_by":"fleet:eu","content_digest":"` + steadyDigest + `","replicas":1}]`))
		default:
			_, _ = w.Write([]byte(`{"valid":false,"problems":[{"stage":"deploy","message":"must not be consulted"}]}`))
		}
	})

	stdout, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
	if err != nil {
		t.Fatalf("plan: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "update(config)") {
		t.Fatalf("fixture did not produce a config-only drift:\n%s", stdout)
	}
	if paths := requestPaths(*reqs, "POST"); len(paths) != 0 {
		t.Fatalf("preflight requests issued for non-deploy actions: %v", paths)
	}
}

// An older server that reports only runtime capabilities is asked the
// runtime-topology question doctor asks, and only for bundles that declare a
// producer or roll schedule; a plain schedule costs no request.
func TestFleetPlan_ServerPreflightFallsBackToRuntimeCapabilities(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/server-info":
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"runtime_capabilities":true}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/reporting/capabilities":
			// An older server's reply, from before elastic pools could host producers.
			_, _ = w.Write([]byte(`{"isolation":"grouped","features":{"grouped":{"supported":true},"deploy_producers":{"supported":false,"reason":"grouped workers do not yet have durable process identity.","remedy":"Use multiplex isolation."}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	})
	plain := strings.Replace(groupedProducerBundleManifest, `deploy_trigger = "first_deploy"`, `deploy_trigger = "never"`, 1)
	file := writeFleetTree(t, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"reporting\"\nsource=\"./reporting\"\n\n[[app]]\nslug=\"plain\"\nsource=\"./plain\"\n",
		map[string]string{"reporting": groupedProducerBundleManifest, "plain": plain})

	_, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
	if exitCode(err) != 1 {
		t.Fatalf("exit=%d err=%v stderr=%s", exitCode(err), err, stderr)
	}
	for _, want := range []string{`app "reporting": `, "grouped workers do not yet have durable process identity.", "Use multiplex isolation."} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if paths := requestPaths(*reqs, "POST"); len(paths) != 0 {
		t.Errorf("deploy-preflight must not be called on a server without the capability: %v", paths)
	}
	if _, ok := findRequest(*reqs, "GET", "/api/apps/plain/capabilities"); ok {
		t.Error("a plain schedule triggered a runtime capability probe")
	}
	if _, ok := findRequest(*reqs, "GET", "/api/apps/reporting/capabilities"); !ok {
		t.Errorf("producer bundle did not probe runtime capabilities; GETs=%v", requestPaths(*reqs, "GET"))
	}
}

// A server that advertises neither capability is left alone: the plan is
// produced exactly as before and no preflight request is issued.
func TestFleetPlan_ServerPreflightAbsentOnOldServer(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/server-info":
			_, _ = w.Write([]byte(`{"version":"0.9.0","capabilities":{}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	})
	file := writeFleetTree(t, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"reporting\"\nsource=\"./reporting\"\n",
		map[string]string{"reporting": groupedProducerBundleManifest})

	stdout, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
	if err != nil {
		t.Fatalf("plan: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "create") {
		t.Fatalf("plan did not report the create:\n%s", stdout)
	}
	for _, r := range *reqs {
		if r.Method != "GET" || strings.HasSuffix(r.Path, "/capabilities") {
			t.Errorf("unexpected request against an old server: %s %s", r.Method, r.Path)
		}
	}
}

// A preflight the server cannot answer (transport failure, non-200) is a
// transport error (exit 3), not a validation verdict.
func TestFleetPlan_ServerPreflightTransportFailureIsExit3(t *testing.T) {
	_, _ = setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/server-info":
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"deploy_preflight":true}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy-preflight"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal error"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	file := writeFleetTree(t, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"reporting\"\nsource=\"./reporting\"\n",
		map[string]string{"reporting": ""})

	_, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
	if exitCode(err) != 3 {
		t.Fatalf("exit=%d err=%v stderr=%s", exitCode(err), err, stderr)
	}
	if !strings.Contains(stderr, "deploy preflight for reporting") || !strings.Contains(stderr, "internal error") {
		t.Fatalf("stderr does not name the failed preflight:\n%s", stderr)
	}
	// A 5xx is the server failing, not a credential problem: the envelope
	// kind is what a CI wrapper branches on to retry rather than re-login.
	if kind, _ := classify(err); kind != KindServerError {
		t.Errorf("classify = %s, want %s for a 500 from the preflight endpoint", kind, KindServerError)
	}
}

// On an older server the runtime-capabilities probe stands in for the deploy
// preflight; a probe the server cannot answer is the same transport failure,
// not a verdict that the deploy would be rejected.
func TestFleetPlan_RuntimeCapabilityProbeFailureIsNotAVerdict(t *testing.T) {
	_, _ = setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/server-info":
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"runtime_capabilities":true}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/capabilities") || r.URL.Path == "/api/runtime-capabilities":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal error"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	file := writeFleetTree(t, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"reporting\"\nsource=\"./reporting\"\n",
		map[string]string{"reporting": groupedProducerBundleManifest})

	_, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
	if exitCode(err) != 3 {
		t.Fatalf("exit=%d err=%v stderr=%s", exitCode(err), err, stderr)
	}
	if kind, _ := classify(err); kind != KindServerError {
		t.Errorf("classify = %s, want %s", kind, KindServerError)
	}
	if strings.Contains(stderr, "the server would reject") {
		t.Errorf("a failed probe was reported as a deploy rejection:\n%s", stderr)
	}
	if !strings.Contains(stderr, "runtime preflight for reporting") || !strings.Contains(stderr, "internal error") {
		t.Errorf("stderr does not name the failed probe:\n%s", stderr)
	}
}

// The app type sent to the preflight is the one the server will detect from
// the upload, not from the source directory: an entrypoint that arrives only
// through a shared [[bundle_file]] input counts, and one the ignore file keeps
// out of the bundle does not. Both would otherwise let an R bundle on a
// Fargate tier pass plan and fail at deploy.
func TestFleetPlan_ServerPreflightDetectsAppTypeFromTheUpload(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/server-info":
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"deploy_preflight":true}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy-preflight"):
			_, _ = w.Write([]byte(`{"valid":true,"isolation":"multiplex","problems":[]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "_shared", "app.R"), "library(shiny)\n")
	mustWrite(t, filepath.Join(dir, "overlay", "README.md"), "entrypoint comes from the shared input\n")
	mustWrite(t, filepath.Join(dir, "ignored", "app.py"), "print('stale prototype')\n")
	mustWrite(t, filepath.Join(dir, "ignored", "app.R"), "library(shiny)\n")
	mustWrite(t, filepath.Join(dir, "ignored", ".shinyhubignore"), "app.py\n")
	file := writeFleetManifest(t, dir, `fleet_id = "eu"

[[bundle_file]]
from = "_shared/app.R"
to = "app.R"
consumers = ["overlay"]

[[app]]
slug = "overlay"
source = "./overlay"

[[app]]
slug = "ignored"
source = "./ignored"
`)

	stdout, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file)
	if err != nil {
		t.Fatalf("plan: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	for _, slug := range []string{"overlay", "ignored"} {
		pre, ok := findRequest(*reqs, "POST", "/api/apps/"+slug+"/deploy-preflight")
		if !ok {
			t.Fatalf("no preflight request for %s; POSTs=%v", slug, requestPaths(*reqs, "POST"))
		}
		var body struct {
			AppType string `json:"app_type"`
		}
		if err := json.Unmarshal(pre.Body, &body); err != nil {
			t.Fatalf("preflight body for %s: %v (%s)", slug, err, pre.Body)
		}
		if body.AppType != "r" {
			t.Errorf("%s: app_type = %q, want r (the type the server detects from the upload)", slug, body.AppType)
		}
	}
}
