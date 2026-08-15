//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscloudwatch "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/cloudlogs"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/metrics"
	"github.com/rvben/shinyhub/internal/process"
)

// This opt-in canary proves that real CloudWatch reads travel through the same
// authenticated API coordinator used by the Logs tab. It needs only a retained
// stream, so it creates no AWS resources and makes exactly two provider reads.
// AWS credentials use the standard SDK chain (including AWS_PROFILE).

type countedProviderLogReader struct {
	inner process.ExternalLogReader
	calls atomic.Int32
}

func (r *countedProviderLogReader) Read(ctx context.Context, details process.ExternalLogs, cursor string, limit int32) (process.ExternalLogPage, error) {
	r.calls.Add(1)
	return r.inner.Read(ctx, details, cursor, limit)
}

func TestIntegrationProviderLogsMultiViewer(t *testing.T) {
	group := os.Getenv("SHINYHUB_PROVIDER_LOG_IT_GROUP")
	stream := os.Getenv("SHINYHUB_PROVIDER_LOG_IT_STREAM")
	if group == "" || stream == "" {
		t.Skip("SHINYHUB_PROVIDER_LOG_IT_GROUP and SHINYHUB_PROVIDER_LOG_IT_STREAM are required")
	}
	region := os.Getenv("SHINYHUB_PROVIDER_LOG_IT_REGION")
	if region == "" {
		region = "eu-west-1"
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(t.Context(), awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("load AWS config: %v", err)
	}
	reader := &countedProviderLogReader{inner: cloudlogs.New(awscloudwatch.NewFromConfig(awsCfg), region)}

	srv, store, _ := newLogsTestServer(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "provider-canary", Name: "Provider canary", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("provider-canary")
	const runID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
		RunID: runID, AppID: app.ID, ReplicaIndex: 0, Tier: "fargate",
		Status: "starting", StartedAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	details, _ := json.Marshal(process.ExternalLogs{
		Provider: "aws_ecs", Region: region,
		Resource: "arn:aws:ecs:" + region + ":000000000000:task/provider-log-canary/retained-stream",
		LogGroup: group, LogStream: stream,
	})
	if err := store.MarkAppLogRunRunning(runID, "fargate", string(details)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAppLogRun(runID, "stopped", time.Now(), false); err != nil {
		t.Fatal(err)
	}
	srv.SetExternalLogReader(reader)
	reg := metrics.New("provider-canary")
	srv.SetMetrics(reg)
	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	providerURL := "/api/apps/provider-canary/logs?replica=0&run=" + runID + "&provider=true"

	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, providerURL, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}

	const viewers = 8
	responses := make(chan *httptest.ResponseRecorder, viewers)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(viewers)
	for range viewers {
		go func() {
			ready.Done()
			<-start
			responses <- request()
		}()
	}
	ready.Wait()
	close(start)
	var responseFailures []string
	for range viewers {
		rec := <-responses
		if rec.Code != http.StatusOK {
			responseFailures = append(responseFailures, fmt.Sprintf("status=%d body=%s", rec.Code, rec.Body.String()))
			continue
		}
		if expected := os.Getenv("SHINYHUB_PROVIDER_LOG_IT_EXPECT"); expected != "" && !strings.Contains(rec.Body.String(), expected) {
			responseFailures = append(responseFailures, fmt.Sprintf("response did not contain %q: %s", expected, rec.Body.String()))
		}
	}
	if len(responseFailures) > 0 {
		t.Fatalf("provider responses failed:\n%s", strings.Join(responseFailures, "\n"))
	}
	if got := reader.calls.Load(); got != 1 {
		t.Fatalf("eight adjacent viewers made %d CloudWatch reads, want 1", got)
	}

	time.Sleep(1100 * time.Millisecond)
	if rec := request(); rec.Code != http.StatusOK {
		t.Fatalf("post-share-window response status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := reader.calls.Load(); got != 2 {
		t.Fatalf("post-share-window CloudWatch reads=%d, want 2", got)
	}

	metricsRec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(metricsRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	metricsBody := metricsRec.Body.String()
	for _, want := range []string{
		`shinyhub_provider_log_reads_total{result="ok"} 2`,
		`shinyhub_provider_log_reads_total{result="shared"} 7`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("provider canary metrics missing %q:\n%s", want, metricsBody)
		}
	}
}
