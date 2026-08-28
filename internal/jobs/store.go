package jobs

import "github.com/rvben/shinyhub/internal/db"

// Store is the narrow interface jobs.Manager needs. *db.Store satisfies it; tests
// can fake it without touching SQLite.
type Store interface {
	GetSchedule(id int64) (*db.Schedule, error)
	GetAppByID(id int64) (*db.App, error)
	ListDeployments(appID int64) ([]*db.Deployment, error)
	HasPendingDeployment(appID int64) (bool, error)
	AppCompatibilityQuarantined(appID int64) (bool, error)
	AppCompatibilityQuarantinedExceptRun(appID, runID int64) (bool, error)
	ScheduleProducerRepairRequired(scheduleID int64) (bool, error)
	ListAppEnvVars(appID int64) ([]db.AppEnvVar, error)
	ListSharedDataSources(consumerAppID int64) ([]*db.SharedDataMount, error)
	InsertScheduleRun(p db.InsertScheduleRunParams) (int64, error)
	InsertDeployScheduleRun(p db.InsertScheduleRunParams) (int64, error)
	GetScheduleRun(id int64) (*db.ScheduleRun, error)
	SetScheduleRunLogPath(runID int64, logPath string) error
	FinishScheduleRun(p db.FinishScheduleRunParams) error
	CompleteScheduleRunAndEnqueueActivation(p db.CompleteScheduleRunParams) (*db.ScheduleActivation, error)
	LogAuditEvent(p db.AuditEventParams)
}
