package jobs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/jobs"
)

func (f *fakeStore) ScheduleFreshnessByApp(appID int64) ([]db.ScheduleFreshness, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sc := f.schedule
	row := db.ScheduleFreshness{ScheduleID: sc.ID, Name: sc.Name, Enabled: sc.Enabled, CronExpr: sc.CronExpr, CreatedAt: sc.CreatedAt, Timezone: sc.Timezone, DeployTrigger: sc.DeployTrigger, ProducerRepairRequired: f.repairRequired}
	for _, run := range f.runs {
		if run.Status == "running" && (row.ActiveRunID == nil || run.ID > *row.ActiveRunID) {
			id := run.ID
			row.ActiveRunID = &id
		}
		if run.Status == "succeeded" && run.FinishedAt != nil && (row.LastSuccessAt == nil || run.FinishedAt.After(*row.LastSuccessAt)) {
			finished := *run.FinishedAt
			row.LastSuccessAt = &finished
		}
	}
	return []db.ScheduleFreshness{row}, nil
}

func TestRefreshStaleConcurrentRequestsJoinExactRun(t *testing.T) {
	for _, policy := range []string{"skip", "queue", "concurrent"} {
		t.Run(policy, func(t *testing.T) {
			rt := &fakeRuntime{block: make(chan struct{})}
			st := newFakeStore(makeSchedule(policy, 30), makeApp())
			st.schedule.CreatedAt = time.Now().Add(-24 * time.Hour)
			m := newTestManager(t, rt, st)
			defer m.Stop(context.Background())
			var wg sync.WaitGroup
			results := make(chan jobs.RefreshResult, 8)
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, err := m.RefreshStale(context.Background(), 1, nil, time.UTC)
					if err != nil {
						t.Error(err)
						return
					}
					results <- result
				}()
			}
			wg.Wait()
			close(results)
			starts := 0
			for result := range results {
				if result.RunID != 1 {
					t.Errorf("wrong exact run: %+v", result)
				}
				if result.Status == "started" {
					starts++
				} else if result.Status != "joined" {
					t.Errorf("unexpected %+v", result)
				}
			}
			if starts != 1 {
				t.Fatalf("started %d runs", starts)
			}
			st.mu.Lock()
			count := len(st.runs)
			st.mu.Unlock()
			if count != 1 {
				t.Fatalf("recorded %d runs", count)
			}
		})
	}
}

func TestRefreshStaleJoinsCronAdmission(t *testing.T) {
	rt := &fakeRuntime{block: make(chan struct{})}
	st := newFakeStore(makeSchedule("concurrent", 30), makeApp())
	m := newTestManager(t, rt, st)
	defer m.Stop(context.Background())
	id, err := m.Run(1, "schedule", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.RefreshStale(context.Background(), 1, nil, time.UTC)
	if err != nil || result.Status != "joined" || result.RunID != id {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRefreshStaleCancelledProducerFenceDoesNotDispatchLater(t *testing.T) {
	st := newFakeStore(makeSchedule("skip", 30), makeApp())
	m := newTestManager(t, &fakeRuntime{}, st)
	defer m.Stop(context.Background())
	release := m.AcquireProducerGates([]int64{1})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := m.RefreshStale(ctx, 1, nil, time.UTC)
	release()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.runs) != 0 {
		t.Fatalf("cancelled admission created runs: %+v", st.runs)
	}
}

func TestRefreshStaleDoesNotDispatchFreshDisabledOrUnknown(t *testing.T) {
	for _, state := range []string{"fresh", "disabled", "unknown"} {
		t.Run(state, func(t *testing.T) {
			st := newFakeStore(makeSchedule("skip", 30), makeApp())
			switch state {
			case "fresh":
				st.schedule.CreatedAt = time.Now()
			case "disabled":
				st.schedule.Enabled = false
			case "unknown":
				st.schedule.CronExpr = "invalid"
			}
			m := newTestManager(t, &fakeRuntime{}, st)
			defer m.Stop(context.Background())
			result, err := m.RefreshStale(context.Background(), 1, nil, time.UTC)
			if state == "unknown" {
				if err == nil {
					t.Fatal("invalid cron accepted")
				}
			} else if err != nil || result.Status != state {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			st.mu.Lock()
			defer st.mu.Unlock()
			if len(st.runs) != 0 {
				t.Fatalf("unexpected runs: %+v", st.runs)
			}
		})
	}
}

func TestRefreshStaleDoesNotRetryUnconvergedProducer(t *testing.T) {
	for _, repair := range []bool{false, true} {
		st := newFakeStore(makeSchedule("skip", 30), makeApp())
		if repair {
			st.repairRequired = true
		} else {
			st.schedule.DeployTrigger = "bundle_change"
		}
		m := newTestManager(t, &fakeRuntime{}, st)
		_, err := m.RefreshStale(context.Background(), 1, nil, time.UTC)
		m.Stop(context.Background())
		if err == nil {
			t.Fatal("unconverged producer refresh accepted")
		}
		st.mu.Lock()
		count := len(st.runs)
		st.mu.Unlock()
		if count != 0 {
			t.Fatalf("unexpected producer retry: %d runs", count)
		}
	}
}

func TestRefreshStaleJoinsExistingRepairWithoutRetry(t *testing.T) {
	st := newFakeStore(makeSchedule("concurrent", 30), makeApp())
	st.schedule.DeployTrigger = "bundle_change"
	st.repairRequired = true
	st.runs[42] = &db.ScheduleRun{ID: 42, ScheduleID: 1, Status: "running"}
	m := newTestManager(t, &fakeRuntime{}, st)
	defer m.Stop(context.Background())
	result, err := m.RefreshStale(context.Background(), 1, nil, time.UTC)
	if err != nil || result.Status != "joined" || result.RunID != 42 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.runs) != 1 {
		t.Fatalf("unexpected producer retry: %d runs", len(st.runs))
	}
}
