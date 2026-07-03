package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestUsersList_Table(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	// The server returns the standard {items,...} envelope, already ordered by
	// username (ListUsers ORDER BY username), so the CLI renders the page as-is.
	setResp(200, `{"items":[{"id":1,"username":"alice","role":"admin","created_at":"2026-01-01T00:00:00Z"},{"id":2,"username":"bob","role":"developer","created_at":"2026-01-02T00:00:00Z"}],"total":2,"limit":0,"offset":0}`)

	out, err := execCLI(t, "users", "list", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 1 || (*reqs)[0].Method != "GET" || (*reqs)[0].Path != "/api/users" {
		t.Fatalf("expected GET /api/users, got %+v", *reqs)
	}
	// Sorted by username: alice before bob.
	if i, j := strings.Index(out, "alice"), strings.Index(out, "bob"); i < 0 || j < 0 || i > j {
		t.Errorf("users should be listed sorted by username:\n%s", out)
	}
	if !strings.Contains(out, "USERNAME") || !strings.Contains(out, "ROLE") {
		t.Errorf("table header missing:\n%s", out)
	}
}

func TestUsersList_JSONEnvelope(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"items":[{"id":1,"username":"alice","role":"admin","created_at":"2026-01-01T00:00:00Z"}],"total":1,"limit":0,"offset":0}`)

	out, err := execCLI(t, "users", "list") // piped => JSON
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); jerr != nil {
		t.Fatalf("not valid JSON: %v\n%q", jerr, out)
	}
	if env["total"] != float64(1) {
		t.Errorf("total = %v, want 1", env["total"])
	}
}

func TestUsersCreate_PostsBody(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(201, `{"id":7,"username":"sam","role":"developer","created_at":"2026-01-01T00:00:00Z"}`)

	out, err := execCLI(t, "users", "create", "--username", "sam", "--password", "s3cr3tpass", "--role", "developer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := (*reqs)[0]
	if r.Method != "POST" || r.Path != "/api/users" {
		t.Fatalf("expected POST /api/users, got %s %s", r.Method, r.Path)
	}
	var body map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["username"] != "sam" || body["password"] != "s3cr3tpass" || body["role"] != "developer" {
		t.Errorf("unexpected create body: %v", body)
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); jerr != nil {
		t.Fatalf("create output not JSON: %v\n%q", jerr, out)
	}
	if env["status"] != "created" || env["id"] != float64(7) {
		t.Errorf("unexpected create envelope: %v", env)
	}
}

func TestUsersCreate_InvalidRoleFailsFast(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	_, err := execCLI(t, "users", "create", "--username", "sam", "--password", "s3cr3tpass", "--role", "superuser")
	if err == nil {
		t.Fatal("expected a validation error for an invalid role")
	}
	if kind, code := classify(err); kind != KindValidation || code != 1 {
		t.Errorf("invalid role should be validation/exit 1, got kind=%q code=%d", kind, code)
	}
	if len(*reqs) != 0 {
		t.Errorf("a bad role must fail before any request, got %d requests", len(*reqs))
	}
}

func TestUsersCreate_403IsAuthError(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(403, `{"error":"forbidden"}`)

	_, err := execCLI(t, "users", "create", "--username", "sam", "--password", "s3cr3tpass")
	if err == nil {
		t.Fatal("expected an error for 403")
	}
	if kind, code := classify(err); kind != KindAuth || code != 3 {
		t.Errorf("403 should be auth/exit 3, got kind=%q code=%d", kind, code)
	}
}

// usersResolveHandler routes the two-request resolve-then-act flow used by
// set-role/reset-password/delete: GET /api/users/{name} returns the id, the
// follow-up PATCH/DELETE on /api/users/{id} succeeds.
func usersResolveHandler(id int64, actStatus int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/users/"):
			_, _ = fmt.Fprintf(w, `{"id":%d,"username":%q}`, id, strings.TrimPrefix(r.URL.Path, "/api/users/"))
		default:
			w.WriteHeader(actStatus)
		}
	}
}

func TestUsersSetRole_ResolvesThenPatches(t *testing.T) {
	_, reqs := setupCLITestHandler(t, usersResolveHandler(9, 200))

	out, err := execCLI(t, "users", "set-role", "sam", "--role", "operator")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 2 {
		t.Fatalf("expected resolve + patch (2 requests), got %d: %+v", len(*reqs), *reqs)
	}
	if (*reqs)[0].Method != "GET" || (*reqs)[0].Path != "/api/users/sam" {
		t.Errorf("first request should resolve the username, got %s %s", (*reqs)[0].Method, (*reqs)[0].Path)
	}
	patch := (*reqs)[1]
	if patch.Method != "PATCH" || patch.Path != "/api/users/9" {
		t.Errorf("second request should PATCH /api/users/9, got %s %s", patch.Method, patch.Path)
	}
	var body map[string]any
	_ = json.Unmarshal(patch.Body, &body)
	if body["role"] != "operator" {
		t.Errorf("patch body role = %v, want operator", body["role"])
	}
	if !strings.Contains(out, "operator") {
		t.Errorf("output should confirm the new role:\n%s", out)
	}
}

func TestUsersResetPassword_PatchesPasswordEndpoint(t *testing.T) {
	_, reqs := setupCLITestHandler(t, usersResolveHandler(9, 204))

	if _, err := execCLI(t, "users", "reset-password", "sam", "--password", "newpass12"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 2 || (*reqs)[1].Path != "/api/users/9/password" {
		t.Fatalf("expected PATCH /api/users/9/password, got %+v", *reqs)
	}
	var body map[string]any
	_ = json.Unmarshal((*reqs)[1].Body, &body)
	if body["password"] != "newpass12" {
		t.Errorf("password not sent: %v", body)
	}
}

func TestUsersDelete_RequiresConfirmationOnNonTTY(t *testing.T) {
	_, reqs := setupCLITestHandler(t, usersResolveHandler(9, 204))

	// execCLI passes no stdin (non-TTY), and --yes is absent.
	_, err := execCLI(t, "users", "delete", "sam")
	if err == nil {
		t.Fatal("delete without --yes on a non-TTY must refuse")
	}
	if kind, _ := classify(err); kind != KindConfirmationRequired {
		t.Errorf("expected confirmation_required, got kind=%q", kind)
	}
	if len(*reqs) != 0 {
		t.Errorf("delete must not touch the server before confirmation, got %d requests", len(*reqs))
	}
}

func TestUsersDelete_WithYesResolvesThenDeletes(t *testing.T) {
	_, reqs := setupCLITestHandler(t, usersResolveHandler(9, 204))

	if _, err := execCLI(t, "users", "delete", "sam", "--yes"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*reqs) != 2 || (*reqs)[1].Method != "DELETE" || (*reqs)[1].Path != "/api/users/9" {
		t.Fatalf("expected DELETE /api/users/9, got %+v", *reqs)
	}
}
