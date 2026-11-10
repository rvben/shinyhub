package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// AppAvailability is a single database snapshot for anonymous monitoring. It
// deliberately contains no diagnostics or ownership information. Compatibility
// failures block serving; an active publisher only blocks new activations.
type AppAvailability struct {
	Slug              string
	Availability      string
	OperatorStopped   bool
	Status            string
	DeploymentID      int64
	ServingBlocked    bool
	ActivationBlocked bool
}

// availabilityColumns uses the published generation, with the same legacy
// fallback as GetServingDeployment. A pending candidate never replaces a
// published release. One statement avoids mixing lifecycle and repair states
// read on opposite sides of a producer publication.
// It deliberately avoids full app records and their diagnostics.
const availabilityColumns = `slug, availability, operator_stopped, status,
 COALESCE((SELECT d.id FROM deployments d WHERE d.app_id = apps.id
   AND d.status = 'succeeded' AND d.id = apps.active_deployment_id),
   (SELECT latest.id FROM deployments latest WHERE latest.app_id = apps.id
     AND latest.status = 'succeeded' AND latest.serving_activation_id IS NULL
     ORDER BY latest.id DESC LIMIT 1), 0),
 CASE WHEN (` + appCompatibilityFailuresSQL + `) THEN 1 ELSE 0 END,
 CASE WHEN (` + appCompatibilityBlockersSQL + `) THEN 1 ELSE 0 END`

func scanAppAvailability(row scanner) (*AppAvailability, error) {
	var a AppAvailability
	var stopped int
	err := row.Scan(&a.Slug, &a.Availability, &stopped, &a.Status, &a.DeploymentID, &a.ServingBlocked, &a.ActivationBlocked)
	a.OperatorStopped = stopped != 0
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) GetAppAvailability(ctx context.Context, slug string) (*AppAvailability, error) {
	return scanAppAvailability(s.db.QueryRowContext(ctx, `SELECT `+availabilityColumns+` FROM apps WHERE slug = ?`, slug))
}

// MaxAvailabilityApps bounds materialization and response size. Oversized fleets
// fail closed instead of returning a silently incomplete monitoring inventory.
const MaxAvailabilityApps = 10000

var ErrAvailabilityFleetTooLarge = errors.New("availability fleet exceeds snapshot limit")

// ListAppAvailability selects the complete exposed inventory in one statement,
// so policy membership and database state share a snapshot. Routing observations
// are evaluated later and may race a deployment, just like per-app probes.
func (s *Store) ListAppAvailability(ctx context.Context, slugs []string, allowOptIn bool) ([]AppAvailability, error) {
	where := "1 = 0"
	args := []any{}
	if allowOptIn {
		where = "availability = 'public'"
	}
	if len(slugs) > 0 {
		arg, err := s.d.slugAllowlistArg(slugs)
		if err != nil {
			return nil, err
		}
		where += " OR " + s.d.slugAllowlistClause()
		args = append(args, arg)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+availabilityColumns+` FROM apps WHERE `+where+` ORDER BY slug LIMIT `+fmt.Sprint(MaxAvailabilityApps+1), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := make([]AppAvailability, 0)
	for rows.Next() {
		a, err := scanAppAvailability(rows)
		if err != nil {
			return nil, err
		}
		if len(apps) == MaxAvailabilityApps {
			return nil, ErrAvailabilityFleetTooLarge
		}
		apps = append(apps, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(apps, func(a, b AppAvailability) int { return strings.Compare(a.Slug, b.Slug) })
	return apps, nil
}

// ClearAppOperatorStopIntent records an accepted activation attempt before boot.
// A failed start must not continue to look like an intentionally disabled app.
// Callers hold the app lifecycle lock and have completed activation admission.
func (s *Store) ClearAppOperatorStopIntent(appID int64) error {
	_, err := s.db.Exec(`UPDATE apps SET operator_stopped = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND operator_stopped <> 0`, appID)
	return err
}
