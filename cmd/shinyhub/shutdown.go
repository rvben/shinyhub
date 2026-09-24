package main

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"time"
)

// shutdownDrainBudget bounds the work ownerWork's shutdown branch does before
// releasing ownership: draining scheduled jobs and acquiring the fleet
// mutation fence share this single deadline, so together they can never hang
// the ownership handoff indefinitely.
const shutdownDrainBudget = 20 * time.Second

// shutdownWatchdogBudget bounds stopOwnership itself (elector shutdown, the
// ownerWork goroutine returning, and scope.Stop()). It is larger than
// shutdownDrainBudget so the drain budget above normally fires first and logs
// a more specific cause; this is the last-resort backstop for anything that
// hangs outside that inner budget (for example loops.Wait() itself).
const shutdownWatchdogBudget = 30 * time.Second

// osExit is os.Exit behind a variable so a test can intercept the "give up
// and exit non-zero" path without terminating the test process.
var osExit = os.Exit

// awaitShutdown runs stop and reports whether it returned within budget. It
// never blocks the caller past budget: on timeout it returns false
// immediately while stop keeps running in its own goroutine (a hung stop
// leaks that goroutine, which is expected - the process is about to exit).
func awaitShutdown(budget time.Duration, stop func(), log *slog.Logger) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		stop()
	}()
	select {
	case <-done:
		return true
	case <-time.After(budget):
		log.Error("shutdown step did not finish within budget", "budget", budget)
		return false
	}
}

// awaitOwnershipStop runs stopOwnership with a bounded watchdog. Graceful
// shutdown ordinarily returns well within budget; if stopOwnership hangs
// (loops.Wait(), an unbounded drain, or the old unbounded fence-retry loop),
// letting the caller proceed would race runServe's deferred store.Close()
// against a goroutine that may still be using the store. Exiting the process
// instead is safe: systemd (or any supervisor) restarts a clean process
// rather than the caller silently closing the store underneath a live
// goroutine. osExit does not return, so this function returns only on the
// success path.
func awaitOwnershipStop(budget time.Duration, stopOwnership func(), log *slog.Logger) {
	if awaitShutdown(budget, stopOwnership, log) {
		return
	}
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	log.Error("ownership release hung past shutdown watchdog budget; exiting rather than risk closing the store under a live goroutine",
		"budget", budget, "goroutine_dump", string(buf[:n]))
	osExit(1)
}

// acquireFenceUntilDeadline retries acquire (srv.AcquireFleetAppOperations)
// until it succeeds or drainCtx is done. Unlike the ordinary
// refreshUntilReady retry (which runs for the life of the process against a
// context that is cancelled only on shutdown), this loop runs DURING
// shutdown itself and previously had no deadline at all, so a fence that
// never became available could hang the ownership handoff forever. It now
// gives up once drainCtx expires and reports ok=false.
func acquireFenceUntilDeadline(drainCtx context.Context, acquire func() (func(), error), backoff time.Duration, log *slog.Logger) (func(), bool) {
	for {
		release, err := acquire()
		if err == nil {
			return release, true
		}
		log.Error("acquire ownership handoff fence", "err", err)
		select {
		case <-drainCtx.Done():
			log.Error("giving up on ownership handoff fence: shutdown drain budget exceeded")
			return nil, false
		case <-time.After(backoff):
		}
	}
}

// drainOwnershipHandoff runs the retiring owner's handoff steps against one
// bounded drainCtx: drain scheduled jobs, then take and release the exclusive
// fleet fence so requests admitted before handoff have finished. It reports
// whether both steps completed. A drain that returns early at the deadline
// leaves its job goroutine running, and a fence that was never taken leaves
// admitted requests running, so on false the caller must not release the lease
// explicitly: a successor would otherwise reconcile rows those goroutines can
// still write.
func drainOwnershipHandoff(drainCtx context.Context, drain func(context.Context) error, acquire func() (func(), error), backoff time.Duration, log *slog.Logger) bool {
	complete := true
	if err := drain(drainCtx); err != nil {
		log.Error("drain scheduled jobs before ownership release", "err", err)
		complete = false
	}
	release, ok := acquireFenceUntilDeadline(drainCtx, acquire, backoff, log)
	if !ok {
		return false
	}
	release()
	return complete
}
