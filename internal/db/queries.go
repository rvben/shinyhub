package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
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

// UpdateUserPassword sets a new password hash for the user identified by ID.
// Returns ErrNotFound if no user with that ID exists.
func (s *Store) UpdateUserPassword(id int64, hash string) error {
	result, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return fmt.Errorf("update user password: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update user password rows: %w", err)
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
	Replicas                int       `json:"replicas"`
	MaxSessionsPerReplica   int       `json:"max_sessions_per_replica"`
	DeployCount             int       `json:"deploy_count"`
	HibernateTimeoutMinutes *int      `json:"hibernate_timeout_minutes"`
	MemoryLimitMB           *int      `json:"memory_limit_mb"`
	CPUQuotaPercent         *int      `json:"cpu_quota_percent"`
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
		       replicas, max_sessions_per_replica, deploy_count,
		       hibernate_timeout_minutes,
		       memory_limit_mb, cpu_quota_percent,
		       created_at, updated_at
		FROM apps WHERE slug = ?`, slug)
	return scanApp(row)
}

// GetApp is an alias for GetAppBySlug.
func (s *Store) GetApp(slug string) (*App, error) {
	return s.GetAppBySlug(slug)
}

func (s *Store) GetAppByID(id int64) (*App, error) {
	row := s.db.QueryRow(`
		SELECT id, slug, name, project_slug, owner_id, access, status,
		       replicas, max_sessions_per_replica, deploy_count,
		       hibernate_timeout_minutes,
		       memory_limit_mb, cpu_quota_percent,
		       created_at, updated_at
		FROM apps WHERE id = ?`, id)
	return scanApp(row)
}

func (s *Store) ListApps(limit, offset int) ([]*App, error) {
	if limit <= 0 {
		limit = -1 // SQLite treats -1 as no limit
	}
	rows, err := s.db.Query(`
		SELECT id, slug, name, project_slug, owner_id, access, status,
		       replicas, max_sessions_per_replica, deploy_count,
		       hibernate_timeout_minutes,
		       memory_limit_mb, cpu_quota_percent,
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

// ListRunningApps returns all apps whose status is 'running'. Used on startup
// to re-adopt processes that survived a server restart.
func (s *Store) ListRunningApps() ([]*App, error) {
	rows, err := s.db.Query(`
		SELECT id, slug, name, project_slug, owner_id, access, status,
		       replicas, max_sessions_per_replica, deploy_count,
		       hibernate_timeout_minutes,
		       memory_limit_mb, cpu_quota_percent,
		       created_at, updated_at
		FROM apps WHERE status = 'running'`)
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
		       replicas, max_sessions_per_replica, deploy_count,
		       hibernate_timeout_minutes,
		       memory_limit_mb, cpu_quota_percent,
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
}

func (s *Store) UpdateAppStatus(p UpdateAppStatusParams) error {
	res, err := s.db.Exec(
		`UPDATE apps SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`,
		p.Status, p.Slug,
	)
	if err != nil {
		return fmt.Errorf("update app status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
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

// UpdateResourceLimitsParams holds the resource limit values to set.
// Nil means "inherit from global config" (stored as NULL in the DB).
type UpdateResourceLimitsParams struct {
	Slug            string
	MemoryLimitMB   *int
	CPUQuotaPercent *int
}

// UpdateResourceLimits sets the per-app resource limits. NULL means inherit global default.
// Returns ErrNotFound if no app with the given slug exists.
func (s *Store) UpdateResourceLimits(p UpdateResourceLimitsParams) error {
	result, err := s.db.Exec(
		`UPDATE apps SET memory_limit_mb = ?, cpu_quota_percent = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE slug = ?`,
		p.MemoryLimitMB, p.CPUQuotaPercent, p.Slug,
	)
	if err != nil {
		return fmt.Errorf("update resource limits: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update resource limits rows: %w", err)
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
	// Status records the terminal outcome of the deploy attempt. Callers pass
	// "succeeded" or "failed". Empty defaults to "pending" — only appropriate
	// for callers that intend to UpdateDeploymentStatus afterwards.
	Status string
}

func (s *Store) CreateDeployment(p CreateDeploymentParams) (*Deployment, error) {
	status := p.Status
	if status == "" {
		status = "pending"
	}
	res, err := s.db.Exec(
		`INSERT INTO deployments (app_id, version, bundle_dir, status) VALUES (?, ?, ?, ?)`,
		p.AppID, p.Version, p.BundleDir, status,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return &Deployment{ID: id, AppID: p.AppID, Version: p.Version, BundleDir: p.BundleDir, Status: status}, nil
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

// GetDeploymentBySlugAndID fetches a single deployment by its ID, verified
// to belong to the app identified by slug. Returns ErrNotFound if the
// deployment does not exist or belongs to a different app.
func (s *Store) GetDeploymentBySlugAndID(slug string, id int64) (*Deployment, error) {
	row := s.db.QueryRow(`
		SELECT d.id, d.app_id, d.version, d.bundle_dir, d.status, d.created_at
		FROM deployments d
		JOIN apps a ON a.id = d.app_id
		WHERE d.id = ? AND a.slug = ?`, id, slug)
	var dep Deployment
	if err := row.Scan(&dep.ID, &dep.AppID, &dep.Version, &dep.BundleDir, &dep.Status, &dep.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &dep, nil
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
// Returns an error if the state does not exist or has expired (>10 minutes old).
// Also sweeps all expired states to prevent unbounded table growth.
func (s *Store) ConsumeOAuthState(state string) error {
	// Sweep stale nonces — ignore errors; this is best-effort cleanup.
	s.db.Exec(`DELETE FROM oauth_states WHERE created_at < datetime('now', '-10 minutes')`) //nolint:errcheck
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

// --- Audit Events ---

// Audit action constants. Most actions in the codebase are still raw string
// literals; new audit producers should prefer constants so handlers and tests
// can reference the same identifier.
const (
	AuditDataPush   = "data.push"
	AuditDataDelete = "data.delete"
)

// AuditEventParams holds the fields for a new audit event.
// UserID is a pointer because some actions (login_failed) have no authenticated user.
type AuditEventParams struct {
	UserID       *int64
	Action       string
	ResourceType string
	ResourceID   string
	Detail       string
	IPAddress    string
}

// AuditEvent is a row from the audit_events table.
type AuditEvent struct {
	ID           int64     `json:"id"`
	UserID       *int64    `json:"user_id,omitempty"`
	Username     *string   `json:"username,omitempty"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Detail       string    `json:"detail"`
	IPAddress    string    `json:"ip_address"`
	CreatedAt    time.Time `json:"created_at"`
}

// LogAuditEvent inserts an audit event. Errors are logged to stderr but do
// not fail the caller — audit recording must never break normal operation.
func (s *Store) LogAuditEvent(p AuditEventParams) {
	_, err := s.db.Exec(`
		INSERT INTO audit_events (user_id, action, resource_type, resource_id, detail, ip_address)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.UserID, p.Action, p.ResourceType, p.ResourceID, p.Detail, p.IPAddress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit log: %v\n", err)
	}
}

// CountAuditEvents returns the total number of rows in audit_events. Used by
// the API handler to compute has_more for pagination — without a total the UI
// can only guess and ends up disabling Next/Prev when more rows exist.
func (s *Store) CountAuditEvents() (int64, error) {
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count audit events: %w", err)
	}
	return n, nil
}

// ListAuditEvents returns audit events ordered newest-first with pagination.
// Each event includes the username of the acting user via a LEFT JOIN on users,
// so anonymous events (no user_id) are still returned with a nil Username.
func (s *Store) ListAuditEvents(limit, offset int) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT ae.id, ae.user_id, u.username,
		       ae.action, ae.resource_type, ae.resource_id,
		       ae.detail, ae.ip_address, ae.created_at
		FROM audit_events ae
		LEFT JOIN users u ON u.id = ae.user_id
		ORDER BY ae.created_at DESC, ae.id DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AuditEvent, 0)
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(
			&e.ID, &e.UserID, &e.Username,
			&e.Action, &e.ResourceType, &e.ResourceID,
			&e.Detail, &e.IPAddress, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// --- App Environment Variables ---

// AppEnvVar represents a per-app environment variable row.
// Secret values are stored encrypted; callers are responsible for
// encrypting before Upsert and decrypting after Get/List.
type AppEnvVar struct {
	ID        int64
	AppID     int64
	Key       string
	Value     []byte
	IsSecret  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UpsertAppEnvVar inserts or updates an env var for the given app.
// On key conflict, value, is_secret, and updated_at are replaced.
func (s *Store) UpsertAppEnvVar(appID int64, key string, value []byte, isSecret bool) error {
	_, err := s.db.Exec(`
		INSERT INTO app_env_vars (app_id, key, value, is_secret, updated_at)
		VALUES (?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(app_id, key) DO UPDATE SET
			value      = excluded.value,
			is_secret  = excluded.is_secret,
			updated_at = strftime('%s','now')`,
		appID, key, value, boolToInt(isSecret))
	return err
}

// GetAppEnvVar fetches a single env var by app ID and key.
// Returns sql.ErrNoRows if no matching row exists.
func (s *Store) GetAppEnvVar(appID int64, key string) (*AppEnvVar, error) {
	var v AppEnvVar
	var isSecretInt int
	var createdAt, updatedAt int64
	err := s.db.QueryRow(`
		SELECT id, app_id, key, value, is_secret, created_at, updated_at
		FROM app_env_vars
		WHERE app_id = ? AND key = ?`, appID, key).Scan(
		&v.ID, &v.AppID, &v.Key, &v.Value, &isSecretInt, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	v.IsSecret = isSecretInt != 0
	v.CreatedAt = time.Unix(createdAt, 0)
	v.UpdatedAt = time.Unix(updatedAt, 0)
	return &v, nil
}

// ListAppEnvVars returns all env vars for the given app, ordered by key.
func (s *Store) ListAppEnvVars(appID int64) ([]AppEnvVar, error) {
	rows, err := s.db.Query(`
		SELECT id, app_id, key, value, is_secret, created_at, updated_at
		FROM app_env_vars
		WHERE app_id = ?
		ORDER BY key`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AppEnvVar, 0)
	for rows.Next() {
		var v AppEnvVar
		var isSecretInt int
		var createdAt, updatedAt int64
		if err := rows.Scan(&v.ID, &v.AppID, &v.Key, &v.Value, &isSecretInt, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		v.IsSecret = isSecretInt != 0
		v.CreatedAt = time.Unix(createdAt, 0)
		v.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteAppEnvVar removes an env var by app ID and key.
// Returns sql.ErrNoRows if no matching row exists.
func (s *Store) DeleteAppEnvVar(appID int64, key string) error {
	res, err := s.db.Exec(`DELETE FROM app_env_vars WHERE app_id = ? AND key = ?`, appID, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CountAppEnvVars returns the number of env vars set for the given app.
func (s *Store) CountAppEnvVars(appID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM app_env_vars WHERE app_id = ?`, appID).Scan(&n)
	return n, err
}

// --- Replicas ---

// Replica represents a single backend process instance for an app.
// Multiple replicas allow an app to run N parallel processes behind the proxy.
type Replica struct {
	AppID     int64  `json:"app_id"`
	Index     int    `json:"index"`
	PID       *int   `json:"pid,omitempty"`
	Port      *int   `json:"port,omitempty"`
	Status    string `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UpsertReplicaParams holds the fields for inserting or updating a replica row.
type UpsertReplicaParams struct {
	AppID  int64
	Index  int
	PID    *int
	Port   *int
	Status string
}

// UpsertReplica inserts a new replica or updates an existing one identified by
// (app_id, idx). All fields are replaced on conflict.
func (s *Store) UpsertReplica(p UpsertReplicaParams) error {
	_, err := s.db.Exec(`
		INSERT INTO replicas (app_id, idx, pid, port, status, updated_at)
		VALUES (?, ?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(app_id, idx) DO UPDATE SET
			pid        = excluded.pid,
			port       = excluded.port,
			status     = excluded.status,
			updated_at = excluded.updated_at`,
		p.AppID, p.Index, p.PID, p.Port, p.Status,
	)
	if err != nil {
		return fmt.Errorf("upsert replica: %w", err)
	}
	return nil
}

// ListReplicas returns all replicas for the given app, ordered by index.
// Returns an empty (non-nil) slice when no replicas exist.
func (s *Store) ListReplicas(appID int64) ([]*Replica, error) {
	rows, err := s.db.Query(`
		SELECT app_id, idx, pid, port, status, updated_at
		FROM replicas WHERE app_id = ? ORDER BY idx`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Replica{}
	for rows.Next() {
		var r Replica
		var updatedAt int64
		if err := rows.Scan(&r.AppID, &r.Index, &r.PID, &r.Port, &r.Status, &updatedAt); err != nil {
			return nil, err
		}
		r.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// DeleteReplica removes the replica at the given index for an app.
func (s *Store) DeleteReplica(appID int64, index int) error {
	_, err := s.db.Exec(`DELETE FROM replicas WHERE app_id = ? AND idx = ?`, appID, index)
	if err != nil {
		return fmt.Errorf("delete replica: %w", err)
	}
	return nil
}

// DeleteReplicasAbove removes all replicas with idx >= keepBelow for an app.
// Used when the operator shrinks the pool.
func (s *Store) DeleteReplicasAbove(appID int64, keepBelow int) error {
	_, err := s.db.Exec(`DELETE FROM replicas WHERE app_id = ? AND idx >= ?`, appID, keepBelow)
	if err != nil {
		return fmt.Errorf("delete replicas above: %w", err)
	}
	return nil
}

// UpdateAppReplicas sets the target replica count for an app.
func (s *Store) UpdateAppReplicas(appID int64, n int) error {
	res, err := s.db.Exec(
		`UPDATE apps SET replicas = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		n, appID,
	)
	if err != nil {
		return fmt.Errorf("update replicas: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateAppMaxSessionsPerReplica sets the per-replica session cap. A value of
// 0 means "use the runtime-wide default".
func (s *Store) UpdateAppMaxSessionsPerReplica(appID int64, n int) error {
	res, err := s.db.Exec(
		`UPDATE apps SET max_sessions_per_replica = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		n, appID,
	)
	if err != nil {
		return fmt.Errorf("update max_sessions_per_replica: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// boolToInt converts a bool to the integer representation used in SQLite.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// scanner interface satisfied by both *sql.Row and *sql.Rows
type scanner interface {
	Scan(dest ...any) error
}

func scanApp(s scanner) (*App, error) {
	var a App
	var projectSlug sql.NullString
	err := s.Scan(
		&a.ID, &a.Slug, &a.Name, &projectSlug, &a.OwnerID, &a.Access,
		&a.Status, &a.Replicas, &a.MaxSessionsPerReplica, &a.DeployCount,
		&a.HibernateTimeoutMinutes, &a.MemoryLimitMB, &a.CPUQuotaPercent,
		&a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if projectSlug.Valid {
		a.ProjectSlug = projectSlug.String
	}
	return &a, nil
}
