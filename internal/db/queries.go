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

// ListUsers returns all users ordered by username.
func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(
		`SELECT id, username, password_hash, role, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, &u)
	}
	if users == nil {
		users = []*User{}
	}
	return users, rows.Err()
}

// UpdateUserRole changes the role of a user identified by ID.
// Returns ErrNotFound if no user with that ID exists.
func (s *Store) UpdateUserRole(id int64, role string) error {
	result, err := s.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, id)
	if err != nil {
		return fmt.Errorf("update user role: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update user role rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUser permanently removes a user and all their associated data
// (FK cascades handle oauth_accounts and api_keys).
// Returns ErrNotFound if no user with that ID exists.
func (s *Store) DeleteUser(id int64) error {
	result, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete user rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
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

// APIKeyInfo is a safe view of an api_keys row — no key_hash exposed.
type APIKeyInfo struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// ListAPIKeys returns all tokens owned by userID, newest first.
func (s *Store) ListAPIKeys(userID int64) ([]APIKeyInfo, error) {
	rows, err := s.db.Query(
		`SELECT id, name, created_at FROM api_keys WHERE user_id = ? ORDER BY created_at DESC`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []APIKeyInfo{}
	for rows.Next() {
		var k APIKeyInfo
		if err := rows.Scan(&k.ID, &k.Name, &k.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// DeleteAPIKey deletes a token by ID.
// For non-admin callers pass ownerID = the caller's user ID; the DELETE is
// scoped to that user so they cannot delete other users' tokens.
// For admin callers pass ownerID = 0 to bypass the ownership check.
// Returns ErrNotFound if no matching row is deleted.
func (s *Store) DeleteAPIKey(id int64, ownerID int64) error {
	var result sql.Result
	var err error
	if ownerID == 0 {
		result, err = s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	} else {
		result, err = s.db.Exec(`DELETE FROM api_keys WHERE id = ? AND user_id = ?`, id, ownerID)
	}
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete api key rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// APIKeyNameExists returns true if the user already has a token with the given name.
func (s *Store) APIKeyNameExists(userID int64, name string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM api_keys WHERE user_id = ? AND name = ?`, userID, name).Scan(&count)
	return count > 0, err
}

// --- Apps ---

type App struct {
	ID                      int64     `json:"id"`
	Slug                    string    `json:"slug"`
	Name                    string    `json:"name"`
	ProjectSlug             string    `json:"project_slug,omitempty"`
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
	if p.ProjectSlug == "" {
		_, err := s.db.Exec(
			`INSERT INTO apps (slug, name, owner_id) VALUES (?, ?, ?)`,
			p.Slug, p.Name, p.OwnerID,
		)
		if err != nil {
			return fmt.Errorf("create app: %w", err)
		}
		return nil
	}
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

func (s *Store) ListApps(limit, offset int) ([]*App, error) {
	if limit <= 0 {
		limit = -1 // SQLite treats -1 as no limit
	}
	rows, err := s.db.Query(`
		SELECT id, slug, name, project_slug, owner_id, access, status,
		       current_port, current_pid, deploy_count, hibernate_timeout_minutes,
		       created_at, updated_at
		FROM apps ORDER BY created_at DESC
		LIMIT ? OFFSET ?`, limit, offset)
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

func (s *Store) ListAppsVisibleToUser(userID int64, limit, offset int) ([]*App, error) {
	if limit <= 0 {
		limit = -1 // SQLite treats -1 as no limit
	}
	rows, err := s.db.Query(`
		SELECT id, slug, name, project_slug, owner_id, access, status,
		       current_port, current_pid, deploy_count, hibernate_timeout_minutes,
		       created_at, updated_at
		FROM apps
		WHERE access = 'public'
		   OR access = 'shared'
		   OR owner_id = ?
		   OR EXISTS (
		       SELECT 1 FROM app_members
		       WHERE app_slug = apps.slug AND user_id = ?
		   )
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`, userID, userID, limit, offset)
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

// UpdateAppName sets the display name for the app identified by slug.
// Returns ErrNotFound if no app with the given slug exists.
func (s *Store) UpdateAppName(slug, name string) error {
	result, err := s.db.Exec(
		`UPDATE apps SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`,
		name, slug,
	)
	if err != nil {
		return fmt.Errorf("update app name: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update app name rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateAppProjectSlug sets the project_slug field for the app identified by
// slug. Pass an empty string to clear the association.
// Returns ErrNotFound if no app with the given slug exists.
func (s *Store) UpdateAppProjectSlug(slug, projectSlug string) error {
	result, err := s.db.Exec(
		`UPDATE apps SET project_slug = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`,
		projectSlug, slug,
	)
	if err != nil {
		return fmt.Errorf("update app project_slug: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update app project_slug rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteApp permanently removes the app and all its associated data.
// FK cascades in the schema remove app_members and deployments automatically.
// Returns ErrNotFound if no app with the given slug exists.
func (s *Store) DeleteApp(slug string) error {
	result, err := s.db.Exec(`DELETE FROM apps WHERE slug = ?`, slug)
	if err != nil {
		return fmt.Errorf("delete app: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete app rows: %w", err)
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

// ListDeploymentsBySlug returns deployments for the app identified by slug,
// ordered newest first. It is a slug-based counterpart to ListDeployments.
func (s *Store) ListDeploymentsBySlug(slug string) ([]DeploymentSummary, error) {
	rows, err := s.db.Query(`
		SELECT d.id, d.version, d.status, d.created_at
		FROM deployments d
		JOIN apps a ON a.id = d.app_id
		WHERE a.slug = ?
		ORDER BY d.created_at DESC, d.id DESC`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]DeploymentSummary, 0)
	for rows.Next() {
		var d DeploymentSummary
		if err := rows.Scan(&d.ID, &d.Version, &d.Status, &d.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// DeploymentSummary is a public view of a deployment row, safe for API responses.
type DeploymentSummary struct {
	ID        int64     `json:"id"`
	Version   string    `json:"version"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
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

// AppMember represents a user explicitly granted access to an app.
type AppMember struct {
	UserID   int64
	Username string
	Role     string
}

func (s *Store) GrantAppAccess(slug string, userID int64) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO app_members (app_slug, user_id) VALUES (?, ?)`, slug, userID)
	if err != nil {
		return fmt.Errorf("grant app access: %w", err)
	}
	return nil
}

func (s *Store) RevokeAppAccess(slug string, userID int64) error {
	result, err := s.db.Exec(
		`DELETE FROM app_members WHERE app_slug = ? AND user_id = ?`, slug, userID)
	if err != nil {
		return fmt.Errorf("revoke app access: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke app access rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetAppMembers returns all users explicitly granted access to slug,
// including their username and role.
func (s *Store) GetAppMembers(slug string) ([]AppMember, error) {
	rows, err := s.db.Query(`
		SELECT am.user_id, u.username, am.role
		FROM app_members am
		JOIN users u ON u.id = am.user_id
		WHERE am.app_slug = ?
		ORDER BY u.username`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []AppMember{}
	for rows.Next() {
		var m AppMember
		if err := rows.Scan(&m.UserID, &m.Username, &m.Role); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// ListAppMembers returns explicitly-granted members of slug with optional
// pagination. Pass limit=0 to return all members.
func (s *Store) ListAppMembers(slug string, limit, offset int) ([]AppMember, error) {
	if limit <= 0 {
		limit = -1 // SQLite treats -1 as no limit
	}
	rows, err := s.db.Query(`
		SELECT am.user_id, u.username, am.role
		FROM app_members am
		JOIN users u ON u.id = am.user_id
		WHERE am.app_slug = ?
		ORDER BY u.username
		LIMIT ? OFFSET ?`, slug, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []AppMember{}
	for rows.Next() {
		var m AppMember
		if err := rows.Scan(&m.UserID, &m.Username, &m.Role); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
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

// GetMemberRole returns the role of userID in app_members for the given slug.
// Returns ErrNotFound if the user is not an explicit member of the app.
func (s *Store) GetMemberRole(slug string, userID int64) (string, error) {
	row := s.db.QueryRow(
		`SELECT role FROM app_members WHERE app_slug = ? AND user_id = ?`, slug, userID)
	var role string
	if err := row.Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return role, nil
}

// SetMemberRole updates the role of an explicit app member. Intended primarily
// for testing and admin use.
func (s *Store) SetMemberRole(slug string, userID int64, role string) error {
	_, err := s.db.Exec(
		`UPDATE app_members SET role = ? WHERE app_slug = ? AND user_id = ?`,
		role, slug, userID)
	if err != nil {
		return fmt.Errorf("set member role: %w", err)
	}
	return nil
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

// --- OAuth Accounts ---

type OAuthAccount struct {
	ID         int64
	UserID     int64
	Provider   string
	ProviderID string
	CreatedAt  time.Time
}

type CreateOAuthAccountParams struct {
	UserID     int64
	Provider   string
	ProviderID string
}

func (s *Store) CreateOAuthAccount(p CreateOAuthAccountParams) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO oauth_accounts (user_id, provider, provider_id) VALUES (?, ?, ?)`,
		p.UserID, p.Provider, p.ProviderID,
	)
	if err != nil {
		return fmt.Errorf("create oauth account: %w", err)
	}
	return nil
}

func (s *Store) GetUserByOAuthAccount(provider, providerID string) (*User, error) {
	row := s.db.QueryRow(`
		SELECT u.id, u.username, u.password_hash, u.role, u.created_at
		FROM users u
		JOIN oauth_accounts o ON o.user_id = u.id
		WHERE o.provider = ? AND o.provider_id = ?`, provider, providerID)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// --- OAuth State (CSRF nonce) ---

func (s *Store) CreateOAuthState(state string) error {
	_, err := s.db.Exec(`INSERT INTO oauth_states (state) VALUES (?)`, state)
	if err != nil {
		return fmt.Errorf("create oauth state: %w", err)
	}
	return nil
}

// ConsumeOAuthState validates the state nonce and deletes it (one-time use).
// Returns an error if the state does not exist.
func (s *Store) ConsumeOAuthState(state string) error {
	res, err := s.db.Exec(`DELETE FROM oauth_states WHERE state = ?`, state)
	if err != nil {
		return fmt.Errorf("consume oauth state: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("oauth state not found or already used")
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
