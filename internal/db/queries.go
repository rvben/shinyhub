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
	ID          int64
	Slug        string
	Name        string
	ProjectSlug string
	OwnerID     int64
	Access      string
	Status      string
	CurrentPort *int
	CurrentPID  *int
	DeployCount int
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
		       current_port, current_pid, deploy_count, created_at, updated_at
		FROM apps WHERE slug = ?`, slug)
	return scanApp(row)
}

func (s *Store) ListApps() ([]*App, error) {
	rows, err := s.db.Query(`
		SELECT id, slug, name, project_slug, owner_id, access, status,
		       current_port, current_pid, deploy_count, created_at, updated_at
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
		FROM deployments WHERE app_id = ? ORDER BY created_at DESC`, appID)
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

// scanner interface satisfied by both *sql.Row and *sql.Rows
type scanner interface {
	Scan(dest ...any) error
}

func scanApp(s scanner) (*App, error) {
	var a App
	if err := s.Scan(
		&a.ID, &a.Slug, &a.Name, &a.ProjectSlug, &a.OwnerID, &a.Access,
		&a.Status, &a.CurrentPort, &a.CurrentPID, &a.DeployCount,
		&a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}
