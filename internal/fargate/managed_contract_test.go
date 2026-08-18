package fargate

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/process/runtimetest"
)

type contractECS struct {
	mu      sync.Mutex
	next    int
	tasks   map[string]ecstypes.Task
	stopped map[string]bool
}

func newContractECS() *contractECS {
	return &contractECS{tasks: make(map[string]ecstypes.Task), stopped: make(map[string]bool)}
}

func (f *contractECS) RunTask(_ context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	sequence := strconv.Itoa(f.next)
	arn := "arn:aws:ecs:eu-west-1:111122223333:task/shinyhub/contract-" + sequence
	task := taskWithIP(arn, "10.0.0."+sequence, "RUNNING")
	task.TaskDefinitionArn = aws.String(testCfg().TaskDefinition)
	task.Tags = append([]ecstypes.Tag(nil), in.Tags...)
	f.tasks[arn] = task
	return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{task}}, nil
}

func (f *contractECS) StopTask(_ context.Context, in *ecs.StopTaskInput, _ ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	arn := aws.ToString(in.Task)
	task := f.tasks[arn]
	task.LastStatus = aws.String("STOPPED")
	f.tasks[arn] = task
	f.stopped[arn] = true
	return &ecs.StopTaskOutput{Task: &task}, nil
}

func (f *contractECS) DescribeTasks(_ context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &ecs.DescribeTasksOutput{}
	for _, arn := range in.Tasks {
		if task, ok := f.tasks[arn]; ok {
			out.Tasks = append(out.Tasks, task)
		}
	}
	return out, nil
}

func (f *contractECS) ListTasks(_ context.Context, _ *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &ecs.ListTasksOutput{}
	for arn := range f.tasks {
		if !f.stopped[arn] {
			out.TaskArns = append(out.TaskArns, arn)
		}
	}
	return out, nil
}

func (*contractECS) DescribeTaskDefinition(context.Context, *ecs.DescribeTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	return &ecs.DescribeTaskDefinitionOutput{}, nil
}
func (*contractECS) RegisterTaskDefinition(context.Context, *ecs.RegisterTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error) {
	return &ecs.RegisterTaskDefinitionOutput{}, nil
}
func (*contractECS) ListTaskDefinitions(context.Context, *ecs.ListTaskDefinitionsInput, ...func(*ecs.Options)) (*ecs.ListTaskDefinitionsOutput, error) {
	return &ecs.ListTaskDefinitionsOutput{}, nil
}
func (*contractECS) DeregisterTaskDefinition(context.Context, *ecs.DeregisterTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.DeregisterTaskDefinitionOutput, error) {
	return &ecs.DeregisterTaskDefinitionOutput{}, nil
}

func TestManagedRuntimeContract(t *testing.T) {
	runtimetest.ManagedRuntime(t, Provider, WorkerID, func(t *testing.T) (process.ManagedRuntime, process.StartParams) {
		t.Helper()
		rt := New(newContractECS(), testCfg(), nil,
			WithPollInterval(time.Millisecond),
			WithStartTimeout(100*time.Millisecond))
		params := startParams()
		params.AppID = 7
		return rt, params
	})
}
