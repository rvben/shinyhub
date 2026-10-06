package localrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"
)

// Control targets a single app. Restart replaces it only after readiness;
// Stop suspends source reloads; Resume starts the latest source again.
type Control string

const (
	Restart Control = "restart"
	Stop    Control = "stop"
	Resume  Control = "resume"
)

type developmentSource struct {
	dir, manifestRoot, slug, lockWarning string
	env                                  []string
}

type developmentCandidate struct {
	child     *childProcess
	err       error
	workspace *workspace
}

// One coordinator owns routing, workspaces, and control ordering. Preparation
// and readiness run asynchronously so stop can cancel even a pending launch.
// A cancelled operation is joined before its workspace is reused or unlocked.
func runDevelopmentLoop(ctx context.Context, o Options, w *workspace, lp *localProxy, changes chan struct{}, proxyErrors chan error,
	events *runEvents, stdout, stderr io.Writer, source developmentSource, seedPending bool, seed func(context.Context) error) error {
	currentWorkspace, stagingWorkspace := w, w.alternate()
	var current *childProcess
	var currentCancel, candidateCancel context.CancelFunc
	var results chan developmentCandidate
	controls := o.Controls
	var routeMu sync.Mutex
	provisional := false
	suspended, first, opened := false, true, false
	lp.unavailable.Store(true)
	lp.serve(proxyErrors)

	stop := func(child *childProcess, cancel context.CancelFunc) {
		if child != nil {
			stopChild(child.cmd, child.exitCh, stderr)
		}
		if cancel != nil {
			cancel()
		}
	}
	abort := func() {
		if results != nil {
			candidateCancel()
			result := <-results
			stop(result.child, candidateCancel)
			results, candidateCancel = nil, nil
			// A completed result may still be queued after provisional routing.
			// Restore the serving route before starting any replacement work.
			routeMu.Lock()
			if current != nil {
				_ = lp.routeTo(current.port)
			}
			lp.unavailable.Store(current == nil)
			provisional = false
			routeMu.Unlock()
		}
	}
	defer func() {
		abort()
		stop(current, currentCancel)
	}()

	launch := func(activity string) {
		if !first {
			events.attempt.Add(1)
		}
		first = false
		events.phase(activity, map[string]string{"reloading": "Preparing latest change", "restarting": "Restarting app", "resuming": "Resuming latest source", "preparing": "Preparing workspace"}[activity])
		candidateCtx, cancel := context.WithCancel(ctx)
		candidateCancel = cancel
		resultCh := make(chan developmentCandidate, 1)
		results = resultCh
		workspace := stagingWorkspace
		if current == nil {
			workspace = currentWorkspace
		}
		previous := current
		go func() {
			result := developmentCandidate{workspace: workspace}
			defer func() { resultCh <- result }()
			inputs, err := resolveInputSnapshots(source.manifestRoot, source.dir, o.BundleInputs)
			if err != nil {
				result.err = fmt.Errorf("resolve bundle inputs: %w", err)
				return
			}
			if o.CheckBundle != nil {
				if warning := bundleLockWarning(o.CheckBundle(source.dir, inputs)); warning != source.lockWarning {
					fmt.Fprint(stderr, warning)
					source.lockWarning = warning
				}
			}
			depsChanged, err := workspace.syncSourceWithInputs(source.dir, inputs)
			if err == nil && depsChanged && !o.NoSync {
				err = workspace.markDependenciesDirty()
			}
			if err == nil && seedPending {
				err = seed(candidateCtx)
				if err == nil {
					err = syscall.Flock(int(w.dataLock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
				}
				if err == nil {
					seedPending, depsChanged = false, false
				}
			}
			if err == nil {
				result.child, err = startCandidate(candidateCtx, workspace, source.slug, source.env, o.NoSync, depsChanged, stdout, stderr, events)
			}
			if err == nil {
				err = waitUntilReady(candidateCtx, result.child, nil)
			}
			if err == nil {
				routeMu.Lock()
				err = lp.routeTo(result.child.port)
				if err == nil {
					provisional = true
					lp.unavailable.Store(false)
				}
				routeMu.Unlock()
			}
			if err == nil {
				err = pollReady(candidateCtx, joinReadyURL(lp.URL(), result.child.plan.ReadyPath), 5*time.Second, result.child.plan.ReadyStatus)
				if err != nil {
					routeMu.Lock()
					if previous != nil {
						_ = lp.routeTo(previous.port)
					} else {
						lp.unavailable.Store(true)
					}
					provisional = false
					routeMu.Unlock()
				}
			}
			result.err = err
		}()
	}
	launch("preparing")
	for {
		var exited <-chan error
		if current != nil {
			exited = current.exitCh
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-proxyErrors:
			return err
		case control, ok := <-controls:
			if !ok {
				controls = nil
				continue
			}
			switch control {
			case Stop:
				events.phase("stopping", "Stopping app and suspending automatic reloads")
				abort()
				lp.unavailable.Store(true)
				stop(current, currentCancel)
				current, currentCancel, suspended = nil, nil, true
				events.phase("stopped", "App stopped; automatic reloads suspended")
			case Resume:
				if suspended {
					suspended = false
					launch("resuming")
				}
			case Restart:
				if !suspended {
					abort()
					launch("restarting")
				}
			}
		case <-changes:
			if suspended {
				continue
			}
			if results != nil {
				abort()
				events.phase("superseded", "A newer save replaced the pending candidate")
			}
			launch("reloading")
		case result := <-results:
			cancel := candidateCancel
			results, candidateCancel = nil, nil
			routeMu.Lock()
			provisional = false
			routeMu.Unlock()
			if result.err != nil {
				stop(result.child, cancel)
				lp.unavailable.Store(current == nil)
				if ctx.Err() != nil {
					return nil
				}
				events.phase("failed", result.err.Error())
				if current == nil && o.Controls == nil {
					return result.err
				}
				fmt.Fprintf(stderr, "Launch failed: %v\n", result.err)
				continue
			}
			old, oldCancel := current, currentCancel
			current, currentCancel = result.child, cancel
			if result.workspace != currentWorkspace {
				currentWorkspace, stagingWorkspace = stagingWorkspace, currentWorkspace
			}
			lp.unavailable.Store(false)
			lp.activateBrowserRevision()
			events.emit(Event{Type: "phase", Phase: "ready", Attempt: events.attempt.Load(), Generation: lp.revision.Load(), URL: lp.URL(), Message: "Watching for changes"})
			fmt.Fprintf(stdout, "Ready\n  App: %s\n", lp.URL())
			if o.Open && !opened {
				openBrowser(lp.URL())
			}
			opened = true
			stop(old, oldCancel)
		case exitErr := <-exited:
			if ctx.Err() != nil {
				return nil
			}
			err := errors.New("app exited unexpectedly")
			if exitErr != nil {
				err = fmt.Errorf("app exited: %w", exitErr)
			}
			stop(current, currentCancel)
			current, currentCancel = nil, nil
			routeMu.Lock()
			// A ready candidate owns the public probe while its result is
			// pending. An old process exiting cannot disable that probe.
			if !provisional {
				lp.unavailable.Store(true)
			}
			routeMu.Unlock()
			if o.Controls == nil {
				return err
			}
			events.phase("exited", err.Error())
		}
	}
}
