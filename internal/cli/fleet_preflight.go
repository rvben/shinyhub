package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

// preflightResult is the shared output of the cheap-first pre-flight pipeline
// (cost ordering: cheap local checks, one auth/server call, then remote source
// resolution, then the diff). plan, apply, and apply --dry-run all consume
// this so they cannot diverge. sources maps each manifest slug to the resolved
// local directory its bundle is built from (a git source points at the temp
// clone). cleanup removes any temp clones; the caller MUST defer it.
type preflightResult struct {
	manifest *fleet.Manifest
	caps     serverCaps
	host     string
	diff     []fleet.AppDiff
	sources  map[string]string
	observed map[string]fleet.ObservedApp
	cleanup  func()
}

// fleetPreflight runs manifest+local validation, one auth/server call, then
// remote source resolution, then the diff. cmdName ("plan" / "apply") only
// selects the wording of the two section headers. Problems are reported to
// errOut in cost order and surfaced as an ExitCodeError carrying the
// exit code (1 manifest/usage/source, 3 transport/auth, 6 server-not-ready).
// It performs only GET requests. When waitFor > 0 it first polls
// /api/server-info until the server is a healthy shinyhub or waitFor elapses
// (the EC2-churn case). On any error it removes temp clones itself and returns
// a nil result; on success the caller owns cleanup via the returned closure.
func fleetPreflight(file string, errOut io.Writer, cmdName string, waitFor time.Duration) (*preflightResult, error) {
	var cleanups []func()
	runCleanups := func() {
		for _, c := range cleanups {
			c()
		}
	}

	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(errOut, "no %s found. Run 'shinyhub fleet init' to generate one from your\n"+
				"deployed apps, or pass -f <path> to point at an existing manifest.\n",
				filepath.Base(file))
			return nil, &ExitCodeError{Code: 1, Err: fmt.Errorf("manifest not found: %s", file), Reported: true}
		}
		return nil, &ExitCodeError{Code: 1, Err: fmt.Errorf("read %s: %w", file, err)}
	}

	m, probs := fleet.ParseManifest(data, file)

	// Validate local source existence alongside manifest structure problems so
	// operators see the full picture in one pass. (ParseManifest is pure/no-I/O
	// by design; this cheap local check lives here.) Git sources are validated
	// for URL format only; actual cloning happens after server auth succeeds.
	// ParseSource is called again in the resolve loop below; the duplication is
	// intentional - that loop also clones git sources and computes the bundle digest.
	manifestDir := filepath.Dir(file)
	type sourceCheck struct{ slug, msg string }
	var srcProbs []sourceCheck
	// m is non-nil when the TOML decoded without a hard parse error, even if
	// there are structural problems (fleet_id missing, dup slug, etc.).
	if m != nil {
		for _, app := range m.Apps {
			if app.Source == "" {
				// Already reported as "source is required" by ParseManifest.
				continue
			}
			if _, sp := fleet.ParseSource(app.Source, manifestDir); sp != nil {
				srcProbs = append(srcProbs, sourceCheck{app.Slug, sp.Msg})
			}
		}
	}

	if len(probs) > 0 || len(srcProbs) > 0 {
		fmt.Fprintf(errOut, "shinyhub fleet %s: validating %s\n\n", cmdName, file)
		for _, p := range probs {
			fmt.Fprintf(errOut, "  ✗ %s\n", p.Error())
		}
		for _, sc := range srcProbs {
			fmt.Fprintf(errOut, "  ✗ %s  app %q: %s\n", file, sc.slug, sc.msg)
		}
		total := len(probs) + len(srcProbs)
		fmt.Fprintf(errOut, "\n%d problem(s) found. Nothing was changed. Fix these and re-run.\n", total)
		return nil, &ExitCodeError{Code: 1, Err: fmt.Errorf("%d manifest problem(s)", total), Reported: true}
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(errOut, "  ✗ not authenticated: %v\n     run 'shinyhub login' or pass --config\n", err)
		return nil, &ExitCodeError{Code: 3, Err: err, Reported: true}
	}
	if waitFor > 0 {
		if _, werr := waitForServerReady(cfg, waitFor, serverPollInterval, errOut, time.Now, time.Sleep); werr != nil {
			fmt.Fprintf(errOut, "  ✗ %v\n", werr)
			return nil, &ExitCodeError{Code: 6, Err: werr, Reported: true}
		}
	}
	apps, err := fetchApps(cfg)
	if err != nil {
		// Distinguish "the shinyhub server isn't up yet" (a front proxy on a
		// half-provisioned box answered) from a real transport/auth failure, so
		// the operator is not sent chasing a credential problem that isn't there.
		if nr := serverReadinessProblem(cfg); nr != nil {
			fmt.Fprintf(errOut, "  ✗ %v\n     the shinyhub server is not up yet (a front proxy answered instead).\n"+
				"     retry, or pass --wait-for-server=<duration> to block until it is ready.\n", nr)
			return nil, &ExitCodeError{Code: 6, Err: nr, Reported: true}
		}
		fmt.Fprintf(errOut, "  ✗ cannot reach server %s: %v\n     check the URL / run 'shinyhub login'\n", cfg.Host, err)
		return nil, &ExitCodeError{Code: 3, Err: err, Reported: true}
	}
	caps := fetchServerCaps(cfg)

	localDigests := map[string]string{}
	sources := map[string]string{}
	var resolveProblems []string
	for _, app := range m.Apps {
		ps, sp := fleet.ParseSource(app.Source, manifestDir)
		if sp != nil {
			resolveProblems = append(resolveProblems, fmt.Sprintf("app %q: %s", app.Slug, sp.Msg))
			continue
		}
		dir := ps.LocalPath
		if ps.Kind == fleet.SourceGit {
			gd, _, _, clean, gerr := resolveGitSource(ps)
			if gerr != nil {
				resolveProblems = append(resolveProblems, fmt.Sprintf("app %q: %v", app.Slug, gerr))
				continue
			}
			cleanups = append(cleanups, clean)
			dir = gd
		}
		dg, derr := digestLocalDir(dir)
		if derr != nil {
			resolveProblems = append(resolveProblems, fmt.Sprintf("app %q: %v", app.Slug, derr))
			continue
		}
		localDigests[app.Slug] = dg
		sources[app.Slug] = dir
	}
	if len(resolveProblems) > 0 {
		fmt.Fprintf(errOut, "shinyhub fleet %s: resolving sources\n\n", cmdName)
		for _, p := range resolveProblems {
			fmt.Fprintf(errOut, "  ✗ %s\n", p)
		}
		fmt.Fprintf(errOut, "\n%d source problem(s). Nothing was changed.\n", len(resolveProblems))
		runCleanups()
		return nil, &ExitCodeError{Code: 1, Err: fmt.Errorf("%d source problem(s)", len(resolveProblems)), Reported: true}
	}

	observed := make([]fleet.ObservedApp, 0, len(apps))
	observedBySlug := make(map[string]fleet.ObservedApp, len(apps))
	for _, a := range apps {
		oa := fleet.ObservedApp{
			Slug:                    a.Slug,
			Access:                  a.Access,
			HibernateTimeoutMinutes: a.HibernateTimeoutMinutes,
			Replicas:                intPtrIfPositive(a.Replicas),
			MaxSessionsPerReplica:   intPtrIfPositive(a.MaxSessionsPerReplica),
			ContentDigest:           a.ContentDigest,
			ManagedBy:               a.ManagedBy,
			// A live GET /api/apps observation is always populated (never nil),
			// so an on-server off policy stays distinct from "not observed".
			Autoscale: &fleet.ObservedAutoscale{
				Enabled:     a.AutoscaleEnabled,
				MinReplicas: a.AutoscaleMinReplicas,
				MaxReplicas: a.AutoscaleMaxReplicas,
				Target:      a.AutoscaleTarget,
			},
		}
		observed = append(observed, oa)
		observedBySlug[a.Slug] = oa
	}
	diff := fleet.Diff(m, localDigests, observed)
	return &preflightResult{
		manifest: m, caps: caps, host: cfg.Host, diff: diff,
		sources: sources, observed: observedBySlug, cleanup: runCleanups,
	}, nil
}
