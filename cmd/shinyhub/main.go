package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/appenv"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/autoscale"
	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/cli"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/data"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fargate"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/logging"
	"github.com/rvben/shinyhub/internal/metrics"
	"github.com/rvben/shinyhub/internal/oauth"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/secrets"
	"github.com/rvben/shinyhub/internal/servertrace"
	"github.com/rvben/shinyhub/internal/tracing"
	"github.com/rvben/shinyhub/internal/ui"
	"github.com/rvben/shinyhub/internal/worker"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/hkdf"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
// It defaults to "dev" for local builds. Propagated to internal/cli via
// cli.SetVersion in init().
var version = "dev"

var rootCmd = &cobra.Command{
	Use:     "shinyhub",
	Short:   "ShinyHub — self-hosted platform for deploying and managing Shiny apps",
	Version: version,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the ShinyHub server",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		logger := logging.New()
		slog.SetDefault(logger)
		return runServe(ctx, logger)
	},
}

var backupOut string

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Write a consistent snapshot of all durable state to an archive",
	Long: "Creates a .tar.gz containing a transactionally consistent SQLite\n" +
		"snapshot plus the apps and app-data dirs. Safe to run on a live server.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if backupOut == "" {
			return fmt.Errorf("--out is required")
		}
		cfg, err := config.Load(serverConfigPath())
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if err := backup.Create(cfg, version, backupOut); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "backup written to %s\n", backupOut)
		return nil
	},
}

var restoreCmd = &cobra.Command{
	Use:   "restore <archive>",
	Short: "Restore durable state from a backup archive (server must be stopped)",
	Long: "Restores the database, apps, and app-data from a backup archive.\n" +
		"Stop the server first. Existing state is moved aside with a\n" +
		"'.pre-restore-<timestamp>' suffix (never deleted) so you can roll back.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(serverConfigPath())
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		moved, err := backup.Restore(cfg, args[0])
		for _, p := range moved {
			fmt.Fprintf(os.Stdout, "previous state preserved at %s\n", p)
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "restore complete; start the server to apply any pending migrations")
		return nil
	},
}

// configPath holds the value of the --config flag (empty when unset). Bound on
// serve, backup, and restore so a config file can be selected without an env var.
var configPath string

// serverConfigPath resolves the server config file. Precedence: the --config
// flag, then the SHINYHUB_CONFIG env var, then ./shinyhub.yaml.
func serverConfigPath() string {
	if configPath != "" {
		return configPath
	}
	if v := os.Getenv("SHINYHUB_CONFIG"); v != "" {
		return v
	}
	return "shinyhub.yaml"
}

func init() {
	cli.SetVersion(version)
	backupCmd.Flags().StringVar(&backupOut, "out", "", "Destination archive path (.tar.gz)")
	const configUsage = "Path to the server config file (overrides SHINYHUB_CONFIG; default ./shinyhub.yaml)"
	for _, c := range []*cobra.Command{serveCmd, backupCmd, restoreCmd} {
		c.Flags().StringVar(&configPath, "config", "", configUsage)
	}
	rootCmd.AddCommand(serveCmd, backupCmd, restoreCmd, newWorkerCmd())
	cli.AddCommandsTo(rootCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(cli.ExitCode(err))
	}
}

// startMetricsListener binds addr and serves the Prometheus scrape endpoint at
// /metrics on its own listener, separate from the main application listener so
// server internals are never exposed on the public port. The returned server is
// already serving in a background goroutine; the caller is responsible for
// Shutdown. The listener is returned so callers can log the resolved address
// (useful when addr requests an ephemeral :0 port).
func startMetricsListener(addr string, reg *metrics.Registry) (*http.Server, net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("metrics listen %s: %w", addr, err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", reg.Handler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server", "err", err)
		}
	}()
	return srv, ln, nil
}

// buildRuntime constructs a process.Runtime for a single tier from its TierConfig.
// Docker tiers share the daemon settings from cfg.Runtime.Docker; a burst tier
// may therefore point at the same daemon under a distinct tier name. Fargate tiers
// share cfg.Runtime.Fargate (one ECS cluster) but may use different launch types.
// config.Load validates tier modes, so the default case is unreachable in production.
func buildRuntime(ctx context.Context, tier config.TierConfig, cfg *config.Config, bundleTokenKey []byte) (process.Runtime, error) {
	switch tier.Runtime {
	case "docker":
		dockerRT, err := process.NewDockerRuntime(
			cfg.Runtime.Docker.Socket,
			cfg.Runtime.Docker.Images.Python,
			cfg.Runtime.Docker.Images.R,
			cfg.Runtime.Docker.NetworkMode,
		)
		if err != nil {
			return nil, fmt.Errorf("docker runtime: %w", err)
		}
		return dockerRT, nil
	case "native":
		return process.NewNativeRuntime(), nil
	case "fargate":
		return buildFargateRuntime(ctx, cfg, tier, bundleTokenKey)
	case "remote_docker":
		// Handled upstream: remote tiers are registered via NewRemoteRuntime before
		// RegisterRuntime; buildRuntime is not called for remote_docker tiers.
		return nil, fmt.Errorf("buildRuntime called for remote_docker tier; wire via NewRemoteRuntime instead")
	default:
		return nil, fmt.Errorf("unsupported runtime mode: %s", tier.Runtime)
	}
}

// deriveBundleTokenKey derives the 32-byte key used to mint and verify Fargate
// bundle tokens. Key material comes from the same auth secret as all other
// HKDF derivations, but with a distinct info string so the bundle key is
// independent of the secrets-encryption key. Panics on failure (HKDF read of
// 32 bytes is infallible).
func deriveBundleTokenKey(authSecret string) []byte {
	r := hkdf.New(sha256.New, []byte(authSecret), nil, []byte("shinyhub-fargate-bundle-v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		panic(err)
	}
	return key
}

// deriveStickyCookieKey derives the HMAC key that signs the proxy's
// sticky-routing cookie, independent of the other HKDF-derived keys via its own
// info string. Panics on failure (HKDF read of 32 bytes is infallible).
func deriveStickyCookieKey(authSecret string) []byte {
	r := hkdf.New(sha256.New, []byte(authSecret), nil, []byte("shinyhub-sticky-cookie-v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		panic(err)
	}
	return key
}

// buildFargateRuntime constructs the ECS runtime for one fargate tier from the
// shared cfg.Runtime.Fargate settings and the tier's per-entry launch type.
// The launch type determines the workerID ("fargate" or "ecs-ec2") and whether
// PlatformVersion is set on RunTask. config.Load has already validated the
// required Fargate fields.
func buildFargateRuntime(ctx context.Context, cfg *config.Config, tier config.TierConfig, bundleTokenKey []byte) (process.Runtime, error) {
	fc := cfg.Runtime.Fargate
	var optFns []func(*awsconfig.LoadOptions) error
	if fc.Region != "" {
		optFns = append(optFns, awsconfig.WithRegion(fc.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("load aws config for fargate: %w", err)
	}

	// Resolve the SDK launch type from the tier's string value.
	lt := ecstypes.LaunchTypeFargate
	if tier.LaunchType == "EC2" {
		lt = ecstypes.LaunchTypeEc2
	}

	var opts []fargate.Option
	if fc.RouteViaPublicIP {
		// Out-of-VPC control plane: resolve task public IPs via EC2.
		opts = append(opts, fargate.WithEC2Client(ec2.NewFromConfig(awsCfg)))
	}
	if fc.SecretsNamePrefix != "" {
		// Route apps' secret env vars through AWS Secrets Manager (referenced by
		// ARN from a per-app task-def secrets block) instead of plaintext task
		// overrides, so they never appear in ecs:DescribeTasks.
		opts = append(opts, fargate.WithSecretsStore(
			fargate.NewSecretsManagerStore(secretsmanager.NewFromConfig(awsCfg), fc.SecretsKMSKeyID)))
	}
	return fargate.New(ecs.NewFromConfig(awsCfg), fargate.Config{
		Cluster:          fc.Cluster,
		TaskDefinition:   fc.TaskDefinition,
		ContainerName:    fc.ContainerName,
		Subnets:          fc.Subnets,
		SecurityGroups:   fc.SecurityGroups,
		AssignPublicIP:   fc.AssignPublicIP,
		PlatformVersion:  fc.PlatformVersion,
		RouteViaPublicIP: fc.RouteViaPublicIP,
		TaskCPUUnits:     int32(fc.TaskCPUUnits), // operator-configured task ceiling
		TaskMemoryMB:     int32(fc.TaskMemoryMB), // operator-configured task ceiling
		ControlPlaneURL:  fc.ControlPlaneURL,
		BundleTokenTTL:   fc.BundleTokenTTL,
		BundleTokenKey:   bundleTokenKey,
		LaunchType:       lt,
		SecretNamePrefix: fc.SecretsNamePrefix,
	}, slog.Default(), opts...), nil
}

// hostSampler samples PID-backed replicas (native) via gopsutil and reports
// PID-less replicas (docker/remote/fargate handles, whose RunHandle carries a
// ContainerID rather than a PID) as zero usage without error. Returning no error
// for the PID-less case is deliberate: the status endpoint treats a sampler error
// as a dead replica, so a running replica on a container/fargate tier must not be
// probed by PID (PID 0) and misreported as stopped.
type hostSampler struct{ gops process.GopsutilSampler }

func (h *hostSampler) Sample(handle process.RunHandle) (process.Stats, error) {
	if handle.PID == 0 {
		return process.Stats{}, nil
	}
	return h.gops.Sample(handle)
}

func runServe(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.Load(serverConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := os.MkdirAll(cfg.Storage.AppsDir, 0o750); err != nil {
		return fmt.Errorf("create apps dir: %w", err)
	}

	if err := os.MkdirAll(cfg.Storage.AppDataDir, 0o750); err != nil {
		return fmt.Errorf("create app-data dir: %w", err)
	}

	// Normalize the configured app-data dir to an absolute path once, here at
	// the call site, and pass the absolute value to every consumer (the
	// SharedMounts resolver, process.Manager.SetAppDataRoot, jobs.NewManager).
	// This guarantees that long-running apps and scheduled jobs agree on the
	// same on-disk location regardless of where the server's CWD ends up,
	// closing the bug where a relative storage.app_data_dir caused schedules
	// to write into <bundle>/data/<rel-path>/<slug>/ rather than the
	// persistent data dir.
	absAppDataDir, err := filepath.Abs(cfg.Storage.AppDataDir)
	if err != nil {
		return fmt.Errorf("resolve app data dir: %w", err)
	}

	sweepOrphanTempfiles(absAppDataDir)

	store, err := db.Open(cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	// The database holds password and API-key hashes and the audit log; keep the
	// file owner-only. Best-effort and only for a real on-disk file (plain-path
	// DSN), not an in-memory or parameterised DSN.
	if !strings.Contains(cfg.Database.DSN, ":memory:") {
		if _, statErr := os.Stat(cfg.Database.DSN); statErr == nil {
			_ = os.Chmod(cfg.Database.DSN, 0o600)
		}
	}
	defer func() {
		if err := store.Close(); err != nil {
			slog.Warn("store close", "err", err)
		}
	}()
	if err := store.Migrate(); err != nil {
		return fmt.Errorf("db migrate: %w", err)
	}

	var (
		workerCA  *worker.CA
		workerReg *worker.Registry
	)
	if cfg.Worker.Enabled {
		ca, reg, err := startWorkerHosting(ctx, logger, cfg, store)
		if err != nil {
			return err
		}
		workerCA = ca
		workerReg = reg
	}

	var dialer worker.AgentDialer
	if workerCA != nil {
		d, err := worker.NewMTLSDialer(workerCA.ControlClientCertificate, workerCA.Pool())
		if err != nil {
			return fmt.Errorf("control client cert: %w", err)
		}
		dialer = d
	}

	secretsKey := secrets.DeriveKey(cfg.Auth.Secret)
	bundleTokenKey := deriveBundleTokenKey(cfg.Auth.Secret)

	// readyCh is closed once HTTP listener is live. /readyz returns 503 until then.
	readyCh := make(chan struct{})

	// Bootstrap admin user from env if provided and no users exist
	if adminUser := os.Getenv("SHINYHUB_ADMIN_USER"); adminUser != "" {
		adminPass := os.Getenv("SHINYHUB_ADMIN_PASSWORD")
		if adminPass == "" {
			return fmt.Errorf("SHINYHUB_ADMIN_PASSWORD must not be empty when SHINYHUB_ADMIN_USER is set")
		}
		_, err := store.GetUserByUsername(adminUser)
		if errors.Is(err, db.ErrNotFound) {
			hash, err := auth.HashPassword(adminPass)
			if err != nil {
				return fmt.Errorf("hash admin password: %w", err)
			}
			if err := store.CreateUser(db.CreateUserParams{
				Username:     adminUser,
				PasswordHash: hash,
				Role:         "admin",
			}); err != nil {
				slog.Warn("could not create admin user", "err", err)
			} else {
				slog.Info("admin user created", "username", adminUser)
			}
		} else if err != nil {
			return fmt.Errorf("check admin user: %w", err)
		}
	}

	var deployToken *auth.DeployToken
	if cfg.Auth.DeployToken != "" {
		if err := auth.ValidateDeployTokenFormat(cfg.Auth.DeployToken); err != nil {
			return fmt.Errorf("SHINYHUB_DEPLOY_TOKEN: %w", err)
		}
		sysUser, err := store.UpsertSystemUser(db.SystemUsernameDeploy, cfg.Auth.DeployTokenRole)
		if err != nil {
			return fmt.Errorf("upsert deploy system user: %w", err)
		}
		deployToken = auth.NewDeployToken(cfg.Auth.DeployToken, &auth.ContextUser{
			ID:       sysUser.ID,
			Username: sysUser.Username,
			Role:     sysUser.Role,
		})
		slog.Info("deploy token registered",
			"username", sysUser.Username,
			"role", sysUser.Role)
	}

	// Build one runtime per configured tier. The first tier is the default
	// (config.Load synthesizes a single "local" tier from Mode when none are
	// declared, so single-node behavior is unchanged). The default tier's
	// runtime backs the Manager and every runtime-typed consumer (sampler,
	// jobs, recovery lister, sweeper); additional tiers are registered so
	// placement can route replicas to them.
	defaultTier := cfg.Runtime.DefaultTierName()
	// Find the full TierConfig for the default tier so LaunchType is carried through.
	defaultTierCfg := config.TierConfig{Name: defaultTier, Runtime: cfg.Runtime.Mode}
	for _, t := range cfg.Runtime.Tiers {
		if t.Name == defaultTier {
			defaultTierCfg = t
			break
		}
	}
	rt, err := buildRuntime(ctx, defaultTierCfg, cfg, bundleTokenKey)
	if err != nil {
		return err
	}
	slog.Info("runtime configured", "tier", defaultTier, "mode", defaultTierCfg.Runtime)
	mgr := process.NewManager(cfg.Storage.AppsDir, rt)
	mgr.SetDefaultTier(defaultTier)
	for _, tierCfg := range cfg.Runtime.Tiers {
		if tierCfg.Name == defaultTier {
			continue
		}
		if tierCfg.Runtime == "remote_docker" {
			if workerReg == nil || dialer == nil {
				return fmt.Errorf("tier %q requires worker hosting to be enabled", tierCfg.Name)
			}
			mgr.RegisterRuntime(tierCfg.Name, worker.NewRemoteRuntime(workerReg, tierCfg.Name, dialer))
			slog.Info("remote runtime tier registered", "tier", tierCfg.Name, "mode", tierCfg.Runtime)
			continue
		}
		tierRT, err := buildRuntime(ctx, tierCfg, cfg, bundleTokenKey)
		if err != nil {
			return err
		}
		mgr.RegisterRuntime(tierCfg.Name, tierRT)
		slog.Info("runtime tier registered", "tier", tierCfg.Name, "mode", tierCfg.Runtime)
	}
	mgr.SetEnvResolver(func(slug string) ([]string, []string, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, nil, err
		}
		vars, err := store.ListAppEnvVars(app.ID)
		if err != nil {
			return nil, nil, err
		}
		env, secretEnv, err := appenv.Resolve(vars, secretsKey)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve env for app %s: %w", slug, err)
		}
		return env, secretEnv, nil
	})
	mgr.SetSharedMountResolver(func(slug string) ([]process.SharedMount, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, err
		}
		rows, err := store.ListSharedDataSources(app.ID)
		if err != nil {
			return nil, err
		}
		out := make([]process.SharedMount, 0, len(rows))
		for _, m := range rows {
			out = append(out, process.SharedMount{
				SourceSlug: m.SourceSlug,
				HostPath:   filepath.Join(absAppDataDir, m.SourceSlug),
			})
		}
		return out, nil
	})
	if err := mgr.SetAppDataRoot(absAppDataDir); err != nil {
		return fmt.Errorf("set app data root: %w", err)
	}

	// Tracing: shared ring buffer surfaced by the /traces handler, plus
	// platform-default OTEL_* env vars injected into every app process so
	// Shiny's built-in OpenTelemetry exporter reaches the configured backend
	// without per-app configuration. Both are no-ops when cfg.Tracing.Enabled
	// is false.
	traceBuffer := tracing.NewBuffer(cfg.Tracing.RingBufferSize, time.Duration(cfg.Tracing.SlowRequestMS)*time.Millisecond)
	mgr.SetPlatformDefaultEnvResolver(func(slug string, replica int) []string {
		return tracing.EnvFor(cfg.Tracing, slug, replica)
	})

	prx := proxy.New()
	prx.SetTracing(cfg.Tracing, traceBuffer)
	// Trust forwarding headers only from the configured upstream proxies; a
	// direct client's X-Forwarded-* / Forwarded values are stripped before the
	// request reaches an app backend.
	prx.SetTrustedProxies(cfg.TrustedProxyNets)
	// Sign the sticky-routing cookie so a client cannot forge it to pin a
	// replica and bypass the per-replica session cap.
	prx.SetStickySecret(deriveStickyCookieKey(cfg.Auth.Secret))

	srv := api.New(cfg, store, mgr, prx)
	srv.SetVersion(version)
	if deployToken != nil {
		srv.SetDeployToken(deployToken)
	}
	srv.SetSecretsKey(secretsKey)
	srv.SetTraceBuffer(traceBuffer)

	// When the Fargate secrets backend is configured, wire the per-app cleanup
	// (delete Secrets Manager entries + deregister task-def revisions) that runs
	// on app delete and on the startup tombstone reconcile. Every Fargate runtime
	// instance shares the cluster-wide secrets config, so the first one suffices.
	var fargateSecretsCleaner *fargate.Runtime
	if cfg.Runtime.Fargate.SecretsNamePrefix != "" {
		for _, tierName := range cfg.Runtime.TierOrder() {
			if frt, ok := mgr.RuntimeForTier(tierName).(*fargate.Runtime); ok {
				fargateSecretsCleaner = frt
				break
			}
		}
	}
	if fargateSecretsCleaner != nil {
		srv.SetSecretsCleaner(fargateSecretsCleaner)
	}
	if workerReg != nil {
		srv.SetNodeForTier(func(tier string) string {
			if w, ok := workerReg.WorkerForTier(tier); ok {
				return w.NodeID
			}
			return ""
		})
		srv.SetWorkerRegistry(workerReg)
	}

	// Server self-telemetry: when enabled, instrument the API router and serve
	// the Prometheus scrape endpoint on its own loopback-by-default listener.
	var metricsSrv *http.Server
	var metricsReg *metrics.Registry
	if cfg.Metrics.Enabled {
		reg := metrics.New(version)
		metricsReg = reg
		srv.SetMetrics(reg)
		prx.SetRejectRecorder(reg)
		// Fleet gauges read live counts from the store at scrape time, so
		// "apps/replicas running right now" is answerable from Prometheus alone.
		reg.RegisterFleetGauges(
			func() float64 {
				n, err := store.CountRunningApps()
				if err != nil {
					return 0
				}
				return float64(n)
			},
			func() float64 {
				n, err := store.CountRunningReplicas()
				if err != nil {
					return 0
				}
				return float64(n)
			},
		)
		var mln net.Listener
		metricsSrv, mln, err = startMetricsListener(cfg.Metrics.Addr, reg)
		if err != nil {
			return err
		}
		slog.Info("metrics listening", "addr", mln.Addr().String())
	}

	// Server tracing: when the existing tracing config is enabled, emit OTel
	// spans for control-plane API request handling to the same OTLP endpoint the
	// managed apps export to. The exporter connects lazily, so Setup never blocks
	// on the collector being reachable.
	var tracer *servertrace.Tracer
	if cfg.Tracing.Enabled {
		tracer, err = servertrace.Setup(ctx, cfg.Tracing, version)
		if err != nil {
			return fmt.Errorf("server tracing setup: %w", err)
		}
		srv.SetTracer(tracer)
		slog.Info("server tracing enabled", "endpoint", cfg.Tracing.OTLPEndpoint, "protocol", cfg.Tracing.OTLPProtocol)
	}

	// Emit a structured access log for every proxied app request. Using the
	// Server's trusted-proxy-aware ClientIP keeps the "client" field honest
	// when shinyhub itself sits behind an edge proxy; this is independent of
	// anything the backend app (uvicorn/httpuv) chooses to print in its own
	// log and gives operators a reliable per-slug audit trail.
	prx.SetClientIPResolver(srv.ClientIP)
	// Distinguish unknown-slug requests from hibernated-app requests so the
	// proxy returns 404 for typos / deleted apps instead of looping the
	// loading page indefinitely. The lookup hits SQLite (cached page) and
	// only runs on miss, so the cost is negligible.
	//
	// We distinguish "row missing" (db.ErrNotFound → return false, nil → 404)
	// from "lookup itself failed" (DB unavailable, ctx cancelled → return
	// false, err → fall through to loading page). Conflating them would 404
	// a real app whenever SQLite hiccupped, masking transient infra issues
	// as a permanent "deleted app" UX.
	prx.SetSlugExists(func(slug string) (bool, error) {
		_, err := store.GetAppBySlug(slug)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, db.ErrNotFound) {
			return false, nil
		}
		slog.Warn("proxy_slug_lookup_failed", "slug", slug, "error", err.Error())
		return false, err
	})
	prx.SetAccessLogger(func(e proxy.AccessLogEntry) {
		attrs := []any{
			"slug", e.Slug,
			"method", e.Method,
			"path", e.Path,
			"status", e.Status,
			"bytes", e.Bytes,
			"duration_ms", e.Duration.Milliseconds(),
			"client_ip", e.ClientIP,
			"peer", e.Peer,
		}
		if e.ReplicaIndex >= 0 {
			attrs = append(attrs, "replica", e.ReplicaIndex, "sticky", e.Sticky)
		}
		if e.Reject != "" {
			attrs = append(attrs, "reject", string(e.Reject))
		}
		slog.Info("proxy_access", attrs...)
	})

	// Choose the metrics sampler. A docker default tier samples container stats
	// through the Runtime API. Otherwise use the host sampler, which reads host
	// PIDs for native replicas and reports PID-less replicas (fargate/remote
	// container handles) as zero usage without error, so a running replica on
	// such a tier is never misreported as stopped by a failed PID probe.
	// Compare the default tier's resolved runtime rather than cfg.Runtime.Mode
	// (the legacy field): a config that declares tiers:[{runtime:docker}]
	// without setting runtime.mode must still pick the RuntimeSampler, not the
	// host sampler.
	if defaultTierCfg.Runtime == "docker" {
		srv.SetSampler(&process.RuntimeSampler{Runtime: rt})
	} else {
		srv.SetSampler(&hostSampler{})
	}

	if cfg.OAuth.OIDC.IssuerURL != "" {
		oidcCtx, oidcCancel := context.WithTimeout(context.Background(), 15*time.Second)
		p, err := oauth.NewOIDCProvider(oidcCtx,
			cfg.OAuth.OIDC.IssuerURL,
			cfg.OAuth.OIDC.ClientID,
			cfg.OAuth.OIDC.ClientSecret,
			cfg.OAuth.OIDC.CallbackURL,
			cfg.OAuth.OIDC.DisplayName,
		)
		oidcCancel()
		if err != nil {
			return fmt.Errorf("oidc init: %w", err)
		}
		srv.SetOIDCProvider(p)
		slog.Info("oidc configured", "display_name", cfg.OAuth.OIDC.DisplayName, "issuer", cfg.OAuth.OIDC.IssuerURL)
	}
	if cfg.Auth.ForwardAuth.Enabled {
		slog.Info("forward auth configured",
			"user_header", cfg.Auth.ForwardAuth.UserHeader,
			"groups_header", cfg.Auth.ForwardAuth.GroupsHeader,
			"admin_groups", cfg.Auth.ForwardAuth.AdminGroups,
			"default_role", cfg.Auth.ForwardAuth.DefaultRole,
		)
	}

	deployFn := func(slug, bundleDir string, index int) (*deploy.Result, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, fmt.Errorf("get app for deploy: %w", err)
		}
		deployDefaultMem, deployDefaultCPU := cfg.Runtime.DefaultResourcesForApp(app)
		p := deploy.Params{
			Slug:                  slug,
			AppID:                 app.ID,
			BundleDir:             bundleDir,
			Manager:               mgr,
			Proxy:                 prx,
			Replicas:              app.Replicas,
			Placement:             app.PlacementMap(),
			TierOrder:             cfg.Runtime.TierOrder(),
			DefaultTier:           cfg.Runtime.DefaultTierName(),
			MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, deployDefaultMem),
			CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, deployDefaultCPU),
			MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, cfg.Runtime.DefaultMaxSessionsPerReplica),
			// Pin a shared-mount consumer's restarted replica to the worker set
			// hosting its source data, matching the full-deploy placement so a
			// recovered replica lands beside the data it mounts.
			ColocateWorkers: srv.ColocationPins(app),
		}
		if deps, derr := store.ListDeployments(app.ID); derr == nil && len(deps) > 0 {
			p.ContentDigest = deps[0].ContentDigest
			p.DeploymentID = deps[0].ID
			p.AppVersion = deps[0].Version
		}
		return deploy.RunReplica(p, index)
	}

	lcCfg := lifecycle.Config{
		WatchInterval:                cfg.Lifecycle.WatchInterval,
		RestartMaxAttempts:           cfg.Lifecycle.RestartMaxAttempts,
		HibernateTimeout:             cfg.Lifecycle.HibernateTimeout,
		DefaultMaxSessionsPerReplica: cfg.Runtime.DefaultMaxSessionsPerReplica,
	}
	watcher := lifecycle.New(lcCfg, mgr, prx, store, deployFn)

	// Record hibernate/wake transitions and crash-restart counts when metrics
	// are enabled. Nil-safe inside the watcher when metrics are disabled.
	if metricsReg != nil {
		watcher.SetMetrics(metricsReg)
	}
	// Wire Fargate operation metrics for every tier that uses a Fargate runtime.
	// metricsReg satisfies fargate.FargateMetrics via the methods added to Registry.
	if metricsReg != nil {
		for _, tierName := range cfg.Runtime.TierOrder() {
			tierRT := mgr.RuntimeForTier(tierName)
			if frt, ok := tierRT.(*fargate.Runtime); ok {
				frt.SetMetrics(metricsReg)
			}
		}
	}
	// Emit spans for background wake/restart/hibernate operations into the same
	// provider the API server uses, so cold-start latency and restart storms are
	// visible in the trace backend.
	if tracer != nil {
		watcher.SetTracer(tracer.Tracer())
	}

	// Wire lost-replica healing. For fargate tiers, ECS reachability is
	// validated at startup so the gate is always-true (fargate tasks are
	// never placed via a worker registry). For worker tiers, delegate to
	// the registry. Mixed deployments (fargate burst + worker tiers) are
	// handled by the combined predicate.
	//
	// Note: persistent RunTask failures still consume the normal crash-restart
	// budget, so always-true for fargate tiers cannot cause runaway
	// re-placement.
	hasFargateTier := false
	for _, name := range cfg.Runtime.TierOrder() {
		mode, _ := cfg.Runtime.RuntimeForTier(name)
		if mode == "fargate" {
			hasFargateTier = true
			break
		}
	}
	if workerReg != nil || hasFargateTier {
		watcher.EnableLostReplicaHealing(func(tier string) bool {
			mode, _ := cfg.Runtime.RuntimeForTier(tier)
			if mode == "fargate" {
				return true // ECS is always-reachable as a startup precondition
			}
			if workerReg != nil {
				_, ok := workerReg.WorkerForTier(tier)
				return ok
			}
			return false
		})
	}

	// Fail any deploy interrupted mid-flight before recovery so adoption falls
	// back to the last good deployment.
	lifecycle.ReconcileInflightDeployments(store)

	// Finish any app deletion interrupted between the 'deleting' tombstone and
	// the row removal, then report (do not delete) slug dirs with no owning
	// row. Reconcile first so freshly-cleaned slugs are not flagged as orphans.
	var reconcileCleaner lifecycle.AppSecretsCleaner
	if fargateSecretsCleaner != nil {
		reconcileCleaner = fargateSecretsCleaner
	}
	lifecycle.ReconcileDeletingApps(ctx, store, cfg, reconcileCleaner)
	lifecycle.LogOrphanAppDirs(store, cfg)

	// Re-adopt any processes that survived a server restart. Recovery routes
	// each replica to its tier's runtime via the Manager's registry, so it
	// needs no runtime argument here.
	lifecycle.RecoverProcesses(store, mgr, prx, cfg.Runtime.DefaultMaxSessionsPerReplica)

	// Remove ShinyHub-managed app containers no live replica re-adopted, so
	// stopped containers from prior runs do not accumulate.
	if sweeper, ok := rt.(lifecycle.ContainerSweeper); ok {
		lifecycle.SweepOrphanContainers(mgr, sweeper)
	}

	// Sweep orphan Fargate tasks across ALL registered tiers.
	// The existing SweepOrphanContainers only covers the default-tier runtime;
	// a Fargate burst tier declared via runtime.tiers[] would never be swept
	// without this loop.
	{
		sweepCtx, cancelSweep := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelSweep()
		for _, tierName := range cfg.Runtime.TierOrder() {
			tierRT := mgr.RuntimeForTier(tierName)
			if fts, ok := tierRT.(lifecycle.FargateTaskSweeper); ok {
				lifecycle.SweepOrphanFargateTasks(sweepCtx, mgr, fts)
			}
		}
	}

	// Start the worker-down monitor: when a worker's heartbeat goes stale it is
	// marked down and each of its running replicas transitions to lost and is
	// removed from proxy routing. Only relevant when worker hosting is enabled.
	if workerReg != nil {
		const (
			workerTimeout   = 90 * time.Second
			monitorInterval = 30 * time.Second
			// workerRetention is how long a down, non-revoked worker row with no
			// live replicas is kept before being reaped. It is far longer than the
			// down timeout so a briefly-flapping worker is never tombstoned.
			workerRetention = 24 * time.Hour
		)
		monitor := lifecycle.NewWorkerDownMonitor(store, workerTimeout, workerRetention, workerReg.MarkDown, func(slug string, index int, expectURL string) {
			prx.DeregisterReplicaIfTarget(slug, index, expectURL)
		}, mgr.EvictReplicaIfWorker, workerReg.Forget)
		go monitor.Run(ctx, monitorInterval)
		slog.Info("worker-down monitor started", "timeout", workerTimeout, "interval", monitorInterval, "retention", workerRetention)
	}

	watcherCtx, cancelWatcher := context.WithCancel(context.Background())
	defer cancelWatcher()
	watcherDone := make(chan struct{})
	go func() {
		watcher.Start(watcherCtx)
		close(watcherDone)
	}()

	jobsMgr, err := jobs.NewManager(mgr, cfg.Runtime.TierOrder(), cfg.Runtime.DefaultTierName(), store, secretsKey, cfg.Storage.AppsDir, absAppDataDir)
	if err != nil {
		return fmt.Errorf("init jobs manager: %w", err)
	}
	sched := scheduler.New(jobsMgr, store, cfg.Scheduler.Location)

	schedCtx, cancelSched := context.WithCancel(context.Background())
	defer cancelSched()
	if err := sched.Start(schedCtx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}
	slog.Info("scheduler started")

	srv.SetJobs(jobsMgr, sched)

	// Replica autoscaling is opt-in per app and gated by a global switch. When
	// enabled, the controller evaluates opted-in apps on its own interval and
	// drives the same incremental scale primitives the API exposes; it never
	// scales worker hosts.
	var (
		cancelAutoscale context.CancelFunc
		autoscaleDone   chan struct{}
	)
	if cfg.Runtime.Autoscale.Enabled {
		var asCtx context.Context
		asCtx, cancelAutoscale = context.WithCancel(context.Background())
		defer cancelAutoscale()
		runtimeMax := cfg.Runtime.MaxReplicas
		if runtimeMax <= 0 {
			runtimeMax = 32
		}
		// Look back twice the scan interval so a saturated pool is still observed
		// between ticks, but never beyond the cooldown: a stale saturation event
		// must not bias successive actions once a fresh decision is allowed.
		rejectWindow := 2 * cfg.Runtime.Autoscale.ScanInterval
		if rejectWindow > cfg.Runtime.Autoscale.Cooldown {
			rejectWindow = cfg.Runtime.Autoscale.Cooldown
		}
		controller := autoscale.New(autoscale.Config{
			ScanInterval:  cfg.Runtime.Autoscale.ScanInterval,
			Cooldown:      cfg.Runtime.Autoscale.Cooldown,
			DrainGrace:    30 * time.Second,
			RejectWindow:  rejectWindow,
			DefaultTarget: cfg.Runtime.Autoscale.DefaultTarget,
			DefaultCap:    cfg.Runtime.DefaultMaxSessionsPerReplica,
			RuntimeMax:    runtimeMax,
		}, store, prx, srv, store, slog.Default())
		if metricsReg != nil {
			controller.SetMetrics(metricsReg)
		}
		autoscaleDone = make(chan struct{})
		go func() {
			controller.Run(asCtx)
			close(autoscaleDone)
		}()
		slog.Info("autoscale controller started",
			"scan_interval", cfg.Runtime.Autoscale.ScanInterval,
			"cooldown", cfg.Runtime.Autoscale.Cooldown,
			"default_target", cfg.Runtime.Autoscale.DefaultTarget)
	}

	mux := http.NewServeMux()
	// Observe wraps the timeout handler (not the inner router) so server metrics
	// and traces record the status/latency the client actually sees, including
	// timeout 503s and recovered-panic 500s. It is a no-op unless metrics or
	// tracing are enabled.
	mux.Handle("/api/", srv.Observe(apiTimeoutHandler(srv.Router())))
	// Re-resolve JWT-claimed users against the live DB on every /app/* hit
	// so role demotions and account deletions take effect immediately.
	// Without this an admin's still-valid JWT keeps the admin-bypass path
	// open through the access middleware until the token expires — the same
	// staleness bug the API middleware already guards against via its own
	// userLookup wiring (see internal/api/router.go).
	appUserLookup := func(id int64) (*auth.ContextUser, error) {
		u, err := store.GetUserByID(id)
		if err != nil {
			return nil, err
		}
		return &auth.ContextUser{ID: u.ID, Username: u.Username, Role: u.Role}, nil
	}
	emptyState := access.NeverDeployedMiddleware(store, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup, cfg.TrustedProxyNets)(prx)
	appHandler := access.Middleware(store, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup)(emptyState)
	mux.Handle("/app/", appHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-readyCh:
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ready":false,"reason":"starting"}`))
			return
		}
		pingCtx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
		defer cancel()
		if err := store.PingContext(pingCtx); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ready":false,"reason":"db"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ready":true}`))
	})
	mux.Handle("/static/", ui.Handler())

	// GET /internal/fargate-bundle/{digest} - streams the bundle zip to a Fargate
	// task that presents a short-lived HMAC capability token as a bearer credential.
	// Mounted directly on the main mux (not under /api/) so large bundle streams
	// are not subject to apiTimeoutHandler's 30-second cap.
	fargateBundleHandler := api.NewFargateBundleHandler(store, cfg.Storage.AppsDir, bundleTokenKey)
	internalMux := chi.NewRouter()
	internalMux.Get("/fargate-bundle/{digest}", fargateBundleHandler.Handle)
	mux.Handle("/internal/", http.StripPrefix("/internal", internalMux))

	registerBrandingRoutes(mux, cfg, srv, store, appUserLookup)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           api.SecurityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "version", version, "addr", addr)
		close(readyCh)
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			cancelSched()
			cancelWatcher()
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown", "err", err)
	}
	if metricsSrv != nil {
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("metrics shutdown", "err", err)
		}
	}
	if tracer != nil {
		if err := tracer.Shutdown(shutdownCtx); err != nil {
			slog.Warn("tracer shutdown", "err", err)
		}
	}
	cancelSched()
	sched.Stop()
	// Cron is stopped; cancel any in-flight scheduled runs and wait for
	// them to finalize their DB rows before we close the store.
	jobsMgr.Stop(shutdownCtx)
	cancelWatcher()
	<-watcherDone
	// Stop the autoscale controller and wait for its loop to exit before the
	// store closes, so it cannot issue a scale query against a torn-down store.
	if cancelAutoscale != nil {
		cancelAutoscale()
		<-autoscaleDone
	}

	switch cfg.Server.ShutdownApps {
	case "stop":
		slog.Info("stopping app processes (server.shutdown_apps=stop)")
		if err := mgr.StopAll(); err != nil {
			slog.Warn("stop app processes", "err", err)
		}
	default: // "adopt"
		slog.Info("leaving app processes running for re-adoption (server.shutdown_apps=adopt)")
	}

	slog.Info("shutdown complete")
	return nil
}

// registerBrandingRoutes wires all branding-aware frontend routes onto mux.
// It is extracted from runServe so the integration test can call the identical
// production registration logic without duplicating the wiring.
//
// Routes registered:
//   - /apps/, /users, /audit-log, /login  - SPA shell (branded or stock)
//   - /branding/                          - operator asset files (only when assets present)
//   - /.shinyhub/branding.json            - public branding metadata
//   - /.shinyhub/apps.json                - app list (optional auth)
//   - /                                   - landing page or SPA shell
func registerBrandingRoutes(mux *http.ServeMux, cfg *config.Config, srv *api.Server, store *db.Store, appUserLookup auth.UserLookup) {
	brandingActive := cfg.Branding.IsActive()
	resolved := cfg.Branding.ResolvedAssets()

	// serveShell serves the stock SPA shell: byte-for-byte the existing
	// ServeFileFS path when branding is inactive (preserving Last-Modified /
	// ETag / Range / conditional-GET and SHINYHUB_DEV_STATIC live reload),
	// or the branded render when active (re-reading index.html per request so
	// dev live-reload still works).
	pub := ui.PublicBranding(cfg.Branding, resolved)
	serveShell := func(w http.ResponseWriter, r *http.Request) {
		if !brandingActive {
			http.ServeFileFS(w, r, ui.Static(), "index.html")
			return
		}
		raw, err := fs.ReadFile(ui.Static(), "index.html")
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		out, err := ui.RenderIndex(raw, pub)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(out)
	}

	// SPA routes: /apps/<slug>..., /users, /audit-log, /login. The handler
	// 404s anything outside the IsUIPath allowlist, so legitimate unknowns
	// still return 404 rather than rendering the SPA shell.
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ui.IsUIPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		serveShell(w, r)
	})
	mux.Handle("/apps/", spa)
	mux.Handle("/users", spa)
	mux.Handle("/workers", spa)
	mux.Handle("/audit-log", spa)
	mux.Handle("/login", spa) // always serves the SPA shell, even when landing_page is set

	if brandingActive && len(resolved) > 0 {
		mux.Handle("/branding/", ui.BrandingAssetHandler(resolved))
	}

	// /.shinyhub/* routes use optional-identity: resolve a session/bearer user
	// if present, but do NOT 401 anonymous callers. This mirrors the /app/*
	// userLookup wiring.
	optionalUser := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if u := access.ResolveOptionalUser(r, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup); u != nil {
				r = r.WithContext(auth.WithUser(r.Context(), u))
			}
			next(w, r)
		}
	}
	mux.HandleFunc("/.shinyhub/branding.json", srv.HandleBrandingJSON)
	mux.HandleFunc("/.shinyhub/apps.json", optionalUser(srv.HandleAppsJSON))

	landingFile := cfg.Branding.LandingFile()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if landingFile != "" {
			http.ServeFile(w, r, landingFile)
			return
		}
		serveShell(w, r)
	})
}

// sweepOrphanTempfiles removes stale entries from each app's
// .shinyhub-upload-tmp/ directory left behind by interrupted PUT uploads.
// Failures are logged and otherwise ignored — startup must succeed even when
// a single app's data dir is unreadable.
func sweepOrphanTempfiles(appDataRoot string) {
	entries, err := os.ReadDir(appDataRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("sweep app-data dir", "err", err)
		}
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		appDir := filepath.Join(appDataRoot, e.Name())
		if err := data.CleanupUploadTemp(appDir, time.Hour); err != nil {
			slog.Warn("sweep upload temp", "slug", e.Name(), "err", err)
		}
	}
}

// apiTimeoutHandler wraps the API router with a 30s per-request timeout,
// exempting routes that are either long-lived by design or stream a
// large request body. See isLongLivedAPIRoute for the matrix.
func apiTimeoutHandler(h http.Handler) http.Handler {
	timed := http.TimeoutHandler(h, 30*time.Second, `{"error":"request timeout"}`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLongLivedAPIRoute(r.Method, r.URL.Path) {
			h.ServeHTTP(w, r)
			return
		}
		timed.ServeHTTP(w, r)
	})
}

// isLongLivedAPIRoute reports whether method+path identifies an API
// route that must bypass the per-request timeout. Matching is exact on
// both the HTTP method and the full /api/apps/{slug}/... shape so an
// unrelated route that merely ends in one of these words (or uses a
// different method) keeps the 30s timeout. Qualifying routes:
//
//   - GET /api/apps/{slug}/logs and
//     GET /api/apps/{slug}/schedules/{id}/runs/{run_id}/logs —
//     server-sent log streams that stay open by design.
//   - POST /api/apps/{slug}/deploy — bundle upload, body can be
//     hundreds of MB.
//   - POST /api/apps/{slug}/restart, POST|PUT /api/apps/{slug}/rollback,
//     POST /api/apps/{slug}/stop — pool swaps that stop and relaunch
//     every replica under the per-slug deploy lock. These can
//     legitimately exceed 30s (dependency-heavy launches). Letting
//     http.TimeoutHandler fire would return "request timeout" to the
//     client while the swap keeps mutating runtime + DB state, leaving
//     the two divergent and the operator misinformed.
//   - PUT /api/apps/{slug}/data/<rel> — per-app data upload, also
//     arbitrary-size. Without this exemption http.TimeoutHandler swaps
//     the response writer mid-stream at 30s; the handler keeps writing
//     to a now-disconnected recorder, the file may still complete on
//     disk, and the client sees an ambiguous "request timeout" body
//     instead of either a clean success or a clean failure.
//
// All other API routes keep the 30s timeout so a slow handler cannot
// pin a server goroutine indefinitely.
func isLongLivedAPIRoute(method, path string) bool {
	const prefix = "/api/apps/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := path[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return false // bare /api/apps/{slug}, no sub-resource
	}
	sub := rest[slash+1:] // path after "{slug}/"

	switch sub {
	case "logs":
		return method == http.MethodGet
	case "deploy", "restart", "stop":
		return method == http.MethodPost
	case "rollback":
		return method == http.MethodPost || method == http.MethodPut
	}
	if method == http.MethodPut && strings.HasPrefix(sub, "data/") && len(sub) > len("data/") {
		return true
	}
	if method == http.MethodGet && isScheduleRunLogsPath(sub) {
		return true
	}
	return false
}

// isScheduleRunLogsPath matches the sub-resource
// "schedules/{id}/runs/{run_id}/logs" with non-empty id and run_id.
func isScheduleRunLogsPath(sub string) bool {
	seg := strings.Split(sub, "/")
	return len(seg) == 5 &&
		seg[0] == "schedules" && seg[1] != "" &&
		seg[2] == "runs" && seg[3] != "" &&
		seg[4] == "logs"
}
