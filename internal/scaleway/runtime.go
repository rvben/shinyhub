// Package scaleway provides a process runtime backed by Scaleway Serverless
// Containers v1. Each ShinyHub replica owns one persistent Serverless Container
// resource configured with min_scale=0 and max_scale=1. Scaleway may scale its
// underlying instance to zero while the stable service endpoint remains.
package scaleway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rvben/shinyhub/internal/bundletoken"
	"github.com/rvben/shinyhub/internal/process"
)

const (
	Provider = "scaleway_serverless"
	WorkerID = "scaleway-serverless"

	PrivacyPrivate = "private"
	PrivacyPublic  = "public"

	StatusCreating = "creating"
	StatusUpdating = "updating"
	StatusReady    = "ready"
	StatusDeleting = "deleting"
	StatusError    = "error"

	minMemoryMB = 128
	maxMemoryMB = 12_228
	minMVCpu    = 70
	maxMVCpu    = 6_000
	// Scaleway Serverless Container names must contain between 2 and 34 runes.
	// resourceName only emits ASCII, so its byte and rune lengths are identical.
	maxContainerNameLength = 34
)

var (
	// ErrNotFound is returned by Client implementations when a provider
	// resource no longer exists. Runtime treats it as an idempotent stop/delete.
	ErrNotFound = errors.New("scaleway container not found")
	// ErrRunOnceUnsupported is explicit because Serverless Containers are HTTP
	// services; one-shot commands belong on Scaleway Serverless Jobs.
	ErrRunOnceUnsupported = errors.New("scaleway serverless containers do not support one-shot jobs")
)

// Container is the provider state used by Runtime. SDKClient translates the
// generated Scaleway SDK types into this deliberately small boundary model.
type Container struct {
	ID             string
	Name           string
	Status         string
	ErrorMessage   string
	PublicEndpoint string
	Tags           []string
}

type CreateContainerInput struct {
	Region            string
	NamespaceID       string
	Name              string
	Image             string
	Environment       map[string]string
	SecretEnvironment map[string]string
	MinScale          uint32
	MaxScale          uint32
	MemoryMB          int
	MVCpu             int
	Timeout           time.Duration
	Privacy           string
	Port              int
	Tags              []string
	PrivateNetworkID  string
	Args              []string
}

type UpdateContainerInput struct {
	ID string
	CreateContainerInput
}

type ListContainersInput struct {
	Region      string
	ProjectID   string
	NamespaceID string
}

// Client is the Scaleway Serverless Containers v1 API surface used by Runtime.
// Production uses SDKClient; tests use a stateful fake at this cloud boundary.
type Client interface {
	CreateContainer(context.Context, CreateContainerInput) (Container, error)
	UpdateContainer(context.Context, UpdateContainerInput) (Container, error)
	GetContainer(context.Context, string) (Container, error)
	ListContainers(context.Context, ListContainersInput) ([]Container, error)
	DeleteContainer(context.Context, string) error
}

type Config struct {
	Region           string
	ProjectID        string
	NamespaceID      string
	Image            string
	NamePrefix       string
	ControlPlaneURL  string
	BundleTokenKey   []byte
	BundleTokenTTL   time.Duration
	OriginToken      string
	PrivateNetworkID string
	DefaultMemoryMB  int
	DefaultMVCpu     int
	DurableData      bool
}

type Runtime struct {
	client       Client
	cfg          Config
	log          *slog.Logger
	pollInterval time.Duration
	stateMu      sync.Mutex
	states       map[string]*runState
}

var _ process.ManagedRuntime = (*Runtime)(nil)

type runState struct {
	done    chan struct{}
	stopped bool
}

type Option func(*Runtime)

func WithPollInterval(d time.Duration) Option {
	return func(r *Runtime) { r.pollInterval = d }
}

func New(client Client, cfg Config, log *slog.Logger, opts ...Option) (*Runtime, error) {
	if client == nil {
		return nil, fmt.Errorf("scaleway: client is required")
	}
	if cfg.Region == "" || cfg.ProjectID == "" || cfg.NamespaceID == "" || cfg.Image == "" {
		return nil, fmt.Errorf("scaleway: region, project_id, namespace_id, and image are required")
	}
	if cfg.ControlPlaneURL == "" || len(cfg.BundleTokenKey) == 0 {
		return nil, fmt.Errorf("scaleway: control_plane_url and bundle token key are required")
	}
	if cfg.OriginToken == "" {
		return nil, fmt.Errorf("scaleway: origin token is required for a private container endpoint")
	}
	if cfg.NamePrefix == "" {
		cfg.NamePrefix = "shinyhub"
	}
	if cfg.DefaultMemoryMB == 0 {
		cfg.DefaultMemoryMB = 512
	}
	if cfg.DefaultMVCpu == 0 {
		cfg.DefaultMVCpu = 250
	}
	if cfg.BundleTokenTTL <= 0 {
		cfg.BundleTokenTTL = 10 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	r := &Runtime{
		client: client, cfg: cfg, log: log, pollInterval: 2 * time.Second,
		states: make(map[string]*runState),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

func (r *Runtime) Start(ctx context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	if p.Slug == "" {
		return process.ReplicaEndpoint{}, fmt.Errorf("scaleway: Start requires a non-empty slug")
	}
	in := r.createInput(p)
	existing, err := r.findReplica(ctx, p)
	if err != nil {
		return process.ReplicaEndpoint{}, err
	}
	var container Container
	if existing == nil {
		container, err = r.client.CreateContainer(ctx, in)
		if err != nil {
			return process.ReplicaEndpoint{}, fmt.Errorf("scaleway: create container: %w", err)
		}
	} else {
		container, err = r.client.UpdateContainer(ctx, UpdateContainerInput{
			ID: existing.ID, CreateContainerInput: in,
		})
		if err != nil {
			return process.ReplicaEndpoint{}, fmt.Errorf("scaleway: update container %s: %w", existing.ID, err)
		}
	}
	if container.ID == "" {
		return process.ReplicaEndpoint{}, fmt.Errorf("scaleway: create container returned no id")
	}
	if container.Status != StatusReady || container.PublicEndpoint == "" {
		container, err = r.waitReady(ctx, container.ID)
		if err != nil {
			return process.ReplicaEndpoint{}, err
		}
	}
	r.markStarted(container.ID)
	return process.ReplicaEndpoint{
		URL:      container.PublicEndpoint,
		Provider: Provider,
		WorkerID: WorkerID,
		Handle:   process.RunHandle{ContainerID: WorkerID + "/" + container.ID},
	}, nil
}

func (r *Runtime) HostPreparesDeps() bool    { return false }
func (r *Runtime) AppBindHost() string       { return "0.0.0.0" }
func (r *Runtime) HostProvidesAppData() bool { return false }
func (r *Runtime) TierHasDurableData() bool  { return r.cfg.DurableData }

// ReplicaTransportForWorker authenticates every proxy request to a private
// Scaleway endpoint. The API secret never reaches the browser or app process;
// it is added only on the control-plane-to-provider hop.
func (r *Runtime) ReplicaTransportForWorker(workerID string) http.RoundTripper {
	if workerID != WorkerID {
		return nil
	}
	return &authTransport{base: http.DefaultTransport, token: r.cfg.OriginToken}
}

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("X-Auth-Token", t.token)
	return t.base.RoundTrip(clone)
}

// Signal marks the logical replica stopped without deleting its Serverless
// Container definition. With min_scale=0 the provider keeps no idle instance,
// while retaining the endpoint and image metadata for the next Start.
func (r *Runtime) Signal(handle process.RunHandle, sig syscall.Signal) error {
	if sig != syscall.SIGTERM && sig != syscall.SIGKILL {
		return nil
	}
	id, err := decodeHandle(handle)
	if err != nil {
		return err
	}
	r.markStopped(id)
	return nil
}

// Wait blocks for ShinyHub's logical stop signal while also detecting a
// provider-side deletion or terminal error. Provider scale-to-zero is not an
// exit: the stable container resource remains Ready while no instance exists.
func (r *Runtime) Wait(ctx context.Context, handle process.RunHandle) error {
	id, err := decodeHandle(handle)
	if err != nil {
		return err
	}
	state := r.stateFor(id)
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-state.done:
			return nil
		case <-ticker.C:
			container, getErr := r.client.GetContainer(ctx, id)
			if errors.Is(getErr, ErrNotFound) {
				return nil
			}
			if getErr != nil {
				return fmt.Errorf("scaleway: get container %s while waiting: %w", id, getErr)
			}
			switch container.Status {
			case StatusDeleting:
				return nil
			case StatusError:
				return fmt.Errorf("scaleway: container %s failed: %s", id, container.ErrorMessage)
			}
		}
	}
}

func (r *Runtime) Stats(context.Context, process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}

func (r *Runtime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, ErrRunOnceUnsupported
}

// Inventory reconstructs every retained service definition. A locally stopped
// resource remains in Scaleway for cheap re-wake but is reported Running=false
// until Start activates a new logical replica in this control-plane process.
func (r *Runtime) Inventory(ctx context.Context) ([]process.InventoryItem, error) {
	containers, err := r.client.ListContainers(ctx, ListContainersInput{
		Region: r.cfg.Region, ProjectID: r.cfg.ProjectID, NamespaceID: r.cfg.NamespaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("scaleway: inventory: %w", err)
	}
	items := make([]process.InventoryItem, 0, len(containers))
	for _, container := range containers {
		labels := labelsFromTags(container.Tags)
		if labels[process.LabelManaged] != "true" || labels[process.LabelProvider] != Provider {
			continue
		}
		running := container.Status != StatusDeleting && container.Status != StatusError && !r.isStopped(container.ID)
		endpoint := ""
		if running {
			endpoint = container.PublicEndpoint
		}
		items = append(items, process.InventoryItem{
			ContainerID: container.ID, Labels: labels, Running: running,
			URL: endpoint, WorkerID: WorkerID,
		})
	}
	return items, nil
}

// CleanupApp deletes every retained container tagged with the immutable app
// id. Missing resources are success, making deletion and tombstone recovery
// safely repeatable.
func (r *Runtime) CleanupApp(ctx context.Context, appID int64) error {
	containers, err := r.client.ListContainers(ctx, ListContainersInput{
		Region: r.cfg.Region, ProjectID: r.cfg.ProjectID, NamespaceID: r.cfg.NamespaceID,
	})
	if err != nil {
		return fmt.Errorf("scaleway: list containers for app cleanup: %w", err)
	}
	want := strconv.FormatInt(appID, 10)
	var deleteErrs []error
	for _, container := range containers {
		labels := labelsFromTags(container.Tags)
		if labels[process.LabelManaged] != "true" ||
			labels[process.LabelProvider] != Provider ||
			labels["shinyhub.app_id"] != want {
			continue
		}
		if err := r.client.DeleteContainer(ctx, container.ID); err != nil && !errors.Is(err, ErrNotFound) {
			deleteErrs = append(deleteErrs, fmt.Errorf("scaleway: delete container %s for app %d: %w", container.ID, appID, err))
			continue
		}
		r.stateMu.Lock()
		delete(r.states, container.ID)
		r.stateMu.Unlock()
	}
	return errors.Join(deleteErrs...)
}

func decodeHandle(handle process.RunHandle) (string, error) {
	if handle.ContainerID == "" {
		return "", fmt.Errorf("scaleway: empty run handle")
	}
	prefix := WorkerID + "/"
	if !strings.HasPrefix(handle.ContainerID, prefix) {
		return "", fmt.Errorf("scaleway: handle %q belongs to another runtime", handle.ContainerID)
	}
	id := strings.TrimPrefix(handle.ContainerID, prefix)
	if id == "" {
		return "", fmt.Errorf("scaleway: run handle has no container id")
	}
	return id, nil
}

func (r *Runtime) markStarted(id string) {
	r.stateMu.Lock()
	r.states[id] = &runState{done: make(chan struct{})}
	r.stateMu.Unlock()
}

func (r *Runtime) markStopped(id string) {
	r.stateMu.Lock()
	state := r.states[id]
	if state == nil {
		state = &runState{done: make(chan struct{})}
		r.states[id] = state
	}
	if !state.stopped {
		state.stopped = true
		close(state.done)
	}
	r.stateMu.Unlock()
}

func (r *Runtime) stateFor(id string) *runState {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	state := r.states[id]
	if state == nil {
		state = &runState{done: make(chan struct{})}
		r.states[id] = state
	}
	return state
}

func (r *Runtime) isStopped(id string) bool {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.states[id] != nil && r.states[id].stopped
}

func (r *Runtime) findReplica(ctx context.Context, p process.StartParams) (*Container, error) {
	containers, err := r.client.ListContainers(ctx, ListContainersInput{
		Region: r.cfg.Region, ProjectID: r.cfg.ProjectID, NamespaceID: r.cfg.NamespaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("scaleway: list containers: %w", err)
	}
	var match *Container
	for i := range containers {
		labels := labelsFromTags(containers[i].Tags)
		if labels[process.LabelManaged] != "true" ||
			labels[process.LabelProvider] != Provider ||
			labels[process.LabelSlug] != p.Slug ||
			labels[process.LabelReplicaIndex] != strconv.Itoa(p.Index) ||
			labels["shinyhub.app_id"] != strconv.FormatInt(p.AppID, 10) {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("scaleway: multiple managed containers claim app %d replica %d (%s, %s)",
				p.AppID, p.Index, match.ID, containers[i].ID)
		}
		candidate := containers[i]
		match = &candidate
	}
	return match, nil
}

func (r *Runtime) createInput(p process.StartParams) CreateContainerInput {
	env, secret := r.replicaEnv(p)
	memory := p.MemoryLimitMB
	if memory == 0 {
		memory = r.cfg.DefaultMemoryMB
	}
	memory = clamp(memory, minMemoryMB, maxMemoryMB)
	cpu := r.cfg.DefaultMVCpu
	if p.CPUQuotaPercent > 0 {
		cpu = p.CPUQuotaPercent * 10
	}
	cpu = clamp(cpu, minMVCpu, maxMVCpu)
	return CreateContainerInput{
		Region: r.cfg.Region, NamespaceID: r.cfg.NamespaceID,
		Name: resourceName(r.cfg.NamePrefix, p), Image: r.cfg.Image,
		Environment: env, SecretEnvironment: secret,
		MinScale: 0, MaxScale: 1,
		MemoryMB: memory, MVCpu: cpu, Timeout: time.Hour,
		Privacy: PrivacyPrivate, Port: p.Port,
		Tags: tags(p), PrivateNetworkID: r.cfg.PrivateNetworkID,
		Args: append([]string(nil), p.Command...),
	}
}

func (r *Runtime) replicaEnv(p process.StartParams) (map[string]string, map[string]string) {
	env := envMap(p.Env)
	secret := envMap(p.SecretEnv)
	for key := range secret {
		if strings.HasPrefix(key, "SHINYHUB_") {
			delete(secret, key)
		}
	}
	env["SHINYHUB_SLUG"] = p.Slug
	env["SHINYHUB_REPLICA_INDEX"] = strconv.Itoa(p.Index)
	if p.ContentDigest != "" {
		env["SHINYHUB_CONTENT_DIGEST"] = p.ContentDigest
	}
	if p.DeploymentID > 0 {
		env["SHINYHUB_DEPLOYMENT_ID"] = strconv.FormatInt(p.DeploymentID, 10)
	}
	if p.AppVersion != "" {
		env["SHINYHUB_APP_VERSION"] = p.AppVersion
	}
	env["SHINYHUB_CONTROL_PLANE_URL"] = r.cfg.ControlPlaneURL
	if p.ContentDigest != "" {
		secret["SHINYHUB_BUNDLE_TOKEN"] = bundletoken.Mint(
			r.cfg.BundleTokenKey, p.ContentDigest, r.cfg.BundleTokenTTL, time.Now().Unix())
	}
	return env, secret
}

func envMap(values []string) map[string]string {
	out := make(map[string]string, len(values))
	for _, item := range values {
		if key, value, ok := strings.Cut(item, "="); ok && key != "" {
			out[key] = value
		}
	}
	return out
}

var nonName = regexp.MustCompile(`[^a-z0-9-]+`)

func resourceName(prefix string, p process.StartParams) string {
	prefix = strings.ToLower(prefix)
	prefix = strings.Trim(nonName.ReplaceAllString(prefix, "-"), "-")
	if prefix == "" {
		prefix = "shinyhub"
	}
	identity := "s" + strings.Trim(nonName.ReplaceAllString(strings.ToLower(p.Slug), "-"), "-")
	if p.AppID > 0 {
		identity = "a" + strconv.FormatInt(p.AppID, 10)
	}
	suffix := fmt.Sprintf("-%s-r%d", identity, p.Index)
	maxPrefix := maxContainerNameLength - len(suffix)
	if len(prefix) > maxPrefix {
		prefix = strings.TrimRight(prefix[:maxPrefix], "-")
	}
	return prefix + suffix
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func tags(p process.StartParams) []string {
	values := map[string]string{
		process.LabelManaged:      "true",
		process.LabelProvider:     Provider,
		process.LabelSlug:         p.Slug,
		process.LabelReplicaIndex: strconv.Itoa(p.Index),
		process.LabelTier:         p.Tier,
		process.LabelPort:         strconv.Itoa(p.Port),
		"shinyhub.app_id":         strconv.FormatInt(p.AppID, 10),
	}
	if p.DeploymentID > 0 {
		values[process.LabelDeploymentID] = strconv.FormatInt(p.DeploymentID, 10)
	}
	if p.AppVersion != "" {
		values[process.LabelAppVersion] = p.AppVersion
	}
	if p.ContentDigest != "" {
		values[process.LabelContentDigest] = p.ContentDigest
	}
	order := []string{
		process.LabelManaged, process.LabelProvider, process.LabelSlug,
		process.LabelReplicaIndex, process.LabelTier, process.LabelPort,
		"shinyhub.app_id", process.LabelDeploymentID,
		process.LabelAppVersion, process.LabelContentDigest,
	}
	out := make([]string, 0, len(values))
	for _, key := range order {
		if value, ok := values[key]; ok {
			out = append(out, key+"="+value)
		}
	}
	return out
}

func labelsFromTags(tags []string) map[string]string {
	labels := make(map[string]string, len(tags))
	for _, tag := range tags {
		if key, value, ok := strings.Cut(tag, "="); ok && key != "" {
			labels[key] = value
		}
	}
	return labels
}

func (r *Runtime) waitReady(ctx context.Context, id string) (Container, error) {
	for {
		container, err := r.client.GetContainer(ctx, id)
		if err != nil {
			return Container{}, fmt.Errorf("scaleway: get container %s: %w", id, err)
		}
		switch container.Status {
		case StatusReady:
			if container.PublicEndpoint == "" {
				return Container{}, fmt.Errorf("scaleway: ready container %s has no endpoint", id)
			}
			return container, nil
		case StatusError:
			return Container{}, fmt.Errorf("scaleway: container %s failed: %s", id, container.ErrorMessage)
		}
		timer := time.NewTimer(r.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Container{}, ctx.Err()
		case <-timer.C:
		}
	}
}
