package db_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
)

// ListAuditEventsFiltered's action-only filter (used by the audit log page's
// action dropdown) runs `WHERE ae.action = ? ORDER BY ae.created_at DESC,
// ae.id DESC`. Without an index leading with action, SQLite must scan the
// whole table filtering by action, then build a temp B-tree to satisfy the
// ORDER BY. Guards migration 087's idx_audit_action index.
func TestListAuditEventsFilteredByActionAvoidsTemporarySort(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	store := mustOpenDB(t)
	rows, err := store.DB().Query(`EXPLAIN QUERY PLAN
		SELECT ae.id, ae.user_id, u.username,
		       ae.action, ae.resource_type, ae.resource_id,
		       ae.detail, ae.ip_address, ae.created_at, ae.run_id,
		       COALESCE(u.principal_type, ''), COALESCE(u.service_account_key, ''),
		       ae.credential_id, ae.credential_type, ae.credential_name
		FROM audit_events ae
		LEFT JOIN users u ON u.id = ae.user_id
		WHERE ae.action = ?
		ORDER BY ae.created_at DESC, ae.id DESC LIMIT ? OFFSET ?`, "app.deploy", 100, 0)
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
	if strings.Contains(plan, "TEMP B-TREE") || !strings.Contains(plan, "idx_audit_action") {
		t.Fatalf("audit action filter query is not index-ordered:\n%s", plan)
	}
}
