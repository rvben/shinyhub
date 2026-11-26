package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/spanerr"
)

// ProducerEnvironmentCheck is the outcome of a candidate producer's
// environment check that does not fail the deploy.
type ProducerEnvironmentCheck struct {
	// Schedule is the producer's name.
	Schedule string
	// Checked reports that the producer's uv environment resolved and
	// installed. False with an empty NotChecked means the producer's command
	// does not launch uv, so there was nothing to check.
	Checked bool
	// NotChecked says why a uv environment could not be checked. Such a
	// producer must be reported as unchecked, never as passed.
	NotChecked string
}

// ProducerEnvironmentError is a candidate producer whose environment cannot
// be built: running it would fail before its command starts, and a failed
// producer run quarantines the app. It fails the deploy instead.
type ProducerEnvironmentError struct {
	Schedule string
	// Output is uv's own diagnostic, ANSI-stripped, bounded and redacted.
	Output string
	kind   deployfail.Kind
	err    error
}

func (e *ProducerEnvironmentError) Error() string {
	msg := fmt.Sprintf("schedule %q cannot build its environment: %v", e.Schedule, e.err)
	if e.Output != "" {
		msg += "\n" + e.Output
	}
	return msg
}

func (e *ProducerEnvironmentError) Unwrap() error { return e.err }

// DeployFailureKind classifies the failure for deployfail.Classify.
func (e *ProducerEnvironmentError) DeployFailureKind() string { return string(e.kind) }

// producerCheckBudget, when set by a test, replaces the bundle's build budget
// for the check, whose manifest minimum is too long to wait for in a test.
var producerCheckBudget func(time.Duration) time.Duration

// producerCheckOutputLimit bounds the uv output kept for the error: the tail,
// where uv states the resolution failure.
const producerCheckOutputLimit = 8 << 10

// CheckCandidateProducerEnvironment builds the uv environment a candidate
// producer of deployment runs in, without running the producer, so a deploy
// can fail a producer that would fail before its command starts while the
// previous release still serves: once producers run, any failure counts as a
// possible partial data write and quarantines the app.
//
// It runs once the deployment's bundle is prepared, in that bundle, with the
// producer's launch planned and instrumented exactly as a run's (planLaunch,
// instrumentCommand), and its `uv run` command replaced by one that runs a
// no-op in the same environment (process.UVRunEnvironmentProbe). Like a run,
// it tries that offline from uv's cache first and goes through the package
// index only when the cache cannot serve it. The environment it builds, and
// the uv cache it fills, are therefore the ones the producer's runs use,
// which then need nothing from the package index (see buildRunEnvironment). It gets no app data dir, shared mounts, result
// cache or publication locks: it starts no app code. It holds a slot of the
// host's build gate and runs under the bundle's build budget.
//
// The returned error is a *ProducerEnvironmentError when the environment
// cannot be built, and another error when the check itself could not run.
// A uv launch the probe cannot reproduce, and a check that ran out of time,
// come back as NotChecked rather than as an error.
func (m *Manager) CheckCandidateProducerEnvironment(ctx context.Context, sched *db.Schedule, app *db.App, deployment *db.Deployment) (ProducerEnvironmentCheck, error) {
	result := ProducerEnvironmentCheck{Schedule: sched.Name}
	if sched.AppID != app.ID || deployment.AppID != app.ID {
		return result, errors.New("candidate producer app identity mismatch")
	}
	plan, err := m.planLaunch(sched, app, deployment)
	if err != nil {
		return result, err
	}
	if setupErr := producerTierError(plan.rt, plan.tier); setupErr != nil {
		return result, setupErr
	}
	cmd, setupErr := m.instrumentCommand(deployment.BundleDir, plan.cmd, plan.tier)
	if setupErr != nil {
		return result, setupErr
	}
	// Plan with the server env vars a launch inherits, which the app's own
	// override.
	probe, ok := process.UVRunEnvironmentProbe(cmd, slices.Concat(process.SanitizedEnv(), plan.env, plan.secretEnv), deployment.BundleDir)
	if !ok {
		return result, nil
	}
	if probe.NotChecked != "" {
		result.NotChecked = probe.NotChecked
		return result, nil
	}
	budget, err := deploy.BundleBuildTimeout(deployment.BundleDir)
	if err != nil {
		return result, fmt.Errorf("read manifest: %w", err)
	}
	if producerCheckBudget != nil {
		budget = producerCheckBudget(budget)
	}

	release, err := deploy.AcquireBuildSlot(ctx)
	if err != nil {
		return result, err
	}
	defer release()

	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	params := process.StartParams{
		Slug:          app.Slug,
		AppID:         app.ID,
		Dir:           deployment.BundleDir,
		Env:           plan.env,
		SecretEnv:     plan.secretEnv,
		Tier:          plan.tier,
		ContentDigest: deployment.ContentDigest,
		AppVersion:    deployment.Version,
		DeploymentID:  deployment.ID,
		JobSchedule:   sched.Name,
		// The bundle's data link and shared mounts stay as they are.
		EnvironmentOnly: true,
	}
	// As a run does, try the environment uv's cache serves first: one it
	// serves offline is one the producer's runs can build, even while the
	// package index is unreachable.
	var info process.ExitInfo
	var runErr error
	if probe.OfflineArgv != nil {
		params.Command = probe.OfflineArgv
		info, runErr = plan.rt.RunOnce(runCtx, params, io.Discard)
	}
	out := &tailBuffer{limit: producerCheckOutputLimit}
	if probe.OfflineArgv == nil || runCtx.Err() == nil && (runErr != nil || info.Signaled || info.Code != 0) {
		params.Command = probe.Argv
		info, runErr = plan.rt.RunOnce(runCtx, params, out)
	}
	switch {
	case ctx.Err() != nil:
		return result, ctx.Err()
	case runCtx.Err() == context.DeadlineExceeded:
		result.NotChecked = fmt.Sprintf("resolving the environment did not finish within the %s build budget", budget)
		return result, nil
	case runErr == nil && !info.Signaled && info.Code == 0:
		result.Checked = true
		return result, nil
	}
	cause := runErr
	if cause == nil && info.Signaled {
		cause = errors.New("uv was killed by a signal")
	} else if cause == nil {
		cause = fmt.Errorf("uv exited with code %d", info.Code)
	}
	output := m.redactCheckOutput(deployment.BundleDir, plan, out.String())
	return result, &ProducerEnvironmentError{
		Schedule: sched.Name,
		Output:   output,
		kind:     producerFailureKind(cause.Error() + "\n" + output),
		err:      cause,
	}
}

// producerFailureKind classifies a failed environment check. A missing uv and
// an interpreter uv could not obtain have their own kinds, as in a deploy's
// build; anything else is the dependency build failing.
func producerFailureKind(msg string) deployfail.Kind {
	switch {
	case deployfail.MentionsMissingExecutable(msg, "uv"):
		return deployfail.RuntimeMissing
	case deployfail.MentionsInterpreterProvisioningFailure(msg):
		return deployfail.InterpreterUnavailable
	default:
		return deployfail.BuildFailed
	}
}

// redactCheckOutput prepares uv's output for a deploy response: ANSI escapes
// stripped, URL credentials and query values masked, and every value of a
// per-app secret or of a ${NAME} reference the requirements expanded
// replaced, since uv quotes index URLs and their paths verbatim.
func (m *Manager) redactCheckOutput(dir string, plan *launchPlan, out string) string {
	s := spanerr.RedactURLs(string(process.StripANSI([]byte(out))))
	if idx, err := process.ReadRequirementsIndex(dir, append(process.SanitizedEnv(), plan.env...)); err == nil {
		s = idx.Redact(s)
	}
	for _, kv := range plan.secretEnv {
		// A very short value would mask unrelated text without hiding
		// anything worth hiding.
		if _, v, ok := strings.Cut(kv, "="); ok && len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return strings.TrimSpace(s)
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
	cut   bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.cut = true
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.cut {
		return string(t.buf)
	}
	// Start at a line boundary so the first line is not a fragment.
	s := string(t.buf)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return "[...]\n" + s
}
