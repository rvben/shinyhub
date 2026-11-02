package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
)

// ScheduleRunTransition is one schedule run's committed move from running to a
// terminal status. Exactly one writer observes each transition: the writer
// whose conditional update found the row still running. Everything that
// reports a run outcome (the audit event, the structured log line, the run
// counter) derives from this value, so an outcome is reported once and with
// the status that was actually stored.
//
// ScheduleName and AppSlug are snapshotted when the transition commits, so a
// record stays attributable after the schedule is renamed or deleted.
type ScheduleRunTransition struct {
	RunID             int64
	ScheduleID        int64
	ScheduleName      string
	AppID             int64
	AppSlug           string
	Status            string
	Trigger           string
	TriggeredByUserID *int64
	// ExitCode is nil when no process exit was observed (an interrupted run,
	// or one that failed before its command started).
	ExitCode *int
	// ActivationID and TargetGeneration are set when the transition enqueued
	// a serving-data activation.
	ActivationID     *int64
	TargetGeneration int64
}

// scheduleRunAuditDetail is the stable JSON shape of a schedule_run_<status>
// audit event. exit_code is always present: null means no exit was observed,
// which is a different fact from exit code 0.
type scheduleRunAuditDetail struct {
	App              string `json:"app"`
	Schedule         string `json:"schedule"`
	ScheduleID       int64  `json:"schedule_id"`
	RunID            int64  `json:"run_id"`
	Trigger          string `json:"trigger"`
	ExitCode         *int   `json:"exit_code"`
	ActivationID     *int64 `json:"activation_id,omitempty"`
	TargetGeneration int64  `json:"target_generation,omitempty"`
}

// AuditParams returns the schedule_run_<status> audit event for t.
func (t ScheduleRunTransition) AuditParams() (AuditEventParams, error) {
	detail, err := json.Marshal(scheduleRunAuditDetail{
		App:              t.AppSlug,
		Schedule:         t.ScheduleName,
		ScheduleID:       t.ScheduleID,
		RunID:            t.RunID,
		Trigger:          t.Trigger,
		ExitCode:         t.ExitCode,
		ActivationID:     t.ActivationID,
		TargetGeneration: t.TargetGeneration,
	})
	if err != nil {
		return AuditEventParams{}, fmt.Errorf("encode schedule run audit detail: %w", err)
	}
	return AuditEventParams{
		UserID:       t.TriggeredByUserID,
		Action:       "schedule_run_" + t.Status,
		ResourceType: "schedule",
		ResourceID:   strconv.FormatInt(t.ScheduleID, 10),
		Detail:       string(detail),
	}, nil
}

// insertScheduleRunAuditTx records t's audit event inside the transaction that
// commits the transition, so the run row and its audit event are durable
// together. The caller emits the matching audit_event log line after the
// commit. A failed insert aborts the transition, and is reported here the way
// every other audit write failure is (audit_log_write_failed plus the
// audit-error hook), so a broken audit table is alertable rather than visible
// only as runs that never leave the running state.
func (s *Store) insertScheduleRunAuditTx(ctx context.Context, tx writeTx, t ScheduleRunTransition) (AuditEventParams, error) {
	p, err := t.AuditParams()
	if err != nil {
		return AuditEventParams{}, err
	}
	if err := insertAuditEvent(func(q string, args ...any) (sql.Result, error) {
		return tx.ExecContext(ctx, q, args...)
	}, p); err != nil {
		s.reportAuditWriteFailure(p.Action, err)
		return AuditEventParams{}, fmt.Errorf("audit schedule run %d: %w", t.RunID, err)
	}
	return p, nil
}
