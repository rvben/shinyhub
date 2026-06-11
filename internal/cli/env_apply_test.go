package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// --- Parser ---

func TestParseEnvFile_HappyPath(t *testing.T) {
	in := strings.NewReader(`
# comment
FOO=bar
BAZ="quoted with \"escape\" and \n newline"
QUX='literal $no_expand'
export EXPORTED=ok
EMPTY=
TRAILING_COMMENT=value # ignored
`)
	got, err := parseEnvFile(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []envFileEntry{
		{Key: "FOO", Value: "bar"},
		{Key: "BAZ", Value: "quoted with \"escape\" and \n newline"},
		{Key: "QUX", Value: "literal $no_expand"},
		{Key: "EXPORTED", Value: "ok"},
		{Key: "EMPTY", Value: ""},
		{Key: "TRAILING_COMMENT", Value: "value"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseEnvFile mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestParseEnvFile_RejectsDuplicateKey(t *testing.T) {
	_, err := parseEnvFile(strings.NewReader("FOO=1\nFOO=2\n"))
	if err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("expected duplicate-key error, got %v", err)
	}
}

func TestParseEnvFile_RejectsInvalidKey(t *testing.T) {
	for _, line := range []string{"foo=bar", "1ABC=x", "=novalue", "WITH SPACE=x"} {
		t.Run(line, func(t *testing.T) {
			if _, err := parseEnvFile(strings.NewReader(line)); err == nil {
				t.Errorf("expected error for %q", line)
			}
		})
	}
}

func TestParseEnvFile_UnterminatedQuote(t *testing.T) {
	if _, err := parseEnvFile(strings.NewReader(`FOO="unclosed`)); err == nil {
		t.Error("expected unterminated-quote error")
	}
	if _, err := parseEnvFile(strings.NewReader(`FOO='unclosed`)); err == nil {
		t.Error("expected unterminated-quote error")
	}
}

// --- Differ ---

func TestDiffEnvApply_AddsUpdatesSkippedDeletes(t *testing.T) {
	desired := []envFileEntry{
		{Key: "ADD_ME", Value: "new"},
		{Key: "CHANGE_ME", Value: "v2"},
		{Key: "SAME", Value: "x"},
		{Key: "SECRET_FLIP", Value: "y", Secret: true},
		{Key: "ROTATE_SECRET", Value: "rotated", Secret: true},
	}
	current := []envServerVar{
		{Key: "CHANGE_ME", Value: "v1", Set: true},
		{Key: "SAME", Value: "x", Set: true},
		{Key: "SECRET_FLIP", Set: true}, // was not secret server-side
		{Key: "ROTATE_SECRET", Secret: true, Set: true},
		{Key: "STALE", Value: "old", Set: true},
	}

	plan := diffEnvApply(desired, current, true)

	wantAdds := []envApplyOp{{Key: "ADD_ME"}}
	wantUpdates := []envApplyOp{
		{Key: "CHANGE_ME", Reason: "value"},
		{Key: "ROTATE_SECRET", Secret: true, Reason: "secret-rotate"},
		{Key: "SECRET_FLIP", Secret: true, Reason: "secret-flag"},
	}
	wantDeletes := []envApplyOp{{Key: "STALE"}}
	wantSkipped := []envApplyOp{{Key: "SAME"}}

	if !reflect.DeepEqual(plan.Adds, wantAdds) {
		t.Errorf("Adds: got %#v want %#v", plan.Adds, wantAdds)
	}
	if !reflect.DeepEqual(plan.Updates, wantUpdates) {
		t.Errorf("Updates: got %#v want %#v", plan.Updates, wantUpdates)
	}
	if !reflect.DeepEqual(plan.Deletes, wantDeletes) {
		t.Errorf("Deletes: got %#v want %#v", plan.Deletes, wantDeletes)
	}
	if !reflect.DeepEqual(plan.Skipped, wantSkipped) {
		t.Errorf("Skipped: got %#v want %#v", plan.Skipped, wantSkipped)
	}
}

func TestDiffEnvApply_PruneOffKeepsServerKeys(t *testing.T) {
	desired := []envFileEntry{{Key: "FOO", Value: "1"}}
	current := []envServerVar{
		{Key: "FOO", Value: "1", Set: true},
		{Key: "STALE", Value: "x", Set: true},
	}
	plan := diffEnvApply(desired, current, false)
	if len(plan.Deletes) != 0 {
		t.Errorf("expected no deletes without --prune, got %#v", plan.Deletes)
	}
}

// --- CLI integration ---

// envApplyServer is a small test double that lets each test inject a
// canonical "current env" GET response and capture every subsequent PUT/DELETE.
type envApplyServer struct {
	mu       sync.Mutex
	current  []envServerVar
	requests []capturedReq
	srv      *httptest.Server
}

func newEnvApplyServer(t *testing.T, current []envServerVar) *envApplyServer {
	t.Helper()
	s := &envApplyServer{current: current}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, capturedReq{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body,
		})
		s.mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/env"):
			_ = json.NewEncoder(w).Encode(map[string]any{"env": s.current})
		case r.Method == "PUT":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func writeEnvFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.env")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func setupCLIConfig(t *testing.T, host string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "shinyhub")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := cliConfig{Host: host, Token: "shk_test"}
	f, err := os.Create(filepath.Join(cfgDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(f).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestEnvApply_DryRunDoesNotMutate(t *testing.T) {
	srv := newEnvApplyServer(t, []envServerVar{
		{Key: "OLD", Value: "x", Set: true},
	})
	setupCLIConfig(t, srv.srv.URL)
	path := writeEnvFile(t, "NEW=1\n")

	cmd := newEnvCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"apply", "demo", path, "--dry-run", "--prune"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout:\n%s", err, out.String())
	}

	for _, req := range srv.requests {
		if req.Method == "PUT" || req.Method == "DELETE" {
			t.Errorf("dry-run made mutating request: %s %s", req.Method, req.Path)
		}
	}
	body := out.String()
	if !strings.Contains(body, "dry run") {
		t.Errorf("text output should announce dry-run, got:\n%s", body)
	}
	if !strings.Contains(body, "+ NEW") {
		t.Errorf("text output should show add for NEW, got:\n%s", body)
	}
	if !strings.Contains(body, "- OLD") {
		t.Errorf("text output should show delete for OLD with --prune, got:\n%s", body)
	}
}

func TestEnvApply_AppliesPlan(t *testing.T) {
	srv := newEnvApplyServer(t, []envServerVar{
		{Key: "KEEP", Value: "same", Set: true},
		{Key: "CHANGE", Value: "v1", Set: true},
		{Key: "DROP", Value: "x", Set: true},
	})
	setupCLIConfig(t, srv.srv.URL)
	path := writeEnvFile(t, `
KEEP=same
CHANGE=v2
ADD=fresh
TOKEN=hidden
`)

	cmd := newEnvCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"apply", "demo", path, "--prune", "--secret", "TOKEN"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout:\n%s", err, out.String())
	}

	var puts, deletes []capturedReq
	for _, r := range srv.requests {
		switch r.Method {
		case "PUT":
			puts = append(puts, r)
		case "DELETE":
			deletes = append(deletes, r)
		}
	}
	// Expected mutations: PUT ADD, PUT CHANGE, PUT TOKEN, DELETE DROP.
	wantPutKeys := map[string]bool{"ADD": false, "CHANGE": false, "TOKEN": false}
	for _, p := range puts {
		key := lastPathSegment(p.Path)
		if _, ok := wantPutKeys[key]; ok {
			wantPutKeys[key] = true
		}
	}
	for k, seen := range wantPutKeys {
		if !seen {
			t.Errorf("expected PUT for %s, got %d PUTs total", k, len(puts))
		}
	}
	if len(deletes) != 1 || lastPathSegment(deletes[0].Path) != "DROP" {
		t.Errorf("expected DELETE /env/DROP, got %#v", deletes)
	}

	// TOKEN must be sent with secret=true.
	for _, p := range puts {
		if lastPathSegment(p.Path) != "TOKEN" {
			continue
		}
		var body struct {
			Value  string `json:"value"`
			Secret bool   `json:"secret"`
		}
		if err := json.Unmarshal(p.Body, &body); err != nil {
			t.Fatalf("decode TOKEN body: %v", err)
		}
		if body.Value != "hidden" {
			t.Errorf("TOKEN value: got %q want %q", body.Value, "hidden")
		}
		if !body.Secret {
			t.Error("TOKEN should be sent with secret=true")
		}
	}

	// KEEP must NOT generate a PUT (it's unchanged).
	for _, p := range puts {
		if lastPathSegment(p.Path) == "KEEP" {
			t.Errorf("KEEP is unchanged; no PUT expected")
		}
	}
}

func TestEnvApply_JSONOutput(t *testing.T) {
	srv := newEnvApplyServer(t, []envServerVar{
		{Key: "OLD", Set: true, Value: "x"},
	})
	setupCLIConfig(t, srv.srv.URL)
	path := writeEnvFile(t, "FOO=bar\n")

	cmd := newEnvCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"apply", "demo", path, "--dry-run", "--format", "json", "--prune"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout:\n%s", err, out.String())
	}

	var plan envApplyPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("decode JSON: %v\nraw:\n%s", err, out.String())
	}
	if len(plan.Adds) != 1 || plan.Adds[0].Key != "FOO" {
		t.Errorf("JSON adds = %#v, want [{FOO}]", plan.Adds)
	}
	if len(plan.Deletes) != 1 || plan.Deletes[0].Key != "OLD" {
		t.Errorf("JSON deletes = %#v, want [{OLD}]", plan.Deletes)
	}

	// Values must NEVER appear in JSON output to avoid secret leakage.
	if bytes.Contains(out.Bytes(), []byte("bar")) {
		t.Errorf("plan JSON must not include values, got:\n%s", out.String())
	}
}

func TestEnvApply_SecretKeyNotInFileIsError(t *testing.T) {
	srv := newEnvApplyServer(t, nil)
	setupCLIConfig(t, srv.srv.URL)
	path := writeEnvFile(t, "FOO=1\n")

	cmd := newEnvCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"apply", "demo", path, "--secret", "MISSING", "--dry-run"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "MISSING") {
		t.Errorf("expected error mentioning MISSING, got %v", err)
	}
}

func TestEnvApply_RestartFiresOnce(t *testing.T) {
	srv := newEnvApplyServer(t, nil)
	setupCLIConfig(t, srv.srv.URL)
	path := writeEnvFile(t, "FOO=1\nBAR=2\n")

	cmd := newEnvCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"apply", "demo", path, "--restart"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nout:\n%s", err, out.String())
	}

	restartCount := 0
	for _, r := range srv.requests {
		if r.Method == "PUT" && strings.Contains(r.Query, "restart=true") {
			restartCount++
		}
	}
	if restartCount != 1 {
		t.Errorf("expected exactly one restart=true PUT, got %d", restartCount)
	}
}

// TestEnvApply_FormatTextConflictsWithOutputJson verifies the
// resolveLegacyTextJSON conflict path: --format text combined with -o json
// is a validation error so consumers receive a predictable error envelope
// rather than one flag silently overriding the other.
func TestEnvApply_FormatTextConflictsWithOutputJson(t *testing.T) {
	srv := newEnvApplyServer(t, nil)
	setupCLIConfig(t, srv.srv.URL)
	path := writeEnvFile(t, "FOO=1\n")

	_, err := execCLI(t, "env", "apply", "demo", path, "--format", "text", "-o", "json")
	if err == nil {
		t.Fatal("want error for --format text -o json conflict, got nil")
	}
	if code := exitCode(err); code != 1 {
		t.Errorf("exit code = %d, want 1 (validation)", code)
	}
}

// lastPathSegment returns the segment after the final "/".
func lastPathSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
