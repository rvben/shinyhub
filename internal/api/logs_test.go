package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
)

func newLogsTestServer(t *testing.T) (*api.Server, *db.Store, string) {
	t.Helper()
	appsDir := t.TempDir()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	srv := api.New(cfg, store, mgr, nil)
	return srv, store, appsDir
}

// writeCurrentLogsTestOutput seeds the storage path the handler uses for the
// active backend: legacy local files on SQLite and an immutable shared run on
// Postgres. Keeping these tests backend-neutral prevents a clustered test run
// from accidentally exercising an empty database reader with a local fixture.
func writeCurrentLogsTestOutput(t *testing.T, store *db.Store, appsDir string, content []byte) {
	t.Helper()
	if !store.IsPostgres() {
		logPath := filepath.Join(appsDir, "myapp", "app-0.log")
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(logPath, content, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	app, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	const runID = "99999999-9999-4999-8999-999999999999"
	if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
		RunID: runID, AppID: app.ID, ReplicaIndex: 0, Tier: "local",
		Status: "starting", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAppLogRunRunning(runID, "native", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAppLogChunk(runID, 0, 0, content, db.AppLogRetentionBytes, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestHandleLogs_NoLogFile(t *testing.T) {
	srv, store, _ := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 when no log file exists, got %d", rec.Code)
	}
}

func TestHandleLogSources_MergesLiveRowsAndRetainedScaledDownLogs(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("myapp")
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusRunning,
		Provider: "native", Tier: "local", AppVersion: "v-current",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 1, Status: db.ReplicaStatusRunning,
		Provider: "fargate", Tier: "burst", AppVersion: "v-current",
	}); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(appsDir, "myapp")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app-0.log"), []byte("live\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app-3.log"), []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs/sources", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Sources []struct {
			Replica         int    `json:"replica"`
			Status          string `json:"status"`
			Provider        string `json:"provider"`
			HasLog          bool   `json:"has_log"`
			SizeBytes       int64  `json:"size_bytes"`
			StreamAvailable bool   `json:"stream_available"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sources) != 3 {
		t.Fatalf("sources = %+v", body.Sources)
	}
	if got := body.Sources[0]; got.Replica != 0 || got.Status != "running" || got.Provider != "native" || !got.HasLog {
		t.Errorf("live source = %+v", got)
	}
	if got := body.Sources[1]; got.Replica != 1 || got.Provider != "fargate" || got.HasLog || got.StreamAvailable {
		t.Errorf("external source = %+v", got)
	}
	if got := body.Sources[2]; got.Replica != 3 || got.Status != "stopped" || !got.HasLog || got.SizeBytes == 0 || !got.StreamAvailable {
		t.Errorf("retained source = %+v", got)
	}
}

func TestHandleLogSourcesAndLogsExposeImmutableRunHistory(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("myapp")
	olderID := "11111111-1111-4111-8111-111111111111"
	newerID := "22222222-2222-4222-8222-222222222222"
	base := time.Unix(1_700_000_000, 0)
	for _, p := range []db.CreateAppLogRunParams{
		{RunID: olderID, AppID: app.ID, ReplicaIndex: 0, AppVersion: "v1", Tier: "local", Status: "starting", StartedAt: base},
		{RunID: newerID, AppID: app.ID, ReplicaIndex: 0, AppVersion: "v2", Tier: "local", Status: "starting", StartedAt: base.Add(time.Minute)},
	} {
		if err := store.CreateAppLogRun(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FinishAppLogRun(olderID, "stopped", base.Add(30*time.Second), false); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAppLogRunRunning(newerID, "native", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: "running", Provider: "native", Tier: "local", AppVersion: "v2"}); err != nil {
		t.Fatal(err)
	}
	if store.IsPostgres() {
		if err := store.AppendAppLogChunk(olderID, 0, 0, []byte("old run only\n"), db.AppLogRetentionBytes, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendAppLogChunk(newerID, 0, 0, []byte("new run only\n"), db.AppLogRetentionBytes, time.Now()); err != nil {
			t.Fatal(err)
		}
	} else {
		logDir := filepath.Join(appsDir, "myapp", "logs")
		if err := os.MkdirAll(logDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(logDir, "replica-0-"+olderID+".log"), []byte("old run only\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(logDir, "replica-0-"+newerID+".log"), []byte("new run only\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	rec := request("/api/apps/myapp/logs/sources")
	if rec.Code != http.StatusOK {
		t.Fatalf("sources status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Sources []struct {
			SourceID string `json:"source_id"`
			RunID    string `json:"run_id"`
			Current  bool   `json:"current"`
			Replica  int    `json:"replica"`
			Status   string `json:"status"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sources) != 2 {
		t.Fatalf("sources=%+v", body.Sources)
	}
	if body.Sources[0].RunID != newerID || !body.Sources[0].Current || body.Sources[0].Status != "running" {
		t.Errorf("current=%+v", body.Sources[0])
	}
	if body.Sources[1].RunID != olderID || body.Sources[1].Current || body.Sources[1].Status != "stopped" {
		t.Errorf("history=%+v", body.Sources[1])
	}
	rec = request("/api/apps/myapp/logs?replica=0&run=" + olderID + "&follow=false")
	if rec.Code != http.StatusOK || rec.Body.String() != "old run only\n" {
		t.Fatalf("old run status=%d body=%q", rec.Code, rec.Body.String())
	}
	rec = request("/api/apps/myapp/logs?replica=1&run=" + olderID + "&follow=false")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("mismatched replica status=%d", rec.Code)
	}
}

func TestHandleLogSourcesPreservesExternalLogsForStoppedRun(t *testing.T) {
	srv, store, _ := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("myapp")
	const runID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
		RunID: runID, AppID: app.ID, ReplicaIndex: 4, Tier: "burst",
		Status: "starting", StartedAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	details := process.ExternalLogs{
		Provider: "aws_ecs", Resource: "arn:aws:ecs:eu-west-1:111122223333:task/analytics/task-4",
		Region: "eu-west-1", Cluster: "analytics",
		ConsoleURL: "https://console.aws.amazon.com/ecs/v2/clusters/analytics/tasks/task-4/logs?region=eu-west-1",
	}
	encoded, _ := json.Marshal(details)
	if err := store.MarkAppLogRunRunning(runID, "fargate", string(encoded)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAppLogRun(runID, "stopped", time.Now(), false); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs/sources", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Sources []struct {
			Status          string                `json:"status"`
			StreamAvailable bool                  `json:"stream_available"`
			ExternalLogs    *process.ExternalLogs `json:"external_logs"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sources) != 1 || body.Sources[0].Status != "stopped" || body.Sources[0].StreamAvailable ||
		body.Sources[0].ExternalLogs == nil || *body.Sources[0].ExternalLogs != details {
		t.Fatalf("external stopped source = %+v", body.Sources)
	}
}

type fakeExternalLogReader struct {
	gotDetails process.ExternalLogs
	gotCursor  string
	gotLimit   int32
	page       process.ExternalLogPage
	err        error
}

func (f *fakeExternalLogReader) Read(_ context.Context, details process.ExternalLogs, cursor string, limit int32) (process.ExternalLogPage, error) {
	f.gotDetails, f.gotCursor, f.gotLimit = details, cursor, limit
	return f.page, f.err
}

func TestHandleLogsReadsStoppedFargateRunFromExternalProvider(t *testing.T) {
	srv, store, _ := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("myapp")
	const runID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
		RunID: runID, AppID: app.ID, ReplicaIndex: 3, Tier: "burst",
		Status: "starting", StartedAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	details := process.ExternalLogs{
		Provider: "aws_ecs", Resource: "arn:aws:ecs:eu-west-1:111122223333:task/analytics/task-3",
		Region: "eu-west-1", Cluster: "analytics", LogGroup: "/shinyhub/apps", LogStream: "app/app/task-3",
	}
	encoded, _ := json.Marshal(details)
	if err := store.MarkAppLogRunRunning(runID, "fargate", string(encoded)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAppLogRun(runID, "stopped", time.Now(), false); err != nil {
		t.Fatal(err)
	}

	reader := &fakeExternalLogReader{page: process.ExternalLogPage{
		Events:     []process.ExternalLogEvent{{Message: "booted", Timestamp: time.UnixMilli(1_700_000_000_000)}},
		NextCursor: "next-token",
	}}
	srv.SetExternalLogReader(reader)
	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs?replica=3&run="+runID+"&provider=true&tail=2&cursor=prior-token", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if reader.gotDetails != details || reader.gotCursor != "prior-token" || reader.gotLimit != 2 {
		t.Fatalf("provider read = details %+v cursor %q limit %d", reader.gotDetails, reader.gotCursor, reader.gotLimit)
	}
	var page process.ExternalLogPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Message != "booted" || page.NextCursor != "next-token" {
		t.Fatalf("page = %+v", page)
	}

	req = httptest.NewRequest("GET", "/api/apps/myapp/logs/sources", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sources status=%d body=%s", rec.Code, rec.Body.String())
	}
	var sources struct {
		Sources []struct {
			RunID           string `json:"run_id"`
			InlineAvailable bool   `json:"inline_available"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sources); err != nil {
		t.Fatal(err)
	}
	if len(sources.Sources) != 1 || sources.Sources[0].RunID != runID || !sources.Sources[0].InlineAvailable {
		t.Fatalf("sources = %+v", sources.Sources)
	}

	reader.err = errors.New("AccessDeniedException: secret provider detail")
	req = httptest.NewRequest("GET", "/api/apps/myapp/logs?replica=3&run="+runID+"&provider=true", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "temporarily unavailable") ||
		strings.Contains(rec.Body.String(), "AccessDeniedException") {
		t.Fatalf("provider failure status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogsReadsSharedRunWithoutLocalFile(t *testing.T) {
	srv, store, _ := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("myapp")
	runID := "77777777-7777-4777-8777-777777777777"
	started := time.Now().Add(-time.Minute)
	if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
		RunID: runID, AppID: app.ID, ReplicaIndex: 2, Tier: "remote",
		Status: "running", StartedAt: started,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAppLogRunRunning(runID, "remote_docker", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAppLogChunk(runID, 0, 0, []byte("from another node\n"), db.AppLogRetentionBytes, time.Now()); err != nil {
		t.Fatal(err)
	}
	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	rec := request("/api/apps/myapp/logs?replica=2&run=" + runID + "&follow=false")
	if rec.Code != http.StatusOK || rec.Body.String() != "from another node\n" {
		t.Fatalf("shared log status=%d body=%q", rec.Code, rec.Body.String())
	}
	rec = request("/api/apps/myapp/logs/sources")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"has_log":true`) || !strings.Contains(rec.Body.String(), `"size_bytes":18`) {
		t.Fatalf("shared source status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleLogs_TailLimitsInitialBurst verifies that ?tail=N caps the number
// of initial lines emitted. With a 5-line file and ?tail=2, only the last two
// lines should appear.
func TestHandleLogs_TailLimitsInitialBurst(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	writeCurrentLogsTestOutput(t, store, appsDir, []byte("a\nb\nc\nd\ne\n"))

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs?tail=2&follow=false", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, absent := range []string{"a\n", "b\n", "c\n"} {
		if strings.Contains(body, absent) {
			t.Errorf("body should not contain %q (tail=2 caps to last 2 lines), got:\n%s", absent, body)
		}
	}
	for _, want := range []string{"d\n", "e\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q, got:\n%s", want, body)
		}
	}
}

// TestHandleLogs_NoFollowReturnsPlainText verifies that ?follow=false emits
// plain text/plain output (one line per row, no "data:" SSE prefix) and
// closes the connection immediately. This is the kubectl-style one-shot
// fetch shape that scripts can pipe to tail/grep without parsing SSE frames.
func TestHandleLogs_NoFollowReturnsPlainText(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	writeCurrentLogsTestOutput(t, store, appsDir, []byte("hello\nworld\n"))

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs?follow=false", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain; follow=false should not emit SSE", ct)
	}
	body := rec.Body.String()
	if strings.Contains(body, "data:") {
		t.Errorf("body should not contain SSE 'data:' prefix when follow=false, got:\n%s", body)
	}
	if body != "hello\nworld\n" {
		t.Errorf("body = %q, want %q", body, "hello\nworld\n")
	}
}

func TestHandleLogs_DownloadReturnsEveryRetainedByte(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// More lines than the interactive 10,000-line ceiling, plus a partial final
	// line, proves download is both complete and byte-exact.
	content := []byte("first\n" + strings.Repeat("middle\n", 10_001) + "last without newline")
	writeCurrentLogsTestOutput(t, store, appsDir, content)

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs?download=true", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="myapp-replica-0-current.log"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, content) {
		t.Errorf("downloaded %d bytes, want exact %d-byte retained log", len(got), len(content))
	}
}

// TestHandleLogs_TailZeroRejected ensures the handler rejects nonsensical
// tail values rather than silently emitting nothing or the default 200.
func TestHandleLogs_TailZeroRejected(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	logPath := filepath.Join(appsDir, "myapp", "app-0.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	for _, raw := range []string{"0", "-1", "abc", "1000000"} {
		req := httptest.NewRequest("GET", "/api/apps/myapp/logs?tail="+raw, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("tail=%s: status = %d, want 400", raw, rec.Code)
		}
	}
}

func TestHandleLogs_SSEInitialBurst(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// Pre-populate output for replica 0 (the default).
	writeCurrentLogsTestOutput(t, store, appsDir, []byte("alpha\nbeta\ngamma\n"))

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")

	// Use a context with timeout so the SSE handler returns after the initial burst.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, want := range []string{"id: 6\n", "id: 11\n", "id: 17\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected SSE cursor %q in body, got:\n%s", want, body)
		}
	}
	for _, want := range []string{"data: alpha\n", "data: beta\n", "data: gamma\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected SSE line %q in body, got:\n%s", want, body)
		}
	}
}

func TestHandleLogs_SSEResumesAfterLastEventWithoutReplayingTail(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})
	writeCurrentLogsTestOutput(t, store, appsDir, []byte("alpha\nbeta\ngamma\n"))

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Last-Event-ID", "6") // immediately after "alpha\n"
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "data: alpha\n") {
		t.Fatalf("resumed stream replayed acknowledged line:\n%s", body)
	}
	for _, want := range []string{"id: 11\ndata: beta\n", "id: 17\ndata: gamma\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("resumed stream missing %q:\n%s", want, body)
		}
	}
}

func TestHandleLogs_SSEInvalidLastEventIDFallsBackToInitialTail(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})
	writeCurrentLogsTestOutput(t, store, appsDir, []byte("safe fallback\n"))

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Last-Event-ID", "not-a-cursor")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if body := rec.Body.String(); !strings.Contains(body, "id: 14\ndata: safe fallback\n") {
		t.Fatalf("invalid cursor did not fall back to initial tail:\n%s", body)
	}
}
