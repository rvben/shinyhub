package cli

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

// convergeOpts carries the run-wide knobs for one apply invocation.
type convergeOpts struct {
	adopt              bool
	prune              bool
	allowDegradedPrune bool
	preconditions      bool // server supports If-Match-style headers
	retries            int  // attempts AFTER the first for deploy-bearing actions
	fleetID            string
	runID              string
}

// convergeFleet drives every diff entry in manifest order, continue-on-error,
// returning one applyResult per app.
func convergeFleet(cfg *cliConfig, pf *preflightResult, opt convergeOpts, out io.Writer) []applyResult {
	marker := "fleet:" + opt.fleetID
	entries := make(map[string]fleet.AppEntry, len(pf.manifest.Apps))
	for _, a := range pf.manifest.Apps {
		entries[a.Slug] = a
	}
	results := make([]applyResult, 0, len(pf.diff))
	for _, d := range pf.diff {
		results = append(results, convergeApp(
			cfg, d, entries[d.Slug], pf.observed[d.Slug], pf.sources[d.Slug],
			opt, marker, out))
	}
	return results
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
func deployWithRetry(cfg *cliConfig, slug, dir, visibility string, opt convergeOpts, out io.Writer) (promoted string, attempts int, committed bool, err error) {
	total := 1 + opt.retries
	for attempts = 1; attempts <= total; attempts++ {
		var c bool
		promoted, c, err = deployAppBundle(cfg, slug, dir, visibility, out, opt.runID)
		committed = committed || c
		if err == nil {
			return promoted, attempts, committed, nil
		}
	}
	return "", total, committed, err
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
		promoted, attempts, committed, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
		if err != nil {
			if !committed && !adoptBundleWentLive(cfg, d.Slug, d.ServerDigest) {
				releaseAdoptReservation(cfg, d.Slug, obs.ManagedBy, marker, opt)
			}
			return fail(err, attempts)
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
		promoted, attempts, _, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
		res.attempts = attempts
		if err != nil {
			return fail(err, attempts)
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
		return done(statusCreated)

	case fleet.ActionUpdateSource:
		_, attempts, _, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
		res.attempts = attempts
		if err != nil {
			return fail(err, attempts)
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
		promoted, attempts, _, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
		res.attempts = attempts
		if err != nil {
			return fail(err, attempts)
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
