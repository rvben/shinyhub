package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func newAuditFlagServer(t *testing.T, operatorAccess bool) (*api.Server, *db.Store) {
	t.Helper()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret", OperatorAuditAccess: operatorAccess},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}
	return api.New(cfg, store, nil, nil), store
}

func meCanReadAudit(t *testing.T, srv *api.Server, tok string) bool {
	t.Helper()
	rec := do(t, srv, "GET", "/api/auth/me", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me = %d", rec.Code)
	}
	var resp struct {
		CanReadAudit bool `json:"can_read_audit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.CanReadAudit
}

// TestAuditAccess_OperatorFlag pins the audit-read gate: admin always, operator
// only behind auth.operator_audit_access (default off), everyone else never.
// The /me payload advertises the capability so the UI shows the tab to exactly
// the users who can use it.
func TestAuditAccess_OperatorFlag(t *testing.T) {
	for _, flag := range []bool{false, true} {
		srv, store := newAuditFlagServer(t, flag)
		_, adminTok := mkUser(t, store, "boss", "admin")
		_, opTok := mkUser(t, store, "ops", "operator")
		_, devTok := mkUser(t, store, "dev", "developer")

		if rec := do(t, srv, "GET", "/api/audit", adminTok, nil); rec.Code != http.StatusOK {
			t.Errorf("flag=%v: admin audit = %d, want 200", flag, rec.Code)
		}
		wantOp := http.StatusForbidden
		if flag {
			wantOp = http.StatusOK
		}
		if rec := do(t, srv, "GET", "/api/audit", opTok, nil); rec.Code != wantOp {
			t.Errorf("flag=%v: operator audit = %d, want %d", flag, rec.Code, wantOp)
		}
		if rec := do(t, srv, "GET", "/api/audit", devTok, nil); rec.Code != http.StatusForbidden {
			t.Errorf("flag=%v: developer audit = %d, want 403", flag, rec.Code)
		}

		if !meCanReadAudit(t, srv, adminTok) {
			t.Errorf("flag=%v: admin can_read_audit should be true", flag)
		}
		if got := meCanReadAudit(t, srv, opTok); got != flag {
			t.Errorf("flag=%v: operator can_read_audit = %v", flag, got)
		}
		if meCanReadAudit(t, srv, devTok) {
			t.Errorf("flag=%v: developer can_read_audit should be false", flag)
		}
	}
}
