package cli

import (
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
	retries            int  // attempts AFTER the first for deploy-bearing actions
	healthTimeout      time.Duration
	waitForWarm        bool
	concurrency        int // max apps converged in parallel; <=1 means serial
	fleetID            string
	runID              string
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
		results = append(results, convergeApp(
			cfg, d, entries[d.Slug], pf.observed[d.Slug], pf.sources[d.Slug],
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
			results[i] = convergeApp(
				cfg, d, entries[d.Slug], pf.observed[d.Slug], pf.sources[d.Slug],
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

// deployWithRetry runs the per-app deploy up to 1+retries times and returns
// the freshly promoted digest. Deploy carries no precondition (last-writer-
// wins); a transient failure is retried, the attempt count is reported.
// committed is true if any attempt's bundle was accepted by the server, so
// callers can tell a pre-commit failure (safe to roll back) from a post-commit
// one (this fleet's source is already live).
func deployWithRetry(cfg *cliConfig, slug, dir, visibility string, opt convergeOpts, out io.Writer) (promoted string, attempts int, committed bool, firstFires []firstFireRef, failed []attemptOutcome, err error) {
	total := 1 + opt.retries
	for attempts = 1; attempts <= total; attempts++ {
		var c bool
		var ff []firstFireRef
		var kind deployfail.Kind
		promoted, c, ff, kind, err = deployAppBundle(cfg, slug, dir, visibility, out, opt.runID, opt.healthTimeout)
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
	}
	return "", total, committed, firstFires, failed, err
}

// resolveFirstFires records the per-schedule first-fire outcomes on res and,
// when --wait-for-warm is set, polls each run to completion. Without
// --wait-for-warm it only records that the runs were triggered (async path).
// When waiting, a non-nil error is returned only for genuine run failures:
// skipped_overlap is treated as success by firstFireStatusOK, and a timeout
// (werr != nil) is non-fatal because the run is still warming and the next
// apply self-heals.
func resolveFirstFires(cfg *cliConfig, slug string, refs []firstFireRef, opt convergeOpts, res *applyResult, out io.Writer) error {
	for _, ref := range refs {
		oc := firstFireOutcome{Schedule: ref.Schedule, RunID: ref.RunID}
		if opt.waitForWarm {
			poll := func() (string, error) { return pollScheduleRunStatus(cfg, slug, ref.ScheduleID, ref.RunID) }
			status, werr := waitForFirstFireLoop(poll, opt.healthTimeout, 2*time.Second, fleetHealthProgressInterval, time.Now, time.Sleep, out, ref.Schedule)
			oc.Status = status
			res.firstFires = append(res.firstFires, oc)
			if werr == nil && !firstFireStatusOK(status) {
				return fmt.Errorf("schedule %q first-fire %s", ref.Schedule, status)
			}
			// A timeout (werr != nil) is reported but not fatal: the run is still
			// warming and the next apply self-heals.
			continue
		}
		res.firstFires = append(res.firstFires, oc)
	}
	return nil
}

// applyConfigDrift patches exactly the drifted fleet-declared keys. A
// "visibility" drift goes to the access endpoint; the numeric keys go to
// PATCH /api/apps/{slug}. Both carry the same precondition.
func applyConfigDrift(cfg *cliConfig, slug string, drift []fleet.ConfigDriftItem, ifD, ifMB *string, runID string) error {
	body := map[string]any{}
	for _, c := range drift {
		switch c.Key {
		case "visibility":
			if err := patchAppAccess(cfg, slug, c.Desired, ifD, ifMB, runID); err != nil {
				return err
			}
		case "hibernate_timeout_minutes", "replicas", "max_sessions_per_replica":
			n, perr := strconv.Atoi(c.Desired)
			if perr != nil {
				return fmt.Errorf("app %s: invalid desired %s=%q: %w", slug, c.Key, c.Desired, perr)
			}
			body[c.Key] = n
		}
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
	start := time.Now()
	res := applyResult{slug: d.Slug, action: d.Action}
	done := func(s applyStatus) applyResult {
		res.status, res.duration = s, time.Since(start)
		return res
	}
	fail := func(err error, attempts int) applyResult {
		res.attempts, res.err, res.duration = attempts, err, time.Since(start)
		if isConflictError(err) {
			res.status = statusConflict
		} else {
			res.status = statusFailed
		}
		return res
	}
	// failDeploy records a failure of the bundle deploy itself (the app most
	// likely crashed on startup) and attaches its log tail so the operator sees
	// the cause inline instead of SSHing to read the process log. It is used
	// only where the deploy step failed; post-deploy config/ownership patch and
	// first-fire failures use fail (the app is running, so its tail would be
	// misleading).
	failDeploy := func(err error, attempts int) applyResult {
		fail(err, attempts)
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

	switch d.Action {
	case fleet.ActionUnchanged:
		return done(statusUnchanged)

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
		var ifMB *string
		if opt.preconditions {
			cur := ""
			if obs.ManagedBy != nil {
				cur = *obs.ManagedBy
			}
			ifMB = &cur
		}
		if err := patchManagedBy(cfg, d.Slug, &marker, nil, ifMB, opt.runID); err != nil {
			return fail(err, 1)
		}
		// Redeploy (idempotent if identical). If it fails without the new
		// bundle going live, RELEASE the reservation - restore managed_by to
		// its observed prior value - so a deploy failure never leaves an "owned
		// but undeployed" record and the next plan proposes a clean adopt
		// rather than mislabelling it update(source). If the bundle did go live
		// (2xx, or an ambiguous error whose readback shows the promoted digest
		// advanced past the pre-deploy one), the reservation is KEPT because
		// this fleet's source is now the app's bundle.
		promoted, attempts, committed, firstFires, failed, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
		res.attemptsDetail = failed
		if err != nil {
			if !committed && !adoptBundleWentLive(cfg, d.Slug, d.ServerDigest) {
				releaseAdoptReservation(cfg, d.Slug, obs.ManagedBy, marker, opt)
			}
			return failDeploy(err, attempts)
		}
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
		return done(statusAdopted)

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
		promoted, attempts, _, firstFires, failed, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
		res.attempts = attempts
		res.attemptsDetail = failed
		if err != nil {
			return failDeploy(err, attempts)
		}
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
			if isConflictError(err) {
				return fail(err, attempts)
			}
			res.note = "deployed; ownership marker not stamped, next plan shows adopt: " + err.Error()
			return done(statusCreated)
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
			if err := applyConfigDrift(cfg, d.Slug, cfgDrift, ifDc, ifMc, opt.runID); err != nil {
				res.note = "created; declared config not fully applied, next plan shows update(config): " + err.Error()
				return done(statusCreated)
			}
		}
		return done(statusCreated)

	case fleet.ActionUpdateSource:
		_, attempts, _, firstFires, failed, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
		res.attempts = attempts
		res.attemptsDetail = failed
		if err != nil {
			return failDeploy(err, attempts)
		}
		if ffErr := resolveFirstFires(cfg, d.Slug, firstFires, opt, &res, out); ffErr != nil {
			return fail(ffErr, attempts)
		}
		return done(statusUpdated)

	case fleet.ActionUpdateConfig:
		ifD, ifM := precondPtrs(opt, d.ServerDigest, marker)
		if err := applyConfigDrift(cfg, d.Slug, d.ConfigDrift, ifD, ifM, opt.runID); err != nil {
			return fail(err, 1)
		}
		return done(statusUpdated)

	case fleet.ActionUpdateSourceConfig:
		// Mandatory ordering: deploy first, then patch fleet config
		// on top with a precondition built from the FRESHLY promoted digest -
		// never the stale pre-deploy one.
		promoted, attempts, _, firstFires, failed, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
		res.attempts = attempts
		res.attemptsDetail = failed
		if err != nil {
			return failDeploy(err, attempts)
		}
		if ffErr := resolveFirstFires(cfg, d.Slug, firstFires, opt, &res, out); ffErr != nil {
			return fail(ffErr, attempts)
		}
		ifD, ifM := precondPtrs(opt, promoted, marker)
		if err := applyConfigDrift(cfg, d.Slug, d.ConfigDrift, ifD, ifM, opt.runID); err != nil {
			return fail(err, attempts)
		}
		return done(statusUpdated)
	}

	res.note = "unknown action " + string(d.Action)
	return done(statusSkipped)
}
