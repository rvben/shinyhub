package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rvben/shinyhub/internal/provenance"
)

var ErrFleetStateSuperseded = errors.New("fleet state belongs to a newer run")

// FleetDeclaredValue is one fleet-owned app field in its normalized display
// form. The CLI records only fields declared by the effective fleet+bundle
// manifest; omitted fields deliberately remain dashboard-owned.
type FleetDeclaredValue struct {
	Key     string `json:"key"`
	Desired string `json:"desired"`
}

// FleetStateRun is the provenance attached to a fleet convergence attempt.
type FleetStateRun struct {
	ID         string              `json:"run_id"`
	FleetID    string              `json:"fleet_id"`
	Provenance provenance.Metadata `json:"provenance"`
	Actor      string              `json:"actor,omitempty"`
	CreatedAt  time.Time           `json:"created_at"`
}

// AppFleetState keeps the last successful declaration separate from the most
// recent attempt. A failed attempt can therefore be shown honestly without
// destroying the baseline used to identify temporary dashboard changes.
type AppFleetState struct {
	AppID                int64
	Declaration          []FleetDeclaredValue
	DesiredContentDigest string
	AppliedAt            *time.Time
	SuccessfulRun        *FleetStateRun
	LatestRun            *FleetStateRun
	ConvergenceStatus    string
	ConvergenceError     string
	ConvergenceUpdatedAt time.Time
}

func encodeFleetDeclaration(values []FleetDeclaredValue) (string, error) {
	if values == nil {
		values = []FleetDeclaredValue{}
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode fleet declaration: %w", err)
	}
	return string(b), nil
}

// RecordAppFleetSuccess replaces the drift baseline and clears any prior
// incomplete attempt. The API validates the declaration against live state
// before calling this method.
func (s *Store) RecordAppFleetSuccess(appID int64, runID, digest string, values []FleetDeclaredValue) error {
	decl, err := encodeFleetDeclaration(values)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`
		INSERT INTO app_fleet_state (
			app_id, successful_run_id, declaration, desired_content_digest,
			applied_at, latest_run_id, convergence_status, convergence_error,
			convergence_updated_at
		) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, ?, 'in_sync', '', CURRENT_TIMESTAMP)
		ON CONFLICT(app_id) DO UPDATE SET
			successful_run_id = excluded.successful_run_id,
			declaration = excluded.declaration,
			desired_content_digest = excluded.desired_content_digest,
			applied_at = CURRENT_TIMESTAMP,
			latest_run_id = excluded.latest_run_id,
			convergence_status = 'in_sync',
			convergence_error = '',
			convergence_updated_at = CURRENT_TIMESTAMP
		WHERE (SELECT run_sequence FROM fleet_runs WHERE id = excluded.latest_run_id) >=
		      COALESCE((SELECT run_sequence FROM fleet_runs WHERE id = app_fleet_state.latest_run_id), 0)`,
		appID, runID, decl, digest, runID)
	if err != nil {
		return fmt.Errorf("record app fleet success: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrFleetStateSuperseded
	}
	return nil
}

// RecordAppFleetIncomplete preserves the last successful declaration while
// recording that the newest convergence attempt did not finish.
func (s *Store) RecordAppFleetIncomplete(appID int64, runID, message string) error {
	res, err := s.db.Exec(`
		INSERT INTO app_fleet_state (
			app_id, latest_run_id, convergence_status, convergence_error,
			convergence_updated_at
		) VALUES (?, ?, 'incomplete', ?, CURRENT_TIMESTAMP)
		ON CONFLICT(app_id) DO UPDATE SET
			latest_run_id = excluded.latest_run_id,
			convergence_status = 'incomplete',
			convergence_error = excluded.convergence_error,
			convergence_updated_at = CURRENT_TIMESTAMP
		WHERE (SELECT run_sequence FROM fleet_runs WHERE id = excluded.latest_run_id) >=
		      COALESCE((SELECT run_sequence FROM fleet_runs WHERE id = app_fleet_state.latest_run_id), 0)`,
		appID, runID, message)
	if err != nil {
		return fmt.Errorf("record app fleet incomplete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrFleetStateSuperseded
	}
	return nil
}

func scanFleetStateRun(id, fleetID, raw, actor sql.NullString, created sql.NullTime) (*FleetStateRun, error) {
	if !id.Valid {
		return nil, nil
	}
	run := &FleetStateRun{ID: id.String, FleetID: fleetID.String, Actor: actor.String}
	if created.Valid {
		run.CreatedAt = created.Time
	}
	if raw.Valid && raw.String != "" {
		if err := json.Unmarshal([]byte(raw.String), &run.Provenance); err != nil {
			return nil, fmt.Errorf("decode fleet state provenance: %w", err)
		}
	}
	return run, nil
}

// GetAppFleetState returns nil when no fleet-aware CLI has yet recorded state
// for the app. Older fleet-managed apps therefore remain neutral rather than
// being guessed in or out of sync.
func (s *Store) GetAppFleetState(appID int64) (*AppFleetState, error) {
	var st AppFleetState
	var declaration string
	var applied sql.NullTime
	var successID, successFleet, successProv, successActor sql.NullString
	var successCreated sql.NullTime
	var latestID, latestFleet, latestProv, latestActor sql.NullString
	var latestCreated sql.NullTime
	err := s.db.QueryRow(`
		SELECT fs.app_id, fs.declaration, fs.desired_content_digest, fs.applied_at,
		       fs.convergence_status, fs.convergence_error, fs.convergence_updated_at,
		       sr.id, sr.fleet_id, sr.provenance, su.username, sr.created_at,
		       lr.id, lr.fleet_id, lr.provenance, lu.username, lr.created_at
		FROM app_fleet_state fs
		LEFT JOIN fleet_runs sr ON sr.id = fs.successful_run_id
		LEFT JOIN users su ON su.id = sr.user_id
		LEFT JOIN fleet_runs lr ON lr.id = fs.latest_run_id
		LEFT JOIN users lu ON lu.id = lr.user_id
		WHERE fs.app_id = ?`, appID).Scan(
		&st.AppID, &declaration, &st.DesiredContentDigest, &applied,
		&st.ConvergenceStatus, &st.ConvergenceError, &st.ConvergenceUpdatedAt,
		&successID, &successFleet, &successProv, &successActor, &successCreated,
		&latestID, &latestFleet, &latestProv, &latestActor, &latestCreated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get app fleet state: %w", err)
	}
	if err := json.Unmarshal([]byte(declaration), &st.Declaration); err != nil {
		return nil, fmt.Errorf("decode app fleet declaration: %w", err)
	}
	if applied.Valid {
		v := applied.Time
		st.AppliedAt = &v
	}
	st.SuccessfulRun, err = scanFleetStateRun(successID, successFleet, successProv, successActor, successCreated)
	if err != nil {
		return nil, err
	}
	st.LatestRun, err = scanFleetStateRun(latestID, latestFleet, latestProv, latestActor, latestCreated)
	if err != nil {
		return nil, err
	}
	return &st, nil
}
