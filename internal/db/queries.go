package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

var ErrNotFound = errors.New("not found")

// ErrSlugTaken is returned by CreateApp when the requested slug is already
// used by another app. Callers should surface this as HTTP 409 Conflict.
var ErrSlugTaken = errors.New("slug already taken")

// ValidAppVisibilities is the canonical set of accepted app access values.
// All callers that validate or interpolate visibility strings must reference
// this slice so a future extension to the set automatically propagates.
var ValidAppVisibilities = []string{"private", "shared", "public"}

// IsValidAppVisibility reports whether s is a recognised app visibility value.
func IsValidAppVisibility(s string) bool {
	return slices.Contains(ValidAppVisibilities, s)
}

// ValidMemberRoles is the canonical set of per-app member roles.
var ValidMemberRoles = []string{"viewer", "manager"}

// IsValidMemberRole reports whether s is a recognised app member role.
func IsValidMemberRole(s string) bool {
	return slices.Contains(ValidMemberRoles, s)
}

// --- Users ---

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	DisplayName  string
	// Email is the address asserted by the identity provider on SSO login,
	// persisted so native session requests can forward X-Shinyhub-Email. Empty
	// for local username/password accounts and until the first SSO login sets it.
	Email     string
	CreatedAt time.Time
}

// HasLocalPassword reports whether the hash is a usable bcrypt password (i.e.
// the account can log in with a username/password and may change it from the
// profile page). OAuth/OIDC accounts carry an empty hash and forward-auth /
// system accounts carry the "!disabled" sentinel; neither starts with the
// bcrypt "$2" prefix, so both correctly report false.
func HasLocalPassword(hash string) bool {
	return strings.HasPrefix(hash, "$2")
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
		`SELECT id, username, password_hash, role, display_name, email, created_at FROM users WHERE username = ?`,
		username,
	)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &u.Email, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) GetUserByID(id int64) (*User, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, role, display_name, email, created_at FROM users WHERE id = ?`,
		id,
	)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &u.Email, &u.CreatedAt); err != nil {
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
		`SELECT id, username, password_hash, role, display_name, email, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &u.Email, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, &u)
	}
	if users == nil {
		users = []*User{}
	}
	return users, rows.Err()
}

// GetForwardAuthUser returns the auth.ContextUser for a username, or
// auth.ErrUserNotFound if no such user exists. Adapter for
// auth.ForwardAuthUserStore.
func (s *Store) GetForwardAuthUser(username string) (*auth.ContextUser, error) {
	u, err := s.GetUserByUsername(username)
	if errors.Is(err, ErrNotFound) {
		return nil, auth.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return &auth.ContextUser{ID: u.ID, Username: u.Username, Role: u.Role, DisplayName: u.DisplayName}, nil
}

// CreateForwardAuthUser inserts a user with no local password login path
// (forward-auth only) and returns the new ContextUser. Username uniqueness is
// enforced by the users table; collisions return an error.
func (s *Store) CreateForwardAuthUser(username, role string) (*auth.ContextUser, error) {
	if err := s.CreateUser(CreateUserParams{
		Username:     username,
		PasswordHash: systemUserPasswordHash, // "!disabled" sentinel: no local password login path
		Role:         role,
	}); err != nil {
		return nil, fmt.Errorf("create forward auth user: %w", err)
	}
	u, err := s.GetUserByUsername(username)
	if err != nil {
		return nil, err
	}
	return &auth.ContextUser{ID: u.ID, Username: u.Username, Role: u.Role}, nil
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

// UpdateUserDisplayName sets the friendly display name for the user identified
// by ID (self-service profile edit). The caller is responsible for trimming and
// length-validating name; an empty string is allowed and re-opens the field to
// SSO backfill on the next login. Returns ErrNotFound if no such user exists.
func (s *Store) UpdateUserDisplayName(id int64, name string) error {
	result, err := s.db.Exec(`UPDATE users SET display_name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("update user display name: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update user display name rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDisplayNameFromIdP refreshes a user's display name from an external
// identity provider on login. It applies ONLY to IdP-governed accounts - those
// without a local bcrypt password - so a local account's self-set name is never
// overwritten, even if that local user has also linked an SSO provider. SSO
// accounts are refreshed on every login so a name changed upstream propagates.
// A blank name is a no-op, so a login that omits the name claim never blanks a
// good name. The "$2%" guard is the SQL mirror of HasLocalPassword (bcrypt
// hashes start with "$2"); keep the two in sync. Not finding a matching row is
// not an error.
func (s *Store) SetDisplayNameFromIdP(id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if _, err := s.db.Exec(
		`UPDATE users SET display_name = ? WHERE id = ? AND password_hash NOT LIKE '$2%'`,
		name, id,
	); err != nil {
		return fmt.Errorf("set display name from idp: %w", err)
	}
	return nil
}

// SetEmailFromIdP refreshes a user's email from an external identity provider
// on login. Like SetDisplayNameFromIdP it applies ONLY to IdP-governed accounts
// (those without a local bcrypt password), so a locally-managed account is never
// given SSO-derived PII even if it has also linked a provider. SSO accounts are
// refreshed on every login so an address changed upstream propagates. A blank
// email is a no-op, so a login that omits the email claim never blanks a stored
// one. The "$2%" guard mirrors HasLocalPassword; keep the two in sync. Not
// finding a matching row is not an error.
func (s *Store) SetEmailFromIdP(id int64, email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}
	if _, err := s.db.Exec(
		`UPDATE users SET email = ? WHERE id = ? AND password_hash NOT LIKE '$2%'`,
		email, id,
	); err != nil {
		return fmt.Errorf("set email from idp: %w", err)
	}
	return nil
}

// DeleteUser permanently removes a user and all their associated data
// (FK cascades handle oauth_accounts and api_keys). It refuses (ErrLastAdmin)
// to delete the final admin, in one transaction under the role-mutation lock so
// concurrent deletes cannot race the system to zero admins.
// Returns ErrNotFound if no user with that ID exists.
func (s *Store) DeleteUser(id int64) error {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), roleMutationLockKey)
	if err != nil {
		return fmt.Errorf("delete user: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var role string
	err = tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, id).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete user: read role: %w", err)
	}
	if role == "admin" {
		others, err := countAdminsExceptTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if others == 0 {
			return ErrLastAdmin
		}
	}
	// apps.owner_id has no ON DELETE action (RESTRICT), so deleting a user who
	// owns apps would fail with an opaque FK error. Detect it up front and return
	// a typed sentinel the API maps to a clear 409.
	var ownedApps int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM apps WHERE owner_id = ?`, id).Scan(&ownedApps); err != nil {
		return fmt.Errorf("delete user: count owned apps: %w", err)
	}
	if ownedApps > 0 {
		return ErrUserOwnsApps
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete user rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete user: commit: %w", err)
	}
	committed = true
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
// The INSERT ... ON CONFLICT DO NOTHING plus UPDATE sequence is atomic under
// the database's unique constraint: a concurrent caller cannot race between the
// read and the insert because the constraint is enforced by the engine.
func (s *Store) UpsertSystemUser(username, role string) (*User, error) {
	if !IsSystemUser(username) {
		return nil, fmt.Errorf("upsert system user: %q is not a system username", username)
	}
	if _, err := s.db.Exec(
		`INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?) ON CONFLICT (username) DO NOTHING`,
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
	var id int64
	var createdAt time.Time
	err := s.db.QueryRow(
		`INSERT INTO api_keys (user_id, key_hash, name) VALUES (?, ?, ?) RETURNING id, created_at`,
		p.UserID, p.KeyHash, p.Name,
	).Scan(&id, &createdAt)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("create api key: %w", err)
	}
	return id, createdAt, nil
}

func (s *Store) GetUserByAPIKeyHash(hash string) (*User, error) {
	row := s.db.QueryRow(`
		SELECT u.id, u.username, u.password_hash, u.role, u.display_name, u.email, u.created_at
		FROM users u JOIN api_keys k ON k.user_id = u.id
		WHERE k.key_hash = ?`, hash)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &u.Email, &u.CreatedAt); err != nil {
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
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// IconMime is the MIME type of the app's uploaded icon, or "" when none is
	// set (the UI then renders the generated monogram). The bytes themselves are
	// never loaded into App; they are read on demand via GetAppIcon.
	IconMime                string    `json:"icon_mime,omitempty"`
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
	// LastDeploymentStatus is the status of the app's most-recent deployment row
	// ("succeeded"/"failed"/"pending"), or "" if it has never been deployed. It
	// lets a consumer tell a failed-only deploy from a never-deployed app, which
	// deploy_count (incremented on success only) cannot.
	LastDeploymentStatus string `json:"last_deployment_status,omitempty"`
	// ReplicaPlacement is the per-app replica placement as a JSON object
	// {"tier": count}, or "" when no placement is set (all Replicas on the
	// default tier). The Replicas column remains the authoritative total.
	ReplicaPlacement string `json:"replica_placement,omitempty"`
	// AutoscaleEnabled is true when the autoscale controller may adjust this
	// app's replica count. Off by default; scaling is always opt-in per app.
	AutoscaleEnabled bool `json:"autoscale_enabled"`
	// AutoscaleMinReplicas / AutoscaleMaxReplicas bound the controller's
	// replica count. Both are 0 when autoscaling is disabled.
	AutoscaleMinReplicas int `json:"autoscale_min_replicas"`
	AutoscaleMaxReplicas int `json:"autoscale_max_replicas"`
	// AutoscaleTarget is the target average active sessions per replica as a
	// fraction (0,1] of the per-replica session cap, or 0 to use the
	// runtime-wide default target.
	AutoscaleTarget float64 `json:"autoscale_target"`
	// LastAutoscaleAt is the Unix-epoch seconds of the most recent autoscale
	// action on this app, or 0 if it has never been scaled. The controller reads
	// it as the persisted cooldown so the cooldown survives restart and failover.
	LastAutoscaleAt int64 `json:"last_autoscale_at"`
	// IdentityHeaders is the per-app identity-forwarding override reconciled
	// from the bundle manifest. nil = inherit the global config flag.
	// Effective = global && (IdentityHeaders == nil || *IdentityHeaders).
	IdentityHeaders *bool `json:"identity_headers"`
	// MinWarmReplicas is the pre-warming floor: replicas kept running
	// through idle hibernation. 0 = hibernate fully (the default).
	MinWarmReplicas int `json:"min_warm_replicas"`
	// LastError is a short diagnostic for why the app is "crashed" (a boot
	// error plus the tail of the app log, e.g. a Python traceback). Empty when
	// the app is not crashed; cleared on a successful (re)start.
	LastError string `json:"last_error,omitempty"`
	// CrashedAt is the Unix-epoch seconds of the transition into "crashed", or
	// 0 when the app is not crashed. Cleared alongside LastError on (re)start.
	CrashedAt int64 `json:"crashed_at,omitempty"`
	// WorkerIsolation is the session-isolation mode: "multiplex" (default),
	// "grouped", or "per_session". Non-multiplex modes are demand-driven and
	// single-node only in Phase 1.
	WorkerIsolation              string `json:"worker_isolation"`
	WorkerGroupedSize            int    `json:"worker_grouped_size"`
	WorkerMaxWorkers             int    `json:"worker_max_workers"`
	WorkerMaxSessionLifetimeSecs int    `json:"worker_max_session_lifetime_secs"`

	// EphemeralDataAck records that the operator explicitly accepted ephemeral,
	// task-local app-data for this app. When true, the durable-data guard allows
	// deploying (and pushing data to) this app on a Fargate tier whose storage is
	// ephemeral instead of blocking. Default false.
	EphemeralDataAck bool `json:"ephemeral_data_ack"`
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
		   ORDER BY created_at DESC, id DESC LIMIT 1) AS content_digest,
		(SELECT status FROM deployments WHERE app_id = apps.id
		   ORDER BY created_at DESC, id DESC LIMIT 1) AS last_deployment_status`

// appColumns is the plain apps.* column list shared by every App SELECT, in the
// exact order scanApp expects. It is kept as a single constant so the column
// list and scanApp never drift across the queries below; each query appends
// deploymentSummarySQL for the joined last_deployed_at/current_version/digest.
const appColumns = `id, slug, name, project_slug, owner_id, access, status,
		       replicas, max_sessions_per_replica, deploy_count,
		       hibernate_timeout_minutes,
		       memory_limit_mb, cpu_quota_percent,
		       created_at, updated_at,
		       managed_by, replica_placement,
		       autoscale_enabled, autoscale_min_replicas, autoscale_max_replicas, autoscale_target,
		       last_autoscale_at, identity_headers, min_warm_replicas,
		       last_error, crashed_at, description, icon_mime,
		       worker_isolation, worker_grouped_size, worker_max_workers,
		       worker_max_session_lifetime_secs, ephemeral_data_ack,`

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
	var err error
	if p.ProjectSlug == "" {
		_, err = s.db.Exec(
			`INSERT INTO apps (slug, name, owner_id, access) VALUES (?, ?, ?, ?)`,
			p.Slug, p.Name, p.OwnerID, p.Access,
		)
	} else {
		_, err = s.db.Exec(
			`INSERT INTO apps (slug, name, project_slug, owner_id, access) VALUES (?, ?, ?, ?, ?)`,
			p.Slug, p.Name, p.ProjectSlug, p.OwnerID, p.Access,
		)
	}
	if err != nil {
		if s.d.isUniqueViolation(err) {
			return fmt.Errorf("create app: %w", ErrSlugTaken)
		}
		return fmt.Errorf("create app: %w", err)
	}
	return nil
}

func (s *Store) GetAppBySlug(slug string) (*App, error) {
	defer s.timed("GetAppBySlug")()
	row := s.db.QueryRow(`
		SELECT `+appColumns+deploymentSummarySQL+`
		FROM apps WHERE slug = ?`, slug)
	return scanApp(row)
}

// GetApp is an alias for GetAppBySlug.
func (s *Store) GetApp(slug string) (*App, error) {
	return s.GetAppBySlug(slug)
}

// inPlaceholders returns an "?,?,..." fragment and the args slice for an IN
// clause of n items. The bound DB rewrites `?` to the active dialect. Returns
// ("", nil) for n==0 so callers can short-circuit rather than emit "IN ()".
func inPlaceholders[T any](items []T) (string, []any) {
	if len(items) == 0 {
		return "", nil
	}
	args := make([]any, len(items))
	ph := make([]byte, 0, len(items)*2)
	for i, it := range items {
		if i > 0 {
			ph = append(ph, ',')
		}
		ph = append(ph, '?')
		args[i] = it
	}
	return string(ph), args
}

// GetAppsBySlugs returns the apps for the given slugs in one query (unknown
// slugs are simply absent), so the batch metrics endpoint need not call
// GetAppBySlug per card.
func (s *Store) GetAppsBySlugs(slugs []string) ([]*App, error) {
	ph, args := inPlaceholders(slugs)
	if ph == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT `+appColumns+deploymentSummarySQL+`
		FROM apps WHERE slug IN (`+ph+`)`, args...)
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

func (s *Store) GetAppByID(id int64) (*App, error) {
	row := s.db.QueryRow(`
		SELECT `+appColumns+deploymentSummarySQL+`
		FROM apps WHERE id = ?`, id)
	return scanApp(row)
}

func (s *Store) ListApps(limit, offset int) ([]*App, error) {
	if limit <= 0 {
		limit = s.d.noLimit()
	}
	rows, err := s.db.Query(`
		SELECT `+appColumns+deploymentSummarySQL+`
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

// ListWakingApps returns all apps whose status is 'waking'. Used by the
// active owner's runOnce reconciler to drive apps whose wake was triggered by
// a standby instance (which issues the BeginWake CAS but cannot deploy).
func (s *Store) ListWakingApps() ([]*App, error) {
	rows, err := s.db.Query(`
		SELECT ` + appColumns + deploymentSummarySQL + `
		FROM apps WHERE status = 'waking'`)
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
		SELECT ` + appColumns + deploymentSummarySQL + `
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

// ListHibernatedApps returns all apps whose status is 'hibernated'. Used on
// startup by the warm-restore pass to re-boot and re-freeze apps that were warm
// before a server restart, so their next access is a warm resume rather than a
// cold boot (a frozen process does not survive a service restart).
func (s *Store) ListHibernatedApps() ([]*App, error) {
	rows, err := s.db.Query(`
		SELECT ` + appColumns + deploymentSummarySQL + `
		FROM apps WHERE status = 'hibernated'`)
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

// ListWarmShrunkApps returns apps that currently have warm-parked replicas
// (desired_state='warm') and are still serving (running or degraded). The
// watcher's expansion check iterates exactly this set each tick.
func (s *Store) ListWarmShrunkApps() ([]*App, error) {
	rows, err := s.db.Query(`
		SELECT ` + appColumns + deploymentSummarySQL + `
		FROM apps
		WHERE EXISTS (
			SELECT 1 FROM replicas r
			WHERE r.app_id = apps.id AND r.desired_state = 'warm'
		)
		AND status IN ('running', 'degraded')`)
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

// CountRunningApps returns the number of apps currently in the running state.
// Used by the fleet metrics gauge.
func (s *Store) CountRunningApps() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM apps WHERE status = 'running'`).Scan(&n)
	return n, err
}

// CountCrashedApps returns the number of apps currently in the crashed state -
// apps that exhausted their restart budget and are serving nothing. Used by the
// fleet metrics gauge so operators can alert on the most actionable condition.
func (s *Store) CountCrashedApps() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM apps WHERE status = 'crashed'`).Scan(&n)
	return n, err
}

// CountRunningReplicas returns the number of replica rows currently in the
// running state across all apps. Used by the fleet metrics gauge.
func (s *Store) CountRunningReplicas() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM replicas WHERE status = ?`, ReplicaStatusRunning).Scan(&n)
	return n, err
}

// ListReconcilableApps returns all apps whose status is 'running' or 'degraded'
// - the states the watchdog reconciler may act on. Degraded apps are included so
// a re-placed lost replica (or a recovered crashed slot) can heal the app back
// to running; the narrower ListRunningApps stays reserved for startup recovery,
// which must not re-adopt degraded apps.
func (s *Store) ListReconcilableApps() ([]*App, error) {
	rows, err := s.db.Query(`
		SELECT ` + appColumns + deploymentSummarySQL + `
		FROM apps WHERE status IN ('running', 'degraded')`)
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

// ListAutoscaleApps returns apps that have opted into autoscaling and are in a
// state the controller may act on (running or degraded). Stopped, hibernated,
// and deploying apps are excluded so the controller never resurrects or fights
// another lifecycle path; the scale primitives apply the same status guard.
func (s *Store) ListAutoscaleApps() ([]*App, error) {
	rows, err := s.db.Query(`
		SELECT ` + appColumns + deploymentSummarySQL + `
		FROM apps WHERE autoscale_enabled = 1 AND status IN ('running', 'degraded')`)
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

// SetAppLastAutoscaleAt records the epoch-seconds timestamp of the most recent
// autoscale action for an app, persisting the controller cooldown so it survives
// restart and failover to a standby control-plane instance. A no-op (no error)
// for an unknown slug.
func (s *Store) SetAppLastAutoscaleAt(slug string, epoch int64) error {
	if _, err := s.db.Exec(`UPDATE apps SET last_autoscale_at = ? WHERE slug = ?`, epoch, slug); err != nil {
		return fmt.Errorf("set last_autoscale_at: %w", err)
	}
	return nil
}

// NowEpoch returns the database server's current time as Unix epoch seconds. The
// autoscale cooldown uses it so the armed timestamp (SetAppLastAutoscaleAt) and
// the cooldown check are both measured against a single clock - the DB - immune
// to wall-clock skew between control-plane instances across a failover.
func (s *Store) NowEpoch() (int64, error) {
	var e int64
	if err := s.db.QueryRow(`SELECT ` + s.d.nowEpoch()).Scan(&e); err != nil {
		return 0, fmt.Errorf("db now epoch: %w", err)
	}
	return e, nil
}

// ListDeletingApps returns all apps left in the 'deleting' tombstone state.
// handleDeleteApp marks an app 'deleting' before doing disk cleanup; a crash
// (or a cleanup failure) between the tombstone and the row delete leaves such
// rows behind for startup reconciliation to finish.
func (s *Store) ListDeletingApps() ([]*App, error) {
	rows, err := s.db.Query(`
		SELECT ` + appColumns + deploymentSummarySQL + `
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
		limit = s.d.noLimit()
	}
	rows, err := s.db.Query(`
		SELECT `+appColumns+deploymentSummarySQL+`
		FROM apps
		WHERE access = 'public'
		   OR access = 'shared'
		   OR owner_id = ?
		   OR EXISTS (
		       SELECT 1 FROM app_members
		       WHERE app_slug = apps.slug AND user_id = ?
		   )
		   OR EXISTS (
		       SELECT 1 FROM app_group_access aga
		       JOIN user_groups ug ON ug.group_name = aga.group_name
		       WHERE aga.app_slug = apps.slug AND ug.user_id = ?
		   )
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`, userID, userID, userID, limit, offset)
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
		limit = s.d.noLimit()
	}
	rows, err := s.db.Query(`
		SELECT `+appColumns+deploymentSummarySQL+`
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

// ListViewerBaselineApps returns the apps every authenticated viewer sees
// regardless of per-app membership: access IN ('public','shared'). It powers the
// admin "preview viewer home" scope (GET /api/apps?as=viewer), which shows the
// default viewer experience without impersonating a specific user. Private apps
// (member-only) are intentionally excluded - those vary per viewer.
func (s *Store) ListViewerBaselineApps(limit, offset int) ([]*App, error) {
	if limit <= 0 {
		limit = s.d.noLimit()
	}
	rows, err := s.db.Query(`
		SELECT `+appColumns+deploymentSummarySQL+`
		FROM apps
		WHERE access = 'public' OR access = 'shared'
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
	// Any transition OUT of "crashed" (start, restart, wake, hibernate, stop)
	// clears the recorded crash diagnostic so a stale reason never lingers on a
	// recovered app. A status write TO "crashed" must NOT clear it: crashed
	// transitions go through MarkAppCrashed (which records the reason), and a bare
	// UpdateAppStatus("crashed") - from a current or future caller - must preserve
	// whatever reason is already there rather than wiping it.
	var res sql.Result
	var err error
	if p.Status == "crashed" {
		res, err = s.db.Exec(
			`UPDATE apps SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`,
			p.Status, p.Slug,
		)
	} else {
		res, err = s.db.Exec(
			`UPDATE apps SET status = ?, last_error = '', crashed_at = 0, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`,
			p.Status, p.Slug,
		)
	}
	if err != nil {
		return fmt.Errorf("update app status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkAppCrashed transitions an app into the "crashed" status and records why:
// reason is a short diagnostic (a boot error plus the tail of the app log, e.g.
// a Python traceback). It is the single entry point into the crashed state,
// shared by recovery, warm-restore, and the runtime watchdog when an app's
// replicas cannot be brought up. A "deleting" app is left untouched so an
// in-flight delete always wins. crashed_at is stamped on the database clock.
func (s *Store) MarkAppCrashed(slug, reason string) error {
	epoch, err := s.NowEpoch()
	if err != nil {
		return fmt.Errorf("mark app crashed: %w", err)
	}
	res, err := s.db.Exec(
		`UPDATE apps SET status = 'crashed', last_error = ?, crashed_at = ?, updated_at = CURRENT_TIMESTAMP
		   WHERE slug = ? AND status != 'deleting'`,
		reason, epoch, slug,
	)
	if err != nil {
		return fmt.Errorf("mark app crashed: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// BeginWake atomically transitions a hibernated app to "waking" and reports
// whether THIS caller won the transition. It replaces the watcher's in-memory
// waking guard: exactly one caller (even across a brief two-process control-
// plane overlap during a zero-downtime handoff) gets won=true and performs the
// spawn; everyone else gets false and returns.
func (s *Store) BeginWake(slug string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE apps SET status = 'waking', updated_at = CURRENT_TIMESTAMP
		   WHERE slug = ? AND status = 'hibernated'`,
		slug,
	)
	if err != nil {
		return false, fmt.Errorf("begin wake: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// AbortWake reverts a "waking" app to "hibernated" after a failed wake so a
// later request retries. It is a no-op if the app already moved on (e.g. a
// concurrent stop/delete moved it off "waking"), so it never clobbers a newer
// intent.
func (s *Store) AbortWake(slug string) error {
	if _, err := s.db.Exec(
		`UPDATE apps SET status = 'hibernated', updated_at = CURRENT_TIMESTAMP
		   WHERE slug = ? AND status = 'waking'`,
		slug,
	); err != nil {
		return fmt.Errorf("abort wake: %w", err)
	}
	return nil
}

// FinishWake transitions a "waking" app to "running" and reports whether the
// transition happened. It is conditional on the app still being "waking", so a
// concurrent stop/delete that moved the app off "waking" during the wake is not
// clobbered back to running (won=false, the caller leaves the newer status).
func (s *Store) FinishWake(slug string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE apps SET status = 'running', updated_at = CURRENT_TIMESTAMP
		   WHERE slug = ? AND status = 'waking'`,
		slug,
	)
	if err != nil {
		return false, fmt.Errorf("finish wake: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// HibernateApp atomically transitions a running app to "hibernated" and reports
// whether THIS caller won the transition. The CAS guards against a concurrent
// wake or handoff that already moved the app off "running": if the app is not
// in "running" state the transition is a no-op and won=false is returned.
//
// Callers must issue this CAS BEFORE stopping replicas so that any request
// arriving after the CAS commit hits BeginWake (hibernated->waking) instead of
// an app that is still "running" but has already had its pool removed.
func (s *Store) HibernateApp(slug string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE apps SET status = 'hibernated', updated_at = CURRENT_TIMESTAMP
		   WHERE slug = ? AND status = 'running'`,
		slug,
	)
	if err != nil {
		return false, fmt.Errorf("hibernate app: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
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
	var id int64
	err := s.db.QueryRow(
		`INSERT INTO deployments (app_id, version, bundle_dir, status) VALUES (?, ?, ?, ?) RETURNING id`,
		p.AppID, p.Version, p.BundleDir, status,
	).Scan(&id)
	if err != nil {
		return nil, err
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
	var id int64
	err := s.db.QueryRow(
		`INSERT INTO deployments (app_id, version, bundle_dir, status) VALUES (?, ?, ?, ?) RETURNING id`,
		appID, version, bundleDir, DeploymentPending,
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("begin deployment: %w", err)
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
// adopted as the live bundle. It is a no-op on a row that is not pending. The
// failure reason is left empty; prefer FailDeploymentWithReason so the cause is
// recoverable later.
func (s *Store) FailDeployment(id int64) error {
	return s.FailDeploymentWithReason(id, "")
}

// FailDeploymentWithReason marks an aborted pending deployment as failed and
// records why, so a developer can diagnose the failure after the fact via the
// deployments history. It is a no-op on a row that is not pending.
func (s *Store) FailDeploymentWithReason(id int64, reason string) error {
	_, err := s.db.Exec(
		`UPDATE deployments SET status = ?, failure_reason = ? WHERE id = ? AND status = ?`,
		DeploymentFailed, reason, id, DeploymentPending)
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
	// release_number is the row's rank among the app's SUCCEEDED deployments by id
	// (a human-friendly v1, v2, … shown instead of the epoch version); NULL for
	// failed/pending rows so a failed attempt never consumes a release number.
	// Ordered by id DESC to match ListDeployments' authoritative live-history order
	// (the UI marks the first succeeded row Current).
	rows, err := s.db.Query(`
		SELECT d.id, d.version, d.status, d.failure_reason, d.created_at,
		       CASE WHEN d.status = 'succeeded' THEN (
		           SELECT COUNT(*) FROM deployments d2
		           WHERE d2.app_id = d.app_id AND d2.status = 'succeeded' AND d2.id <= d.id
		       ) END AS release_number
		FROM deployments d
		JOIN apps a ON a.id = d.app_id
		WHERE a.slug = ?
		ORDER BY d.id DESC`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]DeploymentSummary, 0)
	for rows.Next() {
		var d DeploymentSummary
		var release sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Version, &d.Status, &d.FailureReason, &d.CreatedAt, &release); err != nil {
			return nil, err
		}
		if release.Valid {
			n := release.Int64
			d.ReleaseNumber = &n
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// DeploymentSummary is a public view of a deployment row, safe for API responses.
type DeploymentSummary struct {
	ID      int64  `json:"id"`
	Version string `json:"version"`
	Status  string `json:"status"`
	// FailureReason explains why a failed deployment failed; empty for pending
	// and succeeded rows. Always present so the field is a stable, addressable
	// part of the deployments contract (machine consumers and CLI --fields).
	FailureReason string `json:"failure_reason"`
	// ReleaseNumber is the human-friendly v1/v2/… rank among the app's succeeded
	// deployments; nil for failed/pending rows (a failed attempt has no release).
	ReleaseNumber *int64    `json:"release_number"`
	CreatedAt     time.Time `json:"created_at"`
}

// CurrentRelease returns the app's current release number (count of succeeded
// deployments), and the timestamp + epoch version of the live (newest succeeded)
// deployment; ok = whether any succeeded deployment exists. Powers the detail
// header "vN · date" and the bundle-id hover.
//
// One statement so the count and the live row are a consistent snapshot (a
// promotion racing between two queries can't pair an old number with a new date).
// The live row is the newest SUCCEEDED by id - not MAX(created_at) and not the
// status-agnostic current_version - so a failed/pending latest attempt is ignored
// and a rollback (a new succeeded row reusing an old version) reports its own date.
// Reading the row's created_at column (vs an aggregate) avoids the modernc-sqlite
// string-scan trap.
func (s *Store) CurrentRelease(appID int64) (number int, releasedAt time.Time, version string, ok bool) {
	var n int
	var at time.Time
	var v string
	err := s.db.QueryRow(`
		SELECT (SELECT COUNT(*) FROM deployments WHERE app_id = ? AND status = 'succeeded'),
		       created_at, version
		FROM deployments
		WHERE app_id = ? AND status = 'succeeded'
		ORDER BY id DESC LIMIT 1`, appID, appID).Scan(&n, &at, &v)
	if err != nil {
		// No succeeded deploy (sql.ErrNoRows) or a read error → omit the fields.
		return 0, time.Time{}, "", false
	}
	return n, at, v, true
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
	var exists bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM deployments WHERE app_id = ?)`, appID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
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

// GrantAppAccess adds userID as a member of slug with the default viewer role.
// It is idempotent (ON CONFLICT DO NOTHING): re-granting an existing member is a
// no-op and never changes their current role. Use GrantAppAccessWithRole or
// SetMemberRole to set a specific role.
func (s *Store) GrantAppAccess(slug string, userID int64) error {
	_, err := s.db.Exec(
		`INSERT INTO app_members (app_slug, user_id) VALUES (?, ?) ON CONFLICT (app_slug, user_id) DO NOTHING`, slug, userID)
	if err != nil {
		return fmt.Errorf("grant app access: %w", err)
	}
	return nil
}

// GrantAppAccessWithRole grants userID access to slug with the given role,
// upserting the role when the user is already a member. role must be one of
// ValidMemberRoles (validated by callers).
func (s *Store) GrantAppAccessWithRole(slug string, userID int64, role string) error {
	_, err := s.db.Exec(
		`INSERT INTO app_members (app_slug, user_id, role) VALUES (?, ?, ?)
		 ON CONFLICT (app_slug, user_id) DO UPDATE SET role = excluded.role`,
		slug, userID, role)
	if err != nil {
		return fmt.Errorf("grant app access with role: %w", err)
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
		limit = s.d.noLimit()
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

// UserCanAccessApp returns true if userID is the app's owner, has been
// explicitly granted access via app_members, or has access via a group rule
// (app_group_access joined through user_groups). Used by both the API view
// check and the /app/* proxy.
func (s *Store) UserCanAccessApp(slug string, userID int64) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT 1 FROM apps WHERE slug = ? AND owner_id = ?
			UNION ALL
			SELECT 1 FROM app_members WHERE app_slug = ? AND user_id = ?
			UNION ALL
			SELECT 1 FROM app_group_access aga
			    JOIN user_groups ug ON ug.group_name = aga.group_name
			    WHERE aga.app_slug = ? AND ug.user_id = ?
		)`, slug, userID, slug, userID, slug, userID).Scan(&count)
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

// SetMemberRole updates the role of an existing explicit app member. Returns
// ErrNotFound when the user has no app_members row for slug.
func (s *Store) SetMemberRole(slug string, userID int64, role string) error {
	result, err := s.db.Exec(
		`UPDATE app_members SET role = ? WHERE app_slug = ? AND user_id = ?`,
		role, slug, userID)
	if err != nil {
		return fmt.Errorf("set member role: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set member role rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
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

// SetAppDescription updates the app's optional one-line description (shown on
// the Launchpad). An empty string clears it.
func (s *Store) SetAppDescription(slug, description string) error {
	result, err := s.db.Exec(
		`UPDATE apps SET description = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`, description, slug)
	if err != nil {
		return fmt.Errorf("set app description: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set app description rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAppIcon stores the app's icon bytes and MIME type and bumps updated_at,
// which the UI reads as a cache-buster on the icon URL so a replaced icon shows
// immediately. Returns ErrNotFound if no app has the slug.
func (s *Store) SetAppIcon(slug, mime string, data []byte) error {
	result, err := s.db.Exec(
		`UPDATE apps SET icon_mime = ?, icon_data = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`,
		mime, data, slug)
	if err != nil {
		return fmt.Errorf("set app icon: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set app icon rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearAppIcon removes the app's icon (reverting the UI to the monogram) and
// bumps updated_at. Returns ErrNotFound if no app has the slug. Clearing an
// already-iconless app is a no-op success.
func (s *Store) ClearAppIcon(slug string) error {
	result, err := s.db.Exec(
		`UPDATE apps SET icon_mime = '', icon_data = NULL, updated_at = CURRENT_TIMESTAMP WHERE slug = ?`,
		slug)
	if err != nil {
		return fmt.Errorf("clear app icon: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("clear app icon rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetAppIcon returns the stored icon bytes and MIME type. It returns ErrNotFound
// when the app does not exist OR has no icon set, so callers treat "no app" and
// "no icon" identically (both yield a 404 from the serve handler, and the UI
// then shows the monogram).
func (s *Store) GetAppIcon(slug string) (mime string, data []byte, err error) {
	row := s.db.QueryRow(`SELECT icon_mime, icon_data FROM apps WHERE slug = ?`, slug)
	var m sql.NullString
	var d []byte
	if err := row.Scan(&m, &d); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, ErrNotFound
		}
		return "", nil, fmt.Errorf("get app icon: %w", err)
	}
	if !m.Valid || m.String == "" || len(d) == 0 {
		return "", nil, ErrNotFound
	}
	return m.String, d, nil
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
		`INSERT INTO oauth_accounts (user_id, provider, provider_id) VALUES (?, ?, ?) ON CONFLICT (provider, provider_id) DO NOTHING`,
		p.UserID, p.Provider, p.ProviderID,
	)
	if err != nil {
		return fmt.Errorf("create oauth account: %w", err)
	}
	return nil
}

func (s *Store) GetUserByOAuthAccount(provider, providerID string) (*User, error) {
	row := s.db.QueryRow(`
		SELECT u.id, u.username, u.password_hash, u.role, u.display_name, u.email, u.created_at
		FROM users u
		JOIN oauth_accounts o ON o.user_id = u.id
		WHERE o.provider = ? AND o.provider_id = ?`, provider, providerID)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &u.Email, &u.CreatedAt); err != nil {
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

	// beginWrite takes the write lock up front (BEGIN IMMEDIATE on SQLite,
	// standard transaction on Postgres), so concurrent first logins for the
	// same identity serialize here rather than racing to the decisive
	// linked-account read. lockKey=0: the unique constraint handles the
	// single-row upsert race without a Postgres advisory lock.
	tx, err := s.d.beginWrite(ctx, s.rawDB(), 0)
	if err != nil {
		return nil, false, fmt.Errorf("first-login provisioning: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	scanUser := func(row *sql.Row) (*User, error) {
		var u User
		if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &u.Email, &u.CreatedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		return &u, nil
	}
	const linkedQuery = `
		SELECT u.id, u.username, u.password_hash, u.role, u.display_name, u.email, u.created_at
		FROM users u
		JOIN oauth_accounts o ON o.user_id = u.id
		WHERE o.provider = ? AND o.provider_id = ?`
	const userByIDQuery = `
		SELECT id, username, password_hash, role, display_name, email, created_at
		FROM users WHERE id = ?`

	if u, gerr := scanUser(tx.QueryRowContext(ctx, linkedQuery, p.Provider, p.ProviderID)); gerr == nil {
		if cerr := tx.Commit(); cerr != nil {
			return nil, false, fmt.Errorf("commit: %w", cerr)
		}
		committed = true
		return u, false, nil
	} else if !errors.Is(gerr, ErrNotFound) {
		return nil, false, gerr
	}

	var userID int64
	for _, name := range p.UsernameCandidates {
		// Wrap each attempt in a savepoint so a unique-constraint violation
		// does not abort the outer transaction on Postgres (which marks any
		// transaction with a failed statement as aborted until rolled back).
		if _, serr := tx.ExecContext(ctx, "SAVEPOINT sp_user_insert"); serr != nil {
			return nil, false, fmt.Errorf("create user: savepoint: %w", serr)
		}
		ierr := tx.QueryRowContext(ctx,
			`INSERT INTO users (username, password_hash, role) VALUES (?, '', ?) RETURNING id`,
			name, p.Role,
		).Scan(&userID)
		if ierr != nil {
			_, _ = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT sp_user_insert")
			_, _ = tx.ExecContext(ctx, "RELEASE SAVEPOINT sp_user_insert")
			if s.d.isUniqueViolation(ierr) {
				userID = 0
				continue // username taken, try the next candidate
			}
			return nil, false, fmt.Errorf("create user: %w", ierr)
		}
		if _, rerr := tx.ExecContext(ctx, "RELEASE SAVEPOINT sp_user_insert"); rerr != nil {
			return nil, false, fmt.Errorf("create user: release savepoint: %w", rerr)
		}
		break
	}
	if userID == 0 {
		return nil, false, fmt.Errorf("create user: all candidate usernames in use")
	}

	if _, lerr := tx.ExecContext(ctx,
		`INSERT INTO oauth_accounts (user_id, provider, provider_id) VALUES (?, ?, ?)`,
		userID, p.Provider, p.ProviderID,
	); lerr != nil {
		if s.d.isUniqueViolation(lerr) {
			// Lost a concurrent first-login race (rare: beginWrite serializes
			// contenders, so the loser normally sees the link in linkedQuery
			// above). Roll back and use a separate query to resolve.
			_ = tx.Rollback()
			committed = true // suppress the deferred Rollback
			existing, eerr := s.GetUserByOAuthAccount(p.Provider, p.ProviderID)
			if eerr != nil {
				return nil, false, fmt.Errorf("resolve raced oauth user: %w", eerr)
			}
			return existing, false, nil
		}
		return nil, false, fmt.Errorf("link oauth account: %w", lerr)
	}

	// Read the created row within the transaction to confirm the inserted ID.
	created, gerr := scanUser(tx.QueryRowContext(ctx, userByIDQuery, userID))
	if gerr != nil {
		return nil, false, fmt.Errorf("read created user: %w", gerr)
	}
	if cerr := tx.Commit(); cerr != nil {
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
	s.db.Exec(`DELETE FROM oauth_states WHERE created_at < ` + s.d.nowMinusSeconds(600)) //nolint:errcheck
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
	AuditDataPush       = "data.push"
	AuditDataDelete     = "data.delete"
	AuditAppIconSet     = "app.icon.set"
	AuditAppIconCleared = "app.icon.clear"
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

// LogAuditEvent inserts an audit event. A write failure is logged and surfaced
// via the audit-error hook but does not fail the caller — audit recording must
// never break normal operation.
func (s *Store) LogAuditEvent(p AuditEventParams) {
	_, err := s.db.Exec(`
		INSERT INTO audit_events (user_id, action, resource_type, resource_id, detail, ip_address)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.UserID, p.Action, p.ResourceType, p.ResourceID, p.Detail, p.IPAddress)
	if err != nil {
		slog.Error("audit_log_write_failed", "action", p.Action, "err", err)
		if s.auditErrHook != nil {
			s.auditErrHook()
		}
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

// PruneAuditEvents deletes audit events older than the retention window and
// returns the number removed. A non-positive retention is a no-op (returns 0,
// nil) so the operator can keep the full compliance trail by default. The
// cutoff is computed on the DB clock, matching how created_at is stamped.
func (s *Store) PruneAuditEvents(retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	secs := int(retention.Seconds())
	res, err := s.db.Exec(`DELETE FROM audit_events WHERE created_at < ` + s.d.nowMinusSeconds(secs))
	if err != nil {
		return 0, fmt.Errorf("prune audit events: %w", err)
	}
	return res.RowsAffected()
}

// LatestAutoscaleEvent returns the most-recent autoscale_scale_up or
// autoscale_scale_down audit event for the named app slug, or a zero-value
// AuditEvent and false if no such event exists.
//
// The ORDER BY created_at DESC, id DESC with LIMIT 1 relies on SQLite's
// backward scan of the default created_at ordering and is fine at typical
// audit-table sizes. A composite index on (resource_id, action, created_at DESC)
// would speed this up if the audit_events table grows very large.
func (s *Store) LatestAutoscaleEvent(slug string) (AuditEvent, bool, error) {
	row := s.db.QueryRow(`
		SELECT ae.id, ae.user_id, u.username,
		       ae.action, ae.resource_type, ae.resource_id,
		       ae.detail, ae.ip_address, ae.created_at
		FROM audit_events ae
		LEFT JOIN users u ON u.id = ae.user_id
		WHERE ae.resource_type = 'app'
		  AND ae.resource_id   = ?
		  AND ae.action IN ('autoscale_scale_up', 'autoscale_scale_down')
		ORDER BY ae.created_at DESC, ae.id DESC
		LIMIT 1`, slug)
	var ev AuditEvent
	err := row.Scan(
		&ev.ID, &ev.UserID, &ev.Username,
		&ev.Action, &ev.ResourceType, &ev.ResourceID,
		&ev.Detail, &ev.IPAddress, &ev.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return AuditEvent{}, false, nil
	}
	if err != nil {
		return AuditEvent{}, false, fmt.Errorf("latest autoscale event: %w", err)
	}
	return ev, true, nil
}

// LatestAutoscaleEventForSlugs returns the single most recent autoscale event
// per slug in one query (greatest-n-per-group via a window function; supported
// on SQLite >= 3.25 and Postgres). It is the batch form of LatestAutoscaleEvent
// for the metrics endpoint. Slugs with no autoscale events are absent from the
// map.
func (s *Store) LatestAutoscaleEventForSlugs(slugs []string) (map[string]AuditEvent, error) {
	ph, args := inPlaceholders(slugs)
	if ph == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT id, user_id, username, action, resource_type, resource_id,
		       detail, ip_address, created_at
		FROM (
			SELECT ae.id, ae.user_id, u.username, ae.action, ae.resource_type,
			       ae.resource_id, ae.detail, ae.ip_address, ae.created_at,
			       ROW_NUMBER() OVER (PARTITION BY ae.resource_id
			                          ORDER BY ae.created_at DESC, ae.id DESC) AS rn
			FROM audit_events ae
			LEFT JOIN users u ON u.id = ae.user_id
			WHERE ae.resource_type = 'app'
			  AND ae.resource_id IN (`+ph+`)
			  AND ae.action IN ('autoscale_scale_up', 'autoscale_scale_down')
		) t
		WHERE t.rn = 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]AuditEvent, len(slugs))
	for rows.Next() {
		var ev AuditEvent
		if err := rows.Scan(
			&ev.ID, &ev.UserID, &ev.Username,
			&ev.Action, &ev.ResourceType, &ev.ResourceID,
			&ev.Detail, &ev.IPAddress, &ev.CreatedAt,
		); err != nil {
			return nil, err
		}
		out[ev.ResourceID] = ev
	}
	return out, rows.Err()
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
		VALUES (?, ?, ?, ?, `+s.d.nowEpoch()+`)
		ON CONFLICT(app_id, key) DO UPDATE SET
			value      = excluded.value,
			is_secret  = excluded.is_secret,
			updated_at = `+s.d.nowEpoch(),
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
	// ReplicaStatusSuspended marks a replica that is hibernated but resumable: its
	// warmed memory was snapshotted/frozen and host RAM freed, so wake can Resume
	// it instead of cold-booting. Distinct from "stopped", which must cold-boot.
	ReplicaStatusSuspended = "suspended"
)

// ReplicaDesiredWarm marks a replica deliberately stopped by warm-shrink:
// idle hibernation kept the app's warm floor running and parked this one.
// Reconcile and recovery treat warm rows as healthy stopped capacity, and
// warm expansion (not the crash watchdog) boots them back.
const ReplicaDesiredWarm = "warm"

// FleetReplicaCount is one (tier, provider, status) bucket of the replicas
// table, used by the fleet-health overview to break replica counts down per
// backend without an N+1 per-app scan.
type FleetReplicaCount struct {
	Tier     string
	Provider string
	Status   string
	Count    int
}

// FleetReplicaCounts returns replica counts grouped by tier, provider, and
// status across every app, in a single query.
func (s *Store) FleetReplicaCounts() ([]FleetReplicaCount, error) {
	rows, err := s.db.Query(`SELECT tier, provider, status, COUNT(*) FROM replicas GROUP BY tier, provider, status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FleetReplicaCount
	for rows.Next() {
		var c FleetReplicaCount
		if err := rows.Scan(&c.Tier, &c.Provider, &c.Status, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AppLostReplicas is one app's lost-replica count on a tier, slug-resolved.
type AppLostReplicas struct {
	Slug string
	Tier string
	Lost int
}

// AppsWithLostReplicas returns, per app and tier, the count of replicas in the
// lost state, ordered by slug. Drives the fleet-health "degraded apps" list.
func (s *Store) AppsWithLostReplicas() ([]AppLostReplicas, error) {
	rows, err := s.db.Query(`
		SELECT a.slug, r.tier, COUNT(*)
		FROM replicas r JOIN apps a ON a.id = r.app_id
		WHERE r.status = ?
		GROUP BY a.slug, r.tier
		ORDER BY a.slug, r.tier`, ReplicaStatusLost)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppLostReplicas
	for rows.Next() {
		var a AppLostReplicas
		if err := rows.Scan(&a.Slug, &a.Tier, &a.Lost); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

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
	// Reason is a derived, presentation-only annotation set by the read layer
	// (e.g. "worker unavailable" for a lost replica whose tier has no healthy
	// worker). It is never scanned from or persisted to the database.
	Reason string `json:"reason,omitempty"`
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.d.nowEpoch()+`)
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

// ListReplicasForApps returns the replicas for many apps in one query, grouped
// by app ID and ordered by index within each app - the batch form of
// ListReplicas for the metrics endpoint. Apps with no replicas are simply
// absent from the map.
func (s *Store) ListReplicasForApps(appIDs []int64) (map[int64][]*Replica, error) {
	ph, args := inPlaceholders(appIDs)
	if ph == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT app_id, idx, pid, port, status, provider, tier,
		       endpoint_url, worker_id, app_version, desired_state,
		       deployment_id, updated_at
		FROM replicas WHERE app_id IN (`+ph+`) ORDER BY app_id, idx`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]*Replica, len(appIDs))
	for rows.Next() {
		var r Replica
		var updatedAt int64
		if err := rows.Scan(&r.AppID, &r.Index, &r.PID, &r.Port, &r.Status,
			&r.Provider, &r.Tier, &r.EndpointURL, &r.WorkerID, &r.AppVersion,
			&r.DesiredState, &r.DeploymentID, &updatedAt); err != nil {
			return nil, err
		}
		r.UpdatedAt = time.Unix(updatedAt, 0)
		out[r.AppID] = append(out[r.AppID], &r)
	}
	return out, rows.Err()
}

// SuspendedReplica identifies one suspended replica with the app slug needed to
// stop it. Used by the warm-wake GC to evict the oldest suspended replicas when
// the configured cap is exceeded.
type SuspendedReplica struct {
	Slug      string
	AppID     int64
	Index     int
	UpdatedAt time.Time
}

// ListSuspendedReplicas returns every replica whose status is 'suspended',
// joined to the parent app for its slug, ordered oldest-first by updated_at so
// the GC evicts the longest-suspended replicas first.
func (s *Store) ListSuspendedReplicas() ([]SuspendedReplica, error) {
	rows, err := s.db.Query(`
		SELECT a.slug, r.app_id, r.idx, r.updated_at
		FROM replicas r
		JOIN apps a ON a.id = r.app_id
		WHERE r.status = 'suspended'
		ORDER BY r.updated_at ASC, a.slug, r.idx`)
	if err != nil {
		return nil, fmt.Errorf("list suspended replicas: %w", err)
	}
	defer rows.Close()
	var out []SuspendedReplica
	for rows.Next() {
		var sr SuspendedReplica
		var updatedAt int64
		if err := rows.Scan(&sr.Slug, &sr.AppID, &sr.Index, &updatedAt); err != nil {
			return nil, err
		}
		sr.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, sr)
	}
	return out, rows.Err()
}

// RoutableReplica pairs a Replica row with the app slug it belongs to, plus
// the app-level values the pool syncer needs to configure the pool without a
// separate per-app lookup.
type RoutableReplica struct {
	Slug                  string
	AppMaxSessionsPerRepl int   // apps.max_sessions_per_replica
	AppIdentityHeaders    *bool // apps.identity_headers (nil = inherit global)
	Replica               *Replica
}

// ListRoutableReplicas returns every replica whose status is 'running' or
// 'draining', joined to the parent app to carry the slug and pool settings.
// The result spans all apps regardless of the parent app's own status, so a
// degraded app (the watcher marks apps degraded when a replica crashes) with
// surviving running replicas stays routable. Ordered by app slug then replica
// index.
func (s *Store) ListRoutableReplicas() ([]RoutableReplica, error) {
	rows, err := s.db.Query(`
		SELECT a.slug, a.max_sessions_per_replica, a.identity_headers,
		       r.app_id, r.idx, r.pid, r.port, r.status, r.provider, r.tier,
		       r.endpoint_url, r.worker_id, r.app_version, r.desired_state,
		       r.deployment_id, r.updated_at
		FROM replicas r
		JOIN apps a ON a.id = r.app_id
		WHERE r.status IN ('running', 'draining')
		ORDER BY a.slug, r.idx`)
	if err != nil {
		return nil, fmt.Errorf("list routable replicas: %w", err)
	}
	defer rows.Close()
	var out []RoutableReplica
	for rows.Next() {
		var slug string
		var maxSess int
		var identityHeaders *bool
		var r Replica
		var updatedAt int64
		if err := rows.Scan(&slug, &maxSess, &identityHeaders,
			&r.AppID, &r.Index, &r.PID, &r.Port, &r.Status,
			&r.Provider, &r.Tier, &r.EndpointURL, &r.WorkerID, &r.AppVersion,
			&r.DesiredState, &r.DeploymentID, &updatedAt); err != nil {
			return nil, fmt.Errorf("list routable replicas scan: %w", err)
		}
		r.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, RoutableReplica{Slug: slug, AppMaxSessionsPerRepl: maxSess, AppIdentityHeaders: identityHeaders, Replica: &r})
	}
	if out == nil {
		out = []RoutableReplica{}
	}
	return out, rows.Err()
}

// AppHasRunningReplica reports whether the app identified by slug has at
// least one replica row with status='running'. Used by the clustered
// app-readiness probe so all instances answer consistently from the DB instead
// of relying on a locally-observed WebSocket handshake.
func (s *Store) AppHasRunningReplica(slug string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM replicas r
			JOIN apps a ON a.id = r.app_id
			WHERE a.slug = ? AND r.status = 'running'
		)`, slug).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("app has running replica: %w", err)
	}
	return exists, nil
}

// DeleteReplica removes the replica at the given index for an app.
func (s *Store) DeleteReplica(appID int64, index int) error {
	_, err := s.db.Exec(`DELETE FROM replicas WHERE app_id = ? AND idx = ?`, appID, index)
	if err != nil {
		return fmt.Errorf("delete replica: %w", err)
	}
	return nil
}

// SetReplicaDesiredState updates the desired_state column for a single replica
// row identified by (app_id, idx). It is used by the distributed scale-down
// path to record drain intent before the stop, so other instances' pool syncers
// can observe the intent and stop routing new sessions to the slot. Callers
// pass "draining" before the drain wait and "running" to revert on stop failure.
func (s *Store) SetReplicaDesiredState(appID int64, idx int, state string) error {
	_, err := s.db.Exec(
		`UPDATE replicas SET desired_state = ? WHERE app_id = ? AND idx = ?`,
		state, appID, idx,
	)
	if err != nil {
		return fmt.Errorf("set replica desired state: %w", err)
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

// UpdateAppMinWarmReplicas sets the pre-warming floor for the app. The watcher
// never hibernates more replicas than (apps.replicas - min_warm_replicas).
func (s *Store) UpdateAppMinWarmReplicas(appID int64, n int) error {
	res, err := s.db.Exec(
		`UPDATE apps SET min_warm_replicas = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		n, appID,
	)
	if err != nil {
		return fmt.Errorf("update min_warm_replicas: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateAppEphemeralDataAck records (or clears) the operator's explicit
// acknowledgement that this app's data may be ephemeral, task-local storage.
// It is the escape hatch consulted by the durable-data guard.
func (s *Store) UpdateAppEphemeralDataAck(appID int64, ack bool) error {
	res, err := s.db.Exec(
		`UPDATE apps SET ephemeral_data_ack = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		boolToInt(ack), appID,
	)
	if err != nil {
		return fmt.Errorf("update ephemeral_data_ack: %w", err)
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

// SetAppAutoscaleParams carries the per-app autoscale settings written in one
// update. When Enabled is false the bounds and target are still persisted so a
// re-enable can restore the operator's last choice without re-entering them.
type SetAppAutoscaleParams struct {
	AppID       int64
	Enabled     bool
	MinReplicas int
	MaxReplicas int
	Target      float64
}

// SetAppAutoscale persists an app's autoscale configuration. Validation of the
// bounds and target (min >= 1, max <= runtime ceiling, min <= max, target in
// (0,1]) is the API layer's responsibility; this only writes the values.
func (s *Store) SetAppAutoscale(p SetAppAutoscaleParams) error {
	res, err := s.db.Exec(
		`UPDATE apps SET autoscale_enabled = ?, autoscale_min_replicas = ?,
		        autoscale_max_replicas = ?, autoscale_target = ?,
		        updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		boolToInt(p.Enabled), p.MinReplicas, p.MaxReplicas, p.Target, p.AppID,
	)
	if err != nil {
		return fmt.Errorf("set autoscale: %w", err)
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

	// SetIdentityHeaders is true on every manifest apply (the field
	// reconciles unconditionally: key removed => NULL => inherit global).
	SetIdentityHeaders bool
	IdentityHeaders    *bool

	SetMinWarmReplicas bool
	MinWarmReplicas    int

	// Resource limits reconcile like replicas (declared-only). The value is a
	// *int so a non-nil pointer (including 0 = explicit unlimited) writes the
	// column and a nil pointer with Set*=true clears it to NULL — the latter is
	// how the failed-deploy revert restores a pre-manifest inherit state.
	SetMemoryLimitMB bool
	MemoryLimitMB    *int

	SetCPUQuotaPercent bool
	CPUQuotaPercent    *int

	// Autoscale reconciles atomically: SetAutoscale=true writes all four
	// autoscale_* columns in one UPDATE (the manifest block is the complete
	// policy, mirroring SetAppAutoscale). SetAutoscale=false leaves the stored
	// policy - including anything set via `apps set --autoscale` - untouched.
	SetAutoscale         bool
	AutoscaleEnabled     bool
	AutoscaleMinReplicas int
	AutoscaleMaxReplicas int
	AutoscaleTarget      float64

	SetWorkerIsolation           bool
	WorkerIsolation              string
	SetWorkerGroupedSize         bool
	WorkerGroupedSize            int
	SetWorkerMaxWorkers          bool
	WorkerMaxWorkers             int
	SetWorkerMaxSessionLifetime  bool
	WorkerMaxSessionLifetimeSecs int
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

	if p.SetIdentityHeaders {
		if _, err := tx.Exec(
			`UPDATE apps SET identity_headers = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.IdentityHeaders, p.AppID,
		); err != nil {
			return fmt.Errorf("update identity_headers: %w", err)
		}
	}

	if p.SetMinWarmReplicas {
		if _, err := tx.Exec(
			`UPDATE apps SET min_warm_replicas = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.MinWarmReplicas, p.AppID,
		); err != nil {
			return fmt.Errorf("update min_warm_replicas: %w", err)
		}
	}

	if p.SetMemoryLimitMB {
		if _, err := tx.Exec(
			`UPDATE apps SET memory_limit_mb = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.MemoryLimitMB, p.AppID,
		); err != nil {
			return fmt.Errorf("update memory_limit_mb: %w", err)
		}
	}

	if p.SetCPUQuotaPercent {
		if _, err := tx.Exec(
			`UPDATE apps SET cpu_quota_percent = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.CPUQuotaPercent, p.AppID,
		); err != nil {
			return fmt.Errorf("update cpu_quota_percent: %w", err)
		}
	}

	if p.SetAutoscale {
		if _, err := tx.Exec(
			`UPDATE apps SET autoscale_enabled = ?, autoscale_min_replicas = ?,
			        autoscale_max_replicas = ?, autoscale_target = ?,
			        updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			boolToInt(p.AutoscaleEnabled), p.AutoscaleMinReplicas, p.AutoscaleMaxReplicas, p.AutoscaleTarget, p.AppID,
		); err != nil {
			return fmt.Errorf("update autoscale: %w", err)
		}
	}

	if p.SetWorkerIsolation {
		if _, err := tx.Exec(
			`UPDATE apps SET worker_isolation = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.WorkerIsolation, p.AppID,
		); err != nil {
			return fmt.Errorf("update worker_isolation: %w", err)
		}
	}
	if p.SetWorkerGroupedSize {
		if _, err := tx.Exec(
			`UPDATE apps SET worker_grouped_size = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.WorkerGroupedSize, p.AppID,
		); err != nil {
			return fmt.Errorf("update worker_grouped_size: %w", err)
		}
	}
	if p.SetWorkerMaxWorkers {
		if _, err := tx.Exec(
			`UPDATE apps SET worker_max_workers = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.WorkerMaxWorkers, p.AppID,
		); err != nil {
			return fmt.Errorf("update worker_max_workers: %w", err)
		}
	}
	if p.SetWorkerMaxSessionLifetime {
		if _, err := tx.Exec(
			`UPDATE apps SET worker_max_session_lifetime_secs = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.WorkerMaxSessionLifetimeSecs, p.AppID,
		); err != nil {
			return fmt.Errorf("update worker_max_session_lifetime_secs: %w", err)
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

	SetMinWarmReplicas bool
	MinWarmReplicas    int

	SetWorkerIsolation           bool
	WorkerIsolation              string
	SetWorkerGroupedSize         bool
	WorkerGroupedSize            int
	SetWorkerMaxWorkers          bool
	WorkerMaxWorkers             int
	SetWorkerMaxSessionLifetime  bool
	WorkerMaxSessionLifetimeSecs int
}

// PatchAppSettings applies any subset of the user-editable app settings in a
// single SQLite transaction, so a failure partway through cannot leave the
// row half-updated. It returns the app's prior status and replica count
// (read inside the same transaction) so the caller can decide whether a
// running pool needs a redeploy. Returns ErrNotFound if no app has the slug.
func (s *Store) PatchAppSettings(p PatchAppSettingsParams) (priorStatus string, priorReplicas int, priorMem, priorCPU *int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", 0, nil, nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var appID int64
	var curMem, curCPU sql.NullInt64
	if err := tx.QueryRow(
		`SELECT id, status, replicas, memory_limit_mb, cpu_quota_percent FROM apps WHERE slug = ?`,
		p.Slug,
	).Scan(&appID, &priorStatus, &priorReplicas, &curMem, &curCPU); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, nil, nil, ErrNotFound
		}
		return "", 0, nil, nil, fmt.Errorf("load app: %w", err)
	}

	if p.SetHibernate {
		if _, err := tx.Exec(
			`UPDATE apps SET hibernate_timeout_minutes = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.HibernateMinutes, appID,
		); err != nil {
			return "", 0, nil, nil, fmt.Errorf("update hibernate: %w", err)
		}
	}
	if p.SetName {
		if _, err := tx.Exec(
			`UPDATE apps SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.Name, appID,
		); err != nil {
			return "", 0, nil, nil, fmt.Errorf("update name: %w", err)
		}
	}
	if p.SetProjectSlug {
		if _, err := tx.Exec(
			`UPDATE apps SET project_slug = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.ProjectSlug, appID,
		); err != nil {
			return "", 0, nil, nil, fmt.Errorf("update project_slug: %w", err)
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
			return "", 0, nil, nil, fmt.Errorf("update resource limits: %w", err)
		}
	}
	if p.SetReplicas {
		if p.Replicas < priorReplicas {
			if _, err := tx.Exec(
				`DELETE FROM replicas WHERE app_id = ? AND idx >= ?`,
				appID, p.Replicas,
			); err != nil {
				return "", 0, nil, nil, fmt.Errorf("prune replicas: %w", err)
			}
		}
		if _, err := tx.Exec(
			`UPDATE apps SET replicas = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.Replicas, appID,
		); err != nil {
			return "", 0, nil, nil, fmt.Errorf("update replicas: %w", err)
		}
	}
	if p.SetMaxSessions {
		if _, err := tx.Exec(
			`UPDATE apps SET max_sessions_per_replica = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.MaxSessions, appID,
		); err != nil {
			return "", 0, nil, nil, fmt.Errorf("update max_sessions_per_replica: %w", err)
		}
	}

	if p.SetMinWarmReplicas {
		if _, err := tx.Exec(
			`UPDATE apps SET min_warm_replicas = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.MinWarmReplicas, appID,
		); err != nil {
			return "", 0, nil, nil, fmt.Errorf("update min_warm_replicas: %w", err)
		}
	}
	if p.SetWorkerIsolation {
		if _, err := tx.Exec(
			`UPDATE apps SET worker_isolation = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.WorkerIsolation, appID,
		); err != nil {
			return "", 0, nil, nil, fmt.Errorf("update worker_isolation: %w", err)
		}
	}
	if p.SetWorkerGroupedSize {
		if _, err := tx.Exec(
			`UPDATE apps SET worker_grouped_size = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.WorkerGroupedSize, appID,
		); err != nil {
			return "", 0, nil, nil, fmt.Errorf("update worker_grouped_size: %w", err)
		}
	}
	if p.SetWorkerMaxWorkers {
		if _, err := tx.Exec(
			`UPDATE apps SET worker_max_workers = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.WorkerMaxWorkers, appID,
		); err != nil {
			return "", 0, nil, nil, fmt.Errorf("update worker_max_workers: %w", err)
		}
	}
	if p.SetWorkerMaxSessionLifetime {
		if _, err := tx.Exec(
			`UPDATE apps SET worker_max_session_lifetime_secs = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			p.WorkerMaxSessionLifetimeSecs, appID,
		); err != nil {
			return "", 0, nil, nil, fmt.Errorf("update worker_max_session_lifetime_secs: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", 0, nil, nil, fmt.Errorf("commit: %w", err)
	}
	// priorMem/priorCPU are the resource columns read inside this transaction, so
	// callers detect a real change (and audit the true old value) without a
	// time-of-check/time-of-use race against a concurrent PATCH.
	return priorStatus, priorReplicas, intPtrFromNull(curMem), intPtrFromNull(curCPU), nil
}

// intPtrFromNull maps a sql.NullInt64 to a *int (invalid ⇒ nil).
func intPtrFromNull(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
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
	var projectSlug, currentVersion, contentDigest, lastDeploymentStatus sql.NullString
	// last_deployed_at is the result of MAX(deployments.created_at). SQLite
	// aggregates lose the original column type, so the driver returns the
	// value as a string. We parse it manually below.
	var lastDeployedAtRaw sql.NullString
	var autoscaleEnabledInt int
	var ephemeralDataAckInt int
	err := s.Scan(
		&a.ID, &a.Slug, &a.Name, &projectSlug, &a.OwnerID, &a.Access,
		&a.Status, &a.Replicas, &a.MaxSessionsPerReplica, &a.DeployCount,
		&a.HibernateTimeoutMinutes, &a.MemoryLimitMB, &a.CPUQuotaPercent,
		&a.CreatedAt, &a.UpdatedAt,
		&a.ManagedBy, &a.ReplicaPlacement,
		&autoscaleEnabledInt, &a.AutoscaleMinReplicas, &a.AutoscaleMaxReplicas, &a.AutoscaleTarget,
		&a.LastAutoscaleAt, &a.IdentityHeaders, &a.MinWarmReplicas,
		&a.LastError, &a.CrashedAt, &a.Description, &a.IconMime,
		&a.WorkerIsolation, &a.WorkerGroupedSize, &a.WorkerMaxWorkers,
		&a.WorkerMaxSessionLifetimeSecs, &ephemeralDataAckInt,
		&lastDeployedAtRaw, &currentVersion, &contentDigest, &lastDeploymentStatus,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.AutoscaleEnabled = autoscaleEnabledInt != 0
	a.EphemeralDataAck = ephemeralDataAckInt != 0
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
	if lastDeploymentStatus.Valid {
		a.LastDeploymentStatus = lastDeploymentStatus.String
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
		VALUES (?, ?, ?, ?, ?, ?, ?, `+s.d.nowText()+`)
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
		UPDATE workers SET last_heartbeat = `+s.d.nowText()+`, cert_fingerprint = ?, status = ?
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

// SupersedeTierAddrWorkers marks down every up worker sharing a (tier, advertise
// address) except the given node id, in a single statement. Used when a worker
// (re)registers at an endpoint so a stale duplicate at that same endpoint - an
// agent that rejoined under a fresh node id after losing its persisted identity -
// stops being a routing candidate. Distinct-address workers on the tier are real
// multi-worker capacity and are left up. Zero affected rows is valid (no prior
// worker at the endpoint), so unlike SetWorkerStatus this does not return
// ErrNotFound.
func (s *Store) SupersedeTierAddrWorkers(tier, advertiseAddr, exceptNodeID string) error {
	_, err := s.db.Exec(
		`UPDATE workers SET status = 'down' WHERE tier = ? AND advertise_addr = ? AND node_id <> ? AND status = 'up'`,
		tier, advertiseAddr, exceptNodeID)
	if err != nil {
		return fmt.Errorf("supersede tier %q addr %q workers: %w", tier, advertiseAddr, err)
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
		    revoked_at = CASE WHEN revoked_at = '' THEN `+s.d.nowText()+` ELSE revoked_at END
		WHERE node_id = ?`, nodeID)
	if err != nil {
		return fmt.Errorf("revoke worker %q: %w", nodeID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteStaleWorkers tombstones long-dead worker rows so the table does not grow
// without bound. A row is removed only when all of the following hold:
//   - it is marked down (an up worker is never reaped),
//   - it was never administratively revoked (revoked rows are kept for audit),
//   - its last_heartbeat is at or before cutoff (well past the down timeout),
//   - it hosts no non-terminal replica (running or crashed); lost and stopped
//     replicas are terminal from this worker's perspective and do not block
//     reaping, since re-placement assigns a fresh worker_id.
//
// cutoff is formatted to match the stored UTC datetime string so the comparison
// is chronological. Returns the node ids of the reaped rows so the caller can
// drop them from any in-memory index.
func (s *Store) DeleteStaleWorkers(cutoff time.Time) ([]string, error) {
	rows, err := s.db.Query(`
		DELETE FROM workers
		WHERE status = 'down'
		  AND revoked_at = ''
		  AND last_heartbeat <= ?
		  AND NOT EXISTS (
		      SELECT 1 FROM replicas r
		      WHERE r.worker_id = workers.node_id
		        AND r.status IN ('running', 'crashed')
		  )
		RETURNING node_id`,
		cutoff.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, fmt.Errorf("delete stale workers: %w", err)
	}
	defer rows.Close()
	var reaped []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, fmt.Errorf("scan reaped worker: %w", err)
		}
		reaped = append(reaped, nodeID)
	}
	return reaped, rows.Err()
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

// WorkerReplicaLoad is a worker's running-replica load for least-loaded
// placement: Total counts running replicas across all apps; SameApp counts how
// many of those belong to the candidate app, so placement can break load ties
// by avoiding co-locating an app's own replicas on one worker.
type WorkerReplicaLoad struct {
	Total   int
	SameApp int
}

// RunningReplicaLoadByWorker returns, keyed by worker_id, each worker's running
// replica load and how much of it belongs to slug. Only running replicas count
// (a lost or crashed replica's former worker is not charged), and workers
// hosting no running replica are absent (placement treats a missing entry as
// zero load).
func (s *Store) RunningReplicaLoadByWorker(slug string) (map[string]WorkerReplicaLoad, error) {
	rows, err := s.db.Query(`
		SELECT r.worker_id,
		       COUNT(*) AS total,
		       COALESCE(SUM(CASE WHEN a.slug = ? THEN 1 ELSE 0 END), 0) AS same_app
		FROM replicas r JOIN apps a ON a.id = r.app_id
		WHERE r.status = ? AND r.worker_id <> ''
		GROUP BY r.worker_id`, slug, ReplicaStatusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]WorkerReplicaLoad{}
	for rows.Next() {
		var nodeID string
		var load WorkerReplicaLoad
		if err := rows.Scan(&nodeID, &load.Total, &load.SameApp); err != nil {
			return nil, err
		}
		out[nodeID] = load
	}
	return out, rows.Err()
}

// RunningReplicaWorkersForSlug returns the distinct workers currently hosting a
// running replica of slug. A shared-mount consumer pins to one of these so it
// lands on a worker that also hosts its source's provisioned data. Only running
// replicas count (a lost or crashed replica's former worker is not a valid mount
// host) and replicas with no worker id (native/local tier) are excluded.
func (s *Store) RunningReplicaWorkersForSlug(slug string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT r.worker_id
		FROM replicas r JOIN apps a ON a.id = r.app_id
		WHERE a.slug = ? AND r.status = ? AND r.worker_id <> ''
		ORDER BY r.worker_id`, slug, ReplicaStatusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, err
		}
		out = append(out, nodeID)
	}
	return out, rows.Err()
}

// UpdateReplicaStatus sets the status of a single replica identified by
// (app_id, idx) and refreshes its updated_at timestamp.
func (s *Store) UpdateReplicaStatus(appID int64, index int, status string) error {
	_, err := s.db.Exec(
		`UPDATE replicas SET status = ?, updated_at = `+s.d.nowEpoch()+`
		   WHERE app_id = ? AND idx = ?`, status, appID, index)
	return err
}

// UpdateReplicaEndpoint sets the routing endpoint URL of a single replica
// identified by (app_id, idx) and refreshes its updated_at timestamp. Recovery
// uses it after re-adopting a remote replica so the stored endpoint_url tracks
// the URL the proxy route was actually registered with: the worker-loss path
// deregisters a slot only while the live route still equals the row's
// endpoint_url, so a stale stored value would leave a dead worker routable.
func (s *Store) UpdateReplicaEndpoint(appID int64, index int, endpointURL string) error {
	_, err := s.db.Exec(
		`UPDATE replicas SET endpoint_url = ?, updated_at = `+s.d.nowEpoch()+`
		   WHERE app_id = ? AND idx = ?`, endpointURL, appID, index)
	if err != nil {
		return fmt.Errorf("update replica endpoint: %w", err)
	}
	return nil
}

// MarkReplicaLostIfOwnedBy transitions (app_id, idx) to lost only while it is
// still running and still attributed to workerID, returning whether the row
// actually changed. The ownership-and-status guard prevents a worker-loss pass
// (admin revoke or the down-sweep) that read a stale snapshot from clobbering a
// replica that a concurrent redeploy already re-placed onto a healthy worker:
// such a row no longer matches workerID, so the update is a no-op and the caller
// skips deregistering the (now healthy) routing slot.
func (s *Store) MarkReplicaLostIfOwnedBy(appID int64, index int, workerID string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE replicas SET status = ?, updated_at = `+s.d.nowEpoch()+`
		   WHERE app_id = ? AND idx = ? AND worker_id = ? AND status = ?`,
		ReplicaStatusLost, appID, index, workerID, ReplicaStatusRunning)
	if err != nil {
		return false, fmt.Errorf("mark replica lost: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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
