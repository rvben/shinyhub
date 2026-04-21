package process

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// DockerRuntime implements Runtime using the Docker Engine API.
// Each app runs in its own container with the bundle directory mounted at /app.
type DockerRuntime struct {
	client      *dockerClient
	pythonImage string
	rImage      string
}

// Compile-time interface check.
var _ Runtime = (*DockerRuntime)(nil)

// NewDockerRuntime creates a DockerRuntime connected to socketPath.
// Returns an error if the socket is unreachable (verified by pinging the API).
func NewDockerRuntime(socketPath, pythonImage, rImage string) (*DockerRuntime, error) {
	client := newDockerClient(socketPath)
	if err := client.get("/_ping", nil); err != nil {
		return nil, fmt.Errorf("docker socket %s: %w", socketPath, err)
	}
	return &DockerRuntime{client: client, pythonImage: pythonImage, rImage: rImage}, nil
}

func (r *DockerRuntime) Start(_ context.Context, p StartParams, logWriter io.Writer) (RunHandle, error) {
	image := r.imageForCommand(p.Command)

	labels := map[string]string{
		"shinyhub.managed":       "true",
		"shinyhub.slug":          p.Slug,
		"shinyhub.replica_index": strconv.Itoa(p.Index),
	}

	cfg := containerConfig{
		Image:   image,
		Cmd:     p.Command,
		Env:     append(filteredEnv(), p.Env...),
		WorkDir: "/app",
		Mounts: []containerMount{
			{Source: filepath.Clean(p.Dir), Target: "/app", Mode: "rw"},
		},
		Labels:      labels,
		NetworkMode: "host",
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
		return RunHandle{}, fmt.Errorf("create container for %s: %w", p.Slug, err)
	}

	if err := r.client.startContainer(id); err != nil {
		if err := r.client.removeContainer(id); err != nil {
			slog.Warn("docker cleanup container after failed start", "container", id, "err", err)
		}
		return RunHandle{}, fmt.Errorf("start container for %s: %w", p.Slug, err)
	}

	go r.streamLogs(id, logWriter)

	return RunHandle{ContainerID: id}, nil
}

func (r *DockerRuntime) Signal(handle RunHandle, sig syscall.Signal) error {
	sigStr := sigName(sig)
	if err := r.client.killContainer(handle.ContainerID, sigStr); err != nil {
		return fmt.Errorf("signal container %s: %w", handle.ContainerID, err)
	}
	return nil
}

func (r *DockerRuntime) Wait(ctx context.Context, handle RunHandle) error {
	return r.client.waitContainer(ctx, handle.ContainerID)
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
