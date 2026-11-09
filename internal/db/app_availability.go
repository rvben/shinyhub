package db

import (
	"context"
	"database/sql"
	"errors"
)

// AppAvailability is a single database snapshot for anonymous monitoring. It
// deliberately contains no diagnostics or identity information. Compatibility
// failures block serving; an active publisher only blocks new activations.
type AppAvailability struct {
	Status            string
	DeploymentID      int64
	ServingBlocked    bool
	ActivationBlocked bool
}

// GetAppAvailability uses the published generation, with the same legacy
// fallback as GetServingDeployment. A pending candidate never replaces a
// published release. One statement avoids mixing lifecycle and repair states
// read on opposite sides of a producer publication.
func (s *Store) GetAppAvailability(ctx context.Context, slug string) (*AppAvailability, error) {
	var a AppAvailability
	err := s.db.QueryRowContext(ctx, `SELECT status,
		COALESCE((SELECT d.id FROM deployments d WHERE d.app_id = apps.id
		  AND d.status = 'succeeded' AND d.id = apps.active_deployment_id),
		  (SELECT latest.id FROM deployments latest WHERE latest.app_id = apps.id
		    AND latest.status = 'succeeded' AND latest.serving_activation_id IS NULL
		    ORDER BY latest.id DESC LIMIT 1), 0),
		CASE WHEN (`+appCompatibilityFailuresSQL+`) THEN 1 ELSE 0 END,
		CASE WHEN (`+appCompatibilityBlockersSQL+`) THEN 1 ELSE 0 END
		FROM apps WHERE slug = ?`, slug).Scan(&a.Status, &a.DeploymentID, &a.ServingBlocked, &a.ActivationBlocked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}
