package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fleet"
)

func TestAvailabilityDeploymentPolicyAndSparseUpdates(t *testing.T) {
	srv, s, token := newManifestE2EServer(t)
	owner, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	manifest := "[app]\navailability='public'\n"
	if rec := deployManifest(t, srv, token, "demo", manifest); rec.Code != 400 {
		t.Fatalf("denied deploy: %d %s", rec.Code, rec.Body)
	}
	a, err := s.GetAppBySlug("demo")
	if err != nil || a.Availability != "off" || a.DeployCount != 0 {
		t.Fatalf("denied deployment mutated app: %+v %v", a, err)
	}
	srv.cfg.Server.AppAvailabilityOptIn = "allow"
	if rec := deployManifest(t, srv, token, "demo", manifest); rec.Code != 200 {
		t.Fatalf("opt-in deploy: %d %s", rec.Code, rec.Body)
	}
	a, err = s.GetAppBySlug("demo")
	if err != nil || a.Availability != "public" {
		t.Fatalf("opt-in not persisted: %+v %v", a, err)
	}
	revision := appResourceRevision(a)
	if rec := deployManifest(t, srv, token, "demo", "[app]\nname='Renamed'\n"); rec.Code != 200 {
		t.Fatalf("sparse deploy: %d %s", rec.Code, rec.Body)
	}
	a, err = s.GetAppBySlug("demo")
	if err != nil || a.Availability != "public" {
		t.Fatalf("omission revoked opt-in: %+v %v", a, err)
	}
	patch := func(body, credential string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PATCH", "/api/apps/demo", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+credential)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	for _, body := range []string{`{"availability":null}`, `{"availability":true}`, `{"availability":"yes"}`} {
		if rec := patch(body, token); rec.Code != 400 {
			t.Fatalf("invalid availability accepted: %d %s", rec.Code, rec.Body)
		}
	}
	srv.cfg.Server.AppAvailabilityOptIn = "deny"
	if rec := patch(`{"availability":"public"}`, token); rec.Code != 200 {
		t.Fatalf("unchanged public setting rejected under deny: %d %s", rec.Code, rec.Body)
	}
	if rec := deployManifest(t, srv, token, "demo", manifest); rec.Code != 200 {
		t.Fatalf("unchanged public redeploy rejected under deny: %d %s", rec.Code, rec.Body)
	}
	a, err = s.GetAppBySlug("demo")
	if err != nil || a.Name != "Renamed" {
		t.Fatalf("invalid patch partially applied: %+v %v", a, err)
	}
	if rec := patch(`{"availability":"off"}`, "invalid"); rec.Code != 401 {
		t.Fatalf("unauthorized patch: %d %s", rec.Code, rec.Body)
	}
	if rec := patch(`{"availability":"off"}`, token); rec.Code != 200 {
		t.Fatalf("revocation under deny: %d %s", rec.Code, rec.Body)
	}
	a, err = s.GetAppBySlug("demo")
	if err != nil || a.Availability != "off" || appResourceRevision(a) == revision {
		t.Fatalf("revocation not reflected: %+v %v", a, err)
	}
	if rec := patch(`{"availability":"public","name":"Should not persist"}`, token); rec.Code != 400 {
		t.Fatalf("new opt-in under deny: %d %s", rec.Code, rec.Body)
	}
	a, err = s.GetAppBySlug("demo")
	if err != nil || a.Name != "Renamed" || a.Availability != "off" {
		t.Fatalf("denied patch partially applied: %+v %v", a, err)
	}
	// A setting-only change participates in revisions even within one timestamp tick.
	before := appResourceRevision(a)
	a.Availability = "public"
	if before == appResourceRevision(a) {
		t.Fatal("availability omitted from optimistic concurrency fingerprint")
	}
}

func TestAvailabilityIntentThroughStopDeployAndFailedStart(t *testing.T) {
	srv, s, token := newManifestE2EServer(t)
	owner, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if rec := deployManifest(t, srv, token, "demo", "[app]\nname='Demo'\n"); rec.Code != 200 {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body)
	}
	action := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/apps/demo/"+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	check := func(want bool) {
		t.Helper()
		a, err := s.GetAppAvailability(context.Background(), "demo")
		if err != nil || a.OperatorStopped != want {
			t.Fatalf("intent=%v: %+v %v", want, a, err)
		}
	}
	if rec := action("stop"); rec.Code != 200 {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body)
	}
	check(true)
	// A draft rejected before runtime mutation is not an accepted start.
	body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": "from shiny import App\n"})
	req := httptest.NewRequest("POST", "/api/apps/demo/deploy?start=true", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	req = req.WithContext(context.WithValue(req.Context(), draftDigestKey{}, "sha256:changed"))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 409 {
		t.Fatalf("draft refusal: %d %s", rec.Code, rec.Body)
	}
	check(true)
	if rec := deployManifest(t, srv, token, "demo", "[app]\nname='Prepared while stopped'\n"); rec.Code != 200 {
		t.Fatalf("stopped deploy: %d %s", rec.Code, rec.Body)
	}
	check(true)
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) { return nil, errors.New("boot failure") })
	if rec := action("restart"); rec.Code != 500 {
		t.Fatalf("failed start: %d %s", rec.Code, rec.Body)
	}
	check(false)
	// Stopping the failed app explicitly establishes new intent, even if already stopped.
	if rec := action("stop"); rec.Code != 200 {
		t.Fatalf("second stop: %d %s", rec.Code, rec.Body)
	}
	check(true)
	if rec := action("rollback"); rec.Code != 500 {
		t.Fatalf("failed rollback: %d %s", rec.Code, rec.Body)
	}
	check(false)
	if rec := action("stop"); rec.Code != 200 {
		t.Fatalf("stop before delete: %d %s", rec.Code, rec.Body)
	}
	deleteReq := httptest.NewRequest("DELETE", "/api/apps/demo", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+token)
	deleted := httptest.NewRecorder()
	srv.Router().ServeHTTP(deleted, deleteReq)
	if deleted.Code != 200 {
		t.Fatalf("delete operator-stopped app: %d %s", deleted.Code, deleted.Body)
	}
}

func TestAvailabilityFailedFirstDeploymentDoesNotInventStopIntent(t *testing.T) {
	srv, s, token := newManifestE2EServer(t)
	srv.cfg.Server.AppAvailabilityOptIn = "allow"
	owner, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateApp(db.CreateAppParams{Slug: "boom", Name: "Boom", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) { return nil, errors.New("boot failure") })
	if rec := deployManifest(t, srv, token, "boom", "[app]\navailability='public'\n"); rec.Code != 500 {
		t.Fatalf("failed deploy: %d %s", rec.Code, rec.Body)
	}
	a, err := s.GetAppAvailability(context.Background(), "boom")
	if err != nil || a.OperatorStopped || a.Status != "stopped" || a.Availability != "public" {
		t.Fatalf("failed first deployment misclassified or metadata lost: %+v %v", a, err)
	}
}

func TestAvailabilityCreateAndPolicyPreflight(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	create := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/apps", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	const body = `{"slug":"enrolled","name":"Enrolled","availability":"public"}`
	if rec := create(body); rec.Code != 400 {
		t.Fatalf("create bypassed policy: %d %s", rec.Code, rec.Body)
	}
	if _, err := store.GetAppBySlug("enrolled"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("denied create mutated store: %v", err)
	}
	srv.cfg.Server.AppAvailabilityOptIn = "allow"
	if rec := create(body); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	app, err := store.GetAppAvailability(context.Background(), "enrolled")
	if err != nil || app.Availability != "public" || app.OperatorStopped {
		t.Fatalf("initial enrollment: %+v %v", app, err)
	}
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) { return nil, errors.New("boot failure") })
	if rec := deployManifest(t, srv, token, "enrolled", "[app]\nname='Enrolled'\n"); rec.Code != 500 {
		t.Fatalf("failed boot: %d %s", rec.Code, rec.Body)
	}
	app, err = store.GetAppAvailability(context.Background(), "enrolled")
	if err != nil || app.Availability != "public" || app.OperatorStopped {
		t.Fatalf("enrollment lost after failed boot: %+v %v", app, err)
	}
	srv.cfg.Server.AppAvailabilityOptIn = "deny"
	check := func(manifest string, settings map[string]any) deployPreflightResponse {
		rec := postDeployPreflight(t, srv, token, "enrolled", preflightBody(t, manifest, settings, "python"))
		var reply deployPreflightResponse
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &reply) != nil {
			t.Fatalf("preflight: %d %s", rec.Code, rec.Body)
		}
		return reply
	}
	if reply := check("", map[string]any{"availability": "public"}); !reply.Valid || len(reply.Warnings) != 1 {
		t.Fatalf("suppression warning missing: %+v", reply)
	}
	if reply := check("[app]\navailability='off'\n", map[string]any{"availability": "public"}); reply.Valid || len(reply.Problems) != 1 || reply.Problems[0].Stage != "settings" {
		t.Fatalf("projected policy bypass: %+v", reply)
	}
	if reply := check("", map[string]any{"availability": "off"}); !reply.Valid || len(reply.Warnings) != 0 {
		t.Fatalf("revocation preflight: %+v", reply)
	}
}

// Pause at the first body read, after authorization has captured app identity
// and before a mutation can acquire its operation lock.
type availabilityBlockedBody struct {
	io.Reader
	started chan struct{}
	resume  <-chan struct{}
	once    sync.Once
}

func (b *availabilityBlockedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.resume })
	return b.Reader.Read(p)
}
func (b *availabilityBlockedBody) Close() error { return nil }

func TestAvailabilityMutationPinsAuthorizedIdentity(t *testing.T) {
	for _, method := range []string{"PATCH", "POST"} {
		t.Run(method, func(t *testing.T) {
			srv, store, token := newManifestE2EServer(t)
			srv.cfg.Server.AppAvailabilityOptIn = "allow"
			owner, err := store.GetUserByUsername("admin")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateApp(db.CreateAppParams{Slug: "reused", Name: "Original", OwnerID: owner.ID}); err != nil {
				t.Fatal(err)
			}
			original, err := store.GetAppBySlug("reused")
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/apps/reused"
			var reader io.Reader = strings.NewReader(`{"availability":"public"}`)
			contentType := "application/json"
			if method == "POST" {
				path += "/deploy"
				reader, contentType = buildMultiFileBundleUpload(t, map[string]string{"app.py": "from shiny import App\n", "shinyhub.toml": "[app]\navailability='public'\n"})
			}
			started, resume := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(resume) }) }
			t.Cleanup(release)
			req := httptest.NewRequest(method, path, &availabilityBlockedBody{Reader: reader, started: started, resume: resume})
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("Authorization", "Bearer "+token)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { rec := httptest.NewRecorder(); srv.Router().ServeHTTP(rec, req); done <- rec }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not reach body read")
			}
			if err := store.DeleteApp("reused"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateApp(db.CreateAppParams{Slug: "reused", Name: "Replacement", OwnerID: owner.ID}); err != nil {
				t.Fatal(err)
			}
			replacement, err := store.GetAppBySlug("reused")
			if err != nil || replacement.ID == original.ID {
				t.Fatalf("replacement: %+v %v", replacement, err)
			}
			release()
			select {
			case rec := <-done:
				if rec.Code != 404 || !strings.Contains(rec.Body.String(), "not found") {
					t.Fatalf("stale authorization affected replacement: %d %s", rec.Code, rec.Body)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("handler did not return")
			}
			replacement, err = store.GetAppBySlug("reused")
			if err != nil || replacement.Availability != "off" || replacement.DeployCount != 0 {
				t.Fatalf("replacement mutated: %+v %v", replacement, err)
			}
		})
	}
}

func TestAvailabilityPolicyUsesLockedCurrentState(t *testing.T) {
	for _, method := range []string{"PATCH", "POST"} {
		t.Run(method, func(t *testing.T) {
			srv, store, token := newManifestE2EServer(t)
			owner, err := store.GetUserByUsername("admin")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID, Availability: "public"}); err != nil {
				t.Fatal(err)
			}
			started, resume := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(resume) }) }
			t.Cleanup(release)
			path := "/api/apps/demo"
			var reader io.Reader = strings.NewReader(`{"availability":"public"}`)
			contentType := "application/json"
			if method == "POST" {
				path += "/deploy"
				reader, contentType = buildMultiFileBundleUpload(t, map[string]string{"app.py": "from shiny import App\n", "shinyhub.toml": "[app]\navailability='public'\n"})
			}
			req := httptest.NewRequest(method, path, &availabilityBlockedBody{Reader: reader, started: started, resume: resume})
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("Authorization", "Bearer "+token)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { rec := httptest.NewRecorder(); srv.Router().ServeHTTP(rec, req); done <- rec }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not reach body read")
			}
			if _, err := store.PatchAppSettings(db.PatchAppSettingsParams{Slug: "demo", SetAvailability: true, Availability: "off"}); err != nil {
				t.Fatal(err)
			}
			release()
			select {
			case rec := <-done:
				if rec.Code != 400 {
					t.Fatalf("stale public state bypassed deny policy: %d %s", rec.Code, rec.Body)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("handler did not return")
			}
			app, err := store.GetAppBySlug("demo")
			if err != nil || app.Availability != "off" {
				t.Fatalf("revocation undone: %+v %v", app, err)
			}
		})
	}
}

func TestAvailabilityQueuedActivationReadsCurrentStopIntent(t *testing.T) {
	for _, action := range []string{"restart", "rollback"} {
		t.Run(action, func(t *testing.T) {
			srv, store, token := newManifestE2EServer(t)
			owner, err := store.GetUserByUsername("admin")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
				t.Fatal(err)
			}
			for _, manifest := range []string{"[app]\nname='First'\n", "[app]\nname='Second'\n"} {
				if rec := deployManifest(t, srv, token, "demo", manifest); rec.Code != 200 {
					t.Fatalf("deploy: %d %s", rec.Code, rec.Body)
				}
			}
			unlock := srv.acquireDeployLock("demo")
			var once sync.Once
			release := func() { once.Do(unlock) }
			defer release()
			req := httptest.NewRequest("POST", "/api/apps/demo/"+action, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { rec := httptest.NewRecorder(); srv.Router().ServeHTTP(rec, req); done <- rec }()
			waitParkedOnDeployLock(t, done)
			// Simulate a stop winning the operation fence after the authorization read.
			if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "stopped", OperatorStopped: true}); err != nil {
				t.Fatal(err)
			}
			srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) {
				return nil, errors.New("boot failure after concurrent stop")
			})
			release()
			select {
			case rec := <-done:
				if rec.Code != 500 {
					t.Fatalf("failed %s: %d %s", action, rec.Code, rec.Body)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("activation did not return")
			}
			app, err := store.GetAppAvailability(context.Background(), "demo")
			if err != nil || app.OperatorStopped {
				t.Fatalf("accepted activation preserved stale stop intent: %+v %v", app, err)
			}
		})
	}
}

func TestAvailabilityRollbackPreservesRevocationAndRefusalPreservesStop(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	srv.cfg.Server.AppAvailabilityOptIn = "allow"
	owner, err := store.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	for _, manifest := range []string{"[app]\navailability='public'\n", "[app]\navailability='off'\n"} {
		if rec := deployManifest(t, srv, token, "demo", manifest); rec.Code != 200 {
			t.Fatalf("deploy: %d %s", rec.Code, rec.Body)
		}
	}
	post := func(action string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/apps/demo/"+action, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	if rec := post("stop"); rec.Code != 200 {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body)
	}
	srv.cfg.Server.AppAvailabilityOptIn = "deny"
	if rec := post("rollback"); rec.Code != 200 {
		t.Fatalf("rollback restored old exposure declaration: %d %s", rec.Code, rec.Body)
	}
	app, err := store.GetAppAvailability(context.Background(), "demo")
	if err != nil || app.Availability != "off" || app.OperatorStopped {
		t.Fatalf("rollback undid revocation: %+v %v", app, err)
	}
	if rec := post("stop"); rec.Code != 200 {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body)
	}
	record, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	deps, err := store.ListRecentDeployments(record.ID, 2)
	if err != nil || len(deps) != 2 {
		t.Fatalf("history: %+v %v", deps, err)
	}
	if err := os.RemoveAll(deps[1].BundleDir); err != nil {
		t.Fatal(err)
	}
	if rec := post("rollback"); rec.Code != 409 {
		t.Fatalf("missing target refusal: %d %s", rec.Code, rec.Body)
	}
	app, err = store.GetAppAvailability(context.Background(), "demo")
	if err != nil || !app.OperatorStopped || app.Availability != "off" {
		t.Fatalf("refusal changed intent/exposure: %+v %v", app, err)
	}
}

func TestAvailabilityPatchAuditsChangesExactlyOnce(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	srv.cfg.Server.AppAvailabilityOptIn = "allow"
	owner, err := store.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		value, old string
		count      int
	}{{"public", "off", 1}, {"public", "off", 1}, {"off", "public", 2}, {"off", "public", 2}} {
		req := httptest.NewRequest("PATCH", "/api/apps/demo", strings.NewReader(`{"availability":"`+step.value+`"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("patch %s: %d %s", step.value, rec.Code, rec.Body)
		}
		events, err := store.ListAuditEvents("update_app", 10, 0)
		if err != nil || len(events) != step.count {
			t.Fatalf("patch %s audit count=%d want=%d err=%v", step.value, len(events), step.count, err)
		}
		var detail struct {
			Availability struct {
				Old string `json:"old"`
				New string `json:"new"`
			} `json:"availability"`
		}
		if json.Unmarshal([]byte(events[0].Detail), &detail) != nil || detail.Availability.Old != step.old || detail.Availability.New != step.value || events[0].ResourceID != "demo" {
			t.Fatalf("incorrect exposure audit: %+v", events[0])
		}
	}
}

func TestAvailabilityPatchDoesNotArmRedeploy(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	srv.cfg.Server.AppAvailabilityOptIn = "allow"
	owner, err := store.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if rec := deployManifest(t, srv, token, "demo", "[app]\nname='Demo'\n"); rec.Code != 200 {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body)
	}
	before, err := store.GetAppBySlug("demo")
	if err != nil || before.Status != "running" {
		t.Fatalf("running app: %+v %v", before, err)
	}
	for _, value := range []string{"public", "off"} {
		req := httptest.NewRequest("PATCH", "/api/apps/demo", strings.NewReader(`{"availability":"`+value+`"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("patch %s: %d %s", value, rec.Code, rec.Body)
		}
		var reply map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		if _, armed := reply["redeploy_seq"]; armed {
			t.Fatalf("availability-only patch armed redeploy: %s", rec.Body)
		}
		after, err := store.GetAppBySlug("demo")
		if err != nil || after.Availability != value || after.Status != before.Status || after.RedeploySeqLaunched != before.RedeploySeqLaunched || after.DeployCount != before.DeployCount {
			t.Fatalf("availability-only patch changed execution: before=%+v after=%+v err=%v", before, after, err)
		}
		// Saved-settings exports describe the current stored value, including a
		// revocation. They must not replace public with a hardcoded default.
		bundle, err := deploy.ParseManifest([]byte(configurationSavedTOML(after, false)))
		if err != nil || bundle.App.Availability == nil || *bundle.App.Availability != value {
			t.Fatalf("bundle export lost saved availability: %+v %v", bundle, err)
		}
		var fragment struct {
			App []struct {
				Config fleet.Config `toml:"config"`
			} `toml:"app"`
		}
		if _, err := toml.Decode(configurationSavedTOML(after, true), &fragment); err != nil || len(fragment.App) != 1 || fragment.App[0].Config.Availability == nil || *fragment.App[0].Config.Availability != value {
			t.Fatalf("fleet export lost saved availability: %+v %v", fragment, err)
		}
	}
}
