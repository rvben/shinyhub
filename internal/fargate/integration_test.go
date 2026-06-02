//go:build integration

package fargate

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/rvben/shinyhub/internal/process"
)

// This file holds the real-cluster smoke test for the Fargate runtime. There is
// no open-source ECS emulator that supports the Fargate awsvpc RunTask path
// (LocalStack gates ECS behind Pro; moto crashes building the task ENI), so the
// only faithful end-to-end check runs against a real ECS cluster.
//
// It is excluded from the default suite three ways: the `integration` build tag,
// the `make test-fargate-it` target, and a hard skip unless the cluster env is
// set. Running it launches a real Fargate task (which incurs AWS charges) and
// stops it again.
//
// Required environment (skips cleanly when SHINYHUB_FARGATE_IT_CLUSTER is unset):
//
//	SHINYHUB_FARGATE_IT_CLUSTER          ECS cluster name or ARN (enables the test)
//	SHINYHUB_FARGATE_IT_TASKDEF          task definition family[:rev] or ARN
//	SHINYHUB_FARGATE_IT_CONTAINER        container name to apply overrides to
//	SHINYHUB_FARGATE_IT_SUBNETS          comma-separated awsvpc subnet IDs
//	SHINYHUB_FARGATE_IT_SECURITY_GROUPS  comma-separated SG IDs (optional)
//	SHINYHUB_FARGATE_IT_ASSIGN_PUBLIC_IP "true" for public subnets (optional)
//	SHINYHUB_FARGATE_IT_REGION           AWS region (optional; else default chain)
//	SHINYHUB_FARGATE_IT_COMMAND          comma-separated launch command override
//	                                     (optional; else the task def's command)
//	SHINYHUB_FARGATE_IT_PORT             route port to assert in the URL (default 8000)
//
// AWS credentials resolve through the standard SDK chain (AWS_PROFILE, env vars,
// SSO, instance role). The task definition's container should stay running long
// enough to acquire an ENI (e.g. a container whose command sleeps), so Start can
// observe the private IP.

func itConfig(t *testing.T) (*ecs.Client, Config, process.StartParams) {
	t.Helper()
	cluster := os.Getenv("SHINYHUB_FARGATE_IT_CLUSTER")
	if cluster == "" {
		t.Skip("SHINYHUB_FARGATE_IT_CLUSTER not set; skipping real-cluster Fargate integration test")
	}
	subnets := splitEnvList(os.Getenv("SHINYHUB_FARGATE_IT_SUBNETS"))
	if len(subnets) == 0 {
		t.Fatal("SHINYHUB_FARGATE_IT_SUBNETS is required when the cluster is set")
	}
	cfg := Config{
		Cluster:        cluster,
		TaskDefinition: os.Getenv("SHINYHUB_FARGATE_IT_TASKDEF"),
		ContainerName:  os.Getenv("SHINYHUB_FARGATE_IT_CONTAINER"),
		Subnets:        subnets,
		SecurityGroups: splitEnvList(os.Getenv("SHINYHUB_FARGATE_IT_SECURITY_GROUPS")),
		AssignPublicIP: os.Getenv("SHINYHUB_FARGATE_IT_ASSIGN_PUBLIC_IP") == "true",
	}
	if cfg.TaskDefinition == "" || cfg.ContainerName == "" {
		t.Fatal("SHINYHUB_FARGATE_IT_TASKDEF and SHINYHUB_FARGATE_IT_CONTAINER are required")
	}

	var opts []func(*awsconfig.LoadOptions) error
	if region := os.Getenv("SHINYHUB_FARGATE_IT_REGION"); region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}

	port := 8000
	if v := os.Getenv("SHINYHUB_FARGATE_IT_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			port = n
		}
	}
	p := process.StartParams{
		Slug:         "shinyhub-it",
		Index:        0,
		Tier:         "it",
		Port:         port,
		Command:      splitEnvList(os.Getenv("SHINYHUB_FARGATE_IT_COMMAND")),
		DeploymentID: 1,
		AppVersion:   "it",
	}
	return ecs.NewFromConfig(awsCfg), cfg, p
}

func splitEnvList(v string) []string {
	out := []string{}
	for _, p := range strings.Split(v, ",") {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// TestIntegration_StartInventorySignalWait drives the full lifecycle against a
// real cluster: launch a task, confirm it routes and appears in inventory, then
// stop it and confirm it terminates. The task is always stopped on cleanup even
// if an assertion fails midway, so a failed run does not leak a billed task.
func TestIntegration_StartInventorySignalWait(t *testing.T) {
	client, cfg, p := itConfig(t)
	rt := New(client, cfg, nil,
		WithPollInterval(5*time.Second),
		WithStartTimeout(4*time.Minute))

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	ep, err := rt.Start(ctx, p, io.Discard)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Guarantee teardown of the real task regardless of later failures.
	defer func() {
		if serr := rt.Signal(ep.Handle, syscall.SIGKILL); serr != nil {
			t.Logf("cleanup stop: %v", serr)
		}
	}()

	t.Logf("started task: url=%s handle=%s", ep.URL, ep.Handle.ContainerID)
	if ep.Provider != Provider {
		t.Errorf("Provider = %q, want %q", ep.Provider, Provider)
	}
	if ep.WorkerID != WorkerID {
		t.Errorf("WorkerID = %q, want %q", ep.WorkerID, WorkerID)
	}
	wantSuffix := ":" + strconv.Itoa(p.Port)
	if !strings.HasPrefix(ep.URL, "http://") || !strings.HasSuffix(ep.URL, wantSuffix) {
		t.Errorf("URL = %q, want http://<ip>%s", ep.URL, wantSuffix)
	}
	taskARN, err := decodeHandle(ep.Handle.ContainerID)
	if err != nil || taskARN == "" {
		t.Fatalf("decodeHandle(%q) = %q, %v", ep.Handle.ContainerID, taskARN, err)
	}

	// The live task must show up in inventory with our identifying labels, the
	// constant worker id, and a port-qualified URL the recovery path can re-route.
	items, err := rt.Inventory(ctx)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	var found *process.InventoryItem
	for i := range items {
		if items[i].ContainerID == taskARN {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("started task %s not present in inventory of %d item(s)", taskARN, len(items))
	}
	if found.Labels[process.LabelSlug] != p.Slug || found.Labels[process.LabelReplicaIndex] != "0" {
		t.Errorf("inventory labels = %v", found.Labels)
	}
	if found.WorkerID != WorkerID {
		t.Errorf("inventory WorkerID = %q, want %q", found.WorkerID, WorkerID)
	}
	if found.URL != ep.URL {
		t.Errorf("inventory URL = %q, want %q (recovery must reconstruct the routed URL)", found.URL, ep.URL)
	}

	// Stop the task and confirm it reaches a terminal state within the window.
	if err := rt.Signal(ep.Handle, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal SIGTERM: %v", err)
	}
	if err := rt.Wait(ctx, ep.Handle); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}
