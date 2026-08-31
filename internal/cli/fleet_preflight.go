package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
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
	// projectDiff is empty when the manifest declares no [[project]] blocks, in
	// which case no GET /api/projects is issued at all, so a manifest that does
	// not use the feature behaves exactly as before.
	projectDiff []fleet.ProjectDiff
	bundles     map[string]bundleBuildSpec
	observed    map[string]fleet.ObservedApp
	cleanup     func()
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
	s := stylerFor(errOut)
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
	localSources := make(map[string]string)
	// m is non-nil when the TOML decoded without a hard parse error, even if
	// there are structural problems (fleet_id missing, dup slug, etc.).
	if m != nil {
		for _, app := range m.Apps {
			if app.Source == "" {
				// Already reported as "source is required" by ParseManifest.
				continue
			}
			parsed, sp := fleet.ParseSource(app.Source, manifestDir)
			if sp != nil {
				srcProbs = append(srcProbs, sourceCheck{app.Slug, sp.Msg})
			} else if parsed.Kind == fleet.SourceLocal {
				localSources[app.Slug] = parsed.LocalPath
			}
		}
	}
	localBundles, bundleProbs := resolveLocalFleetBundleSpecs(m, file, localSources)

	if len(probs) > 0 || len(srcProbs) > 0 || len(bundleProbs) > 0 {
		fmt.Fprintf(errOut, "shinyhub fleet %s: validating %s\n\n", cmdName, file)
		for _, p := range probs {
			fmt.Fprintf(errOut, "  %s %s\n", s.failMark(), p.Error())
		}
		for _, sc := range srcProbs {
			fmt.Fprintf(errOut, "  %s %s  app %q: %s\n", s.failMark(), file, sc.slug, sc.msg)
		}
		for _, problem := range bundleProbs {
			fmt.Fprintf(errOut, "  %s %s  %s\n", s.failMark(), file, problem)
		}
		total := len(probs) + len(srcProbs) + len(bundleProbs)
		fmt.Fprintf(errOut, "\n%d problem(s) found. Nothing was changed. Fix these and re-run.\n", total)
		return nil, &ExitCodeError{Code: 1, Err: fmt.Errorf("%d manifest problem(s)", total), Reported: true}
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(errOut, "  %s not authenticated: %v\n     run 'shinyhub connect <url>' or pass --config\n", s.failMark(), err)
		return nil, &ExitCodeError{Code: 3, Err: err, Reported: true}
	}
	if waitFor > 0 {
		if _, werr := waitForServerReady(cfg, waitFor, serverPollInterval, errOut, time.Now, time.Sleep); werr != nil {
			fmt.Fprintf(errOut, "  %s %v\n", s.failMark(), werr)
			return nil, &ExitCodeError{Code: 6, Err: werr, Reported: true}
		}
	}
	apps, err := fetchApps(cfg)
	if err != nil {
		// Distinguish "the shinyhub server isn't up yet" (a front proxy on a
		// half-provisioned box answered) from a real transport/auth failure, so
		// the operator is not sent chasing a credential problem that isn't there.
		if nr := serverReadinessProblem(cfg); nr != nil {
			fmt.Fprintf(errOut, "  %s %v\n     the shinyhub server is not up yet (a front proxy answered instead).\n"+
				"     retry, or pass --wait-for-server=<duration> to block until it is ready.\n", s.failMark(), nr)
			return nil, &ExitCodeError{Code: 6, Err: nr, Reported: true}
		}
		return nil, reportAppsFetchError(cfg, errOut, err)
	}
	caps := fetchServerCaps(cfg)

	localDigests := map[string]string{}
	bundles := localBundles
	var resolveProblems []string
	for i := range m.Apps {
		app := &m.Apps[i]
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
			bundles[app.Slug] = bundleBuildSpec{Dir: dir}
		}
		spec, ok := bundles[app.Slug]
		if !ok {
			spec = bundleBuildSpec{Dir: dir}
			bundles[app.Slug] = spec
		}
		dg, derr := digestBundleSpec(spec)
		if derr != nil {
			resolveProblems = append(resolveProblems, fmt.Sprintf("app %q: %v", app.Slug, derr))
			continue
		}
		localDigests[app.Slug] = dg
		bm, merr := deploy.LoadManifest(dir)
		if merr != nil {
			resolveProblems = append(resolveProblems, fmt.Sprintf("app %q: %v", app.Slug, merr))
			continue
		}
		if bm != nil {
			app.Bundle = bundleFleetConfig(bm.App)
		}
	}
	if len(resolveProblems) > 0 {
		fmt.Fprintf(errOut, "shinyhub fleet %s: resolving sources\n\n", cmdName)
		for _, p := range resolveProblems {
			fmt.Fprintf(errOut, "  %s %s\n", s.failMark(), p)
		}
		fmt.Fprintf(errOut, "\n%d source problem(s). Nothing was changed.\n", len(resolveProblems))
		runCleanups()
		return nil, &ExitCodeError{Code: 1, Err: fmt.Errorf("%d source problem(s)", len(resolveProblems)), Reported: true}
	}

	observed := make([]fleet.ObservedApp, 0, len(apps))
	observedBySlug := make(map[string]fleet.ObservedApp, len(apps))
	for _, a := range apps {
		oa := fleet.ObservedApp{
			Slug: a.Slug,
			// Taken by address so a stored empty description reads as the real
			// value "" rather than "not observed"; the API omits the key when
			// empty, which decodes to the same "".
			Name:                         &a.Name,
			Description:                  &a.Description,
			Icon:                         &a.IconEmoji,
			ProjectSlug:                  &a.ProjectSlug,
			Access:                       a.Access,
			HibernateTimeoutMinutes:      a.HibernateTimeoutMinutes,
			Replicas:                     intPtrIfPositive(a.Replicas),
			MaxSessionsPerReplica:        intPtr(a.MaxSessionsPerReplica),
			RenderSeconds:                floatPtr(a.RenderSeconds),
			IdentityHeaders:              a.IdentityHeaders,
			UsageIdentityMode:            a.UsageIdentityMode,
			MinWarmReplicas:              intPtr(a.MinWarmReplicas),
			MemoryLimitMB:                a.MemoryLimitMB,
			CPUQuotaPercent:              a.CPUQuotaPercent,
			WorkerIsolation:              stringPtr(a.WorkerIsolation),
			WorkerGroupedSize:            intPtr(a.WorkerGroupedSize),
			WorkerMaxWorkers:             intPtr(a.WorkerMaxWorkers),
			WorkerWarmSpares:             intPtr(a.WorkerWarmSpares),
			WorkerMaxSessionLifetimeSecs: intPtr(a.WorkerMaxSessionLifetimeSecs),
			ContentDigest:                a.ContentDigest,
			ManagedBy:                    a.ManagedBy,
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

	var projectDiff []fleet.ProjectDiff
	if len(m.Projects) > 0 {
		projects, perr := fetchProjects(cfg)
		if perr != nil {
			fmt.Fprintf(errOut, "  %s %v\n", s.failMark(), perr)
			runCleanups()
			return nil, &ExitCodeError{Code: 3, Err: perr, Reported: true}
		}
		observedProjects := make([]fleet.ObservedProject, 0, len(projects))
		for _, p := range projects {
			// Taken by address for the same reason the app fields are: a stored
			// empty name reads as the real value "" rather than "not observed".
			observedProjects = append(observedProjects, fleet.ObservedProject{
				Slug: p.Slug, Name: &p.Name, Description: &p.Description, IconEmoji: &p.IconEmoji,
			})
		}
		projectDiff = fleet.DiffProjects(m, observedProjects)
	}

	return &preflightResult{
		manifest: m, caps: caps, host: cfg.Host, diff: diff, projectDiff: projectDiff,
		bundles: bundles, observed: observedBySlug, cleanup: runCleanups,
	}, nil
}

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }
func stringPtr(v string) *string  { return &v }

// bundleFleetConfig maps the durable, PATCHable subset of shinyhub.toml [app]
// into the fleet differ's desired config. Command/timeouts are boot-time-only
// and intentionally absent; every field here has a stored API counterpart.
func bundleFleetConfig(a deploy.AppSettings) fleet.Config {
	c := fleet.Config{
		Name: a.Name, Description: a.Description, Project: a.Project, Icon: a.Icon,
		HibernateTimeoutMinutes: a.HibernateTimeoutMinutes,
		HibernateResetToDefault: a.HibernateResetToDefault,
		Replicas:                a.Replicas, MaxSessionsPerReplica: a.MaxSessionsPerReplica,
		RenderSeconds: a.RenderSeconds, IdentityHeaders: a.IdentityHeaders,
		UsageIdentityMode: a.UsageIdentityMode,
		MinWarmReplicas:   a.MinWarmReplicas, MemoryLimitMB: a.MemoryLimitMB,
		CPUQuotaPercent: a.CPUQuotaPercent,
	}
	if a.Autoscale != nil {
		c.Autoscale = &fleet.AutoscaleConfig{
			Enabled: a.Autoscale.Enabled, MinReplicas: a.Autoscale.MinReplicas,
			MaxReplicas: a.Autoscale.MaxReplicas, Target: a.Autoscale.Target,
		}
	}
	if a.Worker != nil {
		c.WorkerIsolation = a.Worker.Isolation
		c.WorkerGroupedSize = a.Worker.GroupedSize
		c.WorkerMaxWorkers = a.Worker.MaxWorkers
		c.WorkerWarmSpares = a.Worker.WarmSpares
		c.WorkerMaxSessionLifetimeSecs = a.Worker.MaxSessionLifetimeSecs
	}
	return c
}
