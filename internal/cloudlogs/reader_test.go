package cloudlogs

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cloudwatchtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/rvben/shinyhub/internal/process"
)

type fakeClient struct {
	input *cloudwatchlogs.GetLogEventsInput
}

func (f *fakeClient) GetLogEvents(_ context.Context, input *cloudwatchlogs.GetLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
	f.input = input
	return &cloudwatchlogs.GetLogEventsOutput{
		Events:           []cloudwatchtypes.OutputLogEvent{{Message: aws.String("ready"), Timestamp: aws.Int64(1_700_000_000_000)}},
		NextForwardToken: aws.String("forward-token"),
	}, nil
}

func TestReaderReturnsLatestCloudWatchEvents(t *testing.T) {
	client := &fakeClient{}
	reader := New(client, "eu-west-1")
	details := process.ExternalLogs{
		Provider: "aws_ecs", Region: "eu-west-1",
		LogGroup: "/shinyhub/apps", LogStream: "app/app/task-1",
	}
	page, err := reader.Read(context.Background(), details, "", 200)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := aws.ToString(client.input.LogGroupName); got != details.LogGroup {
		t.Errorf("LogGroupName = %q", got)
	}
	if got := aws.ToString(client.input.LogStreamName); got != details.LogStream {
		t.Errorf("LogStreamName = %q", got)
	}
	if aws.ToBool(client.input.StartFromHead) || client.input.NextToken != nil || aws.ToInt32(client.input.Limit) != 200 {
		t.Errorf("initial input = %+v", client.input)
	}
	if len(page.Events) != 1 || page.Events[0].Message != "ready" ||
		!page.Events[0].Timestamp.Equal(time.UnixMilli(1_700_000_000_000)) || page.NextCursor != "forward-token" {
		t.Fatalf("page = %+v", page)
	}
}

func TestReaderResumesFromForwardCursor(t *testing.T) {
	client := &fakeClient{}
	reader := New(client, "eu-west-1")
	_, err := reader.Read(context.Background(), process.ExternalLogs{
		Provider: "aws_ecs", Region: "eu-west-1",
		LogGroup: "/shinyhub/apps", LogStream: "app/app/task-1",
	}, "prior-forward-token", 50)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := aws.ToString(client.input.NextToken); got != "prior-forward-token" {
		t.Errorf("NextToken = %q", got)
	}
	if !aws.ToBool(client.input.StartFromHead) {
		t.Error("StartFromHead must be true when resuming with nextForwardToken")
	}
}

func TestReaderRejectsUntrustedLogLocationsBeforeCallingAWS(t *testing.T) {
	tests := []struct {
		name    string
		details process.ExternalLogs
	}{
		{
			name: "cross-region",
			details: process.ExternalLogs{Provider: "aws_ecs", Region: "us-east-1",
				LogGroup: "/shinyhub/apps", LogStream: "app/app/task-1"},
		},
		{
			name: "invalid-group",
			details: process.ExternalLogs{Provider: "aws_ecs", Region: "eu-west-1",
				LogGroup: "group with spaces", LogStream: "app/app/task-1"},
		},
		{
			name: "wildcard-stream",
			details: process.ExternalLogs{Provider: "aws_ecs", Region: "eu-west-1",
				LogGroup: "/shinyhub/apps", LogStream: "app/*"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeClient{}
			if _, err := New(client, "eu-west-1").Read(context.Background(), tt.details, "", 200); err == nil {
				t.Fatal("Read succeeded for untrusted location")
			}
			if client.input != nil {
				t.Fatalf("AWS called with %+v", client.input)
			}
		})
	}
}
