package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rvben/shinyhub/internal/provenance"
)

var ErrFleetRunConflict = errors.New("fleet run id already registered with different metadata")

type FleetRun struct {
	ID         string              `json:"id"`
	FleetID    string              `json:"fleet_id"`
	Kind       string              `json:"kind"`
	Provenance provenance.Metadata `json:"provenance"`
	CreatedAt  time.Time           `json:"created_at"`
}

type CreateFleetRunParams struct {
	ID         string
	FleetID    string
	Kind       string
	Provenance provenance.Metadata
	UserID     *int64
}

func (s *Store) CreateFleetRun(p CreateFleetRunParams) (*FleetRun, bool, error) {
	encoded, err := json.Marshal(p.Provenance)
	if err != nil {
		return nil, false, fmt.Errorf("encode fleet run provenance: %w", err)
	}
	res, err := s.db.Exec(`INSERT INTO fleet_runs (id, fleet_id, kind, provenance, user_id)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, p.ID, p.FleetID, p.Kind, string(encoded), p.UserID)
	if err != nil {
		return nil, false, fmt.Errorf("create fleet run: %w", err)
	}
	n, _ := res.RowsAffected()
	run, userID, err := s.getFleetRun(p.ID)
	if err != nil {
		return nil, false, err
	}
	if run.FleetID != p.FleetID || run.Kind != p.Kind || string(encoded) != mustMarshal(run.Provenance) || !sameNullableID(userID, p.UserID) {
		return nil, false, ErrFleetRunConflict
	}
	return run, n > 0, nil
}

func (s *Store) GetFleetRun(id string) (*FleetRun, error) {
	run, _, err := s.getFleetRun(id)
	return run, err
}

func (s *Store) getFleetRun(id string) (*FleetRun, *int64, error) {
	var run FleetRun
	var raw string
	var uid sql.NullInt64
	if err := s.db.QueryRow(`SELECT id, fleet_id, kind, provenance, user_id, created_at FROM fleet_runs WHERE id = ?`, id).
		Scan(&run.ID, &run.FleetID, &run.Kind, &raw, &uid, &run.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("get fleet run: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &run.Provenance); err != nil {
		return nil, nil, fmt.Errorf("decode fleet run provenance: %w", err)
	}
	var userID *int64
	if uid.Valid {
		v := uid.Int64
		userID = &v
	}
	return &run, userID, nil
}

func mustMarshal(v any) string { b, _ := json.Marshal(v); return string(b) }
func sameNullableID(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
