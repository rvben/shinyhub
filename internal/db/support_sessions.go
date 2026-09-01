package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

const SupportSessionDuration = 15 * time.Minute

const supportSessionRetention = 30 * 24 * time.Hour

var ErrSupportSessionActive = errors.New("an active support session already exists for this administrator")

type CreateSupportSessionParams struct {
	ID                string
	ActorUserID       int64
	ActorUsername     string
	ActorTokenEpoch   int64
	SubjectUserID     int64
	SubjectUsername   string
	SubjectRole       string
	SubjectTokenEpoch int64
	AppID             int64
	AppSlug           string
	Reason            string
	LaunchCodeHash    string
	ExpiresAt         time.Time
	AuditDetail       string
	IPAddress         string
}

type SupportSession struct {
	ID              string
	ActorUserID     *int64
	ActorUsername   string
	SubjectUserID   *int64
	SubjectUsername string
	AppSlug         string
	Reason          string
	TokenJTI        string
	TokenExpiresAt  *time.Time
	CreatedAt       time.Time
	ExpiresAt       time.Time
	StoppedAt       *time.Time
	StopReason      string
	// FirstUsedAt is set by the first request the access middleware admits
	// under this session. Until then the browser has not arrived on the app,
	// and the reaper may still close the session as abandoned.
	FirstUsedAt  *time.Time
	NewlyStopped bool
}

// GetActiveSupportSessionForActor returns the actor's one live, unexpired
// support session. Expired rows are deliberately left for the existing lazy
// expiry path so a read does not create an audit transition.
func (s *Store) GetActiveSupportSessionForActor(actorID int64) (*SupportSession, error) {
	row := s.db.QueryRow(`SELECT id, actor_user_id, actor_username, subject_user_id, subject_username,
		       app_slug_snapshot, reason, COALESCE(token_jti, ''), token_expires_at,
		       created_at, expires_at, stopped_at, stop_reason, first_used_at
		  FROM support_sessions
		 WHERE actor_user_id = ? AND stopped_at IS NULL AND expires_at > ?
		 ORDER BY created_at DESC LIMIT 1`, actorID, time.Now().UTC())
	session, err := scanSupportSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active support session: %w", err)
	}
	return session, nil
}

// GetSupportSession returns the support session with this ID in whatever
// state it is in, or nil when no such row exists. The root guard cookie carries
// the ID, so a request that reaches an app outside the session's scope can
// name the session that blocks it instead of being served anonymously.
func (s *Store) GetSupportSession(id string) (*SupportSession, error) {
	row := s.db.QueryRow(`SELECT id, actor_user_id, actor_username, subject_user_id, subject_username,
		       app_slug_snapshot, reason, COALESCE(token_jti, ''), token_expires_at,
		       created_at, expires_at, stopped_at, stop_reason, first_used_at
		  FROM support_sessions
		 WHERE id = ?`, id)
	session, err := scanSupportSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get support session: %w", err)
	}
	return session, nil
}

func (s *Store) CreateSupportSession(p CreateSupportSessionParams) error {
	if p.ExpiresAt.IsZero() {
		p.ExpiresAt = time.Now().UTC().Add(SupportSessionDuration)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("create support session: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().UTC()
	// Keep a single live support identity per administrator. Expired sessions
	// and launches never observed by app access beyond a short activation grace
	// are atomically closed before the unique index arbitrates creators.
	rows, err := tx.Query(`UPDATE support_sessions
		SET stopped_at = CURRENT_TIMESTAMP,
		    stop_reason = CASE WHEN expires_at <= ? THEN 'expired' ELSE 'activation_abandoned' END
		WHERE actor_user_id = ? AND stopped_at IS NULL
		  AND (expires_at <= ? OR (first_used_at IS NULL AND created_at < `+s.d.nowMinusSeconds(90)+`))
		RETURNING id, actor_user_id, actor_username, subject_user_id, subject_username,
		          app_slug_snapshot, reason, expires_at, stop_reason,
		          token_jti, token_expires_at`, now, p.ActorUserID, now)
	if err != nil {
		return fmt.Errorf("expire prior support session: %w", err)
	}
	type abandonedToken struct {
		id, actorUsername, subjectUsername, appSlug, reason, stopReason string
		actorID, subjectID                                              sql.NullInt64
		sessionExpiresAt                                                time.Time
		jti                                                             sql.NullString
		tokenExpiresAt                                                  sql.NullTime
	}
	var abandoned []abandonedToken
	for rows.Next() {
		var token abandonedToken
		if err := rows.Scan(&token.id, &token.actorID, &token.actorUsername, &token.subjectID,
			&token.subjectUsername, &token.appSlug, &token.reason, &token.sessionExpiresAt,
			&token.stopReason, &token.jti, &token.tokenExpiresAt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("expire prior support session: %w", err)
		}
		abandoned = append(abandoned, token)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("expire prior support session: %w", err)
	}
	for _, token := range abandoned {
		if token.jti.Valid && token.jti.String != "" && token.subjectID.Valid && token.tokenExpiresAt.Valid {
			if _, err := tx.Exec(`INSERT INTO revoked_tokens (jti, user_id, expires_at)
				VALUES (?, ?, ?) ON CONFLICT(jti) DO NOTHING`,
				token.jti.String, token.subjectID.Int64, token.tokenExpiresAt.Time.Unix()); err != nil {
				return fmt.Errorf("revoke abandoned support session: %w", err)
			}
		}
		detail, _ := json.Marshal(map[string]any{
			"actor_username": token.actorUsername, "subject_user_id": nullableInt64(token.subjectID),
			"subject_username": token.subjectUsername, "app_slug": token.appSlug,
			"reason": token.reason, "stop_reason": token.stopReason,
			"expires_at": token.sessionExpiresAt, "stopped_at": now,
		})
		if _, err := tx.Exec(`INSERT INTO audit_events
			(user_id, action, resource_type, resource_id, detail, ip_address)
			VALUES (?, 'support_session.stop', 'support_session', ?, ?, '')`,
			nullableInt64(token.actorID), token.id, string(detail)); err != nil {
			return fmt.Errorf("audit abandoned support session: %w", err)
		}
	}
	res, err := tx.Exec(`
		INSERT INTO support_sessions
			(id, actor_user_id, actor_username, actor_token_epoch,
			 subject_user_id, subject_username, subject_role, subject_token_epoch,
			 app_id, app_slug, app_slug_snapshot, reason, launch_code_hash, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		p.ID, p.ActorUserID, p.ActorUsername, p.ActorTokenEpoch,
		p.SubjectUserID, p.SubjectUsername, p.SubjectRole, p.SubjectTokenEpoch,
		p.AppID, p.AppSlug, p.AppSlug, p.Reason, p.LaunchCodeHash, p.ExpiresAt.UTC())
	if err != nil {
		return fmt.Errorf("create support session: %w", err)
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil || n != 1 {
		return ErrSupportSessionActive
	}
	if _, err := tx.Exec(`INSERT INTO audit_events
		(user_id, action, resource_type, resource_id, detail, ip_address)
		VALUES (?, 'support_session.start', 'support_session', ?, ?, ?)`,
		p.ActorUserID, p.ID, p.AuditDetail, p.IPAddress); err != nil {
		return fmt.Errorf("audit support session start: %w", err)
	}
	// The durable audit event is the long-term record; the capability table is
	// retained only long enough for operational investigation.
	_, _ = tx.Exec(`DELETE FROM support_sessions WHERE expires_at < ?`, now.Add(-supportSessionRetention))
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create support session: %w", err)
	}
	return nil
}

// consumeSupportLaunch atomically consumes the one-time capability and builds
// a dual-principal ContextUser. Both identities are live-resolved so a deleted,
// demoted, or newly privileged account fails closed before any cookie is set.
func (s *Store) consumeSupportLaunch(codeHash, appSlug string) (*auth.ContextUser, error) {
	row := s.db.QueryRow(`
		UPDATE support_sessions
		   SET launch_consumed_at = CURRENT_TIMESTAMP
		 WHERE launch_code_hash = ?
		   AND app_slug_snapshot = ?
		   AND launch_consumed_at IS NULL
		   AND stopped_at IS NULL
		   AND expires_at > ?
		   AND created_at >= `+s.d.nowMinusSeconds(60)+`
		 RETURNING id, actor_user_id, actor_token_epoch, subject_user_id,
		           subject_role, subject_token_epoch, app_id, app_slug_snapshot, expires_at`,
		codeHash, appSlug, time.Now().UTC())
	var (
		id                        string
		actorID, subjectID, appID sql.NullInt64
		actorEpoch, subjectEpoch  int64
		subjectRole               string
		slug                      string
		expiresAt                 time.Time
	)
	if err := row.Scan(&id, &actorID, &actorEpoch, &subjectID, &subjectRole, &subjectEpoch, &appID, &slug, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("consume support launch: %w", err)
	}
	if !actorID.Valid || !subjectID.Valid || !appID.Valid {
		return nil, ErrNotFound
	}
	actor, err := s.LookupContextUser(actorID.Int64)
	if err != nil || actor == nil || actor.Role != string(auth.RoleAdmin) || actor.IsServiceAccount() || actor.TokenEpoch != actorEpoch {
		return nil, ErrNotFound
	}
	subject, err := s.LookupContextUser(subjectID.Int64)
	if err != nil || subject == nil || subject.TokenEpoch != subjectEpoch || subject.IsServiceAccount() ||
		subject.Role != subjectRole || (subjectRole != string(auth.RoleViewer) && subjectRole != string(auth.RoleDeveloper)) {
		return nil, ErrNotFound
	}
	subject.SupportSession = &auth.SupportSessionContext{
		ID:              id,
		ActorID:         actor.ID,
		ActorUsername:   actor.Username,
		ActorTokenEpoch: actor.TokenEpoch,
		AppID:           appID.Int64,
		AppSlug:         slug,
		ExpiresAt:       expiresAt,
	}
	return subject, nil
}

// AbortSupportSession closes a launch before any cookie has been emitted. It
// also revokes a token when an activation write committed but returned an
// ambiguous transport error.
func (s *Store) AbortSupportSession(id, reason string) error {
	_, err := s.StopSupportSession(id, reason, "")
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("abort support session: %w", err)
	}
	return nil
}

// ObserveSupportSession records that the activated browser identity reached
// access middleware. Until this point an activation may have committed without
// a cookie reaching the browser, so creation may safely reap it after grace.
func (s *Store) ObserveSupportSession(id string) error {
	res, err := s.db.Exec(`UPDATE support_sessions
		SET first_used_at = CURRENT_TIMESTAMP
		WHERE id = ? AND first_used_at IS NULL AND stopped_at IS NULL AND expires_at > ?`, id, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("observe support session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return nil
	}
	var exists int
	if err := s.db.QueryRow(`SELECT 1 FROM support_sessions
		WHERE id = ? AND first_used_at IS NOT NULL AND stopped_at IS NULL AND expires_at > ?`, id, time.Now().UTC()).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("observe support session: %w", err)
	}
	return nil
}

// ActivateSupportSession binds the short-lived JWT to its durable session row.
// The JTI is what lets StopSupportSession terminate already-open WebSockets.
func (s *Store) ActivateSupportSession(id, jti string, expiresAt time.Time) error {
	res, err := s.db.Exec(`
		UPDATE support_sessions
		   SET token_jti = ?, token_expires_at = ?
		 WHERE id = ? AND launch_consumed_at IS NOT NULL
		   AND token_jti IS NULL AND stopped_at IS NULL
		   AND expires_at > ?`,
		jti, expiresAt.UTC(), id, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("activate support session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

// StopSupportSession ends a support session and revokes its token in one
// transaction. It is idempotent so retries from the injected banner are safe.
func (s *Store) StopSupportSession(id, reason, ipAddress string) (*SupportSession, error) {
	return s.stopSupportSession(id, nil, reason, ipAddress)
}

// StopActiveSupportSessionForActor ends only the current support identity
// owned by actorID. Keeping actor selection inside the update prevents a
// control-plane caller from turning a support-session identifier into an IDOR.
func (s *Store) StopActiveSupportSessionForActor(actorID int64, reason, ipAddress string) (*SupportSession, error) {
	return s.stopSupportSession("", &actorID, reason, ipAddress)
}

type supportSessionScanner interface {
	Scan(dest ...any) error
}

func scanSupportSession(row supportSessionScanner) (*SupportSession, error) {
	var (
		session                           SupportSession
		actorID, subjectID                sql.NullInt64
		tokenExpiry, stoppedAt, firstUsed sql.NullTime
	)
	if err := row.Scan(&session.ID, &actorID, &session.ActorUsername, &subjectID, &session.SubjectUsername,
		&session.AppSlug, &session.Reason, &session.TokenJTI, &tokenExpiry,
		&session.CreatedAt, &session.ExpiresAt, &stoppedAt, &session.StopReason, &firstUsed); err != nil {
		return nil, err
	}
	if firstUsed.Valid {
		v := firstUsed.Time
		session.FirstUsedAt = &v
	}
	if actorID.Valid {
		v := actorID.Int64
		session.ActorUserID = &v
	}
	if subjectID.Valid {
		v := subjectID.Int64
		session.SubjectUserID = &v
	}
	if tokenExpiry.Valid {
		v := tokenExpiry.Time
		session.TokenExpiresAt = &v
	}
	if stoppedAt.Valid {
		v := stoppedAt.Time
		session.StoppedAt = &v
	}
	return &session, nil
}

func (s *Store) stopSupportSession(id string, actorID *int64, reason, ipAddress string) (*SupportSession, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var row *sql.Row
	if actorID != nil {
		row = tx.QueryRow(`
			UPDATE support_sessions
			   SET stopped_at = CURRENT_TIMESTAMP, stop_reason = ?
			 WHERE actor_user_id = ? AND stopped_at IS NULL AND expires_at > ?
			 RETURNING id, actor_user_id, actor_username, subject_user_id, subject_username,
			       app_slug_snapshot, reason, COALESCE(token_jti, ''), token_expires_at,
			       created_at, expires_at, stopped_at, stop_reason, first_used_at`, reason, *actorID, time.Now().UTC())
	} else {
		row = tx.QueryRow(`
			UPDATE support_sessions
			   SET stopped_at = CURRENT_TIMESTAMP, stop_reason = ?
			 WHERE id = ? AND stopped_at IS NULL
			 RETURNING id, actor_user_id, actor_username, subject_user_id, subject_username,
			       app_slug_snapshot, reason, COALESCE(token_jti, ''), token_expires_at,
			       created_at, expires_at, stopped_at, stop_reason, first_used_at`, reason, id)
	}
	session, scanErr := scanSupportSession(row)
	won := scanErr == nil
	if errors.Is(scanErr, sql.ErrNoRows) {
		if actorID != nil {
			return nil, ErrNotFound
		}
		won = false
		row = tx.QueryRow(`SELECT id, actor_user_id, actor_username, subject_user_id, subject_username,
		       app_slug_snapshot, reason, COALESCE(token_jti, ''), token_expires_at,
		       created_at, expires_at, stopped_at, stop_reason, first_used_at
		  FROM support_sessions WHERE id = ?`, id)
		session, scanErr = scanSupportSession(row)
		if errors.Is(scanErr, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
	}
	if scanErr != nil {
		return nil, fmt.Errorf("get support session: %w", scanErr)
	}
	if won && session.TokenJTI != "" && session.TokenExpiresAt != nil && session.SubjectUserID != nil {
		if _, err := tx.Exec(`INSERT INTO revoked_tokens (jti, user_id, expires_at)
			VALUES (?, ?, ?) ON CONFLICT(jti) DO NOTHING`,
			session.TokenJTI, *session.SubjectUserID, session.TokenExpiresAt.Unix()); err != nil {
			return nil, fmt.Errorf("revoke support session token: %w", err)
		}
	}
	if won {
		detail, _ := json.Marshal(map[string]any{
			"actor_username": session.ActorUsername, "subject_user_id": session.SubjectUserID,
			"subject_username": session.SubjectUsername, "app_slug": session.AppSlug,
			"reason": session.Reason, "stop_reason": session.StopReason,
			"expires_at": session.ExpiresAt, "stopped_at": time.Now().UTC(),
		})
		if _, err := tx.Exec(`INSERT INTO audit_events
			(user_id, action, resource_type, resource_id, detail, ip_address)
			VALUES (?, 'support_session.stop', 'support_session', ?, ?, ?)`,
			session.ActorUserID, session.ID, string(detail), ipAddress); err != nil {
			return nil, fmt.Errorf("audit support session stop: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	session.NewlyStopped = won
	return session, nil
}
