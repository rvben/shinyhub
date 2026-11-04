package jobs

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSharedMountPauseClosesPreAdmittedJobsAndReopensOnlyOnce(t *testing.T) {
	m := &Manager{publicationLockDir: t.TempDir(), active: map[int64]context.CancelFunc{}, activeApps: map[int64]int64{}, appActive: map[int64]int{}, appDrained: map[int64]chan struct{}{}}
	release, err := m.TryPauseAppRuns(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.commitAdmission(10, 1, func() {}, true); !errors.Is(err, ErrManagerStopped) {
		t.Fatalf("pre-admitted job crossed mutation: %v", err)
	}
	if err := m.commitAdmission(11, 2, func() {}, false); err != nil {
		t.Fatalf("unrelated app blocked: %v", err)
	}
	m.unregisterActive(11)
	m.wg.Done()
	release()
	release()
	if err := m.commitAdmission(12, 1, func() {}, false); err != nil {
		t.Fatalf("admissions not restored: %v", err)
	}
	if _, err := m.TryPauseAppRuns(1); !errors.Is(err, ErrAppRunsActive) {
		t.Fatalf("active job not rejected: %v", err)
	}
	m.unregisterActive(12)
	m.wg.Done()
	next, err := m.TryPauseAppRuns(1)
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestSharedMountPauseDetectsPhysicalJobsAcrossOwners(t *testing.T) {
	root := t.TempDir()
	old := &Manager{publicationLockDir: root}
	next := &Manager{publicationLockDir: root}
	releaseJob, _, err := old.acquireJobExecutionFileLockContext(context.Background(), 42, unix.LOCK_SH)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.TryPauseAppRuns(42); !errors.Is(err, ErrAppRunsActive) {
		t.Fatalf("surviving physical job overlooked: %v", err)
	}
	releaseJob()
	release, err := next.TryPauseAppRuns(42)
	if err != nil {
		t.Fatalf("failed pause leaked fence: %v", err)
	}
	release()
}
