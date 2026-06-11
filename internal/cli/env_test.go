package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type capturedReq struct {
	Method string
	Path   string
	Query  string
	Body   []byte
	Auth   string
}

// writeTestCLIConfig points the CLI at host by writing a config.json into a
// fresh HOME, so execCLI/execCLISplit pick it up exactly as the shipped binary
// would.
func writeTestCLIConfig(t *testing.T, host string) {
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

func setupCLITest(t *testing.T) (*httptest.Server, *[]capturedReq, func(int, string)) {
	t.Helper()
	respStatus := 200
	respBody := `{}`
	srv, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(respStatus)
		_, _ = w.Write([]byte(respBody))
	})
	setResp := func(status int, body string) {
		respStatus = status
		respBody = body
	}
	return srv, reqs, setResp
}

// setupCLITestHandler is the lower-level harness used by tests that need to
// vary the response per request (e.g. polling loops that must observe a state
// transition across successive GETs). It still captures every request for
// assertions. The handler is invoked after the request has been recorded.
func setupCLITestHandler(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *[]capturedReq) {
	t.Helper()
	var reqs []capturedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		reqs = append(reqs, capturedReq{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Body:   body,
			Auth:   r.Header.Get("Authorization"),
		})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	writeTestCLIConfig(t, srv.URL)
	return srv, &reqs
}

func TestEnvSet_NonSecret(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	cmd := newEnvCmd()
	cmd.SetArgs([]string{"set", "demo", "AWS_REGION=eu-west-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	req := (*reqs)[0]
	if req.Method != "PUT" {
		t.Errorf("expected PUT, got %s", req.Method)
	}
	if req.Path != "/api/apps/demo/env/AWS_REGION" {
		t.Errorf("unexpected path: %s", req.Path)
	}
	if req.Auth != "Token shk_test" {
		t.Errorf("unexpected auth: %s", req.Auth)
	}

	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["value"] != "eu-west-1" {
		t.Errorf("expected value eu-west-1, got %v", body["value"])
	}
	if body["secret"] != false {
		t.Errorf("expected secret false, got %v", body["secret"])
	}
}

func TestEnvSet_SecretFromArg(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	cmd := newEnvCmd()
	cmd.SetArgs([]string{"set", "demo", "DB_PASS=hunter2", "--secret"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["value"] != "hunter2" {
		t.Errorf("expected value hunter2, got %v", body["value"])
	}
	if body["secret"] != true {
		t.Errorf("expected secret true, got %v", body["secret"])
	}
}

func TestEnvSet_SecretStdin(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	cmd := newEnvCmd()
	cmd.SetArgs([]string{"set", "demo", "DB_PASS", "--secret", "--stdin"})
	cmd.SetIn(strings.NewReader("super-secret\n"))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["value"] != "super-secret" {
		t.Errorf("expected value 'super-secret' (trimmed), got %v", body["value"])
	}
	if body["secret"] != true {
		t.Errorf("expected secret true, got %v", body["secret"])
	}
}

func TestEnvSet_RejectsBareKeyWithoutStdin(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	cmd := newEnvCmd()
	cmd.SetArgs([]string{"set", "demo", "FOO"})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error for bare key without --stdin, got nil")
	}

	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests, got %d", len(*reqs))
	}
}

func TestEnvSet_RejectsInvalidKey(t *testing.T) {
	_, reqs, _ := setupCLITest(t)

	cmd := newEnvCmd()
	cmd.SetArgs([]string{"set", "demo", "foo=bar"})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error for lowercase key, got nil")
	}
	// Error message should be human-friendly and include the invalid key.
	if err != nil && !strings.Contains(err.Error(), "FOO_BAR") {
		t.Errorf("error should reference example FOO_BAR, got: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "foo") {
		t.Errorf("error should include the invalid key 'foo', got: %v", err)
	}

	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests, got %d", len(*reqs))
	}
}

func TestEnvSet_RestartFlag(t *testing.T) {
	// The env set --restart implementation uses two requests: the first write to
	// check whether the value changed, and a second with ?restart=true only when
	// the value was new (changed:true). The default mock returns {} which the CLI
	// treats as changed=true, so two requests are expected.
	_, reqs, _ := setupCLITest(t)

	cmd := newEnvCmd()
	cmd.SetArgs([]string{"set", "demo", "FOO=bar", "--restart"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 2 {
		t.Fatalf("expected 2 requests (write + restart), got %d", len(*reqs))
	}
	// The first request must NOT have restart=true; it is the value-write probe.
	if strings.Contains((*reqs)[0].Query, "restart=true") {
		t.Errorf("first request must not contain restart=true, got %q", (*reqs)[0].Query)
	}
	// The second request must include restart=true to trigger the app cycle.
	if !strings.Contains((*reqs)[1].Query, "restart=true") {
		t.Errorf("expected second request query to contain restart=true, got %q", (*reqs)[1].Query)
	}
}

func TestEnvLs_MasksSecrets(t *testing.T) {
	resetFormatState(t)
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"env":[{"key":"AWS_REGION","value":"eu-west-1","secret":false,"set":true,"updated_at":1},{"key":"DB_PASS","value":"","secret":true,"set":true,"updated_at":2}]}`)

	// Use --output table to force table rendering and verify secret masking.
	// Non-TTY runs default to JSON; the masking is a table-mode display choice.
	root := testRoot()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"env", "ls", "demo", "--output", "table"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "AWS_REGION") {
		t.Error("expected output to contain AWS_REGION")
	}
	if !strings.Contains(out, "eu-west-1") {
		t.Error("expected output to contain eu-west-1")
	}
	if !strings.Contains(out, "DB_PASS") {
		t.Error("expected output to contain DB_PASS")
	}
	// Secret values should be masked in table mode.
	if !strings.Contains(out, "••••••") {
		t.Error("expected output to contain secret mask ••••••")
	}
}

func TestEnvRm(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(204, "")

	cmd := newEnvCmd()
	cmd.SetArgs([]string{"rm", "demo", "AWS_REGION"})
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
	if req.Path != "/api/apps/demo/env/AWS_REGION" {
		t.Errorf("unexpected path: %s", req.Path)
	}
}

func TestEnvRm_RestartFlag(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(204, "")

	cmd := newEnvCmd()
	cmd.SetArgs([]string{"rm", "demo", "AWS_REGION", "--restart"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if !strings.Contains((*reqs)[0].Query, "restart=true") {
		t.Errorf("expected query to contain restart=true, got %q", (*reqs)[0].Query)
	}
}

func TestEnvCmd_ServerError(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(422, `{"error":"invalid key"}`)

	cmd := newEnvCmd()
	cmd.SetArgs([]string{"set", "demo", "FOO=bar"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for server 422, got nil")
	}
	if !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("expected error to contain server error text, got: %v", err)
	}
}
