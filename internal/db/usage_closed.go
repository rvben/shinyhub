package db

// usageClosedAggregateQuery combines transactionally maintained closed totals
// with the live open rows. Both sides and any identity deduplication share one
// statement snapshot, so a concurrent closure cannot disappear or count twice.
// Only counters are materialized: distinct identities and interval peaks must
// still be derived from retained rows, since neither is additive across days.
func (s *Store) usageClosedAggregateQuery(identityMode string) string {
	dayUnique, summaryUnique := "0", "0"
	if identityMode != "unattributed" {
		// Avoid a historical identity scan when there are no person sessions.
		// The table's constraints forbid identity keys on other principal kinds.
		dayUnique = `CASE WHEN people > 0 THEN (SELECT COUNT(DISTINCT ` + s.usageViewerExpr() + `)
			FROM usage_sessions WHERE app_id = ?1 AND ` + s.usageDateExpr() + ` = totals.day) ELSE 0 END`
		summaryUnique = `CASE WHEN (SELECT SUM(people) FROM totals) > 0 THEN
			(SELECT COUNT(DISTINCT ` + s.usageViewerExpr() + `) FROM usage_sessions
			WHERE app_id = ?1 AND started_at >= ?2) ELSE 0 END`
	}
	return `WITH contributions AS (
		SELECT day, sessions, person_sessions AS people, anonymous_sessions AS anonymous,
			service_sessions AS service,
			CASE ?5 WHEN 'identified' THEN identified_person_sessions
				WHEN 'pseudonymous' THEN pseudonymous_person_sessions ELSE 0 END AS eligible,
			0 AS active, total_duration_seconds AS duration
		FROM usage_closed_daily WHERE app_id = ?1 AND day >= ?3
		UNION ALL
		SELECT ` + s.usageDateExpr() + `, COUNT(*),
			SUM(principal_kind = 'person'), SUM(principal_kind = 'anonymous'),
			SUM(principal_kind = 'service_account'),
			SUM(principal_kind = 'person' AND
				((?5 = 'identified' AND identity_mode = 'identified' AND user_id IS NOT NULL) OR
				 (?5 = 'pseudonymous' AND identity_mode = 'pseudonymous' AND viewer_key IS NOT NULL))),
			SUM(heartbeat_at >= ?4), COALESCE(SUM(` + s.usageDurationExpr() + `), 0)
		FROM usage_sessions WHERE app_id = ?1 AND ended_at IS NULL AND started_at >= ?2
		GROUP BY 1
	), totals AS (
		SELECT day, SUM(sessions) AS sessions, SUM(people) AS people,
			SUM(anonymous) AS anonymous, SUM(service) AS service,
			SUM(eligible) AS eligible, SUM(active) AS active, SUM(duration) AS duration
		FROM contributions GROUP BY day
	)
	SELECT day, sessions, ` + dayUnique + `, ` + summaryUnique + `,
		people, anonymous, service, eligible, active, duration,
		(SELECT MAX(started_at) FROM usage_sessions
		 WHERE app_id = ?1 AND ` + s.usageDateExpr() + ` = totals.day)
	FROM totals ORDER BY day`
}
