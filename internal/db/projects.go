package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrProjectNotFound is returned by the project reads and mutations when no row
// carries the requested slug. Handlers map it to 404.
var ErrProjectNotFound = errors.New("project not found")

// Project is a display container for apps. apps.project_slug references
// Slug without a foreign key, so a Project row may be absent for a slug that
// apps already use (grouped, just unnamed) and deleting one never touches apps.
type Project struct {
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	IconEmoji   string    `json:"icon_emoji"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ProjectListItem is a project as the list endpoints return it: display
// metadata plus how many apps the query counted in it.
//
// The count lives here rather than on Project because it is a property of the
// QUERY, not of the project: the scoped list counts only apps the caller can
// see. Putting it on Project would let GetProject, which computes no count,
// serialize a missing count as 0.
//
// It restates the fields instead of embedding Project so it can OMIT the
// timestamps. The scoped list has none to report (an unnamed project has no
// projects row at all), and an embedded Project would serialize those as
// "0001-01-01T00:00:00Z" - an absent value rendered as a plausible one. Both
// list branches therefore produce byte-identical shapes, and a client cannot
// come to depend on a timestamp that is only present for admins.
type ProjectListItem struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IconEmoji   string `json:"icon_emoji"`
	AppCount    int    `json:"app_count"`
}

const projectColumns = `slug, name, description, icon_emoji, created_at, updated_at`

func scanProject(row interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	if err := row.Scan(&p.Slug, &p.Name, &p.Description, &p.IconEmoji, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) GetProject(slug string) (*Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectColumns+` FROM projects WHERE slug = ?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

// ListProjects returns every project ordered by slug, each with a count of ALL
// apps referencing it. limit <= 0 means "all". The count is a correlated
// subquery rather than a JOIN + GROUP BY so a project with no apps still
// returns a row carrying 0, with no LEFT JOIN NULL handling.
func (s *Store) ListProjects(limit, offset int) ([]*ProjectListItem, error) {
	if limit <= 0 {
		limit = s.d.noLimit()
	}
	rows, err := s.db.Query(`
		SELECT slug, name, description, icon_emoji,
		       (SELECT COUNT(*) FROM apps WHERE apps.project_slug = projects.slug)
		FROM projects ORDER BY slug LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []*ProjectListItem
	for rows.Next() {
		var it ProjectListItem
		if err := rows.Scan(&it.Slug, &it.Name, &it.Description, &it.IconEmoji, &it.AppCount); err != nil {
			return nil, err
		}
		out = append(out, &it)
	}
	return out, rows.Err()
}

// UpsertProject inserts a project if its slug is free and reports whether it
// created one. It NEVER overwrites an existing row: POST /api/projects is
// idempotent, and renaming goes through UpdateProject. created is derived from
// RowsAffected, which ON CONFLICT DO NOTHING reports as 0 for a no-op on both
// backends.
func (s *Store) UpsertProject(p Project) (bool, error) {
	res, err := s.db.Exec(
		`INSERT INTO projects (slug, name, description, icon_emoji)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (slug) DO NOTHING`,
		p.Slug, p.Name, p.Description, p.IconEmoji,
	)
	if err != nil {
		return false, fmt.Errorf("upsert project: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("upsert project: %w", err)
	}
	return n > 0, nil
}

// EnsureProject is UpsertProject with no display metadata: the form app writers
// use when an app names a project that may not exist yet.
func (s *Store) EnsureProject(slug string) (bool, error) {
	if slug == "" {
		return false, nil
	}
	return s.UpsertProject(Project{Slug: slug})
}

// UpdateProjectParams patches display metadata. Each Set flag is keyed off the
// request having mentioned the field, not off the value being non-empty, so an
// explicit "" clears it.
type UpdateProjectParams struct {
	Slug           string
	SetName        bool
	Name           string
	SetDescription bool
	Description    string
	SetIconEmoji   bool
	IconEmoji      string
}

func (s *Store) UpdateProject(p UpdateProjectParams) error {
	sets := []string{}
	args := []any{}
	if p.SetName {
		sets = append(sets, "name = ?")
		args = append(args, p.Name)
	}
	if p.SetDescription {
		sets = append(sets, "description = ?")
		args = append(args, p.Description)
	}
	if p.SetIconEmoji {
		sets = append(sets, "icon_emoji = ?")
		args = append(args, p.IconEmoji)
	}
	if len(sets) == 0 {
		// Nothing to change, but the caller still expects a 404 for a slug
		// that does not exist.
		if _, err := s.GetProject(p.Slug); err != nil {
			return err
		}
		return nil
	}
	sets = append(sets, "updated_at = "+s.d.now())
	args = append(args, p.Slug)
	res, err := s.db.Exec(`UPDATE projects SET `+strings.Join(sets, ", ")+` WHERE slug = ?`, args...)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	if n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// DeleteProject removes the display metadata only. apps.project_slug is a soft
// reference with no foreign key, so apps keep their grouping value and simply
// render under the bare slug again. The handler refuses the delete while apps
// still reference it; this is the unguarded primitive.
func (s *Store) DeleteProject(slug string) error {
	res, err := s.db.Exec(`DELETE FROM projects WHERE slug = ?`, slug)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// ListProjectsVisibleToUser returns every project referenced by at least one
// app the user can see, joined to its display metadata, with AppCount counting
// only those visible apps. The LEFT JOIN is what makes an un-named project (an
// app naming a slug nobody has described yet) still appear, with empty
// metadata. project_slug = "" means "no project" and is excluded. Ordering is
// by slug so the client renders a stable list.
//
// GROUP BY apps.project_slug is what produces the count; the grouped
// COALESCE(p.*) values are constant within a group because the join is on that
// same slug, so no aggregate is needed around them. A project every one of
// whose apps is invisible produces no rows at all and so is absent, which is
// the access-control requirement.
func (s *Store) ListProjectsVisibleToUser(userID int64) ([]*ProjectListItem, error) {
	rows, err := s.db.Query(`
		SELECT apps.project_slug,
		       COALESCE(p.name, ''), COALESCE(p.description, ''), COALESCE(p.icon_emoji, ''),
		       COUNT(*)
		FROM apps
		LEFT JOIN projects p ON p.slug = apps.project_slug
		WHERE apps.project_slug <> ''
		  AND (`+appVisibleToUserWhere+`)
		GROUP BY apps.project_slug, p.name, p.description, p.icon_emoji
		ORDER BY apps.project_slug`, userID, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("list visible projects: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []*ProjectListItem
	for rows.Next() {
		var it ProjectListItem
		if err := rows.Scan(&it.Slug, &it.Name, &it.Description, &it.IconEmoji, &it.AppCount); err != nil {
			return nil, err
		}
		out = append(out, &it)
	}
	return out, rows.Err()
}

// CountAppsInProject counts apps referencing slug, across ALL apps regardless
// of the caller's visibility. The delete guard must see apps the caller cannot,
// or a viewer-invisible app would be silently orphaned.
func (s *Store) CountAppsInProject(slug string) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM apps WHERE project_slug = ?`, slug).Scan(&n); err != nil {
		return 0, fmt.Errorf("count apps in project: %w", err)
	}
	return n, nil
}
