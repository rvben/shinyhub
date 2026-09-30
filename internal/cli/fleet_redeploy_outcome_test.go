package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fleet"
)

// redeployFake serves one app the way a redeploy_outcome server does: a PATCH
// that changes a pool-shape key on a running app arms a settings redeploy
// (redeploy_seq_launched advances), and the outcome is reported on a later
// read, after `delay` reads have seen it still owed. What the outcome is, and
// what status the app lands in, comes from script.
type redeployFake struct {
	t        *testing.T
	mu       sync.Mutex
	app      *fakeApp
	launched int64
	served   int64
	outcome  string
	reason   string
	delay    int
	pending  int
	// script decides the outcome of seq and the app status it leaves behind
	// ("" keeps the current status).
	script func(seq int64) (outcome, reason, status string)
	// patchCode answers the nth PATCH (1-based); the write is stored and the
	// redeploy armed even for a 5xx, as a late failure after commit does.
	patchCode func(n int) int
	// onGet runs on every app read with the lock held, before any outcome is
	// delivered, so a test can model a concurrent writer.
	onGet func(f *redeployFake, n int)
	// deployWrites are stored by a bundle deploy, as a bundle's shinyhub.toml
	// is.
	deployWrites map[string]any

	patches     int
	gets        int
	fleetStates []string
}

func newRedeployFake(t *testing.T, app *fakeApp) (*redeployFake, *cliConfig) {
	t.Helper()
	if app.status == "" {
		app.status = "running"
	}
	f := &redeployFake{t: t, app: app, delay: 1}
	base := "/api/apps/" + app.Slug
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == base+"/deploy":
			app.ContentDigest = "sha256:PROMOTED"
			app.status = "running"
			if f.deployWrites != nil {
				app.store(f.deployWrites)
			}
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{f.view()})
		case r.Method == "PATCH" && r.URL.Path == base+"/access":
			var b struct{ Access string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			app.Access = b.Access
		case r.Method == "GET" && r.URL.Path == base:
			f.gets++
			if f.onGet != nil {
				f.onGet(f, f.gets)
			}
			if f.launched > f.served {
				if f.pending > 0 {
					f.pending--
				} else {
					f.deliver()
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"app": f.view()})
		case r.Method == "PUT" && r.URL.Path == base+"/fleet-state":
			var b struct{ Status string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.fleetStates = append(f.fleetStates, b.Status)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "PATCH" && r.URL.Path == base:
			f.patches++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.patchLocked(body)
			code := http.StatusOK
			if f.patchCode != nil {
				code = f.patchCode(f.patches)
			}
			if code >= 300 {
				w.WriteHeader(code)
				_, _ = io.WriteString(w, `{"error":"internal server error"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"app": f.view()})
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return f, &cliConfig{Host: srv.URL, Token: "shk_test"}
}

// patchLocked stores body and arms a redeploy when it changes the pool shape
// of a running app, as PatchAppSettings does.
func (f *redeployFake) patchLocked(body map[string]any) {
	changed := false
	for k, v := range body {
		if !poolShapeKeys[k] {
			continue
		}
		if k == "replicas" {
			changed = changed || int(v.(float64)) != f.app.Replicas
			continue
		}
		changed = changed || f.app.settings[k] != v
	}
	f.app.store(body)
	if changed && f.app.status == "running" {
		f.launched++
		f.pending = f.delay
	}
}

func (f *redeployFake) deliver() {
	f.served = f.launched
	outcome, reason, status := f.script(f.served)
	f.outcome, f.reason = outcome, reason
	if status != "" {
		f.app.status = status
	}
}

func (f *redeployFake) view() map[string]any {
	m := f.app.view()
	m["redeploy_seq_launched"] = f.launched
	if f.served > 0 {
		m["last_redeploy"] = map[string]any{
			"seq": f.served, "outcome": f.outcome, "reason": f.reason, "at": time.Now().UTC(),
		}
	}
	return m
}

// observe is the plan's observation of the app right now.
func (f *redeployFake) observe() fleet.ObservedApp {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := json.Marshal(f.view())
	if err != nil {
		f.t.Fatal(err)
	}
	var a db.App
	if err := json.Unmarshal(raw, &a); err != nil {
		f.t.Fatal(err)
	}
	return observedFromApp(a)
}

func (f *redeployFake) states() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.fleetStates...)
}

func fastRedeployPolls(t *testing.T) {
	t.Helper()
	prev := redeployOutcomePollEvery
	redeployOutcomePollEvery = time.Millisecond
	t.Cleanup(func() { redeployOutcomePollEvery = prev })
}

func outcomeOpts() convergeOpts {
	return convergeOpts{preconditions: true, fleetID: "eu", runID: "r", fleetState: true,
		redeployOutcome: true, healthTimeout: 5 * time.Second}
}

func scripted(outcome, reason, status string) func(int64) (string, string, string) {
	return func(int64) (string, string, string) { return outcome, reason, status }
}

// applyReplicas runs an update(config) that raises replicas from 1 to 3.
func applyReplicas(t *testing.T, f *redeployFake, cfg *cliConfig, opt convergeOpts, out io.Writer) applyResult {
	t.Helper()
	obs := f.observe()
	entry := fleet.AppEntry{Slug: f.app.Slug, Config: fleet.Config{Replicas: stateInt(3)}}
	d := fleet.AppDiff{Slug: f.app.Slug, Action: fleet.ActionUpdateConfig, Owned: true,
		ConfigDrift: fleet.ConfigDrift(entry, obs)}
	if len(d.ConfigDrift) != 1 || d.ConfigDrift[0].Key != "replicas" {
		t.Fatalf("setup: drift = %#v, want replicas only", d.ConfigDrift)
	}
	return convergeApp(cfg, d, entry, obs, "", opt, "fleet:eu", out)
}

func wantFailedPartial(t *testing.T, f *redeployFake, r applyResult, errSub string) {
	t.Helper()
	if r.status != statusFailed {
		t.Fatalf("status = %s (err %v), want failed", r.status, r.err)
	}
	if r.mutation != mutationPartial {
		t.Errorf("mutation = %s, want partial: the settings are stored", r.mutation)
	}
	if r.err == nil || !strings.Contains(r.err.Error(), errSub) {
		t.Errorf("err = %v, want it to contain %q", r.err, errSub)
	}
	for _, s := range f.states() {
		if s == fleetConvergenceInSync {
			t.Errorf("fleet state recorded %q; a pool off its settings must not be in_sync (states %v)", s, f.states())
		}
	}
}

func wantUpdatedInSync(t *testing.T, f *redeployFake, r applyResult, want applyStatus) {
	t.Helper()
	if r.status != want {
		t.Fatalf("status = %s (err %v), want %s", r.status, r.err, want)
	}
	st := f.states()
	if len(st) == 0 || st[len(st)-1] != fleetConvergenceInSync {
		t.Errorf("fleet states = %v, want in_sync last", st)
	}
}

func TestRedeployOutcome_BadOutcomesFailApply(t *testing.T) {
	fastRedeployPolls(t)
	for _, tc := range []struct {
		name, outcome, reason string
	}{
		{"partial", "partial", "1 of 3 replicas failed to start"},
		{"failed", "failed", "deploy failed: exec: no such file"},
		{"quarantined", "skipped", "quarantined"},
		{"activation deferred", "skipped", "activation_deferred"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
			f.script = scripted(tc.outcome, tc.reason, "")
			r := applyReplicas(t, f, cfg, outcomeOpts(), io.Discard)
			wantFailedPartial(t, f, r, tc.outcome+": "+tc.reason)
		})
	}
}

func TestRedeployOutcome_CompletedRunningSucceeds(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
	f.delay = 3
	f.script = scripted("completed", "", "")
	var out bytes.Buffer
	r := applyReplicas(t, f, cfg, outcomeOpts(), &out)
	wantUpdatedInSync(t, f, r, statusUpdated)
	if !strings.Contains(out.String(), "waiting for the settings redeploy (seq 1)") {
		t.Errorf("output does not report the wait:\n%s", out.String())
	}
}

func TestRedeployOutcome_CompletedElasticIdleSucceeds(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
	f.script = scripted("completed", "", "idle")
	r := applyReplicas(t, f, cfg, outcomeOpts(), io.Discard)
	wantUpdatedInSync(t, f, r, statusUpdated)
}

func TestRedeployOutcome_NotRunningFollowsServingState(t *testing.T) {
	fastRedeployPolls(t)
	t.Run("hibernated is settled", func(t *testing.T) {
		f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
		f.script = scripted("skipped", "not_running", "hibernated")
		r := applyReplicas(t, f, cfg, outcomeOpts(), io.Discard)
		wantUpdatedInSync(t, f, r, statusUpdated)
	})
	t.Run("crashed during the wait fails", func(t *testing.T) {
		f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
		f.script = scripted("skipped", "not_running", "crashed")
		r := applyReplicas(t, f, cfg, outcomeOpts(), io.Discard)
		if r.status != statusFailed {
			t.Fatalf("status = %s (err %v), want failed: a crashed app is not converged", r.status, r.err)
		}
		for _, s := range f.states() {
			if s == fleetConvergenceInSync {
				t.Errorf("recorded in_sync for a crashed app (states %v)", f.states())
			}
		}
	})
}

func TestRedeployOutcome_NothingLaunchedDoesNotWait(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
	f.script = func(int64) (string, string, string) {
		t.Fatal("no redeploy was launched, so none may be delivered")
		return "", "", ""
	}
	obs := f.observe()
	entry := fleet.AppEntry{Slug: "app", Config: fleet.Config{Name: strp("Sales")}}
	d := fleet.AppDiff{Slug: "app", Action: fleet.ActionUpdateConfig, Owned: true, ConfigDrift: fleet.ConfigDrift(entry, obs)}
	var out bytes.Buffer
	r := convergeApp(cfg, d, entry, obs, "", outcomeOpts(), "fleet:eu", &out)
	wantUpdatedInSync(t, f, r, statusUpdated)
	if strings.Contains(out.String(), "waiting for the settings redeploy") {
		t.Errorf("waited although nothing was launched:\n%s", out.String())
	}
}

func TestRedeployOutcome_SupersedingSeqIsJudged(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
	f.delay = 2
	// While this apply waits on seq 1, another writer changes a different pool
	// key, launching seq 2; seq 1 is fenced out and never reports.
	f.onGet = func(f *redeployFake, n int) {
		if f.launched == 1 && f.served == 0 {
			f.patchLocked(map[string]any{"memory_limit_mb": 512.0})
		}
	}
	f.script = func(seq int64) (string, string, string) {
		if seq != 2 {
			t.Errorf("seq %d reported; only the latest launch reports", seq)
		}
		return "partial", "1 of 3 replicas failed to start", ""
	}
	r := applyReplicas(t, f, cfg, outcomeOpts(), io.Discard)
	wantFailedPartial(t, f, r, "seq 2")
}

func TestRedeployOutcome_ConcurrentWriterFailsDespiteCompleted(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
	f.delay = 2
	// Our PATCH sets replicas 3; before the redeploy reports, another writer
	// sets it back to 1. The redeploy that reports is theirs and completes
	// cleanly, on a pool that is not what this apply declared.
	f.onGet = func(f *redeployFake, n int) {
		if f.launched == 1 && f.served == 0 {
			f.patchLocked(map[string]any{"replicas": 1.0})
		}
	}
	f.script = scripted("completed", "", "")
	r := applyReplicas(t, f, cfg, outcomeOpts(), io.Discard)
	wantFailedPartial(t, f, r, "no longer matches the declaration")
}

func TestRedeployOutcome_FiveHundredAfterLaunchIsJudged(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
	// The first PATCH commits and launches seq 1, then answers 500; the retry
	// re-sends the same value, which launches nothing and answers 200.
	f.patchCode = func(n int) int {
		if n == 1 {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	}
	f.script = scripted("failed", "deploy failed: port in use", "")
	opt := outcomeOpts()
	opt.retries = 1
	r := applyReplicas(t, f, cfg, opt, io.Discard)
	if f.patches != 2 {
		t.Fatalf("PATCH count = %d, want 2 (a 5xx is retried)", f.patches)
	}
	wantFailedPartial(t, f, r, "failed: deploy failed: port in use")
}

func TestRedeployOutcome_NotReportedTimesOut(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
	f.delay = 1 << 30
	f.script = scripted("completed", "", "")
	opt := outcomeOpts()
	opt.healthTimeout = 50 * time.Millisecond
	r := applyReplicas(t, f, cfg, opt, io.Discard)
	wantFailedPartial(t, f, r, "outcome not reported")
}

func TestRedeployOutcome_OldServerWarnsUnverified(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
	f.script = scripted("partial", "1 of 3 replicas failed to start", "")
	opt := outcomeOpts()
	opt.redeployOutcome = false
	r := applyReplicas(t, f, cfg, opt, io.Discard)
	wantUpdatedInSync(t, f, r, statusUpdated)
	if len(r.warnings) != 1 || !strings.Contains(r.warnings[0], "replicas change was not verified") {
		t.Errorf("warnings = %q, want the unverified replicas warning", r.warnings)
	}
}

func TestRedeployOutcome_CreateWithPartialFails(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "new", Access: "private", Replicas: 1})
	f.script = scripted("partial", "2 of 3 replicas failed to start", "")
	entry := fleet.AppEntry{Slug: "new", Source: "./x", Visibility: "private", Config: fleet.Config{Replicas: stateInt(3)}}
	d := fleet.AppDiff{Slug: "new", Action: fleet.ActionCreate}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{}, sourceDir(t), outcomeOpts(), "fleet:eu", io.Discard)
	wantFailedPartial(t, f, r, "created but declared config was not fully applied")
	if !strings.Contains(r.err.Error(), "partial: 2 of 3 replicas failed to start") {
		t.Errorf("err = %v, want the redeploy reason", r.err)
	}
}

func TestRedeployOutcome_SourceUpdateReassertWithPartialFails(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "src", Access: "private", Replicas: 3, ContentDigest: "sha256:OLD"})
	f.script = scripted("partial", "1 of 3 replicas failed to start", "")
	entry := fleet.AppEntry{Slug: "src", Source: "./x", Visibility: "private", Config: fleet.Config{Replicas: stateInt(3)}}
	// The bundle's shinyhub.toml sets replicas 1, so the post-deploy
	// convergence PATCHes 3 back and arms a redeploy.
	f.deployWrites = map[string]any{"replicas": 1.0}
	obs := f.observe()
	d := fleet.AppDiff{Slug: "src", Action: fleet.ActionUpdateSource, Owned: true, ServerDigest: "sha256:OLD"}
	r := convergeApp(cfg, d, entry, obs, sourceDir(t), outcomeOpts(), "fleet:eu", io.Discard)
	wantFailedPartial(t, f, r, "source updated but declared config was not reasserted")
}

// unchangedApply runs the apply that follows one whose redeploy is owed or
// already reported: the stored settings match, so the plan is unchanged.
func unchangedApply(t *testing.T, f *redeployFake, cfg *cliConfig, opt convergeOpts, out io.Writer) applyResult {
	t.Helper()
	entry := fleet.AppEntry{Slug: f.app.Slug, Config: fleet.Config{Replicas: stateInt(3)}}
	obs := f.observe()
	if drift := fleet.ConfigDrift(entry, obs); len(drift) != 0 {
		t.Fatalf("setup: drift = %#v, want none", drift)
	}
	d := fleet.AppDiff{Slug: f.app.Slug, Action: fleet.ActionUnchanged, Owned: true}
	return convergeApp(cfg, d, entry, obs, "", opt, "fleet:eu", out)
}

func TestRedeployOutcome_UnchangedWaitsOnOwedRedeploy(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 3})
	// An earlier apply stored replicas 3 and launched seq 1, which has not
	// reported yet.
	f.launched, f.pending = 1, 2
	f.script = scripted("failed", "deploy failed: exec: no such file", "")
	r := unchangedApply(t, f, cfg, outcomeOpts(), io.Discard)
	if r.status != statusFailed || r.err == nil || !strings.Contains(r.err.Error(), "failed: deploy failed") {
		t.Fatalf("status = %s err = %v, want failed on the owed redeploy's outcome", r.status, r.err)
	}
	for _, s := range f.states() {
		if s == fleetConvergenceInSync {
			t.Errorf("recorded in_sync while the owed redeploy failed (states %v)", f.states())
		}
	}
}

func TestRedeployOutcome_UnchangedAfterBadOutcome(t *testing.T) {
	fastRedeployPolls(t)
	bad := func(t *testing.T, status string) (*redeployFake, *cliConfig) {
		f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 3, status: status})
		f.launched, f.served = 1, 1
		f.outcome, f.reason = "partial", "1 of 3 replicas failed to start"
		f.script = func(int64) (string, string, string) { t.Fatal("nothing is owed"); return "", "", "" }
		return f, cfg
	}
	t.Run("healthy since: warns and records in_sync", func(t *testing.T) {
		f, cfg := bad(t, "running")
		r := unchangedApply(t, f, cfg, outcomeOpts(), io.Discard)
		wantUpdatedInSync(t, f, r, statusUnchanged)
		if len(r.warnings) != 1 || !strings.Contains(r.warnings[0], "last settings redeploy partial: 1 of 3 replicas failed to start") ||
			!strings.Contains(r.warnings[0], "shinyhub apps restart app") {
			t.Errorf("warnings = %q, want the last-redeploy warning with the restart remedy", r.warnings)
		}
	})
	t.Run("still crashed: fails without --verify", func(t *testing.T) {
		f, cfg := bad(t, "crashed")
		r := unchangedApply(t, f, cfg, outcomeOpts(), io.Discard)
		if r.status != statusFailed {
			t.Fatalf("status = %s, want failed: the gate runs on a bad last outcome even without --verify", r.status)
		}
	})
	t.Run("completed outcome: no warning", func(t *testing.T) {
		f, cfg := bad(t, "running")
		f.outcome, f.reason = "completed", ""
		r := unchangedApply(t, f, cfg, outcomeOpts(), io.Discard)
		wantUpdatedInSync(t, f, r, statusUnchanged)
		if len(r.warnings) != 0 {
			t.Errorf("warnings = %q, want none after a completed redeploy", r.warnings)
		}
	})
}

func TestRedeployWarning(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   fleet.RedeployState
		want string
	}{
		{"never launched", fleet.RedeployState{}, ""},
		{"owed", fleet.RedeployState{Launched: 2, Served: 1, Outcome: "partial"}, ""},
		{"completed", fleet.RedeployState{Launched: 1, Served: 1, Outcome: "completed"}, ""},
		{"not running", fleet.RedeployState{Launched: 1, Served: 1, Outcome: "skipped", Reason: "not_running"}, ""},
		{"quarantined", fleet.RedeployState{Launched: 1, Served: 1, Outcome: "skipped", Reason: "quarantined"},
			"last settings redeploy skipped: quarantined; stored settings may not be live; `shinyhub apps restart s` applies them"},
		{"failed", fleet.RedeployState{Launched: 3, Served: 3, Outcome: "failed", Reason: "boom"},
			"last settings redeploy failed: boom; stored settings may not be live; `shinyhub apps restart s` applies them"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := redeployWarning("s", tc.st); got != tc.want {
				t.Errorf("redeployWarning = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRedeployOutcome_AdoptShortcutJudgesOwedRedeploy(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 3, ContentDigest: "sha256:SAME"})
	f.launched, f.pending = 1, 1
	f.script = scripted("partial", "1 of 3 replicas failed to start", "")
	entry := fleet.AppEntry{Slug: "app", Source: "./x", Visibility: "private", Config: fleet.Config{Replicas: stateInt(3)}}
	obs := f.observe()
	d := fleet.AppDiff{Slug: "app", Action: fleet.ActionAdopt, LocalDigest: "sha256:SAME", ServerDigest: "sha256:SAME"}
	opt := outcomeOpts()
	opt.adopt = true
	r := convergeApp(cfg, d, entry, obs, sourceDir(t), opt, "fleet:eu", io.Discard)
	wantFailedPartial(t, f, r, "partial: 1 of 3 replicas failed to start")
}

// The adoption shortcut skips the apply's own health gate, so with --verify it
// must still run the gate the backlog check defers to that step: an adopted
// app whose pool is crashed is not converged either way.
func TestRedeployOutcome_AdoptShortcutVerifyGatesCrashedPool(t *testing.T) {
	fastRedeployPolls(t)
	adopt := func(t *testing.T, f *redeployFake, cfg *cliConfig) applyResult {
		entry := fleet.AppEntry{Slug: "app", Source: "./x", Visibility: "private", Config: fleet.Config{Replicas: stateInt(3)}}
		d := fleet.AppDiff{Slug: "app", Action: fleet.ActionAdopt, LocalDigest: "sha256:SAME", ServerDigest: "sha256:SAME"}
		opt := outcomeOpts()
		opt.adopt, opt.verifyHealth, opt.healthTimeout = true, true, 200*time.Millisecond
		return convergeApp(cfg, d, entry, f.observe(), sourceDir(t), opt, "fleet:eu", io.Discard)
	}
	wantFailedNotInSync := func(t *testing.T, f *redeployFake, r applyResult) {
		t.Helper()
		if r.status != statusFailed {
			t.Fatalf("status = %s (err %v), want failed: an adopted crashed pool is not converged", r.status, r.err)
		}
		for _, s := range f.states() {
			if s == fleetConvergenceInSync {
				t.Errorf("recorded in_sync for a crashed pool (states %v)", f.states())
			}
		}
	}
	t.Run("bad last outcome", func(t *testing.T) {
		f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 3, ContentDigest: "sha256:SAME", status: "crashed"})
		f.launched, f.served = 1, 1
		f.outcome, f.reason = "partial", "1 of 3 replicas failed to start"
		f.script = func(int64) (string, string, string) { t.Fatal("nothing is owed"); return "", "", "" }
		wantFailedNotInSync(t, f, adopt(t, f, cfg))
	})
	t.Run("owed redeploy completes then crashes", func(t *testing.T) {
		f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 3, ContentDigest: "sha256:SAME"})
		f.launched, f.pending = 1, 1
		f.script = scripted("completed", "", "crashed")
		wantFailedNotInSync(t, f, adopt(t, f, cfg))
	})
}

// A plan reads the latest settings redeploy of every observed app, so a
// failure the launching apply never saw (it was interrupted) is still
// surfaced: as a plan warning in human output, and on the app in JSON. A
// server without outcome reporting says nothing about it.
func TestFleetPlan_WarnsOnLastSettingsRedeploy(t *testing.T) {
	file := writeFleetTree(t, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"steady\"\nsource=\"./steady\"\n\n  [app.config]\n  replicas = 3\n",
		map[string]string{"steady": ""})
	digest, err := digestLocalDir(filepath.Join(filepath.Dir(file), "steady"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "steady: last settings redeploy partial: 1 of 3 replicas failed to start"
	for _, tc := range []struct {
		name string
		caps string
		warn bool
	}{
		{"reporting server", `{"content_digest":true,"redeploy_outcome":true}`, true},
		{"older server", `{"content_digest":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _ = setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/server-info":
					_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":` + tc.caps + `}`))
				case r.Method == "GET" && r.URL.Path == "/api/apps":
					_, _ = w.Write([]byte(`[{"slug":"steady","access":"private","managed_by":"fleet:eu","content_digest":"` + digest +
						`","replicas":3,"redeploy_seq_launched":2,"last_redeploy":{"seq":2,"outcome":"partial","reason":"1 of 3 replicas failed to start"}}]`))
				default:
					_, _ = w.Write([]byte(`{}`))
				}
			})
			human, stderr, err := execCLISplit(t, "fleet", "plan", "-f", file, "-o", "table", "--no-color")
			if err != nil {
				t.Fatalf("plan: %v\nstdout=%s\nstderr=%s", err, human, stderr)
			}
			if !strings.Contains(human, "0 to update") || !strings.Contains(human, "1 unchanged") {
				t.Fatalf("fixture did not plan steady unchanged:\n%s", human)
			}
			if got := strings.Contains(human, want) && strings.Contains(human, "Warnings (1)"); got != tc.warn {
				t.Errorf("human plan warning shown = %v, want %v:\nstdout=%s\nstderr=%s", got, tc.warn, human, stderr)
			}
			raw, _, err := execCLISplit(t, "fleet", "plan", "-f", file, "--json")
			if err != nil {
				t.Fatalf("plan --json: %v\n%s", err, raw)
			}
			var env struct {
				Apps []struct {
					Slug     string   `json:"slug"`
					Action   string   `json:"action"`
					Warnings []string `json:"warnings"`
				} `json:"apps"`
			}
			if err := json.Unmarshal([]byte(raw), &env); err != nil {
				t.Fatalf("decode plan json: %v\n%s", err, raw)
			}
			if len(env.Apps) != 1 || env.Apps[0].Action != string(fleet.ActionUnchanged) {
				t.Fatalf("apps = %+v, want steady unchanged", env.Apps)
			}
			got := len(env.Apps[0].Warnings) == 1 && strings.HasPrefix("steady: "+env.Apps[0].Warnings[0], want)
			if got != tc.warn {
				t.Errorf("json warnings = %q, want present=%v", env.Apps[0].Warnings, tc.warn)
			}
		})
	}
}

// A PATCH on an app that is not running launches nothing, but the last
// redeploy may still have left the pool off its settings. The apply judges
// that outcome instead of recording in_sync on the strength of the PATCH.
func TestRedeployOutcome_NoLaunchJudgesLastOutcome(t *testing.T) {
	fastRedeployPolls(t)
	bad := func(t *testing.T, status string) (*redeployFake, *cliConfig) {
		f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1, status: status})
		f.launched, f.served = 1, 1
		f.outcome, f.reason = "partial", "1 of 3 replicas failed to start"
		f.script = func(int64) (string, string, string) { t.Fatal("nothing is owed"); return "", "", "" }
		return f, cfg
	}
	t.Run("still crashed: fails", func(t *testing.T) {
		f, cfg := bad(t, "crashed")
		r := applyReplicas(t, f, cfg, outcomeOpts(), io.Discard)
		if f.launched != 1 {
			t.Fatalf("setup: launched = %d, want the PATCH to arm nothing", f.launched)
		}
		if r.status != statusFailed {
			t.Fatalf("status = %s (err %v), want failed on the last partial redeploy of a crashed app", r.status, r.err)
		}
		if len(r.warnings) != 1 || !strings.Contains(r.warnings[0], "last settings redeploy partial") {
			t.Errorf("warnings = %q, want the last-redeploy warning", r.warnings)
		}
		for _, s := range f.states() {
			if s == fleetConvergenceInSync {
				t.Errorf("recorded in_sync for a crashed pool (states %v)", f.states())
			}
		}
	})
	t.Run("healthy since: warns and succeeds", func(t *testing.T) {
		f, cfg := bad(t, "running")
		f.app.Replicas = 3
		// Stored 3 already, so the PATCH changes a non-pool key only.
		entry := fleet.AppEntry{Slug: "app", Config: fleet.Config{Replicas: stateInt(3), HibernateTimeoutMinutes: stateInt(7)}}
		obs := f.observe()
		d := fleet.AppDiff{Slug: "app", Action: fleet.ActionUpdateConfig, Owned: true, ConfigDrift: fleet.ConfigDrift(entry, obs)}
		if len(d.ConfigDrift) == 0 {
			t.Fatal("setup: want config drift")
		}
		r := convergeApp(cfg, d, entry, obs, "", outcomeOpts(), "fleet:eu", io.Discard)
		wantUpdatedInSync(t, f, r, statusUpdated)
		if len(r.warnings) != 1 || !strings.Contains(r.warnings[0], "last settings redeploy partial") {
			t.Errorf("warnings = %q, want the last-redeploy warning", r.warnings)
		}
	})
}

// The plan saw a seq owed, and it reported before the apply's first read. The
// apply must still judge that outcome rather than read "nothing owed" as
// settled.
func TestRedeployOutcome_OwedAtPlanReportedBeforeApply(t *testing.T) {
	fastRedeployPolls(t)
	owed := func(t *testing.T, app *fakeApp) (*redeployFake, *cliConfig, fleet.ObservedApp) {
		f, cfg := newRedeployFake(t, app)
		f.launched, f.served, f.outcome = 2, 1, "completed"
		f.pending = 0
		f.script = scripted("partial", "1 of 3 replicas failed to start", "")
		obs := f.observe()
		if !obs.Redeploy.Owed() {
			t.Fatalf("setup: plan observation %+v, want seq 2 owed", obs.Redeploy)
		}
		return f, cfg, obs
	}
	t.Run("unchanged", func(t *testing.T) {
		f, cfg, obs := owed(t, &fakeApp{Slug: "app", Access: "private", Replicas: 3})
		entry := fleet.AppEntry{Slug: "app", Config: fleet.Config{Replicas: stateInt(3)}}
		d := fleet.AppDiff{Slug: "app", Action: fleet.ActionUnchanged, Owned: true}
		r := convergeApp(cfg, d, entry, obs, "", outcomeOpts(), "fleet:eu", io.Discard)
		if r.status != statusFailed || r.err == nil || !strings.Contains(r.err.Error(), "partial: 1 of 3 replicas failed to start") {
			t.Fatalf("status = %s err = %v, want failed on the seq the plan saw owed", r.status, r.err)
		}
		for _, s := range f.states() {
			if s == fleetConvergenceInSync {
				t.Errorf("recorded in_sync (states %v)", f.states())
			}
		}
	})
	t.Run("adopt", func(t *testing.T) {
		f, cfg, obs := owed(t, &fakeApp{Slug: "app", Access: "private", Replicas: 3, ContentDigest: "sha256:SAME"})
		entry := fleet.AppEntry{Slug: "app", Source: "./x", Visibility: "private", Config: fleet.Config{Replicas: stateInt(3)}}
		d := fleet.AppDiff{Slug: "app", Action: fleet.ActionAdopt, LocalDigest: "sha256:SAME", ServerDigest: "sha256:SAME"}
		opt := outcomeOpts()
		opt.adopt = true
		r := convergeApp(cfg, d, entry, obs, sourceDir(t), opt, "fleet:eu", io.Discard)
		wantFailedPartial(t, f, r, "partial: 1 of 3 replicas failed to start")
	})
	t.Run("config patch", func(t *testing.T) {
		f, cfg, obs := owed(t, &fakeApp{Slug: "app", Access: "private", Replicas: 3})
		// A non-pool key drifts, so the PATCH launches nothing of its own.
		entry := fleet.AppEntry{Slug: "app", Config: fleet.Config{Replicas: stateInt(3), HibernateTimeoutMinutes: stateInt(7)}}
		d := fleet.AppDiff{Slug: "app", Action: fleet.ActionUpdateConfig, Owned: true, ConfigDrift: fleet.ConfigDrift(entry, obs)}
		r := convergeApp(cfg, d, entry, obs, "", outcomeOpts(), "fleet:eu", io.Discard)
		wantFailedPartial(t, f, r, "partial: 1 of 3 replicas failed to start")
	})
}

// A read that hangs is bounded by the outcome timeout, not by the HTTP
// client's own much longer one.
func TestRedeployOutcome_HungReadIsBoundedByTimeout(t *testing.T) {
	fastRedeployPolls(t)
	// hangFrom 1 hangs the read that follows the PATCH; 2 answers it and
	// hangs the outcome poll.
	for _, hangFrom := range []int{1, 2} {
		t.Run(fmt.Sprintf("hang from read %d", hangFrom), func(t *testing.T) {
			f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
			f.script = scripted("completed", "", "")
			f.delay = 1 << 30
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			owedGets := 0
			f.onGet = func(f *redeployFake, n int) {
				if f.launched <= f.served {
					return
				}
				if owedGets++; owedGets >= hangFrom {
					f.mu.Unlock()
					<-release
					f.mu.Lock()
				}
			}
			opt := outcomeOpts()
			opt.healthTimeout = 200 * time.Millisecond
			start := time.Now()
			r := applyReplicas(t, f, cfg, opt, io.Discard)
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("apply took %s, want it bounded by the %s outcome timeout", elapsed, opt.healthTimeout)
			}
			if r.status != statusFailed {
				t.Fatalf("status = %s (err %v), want failed when the outcome read never answers", r.status, r.err)
			}
		})
	}
}

func TestDescribeRedeploy_FlattensMultiLineReason(t *testing.T) {
	st := fleet.RedeployState{Outcome: "failed", Reason: "deploy failed:\n  exit status 1\n\n  ModuleNotFoundError: shiny\n"}
	want := "failed: deploy failed:; exit status 1; ModuleNotFoundError: shiny"
	if got := describeRedeploy(st); got != want {
		t.Errorf("describeRedeploy = %q, want %q", got, want)
	}
	if got := describeRedeploy(fleet.RedeployState{Outcome: "completed"}); got != "completed" {
		t.Errorf("describeRedeploy = %q, want the bare outcome", got)
	}
}

// The post-deploy convergence asserts the apply still owns what it deployed.
// When no declared key drifted it sends no PATCH, so there is no conditional
// write for the server to refuse; the apply must still notice a writer that
// took the app or replaced its bundle after the deploy, rather than report
// the app updated and in sync.
func TestConvergeDeclaredConfig_NoDriftStillHonorsPreconditions(t *testing.T) {
	fastRedeployPolls(t)
	for _, tc := range []struct {
		name  string
		steal func(a *fakeApp)
	}{
		{"owner changed", func(a *fakeApp) { other := "fleet:other"; a.ManagedBy = &other }},
		{"bundle replaced", func(a *fakeApp) { a.ContentDigest = "sha256:THEIRS" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := "fleet:eu"
			f, cfg := newRedeployFake(t, &fakeApp{Slug: "src", Access: "private", Replicas: 3, ContentDigest: "sha256:OLD", ManagedBy: &marker})
			f.script = func(int64) (string, string, string) { t.Fatal("nothing is owed"); return "", "", "" }
			// The first read of the new bundle is the deploy's own health
			// wait, which precedes its digest readback; the writer strikes
			// after that, when the convergence reads the app.
			promotedReads := 0
			f.onGet = func(f *redeployFake, _ int) {
				if f.app.ContentDigest == "sha256:PROMOTED" {
					if promotedReads++; promotedReads == 2 {
						tc.steal(f.app)
					}
				}
			}
			entry := fleet.AppEntry{Slug: "src", Source: "./x", Visibility: "private", Config: fleet.Config{Replicas: stateInt(3)}}
			obs := f.observe()
			d := fleet.AppDiff{Slug: "src", Action: fleet.ActionUpdateSource, Owned: true, ServerDigest: "sha256:OLD"}
			opt := outcomeOpts()
			opt.fleetState = false
			r := convergeApp(cfg, d, entry, obs, sourceDir(t), opt, marker, io.Discard)
			if r.status != statusConflict {
				t.Fatalf("status = %s (err %v), want conflict", r.status, r.err)
			}
			if f.patches != 0 {
				t.Errorf("patches = %d, want none: nothing drifted", f.patches)
			}
		})
	}
}

// TestRedeployOutcome_RedeployLaunchedAfterJudgingIsJudged covers a settings
// change another writer commits after this apply's redeploy reported: the
// read that ends the settle shows a newer redeploy, which is judged before
// the apply records anything.
func TestRedeployOutcome_RedeployLaunchedAfterJudgingIsJudged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome string
		// reported delivers the newer outcome on the read that shows its
		// launch, so the settle's final read already carries it.
		reported bool
		wantErr  bool
	}{
		{"newer redeploy partial fails", "partial", false, true},
		{"newer redeploy already reported partial fails", "partial", true, true},
		{"newer redeploy completed succeeds", "completed", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fastRedeployPolls(t)
			f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
			fired := false
			f.onGet = func(f *redeployFake, n int) {
				if !fired && f.launched == 1 && f.served == 1 {
					fired = true
					if tc.reported {
						f.delay = 0
					}
					f.patchLocked(map[string]any{"memory_limit_mb": 512.0})
				}
			}
			f.script = func(seq int64) (string, string, string) {
				if seq == 1 {
					return "completed", "", ""
				}
				return tc.outcome, "1 of 3 replicas failed to start", ""
			}
			r := applyReplicas(t, f, cfg, outcomeOpts(), io.Discard)
			if !fired {
				t.Fatal("setup: the concurrent writer never ran")
			}
			if tc.wantErr {
				wantFailedPartial(t, f, r, "seq 2")
				return
			}
			wantUpdatedInSync(t, f, r, statusUpdated)
			if f.served != 2 {
				t.Errorf("served = %d, want 2: the newer redeploy must be awaited", f.served)
			}
		})
	}
}

// TestRedeployOutcome_FailureRecordedUnderJudgedSeqFails covers a restart
// that fails after this apply judged its redeploy: the boot outcome replaces
// the completed one under the same seq, and the settle's final read shows it.
func TestRedeployOutcome_FailureRecordedUnderJudgedSeqFails(t *testing.T) {
	fastRedeployPolls(t)
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
	f.script = scripted("completed", "", "")
	fired := false
	f.onGet = func(f *redeployFake, n int) {
		if f.served == 1 && f.outcome == "completed" && !fired {
			// Leave the read that reports seq 1 alone; fail on the next one.
			fired = true
			return
		}
		if fired {
			f.outcome, f.reason = "failed", "restart: replica 0 exited with code 1"
		}
	}
	r := applyReplicas(t, f, cfg, outcomeOpts(), io.Discard)
	if !fired {
		t.Fatal("setup: the restart failure never ran")
	}
	wantFailedPartial(t, f, r, "did not come up")
}

// TestConvergeDeclaredConfig_HungReadIsBoundedByTimeout covers the readbacks
// around the post-deploy config PATCH: a server that stops answering must not
// hold the apply past the health timeout.
func TestConvergeDeclaredConfig_HungReadIsBoundedByTimeout(t *testing.T) {
	fastRedeployPolls(t)
	// Read 1 precedes the PATCH; read 2 follows it.
	for _, hangAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("hang at read %d", hangAt), func(t *testing.T) {
			f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
			f.script = scripted("completed", "", "")
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			f.onGet = func(f *redeployFake, n int) {
				if n == hangAt {
					f.mu.Unlock()
					<-release
					f.mu.Lock()
				}
			}
			opt := outcomeOpts()
			opt.healthTimeout = 200 * time.Millisecond
			entry := fleet.AppEntry{Slug: "app", Config: fleet.Config{Replicas: stateInt(3)}}
			start := time.Now()
			_, err := convergeDeclaredConfig(cfg, "app", entry, nil, nil, opt, io.Discard)
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("converge took %s, want it bounded by the %s health timeout", elapsed, opt.healthTimeout)
			}
			if err == nil {
				t.Fatal("converge succeeded although a read never answered")
			}
			if hangAt == 2 && f.patches != 1 {
				t.Fatalf("patches = %d, want the PATCH sent before the hung read", f.patches)
			}
		})
	}
}

// A health timeout shorter than the poll interval still gets its reads: the
// wait polls within whatever budget remains instead of giving up because a
// full interval no longer fits, so a redeploy that reports inside the budget
// passes. The real interval is kept on purpose; the defect only exists when it
// exceeds the remaining budget.
func TestRedeployOutcome_ShortTimeoutStillPollsWithinBudget(t *testing.T) {
	if redeployOutcomePollEvery <= time.Second {
		t.Fatalf("setup: poll interval %s must exceed the 1s budget", redeployOutcomePollEvery)
	}
	f, cfg := newRedeployFake(t, &fakeApp{Slug: "app", Access: "private", Replicas: 1})
	f.delay = 2
	f.script = scripted("completed", "", "")
	opt := outcomeOpts()
	opt.healthTimeout = time.Second
	var out bytes.Buffer
	start := time.Now()
	r := applyReplicas(t, f, cfg, opt, &out)
	wantUpdatedInSync(t, f, r, statusUpdated)
	if !strings.Contains(out.String(), "waiting for the settings redeploy (seq 1)") {
		t.Fatalf("the redeploy reported before the wait, so the budget was never exercised:\n%s", out.String())
	}
	if took := time.Since(start); took > opt.healthTimeout+500*time.Millisecond {
		t.Errorf("apply took %s, want it within the %s budget", took, opt.healthTimeout)
	}
}

type slowTransport func(*http.Request) (*http.Response, error)

func (f slowTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The outcome wait, the serving check and the final read share one
// --health-timeout budget. Each used to get the whole timeout, so a settle
// whose wait used most of it could run to roughly three times the budget,
// and succeed after it had passed.
func TestSettleRedeploy_StagesShareOneBudget(t *testing.T) {
	// Sharing one budget fails near budget+perRead. A fresh budget per stage
	// leaves each stage room for its read, so the settle succeeds after the
	// budget has passed. Reads are long enough relative to the limit that
	// scheduler delay on a loaded host cannot push the shared path past it.
	const perRead = 150 * time.Millisecond
	const budget = 400 * time.Millisecond
	prev := httpClient
	t.Cleanup(func() { httpClient = prev })
	httpClient = &apiClient{&http.Client{Transport: slowTransport(func(r *http.Request) (*http.Response, error) {
		select {
		case <-time.After(perRead):
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		body := `{"app":{"slug":"app","status":"running","redeploy_seq_launched":1,"last_redeploy":{"seq":1,"outcome":"completed"}}}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	start := time.Now()
	_, err := settleRedeploy(&cliConfig{Host: "http://example.invalid"}, "app", 0, &db.App{RedeploySeqLaunched: 1}, nil,
		convergeOpts{redeployOutcome: true, healthTimeout: budget}, io.Discard)
	took := time.Since(start)
	if err == nil {
		t.Fatalf("settle succeeded in %s although confirming the pool needs more reads than fit the %s budget", took, budget)
	}
	// A failed serving check fetches the log tail as diagnostics after the
	// wait has given up; that one read is the only allowance past the budget,
	// and the rest of the limit is headroom for scheduler delay.
	if limit := 2 * budget; took > limit {
		t.Fatalf("settle took %s (err %v), want it within %s", took, err, limit)
	}
}
