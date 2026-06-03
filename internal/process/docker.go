package process

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// providerDocker is the provider name DockerRuntime reports for the replicas it
// starts (in ReplicaEndpoint and the shinyhub.provider container label).
const providerDocker = "docker"

// dockerLabels builds the label set for a long-running app replica container.
// An empty StartParams.Tier defaults to DefaultTier so every replica container
// carries a concrete tier for recovery to route on. When present, deployment_id,
// app_version, and content_digest are stamped so recovery can reconcile by
// deployment and reject stale containers. Zero/empty values are omitted to keep
// the label set clean for callers that do not supply deployment metadata.
func dockerLabels(p StartParams) map[string]string {
	tier := p.Tier
	if tier == "" {
		tier = DefaultTier
	}
	labels := map[string]string{
		LabelManaged:      "true",
		LabelSlug:         p.Slug,
		LabelReplicaIndex: strconv.Itoa(p.Index),
		LabelTier:         tier,
		LabelProvider:     providerDocker,
	}
	if p.DeploymentID != 0 {
		labels[LabelDeploymentID] = strconv.FormatInt(p.DeploymentID, 10)
	}
	if p.AppVersion != "" {
		labels[LabelAppVersion] = p.AppVersion
	}
	if p.ContentDigest != "" {
		labels[LabelContentDigest] = p.ContentDigest
	}
	return labels
}

// DockerRuntime implements Runtime using the Docker Engine API.
// Each app runs in its own container with the bundle directory mounted at /app.
type DockerRuntime struct {
	client      *dockerClient
	pythonImage string
	rImage      string
	// networkMode is the Docker network mode applied to every container this
	// runtime starts. "bridge" (default, isolated namespace + 127.0.0.1 host
	// port mapping) or "host" (shares the host network stack).
	networkMode string
}

// Compile-time interface check.
var _ Runtime = (*DockerRuntime)(nil)

// NewDockerRuntime creates a DockerRuntime connected to socketPath.
// networkMode must be "bridge" or "host" (validated by config).
// Returns an error if the socket is unreachable (verified by pinging the API).
func NewDockerRuntime(socketPath, pythonImage, rImage, networkMode string) (*DockerRuntime, error) {
	client := newDockerClient(socketPath)
	if err := client.get("/_ping", nil); err != nil {
		return nil, fmt.Errorf("docker socket %s: %w", socketPath, err)
	}
	if networkMode == "" {
		networkMode = "bridge"
	}
	return &DockerRuntime{
		client:      client,
		pythonImage: pythonImage,
		rImage:      rImage,
		networkMode: networkMode,
	}, nil
}

// HostPreparesDeps reports false: dependency installation happens inside the
// container (via uv/Rscript present in the base image), so callers must not
// run uv sync / renv::restore on the host.
func (r *DockerRuntime) HostPreparesDeps() bool { return false }

// AppBindHost returns the address the app should bind inside the container.
// In host-network mode the container shares the host loopback, so 127.0.0.1
// keeps the "only the proxy can reach the app" boundary intact. In bridge
// mode the container has its own network namespace; the listener must bind
// 0.0.0.0 inside the container so the published 127.0.0.1:port mapping on
// the host can route to it.
func (r *DockerRuntime) AppBindHost() string {
	if r.networkMode == "host" {
		return "127.0.0.1"
	}
	return "0.0.0.0"
}

// HostProvidesAppData reports that the local Docker runtime provisions app data
// on the control-plane host and mounts it into the container.
func (r *DockerRuntime) HostProvidesAppData() bool { return true }

// addSharedMounts appends a read-only mount per SharedMount to cfg.Mounts,
// targeted at /app/data/shared/<source-slug>. Source paths are MkdirAll'd
// host-side so the consumer always has a directory to mount.
//
// dataHostPath is the host directory that backs /app/data inside the container.
// addSharedMounts pre-creates <dataHostPath>/shared/<slug> so the Docker daemon
// (running as root on Linux) does not auto-create it with root ownership; that
// would leave undeletable directories in the workspace owned by the daemon.
func addSharedMounts(cfg *containerConfig, mounts []SharedMount, dataHostPath string) error {
	for _, m := range mounts {
		if err := os.MkdirAll(m.HostPath, 0o750); err != nil {
			return fmt.Errorf("mkdir source data %s: %w", m.HostPath, err)
		}
		targetHost := filepath.Join(dataHostPath, "shared", m.SourceSlug)
		if err := os.MkdirAll(targetHost, 0o750); err != nil {
			return fmt.Errorf("mkdir mount target %s: %w", targetHost, err)
		}
		cfg.Mounts = append(cfg.Mounts, containerMount{
			Source: filepath.Clean(m.HostPath),
			Target: filepath.ToSlash(filepath.Join(cfg.WorkDir, "data", "shared", m.SourceSlug)),
			Mode:   "ro",
		})
	}
	return nil
}

// hostPublishPort returns the host port that the in-container bind port should
// be published to. A remote worker sets HostPublishPort to a host-allocated
// port; the local case leaves it zero and publishes to the same port.
func hostPublishPort(p StartParams) int {
	if p.HostPublishPort > 0 {
		return p.HostPublishPort
	}
	return p.Port
}

// dataHostPath returns the host directory that backs /app/data inside the
// container for the given StartParams. With an explicit AppDataPath that
// directory is used directly; otherwise /app/data lives inside the bundle dir.
func dataHostPath(p StartParams) string {
	if p.AppDataPath != "" {
		return p.AppDataPath
	}
	return filepath.Join(p.Dir, "data")
}

// dockerChildEnv builds the container environment: the scrubbed host env, the
// app's non-secret Env, then its SecretEnv. Secret env vars are injected as
// plaintext (a container shares the host trust boundary like a native process),
// and their keys are disjoint from Env so append order is safe.
func dockerChildEnv(p StartParams) []string {
	// HOME points at the bundle: it is mounted rw and owned by the container's
	// uid (see Start), so uv/renv cache and config writes land in a writable dir.
	// Without this the container, running as a non-root password-less uid, would
	// inherit a HOME (from the scrubbed host env, or "/" by default) it cannot
	// write, and uv would fail. Set before p.Env so an app can still override it.
	env := append(filteredEnv(), "HOME=/app")
	env = append(env, p.Env...)
	env = append(env, p.SecretEnv...)
	return env
}

func (r *DockerRuntime) Start(_ context.Context, p StartParams, logWriter io.Writer) (ReplicaEndpoint, error) {
	image := r.imageForCommand(p.Command)

	// Pull the image if the daemon has never seen it: container create returns a
	// 404 "No such image" otherwise, which is how a deploy onto a fresh worker
	// (or CI runner) fails. A cached image is a no-op.
	if err := r.client.ensureImage(image); err != nil {
		return ReplicaEndpoint{}, fmt.Errorf("ensure image %s for %s: %w", image, p.Slug, err)
	}

	labels := dockerLabels(p)

	cfg := containerConfig{
		Image:   image,
		Cmd:     p.Command,
		Env:     dockerChildEnv(p),
		WorkDir: "/app",
		Mounts: []containerMount{
			{Source: filepath.Clean(p.Dir), Target: "/app", Mode: "rw"}, // writable: in-container dep prep (uv project sync, renv::restore) writes into the bundle dir
		},
		Labels:      labels,
		NetworkMode: r.networkMode,
	}
	// Run as the uid:gid that owns the bundle directory. The container drops all
	// capabilities (no CAP_DAC_OVERRIDE), so a root process inside is still bound
	// by file permissions and cannot write a bundle the worker created under a
	// different uid. Running as the bundle's owner lets uv/renv write into the rw
	// /app mount whether the worker runs as root or an unprivileged service user.
	if fi, err := os.Stat(p.Dir); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			cfg.User = fmt.Sprintf("%d:%d", st.Uid, st.Gid)
		}
	}
	if r.networkMode != "host" && p.Port > 0 {
		// Bridge (or any non-host) network: publish the container's listening
		// port back to the host's loopback so only local clients (the in-process
		// proxy) can reach it. Container-side bind host is set to 0.0.0.0 by
		// AppBindHost so this mapping actually routes.
		cfg.Ports = []containerPortBinding{{
			ContainerPort: p.Port,
			HostPort:      hostPublishPort(p),
			HostIP:        "127.0.0.1",
		}}
	}
	if p.AppDataPath != "" {
		cfg.Mounts = append(cfg.Mounts,
			containerMount{Source: filepath.Clean(p.AppDataPath), Target: "/app-data", Mode: "rw"},
			containerMount{Source: filepath.Clean(p.AppDataPath), Target: filepath.ToSlash(filepath.Join(cfg.WorkDir, "data")), Mode: "rw"},
		)
		// Override any inherited SHINYHUB_APP_DATA (which would be the host path
		// from the Manager) with the in-container path. Docker env honors
		// last-occurrence-wins so appending here is sufficient.
		cfg.Env = append(cfg.Env, "SHINYHUB_APP_DATA=/app-data")
	}
	if err := addSharedMounts(&cfg, p.SharedMounts, dataHostPath(p)); err != nil {
		return ReplicaEndpoint{}, err
	}
	if p.MemoryLimitMB > 0 {
		cfg.MemoryBytes = int64(p.MemoryLimitMB) * 1024 * 1024
	}
	if p.CPUQuotaPercent > 0 {
		// NanoCPUs: 1e9 = 1 CPU core. CPUQuotaPercent=100 → 1 core.
		cfg.NanoCPUs = int64(p.CPUQuotaPercent) * 1e7
	}

	id, err := r.client.createContainer(cfg)
	if err != nil {
		return ReplicaEndpoint{}, fmt.Errorf("create container for %s: %w", p.Slug, err)
	}

	if err := r.client.startContainer(id); err != nil {
		if err := r.client.removeContainer(id); err != nil {
			slog.Warn("docker cleanup container after failed start", "container", id, "err", err)
		}
		return ReplicaEndpoint{}, fmt.Errorf("start container for %s: %w", p.Slug, err)
	}

	go r.streamLogs(id, logWriter)

	return ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: providerDocker,
		WorkerID: id,
		Handle:   RunHandle{ContainerID: id},
	}, nil
}

// RemoveHandle force-removes the container behind handle. Long-running app
// containers are created without AutoRemove (so a crash leaves the container
// inspectable for recovery), so they must be explicitly removed once the
// Manager has confirmed the process exited on stop/replace; otherwise stopped
// containers accumulate. Satisfies the optional containerRemover capability
// the Manager type-asserts for. A nil/empty ID or an already-gone container
// is treated as success.
func (r *DockerRuntime) RemoveHandle(handle RunHandle) error {
	if handle.ContainerID == "" {
		return nil
	}
	return r.client.removeContainer(handle.ContainerID)
}

func (r *DockerRuntime) Signal(handle RunHandle, sig syscall.Signal) error {
	sigStr := sigName(sig)
	if err := r.client.killContainer(handle.ContainerID, sigStr); err != nil {
		return fmt.Errorf("signal container %s: %w", handle.ContainerID, err)
	}
	return nil
}

func (r *DockerRuntime) Wait(ctx context.Context, handle RunHandle) error {
	_, err := r.client.waitContainer(ctx, handle.ContainerID)
	return err
}

func (r *DockerRuntime) Stats(ctx context.Context, handle RunHandle) (float64, uint64, error) {
	return r.client.containerStats(ctx, handle.ContainerID)
}

// ListByLabel returns containers with the given label filter (JSON filter string).
func (r *DockerRuntime) ListByLabel(labelFilter string) ([]ContainerInfo, error) {
	containers, err := r.client.listContainers(labelFilter)
	if err != nil {
		return nil, err
	}
	out := make([]ContainerInfo, len(containers))
	for i, c := range containers {
		out[i] = ContainerInfo{ID: c.ID, Labels: c.Labels}
	}
	return out, nil
}

// InspectPID returns the host PID of the container's init process.
func (r *DockerRuntime) InspectPID(containerID string) (int, error) {
	state, err := r.client.inspectContainer(containerID)
	if err != nil {
		return 0, err
	}
	return state.Pid, nil
}

// PublishedHostPort returns the host port the container's published bind port
// maps to, or 0 when nothing is published. The data-plane proxy uses this to
// rebuild its target after an agent restart re-adopts a running container.
func (r *DockerRuntime) PublishedHostPort(containerID string) (int, error) {
	return r.client.publishedHostPort(containerID)
}

// imageForCommand selects the base image based on the command.
// uv → Python image; Rscript → R image.
func (r *DockerRuntime) imageForCommand(cmd []string) string {
	if len(cmd) == 0 {
		return r.pythonImage
	}
	base := filepath.Base(cmd[0])
	if strings.HasPrefix(base, "Rscript") {
		return r.rImage
	}
	return r.pythonImage
}

// streamLogs attaches to the container stdout/stderr and copies to w.
// Docker attach uses a multiplexed stream format with 8-byte frame headers.
func (r *DockerRuntime) streamLogs(id string, w io.Writer) {
	attachURL := fmt.Sprintf("%s/containers/%s/attach?stream=1&stdout=1&stderr=1&logs=1",
		r.client.base, url.PathEscape(id))
	resp, err := r.client.stream.Post(attachURL, "", nil)
	if err != nil || resp == nil {
		return
	}
	defer resp.Body.Close()
	// Docker multiplexed stream: 8-byte header [stream_type(1), 0,0,0, size(4 BE)] + payload.
	buf := make([]byte, 32*1024)
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(resp.Body, hdr); err != nil {
			return
		}
		size := int(hdr[4])<<24 | int(hdr[5])<<16 | int(hdr[6])<<8 | int(hdr[7])
		if size == 0 {
			continue
		}
		remaining := size
		for remaining > 0 {
			n := remaining
			if n > len(buf) {
				n = len(buf)
			}
			nr, err := io.ReadFull(resp.Body, buf[:n])
			if nr > 0 {
				w.Write(buf[:nr]) //nolint:errcheck
			}
			remaining -= nr
			if err != nil {
				return
			}
		}
	}
}

// sigName converts a syscall.Signal to the string Docker's kill API expects.
func sigName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGHUP:
		return "SIGHUP"
	default:
		return fmt.Sprintf("%d", sig)
	}
}

// RunOnce creates a one-shot container with AutoRemove=true, starts it, and
// blocks on /containers/{id}/wait. Ctx cancel sends SIGTERM via the kill API,
// then SIGKILL after a 10-second grace.
func (r *DockerRuntime) RunOnce(ctx context.Context, p StartParams, logWriter io.Writer) (ExitInfo, error) {
	image := r.imageForCommand(p.Command)
	cfg := containerConfig{
		Image:   image,
		Cmd:     p.Command,
		Env:     dockerChildEnv(p),
		WorkDir: "/app",
		Mounts: []containerMount{
			{Source: filepath.Clean(p.Dir), Target: "/app", Mode: "rw"}, // writable: in-container dep prep (uv project sync, renv::restore) writes into the bundle dir
		},
		Labels: map[string]string{
			"shinyhub.managed": "true",
			"shinyhub.slug":    p.Slug,
			"shinyhub.kind":    "schedule-run",
		},
		NetworkMode: r.networkMode,
		AutoRemove:  true,
	}
	if p.AppDataPath != "" {
		cfg.Mounts = append(cfg.Mounts,
			containerMount{Source: filepath.Clean(p.AppDataPath), Target: "/app-data", Mode: "rw"},
			containerMount{Source: filepath.Clean(p.AppDataPath), Target: filepath.ToSlash(filepath.Join(cfg.WorkDir, "data")), Mode: "rw"},
		)
		cfg.Env = append(cfg.Env, "SHINYHUB_APP_DATA=/app-data")
	}
	if err := addSharedMounts(&cfg, p.SharedMounts, dataHostPath(p)); err != nil {
		return ExitInfo{}, err
	}
	if p.MemoryLimitMB > 0 {
		cfg.MemoryBytes = int64(p.MemoryLimitMB) * 1024 * 1024
	}
	if p.CPUQuotaPercent > 0 {
		cfg.NanoCPUs = int64(p.CPUQuotaPercent) * 1e7
	}

	id, err := r.client.createContainer(cfg)
	if err != nil {
		return ExitInfo{}, fmt.Errorf("create one-shot container for %s: %w", p.Slug, err)
	}
	if err := r.client.startContainer(id); err != nil {
		_ = r.client.removeContainer(id)
		return ExitInfo{}, fmt.Errorf("start one-shot container for %s: %w", p.Slug, err)
	}

	go r.streamLogs(id, logWriter)

	waitDone := make(chan waitResult, 1)
	go func() {
		code, err := r.client.waitContainer(context.Background(), id)
		waitDone <- waitResult{code: code, err: err}
	}()

	select {
	case <-ctx.Done():
		_ = r.client.killContainer(id, "SIGTERM")
		select {
		case <-waitDone:
		case <-time.After(10 * time.Second):
			_ = r.client.killContainer(id, "SIGKILL")
			<-waitDone
		}
		return ExitInfo{Code: -1, Signaled: true}, nil
	case res := <-waitDone:
		if res.err != nil {
			return ExitInfo{}, fmt.Errorf("wait one-shot %s: %w", id, res.err)
		}
		return ExitInfo{Code: res.code}, nil
	}
}

// waitResult carries the outcome of a waitContainer call.
type waitResult struct {
	code int
	err  error
}
