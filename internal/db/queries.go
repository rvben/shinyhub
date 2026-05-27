package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

var ErrNotFound = errors.New("not found")

// ValidAppVisibilities is the canonical set of accepted app access values.
// All callers that validate or interpolate visibility strings must reference
// this slice so a future extension to the set automatically propagates.
var ValidAppVisibilities = []string{"private", "shared", "public"}

// IsValidAppVisibility reports whether s is a recognised app visibility value.
func IsValidAppVisibility(s string) bool {
	return slices.Contains(ValidAppVisibilities, s)
}

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

// SystemUsernameDeploy is the username of the synthetic system user that owns
// requests authenticated by SHINYHUB_DEPLOY_TOKEN. Treated as immutable by the
// users API: role, password, and existence are owned by the env var and the
// startup upsert, not by admins clicking around the UI.
const SystemUsernameDeploy = "__deploy__"

// systemUsernames is the canonical set of usernames managed exclusively by the
// server bootstrap. Membership is constant for the lifetime of a release; no
// runtime mutation. Add new entries here when introducing further system
// users.
var systemUsernames = map[string]struct{}{
	SystemUsernameDeploy: {},
}

// IsSystemUser reports whether username names a server-managed system user.
// The user-management handlers consult this to refuse role changes, password
// resets, and deletions targeting these accounts.
func IsSystemUser(username string) bool {
	_, ok := systemUsernames[username]
	return ok
}

// systemUserPasswordHash is a sentinel that bcrypt.CompareHashAndPassword will
// never match (length below the bcrypt format minimum). The synthetic deploy
// user has no password login path; storing a real bcrypt hash would imply one
// exists, which would be a footgun.
const systemUserPasswordHash = "!disabled"

// UpsertSystemUser inserts the synthetic user named username at the given role,
// or updates the existing row's role to match. Returns the resulting row.
// Idempotent: safe to call on every startup.
//
// Atomic at the SQLite level: INSERT OR IGNORE plus UPDATE means concurrent
// callers cannot race between SELECT and INSERT.
func (s *Store) UpsertSystemUser(username, role string) (*User, error) {
	if !IsSystemUser(username) {
		return nil, fmt.Errorf("upsert system user: %q is not a system username", username)
	}
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO users (username, password_hash, role) VALUES (?, ?, ?)`,
		username, systemUserPasswordHash, role,
	); err != nil {
		return nil, fmt.Errorf("insert system user: %w", err)
	}
	if _, err := s.db.Exec(
		`UPDATE users SET role = ? WHERE username = ?`,
		role, username,
	); err != nil {
		return nil, fmt.Errorf("update system user role: %w", err)
	}
	return s.GetUserByUsername(username)
}

// --- API Keys ---

type CreateAPIKeyParams struct {
	UserID  int64
	KeyHash string
	Name    string
}

// CreateAPIKey inserts a new API key and returns the inserted row's ID and
// creation timestamp.
func (s *Store) CreateAPIKey(p CreateAPIKeyParams) (int64, time.Time, error) {
	result, err := s.db.Exec(
		`INSERT INTO api_keys (user_id, key_hash, name) VALUES (?, ?, ?)`,
		p.UserID, p.KeyHash, p.Name,
	)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("create api key: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("create api key last id: %w", err)
	}
	var createdAt time.Time
	err = s.db.QueryRow(`SELECT created_at FROM api_keys WHERE id = ?`, id).Scan(&createdAt)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("create api key created_at: %w", err)
	}
	return id, createdAt, nil
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
	// LastDeployedAt is the created_at of the most-recent deployment row,
	// or nil if the app has never been deployed. Joined in via the
	// deploymentSummarySQL fragment below.
	LastDeployedAt *time.Time `json:"last_deployed_at,omitempty"`
	// CurrentVersion is the version string of the most-recent deployment,
	// or empty if the app has never been deployed.
	CurrentVersion string `json:"current_version,omitempty"`
	// ManagedBy is the fleet ownership marker ("fleet:<id>") or nil when the
	// app is not fleet-managed. Plain apps.managed_by column.
	ManagedBy *string `json:"managed_by"`
	// ContentDigest is the content digest of the app's newest *succeeded*
	// deployment, or "" if it has never had one. Joined via
	// deploymentSummarySQL; pending/failed deployments are never reflected.
	ContentDigest string `json:"content_digest,omitempty"`
	// ReplicaPlacement is the per-app replica placement as a JSON object
	// {"tier": count}, or "" when no placement is set (all Replicas on the
	// default tier). The Replicas column remains the authoritative total.
	ReplicaPlacement string `json:"replica_placement,omitempty"`
}

// PlacementMap parses ReplicaPlacement into a {tier: count} map. It returns nil
// when placement is unset (all replicas on the default tier) or when the stored
// JSON is malformed, so callers treat an unreadable placement the same as none.
func (a App) PlacementMap() map[string]int {
	if a.ReplicaPlacement == "" {
		return nil
	}
	var m map[string]int
	if err := json.Unmarshal([]byte(a.ReplicaPlacement), &m); err != nil {
		return nil
	}
	return m
}

// deploymentSummarySQL is the SELECT fragment that adds last_deployed_at and
// current_version to any apps query. Kept as a constant so all seven App
// queries (ListApps, ListAppsVisibleToUser, ListPublicApps, ListRunningApps,
// ListDeletingApps, GetAppBySlug, GetAppByID) stay in sync.
const deploymentSummarySQL = `
		(SELECT MAX(created_at) FROM deployments WHERE app_id = apps.id) AS last_deployed_at,
		(SELECT version FROM deployments WHERE app_id = apps.id ORDER BY created_at DESC, id DESC LIMIT 1) AS current_version,
		(SELECT content_digest FROM deployments
		   WHERE app_id = apps.id AND status = 'succeeded'
		   ORDER BY created_at DESC, id DESC LIMIT 1) AS content_digest`

type CreateAppParams struct {
	Slug        string
	Name        string
	ProjectSlug string
	OwnerID     int64
	// Access must be one of ValidAppVisibilities; validated by callers before
	// calling CreateApp. The SQL column DEFAULT 'private' acts as a last-resort
	// safety net only when the column is omitted from the INSERT entirely.
	Access string
}

func (s *Store) CreateApp(p CreateAppParams) error {
	if p.ProjectSlug == "" {
		_, err := s.db.Exec(
			`INSERT INTO apps (slug, name, owner_id, access) VALUES (?, ?, ?, ?)`,
			p.Slug, p.Name, p.OwnerID, p.Access,
		)
		if err != nil {
			return fmt.Errorf("create app: %w", err)
		}
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO apps (slug, name, project_slug, owner_id, access) VALUES (?, ?, ?, ?, ?)`,
		p.Slug, p.Name, p.ProjectSlug, p.OwnerID, p.Access,
	)
	if err != nil {
		return fmt.Errorf("create app: %w", err)
	}
	return nil
}

func (s *Store) GetAppBySlug(slug string) (*App, error) {
	defer s.timed("GetAppBySlug")()
	row := s.db.QueryRow(`
		SELECT id, slug, name, project_slug, owner_id, access, status,
		       replicas, max_sessions_per_replica, deploy_count,
		       hibernate_timeout_minutes,
		       memory_limit_mb, cpu_quota_percent,
		       created_at, updated_at,
		       managed_by, replica_placement,`+deploymentSummarySQL+`
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
		       created_at, updated_at,
		       managed_by, replica_placement,`+deploymentSummarySQL+`
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
		       created_at, updated_at,
		       managed_by, replica_placement,`+deploymentSummarySQL+`
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
		       created_at, updated_at,
		       managed_by, replica_placement,` + deploymentSummarySQL + `
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

// ListDeletingApps returns all apps left in the 'deleting' tombstone state.
// handleDeleteApp marks an app 'deleting' before doing disk cleanup; a crash
// (or a cleanup failure) between the tombstone and the row delete leaves such
// rows behind for startup reconciliation to finish.
func (s *Store) ListDeletingApps() ([]*App, error) {
	rows, err := s.db.Query(`
		SELECT id, slug, name, project_slug, owner_id, access, status,
		       replicas, max_sessions_per_replica, deploy_count,
		       hibernate_timeout_minutes,
		       memory_limit_mb, cpu_quota_percent,
		       created_at, updated_at,
		       managed_by, replica_placement,` + deploymentSummarySQL + `
		FROM apps WHERE status = 'deleting'`)
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

// AllSlugs returns every app slug regardless of status. Used by the startup
// orphan-directory sweep to decide which on-disk slug dirs have no owning row.
func (s *Store) AllSlugs() ([]string, error) {
	rows, err := s.db.Query(`SELECT slug FROM apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		slugs = append(slugs, slug)
	}
	return slugs, rows.Err()
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
		       created_at, updated_at,
		       managed_by, replica_placement,`+deploymentSummarySQL+`
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

// ListPublicApps returns only apps with access = 'public'. It is the ONLY
// query used for anonymous /.shinyhub/apps.json requests: ListAppsVisibleToUser
// also returns 'shared' apps, which are visible to authenticated users only,
// so reusing it for anonymous callers would leak shared apps.
func (s *Store) ListPublicApps(limit, offset int) ([]*App, error) {
	if limit <= 0 {
		limit = -1 // SQLite treats -1 as no limit
	}
	rows, err := s.db.Query(`
		SELECT id, slug, name, project_slug, owner_id, access, status,
		       replicas, max_sessions_per_replica, deploy_count,
		       hibernate_timeout_minutes,
		       memory_limit_mb, cpu_quota_percent,
		       created_at, updated_at,
		       managed_by, replica_placement,`+deploymentSummarySQL+`
		FROM apps
		WHERE access = 'public'
		ORDER BY created_at DESC
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
	ID            int64
	AppID         int64
	Version       string
	BundleDir     string
	Status        string
	ContentDigest string // "" until SetDeploymentDigest records it
	CreatedAt     time.Time
}

// Deployment status lifecycle. A deploy records DeploymentPending before any
// pool swap, then either DeploymentSucceeded once the new pool is serving or
// DeploymentFailed if the attempt aborts. The authoritative "live bundle"
// pointer is the newest row that is neither pending nor failed.
const (
	DeploymentPending   = "pending"
	DeploymentSucceeded = "succeeded"
	DeploymentFailed    = "failed"
)

type CreateDeploymentParams struct {
	AppID     int64
	Version   string
	BundleDir string
	// Status records the outcome of the deploy attempt. Empty defaults to
	// "succeeded" (a row created via this helper represents an already-live
	// bundle). The pending->succeeded/failed flow uses BeginDeployment.
	Status string
}

func (s *Store) CreateDeployment(p CreateDeploymentParams) (*Deployment, error) {
	status := p.Status
	if status == "" {
		status = DeploymentSucceeded
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

// BeginDeployment durably records the intent to deploy bundleDir BEFORE the
// running pool is touched. The row is 'pending' and is deliberately invisible
// to the authoritative "live bundle" reads (ListDeployments) until
// PromoteDeployment confirms the new pool is serving. If the server dies
// mid-deploy the row stays 'pending'; startup reconciliation fails it so it is
// never mistaken for a good deployment.
func (s *Store) BeginDeployment(appID int64, version, bundleDir string) (*Deployment, error) {
	res, err := s.db.Exec(
		`INSERT INTO deployments (app_id, version, bundle_dir, status) VALUES (?, ?, ?, ?)`,
		appID, version, bundleDir, DeploymentPending,
	)
	if err != nil {
		return nil, fmt.Errorf("begin deployment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("begin deployment: last insert id: %w", err)
	}
	return &Deployment{ID: id, AppID: appID, Version: version, BundleDir: bundleDir, Status: DeploymentPending}, nil
}

// PromoteDeployment marks a pending deployment as the live one. It only acts
// on a row still in 'pending' so a late call cannot resurrect a deployment
// that startup reconciliation already failed.
func (s *Store) PromoteDeployment(id int64) error {
	res, err := s.db.Exec(
		`UPDATE deployments SET status = ? WHERE id = ? AND status = ?`,
		DeploymentSucceeded, id, DeploymentPending)
	if err != nil {
		return fmt.Errorf("promote deployment %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("promote deployment %d: rows affected: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("promote deployment %d: not pending", id)
	}
	return nil
}

// FailDeployment marks an aborted pending deployment as failed so it is never
// adopted as the live bundle. It is a no-op on a row that is not pending.
func (s *Store) FailDeployment(id int64) error {
	_, err := s.db.Exec(
		`UPDATE deployments SET status = ? WHERE id = ? AND status = ?`,
		DeploymentFailed, id, DeploymentPending)
	if err != nil {
		return fmt.Errorf("fail deployment %d: %w", id, err)
	}
	return nil
}

// SetDeploymentDigest records the content digest on a deployment row. Called
// after BeginDeployment and before PromoteDeployment so the digest travels
// with the pending row and only becomes authoritative on promotion.
func (s *Store) SetDeploymentDigest(id int64, digest string) error {
	res, err := s.db.Exec(
		`UPDATE deployments SET content_digest = ? WHERE id = ?`, digest, id)
	if err != nil {
		return fmt.Errorf("set deployment digest %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("set deployment digest %d: row not found", id)
	}
	return nil
}

// ListInflightDeployments returns every deployment still in 'pending'. A
// pending row on startup means a deploy was interrupted before the new pool
// was confirmed; the server fails these so recovery falls back to the last
// good deployment.
func (s *Store) ListInflightDeployments() ([]*Deployment, error) {
	rows, err := s.db.Query(
		`SELECT id, app_id, version, bundle_dir, status, content_digest, created_at
		FROM deployments WHERE status = ? ORDER BY id`, DeploymentPending)
	if err != nil {
		return nil, fmt.Errorf("list inflight deployments: %w", err)
	}
	defer rows.Close()
	var ds []*Deployment
	for rows.Next() {
		var d Deployment
		var digest sql.NullString
		if err := rows.Scan(&d.ID, &d.AppID, &d.Version, &d.BundleDir, &d.Status, &digest, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.ContentDigest = digest.String
		ds = append(ds, &d)
	}
	return ds, rows.Err()
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

// ListDeployments returns an app's deployments newest-first, excluding
// in-flight ('pending') and aborted ('failed') rows. Callers treat the result
// as the authoritative live-bundle history: index 0 is the current bundle,
// index 1 the rollback target. An interrupted deploy therefore never shifts
// this pointer until PromoteDeployment confirms the new pool.
func (s *Store) ListDeployments(appID int64) ([]*Deployment, error) {
	rows, err := s.db.Query(`
		SELECT id, app_id, version, bundle_dir, status, content_digest, created_at
		FROM deployments
		WHERE app_id = ? AND status NOT IN ('pending', 'failed')
		ORDER BY id DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ds []*Deployment
	for rows.Next() {
		var d Deployment
		var digest sql.NullString
		if err := rows.Scan(&d.ID, &d.AppID, &d.Version, &d.BundleDir, &d.Status, &digest, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.ContentDigest = digest.String
		ds = append(ds, &d)
	}
	return ds, rows.Err()
}

// HasAnyDeployment reports whether at least one deployment row exists for
// the given app. Used by the never-deployed gate as the authoritative
// "first deploy has happened" signal — keying off the durable deployments
// row instead of the deploy_count counter means a transient counter-write
// failure cannot lock users out of an app whose pool is already live.
func (s *Store) HasAnyDeployment(appID int64) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM deployments WHERE app_id = ?)`, appID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

// GetDeploymentBySlugAndID fetches a single deployment by its ID, verified
// to belong to the app identified by slug. Returns ErrNotFound if the
// deployment does not exist or belongs to a different app.
func (s *Store) GetDeploymentBySlugAndID(slug string, id int64) (*Deployment, error) {
	row := s.db.QueryRow(`
		SELECT d.id, d.app_id, d.version, d.bundle_dir, d.status, d.content_digest, d.created_at
		FROM deployments d
		JOIN apps a ON a.id = d.app_id
		WHERE d.id = ? AND a.slug = ?`, id, slug)
	var dep Deployment
	var digest sql.NullString
	if err := row.Scan(&dep.ID, &dep.AppID, &dep.Version, &dep.BundleDir, &dep.Status, &digest, &dep.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	dep.ContentDigest = digest.String
	return &dep, nil
}

// GetDeploymentByDigest returns the newest non-failed deployment whose recorded
// content digest matches. The control-plane bundle-fetch endpoint uses it to
// resolve a worker's pull-by-digest request to a stored bundle artifact. A
// pending row is eligible (a remote replica may pull before promotion); a
// failed row is not.
func (s *Store) GetDeploymentByDigest(digest string) (*Deployment, error) {
	row := s.db.QueryRow(`
		SELECT id, app_id, version, bundle_dir, status, content_digest, created_at
		FROM deployments
		WHERE content_digest = ? AND status != 'failed'
		ORDER BY id DESC LIMIT 1`, digest)
	var d Deployment
	var dg sql.NullString
	if err := row.Scan(&d.ID, &d.AppID, &d.Version, &d.BundleDir, &d.Status, &dg, &d.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.ContentDigest = dg.String
	return &d, nil
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

// SetAppManagedBy sets or clears (nil) the fleet ownership marker.
func (s *Store) SetAppManagedBy(slug string, managedBy *string) error {
	result, err := s.db.Exec(
		`UPDATE apps SET managed_by = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`, managedBy, slug)
	if err != nil {
		return fmt.Errorf("set app managed_by: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set app managed_by rows: %w", err)
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

type ProvisionOAuthUserParams struct {
	Provider           string
	ProviderID         string
	UsernameCandidates []string
	Role               string
}

// ProvisionOAuthUser atomically resolves the platform user for an external
// identity. If (provider, provider_id) is already linked it returns that
// user. Otherwise it creates a user under the first available candidate
// username and links it, in a single transaction.
//
// Concurrent first logins for the same identity converge on one user: the
// loser of the race rolls back the user it just created and returns the
// winner's, so a session is never issued for an unlinked account and no
// orphan user rows accumulate. The bool return reports whether a new user
// was created (callers use it to emit a create_user audit event).
func (s *Store) ProvisionOAuthUser(p ProvisionOAuthUserParams) (*User, bool, error) {
	ctx := context.Background()

	// A dedicated connection with an explicit BEGIN IMMEDIATE takes the
	// write lock up front, so concurrent first logins for the same identity
	// serialize here rather than racing to the decisive linked-account read.
	// The loser then sees the winner's committed link and returns it. The
	// UNIQUE-conflict path below is kept as defense in depth.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, false, fmt.Errorf("begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	scanUser := func(row *sql.Row) (*User, error) {
		var u User
		if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		return &u, nil
	}
	const linkedQuery = `
		SELECT u.id, u.username, u.password_hash, u.role, u.created_at
		FROM users u
		JOIN oauth_accounts o ON o.user_id = u.id
		WHERE o.provider = ? AND o.provider_id = ?`
	const userByIDQuery = `
		SELECT id, username, password_hash, role, created_at
		FROM users WHERE id = ?`

	if u, gerr := scanUser(conn.QueryRowContext(ctx, linkedQuery, p.Provider, p.ProviderID)); gerr == nil {
		if _, cerr := conn.ExecContext(ctx, "COMMIT"); cerr != nil {
			return nil, false, fmt.Errorf("commit: %w", cerr)
		}
		committed = true
		return u, false, nil
	} else if !errors.Is(gerr, ErrNotFound) {
		return nil, false, gerr
	}

	var userID int64
	for _, name := range p.UsernameCandidates {
		res, ierr := conn.ExecContext(ctx,
			`INSERT INTO users (username, password_hash, role) VALUES (?, '', ?)`,
			name, p.Role,
		)
		if ierr != nil {
			var se *sqlite.Error
			if errors.As(ierr, &se) && se.Code() == sqlitelib.SQLITE_CONSTRAINT_UNIQUE {
				continue // username taken, try the next candidate
			}
			return nil, false, fmt.Errorf("create user: %w", ierr)
		}
		userID, _ = res.LastInsertId()
		break
	}
	if userID == 0 {
		return nil, false, fmt.Errorf("create user: all candidate usernames in use")
	}

	if _, lerr := conn.ExecContext(ctx,
		`INSERT INTO oauth_accounts (user_id, provider, provider_id) VALUES (?, ?, ?)`,
		userID, p.Provider, p.ProviderID,
	); lerr != nil {
		var se *sqlite.Error
		if errors.As(lerr, &se) && se.Code() == sqlitelib.SQLITE_CONSTRAINT_UNIQUE {
			// Lost a concurrent first-login race (rare: BEGIN IMMEDIATE
			// serializes contenders, so the loser normally sees the link
			// in linkedQuery above). Roll back and release this connection
			// BEFORE the fallback read, which needs its own pooled
			// connection — holding this one would deadlock a pool capped
			// at a single connection (in-memory store).
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			committed = true // suppress the deferred ROLLBACK
			_ = conn.Close()
			existing, eerr := s.GetUserByOAuthAccount(p.Provider, p.ProviderID)
			if eerr != nil {
				return nil, false, fmt.Errorf("resolve raced oauth user: %w", eerr)
			}
			return existing, false, nil
		}
		return nil, false, fmt.Errorf("link oauth account: %w", lerr)
	}

	// Read the created row on the same connection/transaction so no second
	// pooled connection is needed while this one is still held.
	created, gerr := scanUser(conn.QueryRowContext(ctx, userByIDQuery, userID))
	if gerr != nil {
		return nil, false, fmt.Errorf("read created user: %w", gerr)
	}
	if _, cerr := conn.ExecContext(ctx, "COMMIT"); cerr != nil {
		return nil, false, fmt.Errorf("commit: %w", cerr)
	}
	committed = true
	return created, true, nil
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
// A non-empty action filters the count to that action so has_more matches the
// filtered listing.
func (s *Store) CountAuditEvents(action string) (int64, error) {
	var n int64
	query := `SELECT COUNT(*) FROM audit_events`
	args := []any{}
	if action != "" {
		query += ` WHERE action = ?`
		args = append(args, action)
	}
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count audit events: %w", err)
	}
	return n, nil
}

// ListAuditEvents returns audit events ordered newest-first with pagination.
// Each event includes the username of the acting user via a LEFT JOIN on users,
// so anonymous events (no user_id) are still returned with a nil Username.
// A non-empty action restricts the listing to that action.
func (s *Store) ListAuditEvents(action string, limit, offset int) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
		SELECT ae.id, ae.user_id, u.username,
		       ae.action, ae.resource_type, ae.resource_id,
		       ae.detail, ae.ip_address, ae.created_at
		FROM audit_events ae
		LEFT JOIN users u ON u.id = ae.user_id`
	args := []any{}
	if action != "" {
		query += ` WHERE ae.action = ?`
		args = append(args, action)
	}
	query += ` ORDER BY ae.created_at DESC, ae.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.Query(query, args...)
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

// Replica status values stored in replicas.status.
const (
	// ReplicaStatusRunning marks a replica the control plane considers healthy.
	ReplicaStatusRunning = "running"
	// ReplicaStatusLost marks a replica whose worker stopped heartbeating. It is
	// excluded from routing and is not auto-restarted.
	ReplicaStatusLost = "lost"
)

// Replica represents a single backend process instance for an app.
// Multiple replicas allow an app to run N parallel processes behind the proxy.
type Replica struct {
	AppID        int64     `json:"app_id"`
	Index        int       `json:"index"`
	PID          *int      `json:"pid,omitempty"`
	Port         *int      `json:"port,omitempty"`
	Status       string    `json:"status"`
	Provider     string    `json:"provider"`
	Tier         string    `json:"tier"`
	EndpointURL  string    `json:"endpoint_url"`
	WorkerID     string    `json:"worker_id"`
	AppVersion   string    `json:"app_version"`
	DesiredState string    `json:"desired_state"`
	DeploymentID *int64    `json:"deployment_id,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// UpsertReplicaParams holds the fields for inserting or updating a replica row.
type UpsertReplicaParams struct {
	AppID        int64
	Index        int
	PID          *int
	Port         *int
	Status       string
	Provider     string
	Tier         string
	EndpointURL  string
	WorkerID     string
	AppVersion   string
	DesiredState string
	DeploymentID *int64
}

// UpsertReplica inserts a new replica or updates an existing one identified by
// (app_id, idx). All fields are replaced on conflict. DesiredState defaults to
// "running" when the caller leaves it empty.
func (s *Store) UpsertReplica(p UpsertReplicaParams) error {
	desired := p.DesiredState
	if desired == "" {
		desired = "running"
	}
	_, err := s.db.Exec(`
		INSERT INTO replicas (app_id, idx, pid, port, status, provider, tier,
		                      endpoint_url, worker_id, app_version, desired_state,
		                      deployment_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(app_id, idx) DO UPDATE SET
			pid           = excluded.pid,
			port          = excluded.port,
			status        = excluded.status,
			provider      = excluded.provider,
			tier          = excluded.tier,
			endpoint_url  = excluded.endpoint_url,
			worker_id     = excluded.worker_id,
			app_version   = excluded.app_version,
			desired_state = excluded.desired_state,
			deployment_id = excluded.deployment_id,
			updated_at    = excluded.updated_at`,
		p.AppID, p.Index, p.PID, p.Port, p.Status, p.Provider, p.Tier,
		p.EndpointURL, p.WorkerID, p.AppVersion, desired, p.DeploymentID,
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
		SELECT app_id, idx, pid, port, status, provider, tier,
		       endpoint_url, worker_id, app_version, desired_state,
		       deployment_id, updated_at
		FROM replicas WHERE app_id = ? ORDER BY idx`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Replica{}
	for rows.Next() {
		var r Replica
		var updatedAt int64
		if err := rows.Scan(&r.AppID, &r.Index, &r.PID, &r.Port, &r.Status,
			&r.Provider, &r.Tier, &r.EndpointURL, &r.WorkerID, &r.AppVersion,
			&r.DesiredState, &r.DeploymentID, &updatedAt); err != nil {
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

// SetAppPlacement persists the per-app replica placement JSON and the resolved
// total replica count in one update. placementJSON is "" to clear placement
// (all replicas on the default tier). total is the authoritative replica count.
func (s *Store) SetAppPlacement(appID int64, placementJSON string, total int) error {
	res, err := s.db.Exec(
		`UPDATE apps SET replica_placement = ?, replicas = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		placementJSON, total, appID,
	)
	if err != nil {
		return fmt.Errorf("set placement: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ApplyAppManifestSettingsParams carries the subset of [app] manifest fields
// to apply. Callers set the Set* booleans to true for each field they want
// written; absent fields are left untouched.
type ApplyAppManifestSettingsParams struct {
	AppID int64
	Slug  string

	SetHibernate     bool
	HibernateMinutes *int // nil = NULL (reset to default); non-nil = explicit value

	SetReplicas      bool
	Replicas         int
	PreviousReplicas int // for shrink: delete replicas where idx >= Replicas

	SetMaxSessionsPerReplica bool
	MaxSessionsPerReplica    int
}

// ApplyAppManifestSettings applies any subset of (hibernate, replicas,
// max_sessions_per_replica) in a single SQLite transaction. Replica
// shrink (DELETE FROM replicas WHERE app_id = ? AND idx >= ?) runs in
// the same transaction so a mid-apply failure rolls back both the count
// change and the row prune — avoids the latent bug where two separate
// writes can drift.
//
// Caller contract: manager.Stop(slug) has already run; no live
// process holds a replica index that may be deleted.
func (s *Store) ApplyAppManifestSettings(p ApplyAppManifestSettingsParams) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if p.SetHibernate {
		if _, err := tx.Exec(
			`UPDATE apps SET hibernate_timeout_minutes = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`,
			p.HibernateMinutes, p.Slug,
		); err != nil {
			return fmt.Errorf("update hibernate: %w", err)
		}
	}

	if p.SetReplicas {
		if p.Replicas < p.PreviousReplicas {
			if _, err := tx.Exec(
				`DELETE FROM replicas WHERE app_id = ? AND idx >= ?`,
				p.AppID, p.Replicas,
			); err != nil {
				return fmt.Errorf("prune replicas: %w", err)
			}
		}
		if _, err := tx.Exec(
			`UPDATE apps SET replicas = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.Replicas, p.AppID,
		); err != nil {
			return fmt.Errorf("update replicas: %w", err)
		}
	}

	if p.SetMaxSessionsPerReplica {
		if _, err := tx.Exec(
			`UPDATE apps SET max_sessions_per_replica = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.MaxSessionsPerReplica, p.AppID,
		); err != nil {
			return fmt.Errorf("update max_sessions_per_replica: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// PatchAppSettingsParams carries the user-PATCHable app fields. Callers set
// each Set* flag for the fields they want written; absent fields are left
// untouched. Resource-limit and replica/max-session merges are resolved
// inside the transaction so the read and write cannot interleave with a
// concurrent writer.
type PatchAppSettingsParams struct {
	Slug string

	SetHibernate     bool
	HibernateMinutes *int // nil = NULL (reset to default)

	SetName bool
	Name    string

	SetProjectSlug bool
	ProjectSlug    string

	SetMemoryLimitMB bool
	MemoryLimitMB    *int // nil = NULL (inherit global)

	SetCPUQuotaPercent bool
	CPUQuotaPercent    *int // nil = NULL (inherit global)

	SetReplicas bool
	Replicas    int

	SetMaxSessions bool
	MaxSessions    int
}

// PatchAppSettings applies any subset of the user-editable app settings in a
// single SQLite transaction, so a failure partway through cannot leave the
// row half-updated. It returns the app's prior status and replica count
// (read inside the same transaction) so the caller can decide whether a
// running pool needs a redeploy. Returns ErrNotFound if no app has the slug.
func (s *Store) PatchAppSettings(p PatchAppSettingsParams) (priorStatus string, priorReplicas int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var appID int64
	var curMem, curCPU sql.NullInt64
	if err := tx.QueryRow(
		`SELECT id, status, replicas, memory_limit_mb, cpu_quota_percent FROM apps WHERE slug = ?`,
		p.Slug,
	).Scan(&appID, &priorStatus, &priorReplicas, &curMem, &curCPU); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, ErrNotFound
		}
		return "", 0, fmt.Errorf("load app: %w", err)
	}

	if p.SetHibernate {
		if _, err := tx.Exec(
			`UPDATE apps SET hibernate_timeout_minutes = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.HibernateMinutes, appID,
		); err != nil {
			return "", 0, fmt.Errorf("update hibernate: %w", err)
		}
	}
	if p.SetName {
		if _, err := tx.Exec(
			`UPDATE apps SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.Name, appID,
		); err != nil {
			return "", 0, fmt.Errorf("update name: %w", err)
		}
	}
	if p.SetProjectSlug {
		if _, err := tx.Exec(
			`UPDATE apps SET project_slug = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.ProjectSlug, appID,
		); err != nil {
			return "", 0, fmt.Errorf("update project_slug: %w", err)
		}
	}
	if p.SetMemoryLimitMB || p.SetCPUQuotaPercent {
		newMem := curMem // preserve the column not being updated
		if p.SetMemoryLimitMB {
			newMem = nullIntFromPtr(p.MemoryLimitMB)
		}
		newCPU := curCPU
		if p.SetCPUQuotaPercent {
			newCPU = nullIntFromPtr(p.CPUQuotaPercent)
		}
		if _, err := tx.Exec(
			`UPDATE apps SET memory_limit_mb = ?, cpu_quota_percent = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			newMem, newCPU, appID,
		); err != nil {
			return "", 0, fmt.Errorf("update resource limits: %w", err)
		}
	}
	if p.SetReplicas {
		if p.Replicas < priorReplicas {
			if _, err := tx.Exec(
				`DELETE FROM replicas WHERE app_id = ? AND idx >= ?`,
				appID, p.Replicas,
			); err != nil {
				return "", 0, fmt.Errorf("prune replicas: %w", err)
			}
		}
		if _, err := tx.Exec(
			`UPDATE apps SET replicas = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.Replicas, appID,
		); err != nil {
			return "", 0, fmt.Errorf("update replicas: %w", err)
		}
	}
	if p.SetMaxSessions {
		if _, err := tx.Exec(
			`UPDATE apps SET max_sessions_per_replica = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.MaxSessions, appID,
		); err != nil {
			return "", 0, fmt.Errorf("update max_sessions_per_replica: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("commit: %w", err)
	}
	return priorStatus, priorReplicas, nil
}

// nullIntFromPtr maps a *int to a sql.NullInt64 (nil ⇒ NULL).
func nullIntFromPtr(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
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
	var projectSlug, currentVersion, contentDigest sql.NullString
	// last_deployed_at is the result of MAX(deployments.created_at). SQLite
	// aggregates lose the original column type, so the driver returns the
	// value as a string. We parse it manually below.
	var lastDeployedAtRaw sql.NullString
	err := s.Scan(
		&a.ID, &a.Slug, &a.Name, &projectSlug, &a.OwnerID, &a.Access,
		&a.Status, &a.Replicas, &a.MaxSessionsPerReplica, &a.DeployCount,
		&a.HibernateTimeoutMinutes, &a.MemoryLimitMB, &a.CPUQuotaPercent,
		&a.CreatedAt, &a.UpdatedAt,
		&a.ManagedBy, &a.ReplicaPlacement,
		&lastDeployedAtRaw, &currentVersion, &contentDigest,
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
	if lastDeployedAtRaw.Valid {
		if t, ok := parseSQLiteTime(lastDeployedAtRaw.String); ok {
			a.LastDeployedAt = &t
		}
	}
	if currentVersion.Valid {
		a.CurrentVersion = currentVersion.String
	}
	if contentDigest.Valid {
		a.ContentDigest = contentDigest.String
	}
	return &a, nil
}

// --- Workers ---

// Worker is one joined worker host (node). NodeID is the stable identity bound
// into the worker's client certificate; it is distinct from a replica's
// container id.
type Worker struct {
	NodeID        string
	Name          string
	AdvertiseAddr string
	Tier          string
	Status        string // "up" | "down"
	Fingerprint   string // SHA-256 of the trusted client cert (hex)
	Version       string
	LastHeartbeat string
	RevokedAt     string // UTC datetime the worker was revoked; empty when never revoked
	CreatedAt     time.Time
}

// Revoked reports whether the worker has been administratively revoked. A
// revoked worker's certificate is rejected by the worker API and excluded from
// control->worker dials regardless of its up/down status or cert TTL.
func (w Worker) Revoked() bool { return w.RevokedAt != "" }

// UpsertWorker inserts or replaces a worker row by node id. Registration uses
// it to record a newly joined node; re-registration (agent restart) refreshes
// the advertise address and certificate fingerprint.
func (s *Store) UpsertWorker(w Worker) error {
	_, err := s.db.Exec(`
		INSERT INTO workers (node_id, name, advertise_addr, tier, status, cert_fingerprint, version, last_heartbeat)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(node_id) DO UPDATE SET
			name = excluded.name,
			advertise_addr = excluded.advertise_addr,
			tier = excluded.tier,
			status = excluded.status,
			cert_fingerprint = excluded.cert_fingerprint,
			version = excluded.version,
			last_heartbeat = excluded.last_heartbeat`,
		w.NodeID, w.Name, w.AdvertiseAddr, w.Tier, w.Status, w.Fingerprint, w.Version)
	if err != nil {
		return fmt.Errorf("upsert worker %q: %w", w.NodeID, err)
	}
	return nil
}

// GetWorker returns the worker row for nodeID, or ErrNotFound if it does not exist.
func (s *Store) GetWorker(nodeID string) (*Worker, error) {
	row := s.db.QueryRow(`
		SELECT node_id, name, advertise_addr, tier, status, cert_fingerprint, version, last_heartbeat, revoked_at, created_at
		FROM workers WHERE node_id = ?`, nodeID)
	var w Worker
	var createdAtRaw string
	if err := row.Scan(&w.NodeID, &w.Name, &w.AdvertiseAddr, &w.Tier, &w.Status,
		&w.Fingerprint, &w.Version, &w.LastHeartbeat, &w.RevokedAt, &createdAtRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if t, ok := parseSQLiteTime(createdAtRaw); ok {
		w.CreatedAt = t
	}
	return &w, nil
}

// ListWorkers returns all registered workers ordered by node_id.
// Returns a non-nil empty slice when no workers are registered.
func (s *Store) ListWorkers() ([]*Worker, error) {
	rows, err := s.db.Query(`
		SELECT node_id, name, advertise_addr, tier, status, cert_fingerprint, version, last_heartbeat, revoked_at, created_at
		FROM workers ORDER BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ws := []*Worker{}
	for rows.Next() {
		var w Worker
		var createdAtRaw string
		if err := rows.Scan(&w.NodeID, &w.Name, &w.AdvertiseAddr, &w.Tier, &w.Status,
			&w.Fingerprint, &w.Version, &w.LastHeartbeat, &w.RevokedAt, &createdAtRaw); err != nil {
			return nil, err
		}
		if t, ok := parseSQLiteTime(createdAtRaw); ok {
			w.CreatedAt = t
		}
		ws = append(ws, &w)
	}
	return ws, rows.Err()
}

// TouchWorkerHeartbeat records a heartbeat: updates last_heartbeat, refreshes the
// trusted cert fingerprint (renewal), and sets status. The caller decides the
// status so a heartbeat from a superseded worker does not resurrect it to up
// alongside the tier's current owner.
func (s *Store) TouchWorkerHeartbeat(nodeID, fingerprint, status string) error {
	res, err := s.db.Exec(`
		UPDATE workers SET last_heartbeat = datetime('now'), cert_fingerprint = ?, status = ?
		WHERE node_id = ?`, fingerprint, status, nodeID)
	if err != nil {
		return fmt.Errorf("touch worker %q: %w", nodeID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetWorkerStatus(nodeID, status string) error {
	res, err := s.db.Exec(`UPDATE workers SET status = ? WHERE node_id = ?`, status, nodeID)
	if err != nil {
		return fmt.Errorf("set worker status %q: %w", nodeID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SupersedeTierWorkers marks every up worker on a tier down except the given
// node id, in a single statement. Used when a new worker wins a tier's routing
// slot so prior registrants stop being routing candidates. Zero affected rows
// is valid (no prior worker on the tier), so unlike SetWorkerStatus this does
// not return ErrNotFound.
func (s *Store) SupersedeTierWorkers(tier, exceptNodeID string) error {
	_, err := s.db.Exec(
		`UPDATE workers SET status = 'down' WHERE tier = ? AND node_id <> ? AND status = 'up'`,
		tier, exceptNodeID)
	if err != nil {
		return fmt.Errorf("supersede tier %q workers: %w", tier, err)
	}
	return nil
}

// RevokeWorker administratively revokes a worker: it marks the node down and
// stamps revoked_at with the current UTC time, preserving the timestamp of the
// first revocation if the worker is revoked again (audit stability). A revoked
// worker's certificate is rejected by the worker API and excluded from
// control->worker dials, independent of its short cert TTL. Returns ErrNotFound
// for an unknown node.
func (s *Store) RevokeWorker(nodeID string) error {
	res, err := s.db.Exec(`
		UPDATE workers
		SET status = 'down',
		    revoked_at = CASE WHEN revoked_at = '' THEN datetime('now') ELSE revoked_at END
		WHERE node_id = ?`, nodeID)
	if err != nil {
		return fmt.Errorf("revoke worker %q: %w", nodeID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteWorker(nodeID string) error {
	res, err := s.db.Exec(`DELETE FROM workers WHERE node_id = ?`, nodeID)
	if err != nil {
		return fmt.Errorf("delete worker %q: %w", nodeID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListWorkersStale returns workers whose last_heartbeat is at or before cutoff
// and that are not already marked down. last_heartbeat is stored as a UTC
// datetime string ('YYYY-MM-DD HH:MM:SS'); cutoff is formatted the same way so
// the string comparison is chronological. A worker registered with no heartbeat
// (empty string) sorts before any real timestamp and is reported stale.
func (s *Store) ListWorkersStale(cutoff time.Time) ([]*Worker, error) {
	rows, err := s.db.Query(`
		SELECT node_id, name, advertise_addr, tier, status, cert_fingerprint, version, last_heartbeat, revoked_at, created_at
		FROM workers WHERE last_heartbeat <= ? AND status != 'down'`,
		cutoff.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ws := []*Worker{}
	for rows.Next() {
		var w Worker
		var createdAtRaw string
		if err := rows.Scan(&w.NodeID, &w.Name, &w.AdvertiseAddr, &w.Tier, &w.Status,
			&w.Fingerprint, &w.Version, &w.LastHeartbeat, &w.RevokedAt, &createdAtRaw); err != nil {
			return nil, err
		}
		if t, ok := parseSQLiteTime(createdAtRaw); ok {
			w.CreatedAt = t
		}
		ws = append(ws, &w)
	}
	return ws, rows.Err()
}

// ListReplicasByWorker returns the replicas whose worker_id matches nodeID.
func (s *Store) ListReplicasByWorker(nodeID string) ([]*Replica, error) {
	rows, err := s.db.Query(`
		SELECT app_id, idx, pid, port, status, provider, tier,
		       endpoint_url, worker_id, app_version, desired_state,
		       deployment_id, updated_at
		FROM replicas WHERE worker_id = ? ORDER BY app_id, idx`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Replica{}
	for rows.Next() {
		var r Replica
		var updatedAt int64
		if err := rows.Scan(&r.AppID, &r.Index, &r.PID, &r.Port, &r.Status,
			&r.Provider, &r.Tier, &r.EndpointURL, &r.WorkerID, &r.AppVersion,
			&r.DesiredState, &r.DeploymentID, &updatedAt); err != nil {
			return nil, err
		}
		r.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// UpdateReplicaStatus sets the status of a single replica identified by
// (app_id, idx) and refreshes its updated_at timestamp.
func (s *Store) UpdateReplicaStatus(appID int64, index int, status string) error {
	_, err := s.db.Exec(
		`UPDATE replicas SET status = ?, updated_at = strftime('%s','now')
		   WHERE app_id = ? AND idx = ?`, status, appID, index)
	return err
}

// parseSQLiteTime parses the timestamp formats SQLite emits for DATETIME
// columns and aggregates over them. CURRENT_TIMESTAMP uses
// "2006-01-02 15:04:05"; values written via Go's time.Time round-trip as
// RFC3339Nano. Returns (zero, false) on unrecognised input.
func parseSQLiteTime(s string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
