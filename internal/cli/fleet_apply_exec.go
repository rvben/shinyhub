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
func deployWithRetry(cfg *cliConfig, slug, dir, visibility string, opt convergeOpts, out io.Writer) (promoted string, attempts int, err error) {
	total := 1 + opt.retries
	for attempts = 1; attempts <= total; attempts++ {
		promoted, err = deployAppBundle(cfg, slug, dir, visibility, out, opt.runID)
		if err == nil {
			return promoted, attempts, nil
		}
	}
	return "", total, err
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
		// Stamp the marker, asserting the managed_by we observed is still
		// current (empty string asserts "currently unmanaged").
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
		// Reconcile like an update: redeploy (idempotent if identical) then
		// assert the manifest's full declared config on top of the bundle's.
		promoted, attempts, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
		if err != nil {
			return fail(err, attempts)
		}
		res.attempts = attempts
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
		promoted, attempts, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
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
		_, attempts, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
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
		promoted, attempts, err := deployWithRetry(cfg, d.Slug, srcDir, entry.Visibility, opt, out)
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
