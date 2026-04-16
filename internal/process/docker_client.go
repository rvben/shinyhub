package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// dockerClient is a minimal Docker Engine API client over a Unix socket.
// It implements only the operations ShinyHub needs.
type dockerClient struct {
	base   string       // e.g. "http://localhost" for Unix socket, or test server URL
	hc     *http.Client // for short-lived API calls (30s timeout)
	stream *http.Client // for long-lived streaming connections (no timeout)
}

// newDockerClient dials the Docker Unix socket at socketPath.
func newDockerClient(socketPath string) *dockerClient {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}
	hc := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{DialContext: dial},
	}
	stream := &http.Client{
		Transport: &http.Transport{DialContext: dial},
	}
	return &dockerClient{base: "http://localhost", hc: hc, stream: stream}
}

// containerConfig holds the subset of Docker container config ShinyHub uses.
type containerConfig struct {
	Image       string
	Cmd         []string
	Env         []string
	WorkDir     string
	Mounts      []containerMount
	Labels      map[string]string
	MemoryBytes int64  // 0 = unlimited
	NanoCPUs    int64  // 0 = unlimited; 1e9 = 1 CPU
	NetworkMode string
}

type containerMount struct {
	Source string
	Target string
	Mode   string // "rw" or "ro"
}

type containerState struct {
	Running bool
	Pid     int
}

type containerSummary struct {
	ID     string
	Labels map[string]string
	State  string
}

// createContainer creates a container and returns its ID.
func (c *dockerClient) createContainer(cfg containerConfig) (string, error) {
	mounts := make([]map[string]any, len(cfg.Mounts))
	for i, m := range cfg.Mounts {
		mounts[i] = map[string]any{
			"Type":     "bind",
			"Source":   m.Source,
			"Target":   m.Target,
			"ReadOnly": m.Mode == "ro",
		}
	}
	networkMode := cfg.NetworkMode
	if networkMode == "" {
		networkMode = "host"
	}
	body := map[string]any{
		"Image":      cfg.Image,
		"Cmd":        cfg.Cmd,
		"Env":        cfg.Env,
		"WorkingDir": cfg.WorkDir,
		"Labels":     cfg.Labels,
		"HostConfig": map[string]any{
			"Mounts":      mounts,
			"NetworkMode": networkMode,
			"Memory":      cfg.MemoryBytes,
			"NanoCPUs":    cfg.NanoCPUs,
		},
	}
	var resp struct {
		Id string `json:"Id"`
	}
	if err := c.post("/containers/create", body, &resp); err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	return resp.Id, nil
}

// startContainer starts a created container by ID.
func (c *dockerClient) startContainer(id string) error {
	return c.postEmpty(fmt.Sprintf("/containers/%s/start", id))
}

// removeContainer forcibly removes a container.
func (c *dockerClient) removeContainer(id string) error {
	url := fmt.Sprintf("%s/containers/%s?force=true", c.base, id)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("remove container: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("remove container: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remove container: status %d: %s", resp.StatusCode, body)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	return nil
}

// inspectContainer returns the running state of a container.
func (c *dockerClient) inspectContainer(id string) (containerState, error) {
	var resp struct {
		State struct {
			Running bool `json:"Running"`
			Pid     int  `json:"Pid"`
		} `json:"State"`
	}
	if err := c.get(fmt.Sprintf("/containers/%s/json", id), &resp); err != nil {
		return containerState{}, fmt.Errorf("inspect container: %w", err)
	}
	return containerState{Running: resp.State.Running, Pid: resp.State.Pid}, nil
}

// waitContainer blocks until the container exits. ctx cancellation aborts the wait.
func (c *dockerClient) waitContainer(ctx context.Context, id string) error {
	url := fmt.Sprintf("%s/containers/%s/wait?condition=not-running", c.base, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return fmt.Errorf("wait container: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("wait container: status %d: %s", resp.StatusCode, body)
	}
	var result struct {
		StatusCode int `json:"StatusCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("wait container: decode response: %w", err)
	}
	return nil
}

// containerStats returns CPU percent and RSS bytes for a running container.
// Uses the Docker one-shot stats endpoint (stream=false).
func (c *dockerClient) containerStats(ctx context.Context, id string) (float64, uint64, error) {
	url := fmt.Sprintf("%s/containers/%s/stats?stream=false", c.base, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("container stats: %w", err)
	}
	defer resp.Body.Close()

	var stats struct {
		CPUStats struct {
			CPUUsage    struct{ TotalUsage uint64 `json:"total_usage"` } `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs  int    `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPUStats struct {
			CPUUsage    struct{ TotalUsage uint64 `json:"total_usage"` } `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		MemoryStats struct {
			Usage uint64 `json:"usage"`
			Cache uint64 `json:"cache"` // v1 cgroups
			Stats struct {
				InactiveFile uint64 `json:"inactive_file"` // v2 cgroups
			} `json:"stats"`
		} `json:"memory_stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return 0, 0, fmt.Errorf("decode stats: %w", err)
	}

	// CPU percent calculation per Docker documentation.
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage) - float64(stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage) - float64(stats.PreCPUStats.SystemUsage)
	numCPU := stats.CPUStats.OnlineCPUs
	if numCPU == 0 {
		numCPU = 1
	}
	var cpuPercent float64
	if systemDelta > 0 {
		cpuPercent = (cpuDelta / systemDelta) * float64(numCPU) * 100.0
	}

	// RSS excludes page cache (not real memory pressure).
	rss := stats.MemoryStats.Usage
	if stats.MemoryStats.Cache > 0 {
		rss -= stats.MemoryStats.Cache
	} else if stats.MemoryStats.Stats.InactiveFile > 0 {
		rss -= stats.MemoryStats.Stats.InactiveFile
	}

	return cpuPercent, rss, nil
}

// listContainers returns containers matching the given filters JSON string.
// Example filtersJSON: `{"label":["shinyhub.slug"]}`
func (c *dockerClient) listContainers(filtersJSON string) ([]containerSummary, error) {
	reqURL := fmt.Sprintf("%s/containers/json?filters=%s", c.base, url.QueryEscape(filtersJSON))
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	defer resp.Body.Close()
	var raw []struct {
		ID     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
		State  string            `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode containers: %w", err)
	}
	out := make([]containerSummary, len(raw))
	for i, r := range raw {
		out[i] = containerSummary{ID: r.ID, Labels: r.Labels, State: r.State}
	}
	return out, nil
}

// killContainer sends a signal to a container. A 404 response means the
// container is already gone and is treated as a no-op.
func (c *dockerClient) killContainer(id string, sig string) error {
	path := fmt.Sprintf("/containers/%s/kill?signal=%s", id, sig)
	req, err := http.NewRequest(http.MethodPost, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("kill container: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("kill container: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("kill container: status %d: %s", resp.StatusCode, body)
}

// --- helpers ---

func (c *dockerClient) post(path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("post %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rawBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, rawBody)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *dockerClient) postEmpty(path string) error {
	req, err := http.NewRequest(http.MethodPost, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("post %s: %w", path, err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}
	return nil
}

func (c *dockerClient) get(path string, out any) error {
	resp, err := c.hc.Get(c.base + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
