package process

// Label keys stamped on every managed replica container or task. All three
// backends (docker, remote_docker, fargate/ECS) write and read these same keys
// so the lifecycle layer can reconcile any backend identically.
const (
	// LabelManaged marks every resource launched by this control plane.
	// Value is always "true".
	LabelManaged = "shinyhub.managed"
	// LabelSlug is the app slug that owns the replica.
	LabelSlug = "shinyhub.slug"
	// LabelReplicaIndex is the zero-based replica index within the pool.
	LabelReplicaIndex = "shinyhub.replica_index"
	// LabelTier is the tier name the replica belongs to.
	LabelTier = "shinyhub.tier"
	// LabelProvider is the runtime provider name (e.g. "docker", "fargate").
	LabelProvider = "shinyhub.provider"
	// LabelDeploymentID is the deployment row ID that placed this replica.
	LabelDeploymentID = "shinyhub.deployment_id"
	// LabelAppVersion is the app version string stamped at deploy time.
	LabelAppVersion = "shinyhub.app_version"
	// LabelContentDigest is the SHA-256 content digest of the deployed bundle.
	LabelContentDigest = "shinyhub.content_digest"
	// LabelPort is the port the app binds inside the replica so recovery can
	// rebuild the full route URL from the resource alone.
	LabelPort = "shinyhub.port"
	// LabelMaxSessions is the per-replica active-connection hard cap persisted on
	// the container so re-adoption after an agent restart restores the same limit.
	// Value is the decimal integer cap; absent or unparseable means no cap.
	LabelMaxSessions = "shinyhub.max_sessions"
)
