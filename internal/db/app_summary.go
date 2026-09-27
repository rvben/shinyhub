package db

import "database/sql"

// AppSummary is the minimal per-app projection needed by endpoints that only
// answer "which apps exist and can this one be opened": /.shinyhub/apps.json
// and the app-switcher's nav.json. Neither reads deployment history or most
// of App's other columns, so this skips deploymentSummarySQL's six
// correlated subqueries per row entirely - the dominant cost of the full App
// query at fleet scale, and one nav.json pays on every app-page load.
type AppSummary struct {
	Slug        string
	Name        string
	Access      string
	Status      string
	IconEmoji   string
	ProjectSlug string
}

// appSummaryColumns is the plain apps.* column list for the lean App query
// family below, in the exact order scanAppSummary expects.
const appSummaryColumns = `slug, name, access, status, icon_emoji, project_slug`

func scanAppSummary(s scanner) (*AppSummary, error) {
	var a AppSummary
	var projectSlug sql.NullString
	if err := s.Scan(&a.Slug, &a.Name, &a.Access, &a.Status, &a.IconEmoji, &projectSlug); err != nil {
		return nil, err
	}
	if projectSlug.Valid {
		a.ProjectSlug = projectSlug.String
	}
	return &a, nil
}

// ListAppSummaries mirrors ListApps' row set (every app, newest first) but
// selects only the AppSummary columns. Used by listAppsVisibleTo for
// privileged callers of /.shinyhub/apps.json and nav.json.
func (s *Store) ListAppSummaries(limit, offset int) ([]*AppSummary, error) {
	if limit <= 0 {
		limit = s.d.noLimit()
	}
	rows, err := s.db.Query(`
		SELECT `+appSummaryColumns+`
		FROM apps ORDER BY created_at DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []*AppSummary
	for rows.Next() {
		app, err := scanAppSummary(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

// ListAppSummariesVisibleToUser mirrors ListAppsVisibleToUser's row set (same
// appVisibleToUserWhere predicate, same ordering) but selects only the
// AppSummary columns.
func (s *Store) ListAppSummariesVisibleToUser(userID int64, limit, offset int) ([]*AppSummary, error) {
	if limit <= 0 {
		limit = s.d.noLimit()
	}
	rows, err := s.db.Query(`
		SELECT `+appSummaryColumns+`
		FROM apps
		WHERE `+appVisibleToUserWhere+`
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`, userID, userID, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []*AppSummary
	for rows.Next() {
		app, err := scanAppSummary(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

// ListPublicAppSummaries mirrors ListPublicApps' row set (access = 'public'
// only) but selects only the AppSummary columns. The only query used for
// anonymous /.shinyhub/apps.json and nav.json requests.
func (s *Store) ListPublicAppSummaries(limit, offset int) ([]*AppSummary, error) {
	if limit <= 0 {
		limit = s.d.noLimit()
	}
	rows, err := s.db.Query(`
		SELECT `+appSummaryColumns+`
		FROM apps
		WHERE access = 'public'
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []*AppSummary
	for rows.Next() {
		app, err := scanAppSummary(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}
