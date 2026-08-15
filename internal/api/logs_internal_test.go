package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/logstream"
	"github.com/rvben/shinyhub/internal/process"
)

func TestWriteLogEventSignalsRetentionGapBeforeAdvancingCursor(t *testing.T) {
	rec := httptest.NewRecorder()
	writeLogEvent(rec, logstream.Record{Line: "earliest available", EndOffset: 42, GapBefore: true})
	body := rec.Body.String()
	want := "event: retention-gap\ndata: Output before this point is no longer retained\n\nid: 42\ndata: earliest available\n\n"
	if body != want {
		t.Fatalf("gap event body = %q, want %q", body, want)
	}
	gapEnd := strings.Index(body, "\n\n")
	if strings.Contains(body[:gapEnd], "id:") {
		t.Fatal("gap advisory advanced Last-Event-ID before the retained record was delivered")
	}
}

func TestWriteLogEventSignalsStreamHealthWithoutAdvancingCursor(t *testing.T) {
	for _, tt := range []struct {
		state logstream.StreamState
		want  string
	}{
		{logstream.StreamDegraded, "event: stream-degraded\ndata: Live output is temporarily delayed while retained log storage recovers\n\n"},
		{logstream.StreamRecovered, "event: stream-recovered\ndata: Live output delivery recovered\n\n"},
	} {
		rec := httptest.NewRecorder()
		writeLogEvent(rec, logstream.Record{StreamState: tt.state})
		if body := rec.Body.String(); body != tt.want {
			t.Errorf("%s event body = %q, want %q", tt.state, body, tt.want)
		} else if strings.Contains(body, "id:") {
			t.Errorf("%s event advanced Last-Event-ID", tt.state)
		}
	}
}

func TestDecodeExternalLogsAllowsOnlyExpectedAWSConsoleOrigins(t *testing.T) {
	encode := func(consoleURL string) string {
		raw, err := json.Marshal(process.ExternalLogs{
			Provider:   "aws_ecs",
			Resource:   "arn:aws:ecs:eu-west-1:123:task/demo/task-1",
			ConsoleURL: consoleURL,
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	const allowed = "https://console.aws.amazon.com/ecs/v2/clusters/demo/tasks/task-1/logs?region=eu-west-1"
	if got := decodeExternalLogs(encode(allowed)); got == nil || got.ConsoleURL != allowed {
		t.Fatalf("allowed console URL = %+v", got)
	}
	for _, unsafe := range []string{
		"http://console.aws.amazon.com/ecs/v2",
		"https://console.aws.amazon.com.evil.test/steal",
		"https://console.aws.amazon.com:444/ecs/v2",
		"https://user@console.aws.amazon.com/ecs/v2",
	} {
		got := decodeExternalLogs(encode(unsafe))
		if got == nil || got.ConsoleURL != "" {
			t.Errorf("unsafe console URL %q = %+v", unsafe, got)
		}
	}
	if got := decodeExternalLogs(`{"provider":"aws_ecs"}`); got != nil {
		t.Errorf("incomplete metadata = %+v", got)
	}
}

func TestDecodeExternalLogsAllowsOnlyRegionScopedCloudWatchOrigins(t *testing.T) {
	encode := func(logURL string) string {
		raw, err := json.Marshal(process.ExternalLogs{
			Provider: "aws_ecs", Resource: "arn:aws:ecs:eu-west-1:123:task/demo/task-1",
			Region: "eu-west-1", LogGroup: "/shinyhub/apps", LogStream: "app/app/task-1", LogURL: logURL,
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	const allowed = "https://eu-west-1.console.aws.amazon.com/cloudwatch/home?region=eu-west-1#logsV2:log-groups"
	if got := decodeExternalLogs(encode(allowed)); got == nil || got.LogURL != allowed {
		t.Fatalf("allowed CloudWatch URL = %+v", got)
	}
	for _, unsafe := range []string{
		"https://us-east-1.console.aws.amazon.com/cloudwatch/home?region=eu-west-1",
		"https://eu-west-1.console.aws.amazon.com.evil.test/steal",
		"http://eu-west-1.console.aws.amazon.com/cloudwatch/home",
	} {
		got := decodeExternalLogs(encode(unsafe))
		if got == nil || got.LogURL != "" {
			t.Errorf("unsafe CloudWatch URL %q = %+v", unsafe, got)
		}
	}
}
