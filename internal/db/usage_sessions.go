package db

import (
	"container/heap"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const usageSessionStaleAfter = 90 * time.Second

// UsageSessionStart is the durable start event emitted after an app WebSocket
// upgrade succeeds. IdentityMode and PolicyGeneration describe the policy
// captured at that moment; the production insert clamps it against the current
// committed policy. UserID and DeploymentID are nullable-by-zero.
type UsageSessionStart struct {
	ID               string
	Slug             string
	DeploymentID     int64
	UserID           int64
	ViewerKey        string
	PrincipalKind    string
	IdentityMode     string
	PolicyGeneration int64
	InstanceID       string
	StartedAt        time.Time
}

// UsageSummary is the aggregate headline for one app and time window.
type UsageSummary struct {
	Sessions               int64      `json:"sessions"`
	UniqueViewers          *int64     `json:"unique_viewers"`
	PeakConcurrentSessions int64      `json:"peak_concurrent_sessions"`
	AuthenticatedSessions  int64      `json:"authenticated_sessions"`
	AnonymousSessions      int64      `json:"anonymous_sessions"`
	ServiceSessions        int64      `json:"service_sessions"`
	ActiveSessions         int64      `json:"active_sessions"`
	AverageDurationSeconds float64    `json:"average_duration_seconds"`
	TotalDurationSeconds   int64      `json:"total_duration_seconds"`
	LastOpenedAt           *time.Time `json:"last_opened_at"`
}

// UsageDay is a UTC calendar bucket. UniqueViewers counts authenticated users;
// anonymous visitors cannot be reliably deduplicated without invasive tracking.
type UsageDay struct {
	Date                   string `json:"date"`
	Sessions               int64  `json:"sessions"`
	UniqueViewers          *int64 `json:"unique_viewers"`
	PeakConcurrentSessions int64  `json:"peak_concurrent_sessions"`
	AuthenticatedSessions  int64  `json:"authenticated_sessions"`
	AnonymousSessions      int64  `json:"anonymous_sessions"`
	ServiceSessions        int64  `json:"service_sessions"`
}

// UsageViewer is returned only to administrators by the API layer.
type UsageViewer struct {
	UserID               int64     `json:"user_id"`
	Username             string    `json:"username"`
	DisplayName          string    `json:"display_name,omitempty"`
	Sessions             int64     `json:"sessions"`
	TotalDurationSeconds int64     `json:"total_duration_seconds"`
	LastOpenedAt         time.Time `json:"last_opened_at"`
}

// UsageRecentSession is an identifiable recent connection, likewise reserved
// for administrators by the API layer.
type UsageRecentSession struct {
	ID              string     `json:"id"`
	PrincipalKind   string     `json:"principal_kind"`
	UserID          *int64     `json:"user_id"`
	Username        string     `json:"username,omitempty"`
	DisplayName     string     `json:"display_name,omitempty"`
	DeploymentID    *int64     `json:"deployment_id"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	DurationSeconds int64      `json:"duration_seconds"`
	Active          bool       `json:"active"`
}

type UsageReport struct {
	Summary UsageSummary         `json:"summary"`
	Daily   []UsageDay           `json:"daily"`
	Viewers []UsageViewer        `json:"viewers"`
	Recent  []UsageRecentSession `json:"recent_sessions"`
}

// BeginUsageSession inserts one successful app connection. The app lookup and
// insert are one statement so deleting an app concurrently cannot create an
// orphan. Usage is best-effort at the caller, but this method reports every
// failure for structured logging.
func (s *Store) BeginUsageSession(p UsageSessionStart) error {
	if err := normalizeUsageSessionStart(&p); err != nil {
		return err
	}
	var deploymentID, userID, viewerKey any
	if p.DeploymentID > 0 {
		deploymentID = p.DeploymentID
	}
	if p.UserID > 0 {
		userID = p.UserID
	}
	if p.ViewerKey != "" {
		viewerKey = p.ViewerKey
	}
	res, err := s.db.Exec(`
		INSERT INTO usage_sessions
			(id, app_id, deployment_id, user_id, viewer_key, principal_kind,
			 identity_mode, policy_generation, instance_id, started_at, heartbeat_at)
		SELECT ?, id, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM apps WHERE slug = ?`,
		p.ID, deploymentID, userID, viewerKey, p.PrincipalKind, p.IdentityMode,
		p.PolicyGeneration, p.InstanceID, p.StartedAt, p.StartedAt, p.Slug)
	if err != nil {
		return fmt.Errorf("begin usage session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("begin usage session rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func normalizeUsageSessionStart(p *UsageSessionStart) error {
	if p.ID == "" || p.Slug == "" || p.InstanceID == "" {
		return errors.New("usage session id, slug, and instance id are required")
	}
	if p.StartedAt.IsZero() {
		p.StartedAt = time.Now().UTC()
	}
	if p.PrincipalKind == "" {
		if p.UserID > 0 {
			p.PrincipalKind = "person"
		} else {
			p.PrincipalKind = "anonymous"
		}
	}
	if p.PrincipalKind != "anonymous" && p.PrincipalKind != "person" && p.PrincipalKind != "service_account" {
		return errors.New("usage principal kind is invalid")
	}
	if p.IdentityMode == "" {
		p.IdentityMode = "identified"
	}
	if p.IdentityMode != "unattributed" && p.IdentityMode != "pseudonymous" && p.IdentityMode != "identified" {
		return errors.New("usage identity mode is invalid")
	}
	if p.PolicyGeneration <= 0 {
		p.PolicyGeneration = 1
	}
	return nil
}

// BeginUsageSessionWithPolicy atomically clamps the policy captured when the
// WebSocket opened against the app and hub policy currently committed in the
// database. A queued pre-transition event can therefore only retain the
// stricter of the two policies, regardless of transaction ordering.
func (s *Store) BeginUsageSessionWithPolicy(p UsageSessionStart) (bool, error) {
	if err := normalizeUsageSessionStart(&p); err != nil {
		return false, err
	}
	var deploymentID, userID, viewerKey any
	if p.DeploymentID > 0 {
		deploymentID = p.DeploymentID
	}
	if p.UserID > 0 {
		userID = p.UserID
	}
	if p.ViewerKey != "" {
		viewerKey = p.ViewerKey
	}
	res, err := s.db.Exec(`WITH current_policy AS (
		SELECT a.id AS app_id, up.generation,
			CASE
				WHEN up.identity_mode = 'unattributed' OR a.usage_identity_mode = 'unattributed' THEN 'unattributed'
				WHEN up.identity_mode = 'pseudonymous' OR a.usage_identity_mode = 'pseudonymous' THEN 'pseudonymous'
				ELSE 'identified'
			END AS identity_mode
		FROM apps a CROSS JOIN usage_policy up
		WHERE a.slug = ? AND up.singleton_id = 1
		  AND COALESCE(a.usage_identity_mode, '') <> 'disabled'
	), resolved AS (
		SELECT app_id, generation,
			CASE
				WHEN identity_mode = 'unattributed' OR ? = 'unattributed' THEN 'unattributed'
				WHEN identity_mode = 'pseudonymous' OR ? = 'pseudonymous' THEN 'pseudonymous'
				ELSE 'identified'
			END AS identity_mode
		FROM current_policy
	)
	INSERT INTO usage_sessions
		(id, app_id, deployment_id, user_id, viewer_key, principal_kind,
		 identity_mode, policy_generation, instance_id, started_at, heartbeat_at)
	SELECT ?, app_id, ?,
		CASE WHEN identity_mode = 'identified' THEN ? ELSE NULL END,
		CASE WHEN identity_mode = 'pseudonymous' THEN ? ELSE NULL END,
		?, identity_mode,
		CASE WHEN generation > ? THEN generation ELSE ? END,
		?, ?, ? FROM resolved`,
		p.Slug, p.IdentityMode, p.IdentityMode, p.ID, deploymentID, userID, viewerKey,
		p.PrincipalKind, p.PolicyGeneration, p.PolicyGeneration, p.InstanceID, p.StartedAt, p.StartedAt)
	if err != nil {
		return false, fmt.Errorf("begin usage session with policy: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("begin usage session with policy rows affected: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	return true, nil
}

// HeartbeatUsageSessions advances the liveness clock for currently open rows.
// A crashed control plane therefore leaves at most one heartbeat interval of
// uncertain duration instead of an apparently permanent active session.
func (s *Store) HeartbeatUsageSessions(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	_, err := s.db.Exec(`UPDATE usage_sessions SET heartbeat_at = `+s.d.now()+`
		WHERE ended_at IS NULL AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return fmt.Errorf("heartbeat usage sessions: %w", err)
	}
	return nil
}

// EndUsageSession closes one row at the observed socket-close time. Repeated
// calls are harmless and cannot move an already-recorded end time.
func (s *Store) EndUsageSession(id string, endedAt time.Time) error {
	if id == "" {
		return nil
	}
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}
	res, err := s.db.Exec(`UPDATE usage_sessions
		SET ended_at = COALESCE(ended_at, ?),
		    heartbeat_at = CASE WHEN ended_at IS NULL THEN ? ELSE heartbeat_at END
		WHERE id = ?`, endedAt, endedAt, id)
	if err != nil {
		return fmt.Errorf("end usage session: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) usageDurationExpr() string {
	if s.IsPostgres() {
		// PostgreSQL's EXTRACT returns NUMERIC. Cast once here so SUM and direct
		// scans have a stable BIGINT representation across drivers.
		return `GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (COALESCE(ended_at, heartbeat_at) - started_at))))::BIGINT`
	}
	// modernc SQLite serializes time.Time with a trailing zone name that its
	// date functions do not parse. The first 19 characters are the normalized
	// UTC wall clock written by every recorder and remain parseable for both
	// RFC3339 (T separator) and SQLite's native timestamp format.
	return `MAX(0,
		strftime('%s', substr(CAST(COALESCE(ended_at, heartbeat_at) AS TEXT), 1, 19)) -
		strftime('%s', substr(CAST(started_at AS TEXT), 1, 19)))`
}

func (s *Store) usageDateExpr() string {
	if s.IsPostgres() {
		return `to_char(started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')`
	}
	// Every writer normalizes to UTC. substr avoids SQLite's date parser
	// returning NULL for driver-specific RFC3339 timestamp encodings.
	return `substr(CAST(started_at AS TEXT), 1, 10)`
}

func (s *Store) usageViewerExpr() string {
	return `CASE
		WHEN user_id IS NOT NULL THEN 'u:' || CAST(user_id AS TEXT)
		WHEN viewer_key IS NOT NULL THEN 'p:' || viewer_key
		ELSE NULL END`
}

// AppUsageReport returns bounded aggregates and, when includeIdentities is
// true, administrator-only viewer and recent-session detail.
func (s *Store) AppUsageReport(ctx context.Context, appID int64, window time.Duration, identityMode string, includeIdentities bool) (UsageReport, error) {
	if window <= 0 {
		return UsageReport{}, errors.New("usage window must be positive")
	}
	now := time.Now().UTC()
	calendarDays := int(math.Ceil(window.Hours() / 24))
	// Reports and the UI chart use UTC calendar days, including today. Anchoring
	// the cutoff at midnight keeps headline totals equal to the sum of the daily
	// buckets instead of admitting a hidden partial day at the left edge.
	cutoff := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, -(calendarDays - 1))
	duration := s.usageDurationExpr()
	activeCutoff := now.Add(-usageSessionStaleAfter)

	report := UsageReport{
		Daily:   []UsageDay{},
		Viewers: []UsageViewer{},
		Recent:  []UsageRecentSession{},
	}
	var last any
	var uniqueViewers int64
	var eligiblePersonRows int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(DISTINCT `+s.usageViewerExpr()+`),
		       COALESCE(SUM(CASE WHEN principal_kind = 'person' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN principal_kind = 'anonymous' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN principal_kind = 'service_account' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN principal_kind = 'person' AND
		         ((? = 'identified' AND identity_mode = 'identified' AND user_id IS NOT NULL) OR
		          (? = 'pseudonymous' AND identity_mode = 'pseudonymous' AND viewer_key IS NOT NULL))
		         THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN ended_at IS NULL AND heartbeat_at >= ? THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(`+duration+`), 0), MAX(started_at)
		FROM usage_sessions WHERE app_id = ? AND started_at >= ?`, identityMode, identityMode, activeCutoff, appID, cutoff).Scan(
		&report.Summary.Sessions,
		&uniqueViewers,
		&report.Summary.AuthenticatedSessions,
		&report.Summary.AnonymousSessions,
		&report.Summary.ServiceSessions,
		&eligiblePersonRows,
		&report.Summary.ActiveSessions,
		&report.Summary.TotalDurationSeconds,
		&last,
	)
	if err != nil {
		return report, fmt.Errorf("usage summary: %w", err)
	}
	if parsed, ok := usageTime(last); ok {
		report.Summary.LastOpenedAt = &parsed
	}
	rawPeak, rawDailyPeaks, err := s.usageConcurrencyPeaks(ctx, s.db, appID, cutoff, now, now)
	if err != nil {
		return report, fmt.Errorf("usage concurrency peaks: %w", err)
	}
	report.Summary.PeakConcurrentSessions = rawPeak

	var aggregateSessions, aggregatePeople, aggregateAnonymous, aggregateService, aggregateDuration, aggregatePeak int64
	var aggregateLast any
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(sessions), 0),
		COALESCE(SUM(person_sessions), 0), COALESCE(SUM(anonymous_sessions), 0),
		COALESCE(SUM(service_sessions), 0), COALESCE(SUM(total_duration_seconds), 0),
		COALESCE(MAX(peak_concurrent_sessions), 0), MAX(last_opened_at)
		FROM usage_daily WHERE app_id = ? AND day >= ?`,
		appID, cutoff.Format("2006-01-02")).Scan(&aggregateSessions, &aggregatePeople,
		&aggregateAnonymous, &aggregateService, &aggregateDuration, &aggregatePeak, &aggregateLast); err != nil {
		return report, fmt.Errorf("usage aggregate summary: %w", err)
	}
	if aggregatePeak > report.Summary.PeakConcurrentSessions {
		report.Summary.PeakConcurrentSessions = aggregatePeak
	}
	rawPersonRows := report.Summary.AuthenticatedSessions
	report.Summary.Sessions += aggregateSessions
	report.Summary.AuthenticatedSessions += aggregatePeople
	report.Summary.AnonymousSessions += aggregateAnonymous
	report.Summary.ServiceSessions += aggregateService
	report.Summary.TotalDurationSeconds += aggregateDuration
	if parsed, ok := usageTime(aggregateLast); ok &&
		(report.Summary.LastOpenedAt == nil || parsed.After(*report.Summary.LastOpenedAt)) {
		report.Summary.LastOpenedAt = &parsed
	}
	if report.Summary.Sessions > 0 {
		report.Summary.AverageDurationSeconds = float64(report.Summary.TotalDurationSeconds) / float64(report.Summary.Sessions)
	}
	// Exact uniqueness is available only when every person row in the window
	// uses the single identity scheme expected by the current effective mode.
	// Mixed prospective upgrades, deleted users, unattributed rows, and person
	// rollups all make cross-row deduplication unknowable.
	if identityMode != "unattributed" && aggregatePeople == 0 && eligiblePersonRows == rawPersonRows {
		report.Summary.UniqueViewers = &uniqueViewers
	}

	daysByDate := map[string]UsageDay{}
	aggregateRows, err := s.db.QueryContext(ctx, `SELECT CAST(day AS TEXT), sessions, person_sessions,
		anonymous_sessions, service_sessions, peak_concurrent_sessions FROM usage_daily
		WHERE app_id = ? AND day >= ? ORDER BY day`, appID, cutoff.Format("2006-01-02"))
	if err != nil {
		return report, fmt.Errorf("usage aggregate daily: %w", err)
	}
	for aggregateRows.Next() {
		var day UsageDay
		if err := aggregateRows.Scan(&day.Date, &day.Sessions, &day.AuthenticatedSessions,
			&day.AnonymousSessions, &day.ServiceSessions, &day.PeakConcurrentSessions); err != nil {
			aggregateRows.Close()
			return report, err
		}
		daysByDate[day.Date] = day
	}
	if err := aggregateRows.Close(); err != nil {
		return report, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT `+s.usageDateExpr()+`, COUNT(*), COUNT(DISTINCT `+s.usageViewerExpr()+`),
		       SUM(CASE WHEN principal_kind = 'person' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN principal_kind = 'anonymous' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN principal_kind = 'service_account' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN principal_kind = 'person' AND
		         ((? = 'identified' AND identity_mode = 'identified' AND user_id IS NOT NULL) OR
		          (? = 'pseudonymous' AND identity_mode = 'pseudonymous' AND viewer_key IS NOT NULL))
		         THEN 1 ELSE 0 END)
		FROM usage_sessions
		WHERE app_id = ? AND started_at >= ?
		GROUP BY 1 ORDER BY 1`, identityMode, identityMode, appID, cutoff)
	if err != nil {
		return report, fmt.Errorf("usage daily: %w", err)
	}
	for rows.Next() {
		var day UsageDay
		var dayUnique int64
		var dayEligible int64
		if err := rows.Scan(&day.Date, &day.Sessions, &dayUnique,
			&day.AuthenticatedSessions, &day.AnonymousSessions, &day.ServiceSessions, &dayEligible); err != nil {
			rows.Close()
			return report, fmt.Errorf("scan usage day: %w", err)
		}
		if identityMode != "unattributed" && dayEligible == day.AuthenticatedSessions {
			day.UniqueViewers = &dayUnique
		}
		if existing, ok := daysByDate[day.Date]; ok {
			rolledPeople := existing.AuthenticatedSessions
			existing.Sessions += day.Sessions
			existing.AuthenticatedSessions += day.AuthenticatedSessions
			existing.AnonymousSessions += day.AnonymousSessions
			existing.ServiceSessions += day.ServiceSessions
			if rolledPeople == 0 {
				existing.UniqueViewers = day.UniqueViewers
			} else {
				// A rolled-up person contribution makes exact daily uniqueness unavailable.
				existing.UniqueViewers = nil
			}
			daysByDate[day.Date] = existing
		} else {
			daysByDate[day.Date] = day
		}
	}
	if err := rows.Close(); err != nil {
		return report, err
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	for key, peak := range rawDailyPeaks {
		day := daysByDate[key]
		day.Date = key
		if peak > day.PeakConcurrentSessions {
			day.PeakConcurrentSessions = peak
		}
		daysByDate[key] = day
	}
	keys := make([]string, 0, len(daysByDate))
	for key := range daysByDate {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		report.Daily = append(report.Daily, daysByDate[key])
	}
	if !includeIdentities {
		return report, nil
	}

	viewerRows, err := s.db.QueryContext(ctx, `
		SELECT us.user_id, u.username, u.display_name, COUNT(*),
		       COALESCE(SUM(`+duration+`), 0), MAX(us.started_at)
		FROM usage_sessions us
		JOIN users u ON u.id = us.user_id
		WHERE us.app_id = ? AND us.started_at >= ?
		GROUP BY us.user_id, u.username, u.display_name
		ORDER BY COUNT(*) DESC, MAX(us.started_at) DESC, u.username
		LIMIT 50`, appID, cutoff)
	if err != nil {
		return report, fmt.Errorf("usage viewers: %w", err)
	}
	for viewerRows.Next() {
		var viewer UsageViewer
		var lastOpened any
		if err := viewerRows.Scan(&viewer.UserID, &viewer.Username, &viewer.DisplayName,
			&viewer.Sessions, &viewer.TotalDurationSeconds, &lastOpened); err != nil {
			viewerRows.Close()
			return report, fmt.Errorf("scan usage viewer: %w", err)
		}
		parsed, ok := usageTime(lastOpened)
		if !ok {
			viewerRows.Close()
			return report, fmt.Errorf("scan usage viewer: invalid last-opened timestamp %v", lastOpened)
		}
		viewer.LastOpenedAt = parsed
		report.Viewers = append(report.Viewers, viewer)
	}
	if err := viewerRows.Close(); err != nil {
		return report, err
	}
	if err := viewerRows.Err(); err != nil {
		return report, err
	}

	recentRows, err := s.db.QueryContext(ctx, `
		SELECT us.id, us.principal_kind, us.user_id, u.username, u.display_name, us.deployment_id,
		       us.started_at, us.heartbeat_at, us.ended_at, `+duration+`
		FROM usage_sessions us
		LEFT JOIN users u ON u.id = us.user_id
		WHERE us.app_id = ? AND us.started_at >= ?
		ORDER BY us.started_at DESC LIMIT 50`, appID, cutoff)
	if err != nil {
		return report, fmt.Errorf("recent usage sessions: %w", err)
	}
	for recentRows.Next() {
		var item UsageRecentSession
		var userID, deploymentID sql.NullInt64
		var username, displayName sql.NullString
		var heartbeat time.Time
		var ended sql.NullTime
		if err := recentRows.Scan(&item.ID, &item.PrincipalKind, &userID, &username, &displayName, &deploymentID,
			&item.StartedAt, &heartbeat, &ended, &item.DurationSeconds); err != nil {
			recentRows.Close()
			return report, fmt.Errorf("scan recent usage session: %w", err)
		}
		item.StartedAt = item.StartedAt.UTC()
		if userID.Valid {
			v := userID.Int64
			item.UserID = &v
		}
		item.Username = username.String
		item.DisplayName = displayName.String
		if deploymentID.Valid {
			v := deploymentID.Int64
			item.DeploymentID = &v
		}
		if ended.Valid {
			v := ended.Time.UTC()
			item.EndedAt = &v
		}
		item.Active = !ended.Valid && now.Sub(heartbeat.UTC()) <= 90*time.Second
		report.Recent = append(report.Recent, item)
	}
	if err := recentRows.Close(); err != nil {
		return report, err
	}
	return report, recentRows.Err()
}

// SQLite drops DATETIME affinity from aggregate expressions such as MAX(), so
// the driver returns a string there; Postgres returns time.Time. Accept both
// without weakening direct timestamp scans elsewhere.
func usageTime(raw any) (time.Time, bool) {
	switch value := raw.(type) {
	case time.Time:
		return value.UTC(), true
	case string:
		return parseSQLiteTime(value)
	case []byte:
		return parseSQLiteTime(string(value))
	default:
		return time.Time{}, false
	}
}

type usageQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type usageEndHeap []int64

func (h usageEndHeap) Len() int           { return len(h) }
func (h usageEndHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h usageEndHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *usageEndHeap) Push(value any)    { *h = append(*h, value.(int64)) }
func (h *usageEndHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// usageConcurrencyPeaks streams retained half-open session intervals in start
// order and keeps only active end times in a min-heap. Its working memory is
// therefore O(concurrent sessions), not O(all sessions in the retention
// window). Open sessions extend to now only while their heartbeat is fresh;
// abandoned rows end at their last observed heartbeat. No identity material
// participates in concurrency reporting.
func (s *Store) usageConcurrencyPeaks(ctx context.Context, q usageQueryer, appID int64, windowStart, windowEnd, now time.Time) (int64, map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT started_at, ended_at, heartbeat_at
		FROM usage_sessions WHERE app_id = ? AND started_at < ?
		AND COALESCE(ended_at, heartbeat_at) > ?
		ORDER BY started_at, id`, appID, windowEnd, windowStart)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	activeCutoff := now.Add(-usageSessionStaleAfter)
	ends := &usageEndHeap{}
	heap.Init(ends)
	daily := make(map[string]int64)
	var peak int64
	nextBoundary := time.Date(windowStart.Year(), windowStart.Month(), windowStart.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	popEnded := func(at time.Time) {
		cutoff := at.UnixNano()
		for ends.Len() > 0 && (*ends)[0] <= cutoff {
			heap.Pop(ends)
		}
	}
	record := func(at time.Time) {
		current := int64(ends.Len())
		if current > peak {
			peak = current
		}
		key := at.UTC().Format("2006-01-02")
		if current > daily[key] {
			daily[key] = current
		}
	}
	for rows.Next() {
		var startedRaw, endedRaw, heartbeatRaw any
		if err := rows.Scan(&startedRaw, &endedRaw, &heartbeatRaw); err != nil {
			return 0, nil, err
		}
		started, ok := usageTime(startedRaw)
		if !ok {
			return 0, nil, fmt.Errorf("parse usage interval start")
		}
		heartbeat, ok := usageTime(heartbeatRaw)
		if !ok {
			return 0, nil, fmt.Errorf("parse usage interval heartbeat")
		}
		ended, closed := usageTime(endedRaw)
		if !closed {
			ended = heartbeat
			if !heartbeat.Before(activeCutoff) {
				ended = now
			}
		}
		if started.Before(windowStart) {
			started = windowStart
		}
		if ended.After(windowEnd) {
			ended = windowEnd
		}
		if !ended.After(started) {
			continue
		}
		for !nextBoundary.After(started) && nextBoundary.Before(windowEnd) {
			popEnded(nextBoundary)
			record(nextBoundary)
			nextBoundary = nextBoundary.AddDate(0, 0, 1)
		}
		popEnded(started)
		heap.Push(ends, ended.UnixNano())
		record(started)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	for nextBoundary.Before(windowEnd) {
		popEnded(nextBoundary)
		record(nextBoundary)
		nextBoundary = nextBoundary.AddDate(0, 0, 1)
	}
	for key, value := range daily {
		if value == 0 {
			delete(daily, key)
		}
	}
	return peak, daily, nil
}

// PruneUsageSessions transactionally rolls completed raw rows into daily,
// non-identifying aggregates before deleting them. Fresh open rows are retained
// even if they cross the cutoff. Rows whose heartbeats have already aged out of
// the active-session window are finalized at their last observed heartbeat so a
// crashed recorder cannot retain them indefinitely.
func (s *Store) PruneUsageSessions(retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	if err := s.finalizeStaleUsageSessions(retention); err != nil {
		return 0, err
	}
	var total int64
	for {
		n, err := s.rollupUsageBatch(retention, 1000)
		if err != nil {
			return total, err
		}
		total += n
		if n < 1000 {
			return total, nil
		}
	}
}

func (s *Store) finalizeStaleUsageSessions(retention time.Duration) error {
	_, err := s.db.Exec(`UPDATE usage_sessions SET ended_at = heartbeat_at
		WHERE ended_at IS NULL
		  AND started_at < ` + s.d.nowMinusSeconds(int(retention.Seconds())) + `
		  AND heartbeat_at < ` + s.d.nowMinusSeconds(int(usageSessionStaleAfter.Seconds())))
	if err != nil {
		return fmt.Errorf("finalize stale usage sessions: %w", err)
	}
	return nil
}

func (s *Store) rollupUsageBatch(retention time.Duration, limit int) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin usage rollup: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	rows, err := tx.Query(`SELECT id, app_id, `+s.usageDateExpr()+`, principal_kind,
		`+s.usageDurationExpr()+`, started_at, ended_at FROM usage_sessions
		WHERE ended_at IS NOT NULL AND started_at < `+s.d.nowMinusSeconds(int(retention.Seconds()))+`
		ORDER BY started_at LIMIT ?`, limit)
	if err != nil {
		return 0, fmt.Errorf("select usage rollup batch: %w", err)
	}
	type dailyKey struct {
		appID int64
		day   string
	}
	type dailyValue struct {
		sessions, people, anonymous, service, duration int64
		last                                           time.Time
	}
	groups := map[dailyKey]dailyValue{}
	peakDays := map[dailyKey]struct{}{}
	var ids []string
	for rows.Next() {
		var id, day, principal string
		var appID, duration int64
		var startedRaw, endedRaw any
		if err := rows.Scan(&id, &appID, &day, &principal, &duration, &startedRaw, &endedRaw); err != nil {
			rows.Close()
			return 0, err
		}
		started, ok := usageTime(startedRaw)
		if !ok {
			rows.Close()
			return 0, fmt.Errorf("parse rolled usage start")
		}
		ended, ok := usageTime(endedRaw)
		if !ok {
			rows.Close()
			return 0, fmt.Errorf("parse rolled usage end")
		}
		ids = append(ids, id)
		key := dailyKey{appID: appID, day: day}
		value := groups[key]
		value.sessions++
		value.duration += duration
		switch principal {
		case "person":
			value.people++
		case "service_account":
			value.service++
		default:
			value.anonymous++
		}
		if started.After(value.last) {
			value.last = started.UTC()
		}
		groups[key] = value
		for cursor := time.Date(started.Year(), started.Month(), started.Day(), 0, 0, 0, 0, time.UTC); cursor.Before(ended); cursor = cursor.AddDate(0, 0, 1) {
			peakDays[dailyKey{appID: appID, day: cursor.Format("2006-01-02")}] = struct{}{}
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for key := range peakDays {
		var finalized bool
		err := tx.QueryRow(`SELECT peak_finalized FROM usage_daily WHERE app_id = ? AND day = ?`,
			key.appID, key.day).Scan(&finalized)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("read usage peak state for %s: %w", key.day, err)
		}
		if finalized {
			continue
		}
		dayStart, err := time.Parse("2006-01-02", key.day)
		if err != nil {
			return 0, err
		}
		dayEnd := dayStart.AddDate(0, 0, 1)
		peak, _, err := s.usageConcurrencyPeaks(context.Background(), tx, key.appID, dayStart, dayEnd, now)
		if err != nil {
			return 0, fmt.Errorf("load usage peak for %s: %w", key.day, err)
		}
		// Past UTC days are immutable because production starts are timestamped
		// at the successful upgrade. Today's peak may still rise after this
		// maintenance pass (for example when an old long-lived session closes),
		// so retain its current maximum without marking it final.
		finalized = !dayEnd.After(todayStart)
		if _, err := tx.Exec(`INSERT INTO usage_daily (app_id, day, peak_concurrent_sessions, peak_finalized)
			VALUES (?, ?, ?, ?) ON CONFLICT (app_id, day) DO UPDATE SET
			peak_concurrent_sessions = CASE
				WHEN usage_daily.peak_concurrent_sessions < excluded.peak_concurrent_sessions
				THEN excluded.peak_concurrent_sessions ELSE usage_daily.peak_concurrent_sessions END,
			peak_finalized = excluded.peak_finalized`,
			key.appID, key.day, peak, finalized); err != nil {
			return 0, fmt.Errorf("preserve usage peak for %s: %w", key.day, err)
		}
	}
	for key, value := range groups {
		_, err := tx.Exec(`INSERT INTO usage_daily
			(app_id, day, sessions, person_sessions, anonymous_sessions, service_sessions,
			 total_duration_seconds, last_opened_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (app_id, day) DO UPDATE SET
			 sessions = usage_daily.sessions + excluded.sessions,
			 person_sessions = usage_daily.person_sessions + excluded.person_sessions,
			 anonymous_sessions = usage_daily.anonymous_sessions + excluded.anonymous_sessions,
			 service_sessions = usage_daily.service_sessions + excluded.service_sessions,
			 total_duration_seconds = usage_daily.total_duration_seconds + excluded.total_duration_seconds,
			 last_opened_at = CASE WHEN usage_daily.last_opened_at IS NULL OR usage_daily.last_opened_at < excluded.last_opened_at
				THEN excluded.last_opened_at ELSE usage_daily.last_opened_at END`,
			key.appID, key.day, value.sessions, value.people, value.anonymous, value.service,
			value.duration, value.last)
		if err != nil {
			return 0, fmt.Errorf("merge usage daily: %w", err)
		}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if _, err := tx.Exec(`DELETE FROM usage_sessions WHERE id IN (`+placeholders+`)`, args...); err != nil {
		return 0, fmt.Errorf("delete rolled usage sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit usage rollup: %w", err)
	}
	return int64(len(ids)), nil
}

func (s *Store) PruneUsageDaily(retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-retention).Format("2006-01-02")
	res, err := s.db.Exec(`DELETE FROM usage_daily WHERE day < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune usage daily: %w", err)
	}
	return res.RowsAffected()
}
