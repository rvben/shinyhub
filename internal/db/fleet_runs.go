package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rvben/shinyhub/internal/provenance"
)

var (
	ErrFleetRunConflict = errors.New("fleet run id already registered with different metadata")
	ErrFleetRunFinished = errors.New("fleet run is already finished")
)

const fleetRunSequenceLock int64 = 0x466c65657452756e // "FleetRun"

type FleetRun struct {
	ID             string              `json:"id"`
	FleetID        string              `json:"fleet_id"`
	Kind           string              `json:"kind"`
	Provenance     provenance.Metadata `json:"provenance"`
	Sequence       int64               `json:"sequence"`
	Status         string              `json:"status"`
	HeartbeatAt    time.Time           `json:"heartbeat_at"`
	FinishedAt     *time.Time          `json:"finished_at,omitempty"`
	ExitCode       *int                `json:"exit_code,omitempty"`
	ExitReason     string              `json:"exit_reason,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
	UserID         *int64              `json:"-"`
	PrincipalID    *int64              `json:"principal_id,omitempty"`
	CredentialID   *int64              `json:"credential_id,omitempty"`
	CredentialType string              `json:"credential_type,omitempty"`
	CredentialName string              `json:"credential_name,omitempty"`
}

type CreateFleetRunParams struct {
	ID             string
	FleetID        string
	Kind           string
	Provenance     provenance.Metadata
	UserID         *int64
	CredentialID   *int64
	CredentialType string
	CredentialName string
}

func (s *Store) CreateFleetRun(p CreateFleetRunParams) (*FleetRun, bool, error) {
	encoded, err := json.Marshal(p.Provenance)
	if err != nil {
		return nil, false, fmt.Errorf("encode fleet run provenance: %w", err)
	}
	tx, err := s.d.beginWrite(context.Background(), s.db.real, fleetRunSequenceLock)
	if err != nil {
		return nil, false, fmt.Errorf("begin fleet run registration: %w", err)
	}
	defer tx.Rollback()

	if run, err := getFleetRunFrom(tx, p.ID); err == nil {
		if !sameFleetRun(run, p, encoded) {
			return nil, false, ErrFleetRunConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit fleet run lookup: %w", err)
		}
		return run, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}

	var sequence int64
	if err := tx.QueryRow(`UPDATE fleet_run_sequence
		SET last_sequence = last_sequence + 1 WHERE singleton = 1
		RETURNING last_sequence`).Scan(&sequence); err != nil {
		return nil, false, fmt.Errorf("allocate fleet run sequence: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO fleet_runs
		(id, fleet_id, kind, provenance, user_id, run_sequence, heartbeat_at,
		 credential_id, credential_type, credential_name)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?, ?)`, p.ID, p.FleetID, p.Kind,
		string(encoded), p.UserID, sequence, p.CredentialID, p.CredentialType, p.CredentialName); err != nil {
		return nil, false, fmt.Errorf("create fleet run: %w", err)
	}
	run, err := getFleetRunFrom(tx, p.ID)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit fleet run registration: %w", err)
	}
	return run, true, nil
}

func (s *Store) GetFleetRun(id string) (*FleetRun, error) {
	return getFleetRunFrom(s.db, id)
}

type fleetRunQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func getFleetRunFrom(q fleetRunQueryer, id string) (*FleetRun, error) {
	var run FleetRun
	var raw string
	var uid sql.NullInt64
	var credentialID sql.NullInt64
	var finished sql.NullTime
	var exitCode sql.NullInt64
	if err := q.QueryRow(`SELECT id, fleet_id, kind, provenance, user_id, run_sequence,
		status, heartbeat_at, finished_at, exit_code, exit_reason, created_at,
		credential_id, credential_type, credential_name
		FROM fleet_runs WHERE id = ?`, id).Scan(
		&run.ID, &run.FleetID, &run.Kind, &raw, &uid, &run.Sequence,
		&run.Status, &run.HeartbeatAt, &finished, &exitCode, &run.ExitReason, &run.CreatedAt,
		&credentialID, &run.CredentialType, &run.CredentialName,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get fleet run: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &run.Provenance); err != nil {
		return nil, fmt.Errorf("decode fleet run provenance: %w", err)
	}
	if uid.Valid {
		v := uid.Int64
		run.UserID = &v
		run.PrincipalID = &v
	}
	if credentialID.Valid {
		v := credentialID.Int64
		run.CredentialID = &v
	}
	if finished.Valid {
		v := finished.Time
		run.FinishedAt = &v
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		run.ExitCode = &v
	}
	return &run, nil
}

func sameFleetRun(run *FleetRun, p CreateFleetRunParams, encoded []byte) bool {
	return run.FleetID == p.FleetID && run.Kind == p.Kind && string(encoded) == mustMarshal(run.Provenance) &&
		sameNullableID(run.UserID, p.UserID) && sameNullableID(run.CredentialID, p.CredentialID)
}

// TouchFleetRun proves that a live client still owns an unfinished apply.
func (s *Store) TouchFleetRun(id string) error {
	res, err := s.db.Exec(`UPDATE fleet_runs SET heartbeat_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'running'`, id)
	if err != nil {
		return fmt.Errorf("heartbeat fleet run: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}
	if _, err := s.GetFleetRun(id); err != nil {
		return err
	}
	return ErrFleetRunFinished
}

// FinishFleetRun records an immutable terminal result. An identical retry is
// accepted so a client may safely repeat a request after losing the response.
func (s *Store) FinishFleetRun(id, status string, exitCode int, reason string) error {
	if status != "succeeded" && status != "partial" && status != "conflict" && status != "failed" {
		return fmt.Errorf("invalid terminal fleet run status %q", status)
	}
	res, err := s.db.Exec(`UPDATE fleet_runs SET status = ?, exit_code = ?, exit_reason = ?,
		finished_at = CURRENT_TIMESTAMP, heartbeat_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'running'`, status, exitCode, reason, id)
	if err != nil {
		return fmt.Errorf("finish fleet run: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}
	run, err := s.GetFleetRun(id)
	if err != nil {
		return err
	}
	if run.Status == status && run.ExitCode != nil && *run.ExitCode == exitCode && run.ExitReason == reason {
		return nil
	}
	return ErrFleetRunFinished
}

func mustMarshal(v any) string { b, _ := json.Marshal(v); return string(b) }
func sameNullableID(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
