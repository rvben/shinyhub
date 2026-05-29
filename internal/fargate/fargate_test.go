package fargate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rvben/shinyhub/internal/process"
)

// fakeECS is a scriptable ECSClient. Each method delegates to its func field
// (defaulting to a benign empty response) and records inputs for assertions.
type fakeECS struct {
	runTaskFn       func(*ecs.RunTaskInput) (*ecs.RunTaskOutput, error)
	stopTaskFn      func(*ecs.StopTaskInput) (*ecs.StopTaskOutput, error)
	describeTasksFn func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error)
	listTasksFn     func(*ecs.ListTasksInput) (*ecs.ListTasksOutput, error)

	runInputs      []*ecs.RunTaskInput
	stopInputs     []*ecs.StopTaskInput
	describeInputs []*ecs.DescribeTasksInput
	listInputs     []*ecs.ListTasksInput
}

func (f *fakeECS) RunTask(_ context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	f.runInputs = append(f.runInputs, in)
	if f.runTaskFn != nil {
		return f.runTaskFn(in)
	}
	return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String("task-arn")}}}, nil
}

func (f *fakeECS) StopTask(_ context.Context, in *ecs.StopTaskInput, _ ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	f.stopInputs = append(f.stopInputs, in)
	if f.stopTaskFn != nil {
		return f.stopTaskFn(in)
	}
	return &ecs.StopTaskOutput{}, nil
}

func (f *fakeECS) DescribeTasks(_ context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	f.describeInputs = append(f.describeInputs, in)
	if f.describeTasksFn != nil {
		return f.describeTasksFn(in)
	}
	return &ecs.DescribeTasksOutput{}, nil
}

func (f *fakeECS) ListTasks(_ context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	f.listInputs = append(f.listInputs, in)
	if f.listTasksFn != nil {
		return f.listTasksFn(in)
	}
	return &ecs.ListTasksOutput{}, nil
}

// fakeEC2 is a scriptable EC2Client for the public-IP routing path.
type fakeEC2 struct {
	describeFn func(*ec2.DescribeNetworkInterfacesInput) (*ec2.DescribeNetworkInterfacesOutput, error)
	calls      int
}

func (f *fakeEC2) DescribeNetworkInterfaces(_ context.Context, in *ec2.DescribeNetworkInterfacesInput, _ ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
	f.calls++
	if f.describeFn != nil {
		return f.describeFn(in)
	}
	return &ec2.DescribeNetworkInterfacesOutput{}, nil
}

// taskWithENI builds a RUNNING task whose awsvpc ENI attachment carries the given
// network-interface id (and private IP), mirroring real DescribeTasks output.
func taskWithENI(arn, eniID, privateIP, status string) ecstypes.Task {
	return ecstypes.Task{
		TaskArn:    aws.String(arn),
		LastStatus: aws.String(status),
		Attachments: []ecstypes.Attachment{{
			Type: aws.String("ElasticNetworkInterface"),
			Details: []ecstypes.KeyValuePair{
				{Name: aws.String("networkInterfaceId"), Value: aws.String(eniID)},
				{Name: aws.String("privateIPv4Address"), Value: aws.String(privateIP)},
			},
		}},
	}
}

func testCfg() Config {
	return Config{
		Cluster:        "shiny-cluster",
		TaskDefinition: "shiny-app:7",
		ContainerName:  "app",
		Subnets:        []string{"subnet-a", "subnet-b"},
		SecurityGroups: []string{"sg-1"},
	}
}

func fastRuntime(client ECSClient) *Runtime {
	return New(client, testCfg(), nil,
		WithPollInterval(time.Millisecond),
		WithStartTimeout(50*time.Millisecond))
}

// taskWithIP builds a RUNNING task description carrying a private IP via the
// container network interface.
func taskWithIP(arn, ip, status string) ecstypes.Task {
	return ecstypes.Task{
		TaskArn:    aws.String(arn),
		LastStatus: aws.String(status),
		Containers: []ecstypes.Container{{
			NetworkInterfaces: []ecstypes.NetworkInterface{{PrivateIpv4Address: aws.String(ip)}},
		}},
	}
}

func startParams() process.StartParams {
	return process.StartParams{
		Slug:            "demo",
		Index:           2,
		Tier:            "burst",
		Command:         []string{"shiny", "run", "--port", "8000"},
		Port:            8000,
		Env:             []string{"FOO=bar", "PORT=8000"},
		MemoryLimitMB:   512,
		CPUQuotaPercent: 50,
		DeploymentID:    99,
		AppVersion:      "v3",
		ContentDigest:   "sha256:abc",
	}
}

func TestStartRunsTaskAndRoutesToPrivateIP(t *testing.T) {
	calls := 0
	f := &fakeECS{
		runTaskFn: func(*ecs.RunTaskInput) (*ecs.RunTaskOutput, error) {
			return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{
				TaskArn:    aws.String("arn:aws:ecs:eu-west-1:111122223333:task/shiny-cluster/abc123"),
				LastStatus: aws.String("PROVISIONING"),
			}}}, nil
		},
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			calls++
			if calls < 2 {
				// First poll: still pending, no IP yet.
				return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
					TaskArn:    aws.String("arn:aws:ecs:eu-west-1:111122223333:task/shiny-cluster/abc123"),
					LastStatus: aws.String("PENDING"),
				}}}, nil
			}
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{
				taskWithIP("arn:aws:ecs:eu-west-1:111122223333:task/shiny-cluster/abc123", "10.1.2.3", "RUNNING"),
			}}, nil
		},
	}
	r := fastRuntime(f)

	ep, err := r.Start(context.Background(), startParams(), io.Discard)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ep.URL != "http://10.1.2.3:8000" {
		t.Errorf("URL = %q, want http://10.1.2.3:8000", ep.URL)
	}
	if ep.Provider != Provider {
		t.Errorf("Provider = %q, want %q", ep.Provider, Provider)
	}
	if ep.WorkerID != WorkerID {
		t.Errorf("WorkerID = %q, want %q", ep.WorkerID, WorkerID)
	}
	wantHandle := "fargate/arn:aws:ecs:eu-west-1:111122223333:task/shiny-cluster/abc123"
	if ep.Handle.ContainerID != wantHandle {
		t.Errorf("Handle = %q, want %q", ep.Handle.ContainerID, wantHandle)
	}

	// The handle round-trips back to the bare task ARN for later signal/wait.
	gotARN, err := decodeHandle(ep.Handle.ContainerID)
	if err != nil {
		t.Fatalf("decodeHandle: %v", err)
	}
	if gotARN != "arn:aws:ecs:eu-west-1:111122223333:task/shiny-cluster/abc123" {
		t.Errorf("decoded ARN = %q", gotARN)
	}
}

func TestStartBuildsCorrectRunTaskInput(t *testing.T) {
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{taskWithIP("task-arn", "10.0.0.9", "RUNNING")}}, nil
		},
	}
	r := fastRuntime(f)
	if _, err := r.Start(context.Background(), startParams(), io.Discard); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(f.runInputs) != 1 {
		t.Fatalf("RunTask called %d times, want 1", len(f.runInputs))
	}
	in := f.runInputs[0]
	if aws.ToString(in.Cluster) != "shiny-cluster" {
		t.Errorf("Cluster = %q", aws.ToString(in.Cluster))
	}
	if aws.ToString(in.TaskDefinition) != "shiny-app:7" {
		t.Errorf("TaskDefinition = %q", aws.ToString(in.TaskDefinition))
	}
	if in.LaunchType != ecstypes.LaunchTypeFargate {
		t.Errorf("LaunchType = %q, want FARGATE", in.LaunchType)
	}
	if aws.ToInt32(in.Count) != 1 {
		t.Errorf("Count = %d, want 1", aws.ToInt32(in.Count))
	}
	if aws.ToString(in.StartedBy) != startedBy {
		t.Errorf("StartedBy = %q, want %q", aws.ToString(in.StartedBy), startedBy)
	}
	if in.NetworkConfiguration == nil || in.NetworkConfiguration.AwsvpcConfiguration == nil {
		t.Fatal("missing awsvpc network configuration")
	}
	vpc := in.NetworkConfiguration.AwsvpcConfiguration
	if len(vpc.Subnets) != 2 || vpc.Subnets[0] != "subnet-a" {
		t.Errorf("Subnets = %v", vpc.Subnets)
	}
	if vpc.AssignPublicIp != ecstypes.AssignPublicIpDisabled {
		t.Errorf("AssignPublicIp = %q, want DISABLED", vpc.AssignPublicIp)
	}
	if in.Overrides == nil || len(in.Overrides.ContainerOverrides) != 1 {
		t.Fatal("missing container override")
	}
	ov := in.Overrides.ContainerOverrides[0]
	if aws.ToString(ov.Name) != "app" {
		t.Errorf("override Name = %q, want app", aws.ToString(ov.Name))
	}
	if len(ov.Command) != 4 || ov.Command[0] != "shiny" {
		t.Errorf("override Command = %v", ov.Command)
	}
	if aws.ToInt32(ov.Memory) != 512 {
		t.Errorf("override Memory = %d, want 512", aws.ToInt32(ov.Memory))
	}
	if aws.ToInt32(ov.Cpu) != 512 { // 50% of one vCPU = 512 CPU units
		t.Errorf("override Cpu = %d, want 512", aws.ToInt32(ov.Cpu))
	}
	env := map[string]string{}
	for _, kv := range ov.Environment {
		env[aws.ToString(kv.Name)] = aws.ToString(kv.Value)
	}
	if env["FOO"] != "bar" || env["PORT"] != "8000" {
		t.Errorf("override Environment = %v", env)
	}
	// The runner image needs the bundle identity to fetch and run the app.
	if env["SHINYHUB_CONTENT_DIGEST"] != "sha256:abc" {
		t.Errorf("SHINYHUB_CONTENT_DIGEST = %q, want sha256:abc", env["SHINYHUB_CONTENT_DIGEST"])
	}
	if env["SHINYHUB_SLUG"] != "demo" || env["SHINYHUB_REPLICA_INDEX"] != "2" ||
		env["SHINYHUB_DEPLOYMENT_ID"] != "99" || env["SHINYHUB_APP_VERSION"] != "v3" {
		t.Errorf("platform env = %v", env)
	}
	tags := map[string]string{}
	for _, tg := range in.Tags {
		tags[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	if tags[tagSlug] != "demo" || tags[tagReplicaIndex] != "2" ||
		tags[tagTier] != "burst" || tags[tagDeploymentID] != "99" || tags[tagAppVersion] != "v3" {
		t.Errorf("tags = %v", tags)
	}
	// The port must be tagged so recovery can rebuild http://<ip>:<port>.
	if tags[tagPort] != "8000" {
		t.Errorf("tags[%s] = %q, want 8000", tagPort, tags[tagPort])
	}
}

func TestInventoryFallsBackToPortlessURLWithoutPortTag(t *testing.T) {
	f := &fakeECS{
		listTasksFn: func(*ecs.ListTasksInput) (*ecs.ListTasksOutput, error) {
			return &ecs.ListTasksOutput{TaskArns: []string{"arn-legacy"}}, nil
		},
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			task := taskWithIP("arn-legacy", "10.9.9.9", "RUNNING")
			task.Tags = []ecstypes.Tag{
				{Key: aws.String(tagSlug), Value: aws.String("demo")},
				{Key: aws.String(tagReplicaIndex), Value: aws.String("0")},
			}
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{task}}, nil
		},
	}
	items, err := fastRuntime(f).Inventory(context.Background())
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(items) != 1 || items[0].URL != "http://10.9.9.9" {
		t.Fatalf("URL = %q, want portless fallback http://10.9.9.9", items[0].URL)
	}
}

func TestStartAssignsPublicIPWhenConfigured(t *testing.T) {
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{taskWithIP("task-arn", "10.0.0.9", "RUNNING")}}, nil
		},
	}
	cfg := testCfg()
	cfg.AssignPublicIP = true
	r := New(f, cfg, nil, WithPollInterval(time.Millisecond), WithStartTimeout(50*time.Millisecond))
	if _, err := r.Start(context.Background(), startParams(), io.Discard); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := f.runInputs[0].NetworkConfiguration.AwsvpcConfiguration.AssignPublicIp; got != ecstypes.AssignPublicIpEnabled {
		t.Errorf("AssignPublicIp = %q, want ENABLED", got)
	}
}

func TestStartToleratesEventuallyConsistentDescribe(t *testing.T) {
	// ECS DescribeTasks can briefly return no task for a freshly created ARN.
	// Start must keep polling rather than fail the otherwise-healthy launch.
	calls := 0
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			calls++
			if calls < 2 {
				return &ecs.DescribeTasksOutput{}, nil // not visible yet
			}
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{taskWithIP("task-arn", "10.2.2.2", "RUNNING")}}, nil
		},
	}
	r := fastRuntime(f)
	ep, err := r.Start(context.Background(), startParams(), io.Discard)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ep.URL != "http://10.2.2.2:8000" {
		t.Errorf("URL = %q, want http://10.2.2.2:8000", ep.URL)
	}
	if len(f.stopInputs) != 0 {
		t.Errorf("a transiently-invisible task must not be stopped; got %d StopTask calls", len(f.stopInputs))
	}
}

func TestReplicaEnvOmitsUnsetIdentity(t *testing.T) {
	p := process.StartParams{Slug: "demo", Index: 0} // no digest, deployment, version
	env := map[string]string{}
	for _, kv := range replicaEnv(p) {
		env[aws.ToString(kv.Name)] = aws.ToString(kv.Value)
	}
	if _, ok := env["SHINYHUB_CONTENT_DIGEST"]; ok {
		t.Error("SHINYHUB_CONTENT_DIGEST should be omitted when ContentDigest is empty")
	}
	if _, ok := env["SHINYHUB_DEPLOYMENT_ID"]; ok {
		t.Error("SHINYHUB_DEPLOYMENT_ID should be omitted when DeploymentID is 0")
	}
	if env["SHINYHUB_SLUG"] != "demo" || env["SHINYHUB_REPLICA_INDEX"] != "0" {
		t.Errorf("always-present platform env missing: %v", env)
	}
}

func TestStartRoutesViaPublicIPWhenConfigured(t *testing.T) {
	const eni = "eni-abc123"
	ecsCalls := 0
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			ecsCalls++
			// ENI attaches on the 2nd poll.
			if ecsCalls < 2 {
				return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
					TaskArn: aws.String("task-arn"), LastStatus: aws.String("PROVISIONING"),
				}}}, nil
			}
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{
				taskWithENI("task-arn", eni, "10.0.0.5", "RUNNING"),
			}}, nil
		},
	}
	e2 := &fakeEC2{
		describeFn: func(in *ec2.DescribeNetworkInterfacesInput) (*ec2.DescribeNetworkInterfacesOutput, error) {
			if len(in.NetworkInterfaceIds) != 1 || in.NetworkInterfaceIds[0] != eni {
				t.Errorf("DescribeNetworkInterfaces ids = %v, want [%s]", in.NetworkInterfaceIds, eni)
			}
			return &ec2.DescribeNetworkInterfacesOutput{NetworkInterfaces: []ec2types.NetworkInterface{{
				Association: &ec2types.NetworkInterfaceAssociation{PublicIp: aws.String("203.0.113.7")},
			}}}, nil
		},
	}
	cfg := testCfg()
	cfg.AssignPublicIP = true
	cfg.RouteViaPublicIP = true
	r := New(f, cfg, nil, WithEC2Client(e2), WithPollInterval(time.Millisecond), WithStartTimeout(50*time.Millisecond))

	ep, err := r.Start(context.Background(), startParams(), io.Discard)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ep.URL != "http://203.0.113.7:8000" {
		t.Errorf("URL = %q, want http://203.0.113.7:8000 (public IP)", ep.URL)
	}
	if e2.calls == 0 {
		t.Error("expected EC2 DescribeNetworkInterfaces to be called for public-IP routing")
	}
}

func TestStartPublicIPWaitsForAssociation(t *testing.T) {
	// ENI is attached but the public IP isn't associated on the first EC2 call;
	// routeIP must return "" (keep polling) rather than error or route to empty.
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{
				taskWithENI("task-arn", "eni-x", "10.0.0.5", "RUNNING"),
			}}, nil
		},
	}
	calls := 0
	e2 := &fakeEC2{
		describeFn: func(*ec2.DescribeNetworkInterfacesInput) (*ec2.DescribeNetworkInterfacesOutput, error) {
			calls++
			if calls < 2 {
				// Association not present yet.
				return &ec2.DescribeNetworkInterfacesOutput{NetworkInterfaces: []ec2types.NetworkInterface{{}}}, nil
			}
			return &ec2.DescribeNetworkInterfacesOutput{NetworkInterfaces: []ec2types.NetworkInterface{{
				Association: &ec2types.NetworkInterfaceAssociation{PublicIp: aws.String("198.51.100.9")},
			}}}, nil
		},
	}
	cfg := testCfg()
	cfg.AssignPublicIP = true
	cfg.RouteViaPublicIP = true
	r := New(f, cfg, nil, WithEC2Client(e2), WithPollInterval(time.Millisecond), WithStartTimeout(time.Second))
	ep, err := r.Start(context.Background(), startParams(), io.Discard)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ep.URL != "http://198.51.100.9:8000" {
		t.Errorf("URL = %q, want http://198.51.100.9:8000", ep.URL)
	}
	if calls < 2 {
		t.Errorf("expected to poll EC2 until association present, calls=%d", calls)
	}
}

func TestRouteViaPublicIPWithoutEC2ClientErrors(t *testing.T) {
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{
				taskWithENI("task-arn", "eni-x", "10.0.0.5", "RUNNING"),
			}}, nil
		},
	}
	cfg := testCfg()
	cfg.RouteViaPublicIP = true // no EC2 client supplied
	r := New(f, cfg, nil, WithPollInterval(time.Millisecond), WithStartTimeout(50*time.Millisecond))
	if _, err := r.Start(context.Background(), startParams(), io.Discard); err == nil {
		t.Fatal("expected error when RouteViaPublicIP set without an EC2 client")
	}
}

func TestStartReturnsErrorOnRunTaskFailure(t *testing.T) {
	f := &fakeECS{
		runTaskFn: func(*ecs.RunTaskInput) (*ecs.RunTaskOutput, error) {
			return &ecs.RunTaskOutput{Failures: []ecstypes.Failure{{
				Reason: aws.String("RESOURCE:MEMORY"),
				Detail: aws.String("no capacity"),
			}}}, nil
		},
	}
	r := fastRuntime(f)
	if _, err := r.Start(context.Background(), startParams(), io.Discard); err == nil {
		t.Fatal("expected error on RunTask failure")
	}
	if len(f.stopInputs) != 0 {
		t.Errorf("StopTask should not be called when no task was created; got %d", len(f.stopInputs))
	}
}

func TestStartStopsTaskThatNeverBecomesRoutable(t *testing.T) {
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			// Never acquires an IP, never STOPPED → Start times out.
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				TaskArn:    aws.String("task-arn"),
				LastStatus: aws.String("PENDING"),
			}}}, nil
		},
	}
	r := fastRuntime(f)
	if _, err := r.Start(context.Background(), startParams(), io.Discard); err == nil {
		t.Fatal("expected timeout error")
	}
	if len(f.stopInputs) != 1 {
		t.Fatalf("expected the leaked task to be stopped once, got %d", len(f.stopInputs))
	}
	if aws.ToString(f.stopInputs[0].Task) != "task-arn" {
		t.Errorf("stopped wrong task: %q", aws.ToString(f.stopInputs[0].Task))
	}
}

func TestStartFailsWhenTaskStopsBeforeRoutable(t *testing.T) {
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				TaskArn:       aws.String("task-arn"),
				LastStatus:    aws.String("STOPPED"),
				StoppedReason: aws.String("EssentialContainerExited"),
			}}}, nil
		},
	}
	r := fastRuntime(f)
	_, err := r.Start(context.Background(), startParams(), io.Discard)
	if err == nil {
		t.Fatal("expected error when task stops before routable")
	}
}

func TestSignalStopsTask(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		f := &fakeECS{}
		r := fastRuntime(f)
		handle := process.RunHandle{ContainerID: encodeHandle("arn:aws:ecs:r:a:task/c/xyz")}
		if err := r.Signal(handle, sig); err != nil {
			t.Fatalf("Signal(%v): %v", sig, err)
		}
		if len(f.stopInputs) != 1 {
			t.Fatalf("Signal(%v): StopTask called %d times, want 1", sig, len(f.stopInputs))
		}
		if aws.ToString(f.stopInputs[0].Task) != "arn:aws:ecs:r:a:task/c/xyz" {
			t.Errorf("Signal(%v): stopped %q", sig, aws.ToString(f.stopInputs[0].Task))
		}
	}
}

func TestSignalIgnoresNonStopSignals(t *testing.T) {
	f := &fakeECS{}
	r := fastRuntime(f)
	handle := process.RunHandle{ContainerID: encodeHandle("task-arn")}
	if err := r.Signal(handle, syscall.SIGHUP); err != nil {
		t.Fatalf("Signal(SIGHUP): %v", err)
	}
	if len(f.stopInputs) != 0 {
		t.Errorf("SIGHUP should be a no-op; StopTask called %d times", len(f.stopInputs))
	}
}

func TestWaitBlocksUntilStopped(t *testing.T) {
	calls := 0
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			calls++
			status := "RUNNING"
			if calls >= 3 {
				status = "STOPPED"
			}
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				TaskArn:    aws.String("task-arn"),
				LastStatus: aws.String(status),
			}}}, nil
		},
	}
	r := fastRuntime(f)
	handle := process.RunHandle{ContainerID: encodeHandle("task-arn")}
	if err := r.Wait(context.Background(), handle); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if calls < 3 {
		t.Errorf("Wait returned after %d polls, expected to poll until STOPPED", calls)
	}
}

func TestWaitTreatsMissingTaskAsStopped(t *testing.T) {
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{}, nil // task aged out of ECS
		},
	}
	r := fastRuntime(f)
	if err := r.Wait(context.Background(), process.RunHandle{ContainerID: encodeHandle("task-arn")}); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestWaitRespectsContextCancel(t *testing.T) {
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				TaskArn: aws.String("task-arn"), LastStatus: aws.String("RUNNING"),
			}}}, nil
		},
	}
	r := New(f, testCfg(), nil, WithPollInterval(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.Wait(ctx, process.RunHandle{ContainerID: encodeHandle("task-arn")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait err = %v, want context.Canceled", err)
	}
}

func TestStatsReturnsZeroWithoutError(t *testing.T) {
	// A nil error is required: the status endpoint treats a sampler error as a
	// dead replica, so erroring here would misreport a live Fargate task as
	// stopped. Stats reports zero usage instead.
	r := fastRuntime(&fakeECS{})
	cpu, rss, err := r.Stats(context.Background(), process.RunHandle{ContainerID: encodeHandle("task-arn")})
	if err != nil {
		t.Fatalf("Stats err = %v, want nil", err)
	}
	if cpu != 0 || rss != 0 {
		t.Errorf("Stats = (%v, %v), want (0, 0)", cpu, rss)
	}
}

func TestRunOnceReturnsExitCode(t *testing.T) {
	calls := 0
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			calls++
			if calls < 2 {
				return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
					TaskArn: aws.String("task-arn"), LastStatus: aws.String("RUNNING"),
				}}}, nil
			}
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				TaskArn:    aws.String("task-arn"),
				LastStatus: aws.String("STOPPED"),
				Containers: []ecstypes.Container{{ExitCode: aws.Int32(0)}},
			}}}, nil
		},
	}
	r := fastRuntime(f)
	info, err := r.RunOnce(context.Background(), startParams(), io.Discard)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if info.Code != 0 || info.Signaled {
		t.Errorf("ExitInfo = %+v, want {Code:0}", info)
	}
}

func TestRunOnceNonZeroExit(t *testing.T) {
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				TaskArn:    aws.String("task-arn"),
				LastStatus: aws.String("STOPPED"),
				Containers: []ecstypes.Container{{ExitCode: aws.Int32(3)}},
			}}}, nil
		},
	}
	r := fastRuntime(f)
	info, err := r.RunOnce(context.Background(), startParams(), io.Discard)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if info.Code != 3 {
		t.Errorf("ExitInfo.Code = %d, want 3", info.Code)
	}
}

func TestInventoryReconcilesManagedTasks(t *testing.T) {
	f := &fakeECS{
		listTasksFn: func(in *ecs.ListTasksInput) (*ecs.ListTasksOutput, error) {
			if aws.ToString(in.StartedBy) != startedBy {
				t.Errorf("ListTasks StartedBy = %q, want %q", aws.ToString(in.StartedBy), startedBy)
			}
			return &ecs.ListTasksOutput{TaskArns: []string{"arn-running", "arn-untagged"}}, nil
		},
		describeTasksFn: func(in *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			hasTags := false
			for _, fld := range in.Include {
				if fld == ecstypes.TaskFieldTags {
					hasTags = true
				}
			}
			if !hasTags {
				t.Error("DescribeTasks for inventory must include TAGS")
			}
			running := taskWithIP("arn-running", "10.4.5.6", "RUNNING")
			running.Tags = []ecstypes.Tag{
				{Key: aws.String(tagSlug), Value: aws.String("demo")},
				{Key: aws.String(tagReplicaIndex), Value: aws.String("1")},
				{Key: aws.String(tagDeploymentID), Value: aws.String("42")},
				{Key: aws.String(tagPort), Value: aws.String("8000")},
			}
			untagged := ecstypes.Task{TaskArn: aws.String("arn-untagged"), LastStatus: aws.String("RUNNING")}
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{running, untagged}}, nil
		},
	}
	r := fastRuntime(f)
	items, err := r.Inventory(context.Background())
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (untagged task skipped)", len(items))
	}
	it := items[0]
	if it.ContainerID != "arn-running" {
		t.Errorf("ContainerID = %q", it.ContainerID)
	}
	if !it.Running {
		t.Error("Running = false, want true")
	}
	if it.URL != "http://10.4.5.6:8000" {
		t.Errorf("URL = %q, want http://10.4.5.6:8000 (recovered route must keep the app port)", it.URL)
	}
	if it.WorkerID != WorkerID {
		t.Errorf("WorkerID = %q, want %q", it.WorkerID, WorkerID)
	}
	if it.Labels[tagSlug] != "demo" || it.Labels[tagReplicaIndex] != "1" || it.Labels[tagDeploymentID] != "42" {
		t.Errorf("Labels = %v", it.Labels)
	}
}

func TestInventoryPaginatesListTasks(t *testing.T) {
	page := 0
	f := &fakeECS{
		listTasksFn: func(in *ecs.ListTasksInput) (*ecs.ListTasksOutput, error) {
			page++
			if page == 1 {
				if in.NextToken != nil {
					t.Error("first ListTasks should have no NextToken")
				}
				return &ecs.ListTasksOutput{TaskArns: []string{"arn-1"}, NextToken: aws.String("more")}, nil
			}
			if aws.ToString(in.NextToken) != "more" {
				t.Errorf("second ListTasks NextToken = %q, want more", aws.ToString(in.NextToken))
			}
			return &ecs.ListTasksOutput{TaskArns: []string{"arn-2"}}, nil
		},
		describeTasksFn: func(in *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			if len(in.Tasks) != 2 {
				t.Errorf("DescribeTasks got %d arns, want 2 (both pages)", len(in.Tasks))
			}
			return &ecs.DescribeTasksOutput{}, nil
		},
	}
	r := fastRuntime(f)
	if _, err := r.Inventory(context.Background()); err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if page != 2 {
		t.Errorf("ListTasks called %d times, want 2", page)
	}
}

// TestInventoryReportsPROVISIONINGTaskAsRunning asserts that a task in
// PROVISIONING or PENDING state is reported as Running=true, not Running=false.
// A Running=false item in recovery is treated as "gone" and triggers re-placement,
// which would launch a duplicate Fargate task. PROVISIONING/PENDING tasks are
// live; only STOPPED tasks are "gone".
func TestInventoryReportsPROVISIONINGTaskAsRunning(t *testing.T) {
	for _, status := range []string{"PROVISIONING", "PENDING"} {
		status := status
		t.Run(status, func(t *testing.T) {
			f := &fakeECS{
				listTasksFn: func(*ecs.ListTasksInput) (*ecs.ListTasksOutput, error) {
					return &ecs.ListTasksOutput{TaskArns: []string{"arn-1"}}, nil
				},
				describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
					return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
						TaskArn:    aws.String("arn-1"),
						LastStatus: aws.String(status),
						Tags: []ecstypes.Tag{
							{Key: aws.String(tagSlug), Value: aws.String("demo")},
						},
					}}}, nil
				},
			}
			items, err := fastRuntime(f).Inventory(context.Background())
			if err != nil {
				t.Fatalf("Inventory: %v", err)
			}
			if len(items) != 1 {
				t.Fatalf("got %d items, want 1", len(items))
			}
			if !items[0].Running {
				t.Errorf("status %s: Running=false, want true (not STOPPED = still alive)", status)
			}
		})
	}
}

// TestInventoryReportsSTOPPEDTaskAsNotRunning asserts that only STOPPED tasks
// report Running=false, documenting the "not stopped" semantics.
func TestInventoryReportsSTOPPEDTaskAsNotRunning(t *testing.T) {
	f := &fakeECS{
		listTasksFn: func(*ecs.ListTasksInput) (*ecs.ListTasksOutput, error) {
			return &ecs.ListTasksOutput{TaskArns: []string{"arn-stopped"}}, nil
		},
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				TaskArn:    aws.String("arn-stopped"),
				LastStatus: aws.String("STOPPED"),
				Tags: []ecstypes.Tag{
					{Key: aws.String(tagSlug), Value: aws.String("demo")},
				},
			}}}, nil
		},
	}
	items, err := fastRuntime(f).Inventory(context.Background())
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Running {
		t.Error("STOPPED task must report Running=false")
	}
}

// TestRunOnceRejectsEmptySlug asserts that RunOnce returns an error immediately
// when StartParams.Slug is empty, matching the same guard in Start. RunTask must
// not be called when slug validation fails.
func TestRunOnceRejectsEmptySlug(t *testing.T) {
	f := &fakeECS{}
	r := fastRuntime(f)
	p := startParams()
	p.Slug = ""
	_, err := r.RunOnce(context.Background(), p, io.Discard)
	if err == nil {
		t.Fatal("RunOnce with empty slug must return an error")
	}
	if len(f.runInputs) != 0 {
		t.Error("RunTask must not be called when slug is empty")
	}
}

// TestRunOnceCancelsViaDescribeError asserts that when ctx is cancelled during
// the describeTask poll (describe returns ctx.Err), RunOnce stops the task and
// returns a signalled exit - not an error.
func TestRunOnceCancelsViaDescribeError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			cancel() // trigger cancellation on first describe
			return nil, context.Canceled
		},
	}
	r := fastRuntime(f)
	info, err := r.RunOnce(ctx, startParams(), io.Discard)
	if err != nil {
		t.Fatalf("RunOnce on ctx cancel: unexpected error %v", err)
	}
	if info.Code != -1 || !info.Signaled {
		t.Errorf("ExitInfo = %+v, want {Code:-1, Signaled:true}", info)
	}
	// stop() must have been called to clean up the task.
	if len(f.stopInputs) != 1 {
		t.Errorf("expected 1 StopTask on cancellation, got %d", len(f.stopInputs))
	}
}

// TestRunOnceCancelsViaSleepError asserts that when ctx is cancelled during the
// sleep between polls (sleep returns ctx.Err), RunOnce stops the task and
// returns a signalled exit.
func TestRunOnceCancelsViaSleepError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var polls atomic.Int32
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			polls.Add(1)
			// Return RUNNING so RunOnce sleeps between polls.
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				TaskArn:    aws.String("task-arn"),
				LastStatus: aws.String("RUNNING"),
			}}}, nil
		},
	}
	// Use a long poll interval so sleep() will be waiting when we cancel.
	r := New(f, testCfg(), nil, WithPollInterval(time.Hour))
	type result struct {
		info process.ExitInfo
		err  error
	}
	done := make(chan result, 1)
	go func() {
		info, err := r.RunOnce(ctx, startParams(), io.Discard)
		done <- result{info, err}
	}()
	// Let at least one describe poll happen, then cancel.
	for polls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	res := <-done
	if res.err != nil {
		t.Fatalf("RunOnce on sleep cancel: unexpected error %v", res.err)
	}
	if res.info.Code != -1 || !res.info.Signaled {
		t.Errorf("ExitInfo = %+v, want {Code:-1, Signaled:true}", res.info)
	}
	if len(f.stopInputs) != 1 {
		t.Errorf("expected 1 StopTask on sleep cancellation, got %d", len(f.stopInputs))
	}
}

// TestInventoryBatches101Tasks asserts that Inventory correctly handles 101
// tasks (splits into two DescribeTasks calls: one for 100 and one for 1).
// A regression here would silently drop the 101st task from recovery.
func TestInventoryBatches101Tasks(t *testing.T) {
	// Build 101 task ARNs.
	arns := make([]string, 101)
	for i := range arns {
		arns[i] = fmt.Sprintf("arn-%03d", i)
	}
	f := &fakeECS{
		listTasksFn: func(*ecs.ListTasksInput) (*ecs.ListTasksOutput, error) {
			return &ecs.ListTasksOutput{TaskArns: arns}, nil
		},
		describeTasksFn: func(in *ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			// Return each ARN as a task with a slug tag so it passes the slug filter.
			tasks := make([]ecstypes.Task, len(in.Tasks))
			for i, arn := range in.Tasks {
				tasks[i] = ecstypes.Task{
					TaskArn:    aws.String(arn),
					LastStatus: aws.String("RUNNING"),
					Containers: []ecstypes.Container{{
						NetworkInterfaces: []ecstypes.NetworkInterface{{
							PrivateIpv4Address: aws.String("10.0.0.1"),
						}},
					}},
					Tags: []ecstypes.Tag{
						{Key: aws.String(tagSlug), Value: aws.String("demo")},
						{Key: aws.String(tagPort), Value: aws.String("8000")},
					},
				}
			}
			return &ecs.DescribeTasksOutput{Tasks: tasks}, nil
		},
	}
	r := fastRuntime(f)
	items, err := r.Inventory(context.Background())
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(items) != 101 {
		t.Errorf("Inventory returned %d items, want 101", len(items))
	}
	// Verify two DescribeTasks calls: one for 100, one for 1.
	if len(f.describeInputs) != 2 {
		t.Errorf("DescribeTasks called %d times, want 2 (batched at 100)", len(f.describeInputs))
	}
	if len(f.describeInputs[0].Tasks) != 100 {
		t.Errorf("first batch = %d tasks, want 100", len(f.describeInputs[0].Tasks))
	}
	if len(f.describeInputs[1].Tasks) != 1 {
		t.Errorf("second batch = %d tasks, want 1", len(f.describeInputs[1].Tasks))
	}
}

func TestDecodeHandleAcceptsBareARN(t *testing.T) {
	arn, err := decodeHandle("arn:aws:ecs:r:a:task/c/raw")
	if err != nil {
		t.Fatalf("decodeHandle: %v", err)
	}
	if arn != "arn:aws:ecs:r:a:task/c/raw" {
		t.Errorf("arn = %q", arn)
	}
}

func TestDecodeHandleRejectsEmpty(t *testing.T) {
	if _, err := decodeHandle(""); err == nil {
		t.Fatal("expected error on empty handle")
	}
}

// TestDescribeTaskMISSINGKeepsPolling asserts that a MISSING failure in
// out.Failures is treated as eventual consistency (not visible yet) and does
// NOT cause waitForIP to fail immediately. A subsequent poll returns the task
// with an IP, so Start succeeds.
func TestDescribeTaskMISSINGKeepsPolling(t *testing.T) {
	calls := 0
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			calls++
			if calls == 1 {
				// First poll: ECS returns a MISSING failure (eventual consistency).
				return &ecs.DescribeTasksOutput{
					Failures: []ecstypes.Failure{{
						Arn:    aws.String("task-arn"),
						Reason: aws.String("MISSING"),
					}},
				}, nil
			}
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{
				taskWithIP("task-arn", "10.0.0.5", "RUNNING"),
			}}, nil
		},
	}
	r := fastRuntime(f)
	ep, err := r.Start(context.Background(), startParams(), io.Discard)
	if err != nil {
		t.Fatalf("Start: unexpected error on MISSING failure: %v", err)
	}
	if ep.URL != "http://10.0.0.5:8000" {
		t.Errorf("URL = %q, want http://10.0.0.5:8000", ep.URL)
	}
	if calls < 2 {
		t.Errorf("expected at least 2 describe calls (MISSING then success), got %d", calls)
	}
}

// TestDescribeTaskHardFailureReturnsFastError asserts that a non-MISSING failure
// reason in out.Failures causes describeTask to return a wrapped hard error
// immediately (not nil). This causes waitForIP/Wait/RunOnce to fail fast instead
// of polling to timeout.
func TestDescribeTaskHardFailureReturnsFastError(t *testing.T) {
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{
				Failures: []ecstypes.Failure{{
					Arn:    aws.String("task-arn"),
					Reason: aws.String("RESOURCE:MEMORY"),
					Detail: aws.String("no capacity in us-east-1a"),
				}},
			}, nil
		},
	}
	r := fastRuntime(f)
	_, err := r.Start(context.Background(), startParams(), io.Discard)
	if err == nil {
		t.Fatal("expected hard error on RESOURCE:MEMORY failure, got nil")
	}
	if !strings.Contains(err.Error(), "RESOURCE:MEMORY") {
		t.Errorf("error %q should contain the Reason code RESOURCE:MEMORY", err.Error())
	}
	// At most one describe call: fail fast, do not poll.
	if len(f.describeInputs) > 1 {
		t.Errorf("expected 1 describe call on hard failure, got %d", len(f.describeInputs))
	}
}

func TestTaskPrivateIPFallsBackToAttachment(t *testing.T) {
	task := ecstypes.Task{
		Attachments: []ecstypes.Attachment{{
			Type: aws.String("ElasticNetworkInterface"),
			Details: []ecstypes.KeyValuePair{
				{Name: aws.String("networkInterfaceId"), Value: aws.String("eni-1")},
				{Name: aws.String("privateIPv4Address"), Value: aws.String("172.31.0.5")},
			},
		}},
	}
	if got := taskPrivateIP(task); got != "172.31.0.5" {
		t.Errorf("taskPrivateIP = %q, want 172.31.0.5", got)
	}
}

// contextCapturingECS wraps fakeECS and captures the context passed to StopTask
// so tests can assert it has a deadline.
type contextCapturingECS struct {
	fakeECS
	stopCtx context.Context
}

func (c *contextCapturingECS) StopTask(ctx context.Context, in *ecs.StopTaskInput, _ ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	c.stopCtx = ctx
	c.fakeECS.stopInputs = append(c.fakeECS.stopInputs, in)
	return &ecs.StopTaskOutput{}, nil
}

// TestSignalCallsStopTask asserts that Signal(SIGTERM) invokes StopTask and
// returns without error. The deadline invariant on the stop context is
// separately pinned by TestSignalStopContextHasDeadline.
func TestSignalCallsStopTask(t *testing.T) {
	stopCalled := false
	f := &fakeECS{}
	f.stopTaskFn = func(_ *ecs.StopTaskInput) (*ecs.StopTaskOutput, error) {
		stopCalled = true
		return &ecs.StopTaskOutput{}, nil
	}
	r := fastRuntime(f)
	handle := process.RunHandle{ContainerID: encodeHandle("arn:aws:ecs:r:a:task/c/sigtest")}
	if err := r.Signal(handle, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: unexpected error: %v", err)
	}
	if !stopCalled {
		t.Error("StopTask was never called")
	}
}

// TestSignalStopContextHasDeadline asserts that the context passed to
// stop() inside Signal has a deadline (is not context.Background). Uses a
// context-capturing fake to inspect the context directly.
func TestSignalStopContextHasDeadline(t *testing.T) {
	f := &contextCapturingECS{}
	r := fastRuntime(f)
	handle := process.RunHandle{ContainerID: encodeHandle("task-arn")}
	if err := r.Signal(handle, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if f.stopCtx == nil {
		t.Fatal("StopTask was not called")
	}
	if _, ok := f.stopCtx.Deadline(); !ok {
		t.Error("Signal must pass a context with a deadline to StopTask (not context.Background)")
	}
}

// TestSignalCompletesEvenIfCallerContextAlreadyCancelled documents that Signal
// uses its own internal timeout context so the caller cannot cancel it - Signal
// has no caller context parameter, so this is a no-op guard that confirms the
// basic happy path remains unaffected.
func TestSignalCompletesEvenIfCallerContextAlreadyCancelled(t *testing.T) {
	f := &fakeECS{}
	r := fastRuntime(f)
	handle := process.RunHandle{ContainerID: encodeHandle("task-arn")}
	if err := r.Signal(handle, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if len(f.stopInputs) != 1 {
		t.Fatalf("StopTask called %d times, want 1", len(f.stopInputs))
	}
}

// TestCPURounding asserts the CPU unit conversion rounds (not truncates) so that
// non-multiples of 100 produce the closest representable ECS value.
func TestCPURounding(t *testing.T) {
	cases := []struct {
		pct  int
		want int32
	}{
		{10, 102}, // (10*1024+50)/100 = 10290/100 = 102
		{50, 512}, // (50*1024+50)/100 = 51250/100 = 512
		{75, 768}, // (75*1024+50)/100 = 76850/100 = 768
		{33, 338}, // (33*1024+50)/100 = 33842/100 = 338 (truncation gives 337)
	}
	for _, tc := range cases {
		p := process.StartParams{CPUQuotaPercent: tc.pct}
		ov := containerOverride("app", p)
		if got := aws.ToInt32(ov.Cpu); got != tc.want {
			t.Errorf("CPUQuotaPercent=%d: Cpu=%d, want %d", tc.pct, got, tc.want)
		}
	}
}

// TestStartRejectsEmptySlug asserts that Start returns an error immediately when
// StartParams.Slug is empty, rather than launching a task that cannot self-
// identify. SHINYHUB_SLUG must always be present in the task env.
func TestStartRejectsEmptySlug(t *testing.T) {
	f := &fakeECS{}
	r := fastRuntime(f)
	p := startParams()
	p.Slug = ""
	_, err := r.Start(context.Background(), p, io.Discard)
	if err == nil {
		t.Fatal("Start with empty slug must return an error")
	}
	if len(f.runInputs) != 0 {
		t.Error("RunTask must not be called when slug is empty")
	}
}

// TestReplicaEnvAlwaysEmitsSHINYHUBSLUG asserts that SHINYHUB_SLUG is always
// present in the container environment. The runner image requires this variable
// to identify the app; it must never be silently omitted.
func TestReplicaEnvAlwaysEmitsSHINYHUBSLUG(t *testing.T) {
	p := process.StartParams{Slug: "myapp", Index: 0}
	env := map[string]string{}
	for _, kv := range replicaEnv(p) {
		env[aws.ToString(kv.Name)] = aws.ToString(kv.Value)
	}
	if env["SHINYHUB_SLUG"] != "myapp" {
		t.Errorf("SHINYHUB_SLUG = %q, want myapp", env["SHINYHUB_SLUG"])
	}
}

// TestClientTokenIsSameWithinTimeBucket asserts that two calls with the same
// inputs but the same time bucket produce identical tokens (idempotency: a
// control-plane retry within the 10-min ECS window re-uses the same token).
func TestClientTokenIsSameWithinTimeBucket(t *testing.T) {
	// unix/600 == 2 for these two values (1200 and 1199 are in different buckets;
	// use 1201 and 1250 which are both in bucket 2).
	tok1 := clientToken("my-cluster", "demo", 1, 42, 1201)
	tok2 := clientToken("my-cluster", "demo", 1, 42, 1250)
	if tok1 != tok2 {
		t.Errorf("tokens differ within the same 10-min bucket: %q != %q", tok1, tok2)
	}
}

// TestClientTokenDiffersAcrossTimeBucket asserts that the token changes when
// the time bucket advances, so a deliberate re-launch after a STOPPED task
// in the prior window still issues a fresh RunTask instead of being deduplicated.
func TestClientTokenDiffersAcrossTimeBucket(t *testing.T) {
	// bucket 2 (unix 1200-1799) vs bucket 3 (unix 1800-2399)
	tok1 := clientToken("my-cluster", "demo", 1, 42, 1201)
	tok2 := clientToken("my-cluster", "demo", 1, 42, 1801)
	if tok1 == tok2 {
		t.Errorf("tokens should differ across time buckets, but both = %q", tok1)
	}
}

// TestClientTokenDiffersAcrossReplicas asserts that different replica indices
// produce different tokens even in the same time bucket, so concurrent replica
// launches produce independent idempotency keys.
func TestClientTokenDiffersAcrossReplicas(t *testing.T) {
	tok0 := clientToken("my-cluster", "demo", 0, 42, 1201)
	tok1 := clientToken("my-cluster", "demo", 1, 42, 1201)
	if tok0 == tok1 {
		t.Errorf("replica 0 and replica 1 have same token: %q", tok0)
	}
}

// TestClientTokenLengthIs64Chars asserts the token fits the ECS ClientToken
// maximum length of 64 characters.
func TestClientTokenLengthIs64Chars(t *testing.T) {
	tok := clientToken("cluster", "slug", 0, 1, 1000)
	if len(tok) != 64 {
		t.Errorf("clientToken length = %d, want 64", len(tok))
	}
}

// TestClientTokenZeroDeploymentID asserts that a zero deployment ID (pre-first-
// deploy edge) still produces a valid 64-char token that differs across time
// buckets, so distinct starts are not conflated.
func TestClientTokenZeroDeploymentID(t *testing.T) {
	tok1 := clientToken("cluster", "slug", 0, 0, 1201)
	tok2 := clientToken("cluster", "slug", 0, 0, 1801)
	if len(tok1) != 64 {
		t.Errorf("zero-deploymentID token length = %d, want 64", len(tok1))
	}
	if tok1 == tok2 {
		t.Errorf("zero-deploymentID tokens should differ across time buckets")
	}
}

// fakeFargateMetrics records every call for assertion in tests.
type fakeFargateMetrics struct {
	mu               sync.Mutex
	runTaskResults   []string
	waitIPTimeouts   int
	stopTaskResults  []string
	inventoryErrors  int
	runTaskLatencies []float64
}

func (f *fakeFargateMetrics) RecordRunTask(result string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runTaskResults = append(f.runTaskResults, result)
}
func (f *fakeFargateMetrics) RecordWaitIPTimeout() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitIPTimeouts++
}
func (f *fakeFargateMetrics) RecordStopTask(result string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopTaskResults = append(f.stopTaskResults, result)
}
func (f *fakeFargateMetrics) RecordInventoryError() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inventoryErrors++
}
func (f *fakeFargateMetrics) ObserveRunTaskLatency(seconds float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runTaskLatencies = append(f.runTaskLatencies, seconds)
}

func TestSetMetrics_DefaultIsNoop(t *testing.T) {
	r := fastRuntime(&fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{taskWithIP("task-arn", "10.0.0.1", "RUNNING")}}, nil
		},
	})
	// No SetMetrics call: must not panic on any operation.
	_, err := r.Start(context.Background(), startParams(), io.Discard)
	if err != nil {
		t.Fatalf("Start with no-op metrics: %v", err)
	}
}

func TestMetrics_StartRecordsRunTaskOk(t *testing.T) {
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{taskWithIP("task-arn", "10.0.0.5", "RUNNING")}}, nil
		},
	}
	r := fastRuntime(f)
	fm := &fakeFargateMetrics{}
	r.SetMetrics(fm)

	if _, err := r.Start(context.Background(), startParams(), io.Discard); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.runTaskResults) != 1 || fm.runTaskResults[0] != "ok" {
		t.Errorf("RecordRunTask results = %v, want [ok]", fm.runTaskResults)
	}
	if len(fm.runTaskLatencies) != 1 || fm.runTaskLatencies[0] <= 0 {
		t.Errorf("ObserveRunTaskLatency = %v, want one positive value", fm.runTaskLatencies)
	}
}

func TestMetrics_StartRecordsRunTaskError(t *testing.T) {
	f := &fakeECS{
		runTaskFn: func(*ecs.RunTaskInput) (*ecs.RunTaskOutput, error) {
			return nil, errors.New("iam forbidden")
		},
	}
	r := fastRuntime(f)
	fm := &fakeFargateMetrics{}
	r.SetMetrics(fm)

	if _, err := r.Start(context.Background(), startParams(), io.Discard); err == nil {
		t.Fatal("expected error")
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.runTaskResults) != 1 || fm.runTaskResults[0] != "error" {
		t.Errorf("RecordRunTask results = %v, want [error]", fm.runTaskResults)
	}
	if len(fm.runTaskLatencies) != 1 {
		t.Errorf("ObserveRunTaskLatency calls = %d, want 1 (even on error)", len(fm.runTaskLatencies))
	}
}

func TestMetrics_StartRecordsRunTaskFailureEntry(t *testing.T) {
	// RunTask returns HTTP 200 but with a Failures entry (ECS scheduling failure).
	f := &fakeECS{
		runTaskFn: func(*ecs.RunTaskInput) (*ecs.RunTaskOutput, error) {
			return &ecs.RunTaskOutput{Failures: []ecstypes.Failure{{
				Reason: aws.String("RESOURCE:MEMORY"),
				Detail: aws.String("no capacity"),
			}}}, nil
		},
	}
	r := fastRuntime(f)
	fm := &fakeFargateMetrics{}
	r.SetMetrics(fm)

	if _, err := r.Start(context.Background(), startParams(), io.Discard); err == nil {
		t.Fatal("expected error")
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.runTaskResults) != 1 || fm.runTaskResults[0] != "error" {
		t.Errorf("RecordRunTask results = %v, want [error] for Failures entry", fm.runTaskResults)
	}
}

func TestMetrics_WaitIPTimeoutIncremented(t *testing.T) {
	// Task stays PENDING forever -> waitForIP times out.
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{{
				TaskArn:    aws.String("task-arn"),
				LastStatus: aws.String("PENDING"),
			}}}, nil
		},
	}
	r := fastRuntime(f)
	fm := &fakeFargateMetrics{}
	r.SetMetrics(fm)

	_, err := r.Start(context.Background(), startParams(), io.Discard)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.waitIPTimeouts != 1 {
		t.Errorf("RecordWaitIPTimeout count = %d, want 1", fm.waitIPTimeouts)
	}
}

func TestMetrics_StopTaskOk(t *testing.T) {
	f := &fakeECS{}
	r := fastRuntime(f)
	fm := &fakeFargateMetrics{}
	r.SetMetrics(fm)

	handle := process.RunHandle{ContainerID: encodeHandle("arn:aws:ecs:r:a:task/c/xyz")}
	if err := r.Signal(handle, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.stopTaskResults) != 1 || fm.stopTaskResults[0] != "ok" {
		t.Errorf("RecordStopTask results = %v, want [ok]", fm.stopTaskResults)
	}
}

func TestMetrics_StopTaskError(t *testing.T) {
	f := &fakeECS{
		stopTaskFn: func(*ecs.StopTaskInput) (*ecs.StopTaskOutput, error) {
			return nil, errors.New("ecs: task not found")
		},
	}
	r := fastRuntime(f)
	fm := &fakeFargateMetrics{}
	r.SetMetrics(fm)

	handle := process.RunHandle{ContainerID: encodeHandle("arn:aws:ecs:r:a:task/c/abc")}
	err := r.Signal(handle, syscall.SIGTERM)
	if err == nil {
		t.Fatal("expected error from StopTask")
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.stopTaskResults) != 1 || fm.stopTaskResults[0] != "error" {
		t.Errorf("RecordStopTask results = %v, want [error]", fm.stopTaskResults)
	}
}

func TestSetMetrics_NilResetToNoop(t *testing.T) {
	r := fastRuntime(&fakeECS{})
	fm := &fakeFargateMetrics{}
	r.SetMetrics(fm)
	r.SetMetrics(nil) // reset to no-op
	// Calling Start (which will fail via RunTask) should not panic.
	r.client.(*fakeECS).runTaskFn = func(*ecs.RunTaskInput) (*ecs.RunTaskOutput, error) {
		return nil, errors.New("aws down")
	}
	_, _ = r.Start(context.Background(), startParams(), io.Discard)
	// No assertion needed: test passes as long as no panic occurs.
}

// TestRunTaskReceivesClientToken asserts that Start passes a non-empty
// ClientToken on the RunTask call so ECS can deduplicate control-plane retries.
func TestRunTaskReceivesClientToken(t *testing.T) {
	f := &fakeECS{
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{
				taskWithIP("task-arn", "10.0.0.1", "RUNNING"),
			}}, nil
		},
	}
	r := fastRuntime(f)
	if _, err := r.Start(context.Background(), startParams(), io.Discard); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(f.runInputs) != 1 {
		t.Fatalf("RunTask called %d times, want 1", len(f.runInputs))
	}
	ct := aws.ToString(f.runInputs[0].ClientToken)
	if ct == "" {
		t.Error("RunTask ClientToken must be non-empty")
	}
	if len(ct) != 64 {
		t.Errorf("RunTask ClientToken length = %d, want 64", len(ct))
	}
}
