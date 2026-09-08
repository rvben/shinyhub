package db

import (
	"database/sql"
	"errors"
	"time"
)

// DeploymentDraft retains an immutable upload independently of serving history.
// Preview applications have their own access grants, environment and data.
type DeploymentDraft struct {
	ID            string `json:"id"`
	AppID         int64  `json:"app_id"`
	PreviewAppID  *int64 `json:"preview_app_id,omitempty"`
	PreviewSlug   string `json:"preview_slug,omitempty"`
	ContentDigest string `json:"content_digest"`
	BaseRevision  string `json:"-"`
	CreatedBy     *int64 `json:"created_by,omitempty"`
	ExpiresAt     int64  `json:"expires_at"`
	CreatedAt     int64  `json:"created_at"`
	PromotedAt    *int64 `json:"promoted_at,omitempty"`
}

func (s *Store) CreateDeploymentDraft(d DeploymentDraft) error {
	_, err := s.db.Exec(`INSERT INTO deployment_drafts
 (id, app_id, content_digest, base_revision, created_by, expires_at, created_at)
 VALUES (?, ?, ?, ?, ?, ?, ?)`, d.ID, d.AppID, d.ContentDigest, d.BaseRevision, d.CreatedBy, d.ExpiresAt, d.CreatedAt)
	return err
}

func scanDraft(row interface{ Scan(...any) error }) (*DeploymentDraft, error) {
	var d DeploymentDraft
	err := row.Scan(&d.ID, &d.AppID, &d.PreviewAppID, &d.PreviewSlug, &d.ContentDigest, &d.BaseRevision, &d.CreatedBy, &d.ExpiresAt, &d.CreatedAt, &d.PromotedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &d, err
}

const draftSelect = `SELECT d.id, d.app_id, d.preview_app_id, COALESCE(a.slug, ''), d.content_digest,
 d.base_revision, d.created_by, d.expires_at, d.created_at, d.promoted_at
 FROM deployment_drafts d LEFT JOIN apps a ON a.id = d.preview_app_id`

func (s *Store) GetDeploymentDraft(appID int64, id string) (*DeploymentDraft, error) {
	return scanDraft(s.db.QueryRow(draftSelect+` WHERE d.app_id = ? AND d.id = ?`, appID, id))
}

func (s *Store) ListDeploymentDrafts(appID int64) ([]DeploymentDraft, error) {
	rows, err := s.db.Query(draftSelect+` WHERE d.app_id = ? ORDER BY d.created_at DESC, d.id LIMIT 100`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []DeploymentDraft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *d)
	}
	return result, rows.Err()
}

func (s *Store) IsDraftPreview(slug string) (bool, error) {
	var found bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM deployment_drafts d JOIN apps a ON a.id = d.preview_app_id WHERE a.slug = ?)`, slug).Scan(&found)
	return found, err
}

func (s *Store) MarkDraftPromoted(appID int64, id string) error {
	_, err := s.db.Exec(`UPDATE deployment_drafts SET promoted_at = ? WHERE app_id = ? AND id = ?`, time.Now().Unix(), appID, id)
	return err
}

func (s *Store) DeleteDeploymentDraft(appID int64, id string) error {
	_, err := s.db.Exec(`DELETE FROM deployment_drafts WHERE app_id = ? AND id = ?`, appID, id)
	return err
}

// ExpiredDraftBundles waits for the existing app reaper to remove previews.
// Retained upload cleanup can then proceed without orphaning a running preview.
func (s *Store) ExpiredDraftBundles(now time.Time) ([]struct {
	AppID    int64
	Slug, ID string
}, error) {
	rows, err := s.db.Query(`SELECT d.app_id, a.slug, d.id FROM deployment_drafts d JOIN apps a ON a.id=d.app_id
 WHERE d.expires_at <= ? AND d.preview_app_id IS NULL LIMIT 100`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []struct {
		AppID    int64
		Slug, ID string
	}
	for rows.Next() {
		var item struct {
			AppID    int64
			Slug, ID string
		}
		if err := rows.Scan(&item.AppID, &item.Slug, &item.ID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
