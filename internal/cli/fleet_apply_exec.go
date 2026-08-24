package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/fleet"
)

// attemptOutcome records why a single deploy attempt failed. Only failed
// attempts are recorded; a successful attempt produces no outcome.
type attemptOutcome struct {
	Attempt int
	Kind    deployfail.Kind
	Err     string
}

// convergeOpts carries the run-wide knobs for one apply invocation.
type convergeOpts struct {
	adopt              bool
	prune              bool
	allowDegradedPrune bool
	preconditions      bool // server supports If-Match-style headers
	retries            int  // attempts AFTER the first for deploys/transient config PATCHes
	healthTimeout      time.Duration
	warmTimeout        time.Duration
	waitForWarm        bool
	verifySchedules    bool
	verifyHealth       bool
	restartAfterWarm   bool
	concurrency        int // max apps converged in parallel; <=1 means serial
	fleetID            string
	runID              string
	fleetState         bool // server persists per-app declaration/convergence state
}

const (
	failureConfigReassertFailed = "config_reassert_failed"
	failureInvalidAction        = "invalid_action"
	failureHealthVerification   = "health_verification_failed"
)

// resultWarningWriter preserves live progress output while collecting the
// warning as structured per-app result data for --json callers.
type resultWarningWriter struct {
	io.Writer
	result *applyResult
}

func (w *resultWarningWriter) addFleetWarning(message string) {
	if message == "" {
		return
	}
	w.result.warnings = append(w.result.warnings, message)
}

// convergeFleet drives every diff entry, continue-on-error, returning one
// applyResult per app in manifest order. With concurrency>1 it runs a bounded
// worker pool; otherwise the serial path. Both share convergeApp; any change to
// one loop body MUST be mirrored in the other so the paths cannot diverge.
func convergeFleet(cfg *cliConfig, pf *preflightResult, opt convergeOpts, out io.Writer) []applyResult {
	marker := "fleet:" + opt.fleetID
	entries := make(map[string]fleet.AppEntry, len(pf.manifest.Apps))
	for _, a := range pf.manifest.Apps {
		entries[a.Slug] = a
	}
	if opt.concurrency <= 1 {
		return convergeSerial(cfg, pf, entries, opt, marker, out)
	}
	return convergeParallel(cfg, pf, entries, opt, marker, out)
}

// convergeSerial is the original loop, kept verbatim so --concurrency 1 is
// byte-for-byte today's behaviour (same output order). Any loop-body change
// here must also be made in convergeParallel.
func convergeSerial(cfg *cliConfig, pf *preflightResult, entries map[string]fleet.AppEntry, opt convergeOpts, marker string, out io.Writer) []applyResult {
	results := make([]applyResult, 0, len(pf.diff))
	for _, d := range pf.diff {
		results = append(results, convergeAppFromSpec(
			cfg, d, entries[d.Slug], pf.observed[d.Slug], pf.bundles[d.Slug],
			opt, marker, out))
	}
	return results
}

// convergeParallel runs up to opt.concurrency convergeApp calls at once. Each
// goroutine writes its own results[i] index (pre-allocated slice, never
// appended) so the returned order is manifest order regardless of completion
// order. Progress writes are serialized whole-line by syncWriter.
func convergeParallel(cfg *cliConfig, pf *preflightResult, entries map[string]fleet.AppEntry, opt convergeOpts, marker string, out io.Writer) []applyResult {
	results := make([]applyResult, len(pf.diff))
	sw := &syncWriter{w: out}
	sem := make(chan struct{}, opt.concurrency)
	var wg sync.WaitGroup
	for i, d := range pf.diff {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, d fleet.AppDiff) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = convergeAppFromSpec(
				cfg, d, entries[d.Slug], pf.observed[d.Slug], pf.bundles[d.Slug],
				opt, marker, sw)
		}(i, d)
	}
	wg.Wait()
	return results
}

// syncWriter serializes concurrent writes so each progress line (one Fprintf =
// one Write) stays whole when N apps converge in parallel.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// precondPtrs returns the (ifDigest, ifManagedBy) header pointers for a
// gated mutation, or (nil, nil) in degraded mode (no server preconditions).
func precondPtrs(opt convergeOpts, digest, managedBy string) (*string, *string) {
	if !opt.preconditions {
		return nil, nil
	}
	d, m := digest, managedBy
	return &d, &m
}

// declaredProject flattens a manifest's declared project for the create path.
// Both nil ("not declared") and a non-nil "" ("ungrouped") produce "", because
// a new app is ungrouped by default either way; the distinction only matters on
// the PATCH path, where the pointer preserves it.
func declaredProject(c fleet.Config) string {
	if c.Project == nil {
		return ""
	}
	return *c.Project
}

// deployWithRetry runs the per-app deploy up to 1+retries times and returns
// the freshly promoted digest. Current servers fence the upload against the
// exact digest and fleet owner observed by plan, so overlapping applies cannot
// silently become last-writer-wins.
// committed is true if any attempt's bundle was accepted by the server, so
// callers can tell a pre-commit failure (safe to roll back) from a post-commit
// one (this fleet's source is already live). Once committed, retries only
// re-check health and digest readback; they never upload the bundle again.
func deployWithRetry(cfg *cliConfig, slug string, spec bundleBuildSpec, visibility, project string, opt convergeOpts, out io.Writer, expectedDigest, expectedManagedBy string) (promoted string, attempts int, committed bool, firstFires []firstFireRef, failed []attemptOutcome, err error) {
	total := 1 + opt.retries
	ifDigest, ifManagedBy := precondPtrs(opt, expectedDigest, expectedManagedBy)
	for attempts = 1; attempts <= total; attempts++ {
		var c bool
		var ff []firstFireRef
		var kind deployfail.Kind
		promoted, c, ff, kind, err = deployAppBundleFromSpec(cfg, slug, spec, visibility, project, out, opt.runID, opt.healthTimeout, ifDigest, ifManagedBy)
		committed = committed || c
		// Keep the first-fire refs from whichever attempt actually fired them.
		// A later retry of an already-created schedule returns none (the gate is
		// closed), so it must not clobber an earlier attempt's refs.
		if len(ff) > 0 {
			firstFires = ff
		}
		if err == nil {
			return promoted, attempts, committed, firstFires, failed, nil
		}
		failed = append(failed, attemptOutcome{Attempt: attempts, Kind: kind, Err: err.Error()})
		if attempts == total {
			return "", attempts, committed, firstFires, failed, err
		}
		if committed {
			for attempts++; attempts <= total; attempts++ {
				promoted, err = completeCommittedDeploy(cfg, slug, out, opt.healthTimeout)
				if err == nil {
					return promoted, attempts, true, firstFires, failed, nil
				}
				failed = append(failed, attemptOutcome{Attempt: attempts, Kind: deployfail.ReadinessTimeout, Err: err.Error()})
			}
			return "", total, true, firstFires, failed, err
		}
		if !retryableDeployFailure(kind) {
			return "", attempts, false, firstFires, failed, err
		}
	}
	return "", total, committed, firstFires, failed, err
}

func completeCommittedDeploy(cfg *cliConfig, slug string, out io.Writer, timeout time.Duration) (string, error) {
	if err := verifyFleetHealthy(cfg, slug, out, timeout); err != nil {
		return "", err
	}
	return readPromotedDigest(cfg, slug)
}

// retryableDeployFailure is intentionally narrow. Infrastructure and timing
// failures may heal; invalid bundles, missing runtimes, build/hook errors and
// crashes are deterministic until the source or server is changed and must not
// be repeated implicitly.
func retryableDeployFailure(kind deployfail.Kind) bool {
	switch kind {
	case deployfail.ReadinessTimeout, deployfail.ServerError, deployfail.TransportError:
		return true
	default:
		return false
	}
}

// resolveFirstFires records the per-schedule first-fire outcomes on res and,
// when --wait-for-warm or --restart-after-warm is set, polls each run to
// completion. Without either it only records that the runs were triggered.
// The warm timeout is one deadline shared by every first-fire for the app. A
// timeout is a convergence failure: "still warming" is not equivalent to
// "successfully warmed." skipped_overlap is recorded but left to the final
// level check, which may pass only if the overlapping run actually succeeded.
func resolveFirstFires(cfg *cliConfig, slug string, refs []firstFireRef, opt convergeOpts, res *applyResult, out io.Writer) error {
	timeout := warmTimeoutDuration(opt.warmTimeout)
	if res.warmDeadline.IsZero() {
		res.warmDeadline = time.Now().Add(timeout)
	}
	ctx, cancel := context.WithDeadline(context.Background(), res.warmDeadline)
	defer cancel()
	deadline := res.warmDeadline
	for _, ref := range refs {
		oc := firstFireOutcome{Schedule: ref.Schedule, RunID: ref.RunID}
		if opt.waitForWarm || opt.restartAfterWarm {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				res.failureKind = failureWarmWaitTimeout
				appendScheduleLog(cfg, slug, ref.ScheduleID, ref.RunID, ref.Schedule, res)
				return fmt.Errorf("schedule %q first-fire not confirmed within --warm-timeout %s: %w", ref.Schedule, timeout, errFirstFireTimeout)
			}
			poll := func() (string, error) { return pollScheduleRunStatusContext(ctx, cfg, slug, ref.ScheduleID, ref.RunID) }
			// Fleet apps converge concurrently, and schedule names are only unique
			// within an app. Include the slug in live progress so two apps with a
			// same-named schedule remain distinguishable when their lines interleave.
			label := fleetFirstFireLabel(slug, ref.Schedule)
			status, werr := waitForFirstFireLoop(poll, remaining, 2*time.Second, fleetHealthProgressInterval, time.Now, time.Sleep, out, label)
			oc.Status = status
			res.firstFires = append(res.firstFires, oc)
			if werr != nil {
				res.failureKind = failureWarmStateUnavailable
				if errors.Is(werr, errFirstFireTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					res.failureKind = failureWarmWaitTimeout
				}
				appendScheduleLog(cfg, slug, ref.ScheduleID, ref.RunID, ref.Schedule, res)
				return fmt.Errorf("schedule %q first-fire not confirmed within --warm-timeout %s: %w", ref.Schedule, timeout, werr)
			}
			if status == "skipped_overlap" {
				continue
			}
			if !firstFireStatusOK(status) {
				res.failureKind = failureWarmFirstFireFailed
				appendScheduleLog(cfg, slug, ref.ScheduleID, ref.RunID, ref.Schedule, res)
				return fmt.Errorf("schedule %q first-fire %s", ref.Schedule, status)
			}
			continue
		}
		res.firstFires = append(res.firstFires, oc)
	}
	return nil
}

func fleetFirstFireLabel(slug, schedule string) string {
	return slug + "/" + schedule
}

// applyConfigDrift patches exactly the drifted fleet-declared keys. A
// "visibility" drift goes to the access endpoint; the numeric keys go to
// PATCH /api/apps/{slug}. Both carry the same precondition.
func applyConfigDrift(cfg *cliConfig, slug string, drift []fleet.ConfigDriftItem, declared fleet.Config, ifD, ifMB *string, runID string) error {
	body := map[string]any{}
	for _, c := range drift {
		switch c.Key {
		case "visibility":
			if err := patchAppAccess(cfg, slug, c.Desired, ifD, ifMB, runID); err != nil {
				return err
			}
		case "hibernate_timeout_minutes":
			if c.Desired == "(default)" {
				body[c.Key] = nil
				continue
			}
			n, perr := strconv.Atoi(c.Desired)
			if perr != nil {
				return fmt.Errorf("app %s: invalid desired %s=%q: %w", slug, c.Key, c.Desired, perr)
			}
			// The fleet manifest's -1 sentinel has the same meaning as the
			// bundle parser's reset flag and PATCH's JSON null.
			if n == -1 {
				body[c.Key] = nil
			} else {
				body[c.Key] = n
			}
		case "replicas", "max_sessions_per_replica", "min_warm_replicas",
			"memory_limit_mb", "cpu_quota_percent", "worker_grouped_size",
			"worker_max_workers", "worker_warm_spares", "worker_max_session_lifetime_secs":
			n, perr := strconv.Atoi(c.Desired)
			if perr != nil {
				return fmt.Errorf("app %s: invalid desired %s=%q: %w", slug, c.Key, c.Desired, perr)
			}
			body[c.Key] = n
		case "name", "description", "worker_isolation":
			// Rebuilt from the declared config, like autoscale below: the drift
			// item carries a quoted display string, not a value to send.
			if v := declaredString(declared, c.Key); v != nil {
				body[c.Key] = *v
			}
		case "icon":
			if declared.Icon != nil {
				body["icon_emoji"] = *declared.Icon
			}
		case "render_seconds":
			v, perr := strconv.ParseFloat(c.Desired, 64)
			if perr != nil {
				return fmt.Errorf("app %s: invalid desired %s=%q: %w", slug, c.Key, c.Desired, perr)
			}
			body[c.Key] = v
		case "identity_headers":
			v, perr := strconv.ParseBool(c.Desired)
			if perr != nil {
				return fmt.Errorf("app %s: invalid desired %s=%q: %w", slug, c.Key, c.Desired, perr)
			}
			body[c.Key] = v
		case "project":
			// The drift key is `project` (what the operator wrote in the
			// manifest); the API field is `project_slug`. Rebuilt from the
			// declared config like name and description above: the drift item
			// carries a quoted display string, not a value to send.
			if v := declaredString(declared, c.Key); v != nil {
				body["project_slug"] = *v
			}
		case "autoscale":
			// autoscale is a compound value: reconstruct the PATCH object from the
			// declared config rather than parsing the human display string.
			if declared.Autoscale != nil {
				body["autoscale"] = autoscalePatchBody(declared.Autoscale)
			}
		}
	}
	return patchApp(cfg, slug, body, ifD, ifMB, runID)
}

// applyConfigDriftWithRetry gives transient server failures on the config-only
// path the same retry budget deploy-bearing actions already receive. 4xx
// validation/precondition responses are deterministic and never retried.
func applyConfigDriftWithRetry(cfg *cliConfig, slug string, drift []fleet.ConfigDriftItem, declared fleet.Config, ifD, ifMB *string, runID string, retries int) (int, error) {
	for attempt := 1; ; attempt++ {
		err := applyConfigDrift(cfg, slug, drift, declared, ifD, ifMB, runID)
		if err == nil || attempt > retries || !retryableFleetPatch(err) {
			return attempt, err
		}
	}
}

func retryableFleetPatch(err error) bool {
	var hs *httpStatusError
	return errors.As(err, &hs) && hs.Status >= 500
}

// declaredString returns the declared value for a string config key, or nil
// when the manifest does not declare it. Keyed by the same strings the drift
// items use so the two cannot drift apart.
func declaredString(c fleet.Config, key string) *string {
	switch key {
	case "name":
		return c.Name
	case "description":
		return c.Description
	case "project":
		return c.Project
	case "worker_isolation":
		return c.WorkerIsolation
	}
	return nil
}

// reassertFleetConfig re-PATCHes the fleet-declared keys that a bundle deploy
// may have overwritten from the new bundle's shinyhub.toml: the autoscale
// policy and the display metadata (name, description, project), all of which
// the bundle [app] block can also declare. The fleet manifest is the outer
// authority, so it wins over the bundle. Every key here is one whose PATCH
// does NOT trigger a redeploy (unlike replicas), so this is safe to run after
// any deploy, and it is idempotent when the value was already applied via
// drift. No-op when the manifest declares none of them.
func reassertFleetConfig(cfg *cliConfig, slug string, c fleet.Config, ifD, ifMB *string, runID string) error {
	body := map[string]any{}
	if c.Autoscale != nil {
		body["autoscale"] = autoscalePatchBody(c.Autoscale)
	}
	if c.Name != nil {
		body["name"] = *c.Name
	}
	if c.Description != nil {
		body["description"] = *c.Description
	}
	if c.Project != nil {
		body["project_slug"] = *c.Project
	}
	if len(body) == 0 {
		return nil
	}
	return patchApp(cfg, slug, body, ifD, ifMB, runID)
}

// adoptBundleWentLive answers whether an adopt redeploy that returned an error
// nonetheless durably promoted a new bundle. The deploy endpoint returns 500 on
// both pre-promotion and post-promotion paths, so the HTTP status cannot decide
// it; instead we read back the live content digest and report whether it
// advanced past the pre-deploy one.
//
// "Durably" is deliberate. The server treats the promoted (succeeded)
// deployment row as the single source of truth - it is the pointer the
// scheduler, watcher wake, restart, and rollback all consult - and exposes its
// digest on /api/apps. When PromoteDeployment fails after the pool was switched
// the server returns 500 with that pointer NOT advanced, and documents the
// state as "pool is live but the next restart/wake reverts to the old bundle;
// retry to commit". In that case this reports false and the reservation is
// released, which is correct: ownership tracks the durable deployment, and the
// transient running pool is a server-acknowledged inconsistency that self-heals
// on the retry the server asks for. An inconclusive readback (transport error,
// or a server that does not expose a digest) likewise reports false, so we fall
// back to releasing the reservation.
func adoptBundleWentLive(cfg *cliConfig, slug, preDeployDigest string) bool {
	dg, err := readPromotedDigest(cfg, slug)
	if err != nil || dg == "" {
		return false
	}
	return dg != preDeployDigest
}

// releaseAdoptReservation restores managed_by to its observed prior value
// after an adopt redeploy fails, undoing the ownership reservation. The patch
// is gated on the marker we just stamped so it cannot clobber an intervening
// writer. Best-effort: a failed release narrows but cannot fully close the
// limbo window, which is strictly better than always leaving the marker
// stamped on deploy failure.
//
// In degraded mode (no precondition support) the release would be unguarded
// and could clear or overwrite a new owner that took the app between the
// reservation and the deploy failure, so it is skipped: the documented
// degraded race is accepted rather than risking a clobber.
func releaseAdoptReservation(cfg *cliConfig, slug string, prior *string, marker string, opt convergeOpts) {
	if !opt.preconditions {
		return
	}
	m := marker
	_ = patchManagedBy(cfg, slug, prior, nil, &m, opt.runID)
}

// convergeApp reconciles one app. It is total over fleet.Action; an
// unrecognized action is reported as skipped rather than silently dropped.
func convergeApp(cfg *cliConfig, d fleet.AppDiff, entry fleet.AppEntry, obs fleet.ObservedApp, srcDir string, opt convergeOpts, marker string, out io.Writer) applyResult {
	return convergeAppFromSpec(cfg, d, entry, obs, bundleBuildSpec{Dir: srcDir}, opt, marker, out)
}

func convergeAppFromSpec(cfg *cliConfig, d fleet.AppDiff, entry fleet.AppEntry, obs fleet.ObservedApp, spec bundleBuildSpec, opt convergeOpts, marker string, out io.Writer) applyResult {
	start := time.Now()
	res := applyResult{slug: d.Slug, action: d.Action, mutation: mutationNone}
	stateAlreadyRecorded := false
	declaredState := fleet.DeclaredState(entry)
	done := func(s applyStatus) applyResult {
		res.status, res.duration = s, time.Since(start)
		switch s {
		case statusCreated, statusUpdated, statusDeleted, statusAdopted:
			res.mutation = mutationCommitted
		}
		if opt.fleetState && !stateAlreadyRecorded && s != statusDeleted && s != statusSkipped {
			if err := recordAppFleetState(cfg, d.Slug, fleetConvergenceInSync, d.LocalDigest, declaredState, "", opt.runID); err != nil {
				res.status = statusFailed
				res.err = fmt.Errorf("record fleet convergence: %w", err)
			}
		}
		return res
	}
	fail := func(err error, attempts int) applyResult {
		res.attempts, res.err, res.duration = attempts, err, time.Since(start)
		if isConflictError(err) {
			res.status = statusConflict
		} else {
			res.status = statusFailed
		}
		if opt.fleetState && !stateAlreadyRecorded {
			_ = recordAppFleetState(cfg, d.Slug, fleetConvergenceIncomplete, d.LocalDigest, declaredState, err.Error(), opt.runID)
			stateAlreadyRecorded = true
		}
		return res
	}
	// failDeploy records a failure of the bundle deploy itself (the app most
	// likely crashed on startup) and attaches its log tail so the operator sees
	// the cause inline instead of SSHing to read the process log. It is used
	// only where the deploy step failed; post-deploy config/ownership patch and
	// first-fire failures use fail (the app is running, so its tail would be
	// misleading).
	failDeploy := func(err error, attempts int, mutation applyMutationState) applyResult {
		fail(err, attempts)
		res.mutation = mutation
		// Mark this as a deploy-bearing failure so the top-level failure_kind is
		// attributed to the deploy. A post-deploy failure (config patch, first-fire)
		// uses fail directly and must NOT inherit a deploy attempt's kind.
		res.deployFailed = true
		if res.status == statusFailed {
			if tail, lerr := fetchLogTail(cfg, d.Slug, logTailLines); lerr == nil {
				res.logTail = tail
			}
		}
		return res
	}
	finish := func(status applyStatus, attempts int) applyResult {
		if opt.waitForWarm {
			warmBudget := warmTimeoutDuration(opt.warmTimeout)
			if res.warmDeadline.IsZero() {
				res.warmDeadline = time.Now().Add(warmBudget)
			}
			remaining := time.Until(res.warmDeadline)
			if remaining <= 0 {
				res.failureKind = failureWarmWaitTimeout
				return fail(fmt.Errorf("warm gate not confirmed within --warm-timeout %s: %w", warmBudget, errFirstFireTimeout), attempts)
			}
			if err := verifyExistingWarmGateWithWait(cfg, d.Slug, spec.Dir, &res, remaining, out); err != nil {
				return fail(err, attempts)
			}
		}
		if opt.verifySchedules {
			if err := verifyEnabledScheduleFreshness(cfg, d.Slug, &res); err != nil {
				return fail(err, attempts)
			}
		}
		if opt.verifyHealth {
			if err := verifyFleetHealthy(cfg, d.Slug, out, opt.healthTimeout); err != nil {
				res.failureKind = failureHealthVerification
				return fail(err, attempts)
			}
		}
		if opt.restartAfterWarm && len(res.firstFires) > 0 {
			restarted, err := restartAppAfterWarm(cfg, d.Slug, out)
			if err != nil {
				res.failureKind = failureWarmRestartFailed
				return fail(err, attempts)
			}
			res.warmRestarted = restarted
		}
		return done(status)
	}

	switch d.Action {
	case fleet.ActionUnchanged:
		return finish(statusUnchanged, 0)

	case fleet.ActionAdopt:
		if !opt.adopt {
			res.note = "present, not owned by this fleet; re-run with --adopt"
			return done(statusSkipped)
		}
		// Reserve ownership FIRST with a precondition asserting the managed_by
		// we observed is still current (empty string asserts "currently
		// unmanaged"). Reserving before the deploy means a concurrent ownership
		// change is rejected as a 409 BEFORE we upload a bundle - otherwise we
		// could overwrite an app we no longer own.
		var ifD, ifMB *string
		if opt.preconditions {
			// Assert the source observed during plan as well as its owner. Without
			// the digest guard, a concurrent deploy could land between preflight
			// and this reservation and then be silently claimed by the fleet.
			if d.ServerDigest != "" {
				digest := d.ServerDigest
				ifD = &digest
			}
			cur := ""
			if obs.ManagedBy != nil {
				cur = *obs.ManagedBy
			}
			ifMB = &cur
		}
		if err := patchManagedBy(cfg, d.Slug, &marker, ifD, ifMB, opt.runID); err != nil {
			return fail(err, 1)
		}
		res.mutation = mutationUnknown
		// A conditional ownership PATCH is sufficient when source and every
		// declared setting already match. The digest and owner preconditions make
		// this atomic with respect to the preflight observation, so no release or
		// health check is needed and no redundant deployment is manufactured. Older
		// servers without preconditions retain the conservative redeploy path.
		if opt.preconditions && d.LocalDigest != "" && d.LocalDigest == d.ServerDigest && len(d.ConfigDrift) == 0 {
			res.attempts = 1
			res.note = "ownership adopted; source and declared config already matched"
			return done(statusAdopted)
		}
		// Redeploy (idempotent if identical). If it fails without the new
		// bundle going live, RELEASE the reservation - restore managed_by to
		// its observed prior value - so a deploy failure never leaves an "owned
		// but undeployed" record and the next plan proposes a clean adopt
		// rather than mislabelling it update(source). If the bundle did go live
		// (2xx, or an ambiguous error whose readback shows the promoted digest
		// advanced past the pre-deploy one), the reservation is KEPT because
		// this fleet's source is now the app's bundle.
		promoted, attempts, committed, firstFires, failed, err := deployWithRetry(
			cfg, d.Slug, spec, entry.Visibility, declaredProject(entry.Config), opt,
			&resultWarningWriter{Writer: out, result: &res}, d.ServerDigest, marker,
		)
		res.attemptsDetail = failed
		if err != nil {
			wentLive := committed
			if !wentLive {
				wentLive = adoptBundleWentLive(cfg, d.Slug, d.ServerDigest)
			}
			if !wentLive {
				releaseAdoptReservation(cfg, d.Slug, obs.ManagedBy, marker, opt)
			}
			state := mutationUnknown
			if wentLive {
				state = mutationPartial
			}
			return failDeploy(err, attempts, state)
		}
		res.mutation = mutationPartial
		if ffErr := resolveFirstFires(cfg, d.Slug, firstFires, opt, &res, out); ffErr != nil {
			return fail(ffErr, attempts)
		}
		res.attempts = attempts
		// Assert the manifest's full declared config on top of the bundle's.
		ifD, ifM := precondPtrs(opt, promoted, marker)
		if err := patchApp(cfg, d.Slug, fleetConfigBody(entry.Config), ifD, ifM, opt.runID); err != nil {
			return fail(err, attempts)
		}
		if entry.Visibility != "" && entry.Visibility != obs.Access {
			if err := patchAppAccess(cfg, d.Slug, entry.Visibility, ifD, ifM, opt.runID); err != nil {
				return fail(err, attempts)
			}
		}
		return finish(statusAdopted, attempts)

	case fleet.ActionDelete:
		if !opt.prune {
			res.note = "prune candidate; re-run with --prune"
			return done(statusSkipped)
		}
		if !opt.preconditions && !opt.allowDegradedPrune {
			res.note = "prune disabled in degraded mode; upgrade the server or pass --allow-unsafe-degraded-prune"
			return done(statusSkipped)
		}
		ifD, ifM := precondPtrs(opt, d.ServerDigest, marker)
		if err := deleteFleetApp(cfg, d.Slug, ifD, ifM, opt.runID); err != nil {
			return fail(err, 1)
		}
		return done(statusDeleted)

	case fleet.ActionCreate:
		promoted, attempts, committed, firstFires, failed, err := deployWithRetry(
			cfg, d.Slug, spec, entry.Visibility, declaredProject(entry.Config), opt,
			&resultWarningWriter{Writer: out, result: &res}, "", "",
		)
		res.attempts = attempts
		res.attemptsDetail = failed
		if err != nil {
			state := mutationUnknown
			if committed {
				state = mutationPartial
			}
			return failDeploy(err, attempts, state)
		}
		res.mutation = mutationPartial
		if ffErr := resolveFirstFires(cfg, d.Slug, firstFires, opt, &res, out); ffErr != nil {
			return fail(ffErr, attempts)
		}
		// create => app was just made, currently unmanaged. Stamp failure is
		// non-fatal and self-healing (next plan shows adopt) UNLESS it is a
		// precondition conflict, which is a real concurrency signal.
		var ifD, ifM *string
		if opt.preconditions {
			empty := ""
			ifM = &empty
			if promoted != "" {
				p := promoted
				ifD = &p
			}
		}
		if err := patchManagedBy(cfg, d.Slug, &marker, ifD, ifM, opt.runID); err != nil {
			return fail(fmt.Errorf("deployed but ownership marker was not stamped: %w", err), attempts)
		}
		// Apply the manifest's declared [app.config] to the freshly created app.
		// The deploy set the source bundle and visibility; the numeric config
		// (hibernate_timeout, replicas, max_sessions) is applied here so the new
		// app fully matches the manifest and the next plan is a clean no-op rather
		// than spurious "update(config)" drift. Gated on the marker we just
		// stamped (and the promoted digest when known) so a concurrent writer
		// cannot be clobbered. Best-effort: on failure the next plan reapplies it.
		if cfgDrift := fleet.DeclaredConfig(entry); len(cfgDrift) > 0 {
			var ifDc, ifMc *string
			if opt.preconditions {
				m := marker
				ifMc = &m
				if promoted != "" {
					p := promoted
					ifDc = &p
				}
			}
			if err := applyConfigDrift(cfg, d.Slug, cfgDrift, entry.Config, ifDc, ifMc, opt.runID); err != nil {
				return fail(fmt.Errorf("created but declared config was not fully applied: %w", err), attempts)
			}
		}
		return finish(statusCreated, attempts)

	case fleet.ActionUpdateSource:
		promoted, attempts, committed, firstFires, failed, err := deployWithRetry(
			cfg, d.Slug, spec, entry.Visibility, declaredProject(entry.Config), opt,
			&resultWarningWriter{Writer: out, result: &res}, d.ServerDigest, marker,
		)
		res.attempts = attempts
		res.attemptsDetail = failed
		if err != nil {
			state := mutationNone
			if committed {
				state = mutationPartial
			}
			return failDeploy(err, attempts, state)
		}
		res.mutation = mutationPartial
		if ffErr := resolveFirstFires(cfg, d.Slug, firstFires, opt, &res, out); ffErr != nil {
			return fail(ffErr, attempts)
		}
		// A source-only diff means the pre-deploy config matched the manifest, but
		// the new bundle's shinyhub.toml can overwrite the autoscale columns and
		// the display metadata. Reassert those (gated on the freshly promoted
		// digest).
		ifD, ifM := precondPtrs(opt, promoted, marker)
		if err := reassertFleetConfig(cfg, d.Slug, entry.Config, ifD, ifM, opt.runID); err != nil {
			return fail(fmt.Errorf("source updated but declared config was not reasserted: %w", err), attempts)
		}
		return finish(statusUpdated, attempts)

	case fleet.ActionUpdateConfig:
		res.mutation = mutationUnknown
		ifD, ifM := precondPtrs(opt, d.ServerDigest, marker)
		attempts, err := applyConfigDriftWithRetry(cfg, d.Slug, d.ConfigDrift, fleet.EffectiveConfig(entry), ifD, ifM, opt.runID, opt.retries)
		if err != nil {
			return fail(err, attempts)
		}
		res.attempts = attempts
		res.mutation = mutationPartial
		return finish(statusUpdated, attempts)

	case fleet.ActionUpdateSourceConfig:
		// Mandatory ordering: deploy first, then patch fleet config
		// on top with a precondition built from the FRESHLY promoted digest -
		// never the stale pre-deploy one.
		promoted, attempts, committed, firstFires, failed, err := deployWithRetry(
			cfg, d.Slug, spec, entry.Visibility, declaredProject(entry.Config), opt,
			&resultWarningWriter{Writer: out, result: &res}, d.ServerDigest, marker,
		)
		res.attempts = attempts
		res.attemptsDetail = failed
		if err != nil {
			state := mutationNone
			if committed {
				state = mutationPartial
			}
			return failDeploy(err, attempts, state)
		}
		res.mutation = mutationPartial
		if ffErr := resolveFirstFires(cfg, d.Slug, firstFires, opt, &res, out); ffErr != nil {
			return fail(ffErr, attempts)
		}
		ifD, ifM := precondPtrs(opt, promoted, marker)
		// The deploy has just asserted every bundle-declared [app] value. Apply
		// only the outer fleet config on top: it is the higher-precedence layer,
		// and avoiding a second bundle PATCH prevents redundant worker/resource
		// redeploys for fields the deploy already converged.
		if err := patchApp(cfg, d.Slug, fleetConfigBody(entry.Config), ifD, ifM, opt.runID); err != nil {
			return fail(err, attempts)
		}
		// d.ConfigDrift was computed pre-deploy: if autoscale or the display
		// metadata matched then but the new bundle overwrites it, it is not in the
		// drift list. Reassert those (a no-op re-PATCH when they were already
		// applied above; idempotent and non-redeploy-triggering) so the fleet
		// manifest still wins.
		if err := reassertFleetConfig(cfg, d.Slug, entry.Config, ifD, ifM, opt.runID); err != nil {
			res.failureKind = failureConfigReassertFailed
			return fail(fmt.Errorf("source updated but declared config was not fully reasserted: %w", err), attempts)
		}
		return finish(statusUpdated, attempts)
	}

	res.failureKind = failureInvalidAction
	return fail(fmt.Errorf("unknown fleet action %q", d.Action), 0)
}
