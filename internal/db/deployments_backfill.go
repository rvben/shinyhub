package db

import "fmt"

// RelativeBundleDeployment identifies a deployment whose stored bundle_dir is a
// relative path needing normalization to absolute.
type RelativeBundleDeployment struct {
	ID      int64
	Slug    string
	Version string
}

// DeploymentsWithRelativeBundleDir returns deployments whose bundle_dir is not an
// absolute path (does not start with '/'), joined to their app slug, so the
// caller can reconstruct the canonical absolute path.
func (s *Store) DeploymentsWithRelativeBundleDir() ([]RelativeBundleDeployment, error) {
	rows, err := s.db.Query(`
		SELECT d.id, a.slug, d.version
		FROM deployments d JOIN apps a ON a.id = d.app_id
		WHERE d.bundle_dir NOT LIKE '/%'`)
	if err != nil {
		return nil, fmt.Errorf("list relative bundle dirs: %w", err)
	}
	defer rows.Close()
	var out []RelativeBundleDeployment
	for rows.Next() {
		var r RelativeBundleDeployment
		if err := rows.Scan(&r.ID, &r.Slug, &r.Version); err != nil {
			return nil, fmt.Errorf("scan relative bundle dir: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetDeploymentBundleDir updates one deployment's stored bundle_dir.
func (s *Store) SetDeploymentBundleDir(id int64, bundleDir string) error {
	_, err := s.db.Exec(`UPDATE deployments SET bundle_dir = ? WHERE id = ?`, bundleDir, id)
	if err != nil {
		return fmt.Errorf("set bundle dir: %w", err)
	}
	return nil
}
