package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/rvben/shinyhub/internal/provenance"
)

var ErrFleetStateSuperseded = errors.New("fleet state belongs to a newer run")

// fleetDeclarationOrder matches fleet.DeclaredState's durable field order.
// Keeping the established order avoids rewriting baselines produced by older
// official CLIs, while sorting incoming values makes semantic equality
// independent of JSON array order for other clients and future CLI changes.
var fleetDeclarationOrder = map[string]int{
	"visibility": 0, "name": 1, "description": 2, "icon": 3, "project": 4,
	"hibernate_timeout_minutes": 5, "replicas": 6, "max_sessions_per_replica": 7,
	"render_seconds": 8, "identity_headers": 9, "usage_identity_mode": 10,
	"min_warm_replicas": 11, "memory_limit_mb": 12, "cpu_quota_percent": 13,
	"worker_isolation": 14, "worker_grouped_size": 15, "worker_max_workers": 16,
	"worker_warm_spares": 17, "worker_max_session_lifetime_secs": 18, "autoscale": 19,
}

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
	} else {
		values = append([]FleetDeclaredValue(nil), values...)
	}
	sort.Slice(values, func(i, j int) bool {
		iRank, iKnown := fleetDeclarationOrder[values[i].Key]
		jRank, jKnown := fleetDeclarationOrder[values[j].Key]
		if iKnown != jKnown {
			return iKnown
		}
		if iKnown && iRank != jRank {
			return iRank < jRank
		}
		return values[i].Key < values[j].Key
	})
	b, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode fleet declaration: %w", err)
	}
	return string(b), nil
}

// RecordAppFleetSuccess records a successful application using the legacy
// semantics where the caller is assumed to have mutated the app. New callers
// should use RecordAppFleetSuccessWithChange so a verification-only apply
// does not overwrite the provenance and timestamp of the last real change.
func (s *Store) RecordAppFleetSuccess(appID int64, runID, digest string, values []FleetDeclaredValue) error {
	return s.RecordAppFleetSuccessWithChange(appID, runID, digest, values, true)
}

// RecordAppFleetSuccessWithChange replaces the drift baseline and clears any
// prior incomplete attempt. Every call advances latest_run_id and
// convergence_updated_at: those fields mean "last checked". The successful
// application provenance and applied_at advance only when persisted desired
// state or the declaration/digest baseline changed, or when the retained
// successful run belongs to a different fleet than the incoming run. That
// last condition covers adoption: the app changed hands with an identical
// declaration and digest, the adopt run's own fleet-state request was lost,
// and the reader discards a cross-fleet application - so preserving the old
// fleet's run would leave the owning fleet with no recorded application
// until a real declaration change. Repeating an identical same-fleet no-op
// apply therefore remains auditable without looking like an app update.
// The API validates the declaration against live state before calling this
// method.
func (s *Store) RecordAppFleetSuccessWithChange(appID int64, runID, digest string, values []FleetDeclaredValue, stateChanged bool) error {
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
			successful_run_id = CASE
				WHEN ? OR app_fleet_state.declaration <> excluded.declaration
					OR app_fleet_state.desired_content_digest <> excluded.desired_content_digest
					OR (SELECT fleet_id FROM fleet_runs WHERE id = app_fleet_state.successful_run_id)
						IS DISTINCT FROM (SELECT fleet_id FROM fleet_runs WHERE id = excluded.latest_run_id)
				THEN excluded.successful_run_id
				ELSE app_fleet_state.successful_run_id
			END,
			declaration = excluded.declaration,
			desired_content_digest = excluded.desired_content_digest,
			applied_at = CASE
				WHEN ? OR app_fleet_state.declaration <> excluded.declaration
					OR app_fleet_state.desired_content_digest <> excluded.desired_content_digest
					OR (SELECT fleet_id FROM fleet_runs WHERE id = app_fleet_state.successful_run_id)
						IS DISTINCT FROM (SELECT fleet_id FROM fleet_runs WHERE id = excluded.latest_run_id)
				THEN CURRENT_TIMESTAMP
				ELSE app_fleet_state.applied_at
			END,
			latest_run_id = excluded.latest_run_id,
			convergence_status = 'in_sync',
			convergence_error = '',
			convergence_updated_at = CURRENT_TIMESTAMP
		WHERE (SELECT run_sequence FROM fleet_runs WHERE id = excluded.latest_run_id) >=
		      COALESCE((SELECT run_sequence FROM fleet_runs WHERE id = app_fleet_state.latest_run_id), 0)`,
		appID, runID, decl, digest, runID, stateChanged, stateChanged)
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
