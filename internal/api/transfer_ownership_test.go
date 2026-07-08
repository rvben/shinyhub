package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestTransferOwnership_Gate pins who may transfer: the current owner and
// platform admin/operator can; a manager-role member cannot (ownership is a
// step above manage, matching Posit Connect's owner-vs-collaborator split).
func TestTransferOwnership_Gate(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, ownerTok := mkUser(t, store, "owner", "developer")
	aliceID, _ := mkUser(t, store, "alice", "developer")
	mgrID, mgrTok := mkUser(t, store, "mgr", "developer")
	_, adminTok := mkUser(t, store, "boss", "admin")
	if err := store.CreateApp(db.CreateAppParams{Slug: "xfer", Name: "Xfer", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantAppAccessWithRole("xfer", mgrID, "manager"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, "POST", "/api/apps/xfer/owner", mgrTok, []byte(`{"username":"alice"}`))
	if rec.Code != http.StatusForbidden {
		t.Errorf("manager transfer = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	rec = do(t, srv, "POST", "/api/apps/xfer/owner", ownerTok, []byte(`{"username":"alice"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner transfer = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	app, err := store.GetAppBySlug("xfer")
	if err != nil {
		t.Fatal(err)
	}
	if app.OwnerID != aliceID {
		t.Errorf("owner_id = %d, want %d (alice)", app.OwnerID, aliceID)
	}

	// The old owner has no membership on the (private) app anymore: locked out
	// with the anti-enumeration 404 shape.
	rec = do(t, srv, "GET", "/api/apps/xfer", ownerTok, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("old owner GET after transfer = %d, want 404", rec.Code)
	}

	// Admin can transfer it back without being owner or member.
	rec = do(t, srv, "POST", "/api/apps/xfer/owner", adminTok, []byte(fmt.Sprintf(`{"user_id":%d}`, ownerID)))
	if rec.Code != http.StatusOK {
		t.Errorf("admin transfer = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// The transfer is audited with before/after.
	rec = do(t, srv, "GET", "/api/audit?action=transfer_ownership", adminTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit read = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "transfer_ownership") {
		t.Errorf("expected transfer_ownership audit events, got %s", rec.Body.String())
	}
}

// TestTransferOwnership_TargetValidation pins the target rules: system users
// are refused, unknown users 404, and transferring to the current owner is an
// idempotent no-op.
func TestTransferOwnership_TargetValidation(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, ownerTok := mkUser(t, store, "owner", "developer")
	if err := store.CreateApp(db.CreateAppParams{Slug: "xfer", Name: "Xfer", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, "POST", "/api/apps/xfer/owner", ownerTok, []byte(`{"username":"__deploy__"}`))
	if rec.Code != http.StatusForbidden {
		t.Errorf("transfer to system user = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	rec = do(t, srv, "POST", "/api/apps/xfer/owner", ownerTok, []byte(`{"username":"ghost"}`))
	if rec.Code != http.StatusNotFound {
		t.Errorf("transfer to unknown user = %d, want 404", rec.Code)
	}
	rec = do(t, srv, "POST", "/api/apps/xfer/owner", ownerTok, []byte(`{"username":"owner"}`))
	if rec.Code != http.StatusOK {
		t.Errorf("transfer to current owner = %d, want 200 (no-op)", rec.Code)
	}
	rec = do(t, srv, "POST", "/api/apps/xfer/owner", ownerTok, []byte(`{}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("transfer with no target = %d, want 400", rec.Code)
	}
}
