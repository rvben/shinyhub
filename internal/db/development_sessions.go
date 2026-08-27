package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	DevelopmentTargetExisting  = "existing"
	DevelopmentTargetCreated   = "created"
	DevelopmentTargetEphemeral = "ephemeral"

	DevelopmentSessionActive = "active"
	DevelopmentSessionEnded  = "ended"
)

func ValidDevelopmentTarget(kind string) bool {
	switch kind {
	case DevelopmentTargetExisting, DevelopmentTargetCreated, DevelopmentTargetEphemeral:
		return true
	default:
		return false
	}
}

type DevelopmentSession struct {
	ID             string     `json:"id"`
	AppID          int64      `json:"app_id,omitempty"`
	TargetKind     string     `json:"target_kind"`
	Status         string     `json:"status"`
	UserID         *int64     `json:"user_id,omitempty"`
	Actor          string     `json:"actor,omitempty"`
	CredentialID   *int64     `json:"credential_id,omitempty"`
	CredentialType string     `json:"credential_type,omitempty"`
	CredentialName string     `json:"credential_name,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

type UpsertDevelopmentSessionParams struct {
	ID             string
	AppID          int64
	TargetKind     string
	UserID         *int64
	Actor          string
	CredentialID   *int64
	CredentialType string
	CredentialName string
	ExpiresAt      *time.Time
}

// UpsertDevelopmentSession starts a session or refreshes its last-activity
// timestamp. A client-generated ID is permanently bound to one app and target
// kind; attempting to reuse it elsewhere returns an error instead of merging
// unrelated development histories.
func (s *Store) UpsertDevelopmentSession(p UpsertDevelopmentSessionParams) error {
	if p.ID == "" || p.AppID == 0 || !ValidDevelopmentTarget(p.TargetKind) {
		return fmt.Errorf("invalid development session")
	}
	res, err := s.db.Exec(`
		INSERT INTO development_sessions
			(id, app_id, target_kind, status, user_id, actor,
			 credential_id, credential_type, credential_name, expires_at)
		VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			updated_at = CURRENT_TIMESTAMP,
			expires_at = COALESCE(excluded.expires_at, development_sessions.expires_at)
		WHERE development_sessions.app_id = excluded.app_id
		  AND development_sessions.target_kind = excluded.target_kind
		  AND development_sessions.status = 'active'`,
		p.ID, p.AppID, p.TargetKind, p.UserID, p.Actor,
		p.CredentialID, p.CredentialType, p.CredentialName, p.ExpiresAt)
	if err != nil {
		return fmt.Errorf("upsert development session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("development session %q is ended or already bound to another app or target", p.ID)
	}
	return nil
}

func (s *Store) EndDevelopmentSession(appID int64, id string, endedAt time.Time) error {
	res, err := s.db.Exec(`
		UPDATE development_sessions
		SET status = 'ended', ended_at = ?, updated_at = ?
		WHERE id = ? AND app_id = ?`, endedAt, endedAt, id, appID)
	if err != nil {
		return fmt.Errorf("end development session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetDevelopmentSession(appID int64, id string) (*DevelopmentSession, error) {
	row := s.db.QueryRow(`
		SELECT id, app_id, target_kind, status, user_id, actor,
		       credential_id, credential_type, credential_name,
		       created_at, updated_at, ended_at, expires_at
		FROM development_sessions WHERE app_id = ? AND id = ?`, appID, id)
	return scanDevelopmentSession(row)
}

func scanDevelopmentSession(row scanner) (*DevelopmentSession, error) {
	var out DevelopmentSession
	var userID, credentialID sql.NullInt64
	var endedAt, expiresAt sql.NullTime
	if err := row.Scan(&out.ID, &out.AppID, &out.TargetKind, &out.Status,
		&userID, &out.Actor, &credentialID, &out.CredentialType, &out.CredentialName,
		&out.CreatedAt, &out.UpdatedAt, &endedAt, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if userID.Valid {
		id := userID.Int64
		out.UserID = &id
	}
	if credentialID.Valid {
		id := credentialID.Int64
		out.CredentialID = &id
	}
	if endedAt.Valid {
		at := endedAt.Time
		out.EndedAt = &at
	}
	if expiresAt.Valid {
		at := expiresAt.Time
		out.ExpiresAt = &at
	}
	return &out, nil
}

func (s *Store) MarkEphemeralApp(appID int64, sessionID string, expiresAt time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO ephemeral_apps (app_id, development_session_id, expires_at)
		VALUES (?, ?, ?)
		ON CONFLICT(app_id) DO UPDATE SET
			development_session_id = excluded.development_session_id,
			expires_at = excluded.expires_at`, appID, sessionID, expiresAt)
	if err != nil {
		return fmt.Errorf("mark ephemeral app: %w", err)
	}
	return nil
}

type ExpiredEphemeralApp struct {
	AppID     int64
	Slug      string
	SessionID string
	ExpiresAt time.Time
}

func (s *Store) ListExpiredEphemeralApps(now time.Time) ([]ExpiredEphemeralApp, error) {
	rows, err := s.db.Query(`
		SELECT e.app_id, a.slug, e.development_session_id, e.expires_at
		FROM ephemeral_apps e JOIN apps a ON a.id = e.app_id
		WHERE e.expires_at <= ? AND a.status != 'deleting'
		ORDER BY e.expires_at, e.app_id`, now)
	if err != nil {
		return nil, fmt.Errorf("list expired ephemeral apps: %w", err)
	}
	defer rows.Close()
	var out []ExpiredEphemeralApp
	for rows.Next() {
		var app ExpiredEphemeralApp
		if err := rows.Scan(&app.AppID, &app.Slug, &app.SessionID, &app.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

func (s *Store) DevelopmentSessionDeploymentIDs(appID int64, sessionID string) (map[int64]bool, error) {
	rows, err := s.db.Query(`
		SELECT id FROM deployments
		WHERE app_id = ? AND development_session_id = ?`, appID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list development session deployments: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
