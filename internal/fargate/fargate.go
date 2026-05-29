// Package fargate provides a process.Runtime backend that runs each app replica
// as an AWS ECS task on the Fargate launch type. It plugs into the same tier and
// autoscale machinery as the native, docker, and remote_docker runtimes: the
// control plane's autoscale controller decides the replica count and the Manager
// drives this runtime's Start/StopReplica primitives, which translate to ECS
// RunTask / StopTask calls. Each replica is one Fargate task; the proxy routes to
// the task's awsvpc private IP, so the control plane must run inside (or peered
// with) the task's VPC.
//
// All ECS interaction goes through the narrow ECSClient seam, which the AWS SDK's
// *ecs.Client satisfies directly, so the runtime is fully unit-testable with a
// fake client and never needs real AWS credentials in tests.
//
// # Runner-image contract
//
// ShinyHub deploys app bundles, not images, so (like the remote_docker backend,
// which relies on a worker agent) a Fargate tier relies on the configured task
// definition's container being a ShinyHub-compatible "runner" image. The control
// plane supplies, as a container override, the launch command plus these
// environment variables identifying the bundle to run:
//
//	SHINYHUB_SLUG, SHINYHUB_REPLICA_INDEX, SHINYHUB_CONTENT_DIGEST,
//	SHINYHUB_DEPLOYMENT_ID, SHINYHUB_APP_VERSION
//
// The runner image is responsible for fetching the bundle for
// SHINYHUB_CONTENT_DIGEST from the operator-configured control-plane URL, placing
// it at the working directory the launch command expects, and exec'ing the
// command. The control plane never ships the bundle bytes into the task; it only
// names the bundle. This keeps the AWS-facing surface here minimal and leaves
// image build, IAM, and bundle distribution to the operator's deployment.
package fargate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rvben/shinyhub/internal/process"
)

// Provider labels replicas started on a Fargate tier.
const Provider = "fargate"

// WorkerID is the stable, slash-free identity stamped on every Fargate replica.
// Fargate has no per-host workers, but the recovery path keys replica adoption on
// a non-empty worker id and encodes the run handle as "<workerID>/<task-arn>", so
// a constant identity is required. It is slash-free so the handle round-trips
// (the task ARN itself contains slashes). Inventory reconciliation is already
// scoped per tier, so a single constant is unambiguous across clusters.
const WorkerID = "fargate"

// Label keys stamped as ECS task tags so recovery can reconcile a replica row
// against a live task without any local handle. They mirror the docker/remote
// label scheme so the lifecycle reconciler treats every backend identically.
const (
	tagSlug         = "shinyhub.slug"
	tagReplicaIndex = "shinyhub.replica_index"
	tagDeploymentID = "shinyhub.deployment_id"
	tagTier         = "shinyhub.tier"
	tagAppVersion   = "shinyhub.app_version"
	tagManagedBy    = "shinyhub.managed_by"
	// tagPort records the port the app binds inside the task so recovery can
	// rebuild the full route URL (http://<eni-ip>:<port>) from the task alone.
	// Start has the port directly; Inventory recovers it only from this tag.
	tagPort = "shinyhub.port"
)

// startedBy marks every task this control plane launches so Inventory can list
// only ShinyHub-managed tasks on a shared cluster.
const startedBy = "shinyhub"

// ECSClient is the subset of the AWS ECS API the runtime needs. The SDK's
// *ecs.Client satisfies it directly; tests supply a fake.
type ECSClient interface {
	RunTask(ctx context.Context, in *ecs.RunTaskInput, optFns ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
	StopTask(ctx context.Context, in *ecs.StopTaskInput, optFns ...func(*ecs.Options)) (*ecs.StopTaskOutput, error)
	DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, optFns ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	ListTasks(ctx context.Context, in *ecs.ListTasksInput, optFns ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
}

// Config holds the resolved Fargate settings for one tier. The same struct backs
// the YAML config and the runtime constructor.
type Config struct {
	// Cluster is the ECS cluster short name or full ARN tasks run on.
	Cluster string
	// TaskDefinition is the family, family:revision, or full ARN of the task
	// definition to run. It must declare a container named ContainerName that
	// runs the ShinyHub app; this runtime applies the per-replica command, env,
	// and resource limits as container overrides on top of it.
	TaskDefinition string
	// ContainerName is the container within TaskDefinition that command/env/limit
	// overrides target. It must match the container definition's name.
	ContainerName string
	// Subnets are the awsvpc subnet IDs tasks attach to. At least one is required.
	Subnets []string
	// SecurityGroups are the awsvpc security group IDs applied to each task ENI.
	SecurityGroups []string
	// AssignPublicIP maps to the awsvpc assignPublicIp setting. Set it for tasks
	// in public subnets without a NAT gateway; leave false for private subnets.
	AssignPublicIP bool
	// PlatformVersion pins the Fargate platform version (e.g. "1.4.0"). Empty
	// uses the ECS default ("LATEST").
	PlatformVersion string
	// RouteViaPublicIP makes the proxy route to each task's public IPv4 address
	// instead of its awsvpc private IP. This exists for a control plane that runs
	// OUTSIDE the task VPC (development and integration testing): production
	// deployments run the control plane inside or peered with the VPC and route
	// over private IPs, which is the default. When set, AssignPublicIP must also
	// be true and an EC2Client must be supplied; the public IP is resolved via
	// EC2 DescribeNetworkInterfaces on the task ENI. Routing app traffic over the
	// public internet has no transport security, so restrict task security groups
	// to the control plane's address.
	RouteViaPublicIP bool
}

// EC2Client is the subset of the AWS EC2 API needed to resolve a task ENI's
// public IP when RouteViaPublicIP is set. The SDK's *ec2.Client satisfies it.
type EC2Client interface {
	DescribeNetworkInterfaces(ctx context.Context, in *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error)
}

// Runtime implements process.Runtime (and process.ReplicaInventory) by running
// each replica as a Fargate task. It is bound to one cluster/tier; register one
// Runtime per fargate tier.
type Runtime struct {
	client ECSClient
	ec2    EC2Client // non-nil only when cfg.RouteViaPublicIP
	cfg    Config
	log    *slog.Logger

	// pollInterval is the delay between DescribeTasks polls while waiting for a
	// task's network interface (Start) or terminal state (Wait/RunOnce).
	pollInterval time.Duration
	// startTimeout bounds how long Start waits for a task to acquire a routable
	// private IP before giving up and stopping the half-started task.
	startTimeout time.Duration
}

// Option customizes a Runtime. Defaults suit production; tests shrink the poll
// timings.
type Option func(*Runtime)

// WithPollInterval sets the DescribeTasks poll delay used while waiting for a
// task to become routable or to reach a terminal state.
func WithPollInterval(d time.Duration) Option {
	return func(r *Runtime) { r.pollInterval = d }
}

// WithStartTimeout bounds how long Start waits for a task's private IP.
func WithStartTimeout(d time.Duration) Option {
	return func(r *Runtime) { r.startTimeout = d }
}

// WithEC2Client supplies the EC2 client used to resolve task public IPs when
// Config.RouteViaPublicIP is set. It is required in that mode and ignored
// otherwise.
func WithEC2Client(c EC2Client) Option {
	return func(r *Runtime) { r.ec2 = c }
}

// New builds a Fargate runtime against the given client and config. log may be
// nil, in which case the default logger is used.
func New(client ECSClient, cfg Config, log *slog.Logger, opts ...Option) *Runtime {
	if log == nil {
		log = slog.Default()
	}
	r := &Runtime{
		client:       client,
		cfg:          cfg,
		log:          log,
		pollInterval: 2 * time.Second,
		startTimeout: 90 * time.Second,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// HostPreparesDeps reports false: the task image prepares its own dependencies;
// the control-plane host must not run uv/renv for a Fargate replica.
func (r *Runtime) HostPreparesDeps() bool { return false }

// AppBindHost reports "0.0.0.0": the app binds inside the task's own network
// namespace, and the proxy reaches it on the task ENI's private IP.
func (r *Runtime) AppBindHost() string { return "0.0.0.0" }

// HostProvidesAppData reports false: the task provisions its own app-data; the
// control-plane host must not create directories or strip-then-dispatch paths.
func (r *Runtime) HostProvidesAppData() bool { return false }

// encodeHandle joins the constant worker identity and the task ARN into the
// "<workerID>/<task-arn>" form the recovery path also produces, so a handle
// minted by Start and one reconstructed during recovery are interchangeable.
func encodeHandle(taskARN string) string {
	return WorkerID + "/" + taskARN
}

// decodeHandle extracts the task ARN from an opaque handle. It accepts both the
// "<workerID>/<task-arn>" form and a bare task ARN, so a handle minted by Start,
// rebuilt by recovery, or (defensively) passed raw all resolve correctly. The
// split keeps the task ARN intact even though it contains slashes.
func decodeHandle(h string) (string, error) {
	if h == "" {
		return "", fmt.Errorf("fargate: empty run handle")
	}
	if prefix := WorkerID + "/"; strings.HasPrefix(h, prefix) {
		arn := strings.TrimPrefix(h, prefix)
		if arn == "" {
			return "", fmt.Errorf("fargate: handle %q has no task arn", h)
		}
		return arn, nil
	}
	return h, nil
}

func (r *Runtime) networkConfig() *ecstypes.NetworkConfiguration {
	assign := ecstypes.AssignPublicIpDisabled
	if r.cfg.AssignPublicIP {
		assign = ecstypes.AssignPublicIpEnabled
	}
	return &ecstypes.NetworkConfiguration{
		AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
			Subnets:        r.cfg.Subnets,
			SecurityGroups: r.cfg.SecurityGroups,
			AssignPublicIp: assign,
		},
	}
}

// replicaEnv builds the container environment: the app's own env vars first,
// then the SHINYHUB_* platform vars the runner image needs to fetch and identify
// the deployed bundle. The bundle is delivered to the task by the runner image
// (see the package doc's runner-image contract), which resolves it from
// SHINYHUB_CONTENT_DIGEST; the control plane only supplies the identity, never
// the bytes. Platform vars are appended last so they are authoritative for their
// reserved SHINYHUB_ prefix even if an app env var collides.
func replicaEnv(p process.StartParams) []ecstypes.KeyValuePair {
	env := make([]ecstypes.KeyValuePair, 0, len(p.Env)+5)
	for _, kv := range p.Env {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			env = append(env, ecstypes.KeyValuePair{
				Name:  aws.String(kv[:idx]),
				Value: aws.String(kv[idx+1:]),
			})
		}
	}
	add := func(k, v string) {
		if v != "" {
			env = append(env, ecstypes.KeyValuePair{Name: aws.String(k), Value: aws.String(v)})
		}
	}
	add("SHINYHUB_SLUG", p.Slug)
	add("SHINYHUB_REPLICA_INDEX", strconv.Itoa(p.Index))
	add("SHINYHUB_CONTENT_DIGEST", p.ContentDigest)
	if p.DeploymentID > 0 {
		add("SHINYHUB_DEPLOYMENT_ID", strconv.FormatInt(p.DeploymentID, 10))
	}
	add("SHINYHUB_APP_VERSION", p.AppVersion)
	return env
}

// containerOverride builds the per-replica override: the launch command, the
// resolved environment, and the optional CPU/memory limits. CPUQuotaPercent is
// expressed in ECS CPU units (1024 = one vCPU), so 100% maps to 1024 units.
func containerOverride(name string, p process.StartParams) ecstypes.ContainerOverride {
	ov := ecstypes.ContainerOverride{
		Name:        aws.String(name),
		Command:     p.Command,
		Environment: replicaEnv(p),
	}
	if p.MemoryLimitMB > 0 {
		ov.Memory = aws.Int32(int32(p.MemoryLimitMB))
	}
	if p.CPUQuotaPercent > 0 {
		ov.Cpu = aws.Int32(int32(p.CPUQuotaPercent) * 1024 / 100)
	}
	return ov
}

func (r *Runtime) tags(p process.StartParams) []ecstypes.Tag {
	tags := []ecstypes.Tag{
		{Key: aws.String(tagManagedBy), Value: aws.String(startedBy)},
		{Key: aws.String(tagSlug), Value: aws.String(p.Slug)},
		{Key: aws.String(tagReplicaIndex), Value: aws.String(strconv.Itoa(p.Index))},
		{Key: aws.String(tagTier), Value: aws.String(p.Tier)},
		{Key: aws.String(tagPort), Value: aws.String(strconv.Itoa(p.Port))},
	}
	if p.DeploymentID > 0 {
		tags = append(tags, ecstypes.Tag{Key: aws.String(tagDeploymentID), Value: aws.String(strconv.FormatInt(p.DeploymentID, 10))})
	}
	if p.AppVersion != "" {
		tags = append(tags, ecstypes.Tag{Key: aws.String(tagAppVersion), Value: aws.String(p.AppVersion)})
	}
	return tags
}

func (r *Runtime) runTaskInput(p process.StartParams) *ecs.RunTaskInput {
	return &ecs.RunTaskInput{
		Cluster:        aws.String(r.cfg.Cluster),
		TaskDefinition: aws.String(r.cfg.TaskDefinition),
		LaunchType:     ecstypes.LaunchTypeFargate,
		Count:          aws.Int32(1),
		StartedBy:      aws.String(startedBy),
		// The explicit Tags below are the sole source of truth for reconciliation.
		// EnableECSManagedTags adds the cluster's aws:-prefixed managed tags, which
		// never collide with our shinyhub.* keys. Task-definition tag propagation is
		// deliberately not enabled: a task definition that happened to carry a
		// shinyhub.* tag would make RunTask fail with a duplicate-key error.
		EnableECSManagedTags: true,
		PlatformVersion:      optString(r.cfg.PlatformVersion),
		NetworkConfiguration: r.networkConfig(),
		Overrides: &ecstypes.TaskOverride{
			ContainerOverrides: []ecstypes.ContainerOverride{containerOverride(r.cfg.ContainerName, p)},
		},
		Tags: r.tags(p),
	}
}

// Start launches one Fargate task for the replica and waits until it acquires a
// routable private IP, returning the route URL and an opaque handle carrying the
// task ARN. A task that fails to schedule (a RunTask failure entry) or never
// becomes routable within startTimeout is stopped before returning the error, so
// a failed Start never leaks a running task.
func (r *Runtime) Start(ctx context.Context, p process.StartParams, logWriter io.Writer) (process.ReplicaEndpoint, error) {
	out, err := r.client.RunTask(ctx, r.runTaskInput(p))
	if err != nil {
		return process.ReplicaEndpoint{}, fmt.Errorf("fargate: run task: %w", err)
	}
	if len(out.Failures) > 0 {
		f := out.Failures[0]
		return process.ReplicaEndpoint{}, fmt.Errorf("fargate: run task failed: %s: %s",
			aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 || out.Tasks[0].TaskArn == nil {
		return process.ReplicaEndpoint{}, fmt.Errorf("fargate: run task returned no task")
	}
	taskARN := aws.ToString(out.Tasks[0].TaskArn)

	ip, err := r.waitForIP(ctx, taskARN)
	if err != nil {
		// Best-effort teardown so a task that never became routable does not leak.
		r.stop(context.WithoutCancel(ctx), taskARN, "shinyhub: start failed to acquire ip")
		return process.ReplicaEndpoint{}, err
	}
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://%s:%d", ip, p.Port),
		Provider: Provider,
		WorkerID: WorkerID,
		Handle:   process.RunHandle{ContainerID: encodeHandle(taskARN)},
	}, nil
}

// waitForIP polls DescribeTasks until the task exposes a routable IPv4 address or
// reaches a terminal STOPPED state (a fast-failing task), bounded by
// startTimeout and ctx. The routable address is the private IP by default, or
// the ENI's public IP when RouteViaPublicIP is set.
func (r *Runtime) waitForIP(ctx context.Context, taskARN string) (string, error) {
	deadline := time.Now().Add(r.startTimeout)
	for {
		task, err := r.describeTask(ctx, taskARN)
		if err != nil {
			return "", err
		}
		// DescribeTasks is eventually consistent: immediately after RunTask it can
		// briefly return no task for a freshly created ARN. Treat that as "not
		// visible yet" and keep polling until the task appears, stops, or the
		// start timeout expires, rather than failing an otherwise healthy start.
		if task != nil {
			ip, err := r.routeIP(ctx, *task)
			if err != nil {
				return "", err
			}
			if ip != "" {
				return ip, nil
			}
			if aws.ToString(task.LastStatus) == "STOPPED" {
				return "", fmt.Errorf("fargate: task %s stopped before becoming routable: %s",
					taskARN, aws.ToString(task.StoppedReason))
			}
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("fargate: task %s did not acquire an ip within %s", taskARN, r.startTimeout)
		}
		if err := r.sleep(ctx); err != nil {
			return "", err
		}
	}
}

// routeIP returns the address the proxy should dial for a task: the awsvpc
// private IP by default, or the ENI's public IP when RouteViaPublicIP is set. It
// returns "" (no error) when the address is not yet assigned, so callers keep
// polling. A public-IP lookup needs the task's ENI id and one EC2 call.
func (r *Runtime) routeIP(ctx context.Context, task ecstypes.Task) (string, error) {
	if !r.cfg.RouteViaPublicIP {
		return taskPrivateIP(task), nil
	}
	eniID := taskENIID(task)
	if eniID == "" {
		return "", nil // ENI not attached yet
	}
	if r.ec2 == nil {
		return "", fmt.Errorf("fargate: RouteViaPublicIP set but no EC2 client configured")
	}
	out, err := r.ec2.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		NetworkInterfaceIds: []string{eniID},
	})
	if err != nil {
		return "", fmt.Errorf("fargate: describe network interface %s: %w", eniID, err)
	}
	for _, ni := range out.NetworkInterfaces {
		if ni.Association != nil {
			if ip := aws.ToString(ni.Association.PublicIp); ip != "" {
				return ip, nil
			}
		}
	}
	return "", nil // public IP not associated yet
}

// Signal maps a stop-intent signal to ECS StopTask. ECS has no API to deliver an
// arbitrary signal to a Fargate container, so SIGTERM and SIGKILL both request a
// graceful StopTask (which itself sends SIGTERM then SIGKILL after the task's
// stopTimeout); any other signal is a no-op the Manager never relies on for
// Fargate.
func (r *Runtime) Signal(handle process.RunHandle, sig syscall.Signal) error {
	if sig != syscall.SIGTERM && sig != syscall.SIGKILL {
		return nil
	}
	taskARN, err := decodeHandle(handle.ContainerID)
	if err != nil {
		return err
	}
	reason := "shinyhub: replica stop"
	if sig == syscall.SIGKILL {
		reason = "shinyhub: replica kill"
	}
	return r.stop(context.Background(), taskARN, reason)
}

func (r *Runtime) stop(ctx context.Context, taskARN, reason string) error {
	_, err := r.client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(r.cfg.Cluster),
		Task:    aws.String(taskARN),
		Reason:  aws.String(reason),
	})
	if err != nil {
		return fmt.Errorf("fargate: stop task %s: %w", taskARN, err)
	}
	return nil
}

// Wait blocks until the task reaches STOPPED or ctx is cancelled. It reports only
// liveness; exit codes are surfaced by RunOnce, not here.
func (r *Runtime) Wait(ctx context.Context, handle process.RunHandle) error {
	taskARN, err := decodeHandle(handle.ContainerID)
	if err != nil {
		return err
	}
	for {
		task, err := r.describeTask(ctx, taskARN)
		if err != nil {
			return err
		}
		if task == nil || aws.ToString(task.LastStatus) == "STOPPED" {
			return nil
		}
		if err := r.sleep(ctx); err != nil {
			return err
		}
	}
}

// Stats reports zero CPU/RSS with a nil error. Fargate task metrics are
// published to CloudWatch Container Insights, not reachable synchronously from
// the control plane, so per-replica live figures are not collected here. The nil
// error is deliberate: the status endpoint treats a sampler error as a dead
// replica, so returning an error would misreport a healthy Fargate task as
// stopped. The Manager already tracks task liveness; only the CPU/RSS numbers are
// absent (shown as zero).
func (r *Runtime) Stats(ctx context.Context, handle process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}

// RunOnce launches a one-shot task, blocks until it stops, and returns its exit
// info. On ctx cancel it stops the task and reports a signalled exit, matching
// the Runtime contract's SIGTERM-then-kill expectation (ECS StopTask performs the
// term/kill escalation itself).
func (r *Runtime) RunOnce(ctx context.Context, p process.StartParams, logWriter io.Writer) (process.ExitInfo, error) {
	out, err := r.client.RunTask(ctx, r.runTaskInput(p))
	if err != nil {
		return process.ExitInfo{}, fmt.Errorf("fargate: run task: %w", err)
	}
	if len(out.Failures) > 0 {
		f := out.Failures[0]
		return process.ExitInfo{}, fmt.Errorf("fargate: run task failed: %s: %s",
			aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 || out.Tasks[0].TaskArn == nil {
		return process.ExitInfo{}, fmt.Errorf("fargate: run task returned no task")
	}
	taskARN := aws.ToString(out.Tasks[0].TaskArn)

	for {
		task, err := r.describeTask(ctx, taskARN)
		if err != nil {
			// On cancellation, stop the task and report a signalled exit.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				r.stop(context.WithoutCancel(ctx), taskARN, "shinyhub: run-once cancelled")
				return process.ExitInfo{Code: -1, Signaled: true}, nil
			}
			return process.ExitInfo{}, err
		}
		if task != nil && aws.ToString(task.LastStatus) == "STOPPED" {
			return exitInfo(*task), nil
		}
		if err := r.sleep(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				r.stop(context.WithoutCancel(ctx), taskARN, "shinyhub: run-once cancelled")
				return process.ExitInfo{Code: -1, Signaled: true}, nil
			}
			return process.ExitInfo{}, err
		}
	}
}

// Inventory enumerates this cluster's ShinyHub-managed tasks for recovery,
// returning one item per task keyed by the shinyhub.* tags. A task that has not
// yet acquired an IP is reported with an empty URL and is not adopted until a
// later scan; a task without the slug tag is skipped.
func (r *Runtime) Inventory(ctx context.Context) ([]process.InventoryItem, error) {
	arns, err := r.listManagedTaskARNs(ctx)
	if err != nil {
		return nil, err
	}
	if len(arns) == 0 {
		return nil, nil
	}
	items := make([]process.InventoryItem, 0, len(arns))
	// DescribeTasks accepts up to 100 task ARNs per call.
	for start := 0; start < len(arns); start += 100 {
		end := start + 100
		if end > len(arns) {
			end = len(arns)
		}
		out, err := r.client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(r.cfg.Cluster),
			Tasks:   arns[start:end],
			Include: []ecstypes.TaskField{ecstypes.TaskFieldTags},
		})
		if err != nil {
			return nil, fmt.Errorf("fargate: describe tasks: %w", err)
		}
		for _, task := range out.Tasks {
			labels := tagsToLabels(task.Tags)
			if labels[tagSlug] == "" {
				continue
			}
			// Rebuild the full route URL (http://<ip>:<port>) the proxy registered
			// at Start time. The port comes from the tag stamped at RunTask; a task
			// missing the tag (e.g. one started by an older build) falls back to a
			// portless URL rather than being dropped from inventory.
			url := ""
			ip, err := r.routeIP(ctx, task)
			if err != nil {
				return nil, err
			}
			if ip != "" {
				if port := labels[tagPort]; port != "" {
					url = "http://" + ip + ":" + port
				} else {
					url = "http://" + ip
				}
			}
			items = append(items, process.InventoryItem{
				ContainerID: aws.ToString(task.TaskArn),
				Labels:      labels,
				Running:     aws.ToString(task.LastStatus) == "RUNNING",
				URL:         url,
				WorkerID:    WorkerID,
			})
		}
	}
	return items, nil
}

func (r *Runtime) listManagedTaskARNs(ctx context.Context) ([]string, error) {
	var arns []string
	var next *string
	for {
		out, err := r.client.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster:   aws.String(r.cfg.Cluster),
			StartedBy: aws.String(startedBy),
			NextToken: next,
		})
		if err != nil {
			return nil, fmt.Errorf("fargate: list tasks: %w", err)
		}
		arns = append(arns, out.TaskArns...)
		if out.NextToken == nil || *out.NextToken == "" {
			return arns, nil
		}
		next = out.NextToken
	}
}

// describeTask fetches the current state of one task. It returns nil (no error)
// when the task is not yet visible (ECS eventual consistency: a MISSING failure
// or an empty Tasks list on a freshly created ARN); callers keep polling in that
// case. Any other non-nil Failure reason is a permanent scheduling error and is
// returned as a hard wrapped error so waitForIP/Wait/RunOnce fail fast instead
// of polling to timeout.
func (r *Runtime) describeTask(ctx context.Context, taskARN string) (*ecstypes.Task, error) {
	out, err := r.client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(r.cfg.Cluster),
		Tasks:   []string{taskARN},
	})
	if err != nil {
		return nil, fmt.Errorf("fargate: describe task %s: %w", taskARN, err)
	}
	// Check for scheduling failures before checking Tasks. ECS returns Failures
	// instead of (or alongside) Tasks when it cannot place or locate the task.
	for _, f := range out.Failures {
		if aws.ToString(f.Reason) == "MISSING" {
			// MISSING means the task record is not yet visible due to eventual
			// consistency immediately after RunTask. Treat it as "not visible yet":
			// return nil so the caller keeps polling.
			return nil, nil
		}
		// Any other Reason (RESOURCE, ATTRIBUTE, ACCESS_DENIED, ...) is a
		// permanent failure that will not resolve by polling.
		return nil, fmt.Errorf("fargate: task %s failed: reason=%s detail=%s",
			taskARN, aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 {
		return nil, nil
	}
	return &out.Tasks[0], nil
}

// sleep waits one poll interval or returns ctx.Err() on cancellation.
func (r *Runtime) sleep(ctx context.Context) error {
	t := time.NewTimer(r.pollInterval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// taskPrivateIP extracts the awsvpc private IPv4 address from a task, preferring
// the container network interface and falling back to the ENI attachment detail.
func taskPrivateIP(task ecstypes.Task) string {
	for _, c := range task.Containers {
		for _, ni := range c.NetworkInterfaces {
			if ip := aws.ToString(ni.PrivateIpv4Address); ip != "" {
				return ip
			}
		}
	}
	for _, a := range task.Attachments {
		if aws.ToString(a.Type) != "ElasticNetworkInterface" {
			continue
		}
		for _, d := range a.Details {
			if aws.ToString(d.Name) == "privateIPv4Address" {
				if ip := aws.ToString(d.Value); ip != "" {
					return ip
				}
			}
		}
	}
	return ""
}

// taskENIID returns the awsvpc Elastic Network Interface id from a task's
// attachments, or "" if the ENI has not attached yet. Used to resolve the public
// IP via EC2 when RouteViaPublicIP is set.
func taskENIID(task ecstypes.Task) string {
	for _, a := range task.Attachments {
		if aws.ToString(a.Type) != "ElasticNetworkInterface" {
			continue
		}
		for _, d := range a.Details {
			if aws.ToString(d.Name) == "networkInterfaceId" {
				if id := aws.ToString(d.Value); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

func exitInfo(task ecstypes.Task) process.ExitInfo {
	for _, c := range task.Containers {
		if c.ExitCode != nil {
			return process.ExitInfo{Code: int(*c.ExitCode)}
		}
	}
	// No exit code recorded (e.g. the task was stopped before the container ran):
	// report a signalled, non-zero exit so callers treat it as a failure.
	return process.ExitInfo{Code: -1, Signaled: true}
}

func tagsToLabels(tags []ecstypes.Tag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		out[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return aws.String(s)
}

var (
	_ process.Runtime          = (*Runtime)(nil)
	_ process.ReplicaInventory = (*Runtime)(nil)
)
