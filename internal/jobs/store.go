package jobs

import "github.com/rvben/shinyhub/internal/db"

// Store is the narrow interface jobs.Manager needs. *db.Store satisfies it; tests
// can fake it without touching SQLite.
type Store interface {
	GetSchedule(id int64) (*db.Schedule, error)
	GetAppByID(id int64) (*db.App, error)
	ListDeployments(appID int64) ([]*db.Deployment, error)
	ListAppEnvVars(appID int64) ([]db.AppEnvVar, error)
	ListSharedDataSources(consumerAppID int64) ([]*db.SharedDataMount, error)
	InsertScheduleRun(p db.InsertScheduleRunParams) (int64, error)
	SetScheduleRunLogPath(runID int64, logPath string) error
	FinishScheduleRun(p db.FinishScheduleRunParams) error
	LogAuditEvent(p db.AuditEventParams)
}
