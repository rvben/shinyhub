package fargate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3files"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

type sdkHTTPFunc func(*http.Request) (*http.Response, error)

func (call sdkHTTPFunc) Do(req *http.Request) (*http.Response, error) { return call(req) }

func sdkConfig(call sdkHTTPFunc) aws.Config {
	return aws.Config{
		Region: "eu-west-1", RetryMaxAttempts: 1, HTTPClient: call,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
		}),
	}
}

func sdkResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func sdkError[T any](_ *T, err error) error { return err }

func TestEC2AdapterPreservesSDKRequestAndOptions(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request-context")
	called := false
	client := NewEC2Client(ec2.NewFromConfig(sdkConfig(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.URL.Host != "adapter.invalid" || req.Context().Value(contextKey{}) != "request-context" {
			t.Errorf("SDK options/context lost: %s", req.URL)
		}
		if !strings.Contains(req.Header.Get("Authorization"), "/eu-west-1/ec2/aws4_request") {
			t.Errorf("request was not signed for EC2: %q", req.Header.Get("Authorization"))
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if req.Form.Get("Action") != "DescribeNetworkInterfaces" || req.Form.Get("NetworkInterfaceId.1") != "eni-test" {
			t.Errorf("wrong EC2 request: %v", req.Form)
		}
		return sdkResponse(200, `<DescribeNetworkInterfacesResponse><networkInterfaceSet><item><association><publicIp>203.0.113.1</publicIp></association></item></networkInterfaceSet></DescribeNetworkInterfacesResponse>`), nil
	})))
	out, err := client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		NetworkInterfaceIds: []string{"eni-test"},
	}, func(o *ec2.Options) { o.BaseEndpoint = aws.String("https://adapter.invalid") })
	if err != nil {
		t.Fatal(err)
	}
	if !called || len(out.NetworkInterfaces) != 1 || aws.ToString(out.NetworkInterfaces[0].Association.PublicIp) != "203.0.113.1" {
		t.Fatalf("unexpected network interfaces: %+v", out)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = client.DescribeNetworkInterfaces(cancelled, &ec2.DescribeNetworkInterfacesInput{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
}

func TestECSAdapterRetainsEveryRuntimeOperation(t *testing.T) {
	var operation string
	client := NewECSClient(ecs.NewFromConfig(sdkConfig(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "adapter.invalid" || req.Header.Get("X-Amz-Target") != "AmazonEC2ContainerServiceV20141113."+operation {
			t.Errorf("wrong SDK request for %s: %s %s", operation, req.URL, req.Header.Get("X-Amz-Target"))
		}
		return sdkResponse(200, `{}`), nil
	})))
	ctx := context.Background()
	opt := func(o *ecs.Options) { o.BaseEndpoint = aws.String("https://adapter.invalid") }
	tests := []struct {
		name string
		call func() error
	}{
		{"RunTask", func() error {
			return sdkError(client.RunTask(ctx, &ecs.RunTaskInput{TaskDefinition: aws.String("task")}, opt))
		}},
		{"StopTask", func() error { return sdkError(client.StopTask(ctx, &ecs.StopTaskInput{Task: aws.String("task")}, opt)) }},
		{"DescribeTasks", func() error {
			return sdkError(client.DescribeTasks(ctx, &ecs.DescribeTasksInput{Tasks: []string{"task"}}, opt))
		}},
		{"ListTasks", func() error { return sdkError(client.ListTasks(ctx, &ecs.ListTasksInput{}, opt)) }},
		{"DescribeTaskDefinition", func() error {
			return sdkError(client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: aws.String("task")}, opt))
		}},
		{"RegisterTaskDefinition", func() error {
			return sdkError(client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{Family: aws.String("test"), ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String("app"), Image: aws.String("app")}}}, opt))
		}},
		{"ListTaskDefinitions", func() error { return sdkError(client.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{}, opt)) }},
		{"DeregisterTaskDefinition", func() error {
			return sdkError(client.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{TaskDefinition: aws.String("task")}, opt))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			operation = tt.name
			if err := tt.call(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestS3FilesAdapterPreservesLinkedBucketResponse(t *testing.T) {
	client := NewS3FilesDescriber(s3files.NewFromConfig(sdkConfig(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "adapter.invalid" || req.Method != http.MethodGet || !strings.HasSuffix(req.URL.Path, "/fs-test") {
			t.Errorf("wrong S3 Files request: %s %s", req.Method, req.URL)
		}
		return sdkResponse(200, `{"bucket":"arn:aws:s3:::test-bucket","prefix":"apps/"}`), nil
	})))
	out, err := client.GetFileSystem(context.Background(), &s3files.GetFileSystemInput{FileSystemId: aws.String("fs-test")},
		func(o *s3files.Options) { o.BaseEndpoint = aws.String("https://adapter.invalid") })
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(out.Bucket) != "arn:aws:s3:::test-bucket" || aws.ToString(out.Prefix) != "apps/" {
		t.Fatalf("linked bucket lost: %+v", out)
	}
}

func TestObjectPutterAdapterPreservesSDKSigningAndRetries(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "marker-context")
	attempts := 0
	cfg := sdkConfig(func(req *http.Request) (*http.Response, error) {
		attempts++
		if req.Context().Value(contextKey{}) != "marker-context" || req.Method != http.MethodPut ||
			req.URL.Host != "adapter.invalid" || req.URL.Path != "/test-bucket/apps/app-42/.shinyhub-keep" {
			t.Errorf("wrong marker request: %s %s", req.Method, req.URL)
		}
		if !strings.Contains(req.Header.Get("Authorization"), "/eu-west-1/s3/aws4_request") {
			t.Errorf("marker request was not signed for S3: %q", req.Header.Get("Authorization"))
		}
		if req.Body != nil {
			body, err := io.ReadAll(req.Body)
			if err != nil || len(body) != 0 {
				t.Errorf("marker body: %q, %v", body, err)
			}
		}
		if attempts == 1 {
			return sdkResponse(http.StatusServiceUnavailable, `<Error><Code>SlowDown</Code><Message>retry</Message></Error>`), nil
		}
		return sdkResponse(http.StatusOK, ""), nil
	})
	cfg.RetryMaxAttempts = 2
	cfg.Retryer = func() aws.Retryer {
		return retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = 2
			o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
		})
	}
	client := NewObjectPutter(s3.NewFromConfig(cfg))
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"), Key: aws.String("apps/app-42/.shinyhub-keep"),
		Body: strings.NewReader(""),
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("https://adapter.invalid")
		o.UsePathStyle = true
	})
	if err != nil || attempts != 2 {
		t.Fatalf("SDK retry lost: attempts=%d err=%v", attempts, err)
	}
}

func TestSecretsManagerSDKAdapterPreservesTypedErrors(t *testing.T) {
	var operations []string
	client := secretsmanager.NewFromConfig(sdkConfig(func(req *http.Request) (*http.Response, error) {
		target := req.Header.Get("X-Amz-Target")
		operations = append(operations, target)
		if target == "secretsmanager.CreateSecret" {
			return sdkResponse(400, `{"__type":"ResourceExistsException","Message":"already exists"}`), nil
		}
		if target != "secretsmanager.PutSecretValue" {
			t.Errorf("unexpected operation: %s", target)
		}
		return sdkResponse(200, `{"ARN":"test-secret-arn"}`), nil
	}))
	arn, err := NewSecretsManagerStore(client, "").Put(context.Background(), "test/app-1/key", "test-value")
	if err != nil || arn != "test-secret-arn" || len(operations) != 2 {
		t.Fatalf("upsert lost SDK error handling: arn=%q err=%v operations=%v", arn, err, operations)
	}
}
