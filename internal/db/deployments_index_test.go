package db_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
)

// ListRecentDeployments runs the same shape query as ListDeployments but with
// a LIMIT, on the hot paths (watcher, recovery, rollback default target) that
// only ever need the newest one or two rows. Without an index whose leading
// columns match (app_id, id DESC), SQLite must build the full per-app result
// set into a temp B-tree to satisfy ORDER BY id DESC before the LIMIT can
// drop the rest; idx_deployments_app_created is ordered by (created_at DESC,
// id DESC) and does not cover a pure id-ordered scan. Guards migration 086's
// idx_deployments_app_id_desc index.
func TestListRecentDeploymentsQueryAvoidsTemporarySort(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	store := mustOpenDB(t)
	rows, err := store.DB().Query(`EXPLAIN QUERY PLAN
		SELECT id, app_id, version, bundle_dir, status, content_digest, created_at, prepared
		FROM deployments
		WHERE app_id = ? AND status NOT IN ('pending', 'failed')
		ORDER BY id DESC LIMIT ?`, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, "\n")
	if strings.Contains(plan, "TEMP B-TREE") || !strings.Contains(plan, "idx_deployments_app_id_desc") {
		t.Fatalf("recent-deployments query is not index-ordered:\n%s", plan)
	}
}
