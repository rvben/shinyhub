package fargate

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// TestDeployRun_FargateRuntimeRoutesToTaskIP drives the full deploy.Run pool
// boot with a real fargate.Runtime (backed by a scripted fake ECS) registered
// on the process.Manager, asserting that the task's private IP, provider, and
// worker id from fargate.Start flow through deploy.Run into the pool result.
//
// This is the only fast (no-AWS, no-Docker) CI coverage of deploy.Run wired to
// the Fargate backend: the unit tests exercise fargate.Runtime in isolation and
// the remote-worker e2e covers deploy.Run -> remote_docker, but nothing else
// exercises deploy.Run -> fargate.Runtime end to end.
func TestDeployRun_FargateRuntimeRoutesToTaskIP(t *testing.T) {
	const taskARN = "arn:aws:ecs:eu-west-1:111122223333:task/shiny-cluster/abc123"
	f := &fakeECS{
		runTaskFn: func(*ecs.RunTaskInput) (*ecs.RunTaskOutput, error) {
			return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{
				TaskArn:    aws.String(taskARN),
				LastStatus: aws.String("PROVISIONING"),
			}}}, nil
		},
		describeTasksFn: func(*ecs.DescribeTasksInput) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{
				taskWithIP(taskARN, "192.0.2.1", "RUNNING"),
			}}, nil
		},
	}

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("burst", fastRuntime(f))
	prx := proxy.New()

	result, err := deploy.Run(deploy.Params{
		Slug:        "demo",
		BundleDir:   t.TempDir(),
		Command:     []string{"shiny", "run", "--port", "8000"},
		Placement:   map[string]int{"burst": 1},
		TierOrder:   []string{"local", "burst"},
		DefaultTier: "local",
		Manager:     mgr,
		Proxy:       prx,
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	})
	if err != nil {
		t.Fatalf("deploy.Run with fargate runtime: %v", err)
	}
	if len(result.Replicas) != 1 {
		t.Fatalf("want 1 replica booted, got %d (failed=%v)", len(result.Replicas), result.Failed)
	}

	r0 := result.Replicas[0]
	if r0.Tier != "burst" {
		t.Errorf("Tier = %q, want burst", r0.Tier)
	}
	if r0.Provider != Provider {
		t.Errorf("Provider = %q, want %q", r0.Provider, Provider)
	}
	if r0.WorkerID != WorkerID {
		t.Errorf("WorkerID = %q, want %q", r0.WorkerID, WorkerID)
	}
	if !strings.Contains(r0.EndpointURL, "192.0.2.1") {
		t.Errorf("EndpointURL = %q, want it to route to the task IP 192.0.2.1", r0.EndpointURL)
	}
	// A Fargate replica has no local process, so deploy.Run must not invent a PID.
	if r0.PID != 0 {
		t.Errorf("PID = %d, want 0 for a Fargate replica", r0.PID)
	}
}
