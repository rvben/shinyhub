package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fleet"
)

// fetchFleetAppCtx reads GET /api/apps/{slug}, bounded by ctx, and returns the
// app record from its {"app": ...} envelope. It is the per-app read apply uses
// between mutations.
func fetchFleetAppCtx(ctx context.Context, cfg *cliConfig, slug string) (*db.App, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", slug, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, httpError(cfg.Token, "read app "+slug, resp, body)
	}
	var env struct {
		App *db.App `json:"app"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, &protocolError{op: "decode app " + slug, err: err}
	}
	if env.App == nil {
		return nil, &protocolError{op: "decode app " + slug, err: fmt.Errorf("response has no app object")}
	}
	return env.App, nil
}

// convergeDeclaredConfig makes the server's stored settings match the
// effective declaration after a deploy. The deploy applied the new bundle's
// own [app] values, which can replace a fleet-declared value that matched when
// the plan was computed, so the pre-deploy drift list no longer describes what
// needs sending. It re-reads the app, PATCHes exactly the keys that differ now
// (a key the deploy left correct is not re-sent, which matters for keys like
// replicas that the server rejects or redeploys on), and re-reads once more to
// confirm nothing is left. Remaining drift is an error, never a success.
//
// A PATCH that changes the pool shape launches a settings redeploy, and its
// outcome is judged (settleRedeploy) before the final drift check, so a pool
// that failed to adopt the new settings is never reported as converged.
func convergeDeclaredConfig(cfg *cliConfig, slug string, entry fleet.AppEntry, ifD, ifMB *string, opt convergeOpts, out io.Writer) (int, error) {
	app, err := fetchFleetAppWithin(cfg, slug, opt.healthTimeout)
	if err != nil {
		return 0, fmt.Errorf("read settings after deploy: %w", err)
	}
	servedBefore := redeployStateOf(app).Served
	drift := fleet.ConfigDrift(entry, observedFromApp(*app))
	attempts := 0
	if len(drift) > 0 {
		attempts, err = applyConfigDriftWithRetry(cfg, slug, drift, fleet.EffectiveConfig(entry), ifD, ifMB, opt.runID, opt.retries)
		if err != nil {
			return attempts, err
		}
		app, err = fetchFleetAppWithin(cfg, slug, opt.healthTimeout)
		if err != nil {
			return attempts, fmt.Errorf("read settings after config patch: %w", err)
		}
	}
	app, err = settleRedeploy(cfg, slug, servedBefore, app, drift, opt, out)
	if err != nil {
		return attempts, err
	}
	if err := preconditionsHeld(slug, app, ifD, ifMB); err != nil {
		return attempts, err
	}
	if left := fleet.ConfigDrift(entry, observedFromApp(*app)); len(left) > 0 {
		return attempts, fmt.Errorf("declared config did not converge: %s", describeDrift(left))
	}
	return attempts, nil
}

// preconditionsHeld checks the app as last read against the digest and owner
// preconditions the convergence carries, with the server's semantics: an
// empty digest imposes nothing, while an owner precondition compares exactly,
// empty meaning unmanaged. An app read that reports no digest says nothing
// about the bundle, so it is not taken as a change. A convergence that found
// nothing to PATCH sent no conditional write, so without this a writer that
// took the app or replaced its bundle after the deploy would go unnoticed and
// the app be reported in sync.
func preconditionsHeld(slug string, app *db.App, ifD, ifMB *string) error {
	if ifD != nil && *ifD != "" && app.ContentDigest != "" && app.ContentDigest != *ifD {
		return &conflictError{slug: slug, msg: "content_digest changed after deploy"}
	}
	if ifMB != nil {
		cur := ""
		if app.ManagedBy != nil {
			cur = *app.ManagedBy
		}
		if cur != *ifMB {
			return &conflictError{slug: slug, msg: "managed_by changed after deploy"}
		}
	}
	return nil
}

// redeployOutcomePollEvery is the cadence of the settings-redeploy outcome
// wait. It matches the health wait's poll interval.
var redeployOutcomePollEvery = 2 * time.Second

// redeployOutcomeLastRead is the budget kept for the final outcome read, so
// the wait's last poll is not sent with a context that is already expiring.
const redeployOutcomeLastRead = 250 * time.Millisecond

// Settings-redeploy outcomes and the skip reason that means "nothing was
// running to cycle", as the server reports them in last_redeploy.
const (
	redeployCompleted  = "completed"
	redeploySkipped    = "skipped"
	redeployNotRunning = "not_running"
)

// poolShapeKeys are the declared settings whose change makes the server cycle
// the running pool. Only these can launch a settings redeploy.
var poolShapeKeys = map[string]bool{
	"replicas": true, "memory_limit_mb": true, "cpu_quota_percent": true,
	"worker_isolation": true, "worker_grouped_size": true, "worker_max_workers": true,
	"worker_max_session_lifetime_secs": true,
}

// redeployStateOf projects the server's settings-redeploy bookkeeping.
func redeployStateOf(a *db.App) fleet.RedeployState {
	st := fleet.RedeployState{Launched: a.RedeploySeqLaunched}
	if a.LastRedeploy != nil {
		st.Served = a.LastRedeploy.Seq
		st.Outcome = a.LastRedeploy.Outcome
		st.Reason = a.LastRedeploy.Reason
	}
	return st
}

// redeploySettled reports whether a served redeploy left the pool on its
// settings: it cycled cleanly, or there was nothing running to cycle and the
// stored settings apply at the next start.
func redeploySettled(st fleet.RedeployState) bool {
	return st.Outcome == redeployCompleted ||
		(st.Outcome == redeploySkipped && st.Reason == redeployNotRunning)
}

// describeRedeploy renders an outcome as "partial: 1 of 3 replicas failed",
// on one line: a failure reason can carry the app's multi-line log tail.
func describeRedeploy(st fleet.RedeployState) string {
	var lines []string
	for _, line := range strings.Split(st.Reason, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	reason := strings.Join(lines, "; ")
	if reason == "" {
		return st.Outcome
	}
	return st.Outcome + ": " + reason
}

// redeployWarning is the notice for an app whose latest settings redeploy
// did not leave the pool on its stored settings, or "" when there is none.
// The apply that launched that redeploy failed on it, but that run may have
// been interrupted before it saw the outcome.
func redeployWarning(slug string, st fleet.RedeployState) string {
	if st.Launched == 0 || st.Owed() || redeploySettled(st) {
		return ""
	}
	return fmt.Sprintf("last settings redeploy %s; stored settings may not be live; `shinyhub apps restart %s` applies them",
		describeRedeploy(st), slug)
}

// settleRedeploy judges the settings redeploys that reported, or are still
// owed, since servedBefore: the latest seq that had reported when the caller
// last looked. That covers one this apply's PATCH launched, a 5xx attempt
// whose write committed, a concurrent writer's, and one the plan saw owed
// that reported before this read. The stored settings are only live once the
// latest launched redeploy has reported, so it waits for that, and the
// outcome decides:
//
//   - completed, or skipped because nothing was running: the pool is on its
//     settings; the serving-state gate confirms it has not crashed since.
//   - partial, failed, or any other skip: an error. The settings are stored
//     but the pool is not running them.
//   - no outcome within the health timeout: an error, never a success.
//
// With nothing new to judge, an older outcome that left the pool off its
// settings still stands (a PATCH to an app that is not running launches
// nothing, so it does not supersede it): it is reported, and the
// serving-state gate runs even without --verify, so the apply succeeds only
// when the pool is serving.
//
// app is the read that followed the PATCH. patched lists what was sent; it
// only matters to a server without outcome reporting, where a pool-shape
// change is reported as unverified instead.
//
// A settings change committed while this judged the outcome launches another
// redeploy, which the final read shows. That one is judged in turn, within
// the same health timeout, so an apply never records a pool it last saw with
// a redeploy owed or failed as in sync.
func settleRedeploy(cfg *cliConfig, slug string, servedBefore int64, app *db.App, patched []fleet.ConfigDriftItem, opt convergeOpts, out io.Writer) (*db.App, error) {
	if !opt.redeployOutcome {
		for _, it := range patched {
			if poolShapeKeys[it.Key] {
				emitFleetWarning(out, slug, "this server does not report settings redeploy outcomes, so the "+it.Key+" change was not verified to reach the running pool")
				break
			}
		}
		return app, nil
	}
	deadline := time.Now().Add(outcomeTimeout(opt.healthTimeout))
	for {
		final, judged, err := settleRedeployOnce(cfg, slug, servedBefore, app, deadline, opt, out)
		if err != nil {
			return nil, err
		}
		fst := redeployStateOf(final)
		if fst.Launched <= judged.Launched {
			// A restart, deploy or rollback records its boot outcome under the
			// same seq, so an unchanged seq can still carry a failure this
			// pass never judged.
			if fst.Served == judged.Served && !redeploySettled(fst) &&
				(fst.Outcome != judged.Outcome || fst.Reason != judged.Reason) {
				return nil, fmt.Errorf("the pool was rebooted during apply and did not come up: %s; `shinyhub apps restart %s` applies the stored settings",
					describeRedeploy(fst), slug)
			}
			return final, nil
		}
		if time.Until(deadline) <= 0 {
			return nil, fmt.Errorf("settings kept changing during apply: redeploy seq %d launched after seq %d was judged, and no outcome was awaited within the health timeout",
				fst.Launched, judged.Launched)
		}
		// Everything after the judged seq is new to this apply, including a
		// redeploy that has already reported.
		servedBefore, app = judged.Launched, final
	}
}

// settleRedeployOnce is one settleRedeploy pass. It returns the app read last
// and the redeploy state it judged. Every stage (the outcome wait, the
// serving check and the final read) draws on what is left before deadline,
// so the settle as a whole stays within one health timeout.
func settleRedeployOnce(cfg *cliConfig, slug string, servedBefore int64, app *db.App, deadline time.Time, opt convergeOpts, out io.Writer) (*db.App, fleet.RedeployState, error) {
	st := redeployStateOf(app)
	if st.Served <= servedBefore && !st.Owed() {
		w := redeployWarning(slug, st)
		if w == "" {
			return app, st, nil
		}
		emitFleetWarning(out, slug, w)
		if opt.verifyHealth {
			return app, st, nil
		}
		left, err := settleBudget(deadline, slug)
		if err != nil {
			return nil, fleet.RedeployState{}, err
		}
		if err := verifyFleetHealthyForAction(cfg, slug, out, left, "", time.Now().UTC()); err != nil {
			return nil, fleet.RedeployState{}, err
		}
		final, err := fetchFleetAppBy(cfg, slug, deadline)
		return final, st, err
	}
	left, err := settleBudget(deadline, slug)
	if err != nil {
		return nil, fleet.RedeployState{}, err
	}
	settled, err := awaitRedeployOutcome(cfg, slug, left, out)
	if err != nil {
		return nil, fleet.RedeployState{}, err
	}
	st = redeployStateOf(settled)
	if !redeploySettled(st) {
		return nil, fleet.RedeployState{}, fmt.Errorf("settings were stored but the settings redeploy (seq %d) did not apply them: %s; `shinyhub apps restart %s` applies them",
			st.Served, describeRedeploy(st), slug)
	}
	// The pool reported ready on its settings (or was not running); confirm
	// it is still serving, or settled, now. With --verify the apply's own
	// gate runs after this, so it is not repeated here.
	if !opt.verifyHealth {
		left, err := settleBudget(deadline, slug)
		if err != nil {
			return nil, fleet.RedeployState{}, err
		}
		if err := verifyFleetHealthyForAction(cfg, slug, out, left, "redeploy", time.Now().UTC()); err != nil {
			return nil, fleet.RedeployState{}, err
		}
	}
	final, err := fetchFleetAppBy(cfg, slug, deadline)
	return final, st, err
}

// settleBudget is what is left of a settle's health timeout. A spent budget is
// an error rather than a zero timeout, which outcomeTimeout would read as
// "use the default" and so restart the wait.
func settleBudget(deadline time.Time, slug string) (time.Duration, error) {
	if left := time.Until(deadline); left > 0 {
		return left, nil
	}
	return 0, fmt.Errorf("the health timeout ran out before %s's settings redeploy was confirmed; the settings are stored but may not be live", slug)
}

// fetchFleetAppBy reads the app with the read bounded by a settle deadline.
func fetchFleetAppBy(cfg *cliConfig, slug string, deadline time.Time) (*db.App, error) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	return fetchFleetAppCtx(ctx, cfg, slug)
}

func outcomeTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return fleetHealthTimeout
	}
	return timeout
}

// fetchFleetAppWithin reads the app with the read bounded by timeout, so a
// server that stops answering cannot hold the apply past its outcome budget.
func fetchFleetAppWithin(cfg *cliConfig, slug string, timeout time.Duration) (*db.App, error) {
	ctx, cancel := context.WithTimeout(context.Background(), outcomeTimeout(timeout))
	defer cancel()
	return fetchFleetAppCtx(ctx, cfg, slug)
}

// settleConfigPatch settles the redeploy a config PATCH may have launched and
// then confirms the stored settings still match the declaration. The redeploy
// that reported may belong to a concurrent writer that changed the same keys,
// so a healthy pool alone proves nothing about this apply's values.
func settleConfigPatch(cfg *cliConfig, slug string, entry fleet.AppEntry, servedBefore int64, patched []fleet.ConfigDriftItem, opt convergeOpts, out io.Writer) error {
	app, err := fetchFleetAppWithin(cfg, slug, opt.healthTimeout)
	if err != nil {
		return fmt.Errorf("read settings after config patch: %w", err)
	}
	app, err = settleRedeploy(cfg, slug, servedBefore, app, patched, opt, out)
	if err != nil {
		return err
	}
	if left := fleet.ConfigDrift(entry, observedFromApp(*app)); len(left) > 0 {
		return fmt.Errorf("stored config no longer matches the declaration after apply (another writer changed it, or the server adjusted the value): %s", describeDrift(left))
	}
	return nil
}

// checkRedeployBacklog covers an app the plan found already matching. A
// matching declaration says the settings are stored, not that they are live:
// an earlier apply's redeploy may still be owed (this run raced it, or the
// server restarted mid-cycle), or it may have reported a failure that the
// apply which launched it never saw. An owed redeploy is waited for and
// judged like one this run launched, even when it reports before this read.
// A bad latest outcome is reported, and the serving-state gate runs even
// without --verify, so a pool left off its settings is recorded in sync only
// when it is serving. An app the plan saw settled costs no request.
func checkRedeployBacklog(cfg *cliConfig, slug string, entry fleet.AppEntry, obs fleet.ObservedApp, opt convergeOpts, out io.Writer) error {
	if !opt.redeployOutcome {
		return nil
	}
	if !obs.Redeploy.Owed() && redeployWarning(slug, obs.Redeploy) == "" {
		return nil
	}
	return settleConfigPatch(cfg, slug, entry, obs.Redeploy.Served, nil, opt, out)
}

// awaitRedeployOutcome polls the app until the latest launched settings
// redeploy has reported, so a launch during the wait extends it. A 4xx read is
// fatal (the app is gone or access was revoked); other read failures are
// retried until the deadline.
func awaitRedeployOutcome(cfg *cliConfig, slug string, timeout time.Duration, out io.Writer) (*db.App, error) {
	timeout = outcomeTimeout(timeout)
	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	announced := int64(-1)
	var lastErr error
	for {
		app, err := fetchFleetAppCtx(ctx, cfg, slug)
		if err == nil {
			st := redeployStateOf(app)
			if !st.Owed() {
				return app, nil
			}
			if st.Launched != announced {
				fmt.Fprintf(out, "  %s: waiting for the settings redeploy (seq %d) to report\n", slug, st.Launched)
				announced = st.Launched
			}
		} else {
			var hs *httpStatusError
			if errors.As(err, &hs) && hs.Status < 500 {
				return nil, err
			}
			lastErr = err
		}
		// The last read keeps a share of the budget, and the poll shortens to
		// fit what remains, so a budget below one interval still gets its reads.
		lastRead := min(redeployOutcomeLastRead, redeployOutcomePollEvery)
		remaining := time.Until(deadline)
		if remaining <= lastRead {
			msg := "settings redeploy outcome not reported before the health timeout ran out; the settings are stored but may not be live"
			if lastErr != nil {
				msg += fmt.Sprintf(" (last read error: %v)", lastErr)
			}
			return nil, errors.New(msg)
		}
		time.Sleep(min(redeployOutcomePollEvery, remaining-lastRead))
	}
}

// describeDrift renders drift items as "key server -> desired" for errors.
func describeDrift(items []fleet.ConfigDriftItem) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s %s -> %s", it.Key, it.Server, it.Desired))
	}
	return strings.Join(parts, ", ")
}
