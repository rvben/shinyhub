package cloudlogs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/rvben/shinyhub/internal/process"
)

type sdkHTTPFunc func(*http.Request) (*http.Response, error)

func (call sdkHTTPFunc) Do(req *http.Request) (*http.Response, error) { return call(req) }

func TestSDKReaderPreservesResponsesAndThrottling(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"events", 200, `{"events":[{"message":"ready","timestamp":1700000000000}],"nextForwardToken":"next"}`},
		{"throttling", 400, `{"__type":"ThrottlingException","message":"slow down"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			client := cloudwatchlogs.NewFromConfig(aws.Config{
				Region: "eu-west-1", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: sdkHTTPFunc(func(req *http.Request) (*http.Response, error) {
					called = true
					if req.Header.Get("X-Amz-Target") != "Logs_20140328.GetLogEvents" {
						t.Errorf("wrong operation: %s", req.Header.Get("X-Amz-Target"))
					}
					var in struct {
						LogGroupName  string
						NextToken     string
						Limit         int
						StartFromHead bool
					}
					if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
						t.Fatal(err)
					}
					if in.LogGroupName != "/apps" || in.NextToken != "previous" || in.Limit != 25 || !in.StartFromHead {
						t.Errorf("reader request changed: %+v", in)
					}
					return &http.Response{
						StatusCode: tt.status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.1"}},
						Body: io.NopCloser(strings.NewReader(tt.body)),
					}, nil
				}),
			})
			page, err := NewSDKReader(client, "eu-west-1").Read(context.Background(), process.ExternalLogs{
				Provider: "aws_ecs", Region: "eu-west-1", LogGroup: "/apps", LogStream: "app/task",
			}, "previous", 25)
			if !called {
				t.Fatal("SDK operation was not invoked")
			}
			if tt.status == 400 {
				if !errors.Is(err, process.ErrExternalLogsThrottled) {
					t.Fatalf("throttling classification lost: %v", err)
				}
			} else if err != nil || len(page.Events) != 1 || page.Events[0].Message != "ready" || page.NextCursor != "next" {
				t.Fatalf("log response changed: page=%+v err=%v", page, err)
			}
		})
	}
}
