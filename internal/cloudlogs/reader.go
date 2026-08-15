// Package cloudlogs adapts AWS CloudWatch Logs into ShinyHub's provider-log
// page contract. It performs bounded, single-stream reads only; authentication
// and authorization remain the API server's responsibility.
package cloudlogs

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"github.com/rvben/shinyhub/internal/process"
)

var logGroupPattern = regexp.MustCompile(`^[.\-_/#A-Za-z0-9]{1,512}$`)

type client interface {
	GetLogEvents(context.Context, *cloudwatchlogs.GetLogEventsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error)
}

// Reader retrieves one bounded CloudWatch log-stream page.
type Reader struct {
	client client
	region string
}

func New(c client, region string) *Reader {
	return &Reader{client: c, region: region}
}

func (r *Reader) Read(ctx context.Context, details process.ExternalLogs, cursor string, limit int32) (process.ExternalLogPage, error) {
	if r == nil || r.client == nil {
		return process.ExternalLogPage{}, fmt.Errorf("cloudwatch logs reader is not configured")
	}
	if details.Provider != "aws_ecs" || details.Region == "" || details.Region != r.region {
		return process.ExternalLogPage{}, fmt.Errorf("cloudwatch logs region does not match the configured AWS client")
	}
	if !logGroupPattern.MatchString(details.LogGroup) {
		return process.ExternalLogPage{}, fmt.Errorf("invalid CloudWatch log group")
	}
	if details.LogStream == "" || len(details.LogStream) > 512 || strings.ContainsAny(details.LogStream, ":*") {
		return process.ExternalLogPage{}, fmt.Errorf("invalid CloudWatch log stream")
	}
	if limit < 1 || limit > 10000 {
		return process.ExternalLogPage{}, fmt.Errorf("CloudWatch log page limit must be between 1 and 10000")
	}

	in := &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(details.LogGroup),
		LogStreamName: aws.String(details.LogStream),
		Limit:         aws.Int32(limit),
		StartFromHead: aws.Bool(cursor != ""),
	}
	if cursor != "" {
		in.NextToken = aws.String(cursor)
	}
	out, err := r.client.GetLogEvents(ctx, in)
	if err != nil {
		return process.ExternalLogPage{}, fmt.Errorf("get CloudWatch log events: %w", err)
	}
	page := process.ExternalLogPage{
		Events:     make([]process.ExternalLogEvent, 0, len(out.Events)),
		NextCursor: aws.ToString(out.NextForwardToken),
	}
	for _, event := range out.Events {
		page.Events = append(page.Events, process.ExternalLogEvent{
			Message: aws.ToString(event.Message), Timestamp: time.UnixMilli(aws.ToInt64(event.Timestamp)),
		})
	}
	return page, nil
}

var _ process.ExternalLogReader = (*Reader)(nil)
