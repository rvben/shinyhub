package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

// groupedProducerManifest combines an elastic pool with a deploy-triggered
// producer schedule. It deploys on a local native tier, whose workers inherit
// the server's lifetime locks, and the deploy guard rejects it anywhere else.
const groupedProducerManifest = `[app.worker]
isolation = "grouped"
grouped_size = 4
max_workers = 8

[[schedule]]
name = "refresh-data"
cron = "0 2 * * *"
cmd = "python refresh.py"
deploy_trigger = "first_deploy"
`

// containerTier is a server whose only tier runs Docker. Its processes do not
// inherit the server's lifetime locks, so producer schedules are rejected.
var containerTier = config.RuntimeConfig{Tiers: []config.TierConfig{{Name: "local", Runtime: "docker"}}}

type deployPreflightReply struct {
	Valid     bool   `json:"valid"`
	Isolation string `json:"isolation"`
	Problems  []struct {
		Stage   string `json:"stage"`
		Message string `json:"message"`
	} `json:"problems"`
}

func postDeployPreflight(t *testing.T, srv *Server, token, slug string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+slug+"/deploy-preflight", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func decodeDeployPreflight(t *testing.T, rec *httptest.ResponseRecorder) deployPreflightReply {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var reply deployPreflightReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode preflight reply: %v (%s)", err, rec.Body.String())
	}
	return reply
}

func preflightBody(t *testing.T, manifest string, settings map[string]any, appType string) string {
	t.Helper()
	body := map[string]any{"manifest": manifest, "app_type": appType}
	if settings != nil {
		body["settings"] = settings
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func deployErrorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error == "" {
		t.Fatalf("deploy rejection has no error envelope: %s", rec.Body.String())
	}
	return env.Error
}

// The preflight is only worth anything if it says exactly what deploy would
// say. Each case uploads the real bundle, records deploy's rejection, then asks
// the preflight about the same manifest and requires the identical text.
func TestDeployPreflight_MatchesDeployRejection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
		runtime  config.RuntimeConfig
		wantHint string
	}{
		{name: "producer schedule on a container tier", manifest: groupedProducerManifest, runtime: containerTier, wantHint: "local native tier"},
		{name: "malformed manifest", manifest: "[app]\nreplicas = \"many\"\n", wantHint: "shinyhub.toml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, token := newManifestE2EServerCfg(t, tc.runtime)
			seeded := seedStoppedTestApp(t, store, "reporting", "stopped", 0)

			body, ctype := buildMultiFileBundleUpload(t, map[string]string{
				"app.py":        "from shiny import App\n",
				"shinyhub.toml": tc.manifest,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/apps/reporting/deploy", body)
			req.Header.Set("Content-Type", ctype)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("deploy status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			deployMsg := deployErrorMessage(t, rec)
			if !strings.Contains(deployMsg, tc.wantHint) {
				t.Fatalf("deploy rejection %q does not mention %q", deployMsg, tc.wantHint)
			}

			reply := decodeDeployPreflight(t, postDeployPreflight(t, srv, token, "reporting",
				preflightBody(t, tc.manifest, nil, "python")))
			if reply.Valid || len(reply.Problems) != 1 {
				t.Fatalf("preflight = %+v, want exactly one problem", reply)
			}
			if reply.Problems[0].Stage != "deploy" {
				t.Fatalf("stage = %q, want deploy", reply.Problems[0].Stage)
			}
			if reply.Problems[0].Message != deployMsg {
				t.Fatalf("preflight message differs from deploy:\n preflight: %s\n deploy:    %s", reply.Problems[0].Message, deployMsg)
			}

			app, err := store.GetAppBySlug("reporting")
			if err != nil {
				t.Fatal(err)
			}
			if app.WorkerIsolation != seeded.WorkerIsolation {
				t.Fatalf("preflight changed worker_isolation from %q to %q", seeded.WorkerIsolation, app.WorkerIsolation)
			}
			if rows, err := store.ListSchedulesByApp(app.ID); err != nil || len(rows) != 0 {
				t.Fatalf("preflight created schedules: rows=%d err=%v", len(rows), err)
			}
		})
	}
}

func TestDeployPreflight_AcceptsDeployableManifest(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	seedStoppedTestApp(t, store, "reporting", "stopped", 0)

	// A producer schedule on a grouped pool deploys on a native tier: the
	// candidate producer runs behind the exclusive consumer fence and native
	// workers record their identity before they execute.
	reply := decodeDeployPreflight(t, postDeployPreflight(t, srv, token, "reporting",
		preflightBody(t, groupedProducerManifest, nil, "python")))
	if !reply.Valid || len(reply.Problems) != 0 {
		t.Fatalf("grouped producer preflight = %+v, want valid", reply)
	}
	if reply.Isolation != "grouped" {
		t.Fatalf("isolation = %q, want the projected grouped mode", reply.Isolation)
	}

	multiplexProducer := strings.Replace(groupedProducerManifest, `isolation = "grouped"`, `isolation = "multiplex"`, 1)
	reply = decodeDeployPreflight(t, postDeployPreflight(t, srv, token, "reporting",
		preflightBody(t, multiplexProducer, nil, "python")))
	if !reply.Valid || len(reply.Problems) != 0 {
		t.Fatalf("multiplex producer preflight = %+v, want valid", reply)
	}
	if reply.Isolation != "multiplex" {
		t.Fatalf("isolation = %q, want multiplex", reply.Isolation)
	}

	// A plain schedule stays deployable too: no producer semantics, nothing
	// to fence.
	plainGrouped := strings.Replace(groupedProducerManifest, `deploy_trigger = "first_deploy"`, `deploy_trigger = "never"`, 1)
	reply = decodeDeployPreflight(t, postDeployPreflight(t, srv, token, "reporting",
		preflightBody(t, plainGrouped, nil, "python")))
	if !reply.Valid || len(reply.Problems) != 0 {
		t.Fatalf("plain grouped preflight = %+v, want valid", reply)
	}
	if reply.Isolation != "grouped" {
		t.Fatalf("isolation = %q, want the projected grouped mode", reply.Isolation)
	}

	// No manifest at all is a deployable bundle.
	rec := postDeployPreflight(t, srv, token, "reporting", `{"app_type":"python"}`)
	if reply := decodeDeployPreflight(t, rec); !reply.Valid {
		t.Fatalf("manifest-less preflight = %+v, want valid", reply)
	}
}

// Fleet [app.config] is applied by PATCH after the deploy, so the preflight
// validates it against the post-deploy state and labels it separately.
func TestDeployPreflight_SettingsStageUsesServerPolicy(t *testing.T) {
	srv, store, token := newManifestE2EServerCfg(t, config.RuntimeConfig{MaxReplicas: 2})
	seedStoppedTestApp(t, store, "reporting", "stopped", 0)

	reply := decodeDeployPreflight(t, postDeployPreflight(t, srv, token, "reporting",
		preflightBody(t, "", map[string]any{"replicas": 5}, "python")))
	if reply.Valid || len(reply.Problems) != 1 {
		t.Fatalf("preflight = %+v, want one settings problem", reply)
	}
	if reply.Problems[0].Stage != "settings" || !strings.Contains(reply.Problems[0].Message, "replicas must be between 1 and 2") {
		t.Fatalf("settings problem = %+v", reply.Problems[0])
	}

	// A deploy-stage rejection is reported alone: deploy would have stopped
	// there and the settings PATCH would never have run.
	reply = decodeDeployPreflight(t, postDeployPreflight(t, srv, token, "reporting",
		preflightBody(t, "[app]\nreplicas = \"many\"\n", map[string]any{"replicas": 5}, "python")))
	if len(reply.Problems) != 1 || reply.Problems[0].Stage != "deploy" {
		t.Fatalf("preflight = %+v, want only the deploy-stage problem", reply)
	}

	// Unknown settings keys are a client bug, not a silently ignored hint.
	rec := postDeployPreflight(t, srv, token, "reporting", `{"settings":{"hibernate_timeout_minutes":5}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown settings key status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

func TestDeployPreflight_NewAppProjectsServerDefaults(t *testing.T) {
	inherited := groupedProducerManifest[strings.Index(groupedProducerManifest, "[[schedule]]"):]

	// The bundle inherits the server default (grouped) and declares a
	// producer. On a native tier that deploys, and the reply reports the mode
	// the app would be created with.
	srv, store, token := newManifestE2EServerCfg(t, config.RuntimeConfig{DefaultWorkerIsolation: "grouped"})
	if _, err := store.GetAppBySlug("brand-new"); err == nil {
		t.Fatal("fixture app must not exist")
	}
	reply := decodeDeployPreflight(t, postDeployPreflight(t, srv, token, "brand-new",
		preflightBody(t, inherited, nil, "python")))
	if !reply.Valid || len(reply.Problems) != 0 {
		t.Fatalf("new-app preflight = %+v, want valid", reply)
	}
	if reply.Isolation != "grouped" {
		t.Fatalf("isolation = %q, want the server default", reply.Isolation)
	}
	if _, err := store.GetAppBySlug("brand-new"); err == nil {
		t.Fatal("preflight created the app")
	}

	// On a container tier the deploy after create would fail, so the
	// preflight must say so without creating anything.
	srv, store, token = newManifestE2EServerCfg(t, containerTier)
	reply = decodeDeployPreflight(t, postDeployPreflight(t, srv, token, "brand-new",
		preflightBody(t, inherited, nil, "python")))
	if reply.Valid || len(reply.Problems) != 1 || !strings.Contains(reply.Problems[0].Message, "local native tier") {
		t.Fatalf("new-app preflight on a container tier = %+v, want the tier rejection", reply)
	}
	if _, err := store.GetAppBySlug("brand-new"); err == nil {
		t.Fatal("preflight created the app")
	}
}

func TestDeployPreflight_Authorization(t *testing.T) {
	srv, store, _ := newManifestE2EServer(t)
	seedStoppedTestApp(t, store, "reporting", "stopped", 0)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "onlooker", PasswordHash: hash, Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	viewer, _ := store.GetUserByUsername("onlooker")
	viewerToken, _ := auth.IssueJWT(viewer.ID, viewer.Username, viewer.Role, "test-secret")

	body := preflightBody(t, groupedProducerManifest, nil, "python")
	if rec := postDeployPreflight(t, srv, viewerToken, "reporting", body); rec.Code != http.StatusNotFound {
		t.Fatalf("viewer on existing app status=%d, want 404 (no manage rights)", rec.Code)
	}
	if rec := postDeployPreflight(t, srv, viewerToken, "brand-new", body); rec.Code != http.StatusNotFound {
		t.Fatalf("viewer on missing app status=%d, want 404 (cannot create)", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/apps/reporting/deploy-preflight", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d, want 401", rec.Code)
	}
}
