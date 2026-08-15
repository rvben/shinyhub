//go:build integration

package fargate

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/rvben/shinyhub/internal/db"
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

func integrationLogStore(t *testing.T) (*db.Store, int64) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open integration log store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate integration log store: %v", err)
	}
	if err := store.CreateUser(db.CreateUserParams{
		Username: "fargate-it", PasswordHash: "integration-only", Role: "developer",
	}); err != nil {
		t.Fatalf("create integration owner: %v", err)
	}
	owner, err := store.GetUserByUsername("fargate-it")
	if err != nil {
		t.Fatalf("read integration owner: %v", err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "shinyhub-it", Name: "Fargate integration", OwnerID: owner.ID,
	}); err != nil {
		t.Fatalf("create integration app: %v", err)
	}
	app, err := store.GetAppBySlug("shinyhub-it")
	if err != nil {
		t.Fatalf("read integration app: %v", err)
	}
	return store, app.ID
}

func assertExternalLogsHandoff(t *testing.T, details *process.ExternalLogs, taskARN, configuredCluster string) {
	t.Helper()
	if details == nil {
		t.Fatal("Start returned no external log handoff")
	}
	if details.Provider != "aws_ecs" || details.Resource != taskARN {
		t.Fatalf("external log identity = %+v, want provider aws_ecs and task %s", details, taskARN)
	}
	if details.Region == "" || details.Cluster != ecsClusterName(configuredCluster) {
		t.Fatalf("external log location = region %q cluster %q, want non-empty region and cluster %q",
			details.Region, details.Cluster, ecsClusterName(configuredCluster))
	}
	taskID := taskARN[strings.LastIndex(taskARN, "/")+1:]
	u, err := url.Parse(details.ConsoleURL)
	if err != nil {
		t.Fatalf("parse external console URL %q: %v", details.ConsoleURL, err)
	}
	allowedHost := u.Hostname() == "console.aws.amazon.com" ||
		u.Hostname() == "console.amazonaws.cn" ||
		u.Hostname() == "console.amazonaws-us-gov.com"
	wantPath := "/ecs/v2/clusters/" + details.Cluster + "/tasks/" + taskID + "/logs"
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || !allowedHost ||
		u.Path != wantPath || u.Query().Get("region") != details.Region {
		t.Fatalf("external console URL = %q, want safe task Logs URL path %q in region %q",
			details.ConsoleURL, wantPath, details.Region)
	}
}

// TestIntegration_StartInventorySignalWait drives the full lifecycle against a
// real cluster: launch a task, confirm it routes and appears in inventory,
// persist its exact AWS Logs handoff, stop it, and prove the stopped task and
// immutable handoff still identify the same execution. Cleanup stops the task
// after any failure before the normal terminal state, so a failed run does not
// leak a billed task.
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
	stopped := false
	// Guarantee teardown of the real task after any failure before Wait confirms
	// STOPPED. Avoid a redundant StopTask call after the normal path succeeds.
	defer func() {
		if stopped {
			return
		}
		if serr := rt.Signal(ep.Handle, syscall.SIGKILL); serr != nil {
			t.Logf("cleanup stop: %v", serr)
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if werr := rt.Wait(cleanupCtx, ep.Handle); werr != nil {
			t.Logf("cleanup wait for STOPPED: %v", werr)
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
	taskARN, err := rt.decodeHandle(ep.Handle.ContainerID)
	if err != nil || taskARN == "" {
		t.Fatalf("decodeHandle(%q) = %q, %v", ep.Handle.ContainerID, taskARN, err)
	}
	assertExternalLogsHandoff(t, ep.ExternalLogs, taskARN, cfg.Cluster)

	store, appID := integrationLogStore(t)
	const runID = "00000000-0000-4000-8000-000000000001"
	deploymentID := p.DeploymentID
	if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
		RunID: runID, AppID: appID, ReplicaIndex: p.Index,
		DeploymentID: &deploymentID, AppVersion: p.AppVersion, Tier: p.Tier,
		Status: "starting", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create immutable log run: %v", err)
	}
	externalLogs, err := json.Marshal(ep.ExternalLogs)
	if err != nil {
		t.Fatalf("encode external log handoff: %v", err)
	}
	if err := store.MarkAppLogRunRunning(runID, ep.Provider, string(externalLogs)); err != nil {
		t.Fatalf("persist external log handoff: %v", err)
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
	stopped = true

	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer verifyCancel()
	described, err := client.DescribeTasks(verifyCtx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cfg.Cluster), Tasks: []string{taskARN},
	})
	if err != nil {
		t.Fatalf("DescribeTasks after stop: %v", err)
	}
	if len(described.Failures) > 0 || len(described.Tasks) != 1 ||
		aws.ToString(described.Tasks[0].TaskArn) != taskARN ||
		aws.ToString(described.Tasks[0].LastStatus) != "STOPPED" {
		t.Fatalf("stopped task lookup = tasks %+v failures %+v", described.Tasks, described.Failures)
	}

	finishedAt := time.Now()
	if err := store.FinishAppLogRun(runID, "stopped", finishedAt, false); err != nil {
		t.Fatalf("finish immutable log run: %v", err)
	}
	persisted, err := store.GetAppLogRun(appID, runID)
	if err != nil {
		t.Fatalf("read stopped immutable log run: %v", err)
	}
	var persistedHandoff process.ExternalLogs
	if err := json.Unmarshal([]byte(persisted.ExternalLogs), &persistedHandoff); err != nil {
		t.Fatalf("decode persisted external log handoff: %v", err)
	}
	if persisted.Status != "stopped" || persisted.FinishedAt == nil || persisted.Provider != Provider ||
		persisted.ReplicaIndex != p.Index || persisted.DeploymentID == nil ||
		*persisted.DeploymentID != p.DeploymentID ||
		persistedHandoff != *ep.ExternalLogs {
		t.Fatalf("stopped immutable run = %+v handoff=%+v, want original handoff %+v",
			persisted, persistedHandoff, ep.ExternalLogs)
	}
}
