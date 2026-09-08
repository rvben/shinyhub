package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

// RefreshResult identifies the exact work admitted or joined by a recovery request.
type RefreshResult struct {
	ScheduleID int64  `json:"schedule_id"`
	Schedule   string `json:"schedule"`
	Status     string `json:"status"`
	RunID      int64  `json:"run_id,omitempty"`
}

type refreshAdmission struct {
	location *time.Location
	result   RefreshResult
}

// RefreshStale rechecks freshness inside the same admission fence used by cron
// and manual jobs. The request context bounds admission only; admitted jobs keep
// their independent lifetime and are never cancelled by a disconnected client.
func (m *Manager) RefreshStale(ctx context.Context, scheduleID int64, userID *int64, location *time.Location) (RefreshResult, error) {
	request := &refreshAdmission{location: location}
	id, err := m.runAdmitted(ctx, scheduleID, "manual", userID, request)
	request.result.RunID = id
	return request.result, err
}

func (m *Manager) checkRefresh(scheduleID int64, request *refreshAdmission) (bool, int64, error) {
	store, ok := m.store.(interface {
		ScheduleFreshnessByApp(int64) ([]db.ScheduleFreshness, error)
	})
	if !ok {
		return false, 0, errors.New("schedule freshness unavailable")
	}
	sc, err := m.store.GetSchedule(scheduleID)
	if err != nil {
		return false, 0, err
	}
	request.result.ScheduleID, request.result.Schedule = sc.ID, sc.Name
	if !sc.Enabled {
		request.result.Status = "disabled"
		return true, 0, nil
	}
	rows, err := store.ScheduleFreshnessByApp(sc.AppID)
	if err != nil {
		return false, 0, err
	}
	for _, row := range rows {
		if row.ScheduleID != scheduleID {
			continue
		}
		loc, err := row.EffectiveLocationChecked(request.location)
		if err != nil {
			return false, 0, err
		}
		stale, err := schedulespec.EvaluateStale(schedulespec.Freshness{
			Enabled: row.Enabled, CronExpr: row.CronExpr, CreatedAt: row.CreatedAt,
			LastSuccessAt: row.LastSuccessAt,
		}, loc, time.Now())
		if err != nil {
			return false, 0, err
		}
		if !row.Enabled {
			request.result.Status = "disabled"
			return true, 0, nil
		}
		if !stale {
			request.result.Status = "fresh"
			return true, 0, nil
		}
		if row.ActiveRunID != nil {
			request.result.Status = "joined"
			return true, *row.ActiveRunID, nil
		}
		if row.ProducerRepairRequired || (row.DeployTrigger != "" && row.DeployTrigger != schedulespec.DeployTriggerNever && !row.DeployTriggerSatisfied) {
			return false, 0, errors.New("schedule producer is not converged; use --wait-for-warm or explicit producer repair before refreshing stale data")
		}
		return false, 0, nil
	}
	return false, 0, fmt.Errorf("schedule %d freshness unavailable", scheduleID)
}

// Try-lock polling avoids orphaned lock-wait goroutines after request timeout.
func waitAdmission(ctx context.Context, try func() bool) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if try() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// runRefresh waits for residual slot ownership after a terminal row is written.
// Recovery must not manufacture a skipped-overlap row when an old run's deferred
// cleanup has not yet released its active or queue slot.
func (m *Manager) runRefresh(ctx context.Context, sched *db.Schedule, app *db.App, deployment *db.Deployment, userID *int64, gate *sync.RWMutex, request *refreshAdmission) (int64, error) {
	m.mu.Lock()
	slot := m.lockFor(sched.ID)
	m.mu.Unlock()
	if !slot.lock(ctx) {
		gate.RUnlock()
		return 0, ctx.Err()
	}
	if done, id, err := m.checkRefresh(sched.ID, request); done || err != nil {
		slot.unlock()
		gate.RUnlock()
		return id, err
	}
	if err := ctx.Err(); err != nil {
		slot.unlock()
		gate.RUnlock()
		return 0, err
	}
	request.result.Status = "started"
	return m.runWithOwnedSlot(sched, app, deployment, "manual", userID, gate, slot)
}
