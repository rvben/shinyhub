package autoscale

import "github.com/rvben/shinyhub/internal/db"

// AuditRecorder is the subset of *db.Store the controller needs to record
// scale events. db.Store satisfies it; main.go passes store directly.
type AuditRecorder interface {
	LogAuditEvent(p db.AuditEventParams)
}

// Action constants for autoscale audit events.
const (
	ActionScaleUp   = "autoscale_scale_up"
	ActionScaleDown = "autoscale_scale_down"
)
