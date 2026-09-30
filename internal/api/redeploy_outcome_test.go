package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// outcomeHarness is a server over a running app with one promoted deployment
// whose pool starts are answered by a scripted deployRun.
type outcomeHarness struct {
	t     *testing.T
	srv   *Server
	store *db.Store
	app   *db.App
	token string

	mu     sync.Mutex
	calls  []deploy.Params
	result func(deploy.Params) (*deploy.PoolResult, error)
}

func newOutcomeHarness(t *testing.T, slug string, promote bool) *outcomeHarness {
	t.Helper()
	store, app := newRedeployTestStore(t, slug, "running")
	if promote {
		dep, err := store.BeginDeployment(app.ID, "v1", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PromoteDeployment(dep.ID); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, store,
		process.NewManager(t.TempDir(), process.NewNativeRuntime()), proxy.New())
	h := &outcomeHarness{t: t, srv: srv, store: store, app: app}
	h.result = func(p deploy.Params) (*deploy.PoolResult, error) { return startedPool(p.Replicas, nil), nil }
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		h.mu.Lock()
		h.calls = append(h.calls, p)
		fn := h.result
		h.mu.Unlock()
		return fn(p)
	})
	// A replica-only change resizes the live pool one slot at a time.
	srv.deployReplica = func(_ deploy.Params, index int) (*deploy.Result, error) {
		return &deploy.Result{Index: index, PID: 6000 + index, Port: 9600 + index, Provider: "native", Tier: "default"}, nil
	}
	h.token, _ = auth.IssueJWT(1, "bob", "admin", "test-secret")
	return h
}

// startedPool reports replicas started except the indexes in failed.
func startedPool(replicas int, failed []int) *deploy.PoolResult {
	down := make(map[int]bool, len(failed))
	for _, i := range failed {
		down[i] = true
	}
	res := &deploy.PoolResult{Failed: failed}
	for i := 0; i < replicas; i++ {
		if !down[i] {
			res.Replicas = append(res.Replicas, deploy.Result{Index: i, PID: 5000 + i, Port: 9500 + i, Provider: "native", Tier: "default"})
		}
	}
	return res
}

func (h *outcomeHarness) setResult(fn func(deploy.Params) (*deploy.PoolResult, error)) {
	h.mu.Lock()
	h.result = fn
	h.mu.Unlock()
}

func (h *outcomeHarness) runs() []deploy.Params {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]deploy.Params(nil), h.calls...)
}

func (h *outcomeHarness) redeploy(seq int64) {
	h.t.Helper()
	h.srv.markRedeployInFlight(h.app.Slug)
	h.srv.redeployApp(h.app.Slug, seq)
}

func (h *outcomeHarness) lastRedeploy() *db.RedeployOutcome {
	h.t.Helper()
	app, err := h.store.GetAppBySlug(h.app.Slug)
	if err != nil {
		h.t.Fatal(err)
	}
	return app.LastRedeploy
}

func (h *outcomeHarness) request(method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			h.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rec, req)
	return rec
}

func wantOutcome(t *testing.T, got *db.RedeployOutcome, seq int64, outcome, reason string) {
	t.Helper()
	if got == nil {
		t.Fatalf("last_redeploy = nil, want seq %d %s/%q", seq, outcome, reason)
	}
	if got.Seq != seq || got.Outcome != outcome || got.Reason != reason {
		t.Fatalf("last_redeploy = {seq %d %s %q}, want {seq %d %s %q}", got.Seq, got.Outcome, got.Reason, seq, outcome, reason)
	}
	if got.At.IsZero() {
		t.Fatal("last_redeploy.at is zero; an outcome must carry when it was recorded")
	}
}

func TestRedeployOutcome_CleanCycleCompletes(t *testing.T) {
	h := newOutcomeHarness(t, "clean", true)
	seq := armRedeploySeq(t, h.store, "clean")
	h.redeploy(seq)
	if n := len(h.runs()); n != 1 {
		t.Fatalf("pool starts = %d, want 1", n)
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployCompleted, "")
}

func TestRedeployOutcome_FailedReplicasArePartial(t *testing.T) {
	h := newOutcomeHarness(t, "partial", true)
	if err := h.store.UpdateAppReplicas(h.app.ID, 3); err != nil {
		t.Fatal(err)
	}
	h.setResult(func(p deploy.Params) (*deploy.PoolResult, error) { return startedPool(p.Replicas, []int{2}), nil })
	seq := armRedeploySeq(t, h.store, "partial")
	h.redeploy(seq)
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployPartial, "1 of 3 replicas failed to start")
}

func TestRedeployOutcome_PoolStartErrorFails(t *testing.T) {
	h := newOutcomeHarness(t, "boom", true)
	h.setResult(func(deploy.Params) (*deploy.PoolResult, error) { return nil, errors.New("exec: no such file") })
	seq := armRedeploySeq(t, h.store, "boom")
	h.redeploy(seq)
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployFailed, "deploy failed: exec: no such file")
	app, err := h.store.GetAppBySlug("boom")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "degraded" {
		t.Fatalf("status = %q after a failed pool start, want degraded", app.Status)
	}
}

func TestRedeployOutcome_NoDeploymentIsSkipped(t *testing.T) {
	h := newOutcomeHarness(t, "empty", false)
	seq := armRedeploySeq(t, h.store, "empty")
	h.redeploy(seq)
	if n := len(h.runs()); n != 0 {
		t.Fatalf("pool starts = %d for an app with no deployment, want 0", n)
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeploySkipped, "no_deployment")
}

func TestRedeployOutcome_ActivationInFlightIsSkipped(t *testing.T) {
	h := newOutcomeHarness(t, "activating", true)
	seedClaimedActivation(t, h.store, h.app)
	seq := armRedeploySeq(t, h.store, "activating")
	h.redeploy(seq)
	if n := len(h.runs()); n != 0 {
		t.Fatalf("pool starts = %d during a scheduled activation, want 0", n)
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeploySkipped, "activation_deferred")
}

func TestRedeployOutcome_QuarantineIsSkipped(t *testing.T) {
	h := newOutcomeHarness(t, "quarantine", true)
	pending, err := h.store.BeginDeployment(h.app.ID, "candidate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.MarkDeploymentProducerBarrierEntered(pending.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.QuarantineAndFailDeployment(pending.ID, "producer published before consumer failure"); err != nil {
		t.Fatal(err)
	}
	seq := armRedeploySeq(t, h.store, "quarantine")
	h.redeploy(seq)
	if n := len(h.runs()); n != 0 {
		t.Fatalf("pool starts = %d under compatibility quarantine, want 0", n)
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeploySkipped, "quarantined")
}

// A guard that cannot read its own state has not observed the condition it
// guards, so the cycle is a failure the caller must see, never a benign skip.
func TestRedeployOutcome_UnreadableGuardFails(t *testing.T) {
	h := newOutcomeHarness(t, "guardless", true)
	if _, err := h.store.DB().Exec(`DROP TABLE schedule_activations`); err != nil {
		t.Fatal(err)
	}
	seq := armRedeploySeq(t, h.store, "guardless")
	h.redeploy(seq)
	got := h.lastRedeploy()
	if got == nil || got.Seq != seq || got.Outcome != db.RedeployFailed {
		t.Fatalf("last_redeploy = %+v, want seq %d failed", got, seq)
	}
	if n := len(h.runs()); n != 0 {
		t.Fatalf("pool starts = %d with an unreadable guard, want 0", n)
	}
}

// Hibernation (or a stop) that lands while the redeploy waits for the lock
// makes the cycle skip; it must not boot the pool back up.
func TestRedeployOutcome_HibernatedWhileWaitingIsSkipped(t *testing.T) {
	h := newOutcomeHarness(t, "sleepy", true)
	seq := armRedeploySeq(t, h.store, "sleepy")
	release := h.srv.acquireDeployLock("sleepy")
	h.srv.markRedeployInFlight("sleepy")
	done := make(chan struct{})
	go func() {
		h.srv.redeployApp("sleepy", seq)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	if err := h.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "sleepy", Status: "hibernated"}); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("redeployApp did not finish after the lock was released")
	}
	if n := len(h.runs()); n != 0 {
		t.Fatalf("pool starts = %d for an app hibernated during the wait, want 0", n)
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeploySkipped, "not_running")
}

// Once a newer seq has been served, a straggling goroutine for an older seq
// must neither cycle the pool again nor overwrite the newer outcome.
func TestRedeployOutcome_SupersededSeqNeitherCyclesNorOverwrites(t *testing.T) {
	h := newOutcomeHarness(t, "superseded", true)
	first := armRedeploySeq(t, h.store, "superseded")
	second := armRedeploySeq(t, h.store, "superseded")
	h.redeploy(second)
	h.setResult(func(deploy.Params) (*deploy.PoolResult, error) { return nil, errors.New("must not run") })
	h.redeploy(first)
	if n := len(h.runs()); n != 1 {
		t.Fatalf("pool starts = %d, want 1: the superseded seq cycled the pool", n)
	}
	wantOutcome(t, h.lastRedeploy(), second, db.RedeployCompleted, "")
	if h.srv.isRedeployInFlight("superseded") {
		t.Fatal("a discarded seq left redeploy_in_flight set")
	}
}

// A seq committed by a process that died before serving it is owed; the next
// lifecycle owner relaunches and serves it.
func TestRedeployOutcome_RelaunchServesOwedSeq(t *testing.T) {
	h := newOutcomeHarness(t, "owed", true)
	seq := armRedeploySeq(t, h.store, "owed")
	h.srv.RelaunchOwedRedeploys()
	deadline := time.Now().Add(5 * time.Second)
	for h.lastRedeploy() == nil {
		if time.Now().After(deadline) {
			t.Fatal("owed settings redeploy was not served after RelaunchOwedRedeploys")
		}
		time.Sleep(10 * time.Millisecond)
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployCompleted, "")
	if n := len(h.runs()); n != 1 {
		t.Fatalf("pool starts = %d, want 1", n)
	}

	// Nothing is owed any more, so a second relaunch starts nothing.
	h.srv.RelaunchOwedRedeploys()
	time.Sleep(50 * time.Millisecond)
	if n := len(h.runs()); n != 1 {
		t.Fatalf("pool starts = %d after relaunching with nothing owed, want 1", n)
	}
}

// The PATCH response names the seq it launched, so a client waits for exactly
// that outcome; a PATCH that launches nothing names none.
func TestPatchApp_ReportsLaunchedRedeploySeq(t *testing.T) {
	h := newOutcomeHarness(t, "patched", true)

	rec := h.request("PATCH", "/api/apps/patched", map[string]any{"replicas": 2})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH replicas: %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if got, ok := resp["redeploy_seq"].(float64); !ok || got != 1 {
		t.Fatalf("PATCH that changes the pool shape: redeploy_seq = %v, want 1", resp["redeploy_seq"])
	}

	deadline := time.Now().Add(5 * time.Second)
	for h.srv.isRedeployInFlight("patched") {
		if time.Now().After(deadline) {
			t.Fatal("launched redeploy did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}

	rec = h.request("PATCH", "/api/apps/patched", map[string]any{"name": "Renamed"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH name: %d %s", rec.Code, rec.Body.String())
	}
	resp = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if v, ok := resp["redeploy_seq"]; ok {
		t.Fatalf("PATCH that launches nothing: redeploy_seq = %v, want absent", v)
	}

	rec = h.request("GET", "/api/apps/patched", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		App struct {
			RedeploySeqLaunched int64               `json:"redeploy_seq_launched"`
			LastRedeploy        *db.RedeployOutcome `json:"last_redeploy"`
		} `json:"app"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.App.RedeploySeqLaunched != 1 {
		t.Fatalf("GET redeploy_seq_launched = %d, want 1", got.App.RedeploySeqLaunched)
	}
	wantOutcome(t, got.App.LastRedeploy, 1, db.RedeployCompleted, "")
}

// A degraded app still serves on part of its pool, so a settings PATCH that
// changes the pool shape applies it and reports the outcome, rather than
// storing a shape the pool never takes. A replica change resizes the live pool;
// any other shape change cycles it.
func TestPatchApp_DegradedAppAppliesPoolShape(t *testing.T) {
	cases := []struct {
		name   string
		body   map[string]any
		cycles int
	}{
		{"replicas resize", map[string]any{"replicas": 2}, 0},
		{"memory cycles", map[string]any{"memory_limit_mb": 256}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOutcomeHarness(t, "limping", true)
			if err := h.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "limping", Status: "degraded"}); err != nil {
				t.Fatal(err)
			}
			rec := h.request("PATCH", "/api/apps/limping", tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("PATCH: %d %s", rec.Code, rec.Body.String())
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if got, ok := resp["redeploy_seq"].(float64); !ok || got != 1 {
				t.Fatalf("PATCH on a degraded app: redeploy_seq = %v, want 1", resp["redeploy_seq"])
			}
			deadline := time.Now().Add(5 * time.Second)
			for h.lastRedeploy() == nil {
				if time.Now().After(deadline) {
					t.Fatal("the degraded app's settings redeploy never reported")
				}
				time.Sleep(10 * time.Millisecond)
			}
			wantOutcome(t, h.lastRedeploy(), 1, db.RedeployCompleted, "")
			if n := len(h.runs()); n != tc.cycles {
				t.Fatalf("pool cycles = %d, want %d", n, tc.cycles)
			}
			if tc.cycles == 0 {
				rows, err := h.store.ListReplicas(h.app.ID)
				if err != nil || len(rows) != 2 {
					t.Fatalf("resize did not reach 2 replicas: rows=%v err=%v", rows, err)
				}
			}
		})
	}
}

// A restart queued behind a settings PATCH boots the settings that PATCH
// committed, and its outcome serves the seq it booted.
func TestRestart_BootsSettingsCommittedWhileWaiting(t *testing.T) {
	h := newOutcomeHarness(t, "queued", true)
	release := h.srv.acquireDeployLock("queued")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- h.request("POST", "/api/apps/queued/restart", nil) }()
	time.Sleep(50 * time.Millisecond)

	// The settings commit that raced ahead of the restart.
	if err := h.store.UpdateAppReplicas(h.app.ID, 3); err != nil {
		t.Fatal(err)
	}
	seq := armRedeploySeq(t, h.store, "queued")
	release()

	var rec *httptest.ResponseRecorder
	select {
	case rec = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not finish after the lock was released")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("restart: %d %s", rec.Code, rec.Body.String())
	}
	runs := h.runs()
	if len(runs) != 1 || runs[0].Replicas != 3 {
		t.Fatalf("restart made %d pool starts (first with %d replicas), want one start with 3", len(runs), firstReplicas(runs))
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployCompleted, "restart")

	// The launched redeploy for that seq now finds it served.
	h.redeploy(seq)
	if n := len(h.runs()); n != 1 {
		t.Fatalf("pool starts = %d, want 1: the seq the restart served was cycled again", n)
	}
}

func TestRestart_RecordsPartialOutcome(t *testing.T) {
	h := newOutcomeHarness(t, "rpartial", true)
	if err := h.store.UpdateAppReplicas(h.app.ID, 2); err != nil {
		t.Fatal(err)
	}
	seq := armRedeploySeq(t, h.store, "rpartial")
	h.setResult(func(p deploy.Params) (*deploy.PoolResult, error) { return startedPool(p.Replicas, []int{1}), nil })
	if rec := h.request("POST", "/api/apps/rpartial/restart", nil); rec.Code != http.StatusOK {
		t.Fatalf("restart: %d %s", rec.Code, rec.Body.String())
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployPartial, "restart: 1 of 2 replicas failed to start")
}

func TestRestart_RecordsFailedOutcome(t *testing.T) {
	h := newOutcomeHarness(t, "rfail", true)
	seq := armRedeploySeq(t, h.store, "rfail")
	h.setResult(func(deploy.Params) (*deploy.PoolResult, error) { return nil, errors.New("port in use") })
	if rec := h.request("POST", "/api/apps/rfail/restart", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("restart: %d %s, want 500", rec.Code, rec.Body.String())
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployFailed, "restart failed: port in use")
}

func firstReplicas(runs []deploy.Params) int {
	if len(runs) == 0 {
		return -1
	}
	return runs[0].Replicas
}

// failOutcomeWrites makes every write of a redeploy outcome fail until the
// returned drop is called.
func failOutcomeWrites(t *testing.T, store *db.Store) func() {
	return installDBFailureTrigger(t, store, dbFailureTrigger{
		name:      "fail_outcome_write",
		table:     "apps",
		event:     "UPDATE",
		condition: "NEW.last_redeploy_seq <> OLD.last_redeploy_seq",
	})
}

// A transient failure recording the outcome is retried: an unrecorded seq
// stays owed, and clients waiting on it would report the settings as not
// applied although the cycle completed.
func TestRedeploy_RetriesTransientOutcomeWriteFailure(t *testing.T) {
	h := newOutcomeHarness(t, "owretry", true)
	seq := armRedeploySeq(t, h.store, "owretry")
	drop := failOutcomeWrites(t, h.store)
	var waits []time.Duration
	h.srv.outcomeRetrySleep = func(d time.Duration) {
		waits = append(waits, d)
		drop()
	}
	h.redeploy(seq)
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployCompleted, "")
	if len(waits) != 1 {
		t.Fatalf("retries = %d, want 1: the write succeeds on the first retry", len(waits))
	}
}

// A failure that outlasts the backoff gives up rather than holding the deploy
// lock forever; the seq stays owed and a restart serves it.
func TestRedeploy_OutcomeWriteGivesUpAfterBackoff(t *testing.T) {
	h := newOutcomeHarness(t, "owgiveup", true)
	seq := armRedeploySeq(t, h.store, "owgiveup")
	failOutcomeWrites(t, h.store)
	var waits []time.Duration
	h.srv.outcomeRetrySleep = func(d time.Duration) { waits = append(waits, d) }
	h.redeploy(seq)
	if got := h.lastRedeploy(); got != nil {
		t.Fatalf("last_redeploy = %+v, want none recorded", got)
	}
	if len(waits) != len(outcomeWriteBackoff) {
		t.Fatalf("retries = %d, want %d", len(waits), len(outcomeWriteBackoff))
	}
}

// A transient failure taking the claim is retried too. Abandoning it would
// leave the old pool running with the seq owed until the next ownership
// change, and re-sending the same settings cannot recover it because they
// already match and arm nothing. The claim is idempotent, so a retry after an
// ambiguous commit takes the same claim again.
func TestRedeploy_RetriesTransientClaimFailure(t *testing.T) {
	h := newOutcomeHarness(t, "clretry", true)
	seq := armRedeploySeq(t, h.store, "clretry")
	drop := installDBFailureTrigger(t, h.store, dbFailureTrigger{
		name:      "fail_claim_write",
		table:     "apps",
		event:     "UPDATE",
		condition: "NEW.redeploy_claim_seq <> OLD.redeploy_claim_seq",
	})
	retries := 0
	h.srv.outcomeRetrySleep = func(time.Duration) { retries++; drop() }
	h.redeploy(seq)
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployCompleted, "")
	if retries != 1 {
		t.Fatalf("retries = %d, want 1: the claim succeeds on the first retry", retries)
	}
}

func TestRestart_RetriesTransientOutcomeWriteFailure(t *testing.T) {
	h := newOutcomeHarness(t, "rwretry", true)
	seq := armRedeploySeq(t, h.store, "rwretry")
	drop := failOutcomeWrites(t, h.store)
	retries := 0
	h.srv.outcomeRetrySleep = func(time.Duration) { retries++; drop() }
	if rec := h.request("POST", "/api/apps/rwretry/restart", nil); rec.Code != http.StatusOK {
		t.Fatalf("restart: %d %s", rec.Code, rec.Body.String())
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployCompleted, "restart")
	if retries != 1 {
		t.Fatalf("retries = %d, want 1", retries)
	}
}

// A stale lease means a successor owns the app: retrying cannot succeed and
// only holds the deploy lock, so it stops at once.
func TestRetryRedeployWrite_StopsOnStaleLease(t *testing.T) {
	srv := &Server{}
	slept := 0
	srv.outcomeRetrySleep = func(time.Duration) { slept++ }
	calls := 0
	_, err := srv.retryRedeployStore(func() (bool, error) {
		calls++
		return false, db.ErrOwnerFenced
	})
	if !errors.Is(err, db.ErrOwnerFenced) || calls != 1 || slept != 0 {
		t.Fatalf("err %v, calls %d, sleeps %d; want ErrOwnerFenced after one call and no wait", err, calls, slept)
	}
}

// A settings PATCH that commits while a rollback waits for the deploy lock
// arms a redeploy the rollback then serves. The rollback must boot the
// settings that seq names, not the row it read to authorize the request, or
// the seq is recorded completed while the pool runs the old shape and the
// queued redeploy that would have applied it finds nothing owed.
func TestRollback_BootsSettingsCommittedWhileQueued(t *testing.T) {
	h := newOutcomeHarness(t, "rbqueued", false)
	for _, v := range []string{"v1", "v2"} {
		dep, err := h.store.BeginDeployment(h.app.ID, v, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.RecordDeploymentScheduleSnapshot(dep.ID, h.app.ID); err != nil {
			t.Fatal(err)
		}
		if err := h.store.PromoteDeployment(dep.ID); err != nil {
			t.Fatal(err)
		}
	}
	// The rollback reads its body after the authorization read and before
	// the deploy lock; holding the body there lets the PATCH commit in
	// between, as one queued ahead of it on the lock would.
	body := &blockingBody{entered: make(chan struct{}), resume: make(chan struct{})}
	req := httptest.NewRequest("POST", "/api/apps/rbqueued/rollback", body)
	req.Header.Set("Authorization", "Bearer "+h.token)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.srv.Router().ServeHTTP(rec, req)
		done <- rec
	}()
	<-body.entered
	patched, err := h.store.PatchAppSettings(db.PatchAppSettingsParams{Slug: "rbqueued", SetReplicas: true, Replicas: 3, ArmRedeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if patched.RedeploySeq == 0 {
		t.Fatalf("setup: PATCH armed no redeploy (%+v)", patched)
	}
	seq := patched.RedeploySeq
	close(body.resume)
	if rec := <-done; rec.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body.String())
	}
	runs := h.runs()
	if len(runs) == 0 || runs[len(runs)-1].Replicas != 3 {
		t.Fatalf("rollback booted %+v, want the committed replicas 3", runs)
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployCompleted, "rollback")
}

// blockingBody signals when a handler starts reading it and returns EOF only
// once resumed.
type blockingBody struct{ entered, resume chan struct{} }

func (b *blockingBody) Read([]byte) (int, error) {
	close(b.entered)
	<-b.resume
	return 0, io.EOF
}

// A restart or rollback authorizes one app and then waits for the deploy
// lock. If that app is deleted and a different one created under the same
// slug meanwhile, the handler's re-read under the lock returns the newcomer,
// which the caller was never authorized to manage and did not ask to cycle.
// The handler must refuse it rather than boot it.
func TestLockedHandlers_RefuseAppReplacedWhileQueued(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"restart", "/restart"},
		{"rollback", "/rollback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slug := "swap" + tc.name
			h := newOutcomeHarness(t, slug, false)
			for _, v := range []string{"v1", "v2"} {
				dep, err := h.store.BeginDeployment(h.app.ID, v, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if err := h.store.PromoteDeployment(dep.ID); err != nil {
					t.Fatal(err)
				}
			}
			release := h.srv.acquireDeployLock(slug)
			req := httptest.NewRequest("POST", "/api/apps/"+slug+tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+h.token)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rec := httptest.NewRecorder()
				h.srv.Router().ServeHTTP(rec, req)
				done <- rec
			}()
			waitParkedOnDeployLock(t, done)

			if err := h.store.DeleteApp(slug); err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.CreateApp(db.CreateAppParams{Slug: slug, Name: "replacement", OwnerID: h.app.OwnerID}); err != nil {
				t.Fatal(err)
			}
			other, err := h.store.GetAppBySlug(slug)
			if err != nil {
				t.Fatal(err)
			}
			if other.ID == h.app.ID {
				t.Fatalf("setup: replacement reused id %d", other.ID)
			}
			dep, err := h.store.BeginDeployment(other.ID, "r1", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := h.store.PromoteDeployment(dep.ID); err != nil {
				t.Fatal(err)
			}
			release()

			rec := <-done
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s: %d %s, want 404 for the replaced app", tc.name, rec.Code, rec.Body.String())
			}
			if runs := h.runs(); len(runs) != 0 {
				t.Fatalf("%s booted %+v for an app it never authorized", tc.name, runs)
			}
		})
	}
}

// waitParkedOnDeployLock returns once a handler goroutine is blocked
// acquiring the per-app operation lock, so everything it read before the
// lock is already fixed.
func waitParkedOnDeployLock(t *testing.T, done <-chan *httptest.ResponseRecorder) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		select {
		case rec := <-done:
			t.Fatalf("handler returned before the lock: %d %s", rec.Code, rec.Body.String())
		default:
		}
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(g, ".acquireDeployLock(") &&
				(strings.Contains(g, ".handleRestartApp(") || strings.Contains(g, ".handleRollbackApp(")) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("handler never parked on the deploy lock")
}

// The owed scan runs once per ownership span, and re-sending the same
// settings arms nothing, so a transient read failure that abandoned it would
// leave every owed redeploy unserved until the next ownership change. The
// scan is retried like the redeploy's own writes.
func TestRedeployOutcome_RelaunchRetriesTransientScanFailure(t *testing.T) {
	h := newOutcomeHarness(t, "scanretry", true)
	seq := armRedeploySeq(t, h.store, "scanretry")
	if _, err := h.store.DB().Exec(`ALTER TABLE apps RENAME TO apps_scan_fault`); err != nil {
		t.Fatal(err)
	}
	retries := 0
	h.srv.outcomeRetrySleep = func(time.Duration) {
		retries++
		if retries == 1 {
			if _, err := h.store.DB().Exec(`ALTER TABLE apps_scan_fault RENAME TO apps`); err != nil {
				t.Error(err)
			}
		}
	}
	h.srv.RelaunchOwedRedeploys()
	if retries == 0 {
		if _, err := h.store.DB().Exec(`ALTER TABLE apps_scan_fault RENAME TO apps`); err != nil {
			t.Error(err)
		}
		t.Fatal("the failed owed scan was not retried, so the owed redeploy is abandoned")
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.lastRedeploy() == nil {
		if time.Now().After(deadline) {
			t.Fatal("owed settings redeploy was not served after a transient scan failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	wantOutcome(t, h.lastRedeploy(), seq, db.RedeployCompleted, "")
	if retries != 1 {
		t.Fatalf("retries = %d, want 1: the scan succeeds on the first retry", retries)
	}
}
