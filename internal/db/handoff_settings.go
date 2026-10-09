package db

import (
	"encoding/json"
	"fmt"
	"reflect"
)

type handoffSettings struct {
	Before          App
	After           App
	BeforeSchedules []*Schedule
	AfterSchedules  []*Schedule
}

// StageHandoffSettings binds configuration to the pending generation. Promotion
// and compensation publish this snapshot in the same transaction as authority.
func (s *Store) StageHandoffSettings(id int64, before, after *App, prior, desired []*Schedule) error {
	payload, err := json.Marshal(handoffSettings{handoffAppSnapshot(before), handoffAppSnapshot(after), prior, desired})
	if err != nil {
		return err
	}
	res, err := s.db.Exec("UPDATE deployments SET handoff_settings_json=? WHERE id=? AND app_id=? AND status='pending'", string(payload), id, before.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("stage handoff: deployment is not pending")
	}
	return nil
}

func publishHandoffSettingsTx(tx writeTx, id int64, revert bool) error {
	var payload string
	if err := tx.QueryRow("SELECT handoff_settings_json FROM deployments WHERE id=?", id).Scan(&payload); err != nil {
		return err
	}
	if payload == "" {
		return nil
	}
	var snapshot handoffSettings
	if err := json.Unmarshal([]byte(payload), &snapshot); err != nil {
		return err
	}
	app, schedules := snapshot.After, snapshot.AfterSchedules
	if revert {
		app, schedules = snapshot.Before, snapshot.BeforeSchedules
	}
	// Publish only changed effective values, preserving concurrent changes to
	// omitted metadata (for example an uploaded icon during candidate boot).
	columns := []struct {
		name          string
		before, after any
	}{
		{"name", snapshot.Before.Name, snapshot.After.Name}, {"description", snapshot.Before.Description, snapshot.After.Description},
		{"icon_emoji", snapshot.Before.IconEmoji, snapshot.After.IconEmoji}, {"project_slug", snapshot.Before.ProjectSlug, snapshot.After.ProjectSlug},
		{"hibernate_timeout_minutes", snapshot.Before.HibernateTimeoutMinutes, snapshot.After.HibernateTimeoutMinutes},
		{"max_sessions_per_replica", snapshot.Before.MaxSessionsPerReplica, snapshot.After.MaxSessionsPerReplica},
		{"min_warm_replicas", snapshot.Before.MinWarmReplicas, snapshot.After.MinWarmReplicas},
		{"memory_limit_mb", snapshot.Before.MemoryLimitMB, snapshot.After.MemoryLimitMB}, {"cpu_quota_percent", snapshot.Before.CPUQuotaPercent, snapshot.After.CPUQuotaPercent},
		{"render_seconds", snapshot.Before.RenderSeconds, snapshot.After.RenderSeconds},
		{"autoscale_enabled", boolToInt(snapshot.Before.AutoscaleEnabled), boolToInt(snapshot.After.AutoscaleEnabled)},
		{"autoscale_min_replicas", snapshot.Before.AutoscaleMinReplicas, snapshot.After.AutoscaleMinReplicas},
		{"autoscale_max_replicas", snapshot.Before.AutoscaleMaxReplicas, snapshot.After.AutoscaleMaxReplicas},
		{"autoscale_target", snapshot.Before.AutoscaleTarget, snapshot.After.AutoscaleTarget},
	}
	for _, col := range columns {
		if reflect.DeepEqual(col.before, col.after) {
			continue
		}
		value := col.after
		query := "UPDATE apps SET " + col.name + "=?, updated_at=CURRENT_TIMESTAMP WHERE id=?"
		args := []any{value, app.ID}
		if revert {
			value = col.before
			args = []any{value, app.ID}
			if reflect.ValueOf(col.after).Kind() == reflect.Ptr && reflect.ValueOf(col.after).IsNil() {
				query += " AND " + col.name + " IS NULL"
			} else {
				query += " AND " + col.name + "=?"
				args = append(args, col.after)
			}
		}
		if _, err := tx.Exec(query, args...); err != nil {
			return fmt.Errorf("publish handoff %s: %w", col.name, err)
		}
	}
	if app.ProjectSlug != "" {
		if _, err := upsertProject(tx, Project{Slug: app.ProjectSlug}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("UPDATE app_schedules SET enabled=0, deploy_trigger='never', updated_at=CURRENT_TIMESTAMP WHERE app_id=?", app.ID); err != nil {
		return err
	}
	// A reverted schedule never ran while staged; remove newly introduced
	// declarations only if no durable run or activation references them.
	if revert {
		prior := map[string]bool{}
		for _, sc := range snapshot.BeforeSchedules {
			prior[sc.Name] = true
		}
		for _, sc := range snapshot.AfterSchedules {
			if !prior[sc.Name] {
				if _, err := tx.Exec(`DELETE FROM app_schedules WHERE app_id=? AND name=? AND NOT EXISTS (SELECT 1 FROM schedule_runs WHERE schedule_id=app_schedules.id) AND NOT EXISTS (SELECT 1 FROM schedule_activations WHERE schedule_id=app_schedules.id)`, app.ID, sc.Name); err != nil {
					return err
				}
			}
		}
	}
	for _, schedule := range schedules {
		_, err := tx.Exec(`INSERT INTO app_schedules (app_id,name,cron_expr,command_json,inputs_json,enabled,timeout_seconds,overlap_policy,missed_policy,deploy_trigger,timezone,on_success,min_roll_interval_seconds,roll_fallback,max_defer_age_seconds)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(app_id,name) DO UPDATE SET
  cron_expr=excluded.cron_expr,command_json=excluded.command_json,inputs_json=excluded.inputs_json,enabled=excluded.enabled,timeout_seconds=excluded.timeout_seconds,
  overlap_policy=excluded.overlap_policy,missed_policy=excluded.missed_policy,deploy_trigger=excluded.deploy_trigger,timezone=excluded.timezone,on_success=excluded.on_success,
  min_roll_interval_seconds=excluded.min_roll_interval_seconds,roll_fallback=excluded.roll_fallback,max_defer_age_seconds=excluded.max_defer_age_seconds,updated_at=CURRENT_TIMESTAMP`,
			app.ID, schedule.Name, schedule.CronExpr, schedule.CommandJSON, schedule.InputsJSON, boolToInt(schedule.Enabled), schedule.TimeoutSeconds, schedule.OverlapPolicy, schedule.MissedPolicy,
			schedule.DeployTrigger, schedule.Timezone, schedule.OnSuccess, schedule.MinRollIntervalSeconds, schedule.RollFallback, schedule.MaxDeferAgeSeconds)
		if err != nil {
			return err
		}
	}
	if !revert {
		if _, err := tx.Exec("DELETE FROM deployment_schedule_snapshots WHERE deployment_id=?", id); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO deployment_schedule_snapshots (deployment_id,name,cron_expr,command_json,inputs_json,enabled,timeout_seconds,overlap_policy,missed_policy,deploy_trigger,timezone,on_success,min_roll_interval_seconds,roll_fallback,max_defer_age_seconds)
  SELECT ?,name,cron_expr,command_json,inputs_json,enabled,timeout_seconds,overlap_policy,missed_policy,deploy_trigger,timezone,on_success,min_roll_interval_seconds,roll_fallback,max_defer_age_seconds FROM app_schedules WHERE app_id=?`, id, app.ID); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE deployments SET schedule_snapshot_recorded=1 WHERE id=?", id)
		return err
	}
	return nil
}

// Persist only the fields required for cutover/compensation, excluding unrelated
// app metadata and runtime diagnostics from the durable settings snapshot.
func handoffAppSnapshot(a *App) App {
	return App{ID: a.ID, Name: a.Name, Description: a.Description, IconEmoji: a.IconEmoji, ProjectSlug: a.ProjectSlug,
		HibernateTimeoutMinutes: a.HibernateTimeoutMinutes, MaxSessionsPerReplica: a.MaxSessionsPerReplica, MinWarmReplicas: a.MinWarmReplicas,
		MemoryLimitMB: a.MemoryLimitMB, CPUQuotaPercent: a.CPUQuotaPercent, RenderSeconds: a.RenderSeconds,
		AutoscaleEnabled: a.AutoscaleEnabled, AutoscaleMinReplicas: a.AutoscaleMinReplicas, AutoscaleMaxReplicas: a.AutoscaleMaxReplicas, AutoscaleTarget: a.AutoscaleTarget}
}
