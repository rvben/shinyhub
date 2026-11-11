package db

import "fmt"

// ListAppsWithAvailability is an operator preview of stored intent, paginated
// before expensive per-app decoration, just like the ordinary apps listing.
func (s *Store) ListAppsWithAvailability(intent string, limit, offset int) ([]*App, int, error) {
	if limit <= 0 {
		limit = s.d.noLimit()
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM apps WHERE availability = ?`, intent).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT `+appColumns+deploymentSummarySQL+` FROM apps WHERE availability = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, intent, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list apps by availability: %w", err)
	}
	defer rows.Close()
	apps := make([]*App, 0)
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, 0, err
		}
		apps = append(apps, a)
	}
	return apps, total, rows.Err()
}
