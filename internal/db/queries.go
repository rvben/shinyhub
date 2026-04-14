package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrNotFound = errors.New("not found")

// --- Users ---

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
}

type CreateUserParams struct {
	Username     string
	PasswordHash string
	Role         string
}

func (s *Store) CreateUser(p CreateUserParams) error {
	_, err := s.db.Exec(
		`INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?)`,
		p.Username, p.PasswordHash, p.Role,
	)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (s *Store) GetUserByUsername(username string) (*User, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, role, created_at FROM users WHERE username = ?`,
		username,
	)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) GetUserByID(id int64) (*User, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, role, created_at FROM users WHERE id = ?`,
		id,
	)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// --- API Keys ---

type CreateAPIKeyParams struct {
	UserID  int64
	KeyHash string
	Name    string
}

func (s *Store) CreateAPIKey(p CreateAPIKeyParams) error {
	_, err := s.db.Exec(
		`INSERT INTO api_keys (user_id, key_hash, name) VALUES (?, ?, ?)`,
		p.UserID, p.KeyHash, p.Name,
	)
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}
	return nil
}

func (s *Store) GetUserByAPIKeyHash(hash string) (*User, error) {
	row := s.db.QueryRow(`
		SELECT u.id, u.username, u.password_hash, u.role, u.created_at
		FROM users u JOIN api_keys k ON k.user_id = u.id
		WHERE k.key_hash = ?`, hash)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// --- Apps ---

type App struct {
	ID                      int64     `json:"id"`
	Slug                    string    `json:"slug"`
	Name                    string    `json:"name"`
	ProjectSlug             string    `json:"project_slug"`
	OwnerID                 int64     `json:"owner_id"`
	Access                  string    `json:"access"`
	Status                  string    `json:"status"`
	CurrentPort             *int      `json:"port,omitempty"`
	CurrentPID              *int      `json:"pid,omitempty"`
	DeployCount             int       `json:"deploy_count"`
	HibernateTimeoutMinutes *int      `json:"hibernate_timeout_minutes,omitempty"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type CreateAppParams struct {
	Slug        string
	Name        string
	ProjectSlug string
	OwnerID     int64
}

func (s *Store) CreateApp(p CreateAppParams) error {
	_, err := s.db.Exec(
		`INSERT INTO apps (slug, name, project_slug, owner_id) VALUES (?, ?, ?, ?)`,
		p.Slug, p.Name, p.ProjectSlug, p.OwnerID,
	)
	if err != nil {
		return fmt.Errorf("create app: %w", err)
	}
	return nil
}

func (s *Store) GetAppBySlug(slug string) (*App, error) {
	row := s.db.QueryRow(`
		SELECT id, slug, name, project_slug, owner_id, access, status,
		       current_port, current_pid, deploy_count, hibernate_timeout_minutes,
		       created_at, updated_at
		FROM apps WHERE slug = ?`, slug)
	return scanApp(row)
}

func (s *Store) ListApps() ([]*App, error) {
	rows, err := s.db.Query(`
		SELECT id, slug, name, project_slug, owner_id, access, status,
		       current_port, current_pid, deploy_count, hibernate_timeout_minutes,
		       created_at, updated_at
		FROM apps ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []*App
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

type UpdateAppStatusParams struct {
	Slug   string
	Status string
	Port   *int
	PID    *int
}

func (s *Store) UpdateAppStatus(p UpdateAppStatusParams) error {
	_, err := s.db.Exec(`
		UPDATE apps SET status = ?, current_port = ?, current_pid = ?, updated_at = CURRENT_TIMESTAMP
		WHERE slug = ?`, p.Status, p.Port, p.PID, p.Slug)
	if err != nil {
		return fmt.Errorf("update app status: %w", err)
	}
	return nil
}

func (s *Store) IncrementDeployCount(slug string) error {
	_, err := s.db.Exec(`UPDATE apps SET deploy_count = deploy_count + 1 WHERE slug = ?`, slug)
	if err != nil {
		return fmt.Errorf("increment deploy count: %w", err)
	}
	return nil
}

// UpdateHibernateTimeout sets the per-app idle timeout in minutes.
// Pass nil to store SQL NULL (means "use the global config default").
// Pass 0 to disable hibernation for this app specifically.
func (s *Store) UpdateHibernateTimeout(slug string, minutes *int) error {
	result, err := s.db.Exec(
		`UPDATE apps SET hibernate_timeout_minutes = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`,
		minutes, slug,
	)
	if err != nil {
		return fmt.Errorf("update hibernate timeout: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update hibernate timeout rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Deployments ---

type Deployment struct {
	ID        int64
	AppID     int64
	Version   string
	BundleDir string
	Status    string
	CreatedAt time.Time
}

type CreateDeploymentParams struct {
	AppID     int64
	Version   string
	BundleDir string
}

func (s *Store) CreateDeployment(p CreateDeploymentParams) (*Deployment, error) {
	res, err := s.db.Exec(
		`INSERT INTO deployments (app_id, version, bundle_dir) VALUES (?, ?, ?)`,
		p.AppID, p.Version, p.BundleDir,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return &Deployment{ID: id, AppID: p.AppID, Version: p.Version, BundleDir: p.BundleDir, Status: "pending"}, nil
}

func (s *Store) UpdateDeploymentStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE deployments SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("update deployment status: %w", err)
	}
	return nil
}

func (s *Store) ListDeployments(appID int64) ([]*Deployment, error) {
	rows, err := s.db.Query(`
		SELECT id, app_id, version, bundle_dir, status, created_at
		FROM deployments WHERE app_id = ? ORDER BY id DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ds []*Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.AppID, &d.Version, &d.BundleDir, &d.Status, &d.CreatedAt); err != nil {
			return nil, err
		}
		ds = append(ds, &d)
	}
	return ds, rows.Err()
}

// --- App Members ---

func (s *Store) GrantAppAccess(slug string, userID int64) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO app_members (app_slug, user_id) VALUES (?, ?)`, slug, userID)
	if err != nil {
		return fmt.Errorf("grant app access: %w", err)
	}
	return nil
}

func (s *Store) RevokeAppAccess(slug string, userID int64) error {
	_, err := s.db.Exec(
		`DELETE FROM app_members WHERE app_slug = ? AND user_id = ?`, slug, userID)
	if err != nil {
		return fmt.Errorf("revoke app access: %w", err)
	}
	return nil
}

// GetAppMembers returns the IDs of all users explicitly granted access to slug.
func (s *Store) GetAppMembers(slug string) ([]int64, error) {
	rows, err := s.db.Query(`SELECT user_id FROM app_members WHERE app_slug = ?`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UserCanAccessApp returns true if userID is the app's owner or has been
// explicitly granted access via app_members.
func (s *Store) UserCanAccessApp(slug string, userID int64) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT 1 FROM apps WHERE slug = ? AND owner_id = ?
			UNION ALL
			SELECT 1 FROM app_members WHERE app_slug = ? AND user_id = ?
		)`, slug, userID, slug, userID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// SetAppAccess updates the access level for an app.
// Valid values: "public", "private", "shared".
// Returns ErrNotFound if no app with the given slug exists.
func (s *Store) SetAppAccess(slug, access string) error {
	result, err := s.db.Exec(
		`UPDATE apps SET access = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`, access, slug)
	if err != nil {
		return fmt.Errorf("set app access: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set app access rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanner interface satisfied by both *sql.Row and *sql.Rows
type scanner interface {
	Scan(dest ...any) error
}

func scanApp(s scanner) (*App, error) {
	var a App
	if err := s.Scan(
		&a.ID, &a.Slug, &a.Name, &a.ProjectSlug, &a.OwnerID, &a.Access,
		&a.Status, &a.CurrentPort, &a.CurrentPID, &a.DeployCount,
		&a.HibernateTimeoutMinutes,
		&a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}
