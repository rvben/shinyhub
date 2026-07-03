package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// writeMigrationEnvelope mirrors the server's writeList: it paginates the full
// set server-side by ?limit=&offset= and returns the standard
// {items,total,limit,offset} envelope (plus any extra sibling keys). Shared by
// the list-migration mock servers so they behave like production.
func writeMigrationEnvelope(w http.ResponseWriter, r *http.Request, all []map[string]any, extra map[string]any) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	total := len(all)
	start := offset
	if start > total {
		start = total
	}
	end := total
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	env := map[string]any{
		"items": all[start:end], "total": total, "limit": limit, "offset": offset,
	}
	for k, v := range extra {
		env[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(env)
}

// ── env ls ───────────────────────────────────────────────────────────────────

func newEnvLsServer(t *testing.T, slug string) *httptest.Server {
	t.Helper()
	all := []map[string]any{
		{"key": "FOO", "value": "bar", "secret": false, "set": true, "updated_at": 1},
		{"key": "SECRET", "value": "", "secret": true, "set": true, "updated_at": 2},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := fmt.Sprintf("/api/apps/%s/env", slug)
		if r.URL.Path != expected {
			http.NotFound(w, r)
			return
		}
		writeMigrationEnvelope(w, r, all, nil)
	}))
}

func TestEnvLs_JSONEnvelopeWithLimit(t *testing.T) {
	resetFormatState(t)
	srv := newEnvLsServer(t, "myapp")
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"env", "ls", "myapp", "--json", "--limit", "1"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("not the standard envelope: %s", out.String())
	}
	if env.Total != 2 || len(env.Items) != 1 || env.Limit != 1 {
		t.Errorf("total=%d items=%d limit=%d", env.Total, len(env.Items), env.Limit)
	}
	if env.Items[0]["key"] != "FOO" {
		t.Errorf("unexpected first item: %v", env.Items[0])
	}
}

// ── data ls ──────────────────────────────────────────────────────────────────

func newDataLsServer(t *testing.T, slug string) *httptest.Server {
	t.Helper()
	all := []map[string]any{
		{"path": "a.csv", "size": 10, "sha256": "abc", "modified_at": 1735689600},
		{"path": "b.csv", "size": 20, "sha256": "def", "modified_at": 1735689601},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := fmt.Sprintf("/api/apps/%s/data", slug)
		if r.URL.Path != expected {
			http.NotFound(w, r)
			return
		}
		writeMigrationEnvelope(w, r, all, map[string]any{"quota_mb": 512, "used_bytes": 30})
	}))
}

func TestDataLs_JSONEnvelopeWithLimit(t *testing.T) {
	resetFormatState(t)
	srv := newDataLsServer(t, "myapp")
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"data", "ls", "myapp", "--json", "--limit", "1"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items     []map[string]any `json:"items"`
		Total     int              `json:"total"`
		Limit     int              `json:"limit"`
		Offset    int              `json:"offset"`
		QuotaMB   float64          `json:"quota_mb"`
		UsedBytes float64          `json:"used_bytes"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("not the standard envelope: %s", out.String())
	}
	if env.Total != 2 || len(env.Items) != 1 || env.Limit != 1 {
		t.Errorf("total=%d items=%d limit=%d", env.Total, len(env.Items), env.Limit)
	}
	if env.Items[0]["path"] != "a.csv" {
		t.Errorf("unexpected first item: %v", env.Items[0])
	}
}

func TestDataLs_PreservesQuotaEnvelopeKeys(t *testing.T) {
	resetFormatState(t)
	srv := newDataLsServer(t, "myapp")
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"data", "ls", "myapp", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %s", out.String())
	}
	if env["quota_mb"] != float64(512) {
		t.Errorf("quota_mb not preserved in envelope: got %v", env["quota_mb"])
	}
	if env["used_bytes"] != float64(30) {
		t.Errorf("used_bytes not preserved in envelope: got %v", env["used_bytes"])
	}
	// items, total, limit, offset must all be present.
	for _, key := range []string{"items", "total", "limit", "offset"} {
		if _, ok := env[key]; !ok {
			t.Errorf("envelope missing key %q", key)
		}
	}
}

// ── schedule ls ───────────────────────────────────────────────────────────────

func newScheduleLsServer(t *testing.T, slug string) *httptest.Server {
	t.Helper()
	all := []map[string]any{
		{"id": 1, "name": "nightly", "cron_expr": "0 2 * * *", "command": []string{"python", "run.py"}, "enabled": true, "timeout_seconds": 3600, "overlap_policy": "skip", "missed_policy": "skip", "effective_timezone": "UTC", "timezone_inherited": false},
		{"id": 2, "name": "weekly", "cron_expr": "0 3 * * 0", "command": []string{"Rscript", "report.R"}, "enabled": false, "timeout_seconds": 7200, "overlap_policy": "skip", "missed_policy": "skip", "effective_timezone": "UTC", "timezone_inherited": true},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := fmt.Sprintf("/api/apps/%s/schedules", slug)
		if r.URL.Path != expected {
			http.NotFound(w, r)
			return
		}
		writeMigrationEnvelope(w, r, all, nil)
	}))
}

func TestScheduleLs_JSONEnvelopeWithLimit(t *testing.T) {
	resetFormatState(t)
	srv := newScheduleLsServer(t, "myapp")
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"schedule", "ls", "myapp", "--json", "--limit", "1"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("not the standard envelope: %s", out.String())
	}
	if env.Total != 2 || len(env.Items) != 1 || env.Limit != 1 {
		t.Errorf("total=%d items=%d limit=%d", env.Total, len(env.Items), env.Limit)
	}
	if env.Items[0]["name"] != "nightly" {
		t.Errorf("unexpected first item: %v", env.Items[0])
	}
}

// ── share ls ─────────────────────────────────────────────────────────────────

func newShareLsServer(t *testing.T, slug string) *httptest.Server {
	t.Helper()
	all := []map[string]any{
		{"source_slug": "fetcher", "source_id": 7},
		{"source_slug": "loader", "source_id": 9},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := fmt.Sprintf("/api/apps/%s/shared-data", slug)
		if r.URL.Path != expected {
			http.NotFound(w, r)
			return
		}
		writeMigrationEnvelope(w, r, all, nil)
	}))
}

func TestShareLs_JSONEnvelopeWithLimit(t *testing.T) {
	resetFormatState(t)
	srv := newShareLsServer(t, "myapp")
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"share", "ls", "myapp", "--json", "--limit", "1"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("not the standard envelope: %s", out.String())
	}
	if env.Total != 2 || len(env.Items) != 1 || env.Limit != 1 {
		t.Errorf("total=%d items=%d limit=%d", env.Total, len(env.Items), env.Limit)
	}
	if env.Items[0]["source_slug"] != "fetcher" {
		t.Errorf("unexpected first item: %v", env.Items[0])
	}
}

func TestShareLs_NewJSONAlias(t *testing.T) {
	resetFormatState(t)
	srv := newShareLsServer(t, "myapp")
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	// Verify the new --json alias works (share ls previously had no --json).
	root.SetArgs([]string{"share", "ls", "myapp", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %s", out.String())
	}
	if _, ok := env["items"]; !ok {
		t.Error("envelope missing items key")
	}
}
