package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// A deploy or rollback boots the pool on the stored settings, so it supersedes
// the last settings redeploy outcome the same way a restart does. Without that,
// a failed redeploy keeps being reported (and gating fleet applies) after a
// successful deploy already put the pool on its settings.

// failLastRedeploy launches a settings redeploy seq and records it as failed,
// the state a pool-shape change that crashed on boot leaves behind.
func failLastRedeploy(t *testing.T, store *db.Store, slug string) int64 {
	t.Helper()
	if _, err := store.DB().Exec(`UPDATE apps SET redeploy_seq_launched = redeploy_seq_launched + 1,
		last_redeploy_seq = redeploy_seq_launched + 1, last_redeploy_outcome = 'failed',
		last_redeploy_reason = 'boom', last_redeploy_at = 1 WHERE slug = ?`, slug); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	if app.LastRedeploy == nil || app.LastRedeploy.Outcome != db.RedeployFailed || app.RedeploySeqLaunched != app.LastRedeploy.Seq {
		t.Fatalf("setup: last_redeploy = %+v launched %d, want a served failed outcome", app.LastRedeploy, app.RedeploySeqLaunched)
	}
	return app.RedeploySeqLaunched
}

func (h *activationHarness) deploy(slug string) *httptest.ResponseRecorder {
	h.t.Helper()
	body, ctype := buildBundleUpload(h.t, "app.py", "print(2)\n")
	req := httptest.NewRequest("POST", "/api/apps/"+slug+"/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	rec := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rec, req)
	return rec
}

func lastRedeployOf(t *testing.T, store *db.Store, slug string) (*db.RedeployOutcome, int64) {
	t.Helper()
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	return app.LastRedeploy, app.RedeploySeqLaunched
}

func wantBootOutcome(t *testing.T, store *db.Store, slug string, seq int64, outcome, reason string) {
	t.Helper()
	got, _ := lastRedeployOf(t, store, slug)
	if got == nil || got.Seq != seq || got.Outcome != outcome || got.Reason != reason {
		t.Fatalf("last_redeploy = %+v, want {seq %d %s %q}", got, seq, outcome, reason)
	}
}

// wantResponseOutcome asserts the app a deploy or rollback returns already
// carries the outcome it recorded, so a caller reading the response does not
// see the failed outcome the boot just superseded.
func wantResponseOutcome(t *testing.T, rec *httptest.ResponseRecorder, seq int64, outcome string) {
	t.Helper()
	var body struct {
		LastRedeploy *db.RedeployOutcome `json:"last_redeploy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v: %s", err, rec.Body.String())
	}
	if got := body.LastRedeploy; got == nil || got.Seq != seq || got.Outcome != outcome {
		t.Fatalf("response last_redeploy = %+v, want seq %d %s", got, seq, outcome)
	}
}

func TestDeploy_SupersedesFailedRedeployOutcome(t *testing.T) {
	h := newActivationHarness(t, "bdeploy")
	seq := failLastRedeploy(t, h.store, "bdeploy")
	rec := h.deploy("bdeploy")
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body.String())
	}
	wantBootOutcome(t, h.store, "bdeploy", seq, db.RedeployCompleted, "deploy")
	wantResponseOutcome(t, rec, seq, db.RedeployCompleted)
}

func TestDeploy_RecordsPartialBootOutcome(t *testing.T) {
	h := newActivationHarness(t, "bpartial")
	seq := failLastRedeploy(t, h.store, "bpartial")
	h.srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		return &deploy.PoolResult{
			Replicas: []deploy.Result{{Index: 0, PID: 5000, Port: 9500, Provider: "native", Tier: "default"}},
			Failed:   []int{1},
		}, nil
	})
	if rec := h.deploy("bpartial"); rec.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body.String())
	}
	wantBootOutcome(t, h.store, "bpartial", seq, db.RedeployPartial, "deploy: 1 of 2 replicas failed to start")
}

// A deploy that keeps a stopped app down booted nothing; the stored settings
// apply at its next start, which is what skipped/not_running reports.
func TestDeploy_KeptStoppedRecordsNotRunning(t *testing.T) {
	h := newActivationHarness(t, "bstopped")
	seq := failLastRedeploy(t, h.store, "bstopped")
	if err := h.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "bstopped", Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	rec := h.deploy("bstopped")
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body.String())
	}
	wantBootOutcome(t, h.store, "bstopped", seq, db.RedeploySkipped, "not_running")
}

// A settings redeploy launched while the deploy holds the lock was not booted
// by it: it stays owed and reports on its own.
func TestDeploy_LeavesRedeployLaunchedMidDeployOwed(t *testing.T) {
	h := newActivationHarness(t, "bmid")
	seq := failLastRedeploy(t, h.store, "bmid")
	h.srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		if _, err := h.store.DB().Exec(`UPDATE apps SET redeploy_seq_launched = redeploy_seq_launched + 1 WHERE slug = 'bmid'`); err != nil {
			t.Error(err)
		}
		return &deploy.PoolResult{}, nil
	})
	if rec := h.deploy("bmid"); rec.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body.String())
	}
	got, launched := lastRedeployOf(t, h.store, "bmid")
	if launched != seq+1 || got == nil || got.Seq != seq || got.Outcome != db.RedeployFailed {
		t.Fatalf("last_redeploy = %+v launched %d, want seq %d still failed and seq %d owed", got, launched, seq, seq+1)
	}
}

func TestRollback_SupersedesFailedRedeployOutcome(t *testing.T) {
	h := newActivationHarness(t, "brollback")
	if rec := h.deploy("brollback"); rec.Code != http.StatusOK {
		t.Fatalf("second deploy: %d %s", rec.Code, rec.Body.String())
	}
	seq := failLastRedeploy(t, h.store, "brollback")
	rec := h.post("/api/apps/brollback/rollback")
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body.String())
	}
	wantBootOutcome(t, h.store, "brollback", seq, db.RedeployCompleted, "rollback")
	wantResponseOutcome(t, rec, seq, db.RedeployCompleted)
}

// Manifest configuration applied after the promotion can still fail the
// request, but the pool is already on the new bundle and the stored settings,
// so the boot outcome stands; left unrecorded, the superseded failure keeps
// warning and gating fleet applies.
func TestDeploy_RecordsBootOutcomeWhenPostPromotionConfigFails(t *testing.T) {
	h := newActivationHarness(t, "bpostcfg")
	seq := failLastRedeploy(t, h.store, "bpostcfg")
	if _, err := h.store.DB().Exec(`DROP TABLE app_group_access`); err != nil {
		t.Fatal(err)
	}
	body, ctype := buildBundleUploadFiles(t, map[string]string{
		"app.py":        "print(2)\n",
		"shinyhub.toml": "[access]\nviewer_groups = [\"finance\"]\n",
	})
	req := httptest.NewRequest("POST", "/api/apps/bpostcfg/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	rec := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "manifest access apply failed") {
		t.Fatalf("deploy: %d %s, want the manifest access failure", rec.Code, rec.Body.String())
	}
	wantBootOutcome(t, h.store, "bpostcfg", seq, db.RedeployCompleted, "deploy")
}
