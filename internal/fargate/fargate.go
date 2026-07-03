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
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rvben/shinyhub/internal/bundletoken"
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

// EC2WorkerID is the stable worker identity stamped on every ECS EC2 replica.
// It is slash-free for the same reason as WorkerID. Existing Fargate handles
// remain "fargate/<arn>"; EC2 handles are "ecs-ec2/<arn>".
const EC2WorkerID = "ecs-ec2"

// IsECSManagedWorkerID reports whether id is one of the synthetic constant
// worker identities used for ECS-managed replicas (either Fargate or EC2
// launch type). These identities never correspond to a DB worker row, so
// callers that need to distinguish ECS replicas from remote-docker-agent
// replicas use this rather than comparing against WorkerID alone.
func IsECSManagedWorkerID(id string) bool {
	return id == WorkerID || id == EC2WorkerID
}

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
	// DescribeTaskDefinition fetches the operator's base task definition so the
	// runtime can clone it into a per-app revision carrying a secrets block.
	DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, optFns ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
	// RegisterTaskDefinition registers a per-app task-definition revision whose
	// container carries the secrets block (ARNs) for the app's secret env vars.
	RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, optFns ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error)
	// ListTaskDefinitions enumerates a family's revisions so app-delete cleanup
	// can deregister each one (a family is not deleted, only its revisions).
	ListTaskDefinitions(ctx context.Context, in *ecs.ListTaskDefinitionsInput, optFns ...func(*ecs.Options)) (*ecs.ListTaskDefinitionsOutput, error)
	// DeregisterTaskDefinition retires a per-app task-definition revision when an
	// app is deleted, so revisions do not accumulate unbounded.
	DeregisterTaskDefinition(ctx context.Context, in *ecs.DeregisterTaskDefinitionInput, optFns ...func(*ecs.Options)) (*ecs.DeregisterTaskDefinitionOutput, error)
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

	// TaskCPUUnits is the ECS task-level CPU allocation in CPU units (1024 = 1 vCPU).
	// When non-zero, buildContainerOverride clamps per-app CPUQuotaPercent so the
	// container CPU units never exceed the task ceiling.
	TaskCPUUnits int32

	// TaskMemoryMB is the ECS task-level memory allocation in MiB. When non-zero,
	// buildContainerOverride clamps per-app MemoryLimitMB so the container memory
	// never exceeds the task ceiling.
	TaskMemoryMB int32

	// ControlPlaneURL is the URL tasks use to fetch their bundle. Set to the
	// value of runtime.fargate.control_plane_url.
	ControlPlaneURL string

	// BundleTokenTTL is the TTL for minted bundle capability tokens. Set from
	// runtime.fargate.bundle_token_ttl (default 10 minutes).
	BundleTokenTTL time.Duration

	// BundleTokenKey is the 32-byte HKDF-derived key used to mint bundle tokens.
	// Derived once at startup with deriveBundleTokenKey(authSecret) and injected
	// by buildFargateRuntime. Must not be nil when ControlPlaneURL is set.
	BundleTokenKey []byte

	// DurableData asserts (independently of S3Files) that this tier has a durable
	// app-data backend, e.g. a volume the operator attached to the base task
	// definition. TierHasDurableData is true when this is set OR S3Files is
	// configured. When neither holds, task storage is ephemeral scratch: app-data
	// is lost on restart/hibernation and not shared across replicas.
	DurableData bool

	// S3Files, when configured (FileSystemArn set), is the managed durable-data
	// backend: the runtime injects a per-app S3 Files volume + mount point into
	// each app's task-definition revision so {data_dir} resolves onto durable,
	// replica-shared storage. Configuring it makes the tier durable.
	S3Files S3FilesMount

	// LaunchType is the ECS launch type for tasks on this tier. Use
	// ecstypes.LaunchTypeFargate (default) for Fargate tasks or
	// ecstypes.LaunchTypeEc2 for EC2 tasks. The zero value defaults to
	// LaunchTypeFargate in New.
	LaunchType ecstypes.LaunchType

	// SecretNamePrefix namespaces the store names of an app's secret env vars
	// (see SecretName). It should be unique per ShinyHub installation so two
	// installs sharing one AWS account never collide. It is also sanitized into
	// the per-app task-definition family. Only used when a SecretsStore is wired
	// (WithSecretsStore); empty is fine when no app on the tier has secrets.
	SecretNamePrefix string
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
	// secrets, when non-nil, routes an app's secret env vars through a per-app
	// task-definition revision (containerDefinitions[].secrets -> store ARNs)
	// instead of plaintext task overrides, keeping them out of ecs:DescribeTasks.
	// Nil disables the feature: secret env stays plaintext (the Phase 1 behavior).
	secrets SecretsStore
	cfg     Config
	log     *slog.Logger
	// metrics records AWS operation outcomes. Never nil after New(); defaults to
	// noopFargateMetrics{} so callers need no nil guard.
	metrics FargateMetrics

	// workerID is the stable synthetic worker identity for this runtime instance.
	// It is "fargate" for Fargate launch type and "ecs-ec2" for EC2, derived once
	// in New from cfg.LaunchType so every handle, inventory item, and sweep
	// operation uses a consistent value.
	workerID string

	// pollInterval is the delay between DescribeTasks polls while waiting for a
	// task's network interface (Start) or terminal state (Wait/RunOnce).
	pollInterval time.Duration
	// startTimeout bounds how long Start waits for a task to acquire a routable
	// private IP before giving up and stopping the half-started task.
	startTimeout time.Duration

	// appSync serializes secret sync per app id so concurrent replica starts of
	// one app do not each write the store and register a revision.
	appSync keyedMutex
	// syncMu guards syncKeys.
	syncMu sync.Mutex
	// syncKeys caches, per app id, the sync key last written to the store and
	// registered as a task-def revision. The key combines the secret-set hash
	// and the resolved base task-definition identity, so a start reuses the
	// existing revision (skipping the store write and registration) only when
	// BOTH the secrets and the base are unchanged. A changed secret value or a
	// new base revision forces a re-sync, avoiding per-start churn without
	// stranding an app on a stale base task definition.
	syncKeys map[int64]string
}

// FargateMetrics records AWS operation outcomes for the Fargate runtime. A nil
// recorder is a no-op; the no-op default (noopFargateMetrics) satisfies the
// interface so callers need no nil guard. Wire to the Prometheus registry via
// SetMetrics (mirrors lifecycle.Watcher.SetMetrics / lifecycle.MetricsRecorder).
type FargateMetrics interface {
	RecordRunTask(result string) // result: "ok" | "error"
	RecordWaitIPTimeout()
	RecordStopTask(result string) // result: "ok" | "error"
	RecordInventoryError()
	ObserveRunTaskLatency(seconds float64)
}

// noopFargateMetrics is the zero-value no-op implementation.
type noopFargateMetrics struct{}

func (noopFargateMetrics) RecordRunTask(_ string)          {}
func (noopFargateMetrics) RecordWaitIPTimeout()            {}
func (noopFargateMetrics) RecordStopTask(_ string)         {}
func (noopFargateMetrics) RecordInventoryError()           {}
func (noopFargateMetrics) ObserveRunTaskLatency(_ float64) {}

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

// WithSecretsStore enables out-of-band secret injection: an app's secret env
// vars are written to the store and referenced by ARN from a per-app
// task-definition revision's secrets block, instead of appearing as plaintext
// task overrides. Nil leaves the feature disabled (secret env stays plaintext).
func WithSecretsStore(s SecretsStore) Option {
	return func(r *Runtime) { r.secrets = s }
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
		metrics:      noopFargateMetrics{},
		pollInterval: 2 * time.Second,
		startTimeout: 90 * time.Second,
		syncKeys:     make(map[int64]string),
	}
	for _, o := range opts {
		o(r)
	}
	if r.cfg.LaunchType == "" {
		r.cfg.LaunchType = ecstypes.LaunchTypeFargate
	}
	if r.cfg.LaunchType == ecstypes.LaunchTypeEc2 {
		r.workerID = EC2WorkerID
	} else {
		r.workerID = WorkerID
	}
	if r.cfg.RouteViaPublicIP {
		r.log.Warn("fargate: route_via_public_ip is enabled - app traffic is routed over the public internet without transport security; ensure control_plane_url uses https:// so bundle tokens are not transmitted in plaintext",
			"cluster", r.cfg.Cluster,
		)
	}
	return r
}

// SetMetrics wires a recorder for Fargate AWS operation metrics. Called once at
// startup before Start; nil resets to the no-op default.
func (r *Runtime) SetMetrics(m FargateMetrics) {
	if m == nil {
		r.metrics = noopFargateMetrics{}
		return
	}
	r.metrics = m
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

// TierHasDurableData reports whether app-data on this Fargate tier survives task
// restart/hibernation and is shared across replicas. Bare Fargate task storage
// is ephemeral scratch, so this is false unless a managed S3 Files backend is
// configured or the operator asserts durability (Config.DurableData) for a
// manually attached volume.
func (r *Runtime) TierHasDurableData() bool {
	return r.cfg.DurableData || r.cfg.S3Files.Configured()
}

// encodeHandle joins the runtime's worker identity and the task ARN into the
// "<workerID>/<task-arn>" form that recovery also produces, so a handle minted
// by Start and one reconstructed during recovery are interchangeable.
func (r *Runtime) encodeHandle(taskARN string) string {
	return r.workerID + "/" + taskARN
}

// decodeHandle extracts the task ARN from an opaque handle. It accepts the
// "<workerID>/<task-arn>" form, a bare task ARN, and rejects handles that
// carry a different ECS-managed workerID prefix (cross-runtime guard). The
// split keeps the task ARN intact even though it contains slashes.
func (r *Runtime) decodeHandle(h string) (string, error) {
	if h == "" {
		return "", fmt.Errorf("fargate: empty run handle")
	}
	ownPrefix := r.workerID + "/"
	if strings.HasPrefix(h, ownPrefix) {
		arn := strings.TrimPrefix(h, ownPrefix)
		if arn == "" {
			return "", fmt.Errorf("fargate: handle %q has no task arn", h)
		}
		return arn, nil
	}
	// Cross-runtime guard: a handle prefixed with a different ECS-managed
	// workerID would silently pass a malformed ARN to ECS; reject it.
	for _, other := range []string{WorkerID, EC2WorkerID} {
		if other != r.workerID && strings.HasPrefix(h, other+"/") {
			return "", fmt.Errorf("fargate: handle %q belongs to runtime %q, not %q", h, other, r.workerID)
		}
	}
	return h, nil
}

// WorkerID returns the runtime's synthetic worker identity. It is "fargate"
// for Fargate launch type and "ecs-ec2" for EC2 launch type. Satisfies
// lifecycle.FargateTaskSweeper.
func (r *Runtime) WorkerID() string { return r.workerID }

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
// When cfg.ControlPlaneURL is set, SHINYHUB_CONTROL_PLANE_URL and
// SHINYHUB_BUNDLE_TOKEN are included so the runner image can fetch the bundle
// by token without performing mTLS certificate setup.
func (r *Runtime) replicaEnv(p process.StartParams) []ecstypes.KeyValuePair {
	env := make([]ecstypes.KeyValuePair, 0, len(p.Env)+len(p.SecretEnv)+7)
	appendKV := func(kv string) {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			env = append(env, ecstypes.KeyValuePair{
				Name:  aws.String(kv[:idx]),
				Value: aws.String(kv[idx+1:]),
			})
		}
	}
	for _, kv := range p.Env {
		appendKV(kv)
	}
	// SecretEnv is carried as plaintext override Environment here, the same as
	// the native and Docker runtimes. It is kept as a separate slice (rather
	// than merged into Env) so it can later be delivered via the task
	// definition's secrets block (valueFrom an ARN), which keeps secret values
	// out of ecs:DescribeTasks.
	for _, kv := range p.SecretEnv {
		appendKV(kv)
	}
	add := func(k, v string) {
		if v != "" {
			env = append(env, ecstypes.KeyValuePair{Name: aws.String(k), Value: aws.String(v)})
		}
	}
	// SHINYHUB_SLUG is always emitted even when empty: the runner image requires
	// this variable to identify the app. Start validates that slug is non-empty
	// before calling replicaEnv, so this path is belt-and-suspenders.
	env = append(env, ecstypes.KeyValuePair{
		Name:  aws.String("SHINYHUB_SLUG"),
		Value: aws.String(p.Slug),
	})
	add("SHINYHUB_REPLICA_INDEX", strconv.Itoa(p.Index))
	add("SHINYHUB_CONTENT_DIGEST", p.ContentDigest)
	if p.DeploymentID > 0 {
		add("SHINYHUB_DEPLOYMENT_ID", strconv.FormatInt(p.DeploymentID, 10))
	}
	add("SHINYHUB_APP_VERSION", p.AppVersion)
	if r.cfg.ControlPlaneURL != "" {
		add("SHINYHUB_CONTROL_PLANE_URL", r.cfg.ControlPlaneURL)
	}
	if len(r.cfg.BundleTokenKey) > 0 && p.ContentDigest != "" {
		ttl := r.cfg.BundleTokenTTL
		if ttl <= 0 {
			ttl = 10 * time.Minute
		}
		tok := bundletoken.Mint(r.cfg.BundleTokenKey, p.ContentDigest, ttl, time.Now().Unix())
		add("SHINYHUB_BUNDLE_TOKEN", tok)
	}
	return env
}

// buildContainerOverride builds the per-replica override: the launch command,
// the resolved environment, and the optional CPU/memory limits. CPUQuotaPercent
// is converted to ECS CPU units (1024 = one vCPU), so 100% maps to 1024 units.
// When TaskCPUUnits or TaskMemoryMB are configured, values that exceed the
// task-level ceiling are clamped and a Warn is logged so operators notice
// misconfigured per-app limits without a cryptic RunTask error.
func (r *Runtime) buildContainerOverride(p process.StartParams) ecstypes.ContainerOverride {
	ov := ecstypes.ContainerOverride{
		Name:        aws.String(r.cfg.ContainerName),
		Command:     p.Command,
		Environment: r.replicaEnv(p),
	}
	if p.MemoryLimitMB > 0 {
		mem := int32(p.MemoryLimitMB)
		if r.cfg.TaskMemoryMB > 0 && mem > r.cfg.TaskMemoryMB {
			r.log.Warn("fargate: container memory exceeds task ceiling; clamping",
				"slug", p.Slug, "index", p.Index,
				"requested_mb", mem, "task_ceiling_mb", r.cfg.TaskMemoryMB)
			mem = r.cfg.TaskMemoryMB
		}
		ov.Memory = aws.Int32(mem)
	}
	if p.CPUQuotaPercent > 0 {
		// Round to nearest ECS CPU unit: (pct*1024 + 50) / 100.
		// Integer rounding prevents the task-ceiling clamp from misfiring on
		// non-multiples of 100 (e.g. 33% -> 338, not 337).
		cpuUnits := (int32(p.CPUQuotaPercent)*1024 + 50) / 100
		if r.cfg.TaskCPUUnits > 0 && cpuUnits > r.cfg.TaskCPUUnits {
			r.log.Warn("fargate: container CPU exceeds task ceiling; clamping",
				"slug", p.Slug, "index", p.Index,
				"requested_units", cpuUnits, "task_ceiling_units", r.cfg.TaskCPUUnits)
			cpuUnits = r.cfg.TaskCPUUnits
		}
		ov.Cpu = aws.Int32(cpuUnits)
	}
	return ov
}

func (r *Runtime) tags(p process.StartParams) []ecstypes.Tag {
	tags := []ecstypes.Tag{
		{Key: aws.String(process.LabelManaged), Value: aws.String("true")},
		{Key: aws.String(process.LabelSlug), Value: aws.String(p.Slug)},
		{Key: aws.String(process.LabelReplicaIndex), Value: aws.String(strconv.Itoa(p.Index))},
		{Key: aws.String(process.LabelTier), Value: aws.String(p.Tier)},
		{Key: aws.String(process.LabelPort), Value: aws.String(strconv.Itoa(p.Port))},
	}
	if p.DeploymentID > 0 {
		tags = append(tags, ecstypes.Tag{Key: aws.String(process.LabelDeploymentID), Value: aws.String(strconv.FormatInt(p.DeploymentID, 10))})
	}
	if p.AppVersion != "" {
		tags = append(tags, ecstypes.Tag{Key: aws.String(process.LabelAppVersion), Value: aws.String(p.AppVersion)})
	}
	return tags
}

func (r *Runtime) runTaskInput(p process.StartParams, taskDef string) *ecs.RunTaskInput {
	if p.Slug == "" {
		r.log.Warn("fargate: runTaskInput called with empty slug; ClientToken will not be slug-scoped")
	}
	ct := clientToken(r.cfg.Cluster, p.Slug, p.Index, p.DeploymentID, time.Now().Unix(), r.workerID)
	in := &ecs.RunTaskInput{
		Cluster:        aws.String(r.cfg.Cluster),
		TaskDefinition: aws.String(taskDef),
		LaunchType:     r.cfg.LaunchType,
		Count:          aws.Int32(1),
		StartedBy:      aws.String(startedBy),
		ClientToken:    aws.String(ct),
		// The explicit Tags below are the sole source of truth for reconciliation.
		// EnableECSManagedTags adds the cluster's aws:-prefixed managed tags, which
		// never collide with our shinyhub.* keys. Task-definition tag propagation is
		// deliberately not enabled: a task definition that happened to carry a
		// shinyhub.* tag would make RunTask fail with a duplicate-key error.
		EnableECSManagedTags: true,
		NetworkConfiguration: r.networkConfig(),
		Overrides: &ecstypes.TaskOverride{
			ContainerOverrides: []ecstypes.ContainerOverride{r.buildContainerOverride(p)},
		},
		Tags: r.tags(p),
	}
	// PlatformVersion is Fargate-only; ECS rejects it when LaunchType=EC2.
	if r.cfg.LaunchType != ecstypes.LaunchTypeEc2 {
		in.PlatformVersion = optString(r.cfg.PlatformVersion)
	}
	return in
}

// resolveTaskDef decides which task definition a replica runs from. With no
// secrets store wired, or an app that has no secret env, it returns the
// operator's shared base task definition and routed=false: secret env (if any)
// stays in the plaintext override Environment, unchanged from before.
//
// When a store is wired and the app has secret env, it writes each secret value
// to the store, clones the base task definition into a per-app revision whose
// container carries a secrets block referencing the store ARNs, registers it,
// and returns that revision's ARN with routed=true. The caller then omits the
// secret values from the override Environment so they never reach
// ecs:DescribeTasks. Any store or registration failure aborts the Start (fail
// closed) rather than silently falling back to plaintext.
//
// NOTE: this writes the store and registers a revision on every Start; the
// lifecycle phase moves the write to env mutation time and persists the chosen
// revision to avoid version churn.
func (r *Runtime) resolveTaskDef(ctx context.Context, p process.StartParams) (taskDef string, routed bool, err error) {
	needsSecrets := len(p.SecretEnv) > 0
	needsS3Files := r.cfg.S3Files.Configured()
	if !needsSecrets && !needsS3Files {
		return r.cfg.TaskDefinition, false, nil
	}
	if needsSecrets && r.secrets == nil {
		// Fail closed: a Fargate replica with secret env vars must never fall
		// back to plaintext task overrides, which would expose the values via
		// ecs:DescribeTasks. The operator must configure the secrets backend
		// (runtime.fargate.secrets.name_prefix).
		return "", false, fmt.Errorf("fargate: app %d has %d secret env var(s) but runtime.fargate.secrets is not configured; refusing to expose them as plaintext task overrides", p.AppID, len(p.SecretEnv))
	}
	family := taskDefFamily(r.familyPrefix(), p.AppID)
	hash := secretSetHash(p.SecretEnv)

	// Serialize sync for this app so concurrent replica starts collapse into one
	// store write + registration, and reuse the existing revision when nothing
	// changed since the last sync.
	unlock := r.appSync.lock(p.AppID)
	defer unlock()

	// Resolve the base task definition's identity so a new base revision (a new
	// runner image, role, or platform setting the operator rolled out) is picked
	// up even when the app's secrets are unchanged. A pinned base
	// (family:revision or ARN with revision) is immutable, so its literal
	// reference is the identity and no describe is needed on a cache hit; a bare
	// family must be described to learn its current ACTIVE revision.
	var base *ecstypes.TaskDefinition
	baseKey := r.cfg.TaskDefinition
	if !isPinnedTaskDef(r.cfg.TaskDefinition) {
		b, err := r.describeBaseTaskDef(ctx)
		if err != nil {
			return "", false, err
		}
		base = b
		baseKey = aws.ToString(b.TaskDefinitionArn)
	}
	syncKey := hash + "|" + baseKey + "|" + r.s3filesSyncKey(p.Slug)
	if cached, ok := r.cachedSyncKey(p.AppID); ok && cached == syncKey {
		return family, true, nil
	}

	refs := make([]ecstypes.Secret, 0, len(p.SecretEnv))
	for _, kv := range p.SecretEnv {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			continue
		}
		name := SecretName(r.cfg.SecretNamePrefix, p.AppID, key)
		arn, perr := r.secrets.Put(ctx, name, value)
		if perr != nil {
			return "", false, fmt.Errorf("fargate: store secret %q for app %d: %w", key, p.AppID, perr)
		}
		refs = append(refs, ecstypes.Secret{Name: aws.String(key), ValueFrom: aws.String(arn)})
	}
	if len(refs) == 0 && !needsS3Files {
		// SecretEnv held only malformed entries and no managed volume to inject.
		return r.cfg.TaskDefinition, false, nil
	}
	if base == nil {
		// Pinned base: not described above; fetch it now to clone.
		b, err := r.describeBaseTaskDef(ctx)
		if err != nil {
			return "", false, err
		}
		base = b
	}
	in, err := buildTaskDefInput(base, family, r.cfg.ContainerName, refs)
	if err != nil {
		return "", false, err
	}
	// Inject the managed durable-data mount (per-app S3 Files subdirectory).
	if err := addS3FilesMount(in, r.cfg.ContainerName, r.cfg.S3Files, p.Slug); err != nil {
		return "", false, err
	}
	out, err := r.client.RegisterTaskDefinition(ctx, in)
	if err != nil {
		return "", false, fmt.Errorf("fargate: register task def for app %d: %w", p.AppID, err)
	}
	if out.TaskDefinition == nil || out.TaskDefinition.TaskDefinitionArn == nil {
		return "", false, fmt.Errorf("fargate: register task def for app %d returned no arn", p.AppID)
	}
	r.setSyncKey(p.AppID, syncKey)
	// Return the family NAME, not the freshly registered revision ARN. RunTask's
	// ClientToken is keyed on replica identity and a time bucket, not the task
	// definition; if a retry within that window re-registered a new revision and
	// we passed its ARN, ECS would reject the reused token as a ConflictException
	// (same token, different parameters). The family name is stable across
	// re-registrations, and ECS resolves it to the latest ACTIVE revision, which
	// is the one just registered.
	return family, true, nil
}

// describeBaseTaskDef fetches the operator-configured base task definition that
// per-app revisions are cloned from.
func (r *Runtime) describeBaseTaskDef(ctx context.Context) (*ecstypes.TaskDefinition, error) {
	out, err := r.client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(r.cfg.TaskDefinition),
	})
	if err != nil {
		return nil, fmt.Errorf("fargate: describe base task def %q: %w", r.cfg.TaskDefinition, err)
	}
	if out.TaskDefinition == nil {
		return nil, fmt.Errorf("fargate: base task def %q not found", r.cfg.TaskDefinition)
	}
	return out.TaskDefinition, nil
}

// CleanupApp removes the external resources a deleted app left behind: every
// secret under the app's per-app store prefix and every revision of the app's
// task-definition family. It is a no-op when the runtime registers neither
// (no secrets backend and no S3 Files backend). Safe to call repeatedly
// (idempotent): missing secrets and already-inactive revisions are tolerated.
// Called from the app-delete path and the startup tombstone reconcile so secrets
// and task-def revisions never orphan.
//
// Note: this deregisters ECS task-definition revisions but does NOT delete the
// app's data subdirectory on the S3 Files file system; that data outlives the
// app deletion by design (an operator can recover it), consistent with how the
// native/docker runtimes leave app-data on disk.
func (r *Runtime) CleanupApp(ctx context.Context, appID int64) error {
	registersTaskDefs := r.secrets != nil || r.cfg.S3Files.Configured()
	if !registersTaskDefs {
		return nil
	}
	r.forgetSyncKey(appID)

	if r.secrets != nil {
		if err := r.secrets.DeleteByPrefix(ctx, appSecretPrefix(r.cfg.SecretNamePrefix, appID)); err != nil {
			return fmt.Errorf("fargate: delete secrets for app %d: %w", appID, err)
		}
	}

	family := taskDefFamily(r.familyPrefix(), appID)
	var next *string
	for {
		out, err := r.client.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{
			FamilyPrefix: aws.String(family),
			Status:       ecstypes.TaskDefinitionStatusActive,
			NextToken:    next,
		})
		if err != nil {
			return fmt.Errorf("fargate: list task defs for app %d: %w", appID, err)
		}
		for _, arn := range out.TaskDefinitionArns {
			// FamilyPrefix is a prefix match, so skip a sibling family that shares
			// the prefix (e.g. app-7 vs app-70) by requiring an exact family match.
			if familyOfTaskDefARN(arn) != family {
				continue
			}
			if _, derr := r.client.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{
				TaskDefinition: aws.String(arn),
			}); derr != nil {
				return fmt.Errorf("fargate: deregister task def %s: %w", arn, derr)
			}
		}
		if out.NextToken == nil || aws.ToString(out.NextToken) == "" {
			return nil
		}
		next = out.NextToken
	}
}

func (r *Runtime) cachedSyncKey(appID int64) (string, bool) {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	k, ok := r.syncKeys[appID]
	return k, ok
}

func (r *Runtime) setSyncKey(appID int64, key string) {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	r.syncKeys[appID] = key
}

func (r *Runtime) forgetSyncKey(appID int64) {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	delete(r.syncKeys, appID)
}

// secretSetHash is a stable digest of a secret env set ("KEY=VALUE" entries),
// order-independent, used to detect whether an app's secrets changed since the
// last sync. A value change alters the digest and forces a re-sync.
func secretSetHash(secretEnv []string) string {
	sorted := append([]string(nil), secretEnv...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, kv := range sorted {
		h.Write([]byte(kv))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// keyedMutex provides a separate lock per int64 key, so operations on different
// keys proceed concurrently while operations on the same key serialize.
type keyedMutex struct {
	mu sync.Mutex
	m  map[int64]*sync.Mutex
}

// lock acquires the lock for id and returns its unlock function.
func (k *keyedMutex) lock(id int64) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = make(map[int64]*sync.Mutex)
	}
	mu := k.m[id]
	if mu == nil {
		mu = &sync.Mutex{}
		k.m[id] = mu
	}
	k.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// Start launches one Fargate task for the replica and waits until it acquires a
// routable private IP, returning the route URL and an opaque handle carrying the
// task ARN. A task that fails to schedule (a RunTask failure entry) or never
// becomes routable within startTimeout is stopped before returning the error, so
// a failed Start never leaks a running task.
func (r *Runtime) Start(ctx context.Context, p process.StartParams, logWriter io.Writer) (process.ReplicaEndpoint, error) {
	if p.Slug == "" {
		return process.ReplicaEndpoint{}, fmt.Errorf("fargate: Start requires a non-empty slug")
	}
	taskDef, routed, err := r.resolveTaskDef(ctx, p)
	if err != nil {
		return process.ReplicaEndpoint{}, err
	}
	if routed {
		// Secrets are delivered via the task definition's secrets block; keep
		// them out of the plaintext override Environment.
		p.SecretEnv = nil
	}
	startTime := time.Now()
	out, err := r.client.RunTask(ctx, r.runTaskInput(p, taskDef))
	if err != nil {
		r.metrics.RecordRunTask("error")
		r.metrics.ObserveRunTaskLatency(time.Since(startTime).Seconds())
		return process.ReplicaEndpoint{}, fmt.Errorf("fargate: run task: %w", err)
	}
	if len(out.Failures) > 0 {
		f := out.Failures[0]
		r.metrics.RecordRunTask("error")
		r.metrics.ObserveRunTaskLatency(time.Since(startTime).Seconds())
		return process.ReplicaEndpoint{}, fmt.Errorf("fargate: run task failed: %s: %s",
			aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 || out.Tasks[0].TaskArn == nil {
		r.metrics.RecordRunTask("error")
		r.metrics.ObserveRunTaskLatency(time.Since(startTime).Seconds())
		return process.ReplicaEndpoint{}, fmt.Errorf("fargate: run task returned no task")
	}
	taskARN := aws.ToString(out.Tasks[0].TaskArn)
	r.metrics.RecordRunTask("ok")
	r.metrics.ObserveRunTaskLatency(time.Since(startTime).Seconds())
	r.log.Info("fargate run task issued", "slug", p.Slug, "index", p.Index, "task_arn", taskARN)

	ip, err := r.waitForIP(ctx, taskARN)
	if err != nil {
		// Best-effort teardown so a task that never became routable does not leak.
		r.stop(context.WithoutCancel(ctx), taskARN, "shinyhub: start failed to acquire ip")
		return process.ReplicaEndpoint{}, err
	}
	r.log.Info("fargate task routable", "slug", p.Slug, "index", p.Index, "task_arn", taskARN, "ip", ip)
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://%s:%d", ip, p.Port),
		Provider: Provider,
		WorkerID: r.workerID,
		Handle:   process.RunHandle{ContainerID: r.encodeHandle(taskARN)},
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
			r.metrics.RecordWaitIPTimeout()
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

// Signal maps a stop-intent signal to ECS StopTask. ECS has no API to deliver
// an arbitrary signal to a Fargate container, so SIGTERM and SIGKILL both
// request a graceful StopTask (which itself sends SIGTERM then SIGKILL after
// the task's stopTimeout); any other signal is a no-op the Manager never relies
// on for Fargate.
//
// Signal uses a 30-second internal timeout so a hung StopTask call does not
// consume the entire graceful-shutdown budget.
func (r *Runtime) Signal(handle process.RunHandle, sig syscall.Signal) error {
	if sig != syscall.SIGTERM && sig != syscall.SIGKILL {
		return nil
	}
	taskARN, err := r.decodeHandle(handle.ContainerID)
	if err != nil {
		return err
	}
	reason := "shinyhub: replica stop"
	if sig == syscall.SIGKILL {
		reason = "shinyhub: replica kill"
	}
	r.log.Info("fargate signal", "task_arn", taskARN, "signal", sig)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return r.stop(ctx, taskARN, reason)
}

func (r *Runtime) stop(ctx context.Context, taskARN, reason string) error {
	r.log.Debug("fargate stop task", "task_arn", taskARN, "reason", reason)
	_, err := r.client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(r.cfg.Cluster),
		Task:    aws.String(taskARN),
		Reason:  aws.String(reason),
	})
	if err != nil {
		r.metrics.RecordStopTask("error")
		return fmt.Errorf("fargate: stop task %s: %w", taskARN, err)
	}
	r.metrics.RecordStopTask("ok")
	return nil
}

// Wait blocks until the task reaches STOPPED or ctx is cancelled. It reports only
// liveness; exit codes are surfaced by RunOnce, not here.
func (r *Runtime) Wait(ctx context.Context, handle process.RunHandle) error {
	taskARN, err := r.decodeHandle(handle.ContainerID)
	if err != nil {
		return err
	}
	for {
		task, err := r.describeTask(ctx, taskARN)
		if err != nil {
			return err
		}
		if task == nil || aws.ToString(task.LastStatus) == "STOPPED" {
			r.log.Debug("fargate task terminal", "task_arn", taskARN)
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
	if p.Slug == "" {
		return process.ExitInfo{}, fmt.Errorf("fargate: RunOnce requires a non-empty slug")
	}
	taskDef, routed, err := r.resolveTaskDef(ctx, p)
	if err != nil {
		return process.ExitInfo{}, err
	}
	if routed {
		// Secrets are delivered via the task definition's secrets block; keep
		// them out of the plaintext override Environment.
		p.SecretEnv = nil
	}
	startTime := time.Now()
	out, err := r.client.RunTask(ctx, r.runTaskInput(p, taskDef))
	if err != nil {
		r.metrics.RecordRunTask("error")
		r.metrics.ObserveRunTaskLatency(time.Since(startTime).Seconds())
		return process.ExitInfo{}, fmt.Errorf("fargate: run task: %w", err)
	}
	if len(out.Failures) > 0 {
		f := out.Failures[0]
		r.metrics.RecordRunTask("error")
		r.metrics.ObserveRunTaskLatency(time.Since(startTime).Seconds())
		return process.ExitInfo{}, fmt.Errorf("fargate: run task failed: %s: %s",
			aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 || out.Tasks[0].TaskArn == nil {
		r.metrics.RecordRunTask("error")
		r.metrics.ObserveRunTaskLatency(time.Since(startTime).Seconds())
		return process.ExitInfo{}, fmt.Errorf("fargate: run task returned no task")
	}
	taskARN := aws.ToString(out.Tasks[0].TaskArn)
	r.metrics.RecordRunTask("ok")
	r.metrics.ObserveRunTaskLatency(time.Since(startTime).Seconds())

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
		r.metrics.RecordInventoryError()
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
			r.metrics.RecordInventoryError()
			// A per-batch DescribeTasks error means the ECS runtime is partially
			// reachable. Return PartialInventoryError so recovery marks the runtime's
			// replicas indeterminate (ti.unreachable[r.workerID]) instead of allDown.
			// r.workerID is "fargate" or "ecs-ec2" depending on the launch type.
			return items, &process.PartialInventoryError{Workers: []string{r.workerID}}
		}
		for _, task := range out.Tasks {
			// Client-side launch-type filter: the AWS SDK forbids combining StartedBy
			// with LaunchType on ListTasksInput, so we filter here by inspecting
			// task.LaunchType from DescribeTasks. A task whose LaunchType is empty
			// (an older task that predates the field) defaults to Fargate.
			lt := task.LaunchType
			if lt == "" {
				lt = ecstypes.LaunchTypeFargate
			}
			if lt != r.cfg.LaunchType {
				continue
			}
			labels := tagsToLabels(task.Tags)
			if labels[process.LabelSlug] == "" {
				continue
			}
			// Rebuild the full route URL (http://<ip>:<port>) the proxy registered
			// at Start time. The port comes from the tag stamped at RunTask; a task
			// missing the tag (e.g. one started by an older build) falls back to a
			// portless URL rather than being dropped from inventory.
			url := ""
			ip, err := r.routeIP(ctx, task)
			if err != nil {
				r.metrics.RecordInventoryError()
				return nil, err
			}
			if ip != "" {
				if port := labels[process.LabelPort]; port != "" {
					url = "http://" + ip + ":" + port
				} else {
					url = "http://" + ip
				}
			}
			items = append(items, process.InventoryItem{
				ContainerID: aws.ToString(task.TaskArn),
				Labels:      labels,
				// Running is true for any task not in STOPPED state (PROVISIONING, PENDING,
				// or RUNNING). Only STOPPED tasks are Running=false. This matches the
				// InventoryItem.Running semantics: "not stopped" rather than "routable now".
				// Consumers that need a routable URL must additionally check URL != "".
				Running:  aws.ToString(task.LastStatus) != "STOPPED",
				URL:      url,
				WorkerID: r.workerID,
			})
		}
	}
	r.log.Debug("fargate inventory", "count", len(items), "total_arns", len(arns))
	return items, nil
}

// ListManagedTasks returns a TaskRef for each ShinyHub-managed task on the
// cluster (StartedBy="shinyhub") whose launch type matches this runtime's
// configured launch type. It satisfies lifecycle.FargateTaskSweeper.
func (r *Runtime) ListManagedTasks(ctx context.Context) ([]process.TaskRef, error) {
	arns, err := r.listManagedTaskARNs(ctx)
	if err != nil {
		return nil, err
	}
	if len(arns) == 0 {
		return nil, nil
	}
	// Describe to filter by launch type. The AWS SDK forbids combining
	// StartedBy with LaunchType on ListTasksInput, so filtering is done
	// client-side here by inspecting task.LaunchType from DescribeTasks.
	var out []process.TaskRef
	for start := 0; start < len(arns); start += 100 {
		end := start + 100
		if end > len(arns) {
			end = len(arns)
		}
		// Tags are intentionally not requested (no Include: TaskFieldTags):
		// this call only needs task.LaunchType and task.TaskArn, both base
		// response fields, so omitting the tags include keeps the call cheap.
		desc, err := r.client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(r.cfg.Cluster),
			Tasks:   arns[start:end],
		})
		if err != nil {
			return nil, fmt.Errorf("fargate: list managed tasks describe: %w", err)
		}
		for _, t := range desc.Tasks {
			lt := t.LaunchType
			if lt == "" {
				lt = ecstypes.LaunchTypeFargate
			}
			if lt != r.cfg.LaunchType {
				continue
			}
			if t.TaskArn != nil {
				out = append(out, process.TaskRef{ARN: aws.ToString(t.TaskArn)})
			}
		}
	}
	return out, nil
}

// StopTask stops the Fargate task with the given ARN. It satisfies
// lifecycle.FargateTaskSweeper; callers supply the raw task ARN (not an
// encoded handle).
func (r *Runtime) StopTask(ctx context.Context, arn string) error {
	return r.stop(ctx, arn, "shinyhub: orphan sweep")
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

// clientToken derives a stable ECS RunTask ClientToken from the replica's
// identity and a coarse time bucket. ECS deduplicates RunTask calls with the
// same ClientToken within a 10-minute window: a control-plane retry during that
// window returns the already-running task instead of launching a duplicate.
// The time bucket (unix seconds / 600) advances every 10 minutes, so a
// deliberate re-launch after a STOPPED task in the prior window still issues a
// fresh RunTask.
//
// Formula: SHA-256(cluster | slug | index | deploymentID | workerID | bucket)
// as a lowercase hex string. SHA-256 hex is always exactly 64 characters, which
// fits the ECS ClientToken maximum length of 64 exactly.
// The workerID segment differentiates a Fargate and an EC2 RunTask for the same
// replica in the same 10-min window, preventing ECS from deduplicating the EC2
// launch into a running Fargate task (silent cross-contamination).
// When deploymentID is 0 (legacy/pre-deploy rows) the field is omitted so the
// token still differentiates across time buckets.
func clientToken(cluster, slug string, index int, deploymentID int64, nowUnix int64, workerID string) string {
	bucket := strconv.FormatInt(nowUnix/600, 10)
	var b strings.Builder
	b.WriteString(cluster)
	b.WriteByte('|')
	b.WriteString(slug)
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(index))
	b.WriteByte('|')
	if deploymentID > 0 {
		b.WriteString(strconv.FormatInt(deploymentID, 10))
	}
	b.WriteByte('|')
	b.WriteString(workerID)
	b.WriteByte('|')
	b.WriteString(bucket)
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", sum)
}

var (
	_ process.Runtime          = (*Runtime)(nil)
	_ process.ReplicaInventory = (*Runtime)(nil)
)
