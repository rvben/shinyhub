package cli

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// fieldReporter is the subset of *testing.T the field checker needs, so the
// teeth test can pass a recorder and observe whether drift was reported.
type fieldReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// runContractCLI runs a real CLI command end to end against the test server,
// pointing the CLI at it via env exactly as the shipped binary resolves config,
// and returns stdout. A read command that errors fails the test (all commands
// under test are expected to succeed with the seeded fixtures).
func runContractCLI(t *testing.T, host, token string, args ...string) string {
	t.Helper()
	t.Setenv("SHINYHUB_HOST", host)
	t.Setenv("SHINYHUB_TOKEN", token)
	t.Setenv("SHINYHUB_CONFIG", filepath.Join(t.TempDir(), "nonexistent.json"))
	configPathOverride = ""
	t.Cleanup(func() { configPathOverride = "" })

	out, err := execCLI(t, args...)
	if err != nil {
		t.Fatalf("%v failed: %v\noutput: %s", args, err, out)
	}
	return out
}

// TestSchema_OutputFieldsAgainstLiveServer closes the gap the CLI audit flagged
// (T2-9): schema OutputFields/EnvelopeFields are declarative and were never
// checked against real output, so a server field rename (or a handler-synthesized
// envelope key drift) is CI-invisible. The static reflection guard in
// schema_output_fields_test.go only covers apps-list/apps-show against db.App and
// leans on a hand-maintained handlerAdded allowlist; non-app read commands and the
// handler-synthesized keys are unverified.
//
// This drives the REAL CLI end to end against a REAL api.New server (the exact
// production wiring integration_test.go established) with a rich fixture set, then
// asserts every declared OutputField appears in the command's actual JSON output
// and every EnvelopeField appears on the envelope. The list commands pass the
// server's response maps straight through (renderList) and `apps show --json`
// prints the raw server envelope verbatim, so a missing key means the server
// stopped emitting the field the schema promises - the CI-invisible rename class.
func TestSchema_OutputFieldsAgainstLiveServer(t *testing.T) {
	host, token := bootContractServer(t)

	// itemAt says where each command's item object lives in the JSON output:
	//   "items"    - list envelope; the item shape is items[0]
	//   "top"      - the object is the top-level JSON (renderAction commands)
	//   "appOrTop" - apps show: fields live under .app OR at the envelope top level
	//                (the schema flattens both into OutputFields)
	type cmdCheck struct {
		key    string
		args   []string
		itemAt string
	}
	checks := []cmdCheck{
		{"whoami", []string{"whoami"}, "top"},
		{"apps list", []string{"apps", "list"}, "items"},
		{"apps show", []string{"apps", "show", "demo"}, "appOrTop"},
		{"apps deployments", []string{"apps", "deployments", "demo"}, "items"},
		{"apps access list", []string{"apps", "access", "list", "demo"}, "items"},
		{"apps access group-list", []string{"apps", "access", "group-list", "demo"}, "items"},
		{"tokens list", []string{"tokens", "list"}, "items"},
		{"env ls", []string{"env", "ls", "demo"}, "items"},
		{"data ls", []string{"data", "ls", "demo"}, "items"},
		{"schedule ls", []string{"schedule", "ls", "demo"}, "items"},
		{"schedule runs", []string{"schedule", "runs", "demo", "nightly"}, "items"},
		{"schedule status", []string{"schedule", "status", "demo"}, "items"},
		{"share ls", []string{"share", "ls", "demo"}, "items"},
		{"users list", []string{"users", "list"}, "items"},
		{"fleet status", []string{"fleet", "status"}, "items"},
	}

	// Documented, deliberate exclusion (no silent caps): apps metrics needs live
	// running replicas to populate its per-replica metrics fields, which this
	// store-seeded harness (no started processes) cannot produce. Its OutputFields
	// remain covered only by the schema-conformance tests until a process-backed
	// metrics fixture is added.
	t.Logf("excluded (needs live running replicas): apps metrics")

	appTags := jsonTagSet(reflect.TypeOf(db.App{}))

	for _, c := range checks {
		t.Run(c.key, func(t *testing.T) {
			ann, ok := schemaAnnotations[c.key]
			if !ok {
				t.Fatalf("no schema annotation for %q", c.key)
			}
			out := runContractCLI(t, host, token, c.args...)

			var top map[string]any
			if err := json.Unmarshal([]byte(out), &top); err != nil {
				t.Fatalf("%s: output is not a JSON object: %v\noutput: %s", c.key, err, out)
			}

			// EnvelopeFields (list wrapper keys) must be present at the top level.
			for _, f := range ann.EnvelopeFields {
				if _, present := top[f.Name]; !present {
					t.Errorf("%s: envelope field %q missing from live output; keys present: %v",
						c.key, f.Name, keysOf(top))
				}
			}

			// Locate the object that must carry the OutputFields.
			switch c.itemAt {
			case "top":
				assertFieldsPresent(t, c.key, "top-level", ann.OutputFields, top)
			case "items":
				item, ok := firstItem(top)
				if !ok {
					t.Fatalf("%s: expected a non-empty items[] array; got keys %v\noutput: %s",
						c.key, keysOf(top), out)
				}
				assertFieldsPresent(t, c.key, "items[0]", ann.OutputFields, item)
			case "appOrTop":
				appObj, _ := top["app"].(map[string]any)
				for _, f := range ann.OutputFields {
					_, inTop := top[f.Name]
					_, inApp := appObj[f.Name]
					if !inTop && !inApp {
						// A db.App-backed field is expected under .app; an envelope
						// field at the top. Report which so drift is actionable.
						where := "top-level or .app"
						if appTags[f.Name] {
							where = ".app"
						}
						t.Errorf("%s: output field %q missing from %s; app keys: %v, top keys: %v",
							c.key, f.Name, where, keysOf(appObj), keysOf(top))
					}
				}
			}
		})
	}
}

// assertFieldsPresent checks every declared field name is a key of obj.
func assertFieldsPresent(t fieldReporter, cmd, where string, fields []fieldSpec, obj map[string]any) {
	t.Helper()
	for _, f := range fields {
		if _, present := obj[f.Name]; !present {
			t.Errorf("%s: output field %q missing from %s; keys present: %v",
				cmd, f.Name, where, keysOf(obj))
		}
	}
}

// firstItem returns items[0] as an object, if the envelope has a non-empty items array.
func firstItem(top map[string]any) (map[string]any, bool) {
	arr, ok := top["items"].([]any)
	if !ok || len(arr) == 0 {
		return nil, false
	}
	m, ok := arr[0].(map[string]any)
	return m, ok
}

func keysOf(m map[string]any) []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSchema_LiveChecker_CatchesMissingField proves the checker has teeth: an
// expected field the object does NOT contain must be reported. Without this a
// vacuous "always passes" checker would give false confidence.
func TestSchema_LiveChecker_CatchesMissingField(t *testing.T) {
	rec := &recordingT{}
	assertFieldsPresent(rec, "fake cmd", "items[0]",
		[]fieldSpec{{Name: "present"}, {Name: "renamed_on_server"}},
		map[string]any{"present": 1})
	if !rec.failed {
		t.Fatal("checker did not flag a missing field; the live-output guard is vacuous")
	}
}

// recordingT captures whether Errorf fired, so the teeth test can assert the
// checker reports drift without failing the enclosing test. It implements the
// fieldReporter subset the checker uses.
type recordingT struct {
	failed bool
}

func (r *recordingT) Errorf(string, ...any) { r.failed = true }
func (r *recordingT) Helper()               {}

// bootContractServer stands up the production server stack (real store, real
// api.New router with a real manager+proxy) fronted by httptest, registers an
// admin-role deploy token so every read endpoint (including admin-only users) is
// reachable, and seeds a fixture per read command so no list comes back empty.
func bootContractServer(t *testing.T) (host, token string) {
	t.Helper()
	store := dbtest.New(t)
	appsDir := t.TempDir()
	dataDir := t.TempDir()
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret", DeployTokenRole: "admin"},
		Storage: config.StorageConfig{AppsDir: appsDir, AppDataDir: dataDir},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	srv := api.New(cfg, store, mgr, proxy.New())

	rawToken := "contract_admin_token_0123456789abcdef0123456789abcd"
	sysUser, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "admin")
	if err != nil {
		t.Fatalf("upsert system user: %v", err)
	}
	srv.SetDeployToken(auth.NewDeployToken(rawToken, &auth.ContextUser{
		ID:       sysUser.ID,
		Username: sysUser.Username,
		Role:     sysUser.Role,
	}))

	seedContractFixtures(t, store, cfg, dataDir, sysUser.ID)

	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts.URL, rawToken
}

// seedContractFixtures creates one representative row for every read command so
// its list/show output is non-empty and every OutputField can appear.
func seedContractFixtures(t *testing.T, store *db.Store, cfg *config.Config, dataDir string, callerID int64) {
	t.Helper()

	// Owner + a second user (for members + users list).
	mustNoErr(t, "create alice", store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "x", Role: "developer"}))
	mustNoErr(t, "create bob", store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "x", Role: "viewer"}))
	alice := mustUser(t, store, "alice")
	bob := mustUser(t, store, "bob")

	// Primary app + a source app to share into it.
	mustNoErr(t, "create demo", store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo App", OwnerID: alice.ID, Access: "private"}))
	mustNoErr(t, "create srcapp", store.CreateApp(db.CreateAppParams{Slug: "srcapp", Name: "Source", OwnerID: alice.ID, Access: "private"}))
	demo := mustApp(t, store, "demo")
	src := mustApp(t, store, "srcapp")

	// A succeeded deployment so `apps deployments` and the release fields appear.
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: demo.ID, Version: "100", BundleDir: "/tmp/demo/100", Status: "succeeded"}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// An API token owned by the caller (`tokens list` is caller-scoped, and the
	// CLI authenticates as the deploy user).
	if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: callerID, KeyHash: "dummyhash", Name: "ci-token"}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	// An env var (env ls).
	mustNoErr(t, "set env", store.UpsertAppEnvVar(demo.ID, "API_KEY", []byte("v"), false))

	// A member + a group access rule (apps access list / group-list).
	mustNoErr(t, "grant member", store.GrantAppAccessWithRole("demo", bob.ID, "viewer"))
	mustNoErr(t, "grant group", store.GrantAppGroupAccess("demo", "eng", "viewer", "manual"))

	// A share mount (share ls): srcapp -> demo.
	mustNoErr(t, "grant shared data", store.GrantSharedData(demo.ID, src.ID))

	// A schedule named "nightly" + one run (schedule ls / runs / status).
	schedID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: demo.ID, Name: "nightly", CronExpr: "0 2 * * *", CommandJSON: `["echo","hi"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	if _, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "succeeded", Trigger: "cron", StartedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("insert schedule run: %v", err)
	}

	// Mark demo fleet-managed so `fleet status` returns it.
	managedBy := "myfleet"
	mustNoErr(t, "set managed_by", store.SetAppManagedBy("demo", &managedBy))

	// A data file so `data ls` returns an item. The data handler lists files under
	// <AppDataDir>/<slug>/.
	appData := filepath.Join(dataDir, "demo")
	mustNoErr(t, "mkdir data", os.MkdirAll(appData, 0o755))
	mustNoErr(t, "write data file", os.WriteFile(filepath.Join(appData, "dataset.csv"), []byte("a,b\n1,2\n"), 0o644))
}

func mustNoErr(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func mustUser(t *testing.T, store *db.Store, username string) *db.User {
	t.Helper()
	u, err := store.GetUserByUsername(username)
	if err != nil {
		t.Fatalf("get user %s: %v", username, err)
	}
	return u
}

func mustApp(t *testing.T, store *db.Store, slug string) *db.App {
	t.Helper()
	a, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app %s: %v", slug, err)
	}
	return a
}
