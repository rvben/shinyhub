package cli

import (
	"fmt"
	"strings"

	"github.com/rvben/shinyhub/internal/appstatus"
)

type healthScheduleObservation struct {
	Name              string `json:"name"`
	Satisfied         *bool  `json:"satisfied"`
	ConvergenceStatus string `json:"convergence_status"`
	RunID             *int64 `json:"convergence_run_id"`
	Error             string `json:"convergence_error"`
}

type healthActivationObservation struct {
	Schedule    string `json:"schedule_name"`
	RunID       *int64 `json:"schedule_run_id"`
	Status      string `json:"status"`
	Phase       string `json:"phase"`
	LastError   string `json:"last_error"`
	DeferReason string `json:"defer_reason"`
}

// Use current server observations, not historical exits or guesses about which
// boot step might be slow. Older servers simply keep the status-only fallback.
func healthWaitReason(o appHealthObservation) string {
	gate := ""
	if o.CompatibilityQuarantined {
		gate = "Consumer starts blocked: data compatibility is unverified"
	} else if o.ProducerRepairRequired {
		gate = "Producer data needs repair before consumers can start"
	}
	if gate != "" {
		for _, schedule := range o.Schedules {
			if schedule.Satisfied != nil && !*schedule.Satisfied && schedule.ConvergenceStatus == "running" && schedule.RunID != nil && *schedule.RunID > 0 {
				return fmt.Sprintf("Waiting for %s run #%d; %s", liveText(schedule.Name), *schedule.RunID, gate)
			}
		}
		return gate
	}
	for _, schedule := range o.Schedules {
		if schedule.Satisfied == nil || *schedule.Satisfied {
			continue
		}
		name := liveText(schedule.Name)
		if schedule.ConvergenceStatus == "running" && schedule.RunID != nil && *schedule.RunID > 0 {
			return fmt.Sprintf("Waiting for %s run #%d", name, *schedule.RunID)
		}
		if schedule.Error != "" {
			return fmt.Sprintf("Schedule %s: %s", name, liveText(schedule.Error))
		}
		return fmt.Sprintf("Waiting for %s to produce data for this bundle", name)
	}
	for _, activation := range o.Activations {
		switch activation.Status {
		case "pending", "running", "repairing", "deferred_interval", "deferred_capacity":
		default:
			continue // succeeded/failed/superseded entries can describe old runs
		}
		detail := "Activating data from " + liveText(activation.Schedule)
		if activation.RunID != nil && *activation.RunID > 0 {
			detail += fmt.Sprintf(" run #%d", *activation.RunID)
		}
		if activation.DeferReason != "" {
			return detail + ": " + liveText(activation.DeferReason)
		}
		if activation.Phase != "" {
			detail += ": " + strings.ReplaceAll(liveText(activation.Phase), "_", " ")
		} else if activation.LastError != "" {
			detail += "; last attempt: " + liveText(activation.LastError)
		}
		return detail
	}
	if o.RedeployInFlight && appstatus.Serving(o.App.Status) {
		return "Deployment in progress; current version is serving"
	}
	// Only non-serving replicas carry current failure reasons. LastExit belongs
	// to an earlier process and must not make its healthy replacement look broken.
	for _, replica := range o.Replicas {
		if appstatus.Serving(replica.Status) {
			continue
		}
		if replica.Reason != "" {
			return fmt.Sprintf("Replica %d: %s", replica.Index, liveText(replica.Reason))
		}
	}
	if !appstatus.Serving(o.App.Status) && o.App.LastReplicaError != "" {
		return liveText(o.App.LastReplicaError)
	}
	for _, replica := range o.Replicas {
		if replica.Status == "starting" {
			return fmt.Sprintf("Replica %d is starting", replica.Index)
		}
	}
	if o.RedeployInFlight {
		return "Deployment is still in progress"
	}
	return ""
}
