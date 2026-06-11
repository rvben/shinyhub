package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestShare_Ls_FormatsRows verifies share ls hits the right URL and prints rows.
func TestShare_Ls_FormatsRows(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `[{"source_slug":"fetcher","source_id":7},{"source_slug":"loader","source_id":9}]`)

	cmd := newShareCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"ls", "demo"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "GET" {
		t.Errorf("expected GET, got %s", req.Method)
	}
	if req.Path != "/api/apps/demo/shared-data" {
		t.Errorf("unexpected path: %s", req.Path)
	}
	if req.Auth == "" {
		t.Error("expected Authorization header to be set")
	}
	out := buf.String()
	if !strings.Contains(out, "fetcher") || !strings.Contains(out, "loader") {
		t.Errorf("expected output to contain both source slugs, got: %s", out)
	}
}

// TestShare_Add_PostsSourceSlug verifies share add posts the right JSON body.
func TestShare_Add_PostsSourceSlug(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(201, `{"source_slug":"fetcher","source_id":7}`)

	cmd := newShareCmd()
	cmd.SetArgs([]string{"add", "demo", "--from", "fetcher"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "POST" {
		t.Errorf("expected POST, got %s", req.Method)
	}
	if req.Path != "/api/apps/demo/shared-data" {
		t.Errorf("unexpected path: %s", req.Path)
	}

	var body map[string]string
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["source_slug"] != "fetcher" {
		t.Errorf("expected source_slug=fetcher, got %q", body["source_slug"])
	}
}

// TestShare_Add_PrintsReadOnlyWarning verifies that when the server returns a
// read-only-convention warning (native runtime), share add surfaces it on
// stderr while the success line stays on stdout.
func TestShare_Add_PrintsReadOnlyWarning(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(201, `{"source_slug":"fetcher","source_id":7,"warning":"Read-only is a convention under the native runtime. Switch to the Docker runtime for OS-level read-only enforcement."}`)

	cmd := newShareCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"add", "demo", "--from", "fetcher"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// In piped mode the output is a JSON envelope; check for the mounted status.
	if !strings.Contains(out.String(), `"mounted"`) {
		t.Errorf("expected mounted status on stdout, got: %s", out.String())
	}
	if !strings.Contains(errBuf.String(), "Read-only is a convention") {
		t.Errorf("expected read-only warning on stderr, got: %s", errBuf.String())
	}
}

// TestShare_Add_NoWarningWhenAbsent verifies stderr stays clean when the server
// returns no warning (Docker runtime).
func TestShare_Add_NoWarningWhenAbsent(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(201, `{"source_slug":"fetcher","source_id":7}`)

	cmd := newShareCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"add", "demo", "--from", "fetcher"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(errBuf.String()) != "" {
		t.Errorf("expected clean stderr without a warning, got: %s", errBuf.String())
	}
}

// TestShare_Add_RequiresFromFlag verifies cobra rejects add without --from.
func TestShare_Add_RequiresFromFlag(t *testing.T) {
	_, _, _ = setupCLITest(t)

	cmd := newShareCmd()
	cmd.SetArgs([]string{"add", "demo"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing --from flag")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "from") {
		t.Errorf("expected error to mention 'from', got: %v", err)
	}
}

// TestShare_Rm_DeletesBySlug verifies share rm hits the right URL.
func TestShare_Rm_DeletesBySlug(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(204, "")

	cmd := newShareCmd()
	cmd.SetArgs([]string{"rm", "demo", "fetcher"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "DELETE" {
		t.Errorf("expected DELETE, got %s", req.Method)
	}
	if req.Path != "/api/apps/demo/shared-data/fetcher" {
		t.Errorf("unexpected path: %s", req.Path)
	}
}

// TestShare_Rm_PropagatesServerError verifies non-2xx responses surface as errors.
func TestShare_Rm_PropagatesServerError(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(404, `{"error":"not mounted"}`)

	cmd := newShareCmd()
	cmd.SetArgs([]string{"rm", "demo", "missing"})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}
