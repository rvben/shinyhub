package api

// ReplicaStartRequest is the control-plane request to start (or run-once) a
// replica on a worker. The control plane allocates BindPort (the in-container
// listen port); the worker allocates its own host publish port and returns the
// reachable tunnel URL in ReplicaResult.
type ReplicaStartRequest struct {
	Slug             string            `json:"slug"`
	Index            int               `json:"index"`
	Tier             string            `json:"tier"`
	ContentDigest    string            `json:"content_digest"`
	AppVersion       string            `json:"app_version"`
	DeploymentID     int64             `json:"deployment_id"`
	Command          []string          `json:"command"`
	Env              map[string]string `json:"env,omitempty"`
	BindPort         int               `json:"bind_port"`
	SharedMountSlugs []string          `json:"shared_mount_slugs,omitempty"`
	MemoryLimitMB    int               `json:"memory_limit_mb,omitempty"`
	CPUQuotaPercent  int               `json:"cpu_quota_percent,omitempty"`
	// MaxSessions is the per-replica active-connection hard cap the worker enforces
	// in the data plane. 0 means no cap.
	MaxSessions int `json:"max_sessions,omitempty"`
}

// ReplicaResult identifies a started replica and its reachable URL. ContainerID
// is the worker-local container id; the control plane wraps it into an opaque
// "<node_id>/<container_id>" handle.
type ReplicaResult struct {
	NodeID      string `json:"node_id"`
	ContainerID string `json:"container_id"`
	URL         string `json:"url"`
}

// SignalRequest asks the worker to deliver a signal to a replica's container.
type SignalRequest struct {
	Signal int `json:"signal"`
}

// StatsResult reports a replica's current resource usage.
type StatsResult struct {
	CPUPercent float64 `json:"cpu_percent"`
	RSSBytes   uint64  `json:"rss_bytes"`
}

// ExitResult reports how a one-shot process exited. It is populated only by
// RunOnce; the long-running Wait path reports completion through error alone
// and does not surface an exit code.
type ExitResult struct {
	Code     int  `json:"code"`
	Signaled bool `json:"signaled"`
}

// InventoryItem is the wire form of a managed container in the agent inventory.
type InventoryItem struct {
	ContainerID string            `json:"container_id"`
	Labels      map[string]string `json:"labels"`
	Running     bool              `json:"running"`
	URL         string            `json:"url"`
}
