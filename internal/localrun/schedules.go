package localrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// ScheduleError preserves the job's exit status for the foreground CLI.
type ScheduleError struct {
	Name string
	Code int
	Err  error
}

func (e *ScheduleError) Error() string { return fmt.Sprintf("schedule %q: %v", e.Name, e.Err) }
func (e *ScheduleError) Unwrap() error { return e.Err }

func acquireDataLock(dir string, exclusive bool) (*os.File, error) {
	canonical, err := canonicalPath(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve local data lock identity: %w", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolve local data lock registry: %w", err)
	}
	registry := filepath.Join(cache, "shinyhub", "local-data-locks")
	if err := os.MkdirAll(registry, 0o700); err != nil {
		return nil, fmt.Errorf("create local data lock registry: %w", err)
	}
	// Producers may replace their entire data directory; its lock must live
	// outside that directory so replacement cannot create a second fence.
	digest := sha256.Sum256([]byte(canonical))
	f, err := os.OpenFile(filepath.Join(registry, fmt.Sprintf("%x.lock", digest)), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open local data lock: %w", err)
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("local app data is in use: stop local consumers and schedules before running a producer: %s", dir)
	}
	return f, nil
}

// scheduleSelection names the manifest schedules a run executes. The zero
// value selects the startup seed set: enabled deploy-trigger schedules.
type scheduleSelection struct {
	name string
	all  bool
}

func selectionFor(o Options) scheduleSelection {
	return scheduleSelection{name: o.ScheduleName, all: o.AllSchedules}
}

// explicit reports whether the run executes schedules on request rather than
// seeding app data before a development boot.
func (s scheduleSelection) explicit() bool { return s.name != "" || s.all }

func selectSchedules(manifest *deploy.Manifest, sel scheduleSelection) ([]deploy.ScheduleSpec, error) {
	var selected []deploy.ScheduleSpec
	if manifest != nil {
		for _, schedule := range manifest.Schedules {
			switch {
			case sel.name != "":
				if schedule.Name == sel.name {
					selected = append(selected, schedule)
				}
			case sel.all:
				if !schedule.Disabled {
					selected = append(selected, schedule)
				}
			case !schedule.Disabled && schedule.DeployTrigger != "never":
				selected = append(selected, schedule)
			}
		}
	}
	if sel.name != "" && len(selected) == 0 {
		return nil, validationErrorf("schedule %q is not defined in shinyhub.toml", sel.name)
	}
	if sel.all && len(selected) == 0 {
		return nil, validationErrorf("no enabled schedules are defined in shinyhub.toml")
	}
	return selected, nil
}

func runSchedules(ctx context.Context, w *workspace, slug string, userEnv []string, plan *deploy.LaunchPlan, sel scheduleSelection, seedMode string, stdout, stderr io.Writer) error {
	selected, err := selectSchedules(plan.Manifest, sel)
	if err != nil {
		return err
	}
	if sel.all {
		for _, schedule := range plan.Manifest.Schedules {
			if schedule.Disabled {
				fmt.Fprintf(stderr, "note: schedule %q is disabled; skipped (name it to run it explicitly)\n", schedule.Name)
			}
		}
	}
	state, err := loadSeedState(w)
	if err != nil {
		return err
	}
	if state.InProgress {
		fmt.Fprintln(stdout, "==> previous local job failed or was interrupted; initialization records will be rebuilt")
		state.Records = map[string]seedRecord{}
		state.InProgress = false
		if err := saveSeedState(w, state); err != nil {
			return err
		}
	}
	for _, schedule := range selected {
		generation, err := dataGeneration(w.DataDir)
		if err != nil {
			return err
		}
		key, owner := seedIdentity(slug, schedule)
		if !sel.explicit() && seedMode == "missing" && state.Records[key].Generation == generation {
			fmt.Fprintf(stdout, "==> schedule %s: skipped; already initialized (use --seed=always to refresh)\n", schedule.Name)
			continue
		}
		if schedule.Disabled {
			fmt.Fprintf(stderr, "note: schedule %q is disabled; running explicitly\n", schedule.Name)
		}
		reason := "explicit refresh"
		if !sel.explicit() {
			reason = "forced startup refresh"
			if seedMode == "missing" {
				reason = "initialization missing"
			}
		}
		fmt.Fprintf(stdout, "==> schedule %s: running; %s\n", schedule.Name, reason)
		env := append(process.SanitizedEnv(), userEnv...)
		env = append(env, process.RenvPolicyEnvFor(w.BundleDir)...)
		env = append(env, "SHINYHUB_APP_DATA="+w.DataDir, "SHINYHUB_APP_SLUG="+slug, "SHINYHUB_RUN_MODE=local")
		command, indexEnv, err := process.RequirementsLaunch(w.BundleDir, schedule.Command, env)
		if err != nil {
			return fmt.Errorf("schedule %q launch: %w", schedule.Name, err)
		}
		cmd := exec.Command(command[0], command[1:]...) //nolint:gosec
		cmd.Dir = w.BundleDir
		if w.dataLock != nil {
			cmd.ExtraFiles = []*os.File{w.dataLock, w.workspaceLock}
		}
		cmd.Env = append(env, indexEnv...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// A detached descendant must not hold output pipes open indefinitely.
		cmd.WaitDelay = time.Second
		cmd.Stdout = &scheduleWriter{out: stdout, prefix: "[" + schedule.Name + "] ", lineStart: true}
		cmd.Stderr = &scheduleWriter{out: stderr, prefix: "[" + schedule.Name + "] ", lineStart: true}
		// Invalidate all old command versions of this writer before it can run.
		for existing, record := range state.Records {
			if record.Owner == owner {
				delete(state.Records, existing)
			}
		}
		state.InProgress = true
		if err := saveSeedState(w, state); err != nil {
			return err
		}
		jobCtx, cancel := context.WithTimeout(ctx, time.Duration(*schedule.TimeoutSeconds)*time.Second)
		err = executeSchedule(jobCtx, cmd)
		cancel()
		if errors.Is(err, exec.ErrWaitDelay) {
			err = fmt.Errorf("schedule leader exited but background processes kept output open; background work was terminated, run producer steps in the foreground: %w", err)
		}
		if err != nil {
			code := exitCode(err)
			if code < 1 {
				code = 1
			}
			if jobCtx.Err() == context.DeadlineExceeded {
				code = 124
			}
			if ctx.Err() != nil {
				code = 130
			}
			fmt.Fprintln(stderr, "Local jobs use .env, --env-file and --env; inherited variables must be named in SHINYHUB_APP_ENV_ALLOW.")
			return &ScheduleError{Name: schedule.Name, Code: code, Err: err}
		}
		// A producer may replace its data directory. Bind success to the new
		// generation and invalidate successes for generations it removed.
		generation, err = dataGeneration(w.DataDir)
		if err != nil {
			return err
		}
		state.Records[key] = seedRecord{Owner: owner, Generation: generation}
		state.InProgress = false
		if err := saveSeedState(w, state); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "==> schedule %s: initialized successfully\n", schedule.Name)
	}
	// Both startup seeding and an explicit batch must leave every selected
	// producer's output in the current data generation. A later producer can
	// replace the whole directory even though each command exits successfully.
	if sel.name == "" && len(selected) > 0 {
		generation, err := dataGeneration(w.DataDir)
		if err != nil {
			return err
		}
		for _, schedule := range selected {
			key, _ := seedIdentity(slug, schedule)
			if state.Records[key].Generation != generation {
				retry := "--seed=missing"
				if sel.all {
					retry = "--all-schedules"
				}
				return validationErrorf("schedule %q initialization was invalidated by a later producer replacing app data; keep producer writes scoped to their own output directories and rerun %s", schedule.Name, retry)
			}
		}
	}
	return nil
}

// executeSchedule owns cancellation so SIGTERM reaches the entire process
// group, followed by SIGKILL after the schedule's ten-second shutdown grace.
func executeSchedule(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := watchExit(cmd)
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // also stop surviving descendants
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}

// scheduleWriter labels lines while preserving streamed, partial-line output.
type scheduleWriter struct {
	out       io.Writer
	prefix    string
	lineStart bool
}

func (w *scheduleWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		if w.lineStart {
			if _, err := io.WriteString(w.out, w.prefix); err != nil {
				return total, err
			}
			w.lineStart = false
		}
		size := len(p)
		if at := bytes.IndexByte(p, '\n'); at >= 0 {
			size = at + 1
		}
		n, err := w.out.Write(p[:size])
		total += n
		if err != nil {
			return total, err
		}
		if n != size {
			return total, io.ErrShortWrite
		}
		w.lineStart = p[size-1] == '\n'
		p = p[size:]
	}
	return total, nil
}

// ValidateSchedule checks the selected jobs' composed manifest and explicit environment
// without preparing dependencies, creating state, or executing app code.
func ValidateSchedule(o Options) error {
	if _, err := validateUserEnv(o.Env); err != nil {
		return &ValidationError{Err: err}
	}
	manifestRoot := ""
	if o.ManifestPath != "" {
		var err error
		manifestRoot, err = filepath.EvalSymlinks(filepath.Dir(o.ManifestPath))
		if err != nil {
			return &ValidationError{Err: err}
		}
	}
	snapshots, err := resolveInputSnapshots(manifestRoot, o.BundleDir, o.BundleInputs)
	if err != nil {
		return &ValidationError{Err: err}
	}
	manifest, err := deploy.LoadManifest(o.BundleDir)
	if err != nil {
		return &ValidationError{Err: err}
	}
	for _, input := range snapshots {
		if input.To == deploy.ManifestFilename {
			manifest, err = deploy.ParseManifest(input.Data)
			if err != nil {
				return &ValidationError{Err: err}
			}
		}
	}
	_, err = selectSchedules(manifest, selectionFor(o))
	return err
}
