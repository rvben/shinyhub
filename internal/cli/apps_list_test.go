package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newAppsListServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"slug":"a","status":"running","deploy_count":3},{"slug":"b","status":"stopped","deploy_count":1}]`))
	}))
}

func TestAppsList_JSONEnvelopeWithLimit(t *testing.T) {
	resetFormatState(t)
	srv := newAppsListServer(t)
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"apps", "list", "--json", "--limit", "1"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("not the standard envelope: %s", out.String())
	}
	if env.Total != 2 || len(env.Items) != 1 {
		t.Errorf("total=%d items=%d", env.Total, len(env.Items))
	}
}

// ── apps deployments ────────────────────────────────────────────────────────

func newAppsDeploymentsServer(t *testing.T, slug string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := fmt.Sprintf("/api/apps/%s/deployments", slug)
		if r.URL.Path != expected {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":10,"version":"v3","status":"active","created_at":"2026-01-01T00:00:00Z"},{"id":9,"version":"v2","status":"superseded","created_at":"2025-12-01T00:00:00Z"}]`))
	}))
}

func TestAppsDeployments_JSONEnvelopeWithLimit(t *testing.T) {
	resetFormatState(t)
	srv := newAppsDeploymentsServer(t, "myapp")
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"apps", "deployments", "myapp", "--json", "--limit", "1"})
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
}

// ── apps access list ────────────────────────────────────────────────────────

func newAppsAccessListServer(t *testing.T, slug string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := fmt.Sprintf("/api/apps/%s/members", slug)
		if r.URL.Path != expected {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"user_id":1,"username":"alice","role":"manager"},{"user_id":2,"username":"bob","role":"viewer"}]`))
	}))
}

func TestAppsAccessList_JSONEnvelopeWithLimit(t *testing.T) {
	resetFormatState(t)
	srv := newAppsAccessListServer(t, "myapp")
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"apps", "access", "list", "myapp", "--json", "--limit", "1"})
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
	if env.Items[0]["username"] != "alice" {
		t.Errorf("unexpected first item: %v", env.Items[0])
	}
}

// ── apps access group-list ──────────────────────────────────────────────────

func newAppsAccessGroupListServer(t *testing.T, slug string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := fmt.Sprintf("/api/apps/%s/group-access", slug)
		if r.URL.Path != expected {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"group":"eng","role":"manager"},{"group":"qa","role":"viewer"}]`))
	}))
}

func TestAppsAccessGroupList_JSONEnvelopeWithLimit(t *testing.T) {
	resetFormatState(t)
	srv := newAppsAccessGroupListServer(t, "myapp")
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"apps", "access", "group-list", "myapp", "--json", "--limit", "1"})
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
	if env.Items[0]["group"] != "eng" {
		t.Errorf("unexpected first item: %v", env.Items[0])
	}
}

// ── tokens list ─────────────────────────────────────────────────────────────

func newTokensListServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tokens" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"name":"ci-token","created_at":"2026-01-01T00:00:00Z"},{"id":2,"name":"dev-token","created_at":"2026-02-01T00:00:00Z"}]`))
	}))
}

func TestTokensList_JSONEnvelopeWithLimit(t *testing.T) {
	resetFormatState(t)
	srv := newTokensListServer(t)
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"tokens", "list", "--json", "--limit", "1"})
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
	if env.Items[0]["name"] != "ci-token" {
		t.Errorf("unexpected first item: %v", env.Items[0])
	}
}
